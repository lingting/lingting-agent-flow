package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 任务级工作流编排器的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。下层服务
// （步骤执行、验证、代码保存）的错误类别原样向上传递，因此调用方仍能区分
// "状态冲突"与"前置条件缺失"。
var (
	// ErrWorkflowNotFound 表示任务或工作流执行记录不存在。
	ErrWorkflowNotFound = errors.New("app: 工作流执行目标不存在")
	// ErrWorkflowInvalidInput 表示请求参数不合法。
	ErrWorkflowInvalidInput = errors.New("app: 工作流请求参数非法")
	// ErrWorkflowConflict 表示当前状态不允许该操作，或同一任务已有工作流在推进。
	ErrWorkflowConflict = errors.New("app: 工作流状态冲突")
	// ErrWorkflowPrerequisite 表示启动工作流所需的前置条件未满足。
	ErrWorkflowPrerequisite = errors.New("app: 工作流前置条件未满足")
	// ErrWorkflowStopped 表示服务已停止，不再接受新的工作流请求。
	// ErrWorkflowStopped 表示服务已停止，不再接受新的工作流请求。
	ErrWorkflowStopped = errors.New("app: 工作流编排器已停止")
	// ErrPlanInvalidOutput 表示 Planner 输出无法构成一份可执行计划。
	//
	// 它与"Planner 调用失败"是不同的事实：进程可能正常退出，但输出为空、不是结构化
	// 计划或未通过确定性校验。两者都不能让任务进入 planned，但必须能被调用方区分。
	ErrPlanInvalidOutput = errors.New("app: Planner 输出不是合法计划")
)

// errWorkflowCancelRequested 是用户取消工作流时的取消原因。
//
// 它与 ErrWorkflowStopped 区分开：两者都会终止执行，但诊断信息必须说明真实的触发
// 来源，否则"服务关闭"会被误读为"用户取消"。
var errWorkflowCancelRequested = errors.New("app: 用户取消了工作流执行")

// 工作流编排器的默认参数。
const (
	// defaultWorkflowRunLease 是执行权（租约）的默认有效期。
	//
	// 它只用于识别"进程已退出但记录仍停留在 running"的残留：租约过期后其他进程
	// 才允许接管，因此不会出现两个 Worker 同时推进同一任务。
	defaultWorkflowRunLease = 10 * time.Minute
	// defaultWorkflowStopTimeout 是等待在途工作流收敛的默认时限。
	defaultWorkflowStopTimeout = 30 * time.Second
	// maxWorkflowPrompt 是补充提示词的长度上限。
	maxWorkflowPrompt = 64 << 10
	// resumeLockTimeout 是恢复请求等待在途执行释放任务锁的最长时间。
	//
	// 状态已落盘为可恢复时，上一次推进通常只剩收尾写入（事件追加等）；等待比直接报
	// 冲突更符合用户预期，超过时限仍如实返回状态冲突。恢复是显式用户操作，因此这里
	// 允许比 HTTP 默认超时更长的等待。
	resumeLockTimeout = 60 * time.Second
)

// WorkflowTaskRepository 是编排器需要的任务读写能力。
type WorkflowTaskRepository interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	UpdateTaskIfStatus(
		ctx context.Context,
		task domain.Task,
		expected []domain.TaskStatus,
	) error
	// ListTasks 按状态筛选任务，用于启动时收敛计划生成的残留现场。
	ListTasks(ctx context.Context, filter storage.TaskFilter) ([]domain.Task, error)
}

// WorkflowStepRepository 是编排器需要的步骤读写能力。
type WorkflowStepRepository interface {
	GetStep(ctx context.Context, id domain.ID) (*domain.Step, error)
	ListStepsByTask(ctx context.Context, taskID domain.ID) ([]domain.Step, error)
	UpdateStepIfStatus(
		ctx context.Context,
		step domain.Step,
		expected []domain.StepStatus,
	) error
}

// WorkflowSessionRepository 是编排器需要的会话读取能力。
//
// 只读：会话记录由 Pi Runner 与会话服务写入，编排器不重复实现它们。
type WorkflowSessionRepository interface {
	ListSessionsByTask(ctx context.Context, taskID domain.ID) ([]domain.Session, error)
}

// WorkflowRunRepository 是编排器需要的执行记录读写能力。
type WorkflowRunRepository interface {
	CreateWorkflowRun(ctx context.Context, run domain.WorkflowRun) error
	UpdateWorkflowRun(ctx context.Context, run domain.WorkflowRun) error
	GetWorkflowRun(ctx context.Context, id domain.ID) (*domain.WorkflowRun, error)
	GetActiveWorkflowRun(ctx context.Context, taskID domain.ID) (*domain.WorkflowRun, error)
	ListWorkflowRunsByTask(ctx context.Context, taskID domain.ID) ([]domain.WorkflowRun, error)
	ListUnfinishedWorkflowRuns(ctx context.Context) ([]domain.WorkflowRun, error)
}

// WorkflowHostRepository 是编排器需要的执行节点读取能力。
//
// 它用于在执行前拒绝未通过环境预检的节点：判断复用统一的
// EnsureHostUsableForRemoteTask，不在这里复制第二套准入规则。
type WorkflowHostRepository interface {
	GetExecutionHost(ctx context.Context, id domain.ID) (*domain.ExecutionHost, error)
}

// WorkflowContainerPort 是编排器对任务容器的需求。
//
// 编排器只核验与使用容器：它不创建、启动、删除或接管容器，也不会在容器不可用时
// 静默回退到本机执行。RunAgent 用于计划生成：Planner 必须在真实仓库上下文所在的
// 任务容器内运行。
type WorkflowContainerPort interface {
	// InspectContainer 核验容器当前状态。
	InspectContainer(ctx context.Context, request InspectContainerRequest) (ContainerDetail, error)
	// RunAgent 在受管理的任务容器内执行一次 Agent 调用。
	RunAgent(ctx context.Context, request ContainerAgentRequest) (ContainerAgentResult, error)
}

// WorkflowCodeSaverPort 是编排器对代码保存的需求。
//
// 实现负责提交、推送与可选的合并，并分别持久化结果；编排器只读取结果，不重复
// 实现 Git 操作，也不决定"推送成功即任务成功"。
type WorkflowCodeSaverPort interface {
	SaveCode(ctx context.Context, request CodeSaveRequest) (CodeSaveResult, error)
}

// WorkflowStepRunner 是编排器对单步骤开发—验证闭环的需求。
//
// 该接口由 app.StepWorkflowService 实现：编排器不自行创建 Attempt、不自行拼接
// Pi 命令、不自行判定验证结论。
type WorkflowStepRunner interface {
	ExecuteAndValidate(
		ctx context.Context,
		request ExecuteAndValidateRequest,
	) (ExecuteAndValidateResult, error)
}

// TaskWorkflowStore 是编排器依赖的存储能力组合。
//
// 它由 storage.Store 直接满足；测试也可以只实现用到的部分，构造"写入失败"这类
// 真实失败路径而不改动业务代码。
type TaskWorkflowStore interface {
	WorkflowTaskRepository
	WorkflowStepRepository
	WorkflowSessionRepository
	WorkflowRunRepository
	WorkflowHostRepository
	// GetRole 读取角色，用于计划生成时锁定规划角色。
	GetRole(ctx context.Context, id domain.ID) (*domain.Role, error)
	// WithTx 在单个事务内执行 fn，用于把计划步骤与任务状态一次写入。
	WithTx(ctx context.Context, fn func(ctx context.Context, tx storage.Tx) error) error
	// AppendEvent 追加任务事件。
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
}

// TaskWorkflowOptions 是构造任务级工作流编排器的输入。
type TaskWorkflowOptions struct {
	// Store 是持久化入口，必填。
	Store TaskWorkflowStore
	// Steps 是单步骤开发—验证闭环服务，必填。
	Steps WorkflowStepRunner
	// Containers 是容器核验能力，必填。
	Containers WorkflowContainerPort
	// CodeSaver 是代码保存能力。
	//
	// 为空表示本次运行不提供代码保存：编排器仍会按顺序执行并验证全部步骤，但任务
	// 收尾会停留在 blocked 并给出"代码保存未完成"的明确原因，绝不把"步骤全部通过
	// 验证"当作任务完整成功。
	CodeSaver WorkflowCodeSaverPort
	// Sessions 是会话服务，计划生成用它创建并收敛 Planner 会话记录。
	//
	// 为空表示本次运行不提供计划生成：GeneratePlan 直接返回错误，而不是生成一份
	// 没有可追溯会话记录的计划。
	Sessions *SessionService
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
	// Lease 是执行权有效期，零值使用默认值。
	Lease time.Duration
	// DefaultTimeout 是单个步骤开发阶段的默认超时，零值使用闭环服务默认值。
	DefaultTimeout time.Duration
	// DefaultValidationTimeout 是单个步骤验证阶段的默认超时。
	DefaultValidationTimeout time.Duration
	// DefaultPlanTimeout 是 Planner 调用的默认超时，零值使用默认值。
	DefaultPlanTimeout time.Duration
}

// TaskWorkflowService 是任务级工作流编排器。
//
// 职责边界：
//   - 校验任务是否可以进入工作流，并读取已经持久化的计划；
//   - 按稳定顺序逐个调用已有的单步骤开发—验证闭环；
//   - 依据真实结果推进 Workflow Run 的游标与状态；
//   - 在失败、取消或不确定状态时停止推进，等待用户显式决定；
//   - 协调已有的代码保存能力完成任务收尾。
//
// 它同时承担计划生成（GeneratePlan）：在任务容器内调用 Planner、把输出校验为
// 结构化计划并一次写入数据库。它不拼接 Pi 命令、不构造 Docker 命令、不实现 Git
// 操作，也不重复创建 Session：这些职责仍属于既有组件。
type TaskWorkflowService struct {
	store      TaskWorkflowStore
	steps      WorkflowStepRunner
	containers WorkflowContainerPort
	codeSaver  WorkflowCodeSaverPort
	sessions   *SessionService
	logger     *slog.Logger
	now        func() time.Time
	owner      string

	lease                    time.Duration
	defaultTimeout           time.Duration
	defaultValidationTimeout time.Duration
	defaultPlanTimeout       time.Duration
	// taskLocks 保证同一任务在本进程内不会被两个 goroutine 同时推进。
	//
	// 它是第一道防线而不是一致性保证：跨进程并发仍由 workflow_runs 的部分唯一索引
	// 与版本号条件更新拒绝。
	taskLocks keyedTaskMutex
	// cancels 记录在途工作流的取消函数，使取消请求能传播到实际执行。
	cancels cancelRegistry
	// lifecycle 管理后台服务状态与在途工作流，使 Server 停止时能取消并等待收敛。
	lifecycle workflowLifecycle
}

// NewTaskWorkflowService 构造任务级工作流编排器。
func NewTaskWorkflowService(options TaskWorkflowOptions) (*TaskWorkflowService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("工作流编排器初始化失败: 存储不能为空")
	}
	if options.Steps == nil {
		return nil, fmt.Errorf("工作流编排器初始化失败: 步骤闭环服务不能为空")
	}
	if options.Containers == nil {
		return nil, fmt.Errorf("工作流编排器初始化失败: 容器核验能力不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	lease := options.Lease
	if lease <= 0 {
		lease = defaultWorkflowRunLease
	}
	return &TaskWorkflowService{
		store:                    options.Store,
		steps:                    options.Steps,
		containers:               options.Containers,
		codeSaver:                options.CodeSaver,
		sessions:                 options.Sessions,
		logger:                   logger,
		now:                      now,
		owner:                    "workflow-" + domain.NewID().String(),
		lease:                    lease,
		defaultTimeout:           options.DefaultTimeout,
		defaultValidationTimeout: options.DefaultValidationTimeout,
		defaultPlanTimeout:       options.DefaultPlanTimeout,
	}, nil
}

// CodeSaverAvailable 返回是否具备代码保存能力。
//
// 该信息用于让接口与前端如实展示"任务还不能判定为完整成功"的原因，而不是在
// 收尾阶段才暴露为一条模糊的失败。
func (s *TaskWorkflowService) CodeSaverAvailable() bool {
	return s.codeSaver != nil
}

// ActiveWorkflows 返回本进程内正在推进的工作流数量。
func (s *TaskWorkflowService) ActiveWorkflows() int {
	return s.lifecycle.activeCount()
}

// Name 返回后台服务名称。
func (s *TaskWorkflowService) Name() string {
	return "task_workflow"
}

// Start 标记服务可以接受工作流请求，并恢复未结束的执行记录。
//
// 恢复只处理"进程已退出留下的残留"：停留在 running 的记录会按数据库事实核验，
// 无法确认现场时收敛为 recovery_required，不会盲目重放 Pi 调用。blocked 与
// pending 记录保持原状，等待用户显式恢复。
func (s *TaskWorkflowService) Start(ctx context.Context) error {
	state := &s.lifecycle
	state.mu.Lock()
	if state.started {
		state.mu.Unlock()
		return fmt.Errorf("工作流编排器已经启动")
	}
	state.started = true
	// 分离自父上下文：Server 关闭信号通过 Stop 显式传播，而不是依赖 HTTP 请求上下文。
	stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))
	state.stopSignal = stopSignal
	state.stopOnce = stop
	state.mu.Unlock()
	if err := s.recoverUnfinished(ctx); err != nil {
		return err
	}
	// 计划生成的现场同样在启动时收敛：停留在 planning 的任务不能仅凭状态就再次
	// 调用 Planner，必须先按持久化事实判断上一轮到底走到哪一步。
	return s.recoverPlanGeneration(ctx)
}

// Stop 取消在途工作流并等待其收敛。
//
// 停止后新的工作流请求直接返回错误；已经在推进的工作流会收到取消信号，当前步骤
// 由闭环服务收敛为中断或取消，执行记录被写入 blocked 或 recovery_required，
// 不会留下 running 残留。
func (s *TaskWorkflowService) Stop(ctx context.Context) error {
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
	timeout := defaultWorkflowStopTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("工作流编排器停止被取消: %w", ctx.Err())
	case <-time.After(timeout):
		return fmt.Errorf(
			"工作流编排器停止超时: 仍有 %d 个工作流未收敛",
			s.ActiveWorkflows(),
		)
	}
}

// beginRun 登记一次在途工作流，服务已停止时拒绝。
func (s *TaskWorkflowService) beginRun(ctx context.Context) (context.Context, func(), error) {
	state := &s.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stopped {
		return nil, nil, fmt.Errorf("%w: 不再接受新的工作流请求", ErrWorkflowStopped)
	}
	if !state.started {
		return nil, nil, fmt.Errorf("工作流编排器尚未启动")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrWorkflowStopped, err)
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
			cancelRun(ErrWorkflowStopped)
		case <-runCtx.Done():
		}
	}()
	return runCtx, finish, nil
}

// workflowLifecycle 记录后台服务的启动与停止状态。
type workflowLifecycle struct {
	mu      sync.Mutex
	started bool
	stopped bool
	// stopSignal 是停止信号，用于取消在途工作流。
	stopSignal context.Context
	// stopOnce 保证停止信号只关闭一次。
	stopOnce context.CancelFunc
	// runs 统计在途工作流，用于判断是否全部收敛。
	runs sync.WaitGroup
	// active 是在途工作流数量。
	active int
}

// activeCount 返回在途工作流数量。
func (l *workflowLifecycle) activeCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

// keyedTaskMutex 按任务标识提供互斥锁。
//
// 不同任务之间完全并发，同一任务上的编排串行化；无引用时自动回收条目，避免长时间
// 运行后无限增长。
type keyedTaskMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedTaskLock
}

// keyedTaskLock 是一个带引用计数的互斥锁。
type keyedTaskLock struct {
	mu   sync.Mutex
	refs int
}

// Lock 取得指定任务的锁，返回解锁函数。
func (m *keyedTaskMutex) Lock(key string) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*keyedTaskLock)
	}
	entry, ok := m.locks[key]
	if !ok {
		entry = &keyedTaskLock{}
		m.locks[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.mu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(m.locks, key)
		}
		m.mu.Unlock()
	}
}

// loadTask 读取未被逻辑删除的任务。
func (s *TaskWorkflowService) loadTask(ctx context.Context, id domain.ID) (domain.Task, error) {
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrWorkflowNotFound, id)
		}
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrWorkflowNotFound, id)
	}
	return *task, nil
}

// loadSteps 按序号升序读取任务的计划步骤。
func (s *TaskWorkflowService) loadSteps(ctx context.Context, taskID domain.ID) ([]domain.Step, error) {
	steps, err := s.store.ListStepsByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("读取计划步骤失败: %w", err)
	}
	return steps, nil
}

// appendEvent 追加任务事件。
//
// 事件写入失败只记录日志，不回滚已写入的执行状态：状态是唯一真相来源，事件用于
// 事后定位，两者不能因为一次写入失败而互相破坏。
func (s *TaskWorkflowService) appendEvent(
	ctx context.Context,
	taskID domain.ID,
	stepID, attemptID *domain.ID,
	eventType domain.EventType,
	payload map[string]any,
) {
	event, err := domain.NewEvent(
		taskID,
		stepID,
		attemptID,
		nil,
		eventType,
		marshalStepExecutionPayload(payload),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造工作流事件失败",
			slog.String("task_id", taskID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(ctx, event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入工作流事件失败",
			slog.String("task_id", taskID.String()),
			slog.String("event", string(eventType)),
			slog.String("error", errorSummary(err)),
		)
	}
}

// persistRun 以版本号为条件写入执行记录并推进本地版本号。
//
// 冲突说明数据库中的记录已被其他操作推进：调用方必须重新读取真实值，不能假定写入
// 成功，也不能用本地对象覆盖它。
func (s *TaskWorkflowService) persistRun(
	ctx context.Context,
	run domain.WorkflowRun,
) (domain.WorkflowRun, error) {
	if err := s.store.UpdateWorkflowRun(ctx, run); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			current, readErr := s.store.GetWorkflowRun(ctx, run.ID)
			if readErr != nil {
				return run, fmt.Errorf(
					"%w: 工作流执行记录已被其他操作推进，且重新读取失败: %v",
					ErrWorkflowConflict,
					readErr,
				)
			}
			return *current, fmt.Errorf(
				"%w: 工作流执行记录已被其他操作推进（当前状态 %s）",
				ErrWorkflowConflict,
				current.Status,
			)
		}
		return run, fmt.Errorf("写入工作流执行记录失败: %w", err)
	}
	run.Revision++
	return run, nil
}
