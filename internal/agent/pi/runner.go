package pi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/process"
)

// Run 执行一次完整的 Agent 调用。
//
// 返回的 RunResult 始终描述本次执行的实际结果；error 只在执行未能正常完成时非空，
// 便于调用方区分「执行完成」与「执行失败、取消或超时」。
//
// 无论进程如何结束，都会尽力保存已有的 Session 文件：异常退出时保留的内容仍然
// 是排查问题的唯一线索。
func (r *Runner) Run(
	ctx context.Context,
	request agent.RunRequest,
	emit func(agent.Event),
) (agent.RunResult, error) {
	startedAt := time.Now()
	result := agent.RunResult{
		SessionID:    request.SessionID,
		InvocationID: request.InvocationID,
		Model:        request.Model,
		ExitCode:     -1,
		StartedAt:    startedAt,
		Session: agent.SessionFiles{
			Completeness: agent.SessionMissing,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	if request.Container != nil {
		// 本机 Runner 明确拒绝容器目标：容器执行必须走 ContainerRunner，
		// 不能因为远程执行失败而静默回退到本机运行。
		err := fmt.Errorf("pi: 本机 Runner 不支持容器目标，请使用容器 Runner")
		result.Status = agent.RunFailed
		result.Failure = err.Error()
		result.FinishedAt = time.Now()
		emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
			Reason:  "container_target_unsupported",
			Message: err.Error(),
		})
		return result, err
	}
	if err := request.Validate(); err != nil {
		result.Status = agent.RunFailed
		result.Failure = err.Error()
		result.FinishedAt = time.Now()
		return result, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		result.Status = agent.RunCancelled
		result.FinishedAt = time.Now()
		emitEvent(result.SessionID, emit, agent.EventRunCancelled, agent.RunCancelledData{
			Reason: "调用前已取消",
		})
		return result, err
	}

	options := r.resolveOptions()
	if _, err := resolveExecutable(options.Executable); err != nil {
		result.Status = agent.RunFailed
		result.Failure = err.Error()
		result.FinishedAt = time.Now()
		emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
			Reason:  "executable_missing",
			Message: err.Error(),
		})
		return result, err
	}
	paths, err := r.sessionPaths(request)
	if err != nil {
		result.Status = agent.RunFailed
		result.Failure = err.Error()
		result.FinishedAt = time.Now()
		emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
			Reason:  "session_path_invalid",
			Message: err.Error(),
		})
		return result, err
	}
	if err := prepareSessionDir(paths); err != nil {
		result.Status = agent.RunFailed
		result.Failure = err.Error()
		result.FinishedAt = time.Now()
		emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
			Reason:  "session_dir_failed",
			Message: err.Error(),
		})
		return result, err
	}

	timeout := options.Timeout
	if request.Timeout > 0 {
		timeout = request.Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stderrLines := make(chan process.Line, 64)
	supervisor, err := process.Start(
		processCommand(processOptions{
			Executable:   options.Executable,
			PrefixArgs:   options.PrefixArgs,
			Model:        request.Model,
			SystemPrompt: request.SystemPrompt,
			SessionPath:  paths.JSONL,
			WorkingDir:   request.WorkingDir,
			Env:          options.Env,
		}),
		process.Options{
			GraceTimeout: options.GraceTimeout,
			StderrLimit:  options.StderrLimit,
			StderrStream: stderrLines,
		},
	)
	if err != nil {
		result.Status = agent.RunFailed
		result.Failure = err.Error()
		result.FinishedAt = time.Now()
		emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
			Reason:  "start_failed",
			Message: err.Error(),
		})
		return result, err
	}
	result.ProcessID = supervisor.PID()
	emitEvent(result.SessionID, emit, agent.EventRunStarted, agent.RunStartedData{
		Model:      request.Model,
		RoleType:   request.RoleType,
		WorkingDir: request.WorkingDir,
		SessionDir: paths.Dir,
		PID:        supervisor.PID(),
	})

	capture := newOutputCapture(options.OutputLimit, request.SessionID, emit)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		capture.consumeStdout(supervisor.Lines())
	}()
	go func() {
		defer readers.Done()
		capture.consumeStderr(stderrLines)
	}()

	promptErr := writePrompt(supervisor, request.Prompt)
	ended := waitOutcome(runCtx, supervisor, options.GraceTimeout, timeout)
	// 进程回收后管道随之关闭，读取协程会自然结束；这里只做有上限的等待，
	// 避免进程无法回收时永久阻塞。
	waitReaders(&readers, drainTimeout(options.GraceTimeout))

	// 进程已经回收（waitOutcome 覆盖了全部路径）；这里再加一层有上限的等待，
	// 保证极端情况下也不会让调用永久阻塞。
	exit := awaitExit(supervisor, drainTimeout(options.GraceTimeout))
	result.ExitCode = exit.Code
	result.FinalText = agent.TruncateText(capture.stdoutTail(), agent.MaxFinalTextLength)
	result.StderrSummary = agent.TruncateText(capture.stderrTail(), agent.MaxFinalTextLength)
	result.Status = ended.status()
	result.Failure = ended.reason(promptErr, exit, result.StderrSummary)
	if result.Status == agent.RunCompleted && result.Failure != "" {
		// 进程已退出但退出码非零或提示词写入失败：执行未正常完成。
		result.Status = agent.RunFailed
	}

	// Session 处理放在进程回收之后：Pi 在退出前写完 Session 文件，
	// 提前读取可能得到不完整内容。
	result.Session = r.persistSession(runCtx, options, request, paths, result.Status, emit)
	if result.Session.Completeness == agent.SessionMissing && result.Status == agent.RunCompleted {
		// 进程正常结束却没有 Session 文件，属于提取失败，不能报告为成功。
		result.Status = agent.RunFailed
		if result.Failure == "" {
			result.Failure = "未找到 Session 文件: " + paths.JSONL
		}
	}
	// 终态事件放在 Session 处理之后，保证事件反映最终结果。
	emitOutcome(result, emit, ended, promptErr)
	result.FinishedAt = time.Now()
	return result, runError(result)
}

// outcomeKind 描述进程结束的原因。
type outcomeKind int

// 进程结束原因。
const (
	outcomeExited outcomeKind = iota
	outcomeCancelled
	outcomeTimedOut
)

// outcome 是等待进程结束的结果。
type outcome struct {
	kind outcomeKind
	// limit 是本次调用生效的超时时间，用于生成可诊断的说明。
	limit time.Duration
}

// status 返回该结束原因对应的运行状态。
func (o outcome) status() agent.RunStatus {
	switch o.kind {
	case outcomeCancelled:
		return agent.RunCancelled
	case outcomeTimedOut:
		return agent.RunTimedOut
	default:
		return agent.RunCompleted
	}
}

// reason 返回失败或异常的可诊断说明。
//
// 正常退出（退出码为 0 且提示词写入成功）时返回空字符串；其余情况都给出原因，
// 避免把非零退出码当成成功。
func (o outcome) reason(promptErr error, exit process.Exit, stderr string) string {
	switch o.kind {
	case outcomeCancelled:
		return "执行被调用方取消"
	case outcomeTimedOut:
		return fmt.Sprintf("执行超时（上限 %s）", o.limit)
	}
	if promptErr != nil {
		return promptErr.Error()
	}
	if exit.Code != 0 {
		detail := agent.TruncateText(strings.TrimSpace(stderr), maxFailureDetail)
		switch {
		case detail != "":
			return fmt.Sprintf("Pi 异常退出（退出码 %d）: %s", exit.Code, detail)
		case exit.Err != nil:
			return fmt.Sprintf("Pi 异常退出（退出码 %d）: %v", exit.Code, exit.Err)
		default:
			return fmt.Sprintf("Pi 异常退出（退出码 %d）", exit.Code)
		}
	}
	return ""
}

// waitOutcome 等待进程结束、调用取消或超时，并保证进程被回收。
func waitOutcome(
	ctx context.Context,
	supervisor *process.Supervisor,
	grace time.Duration,
	limit time.Duration,
) outcome {
	select {
	case <-supervisor.Done():
		return outcome{kind: outcomeExited}
	case <-ctx.Done():
	}
	_, _ = supervisor.Shutdown(grace)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return outcome{kind: outcomeTimedOut, limit: limit}
	}
	return outcome{kind: outcomeCancelled}
}

// awaitExit 在有上限的时间内等待进程回收结果。
//
// 进程无法回收时返回未知退出码，调用方仍能拿到 Session 与诊断信息。
func awaitExit(supervisor *process.Supervisor, timeout time.Duration) process.Exit {
	exited := make(chan process.Exit, 1)
	go func() {
		exited <- supervisor.Wait()
	}()
	select {
	case exit := <-exited:
		return exit
	case <-time.After(timeout):
		return process.Exit{Code: -1}
	}
}

// waitReaders 在有上限的时间内等待输出读取协程退出。
//
// 进程无法回收时输出管道可能永远不会关闭；此时放弃等待，避免调用永久阻塞。
// 放弃等待只影响尾部输出摘要，不影响进程回收与 Session 处理。
func waitReaders(readers *sync.WaitGroup, timeout time.Duration) {
	finished := make(chan struct{})
	go func() {
		readers.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(timeout):
	}
}

// persistSession 校验 Session 文件并尝试导出 HTML。
//
// 原始 JSONL 已经是本次调用的持久化位置，因此不需要额外复制；
// 这里只校验内容、记录大小，并在导出失败时保留原始文件。
func (r *Runner) persistSession(
	ctx context.Context,
	options Options,
	request agent.RunRequest,
	paths SessionPaths,
	status agent.RunStatus,
	emit func(agent.Event),
) agent.SessionFiles {
	files := agent.SessionFiles{
		Completeness: agent.SessionMissing,
		ExportStatus: agent.ExportNotAttempted,
	}
	info, err := os.Stat(paths.JSONL)
	if err != nil {
		files.Error = fmt.Sprintf("未找到 Session 文件: %s", paths.JSONL)
		if !os.IsNotExist(err) {
			files.Error = fmt.Sprintf("读取 Session 文件失败: %v", err)
		}
		return files
	}
	if info.IsDir() {
		files.Error = fmt.Sprintf("Session 路径是目录而非文件: %s", paths.JSONL)
		return files
	}
	if err := validateSessionFile(paths.JSONL); err != nil {
		// 文件存在但内容不可读或不完整时仍然保留，供人工排查。
		files.Completeness = agent.SessionPartial
		files.JSONLPath = paths.JSONL
		files.Bytes = info.Size()
		files.Error = err.Error()
		emitEvent(request.SessionID, emit,
			agent.EventSessionSaved, agent.SessionSavedData{
				Path:         paths.JSONL,
				Bytes:        files.Bytes,
				Completeness: files.Completeness,
			})
		return files
	}

	files.JSONLPath = paths.JSONL
	files.Bytes = info.Size()
	if status == agent.RunCompleted {
		files.Completeness = agent.SessionComplete
	} else {
		files.Completeness = agent.SessionPartial
	}
	emitEvent(request.SessionID, emit,
		agent.EventSessionSaved, agent.SessionSavedData{
			Path:         paths.JSONL,
			Bytes:        files.Bytes,
			Completeness: files.Completeness,
		})

	if options.DisableExport {
		return files
	}
	if ctx.Err() != nil {
		// 调用已取消或超时：不再启动导出进程，避免为异常调用额外消耗时间。
		return files
	}
	exportErr := r.exportHTML(ctx, options, paths)
	if exportErr != nil {
		files.ExportStatus = agent.ExportFailed
		files.Error = exportErr.Error()
		// 导出失败不影响原始 JSONL；只有正常结束的调用才把完整性降级，
		// 异常调用本来就是 partial，不需要再降级。
		if files.Completeness == agent.SessionComplete {
			files.Completeness = agent.SessionExportFailed
		}
		emitEvent(request.SessionID, emit,
			agent.EventSessionExported, agent.SessionExportedData{
				Status: files.ExportStatus,
				Error:  exportErr.Error(),
			})
		return files
	}
	files.HTMLPath = paths.HTML
	files.ExportStatus = agent.ExportSucceeded
	emitEvent(request.SessionID, emit,
		agent.EventSessionExported, agent.SessionExportedData{
			Path:   paths.HTML,
			Status: files.ExportStatus,
		})
	return files
}

// exportHTML 调用 Pi 官方导出能力把 Session 文件转换为 HTML。
//
// 导出只读取原始 JSONL，不会启动新的交互式会话；导出失败不能导致原始文件丢失。
func (r *Runner) exportHTML(ctx context.Context, options Options, paths SessionPaths) error {
	if err := ensureParent(paths.HTML); err != nil {
		return err
	}
	exportCtx, cancel := context.WithTimeout(ctx, options.ExportTimeout)
	defer cancel()
	stdout, exit, err := runOnce(
		exportCtx,
		options,
		exportArguments(paths.JSONL, paths.HTML),
		options.ExportTimeout,
	)
	if err != nil {
		return fmt.Errorf("导出 HTML 失败: %w", err)
	}
	if exit.Code != 0 {
		return fmt.Errorf(
			"导出 HTML 失败（退出码 %d）: %s",
			exit.Code,
			agent.TruncateText(strings.TrimSpace(stdout), 512),
		)
	}
	if _, statErr := os.Stat(paths.HTML); statErr != nil {
		return fmt.Errorf("导出 HTML 后未找到文件: %s: %w", paths.HTML, statErr)
	}
	return nil
}

// emitOutcome 根据进程结束原因发出终态事件。
func emitOutcome(
	result agent.RunResult,
	emit func(agent.Event),
	ended outcome,
	promptErr error,
) {
	switch ended.kind {
	case outcomeCancelled:
		emitEvent(result.SessionID, emit, agent.EventRunCancelled, agent.RunCancelledData{
			Reason: "执行被调用方取消",
		})
		return
	case outcomeTimedOut:
		emitEvent(result.SessionID, emit, agent.EventRunTimedOut, agent.RunTimedOutData{
			Timeout:  ended.limit.String(),
			ExitCode: result.ExitCode,
		})
		return
	}
	if promptErr != nil {
		emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
			Reason:   "prompt_failed",
			Message:  promptErr.Error(),
			ExitCode: result.ExitCode,
		})
		return
	}
	if result.ExitCode != 0 {
		emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
			Reason:   "exit_nonzero",
			Message:  agent.TruncateText(result.StderrSummary, 512),
			ExitCode: result.ExitCode,
		})
		return
	}
	if result.Status == agent.RunFailed {
		// 进程正常退出但 Session 提取失败：同样不能报告为收敛。
		emitEvent(result.SessionID, emit, agent.EventRunFailed, agent.RunFailedData{
			Reason:   "session_unusable",
			Message:  result.Failure,
			ExitCode: result.ExitCode,
		})
		return
	}
	emitEvent(result.SessionID, emit, agent.EventRunSettled, agent.RunSettledData{ExitCode: result.ExitCode})
}

// emitEvent 编码并发出内部事件，回调为空或编码失败时忽略。
func emitEvent(
	sessionID string,
	emit func(agent.Event),
	eventType agent.EventType,
	payload any,
) {
	if emit == nil {
		return
	}
	event, err := agent.NewEvent(sessionID, eventType, payload, time.Now())
	if err != nil {
		return
	}
	emit(event)
}

// runError 把执行结果转换为错误，便于调用方区分成功与失败。
func runError(result agent.RunResult) error {
	switch result.Status {
	case agent.RunCancelled:
		return fmt.Errorf("pi: 执行被取消: %w", context.Canceled)
	case agent.RunTimedOut:
		return fmt.Errorf("pi: 执行超时: %w", context.DeadlineExceeded)
	case agent.RunFailed:
		if result.Failure != "" {
			return fmt.Errorf("pi: 执行失败（退出码 %d）: %s", result.ExitCode, result.Failure)
		}
		return fmt.Errorf("pi: 执行失败（退出码 %d）", result.ExitCode)
	default:
		// 导出失败只是降级结果：原始 Session 已保存，不作为执行失败返回。
		return nil
	}
}

// sessionPaths 解析本次调用的 Session 文件路径。
func (r *Runner) sessionPaths(request agent.RunRequest) (SessionPaths, error) {
	return SessionLayout{
		TaskID:       request.TaskID,
		StepID:       request.StepID,
		AttemptID:    request.AttemptID,
		SessionID:    request.SessionID,
		InvocationID: request.InvocationID,
	}.Paths(request.SessionRoot)
}
