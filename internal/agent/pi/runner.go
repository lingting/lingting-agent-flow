// Package pi 实现基于 Pi RPC 的统一 Agent Runner。
//
// 一次 Run 对应一个全新的 Pi 子进程：启动后先查询会话状态，再提交提示词，
// 持续消费 stdout 的 JSONL 记录并映射为内部事件，直到收到 agent_settled。
// 进程的终止与回收统一交给 internal/process。
package pi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/process"
)

// 默认超时与容量参数。
const (
	// DefaultGraceTimeout 是关闭与取消时等待进程退出的宽限时间。
	DefaultGraceTimeout = 5 * time.Second
	// DefaultResponseTimeout 是等待单条命令响应的默认超时时间。
	DefaultResponseTimeout = 30 * time.Second
	// DefaultRecordBuffer 是待处理记录的缓冲数量。
	DefaultRecordBuffer = 128
)

// Options 是 Pi Runner 的可选配置。
type Options struct {
	// Executable 是 Pi 可执行文件路径或 PATH 中的名称，为空时使用 pi。
	Executable string
	// Env 是子进程环境变量，nil 表示继承当前进程环境。
	Env []string
	// GraceTimeout 是关闭与取消时的宽限时间，零值使用 DefaultGraceTimeout。
	GraceTimeout time.Duration
	// ResponseTimeout 是等待命令响应的超时时间，零值使用 DefaultResponseTimeout。
	ResponseTimeout time.Duration
}

// Runner 是 Pi 的 Agent Runner 实现。
type Runner struct {
	options Options
}

// NewRunner 构造 Pi Runner。
func NewRunner(options Options) *Runner {
	return &Runner{options: options}
}

// runState 保存一次执行的进程与事件状态。
type runState struct {
	request    agent.RunRequest
	supervisor *process.Supervisor
	decoder    *decoder
	translator *translator
	startedAt  time.Time
	grace      time.Duration
}

// Run 执行一次完整的 Agent 调用。
//
// 返回的 RunResult 始终描述本次执行的实际结果；error 只在执行未能正常完成时非空，
// 便于调用方区分「执行完成」与「执行失败或取消」。
func (r *Runner) Run(
	ctx context.Context,
	request agent.RunRequest,
	emit func(agent.Event),
) (agent.RunResult, error) {
	startedAt := time.Now()
	result := agent.RunResult{SessionID: request.SessionID, ExitCode: -1, StartedAt: startedAt}
	if err := request.Validate(); err != nil {
		result.Status = agent.RunFailed
		result.FinishedAt = time.Now()
		return result, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		result.Status = agent.RunCancelled
		result.FinishedAt = time.Now()
		emitCancelled(result, emit, err)
		return result, err
	}

	options := r.resolveOptions()
	executable, err := resolveExecutable(options.Executable)
	if err != nil {
		result.Status = agent.RunFailed
		result.FinishedAt = time.Now()
		return result, err
	}
	supervisor, err := process.Start(processCommand(processOptions{
		Executable: executable,
		Model:      request.Model,
		WorkingDir: request.WorkingDir,
		Env:        options.Env,
	}), process.Options{GraceTimeout: options.GraceTimeout})
	if err != nil {
		result.Status = agent.RunFailed
		result.FinishedAt = time.Now()
		return result, err
	}

	state := &runState{
		request:    request,
		supervisor: supervisor,
		decoder:    &decoder{},
		translator: newTranslator(request.SessionID, emit),
		startedAt:  startedAt,
		grace:      options.GraceTimeout,
	}
	// 所有结束路径都保证子进程被终止并回收。
	defer func() {
		_, _ = supervisor.Shutdown(state.grace)
	}()

	emitStarted(state, supervisor.PID())
	result = r.execute(ctx, state, options, result)
	result.FinishedAt = time.Now()
	return result, runError(result)
}

// execute 完成握手、提交提示词并消费事件流。
func (r *Runner) execute(
	ctx context.Context,
	state *runState,
	options Options,
	result agent.RunResult,
) agent.RunResult {
	pump := newRecordPump(state.supervisor.Lines(), state.decoder, state.translator, DefaultRecordBuffer)
	defer pump.stop()

	sessionRef, err := r.requestState(ctx, state, pump, options.ResponseTimeout)
	if err != nil {
		// 先停止消费协程并让它排空剩余输出，避免关闭进程时管道背压阻塞。
		pump.stop()
		return r.failWith(result, state, err)
	}
	result.ExternalSessionID = sessionRef.SessionID

	disposition, err := r.sendPrompt(ctx, state, pump, options.ResponseTimeout)
	if err != nil {
		pump.stop()
		return r.failWith(result, state, err)
	}
	if disposition == dispositionHandled {
		// 提示词被扩展命令消费，不会启动 Agent 运行，因此不等待 agent_settled。
		result.Status = agent.RunCompleted
		return result
	}

	settled := pump.waitSettled(ctx)
	pump.stop()
	return r.settle(state, result, settled)
}

// settle 处理事件消费阶段的结束结果与进程关闭。
func (r *Runner) settle(
	state *runState,
	result agent.RunResult,
	outcome outcome,
) agent.RunResult {
	switch outcome.kind {
	case outcomeCancelled:
		r.requestAbort(state)
		state.translator.emitEvent(agent.EventRunCancelled, agent.RunCancelledData{
			Reason: "执行被调用方取消",
		}, time.Now())
		result.Status = agent.RunCancelled
		result.ExitCode = r.shutdown(state).Code
		return result
	case outcomeProtocolFailed, outcomeOutputFailed:
		state.translator.emitEvent(agent.EventRunFailed, agent.RunFailedData{
			Reason:  outcome.kind.String(),
			Message: errorText(outcome.err),
		}, time.Now())
		result.Status = agent.RunFailed
		result.ExitCode = r.shutdown(state).Code
		result.Failure = errorText(outcome.err)
		return result
	}

	exit := r.shutdown(state)
	result.ExitCode = exit.Code
	result.FinalText = agent.TruncateText(state.translator.finalText.String(), agent.MaxFinalTextLength)

	if state.translator.aborted {
		state.translator.emitEvent(agent.EventRunCancelled, agent.RunCancelledData{
			Reason: "agent_settled 声明运行被中止",
		}, time.Now())
		result.Status = agent.RunCancelled
		return result
	}
	if reason := state.translator.failureReason(); reason != "" {
		state.translator.emitEvent(agent.EventRunFailed, agent.RunFailedData{
			Reason:   "run_failed",
			Message:  reason,
			ExitCode: exit.Code,
		}, time.Now())
		result.Status = agent.RunFailed
		result.Failure = reason
		return result
	}
	result.Status = agent.RunCompleted
	return result
}

// requestState 发送 get_state 并返回 Pi 侧的会话状态。
func (r *Runner) requestState(
	ctx context.Context,
	state *runState,
	pump *recordPump,
	timeout time.Duration,
) (stateData, error) {
	id := "agentflow-state-" + state.request.SessionID
	response, err := r.call(ctx, state, pump, timeout, command{ID: id, Type: commandGetState})
	if err != nil {
		return stateData{}, err
	}
	var data stateData
	if err := response.decodeData(&data); err != nil {
		return stateData{}, err
	}
	return data, nil
}

// sendPrompt 发送提示词并返回 Pi 报告的 disposition。
//
// 记录消费协程在发送前已经就绪，避免错过快速到达的完成事件。
func (r *Runner) sendPrompt(
	ctx context.Context,
	state *runState,
	pump *recordPump,
	timeout time.Duration,
) (string, error) {
	id := "agentflow-prompt-" + state.request.SessionID
	response, err := r.call(ctx, state, pump, timeout, command{
		ID:      id,
		Type:    commandPrompt,
		Message: state.request.Prompt,
	})
	if err != nil {
		return "", err
	}
	var data promptData
	if err := response.decodeData(&data); err != nil {
		return "", err
	}
	return data.Disposition, nil
}

// call 发送命令并等待其响应，期间的事件记录被映射为内部事件。
func (r *Runner) call(
	ctx context.Context,
	state *runState,
	pump *recordPump,
	timeout time.Duration,
	cmd command,
) (record, error) {
	payload, err := encodeCommand(cmd)
	if err != nil {
		return record{}, err
	}
	if err := state.supervisor.Write(payload); err != nil {
		return record{}, err
	}
	response, err := pump.awaitResponse(ctx, cmd.ID, timeout)
	if err != nil {
		// 等待响应失败时补上进程诊断输出，便于区分协议问题与 Pi 自身启动失败。
		if stderr := strings.TrimSpace(state.supervisor.Stderr()); stderr != "" {
			return record{}, fmt.Errorf("%w（stderr: %s）", err, agent.TruncateText(stderr, 512))
		}
		return record{}, err
	}
	if !response.Success {
		return record{}, &ProtocolError{
			Record: response.Index,
			Kind:   kindCommandFailed,
			Detail: fmt.Sprintf("%s 命令失败: %s", response.Command, snippet(response.Error)),
		}
	}
	return response, nil
}

// shutdown 请求进程正常退出并返回退出结果。
func (r *Runner) shutdown(state *runState) process.Exit {
	exit, _ := state.supervisor.Shutdown(state.grace)
	return exit
}

// requestAbort 在进程仍可通信时请求中止当前运行。
//
// 这里不等待 abort 的响应：取消路径必须最终落到进程回收，
// 由调用方的宽限时间决定是否强制终止。
func (r *Runner) requestAbort(state *runState) {
	payload, err := encodeCommand(command{Type: commandAbort})
	if err != nil {
		return
	}
	_ = state.supervisor.Write(payload)
}

// failWith 把执行标记为失败，回收进程并发出失败事件。
func (r *Runner) failWith(result agent.RunResult, state *runState, err error) agent.RunResult {
	exit := r.shutdown(state)
	state.translator.emitEvent(agent.EventRunFailed, agent.RunFailedData{
		Reason:   "run_failed",
		Message:  errorText(err),
		ExitCode: exit.Code,
	}, time.Now())
	result.Status = agent.RunFailed
	result.ExitCode = exit.Code
	result.Failure = errorText(err)
	return result
}

// emitStarted 发出 run.started 事件。
func emitStarted(state *runState, pid int) {
	state.translator.emitEvent(agent.EventRunStarted, agent.RunStartedData{
		Model:      state.request.Model,
		WorkingDir: state.request.WorkingDir,
		PID:        pid,
	}, state.startedAt)
}

// emitCancelled 在启动前取消时发出 run.cancelled 事件。
func emitCancelled(result agent.RunResult, emit func(agent.Event), err error) {
	if emit == nil {
		return
	}
	event, buildErr := agent.NewEvent(result.SessionID, agent.EventRunCancelled, agent.RunCancelledData{
		Reason: errorText(err),
	}, result.FinishedAt)
	if buildErr != nil {
		return
	}
	emit(event)
}

// resolveOptions 补齐默认配置。
func (r *Runner) resolveOptions() Options {
	options := r.options
	if options.GraceTimeout <= 0 {
		options.GraceTimeout = DefaultGraceTimeout
	}
	if options.ResponseTimeout <= 0 {
		options.ResponseTimeout = DefaultResponseTimeout
	}
	return options
}

// runError 把执行结果转换为错误，便于调用方区分成功与失败。
func runError(result agent.RunResult) error {
	switch result.Status {
	case agent.RunCancelled:
		return fmt.Errorf("pi: 执行被取消: %w", context.Canceled)
	case agent.RunFailed:
		if result.Failure != "" {
			return fmt.Errorf("pi: 执行失败（退出码 %d）: %s", result.ExitCode, result.Failure)
		}
		return fmt.Errorf("pi: 执行失败（退出码 %d）", result.ExitCode)
	default:
		return nil
	}
}

// errorText 返回错误的可读文本。
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}
