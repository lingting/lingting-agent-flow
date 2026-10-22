package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
	"github.com/lingting/agentflow/internal/storage"
)

// 统一任务启动流程的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。下层服务
// （环境准备、计划生成、工作流编排）的错误类别原样向上传递，因此调用方仍能区分
// "状态冲突"与"前置条件缺失"。
var (
	// ErrTaskLaunchNotFound 表示任务不存在或已删除。
	ErrTaskLaunchNotFound = errors.New("app: 启动目标不存在")
	// ErrTaskLaunchInvalidInput 表示启动请求参数不合法。
	ErrTaskLaunchInvalidInput = errors.New("app: 启动请求参数非法")
	// ErrTaskLaunchConflict 表示当前状态不允许启动，或已有执行在推进同一任务。
	ErrTaskLaunchConflict = errors.New("app: 启动状态冲突")
	// ErrTaskLaunchPrerequisite 表示启动所需的前置条件未满足。
	ErrTaskLaunchPrerequisite = errors.New("app: 启动前置条件未满足")
	// ErrTaskLaunchStopped 表示服务已停止，不再接受新的启动请求。
	ErrTaskLaunchStopped = errors.New("app: 启动服务已停止")
	// ErrTaskLaunchFailed 表示启动流程已经执行但没有成功。
	//
	// 它携带失败阶段与脱敏原因：调用方据此返回"执行失败"而不是把它当成内部错误。
	ErrTaskLaunchFailed = errors.New("app: 启动流程未成功")
)

// 统一启动流程的默认参数与上限。
const (
	// maxTaskLaunchPrompt 是补充提示词的长度上限。
	maxTaskLaunchPrompt = 64 << 10
	// defaultTaskLaunchStopTimeout 是等待在途启动收敛的默认时限。
	defaultTaskLaunchStopTimeout = 30 * time.Second
)

// TaskLaunchRequest 是通过接口发起一次统一启动的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方只能指定任务与可选的执行参数，
// 不能指定容器、Session 路径、工作目录、步骤列表、命令行参数或角色覆盖。
type TaskLaunchRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// Prompt 是可选补充提示词，会追加在计划与每一步骤的提示词之后。
	Prompt string
	// Timeout 是单个步骤开发阶段的超时，零值使用任务配置或服务默认值。
	Timeout time.Duration
	// ValidationTimeout 是单个步骤验证阶段的超时，零值使用服务默认值。
	ValidationTimeout time.Duration
	// PlanTimeout 是本次 Planner 调用的超时，零值使用任务配置或服务默认值。
	PlanTimeout time.Duration
}

// TaskLaunchResult 是一次统一启动的结果。
//
// 它如实区分四件独立的事实：走到了哪个阶段、任务当前真实状态、计划是否已写入
// 数据库、以及失败发生在哪个阶段。调用方不需要再查执行记录就能判断后续可做什么。
type TaskLaunchResult struct {
	// TaskID 是任务标识。
	TaskID domain.ID
	// Stage 是本次启动最后进入的阶段。
	Stage domain.TaskLaunchStage
	// TaskStatus 是收尾后读取到的任务真实状态。
	TaskStatus domain.TaskStatus
	// ContainerID 是本次启动使用的容器登记记录 ID，未准备时为空。
	ContainerID string
	// ContainerPrepared 表示本次调用真的创建了容器。
	ContainerPrepared bool
	// PlanPersisted 表示计划是否已完整写入数据库。
	PlanPersisted bool
	// PlanSteps 是已持久化的计划步骤，按序号升序排列。
	PlanSteps []domain.Step
	// Run 是启动后的工作流执行记录，未启动时为空值。
	Run WorkflowRunView
	// WorkflowStarted 表示本次调用真的启动了工作流推进。
	WorkflowStarted bool
	// FailureStage 是失败发生的阶段名，成功时为空。
	FailureStage string
	// Failure 是失败原因摘要，成功时为空。
	Failure string
	// StartedAt / FinishedAt 是本次启动的起止时间。
	StartedAt  time.Time
	FinishedAt time.Time
}

// TaskLaunchStore 是启动流程依赖的存储能力组合。
//
// 它由 storage.Store 直接满足；测试也可以只实现用到的部分，构造"写入失败"这类
// 真实失败路径而不改动业务代码。
type TaskLaunchStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	ListStepsByTask(ctx context.Context, taskID domain.ID) ([]domain.Step, error)
	UpdateTaskIfStatus(ctx context.Context, task domain.Task, expected []domain.TaskStatus) error
	UpdateStepIfStatus(ctx context.Context, step domain.Step, expected []domain.StepStatus) error
	GetActiveWorkflowRun(ctx context.Context, taskID domain.ID) (*domain.WorkflowRun, error)
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
	// ListTasksWithOccupiedSteps 返回存在执行占用步骤的任务标识。
	//
	// 启动恢复用它定位"进程退出后仍被占用"的步骤，不重放任何 Agent 调用。
	ListTasksWithOccupiedSteps(ctx context.Context) ([]domain.ID, error)
}

// TaskLaunchEnvironmentPort 是启动流程对环境准备能力的需求。
//
// 实现负责校验节点、准备镜像、创建容器并把容器写回任务；启动流程不重复实现它们。
type TaskLaunchEnvironmentPort interface {
	PrepareForTask(ctx context.Context, request TaskEnvironmentRequest) (execenv.Prepared, error)
}

// TaskLaunchContainerPort 是启动流程对容器核验能力的需求。
//
// 启动流程只核验与使用容器：它不创建、启动、删除或接管容器。
type TaskLaunchContainerPort interface {
	InspectContainer(ctx context.Context, request InspectContainerRequest) (ContainerDetail, error)
}

// TaskLaunchPlanPort 是启动流程对计划生成能力的需求。
type TaskLaunchPlanPort interface {
	GeneratePlan(ctx context.Context, request GeneratePlanRequest) (PlanGenerationResult, error)
}

// TaskLaunchWorkflowPort 是启动流程对工作流编排能力的需求。
type TaskLaunchWorkflowPort interface {
	StartWorkflow(ctx context.Context, request StartWorkflowRequest) (WorkflowRunView, error)
}

// TaskLaunchOptions 是构造统一启动服务的输入。
type TaskLaunchOptions struct {
	// Store 是持久化入口，必填。
	Store TaskLaunchStore
	// Environments 提供依据任务准备执行环境的能力。
	//
	// 为空表示本次运行不提供容器准备：缺少容器时启动流程返回前置条件错误，
	// 而不是静默跳过准备去执行一个没有容器的计划。
	Environments TaskLaunchEnvironmentPort
	// Containers 提供容器核验能力，用于判断已有容器是否仍可用。
	Containers TaskLaunchContainerPort
	// Planner 提供计划生成能力。
	Planner TaskLaunchPlanPort
	// Workflow 提供工作流启动能力。
	Workflow TaskLaunchWorkflowPort
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
}

// TaskLaunchService 是统一任务启动流程的唯一应用层入口。
//
// 职责边界：
//   - 依据持久化事实判断下一步该做哪件事（建容器 / 生成计划 / 启动编排）；
//   - 复用既有的环境准备、计划生成与工作流编排能力，不重复实现它们；
//   - 任一阶段失败立即停止并如实返回，不自动重试、不删除容器、不重放外部操作。
//
// 它不拼接 Pi 命令、不构造 Docker 命令、不实现 Git 操作，也不创建 Attempt 与
// Session：这些职责仍属于既有组件。
type TaskLaunchService struct {
	store        TaskLaunchStore
	environments TaskLaunchEnvironmentPort
	containers   TaskLaunchContainerPort
	planner      TaskLaunchPlanPort
	workflow     TaskLaunchWorkflowPort
	logger       *slog.Logger
	now          func() time.Time

	// taskLocks 保证同一任务在本进程内不会被两个 goroutine 同时启动。
	//
	// 它是第一道防线而不是一致性保证：跨进程并发仍由各阶段自身的条件更新与
	// workflow_runs 的部分唯一索引拒绝。
	taskLocks keyedTaskMutex
	// lifecycle 管理后台服务状态与在途启动，使 Server 停止时能取消并等待收敛。
	lifecycle taskLaunchLifecycle
}

// NewTaskLaunchService 构造统一启动服务。
func NewTaskLaunchService(options TaskLaunchOptions) (*TaskLaunchService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("启动服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &TaskLaunchService{
		store:        options.Store,
		environments: options.Environments,
		containers:   options.Containers,
		planner:      options.Planner,
		workflow:     options.Workflow,
		logger:       logger,
		now:          now,
	}, nil
}

// Name 返回后台服务名称。
func (s *TaskLaunchService) Name() string {
	return "task_launch"
}

// Start 标记服务可以接受启动请求，并收敛进程退出留下的步骤执行占用。
//
// 启动本身不重放任何外部操作：工作流执行记录、计划生成现场与取消现场由
// TaskWorkflowService 的启动恢复处理；本服务只补上它没有覆盖的一类残留——
// 仍被占用（running / developing / verifying）的步骤。占用会阻止该步骤再次
// 执行，若不收敛，任务既无法推进也无法自动结束。
func (s *TaskLaunchService) Start(ctx context.Context) error {
	state := &s.lifecycle
	state.mu.Lock()

	if state.started {
		return fmt.Errorf("启动服务已经启动")
	}
	state.started = true
	// 分离自父上下文：Server 关闭信号通过 Stop 显式传播。
	stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))
	state.stopSignal = stopSignal
	state.stopOnce = stop
	state.mu.Unlock()
	// 恢复结论没有落库时不得让服务继续启动：那会让未经处理的现场被当作已恢复。
	return s.RecoverStepOccupancy(ctx)
}

// Stop 取消在途启动并等待其收敛。
func (s *TaskLaunchService) Stop(ctx context.Context) error {
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
	timeout := defaultTaskLaunchStopTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("启动服务停止被取消: %w", ctx.Err())
	case <-time.After(timeout):
		return fmt.Errorf(
			"启动服务停止超时: 仍有 %d 次启动未收敛",
			s.ActiveLaunches(),
		)
	}
}

// ActiveLaunches 返回本进程内正在推进的启动数量。
func (s *TaskLaunchService) ActiveLaunches() int {
	return s.lifecycle.activeCount()
}

// beginLaunch 登记一次在途启动，服务已停止时拒绝。
func (s *TaskLaunchService) beginLaunch(ctx context.Context) (context.Context, func(), error) {
	state := &s.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stopped {
		return nil, nil, fmt.Errorf("%w: 不再接受新的启动请求", ErrTaskLaunchStopped)
	}
	if !state.started {
		return nil, nil, fmt.Errorf("启动服务尚未启动")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrTaskLaunchStopped, err)
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
			cancelRun(ErrTaskLaunchStopped)
		case <-runCtx.Done():
		}
	}()
	return runCtx, finish, nil
}

// taskLaunchLifecycle 记录后台服务的启动与停止状态。
type taskLaunchLifecycle struct {
	mu         sync.Mutex
	started    bool
	stopped    bool
	stopSignal context.Context
	stopOnce   context.CancelFunc
	runs       sync.WaitGroup
	active     int
}

// activeCount 返回在途启动数量。
func (l *taskLaunchLifecycle) activeCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

// loadTask 读取未被逻辑删除的任务。
func (s *TaskLaunchService) loadTask(ctx context.Context, id domain.ID) (domain.Task, error) {
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrTaskLaunchNotFound, id)
		}
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrTaskLaunchNotFound, id)
	}
	return *task, nil
}

// appendEvent 追加任务事件。
//
// 事件写入失败只记录日志，不回滚已写入的执行状态：状态是唯一真相来源，事件用于
// 事后定位，两者不能因为一次写入失败而互相破坏。
func (s *TaskLaunchService) appendEvent(
	ctx context.Context,
	taskID domain.ID,
	eventType domain.EventType,
	payload map[string]any,
) {
	event, err := domain.NewEvent(
		taskID,
		nil,
		nil,
		nil,
		eventType,
		marshalStepExecutionPayload(payload),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造启动事件失败",
			slog.String("task_id", taskID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(ctx, event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入启动事件失败",
			slog.String("task_id", taskID.String()),
			slog.String("event", string(eventType)),
			slog.String("error", errorSummary(err)),
		)
	}
}

// classifyLaunchError 把领域校验错误映射为应用层错误类别。
//
// 规则只在 domain 中定义一次：这里只做类别转换，不重新判断业务语义。
func classifyLaunchError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrInvalidState) {
		return fmt.Errorf("%w: %w", ErrTaskLaunchConflict, err)
	}
	return fmt.Errorf("%w: %w", ErrTaskLaunchPrerequisite, err)
}

// validateLaunchOptions 校验启动参数。
func validateLaunchOptions(prompt string, timeout, validationTimeout, planTimeout time.Duration) error {
	if len(prompt) > maxTaskLaunchPrompt {
		return fmt.Errorf(
			"%w: 补充提示词不能超过 %d 个字符",
			ErrTaskLaunchInvalidInput,
			maxTaskLaunchPrompt,
		)
	}
	for _, item := range []struct {
		name  string
		value time.Duration
	}{
		{name: "开发超时", value: timeout},
		{name: "验证超时", value: validationTimeout},
		{name: "规划超时", value: planTimeout},
	} {
		if item.value < 0 {
			return fmt.Errorf("%w: %s不能为负数", ErrTaskLaunchInvalidInput, item.name)
		}
	}
	return nil
}

// parseLaunchTaskID 解析并校验任务标识。
func parseLaunchTaskID(raw string) (domain.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 任务标识不能为空", ErrTaskLaunchInvalidInput)
	}
	if strings.Contains(trimmed, "/") {
		return "", fmt.Errorf("%w: 任务标识非法", ErrTaskLaunchInvalidInput)
	}
	return domain.ID(trimmed), nil
}
