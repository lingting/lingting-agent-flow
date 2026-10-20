package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/domain"
)

// 统一 Runner 的稳定错误。
var (
	// ErrAgentRunnerStopped 表示服务已停止，在途调用被取消。
	//
	// 它与调用方主动取消区分开：两者都会终止进程，但诊断信息必须说明真实的
	// 触发来源，否则 Server 关闭会被误读为"用户取消了任务"。
	ErrAgentRunnerStopped = errors.New("app: Agent Runner 已停止")
)

// AgentRunRequest 是一次由业务层发起的 Agent 调用。
//
// 该结构只描述"执行什么"，不包含任何进程或文件路径：Session 目录、调用标识与
// 会话标识都由服务端生成，调用方不能指定任意本地路径。
type AgentRunRequest struct {
	// Scope 是会话的任务、步骤与尝试归属。
	Scope domain.SessionScope
	// Role 是本次调用的角色，决定模型与系统提示词。
	Role domain.Role
	// Prompt 是完整提示词。
	Prompt string
	// WorkingDir 是本机执行时的工作目录；容器执行时为空。
	//
	// 传空表示使用服务进程当前目录：这是显式选择，不是隐式回退到某个默认任务目录。
	WorkingDir string
	// Container 是容器执行目标，为空表示在本机执行。
	Container *agent.ContainerTarget
	// Timeout 是本次调用的超时时间，零值使用执行器默认值。
	Timeout time.Duration
	// PiOptions 覆盖 Pi Runner 的默认配置。
	PiOptions pi.Options
}

// AgentRunOutcome 是一次 Agent 调用的结果。
type AgentRunOutcome struct {
	// Session 是已持久化的会话记录，执行失败时同样存在。
	Session domain.Session
	// Result 是执行器返回的原始结果。
	Result agent.RunResult
}

// AgentRunnerService 是业务层调用 Agent 的唯一入口。
//
// 职责边界：
//   - 生成会话标识与调用标识、创建 Session 记录、调用 Runner、写入产出与归档；
//   - 把在途调用纳入统一生命周期管理，Server 停止时取消并等待收敛；
//   - 不决定任务是否成功、不重试、不创建或销毁容器。
//
// 它实现了 lifecycle.Service，因此可以直接注册进 agentflow server，由同一处代码
// 负责启动与停止，不在 HTTP Handler 内创建不受管理的长期 goroutine。
type AgentRunnerService struct {
	sessions *SessionService
	logger   *slog.Logger
	// defaults 是本机执行时使用的默认 Pi 配置。
	defaults pi.Options

	mu      sync.Mutex
	started bool
	stopped bool
	// stopSignal 在服务停止时关闭，用于取消在途调用。
	stopSignal context.Context
	// stopOnce 保证停止信号只关闭一次。
	stopOnce context.CancelFunc
	runs     sync.WaitGroup
	// active 统计在途调用数量，用于停止时判断是否全部收敛。
	active int
}

// AgentRunnerOptions 是构造统一 Runner 服务的输入。
type AgentRunnerOptions struct {
	// Sessions 是会话服务，必填。
	Sessions *SessionService
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Pi 是本机执行的默认 Pi 配置。
	Pi pi.Options
}

// NewAgentRunnerService 构造统一 Runner 服务。
func NewAgentRunnerService(options AgentRunnerOptions) (*AgentRunnerService, error) {
	if options.Sessions == nil {
		return nil, fmt.Errorf("Agent Runner 初始化失败: 会话服务不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentRunnerService{
		sessions: options.Sessions,
		logger:   logger,
		defaults: options.Pi,
	}, nil
}

// Name 返回后台服务名称。
func (s *AgentRunnerService) Name() string {
	return "agent_runner"
}

// Start 标记服务可接受调用。
//
// 该方法不启动任何长期 goroutine：Runner 是请求驱动的，长期运行状态只有"是否
// 允许发起调用"与"在途调用集合"。
func (s *AgentRunnerService) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("Agent Runner 已经启动")
	}
	s.started = true
	// 分离自父上下文：Server 关闭信号通过 Stop 显式传播，而不是依赖 HTTP 请求上下文。
	stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.stopSignal = stopSignal
	s.stopOnce = stop
	return nil
}

// Stop 取消在途调用并等待其收敛。
//
// 停止后新的调用直接返回错误；已经在执行的调用会收到取消信号，Runner 负责结束
// 子进程、保存已有的 Session 文件，并把会话记录推进到 cancelled。
func (s *AgentRunnerService) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	stop := s.stopOnce
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	done := make(chan struct{})
	go func() {
		s.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("Agent Runner 停止被取消: %w", ctx.Err())
	}
}

// Active 返回当前在途调用数量。
func (s *AgentRunnerService) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Run 执行一次 Agent 调用并持久化会话。
//
// 顺序固定为：校验请求 → 创建会话记录并置为 running → 启动进程 → 收集结果 →
// 写入产出与归档。任一步失败都不会残留 running 状态。
func (s *AgentRunnerService) Run(
	ctx context.Context,
	request AgentRunRequest,
) (AgentRunOutcome, error) {
	if err := s.begin(ctx); err != nil {
		return AgentRunOutcome{}, err
	}
	defer s.finish()

	if strings.TrimSpace(request.Role.Model) == "" {
		return AgentRunOutcome{}, fmt.Errorf("Agent 调用失败: 角色 %s 未配置模型", request.Role.ID)
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return AgentRunOutcome{}, fmt.Errorf("Agent 调用失败: 提示词不能为空")
	}
	invocationID := agent.NewInvocationID()
	session, err := s.sessions.Start(ctx, StartRequest{
		Scope:        request.Scope,
		Role:         request.Role,
		InvocationID: invocationID,
		ContainerID:  containerIDOf(request.Container),
		DockerID:     dockerIDOf(request.Container),
	})
	if err != nil {
		return AgentRunOutcome{}, fmt.Errorf("Agent 调用失败: %w", err)
	}

	runCtx, cancel := s.runContext(ctx)
	defer cancel()
	runRequest := agent.RunRequest{
		SessionID:    session.ID.String(),
		TaskID:       request.Scope.TaskID.String(),
		StepID:       optionalID(request.Scope.StepID),
		AttemptID:    optionalID(request.Scope.AttemptID),
		RoleType:     request.Role.Type.String(),
		Model:        request.Role.Model,
		SystemPrompt: request.Role.SystemPrompt,
		Prompt:       request.Prompt,
		WorkingDir:   strings.TrimSpace(request.WorkingDir),
		SessionRoot:  s.sessions.LocalRoot(),
		InvocationID: invocationID,
		Container:    request.Container,
		Timeout:      request.Timeout,
	}
	runner, err := s.runnerFor(request)
	if err != nil {
		// Runner 构造失败同样要收敛会话记录，不能残留 running。
		_, _ = s.sessions.Fail(context.WithoutCancel(ctx), session.ID, time.Now(), err.Error())
		return AgentRunOutcome{}, fmt.Errorf("Agent 调用失败: %w", err)
	}
	result, runErr := runner.Run(runCtx, runRequest, func(event agent.Event) {
		s.logEvent(ctx, event)
	})
	// 会话收尾必须使用未取消的上下文：取消或超时恰恰是最需要写入结果的场景。
	finished, finishErr := s.sessions.Finish(context.WithoutCancel(ctx), FinishRequest{
		SessionID: session.ID,
		Result:    result,
		Archive:   archiveOf(result),
	})
	if finishErr != nil {
		return AgentRunOutcome{Result: result}, fmt.Errorf("Agent 调用失败: %w", finishErr)
	}
	outcome := AgentRunOutcome{Session: finished, Result: result}
	if runErr != nil {
		return outcome, runErr
	}
	return outcome, nil
}

// begin 登记一次在途调用，服务已停止时拒绝。
func (s *AgentRunnerService) begin(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return fmt.Errorf("Agent Runner 已停止，不再接受新的调用")
	}
	if !s.started {
		return fmt.Errorf("Agent Runner 尚未启动")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("Agent 调用失败: %w", err)
	}
	s.active++
	s.runs.Add(1)
	return nil
}

// finish 结束一次在途调用的登记。
func (s *AgentRunnerService) finish() {
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	s.runs.Done()
}

// runContext 把调用上下文与服务停止信号合并。
//
// 两个来源都必须能终止执行：调用方取消（HTTP 请求结束、用户撤销）与服务停止
// （agentflow server 退出）不能互相掩盖。实现方式是在后台监听停止信号并在触发时
// 取消本次调用的上下文，因此 Runner 看到的永远是同一个 ctx。
func (s *AgentRunnerService) runContext(ctx context.Context) (context.Context, context.CancelFunc) {
	s.mu.Lock()
	stop := s.stopSignal
	s.mu.Unlock()
	runCtx, cancelRun := context.WithCancelCause(ctx)
	if stop == nil {
		return runCtx, func() { cancelRun(nil) }
	}
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-stop.Done():
			cancelRun(ErrAgentRunnerStopped)
		case <-runCtx.Done():
		}
	}()
	return runCtx, func() {
		cancelRun(nil)
		<-watcherDone
	}
}

// runnerFor 构造本次调用使用的 Runner。
//
// 本机与容器执行走不同的实现，但都经过同一个 agent.Runner 接口：业务层不需要
// 区分两种运行位置，也不会因为容器执行失败而静默回退到本机。
func (s *AgentRunnerService) runnerFor(request AgentRunRequest) (agent.Runner, error) {
	if request.Container != nil {
		return nil, fmt.Errorf("容器执行必须由容器服务提供执行器，不能在本机 Runner 内直接运行")
	}
	options := request.PiOptions
	options = mergePiOptions(s.defaults, options)
	return pi.NewRunner(options), nil
}

// mergePiOptions 用默认配置补齐请求配置中的空字段。
//
// 只覆盖空值：请求显式给出的配置优先，避免服务默认值悄悄覆盖调用方的选择。
func mergePiOptions(base, override pi.Options) pi.Options {
	merged := override
	if strings.TrimSpace(merged.Executable) == "" {
		merged.Executable = base.Executable
	}
	if len(merged.PrefixArgs) == 0 {
		merged.PrefixArgs = base.PrefixArgs
	}
	if merged.Env == nil {
		merged.Env = base.Env
	}
	if merged.Timeout <= 0 {
		merged.Timeout = base.Timeout
	}
	if merged.ExportTimeout <= 0 {
		merged.ExportTimeout = base.ExportTimeout
	}
	if merged.GraceTimeout <= 0 {
		merged.GraceTimeout = base.GraceTimeout
	}
	if merged.OutputLimit <= 0 {
		merged.OutputLimit = base.OutputLimit
	}
	if merged.StderrLimit <= 0 {
		merged.StderrLimit = base.StderrLimit
	}
	if merged.LogLimit <= 0 {
		merged.LogLimit = base.LogLimit
	}
	return merged
}

// logEvent 把内部事件写入日志，只记录类型与摘要。
func (s *AgentRunnerService) logEvent(ctx context.Context, event agent.Event) {
	s.logger.Log(
		ctx,
		agentEventLogLevel(event.Type),
		"Agent 事件",
		slog.String("session_id", event.SessionID),
		slog.String("event_type", string(event.Type)),
		slog.String("data", summarizeEventData(event.Data)),
	)
}

// containerIDOf 返回容器登记记录 ID，本机执行时为空。
func containerIDOf(target *agent.ContainerTarget) string {
	if target == nil {
		return ""
	}
	return strings.TrimSpace(target.ContainerID)
}

// dockerIDOf 返回 Docker 容器 ID，本机执行时为空。
func dockerIDOf(target *agent.ContainerTarget) string {
	if target == nil {
		return ""
	}
	return strings.TrimSpace(target.DockerID)
}

// optionalID 把可空标识转换为字符串，未设置时为空。
func optionalID(id *domain.ID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
