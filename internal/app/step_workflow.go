package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 单步骤开发—验证闭环的默认参数。
const (
	// defaultStepWorkflowValidationTimeout 是未指定验证超时时的整次验证超时。
	//
	// 它与开发阶段的调用超时是两个独立概念：开发超时约束一次 Agent 调用，验证超时
	// 约束全部检查的总时长。
	defaultStepWorkflowValidationTimeout = 30 * time.Minute
)

// StepWorkflowStore 是闭环协调器需要的持久化能力。
//
// 它只读取归属链与前置步骤，并写入收口状态、反馈与事件：开发与验证各自的执行记录
// 由被调用的服务写入，协调器不重复实现它们。
type StepWorkflowStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	GetStep(ctx context.Context, id domain.ID) (*domain.Step, error)
	ListStepsByTask(ctx context.Context, taskID domain.ID) ([]domain.Step, error)
	GetAttempt(ctx context.Context, id domain.ID) (*domain.Attempt, error)
	UpdateStepIfStatus(
		ctx context.Context,
		step domain.Step,
		expected []domain.StepStatus,
	) error
	UpdateTaskIfStatus(
		ctx context.Context,
		task domain.Task,
		expected []domain.TaskStatus,
	) error
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
	// LatestValidationFailureFeedbackByStep 返回步骤最近一次的验证失败反馈。
	//
	// 没有反馈时返回 storage.ErrNotFound，调用方据此跳过提示词中的历史问题小节。
	LatestValidationFailureFeedbackByStep(
		ctx context.Context,
		stepID domain.ID,
	) (*domain.ValidationFailureFeedback, error)
	// CreateValidationFailureFeedback 追加一条验证失败反馈。
	CreateValidationFailureFeedback(
		ctx context.Context,
		feedback domain.ValidationFailureFeedback,
	) error
	// GetValidationFailureFeedback 读取某轮验证的失败反馈。
	//
	// 修复循环按上一轮的 ValidationRunID 精确读取依据，避免误用其他步骤或任务的
	// 反馈；没有反馈时返回 storage.ErrNotFound。
	GetValidationFailureFeedback(
		ctx context.Context,
		runID domain.ID,
	) (*domain.ValidationFailureFeedback, error)
}

// ExecuteAndValidateRequest 是通过接口发起一次"开发 + 验证"闭环的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方只能指定任务、步骤与可选的执行参数，
// 不能指定 Session 路径、工作目录、命令行参数或角色覆盖。
type ExecuteAndValidateRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// StepID 是待执行的步骤标识。
	StepID string
	// Prompt 是可选的补充提示词，会追加在编排器生成的提示词之后。
	Prompt string
	// Timeout 是开发阶段单次 Agent 调用的超时，零值使用任务配置或默认值。
	Timeout time.Duration
	// ValidationTimeout 是验证阶段的总超时，零值使用服务默认值。
	ValidationTimeout time.Duration
}

// ExecuteAndValidateResult 是一次开发—验证闭环的结果。
//
// 它显式区分四件事，任何一项都不能替代其他项：
//   - 开发阶段是否正常结束（Outcome）；
//   - 验证阶段是否真的发起过（ValidationRan）；
//   - 验证的流程状态与最终结论（ValidationStatus / ValidationVerdict）；
//   - 步骤最终进入什么状态（StepStatus / Verified）。
//
// Verified 为 true 只可能来自真实验证通过；开发成功但未验证不会让它为 true。
type ExecuteAndValidateResult struct {
	TaskID domain.ID
	StepID domain.ID
	// AttemptID 是本次开发尝试；每次闭环都创建新的 Attempt。
	AttemptID domain.ID
	// SessionID 是本次开发尝试对应的独立 Session。
	SessionID     domain.ID
	AttemptNumber int

	// Outcome 是开发阶段结论。
	Outcome domain.StepExecutionOutcome
	// DeveloperSummary 是开发阶段的结果摘要。
	DeveloperSummary string
	// DeveloperFailure 是开发阶段失败原因，成功时为空。
	DeveloperFailure string
	// ExitCode 是开发阶段 Agent 进程退出码，未能取得时为 -1。
	ExitCode int
	// DeveloperFinalized 表示开发阶段的执行记录是否完整写入。
	DeveloperFinalized bool
	// DeveloperFinalizationErrors 是开发阶段写入失败的原因。
	DeveloperFinalizationErrors []string

	// ValidationRan 表示开发成功后是否真的启动了验证。
	ValidationRan bool
	// ValidationSkipReason 是验证未启动或未完成的原因。
	ValidationSkipReason string
	// ValidationRunID 是本次验证记录标识，未发起验证时为空。
	ValidationRunID domain.ID
	// ValidationStatus 是验证流程状态。
	ValidationStatus domain.ValidationRunStatus
	// ValidationVerdict 是验证结论。
	ValidationVerdict domain.VerificationVerdict
	// ValidationPassed 是结论的便捷布尔值，等价于 Verdict == passed。
	ValidationPassed bool
	// ValidationSummary 是验证结论摘要。
	ValidationSummary string
	// ValidationFailureSummary 是验证未通过的原因。
	ValidationFailureSummary string
	// Checks 是逐项检查结果。
	Checks []domain.ValidationCheckResult
	// ValidationPersisted 表示验证结论与检查结果是否可靠写入。
	ValidationPersisted bool
	// ValidationPersistenceErrors 是验证结果写入失败的原因。
	ValidationPersistenceErrors []string
	// ReviewRan 表示开发通过确定性检查后是否真的发起了语义审查。
	ReviewRan bool
	// ReviewStatus 是语义审查的实际结论，未发起审查时为空。
	ReviewStatus domain.ReviewStatus
	// ReviewSummary 是语义审查的可读说明（已脱敏并裁剪）。
	ReviewSummary string
	// ValidatorSessionID 是语义审查使用的会话标识，未发起审查时为空。
	ValidatorSessionID domain.ID
	// Assessment 是语义审查产出的结构化评估，未取得可解释结论时为空。
	Assessment *domain.AgentAssessment
	// InfrastructureError 表示验证因基础设施异常没有拿到可用证据。
	//
	// 它与"代码验证失败"是不同事实：容器不可用、命令无法启动或结果无法确认都属于
	// 基础设施异常，不得伪装成代码失败，也不得判为通过。
	InfrastructureError bool
	// FeedbackPersisted 表示验证失败反馈是否已保存，供后续显式重试引用。
	FeedbackPersisted bool

	// Verification 是从验证记录派生的步骤验证状态。
	Verification domain.StepVerificationState
	// StepStatus 是收口后的步骤状态。
	StepStatus domain.StepStatus
	// TaskStatus 是收口后的任务状态。
	TaskStatus domain.TaskStatus
	// Verified 表示步骤是否进入"已验证完成"。
	Verified bool

	// Finalized 表示收口状态是否全部写入成功。
	Finalized bool
	// FinalizationErrors 是收口写入失败的原因。
	FinalizationErrors []string
	// StartedAt / FinishedAt 是闭环的开始与结束时间。
	StartedAt  time.Time
	FinishedAt time.Time
	// Round 是本次闭环中这一轮修复的轮次（从 1 开始）。
	//
	// 轮次只用于追溯：每一轮都是独立的 Attempt / Session / Validation Run，历史
	// 记录不会被覆盖。
	Round int
	// Attempts 是本次闭环中已确认落库的开发尝试次数（含首轮）。
	Attempts int
	// Repair 是修复循环的最终判定：是否继续、为什么停止。
	//
	// 它由 domain.DecideRepairLoop 依据持久化事实计算，编排器不复制规则。
	Repair domain.RepairDecision
	// RepairCloseout 是修复循环结束时依据 domain.DecideRepairCloseout 得出的收口结论。
	RepairCloseout domain.RepairCloseoutDecision
}

// StepWorkflowOptions 是构造闭环协调器的输入。
type StepWorkflowOptions struct {
	// Steps 是单步骤执行协调器，必填。
	Steps *StepExecutor
	// Validation 是确定性验证引擎，必填。
	Validation *ValidationService
	// Store 是持久化入口，必填。
	Store StepWorkflowStore
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
	// DefaultValidationTimeout 覆盖未指定验证超时时的默认值。
	DefaultValidationTimeout time.Duration
}

// StepWorkflowService 是单步骤"开发 → 验证"闭环协调器。
//
// 职责边界：
//   - 校验任务、步骤归属、前置步骤、验证配置与容器可用性；
//   - 调用 StepExecutor 完成开发阶段（每次新建 Attempt 与独立 Session）；
//   - 只在开发阶段正常结束、容器未变化、验证配置有效时调用 ValidationService；
//   - 依据真实检查结果收口步骤状态，并把失败反馈持久化供后续显式重试引用。
//
// 它不实现任务级编排：不生成计划、不串联多个步骤、不自动重试、不自动修复代码，
// 也不创建、启动、删除或接管容器。
type StepWorkflowService struct {
	steps      *StepExecutor
	validation *ValidationService
	store      StepWorkflowStore
	logger     *slog.Logger
	now        func() time.Time
	// defaultValidationTimeout 是整次验证的默认总超时。
	defaultValidationTimeout time.Duration
	// running 记录本进程内正在闭环执行的步骤，用于快速拒绝重复请求。
	//
	// 它是第一道防线而不是一致性保证：步骤在开发阶段由数据库条件更新占用，并在
	// 验证阶段保持占用，跨进程并发同样会被拒绝。
	running *stepExecutionRegistry
	// lifecycle 管理后台服务状态与在途闭环，使 Server 停止时能取消并等待收敛。
	lifecycle lifecycleState
}

// NewStepWorkflowService 构造单步骤闭环协调器。
func NewStepWorkflowService(options StepWorkflowOptions) (*StepWorkflowService, error) {
	if options.Steps == nil {
		return nil, fmt.Errorf("闭环协调器初始化失败: 步骤执行协调器不能为空")
	}
	if options.Validation == nil {
		return nil, fmt.Errorf("闭环协调器初始化失败: 验证引擎不能为空")
	}
	if options.Store == nil {
		return nil, fmt.Errorf("闭环协调器初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	timeout := options.DefaultValidationTimeout
	if timeout <= 0 {
		timeout = defaultStepWorkflowValidationTimeout
	}
	return &StepWorkflowService{
		steps:                    options.Steps,
		validation:               options.Validation,
		store:                    options.Store,
		logger:                   logger,
		now:                      now,
		defaultValidationTimeout: timeout,
		running:                  newStepExecutionRegistry(),
	}, nil
}

// ActiveWorkflows 返回本进程内正在执行的闭环数量。
func (s *StepWorkflowService) ActiveWorkflows() int {
	return s.running.len()
}

// Name 返回后台服务名称。
func (s *StepWorkflowService) Name() string {
	return "step_workflow"
}

// Start 标记服务可以接受闭环请求。
func (s *StepWorkflowService) Start(ctx context.Context) error {
	state := &s.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.started {
		return fmt.Errorf("闭环协调器已经启动")
	}
	state.started = true
	// 分离自父上下文：Server 关闭信号通过 Stop 显式传播，而不是依赖 HTTP 请求上下文。
	stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))
	state.stopSignal = stopSignal
	state.stopOnce = stop
	return nil
}

// Stop 取消在途闭环并等待其收敛。
//
// 停止后新的闭环请求直接返回错误；已经在执行的闭环会收到取消信号，开发阶段收敛为
// 中断、验证阶段收敛为失败终态，不会留下 running 残留。
func (s *StepWorkflowService) Stop(ctx context.Context) error {
	state := &s.lifecycle
	state.mu.Lock()
	if state.stopped {
		state.mu.Unlock()
		return nil
	}
	state.stopped = true
	stop := state.stopOnce
	state.mu.Unlock()
	if stop != nil {
		stop()
	}
	done := make(chan struct{})
	go func() {
		state.runs.Wait()
		close(done)
	}()
	timeout := defaultStepExecutorStopTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("闭环协调器停止被取消: %w", ctx.Err())
	case <-time.After(timeout):
		return fmt.Errorf(
			"闭环协调器停止超时: 仍有 %d 个闭环未收敛",
			s.ActiveWorkflows(),
		)
	}
}

// beginRun 登记一次在途闭环，服务已停止时拒绝。
func (s *StepWorkflowService) beginRun(ctx context.Context) (context.Context, func(), error) {
	state := &s.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stopped {
		return nil, nil, fmt.Errorf("%w: 不再接受新的闭环请求", ErrStepExecutorStopped)
	}
	if !state.started {
		return nil, nil, fmt.Errorf("闭环协调器尚未启动")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrStepExecutorStopped, err)
	}
	state.active++
	state.runs.Add(1)
	stop := state.stopSignal
	runCtx, cancelRun := context.WithCancelCause(ctx)
	finish := func() {
		cancelRun(nil)
		state.mu.Lock()
		state.active--
		state.mu.Unlock()
		state.runs.Done()
	}
	if stop == nil {
		return runCtx, finish, nil
	}
	go func() {
		select {
		case <-stop.Done():
			cancelRun(ErrStepExecutorStopped)
		case <-runCtx.Done():
		}
	}()
	return runCtx, finish, nil
}

// loadTask 读取任务，不存在或已删除时返回"不存在"。
func (s *StepWorkflowService) loadTask(ctx context.Context, id domain.ID) (domain.Task, error) {
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrStepExecutionNotFound, id)
		}
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrStepExecutionNotFound, id)
	}
	return *task, nil
}

// loadStep 读取步骤并确认它属于指定任务。
func (s *StepWorkflowService) loadStep(
	ctx context.Context,
	task domain.Task,
	id domain.ID,
) (domain.Step, error) {
	step, err := s.store.GetStep(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Step{}, fmt.Errorf("%w: 步骤 %s", ErrStepExecutionNotFound, id)
		}
		return domain.Step{}, fmt.Errorf("读取步骤失败: %w", err)
	}
	if step.TaskID != task.ID {
		// 归属不成立与不存在返回同一错误：不能让调用方通过状态码差异探测其他任务。
		return domain.Step{}, fmt.Errorf("%w: 步骤 %s", ErrStepExecutionNotFound, id)
	}
	return *step, nil
}

// checkPrerequisites 校验指定步骤的全部前序步骤是否已完成。
//
// 规则只在 domain 中定义一次（domain.CheckStepPrerequisites）：这里只负责读取同一
// 任务的步骤列表并把拒绝原因映射为状态冲突。
func (s *StepWorkflowService) checkPrerequisites(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
) error {
	steps, err := s.store.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("读取计划步骤失败: %w", err)
	}
	if len(steps) == 0 {
		return nil
	}
	if err := domain.CheckStepPrerequisites(steps, step); err != nil {
		return fmt.Errorf("%w: %w", ErrStepExecutionConflict, err)
	}
	return nil
}

// initialFeedback 读取步骤最近一次的验证失败反馈，作为首轮执行的修复依据。
//
// 与"读取失败不阻止执行"的旧语义不同，这里区分三种情况：
//   - 没有反馈：返回 nil，表示这是该步骤的首次执行，提示词不需要历史问题小节；
//   - 读取成功但归属不符：返回错误，拿错步骤的反馈会让修复方向完全错误；
//   - 读取失败：返回错误，不能在"不确定上一轮有没有失败"的情况下继续执行。
//
// 归属校验是必需的：数据库不声明外键，孤立或错配的关联 ID 只能由应用层识别。
func (s *StepWorkflowService) initialFeedback(
	ctx context.Context,
	step domain.Step,
) (*domain.ValidationFailureFeedback, error) {
	feedback, err := s.store.LatestValidationFailureFeedbackByStep(ctx, step.ID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取上一次验证失败反馈失败: %w", err)
	}
	if feedback.StepID != step.ID || feedback.TaskID != step.TaskID {
		return nil, fmt.Errorf(
			"%w: 失败反馈 %s 与步骤 %s 的归属不一致",
			ErrStepWorkflowInvalidConfig,
			feedback.ValidationRunID,
			step.ID,
		)
	}
	return feedback, nil
}

// validationTimeout 返回本次验证的总超时时间。
func (s *StepWorkflowService) validationTimeout(requested time.Duration) time.Duration {
	if requested > 0 {
		return requested
	}
	return s.defaultValidationTimeout
}

// parseWorkflowID 解析请求中的标识。
func parseWorkflowID(label, raw string) (domain.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: %s标识不能为空", ErrStepExecutionInvalidInput, label)
	}
	if strings.Contains(trimmed, "/") {
		return "", fmt.Errorf("%w: %s标识非法", ErrStepExecutionInvalidInput, label)
	}
	return domain.ID(trimmed), nil
}
