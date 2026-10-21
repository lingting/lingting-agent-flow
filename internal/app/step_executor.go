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
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 单步骤执行接口的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。
var (
	// ErrStepExecutionNotFound 表示任务或步骤不存在，或步骤不属于指定任务。
	ErrStepExecutionNotFound = errors.New("app: 步骤执行目标不存在")
	// ErrStepExecutionInvalidInput 表示执行请求参数不合法。
	ErrStepExecutionInvalidInput = errors.New("app: 步骤执行请求参数非法")
	// ErrStepExecutionConflict 表示当前状态不允许开始执行，或步骤已被占用。
	ErrStepExecutionConflict = errors.New("app: 步骤执行状态冲突")
	// ErrStepExecutionPrerequisite 表示执行所需的前置条件尚未满足，
	// 例如任务没有受管理的运行中容器，或执行节点未通过环境预检。
	ErrStepExecutionPrerequisite = errors.New("app: 步骤执行前置条件未满足")
)

// 单步骤执行的默认参数。
const (
	// defaultStepExecutionTimeout 是未配置超时时的单次 Agent 调用超时。
	//
	// 它与任务级 agent_timeout_seconds 的区别是"没有任务配置可用"时的兜底值：
	// 任何情况下都不允许一次调用无限期地占用步骤。
	defaultStepExecutionTimeout = 30 * time.Minute
	// maxStepExecutionPrompt 是提示词长度上限。
	maxStepExecutionPrompt = 64 << 10
)

// StepExecuteRequest 是通过接口发起一次单步骤执行的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方只能指定任务、步骤与可选的执行参数，
// 不能指定 Session 路径、本地文件路径或命令行参数。
type StepExecuteRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// StepID 是待执行的步骤标识。
	StepID string
	// Prompt 是可选的附加提示词。
	//
	// 为空时由编排器按任务目标与步骤目标生成；非空时仅作为补充说明追加在后面，
	// 不能替换编排器生成的基础上下文。
	Prompt string
	// Timeout 是本次调用的超时时间，零值使用任务配置或默认值。
	Timeout time.Duration
}

// StepExecutionResult 是一次单步骤执行的结果。
//
// 它如实反映本次 Agent 调用：outcome 为 completed 只代表进程正常退出，
// 不表示步骤验收通过，也不表示任务成功。
type StepExecutionResult struct {
	TaskID    domain.ID
	StepID    domain.ID
	AttemptID domain.ID
	SessionID domain.ID
	// AttemptNumber 是本次尝试在步骤内的序号。
	AttemptNumber int
	// Outcome 是本次执行结论。
	Outcome domain.StepExecutionOutcome
	// Summary 是面向调用方的结果摘要。
	Summary string
	// Failure 是可诊断的失败原因，成功时为空。
	Failure string
	// ExitCode 是 Agent 进程退出码，未能取得时为 -1。
	ExitCode int
	// SessionStatus 是会话执行状态。
	SessionStatus domain.SessionStatus
	// SessionRunStatus 是会话自身的运行结果。
	SessionRunStatus domain.SessionRunStatus
	// ArchiveStatus 是原始会话文件的归档状态。
	ArchiveStatus domain.SessionArchiveStatus
	// ExportStatus 是 HTML 导出状态。
	ExportStatus domain.SessionExportStatus
	// AttemptStatus 是写入后的尝试状态。
	AttemptStatus domain.AttemptStatus
	// StepStatus 是写入后的步骤状态。
	StepStatus domain.StepStatus
	// TaskStatus 是写入后的任务状态。
	TaskStatus domain.TaskStatus
	// Finalized 表示最终状态是否全部写入成功。
	//
	// 为 false 时 FinalizationErrors 给出原因：执行事实仍然如实返回，
	// 但数据库中的记录可能不完整，不会被当作成功吞掉。
	Finalized          bool
	FinalizationErrors []string
	StartedAt          time.Time
	FinishedAt         time.Time
}

// StepExecutorStepRepository 是执行协调器需要的步骤读写能力。
type StepExecutorStepRepository interface {
	GetStep(ctx context.Context, id domain.ID) (*domain.Step, error)
	// ListStepsByTask 按序号返回任务的全部步骤，用于校验前置步骤是否已完成。
	ListStepsByTask(ctx context.Context, taskID domain.ID) ([]domain.Step, error)
	UpdateStepIfStatus(
		ctx context.Context,
		step domain.Step,
		expected []domain.StepStatus,
	) error
}

// StepExecutorTaskRepository 是执行协调器需要的任务读写能力。
type StepExecutorTaskRepository interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	UpdateTaskIfStatus(
		ctx context.Context,
		task domain.Task,
		expected []domain.TaskStatus,
	) error
}

// StepExecutorAttemptRepository 是执行协调器需要的尝试读写能力。
//
// UpdateAttemptIfStatus 的注入点是"执行已完成但状态写入失败"这条路径的可验证来源：
// 替身可以让它失败，从而验证业务代码是否如实报告而不是伪造成功。
type StepExecutorAttemptRepository interface {
	GetAttempt(ctx context.Context, id domain.ID) (*domain.Attempt, error)
	// UpdateAttempt 无条件写入尝试，用于补写执行前后的 Git 工作区状态快照。
	UpdateAttempt(ctx context.Context, attempt domain.Attempt) error
	UpdateAttemptIfStatus(
		ctx context.Context,
		attempt domain.Attempt,
		expected []domain.AttemptStatus,
	) error
}

// StepExecutorRoleRepository 是执行协调器需要的角色读取能力。
type StepExecutorRoleRepository interface {
	GetRole(ctx context.Context, id domain.ID) (*domain.Role, error)
}

// StepExecutorHostRepository 是执行协调器需要的执行节点读取能力。
//
// 它用于在执行前拒绝未通过环境预检的节点：判断使用统一的
// EnsureHostUsableForRemoteTask，不在这里复制第二套准入规则。
type StepExecutorHostRepository interface {
	GetExecutionHost(ctx context.Context, id domain.ID) (*domain.ExecutionHost, error)
}

// StepExecutionStore 是单步骤执行协调器依赖的存储能力组合。
//
// 它由 storage.Store 直接满足；测试也可以只实现用到的部分，构造"尝试创建失败"
// 这类真实失败路径而不改动业务代码。
type StepExecutionStore interface {
	StepExecutorStepRepository
	StepExecutorTaskRepository
	StepExecutorAttemptRepository
	StepExecutorRoleRepository
	StepExecutorHostRepository
	storage.StepExecutionStore
	// AppendEvent 追加任务事件。
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
}

// StepExecutionContainerPort 是单步骤执行对任务容器的需求。
//
// 容器只被核验与使用：协调器不会创建、启动、删除或接管容器，也不会在容器
// 不可用时静默回退到本机执行。
type StepExecutionContainerPort interface {
	// InspectContainer 核验容器当前状态。
	InspectContainer(ctx context.Context, request InspectContainerRequest) (ContainerDetail, error)
	// RunAgent 在受管理的任务容器内执行一次 Agent 调用。
	RunAgent(ctx context.Context, request ContainerAgentRequest) (ContainerAgentResult, error)
}

// StepExecutorOptions 是构造单步骤执行协调器的输入。
type StepExecutorOptions struct {
	// Store 是持久化入口，必填。
	Store StepExecutionStore
	// Containers 是容器核验与容器内 Agent 调用能力，必填。
	Containers StepExecutionContainerPort
	// Sessions 是会话服务，用于核验会话记录，必填。
	Sessions *SessionService
	// Git 是 Git 工作区状态采集能力，可选。
	//
	// 为空表示本次运行不采集代码状态：Attempt 的 Git 字段保持为空，执行本身不受
	// 影响。采集能力缺失不能被当作"工作区干净"。
	Git StepExecutionGitPort
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
	// DefaultTimeout 覆盖未配置超时时的默认调用超时。
	DefaultTimeout time.Duration
}

// StepExecutor 是单个 Step 的执行协调器。
//
// 职责边界：
//   - 校验任务、步骤、角色、容器的归属与状态，并以数据库条件更新原子占用步骤；
//   - 为每次真实执行创建新的 Attempt 与独立 Session；
//   - 只通过容器内 Agent 执行端口调用 Pi，不自行拼接命令；
//   - 把真实执行结果映射为 Attempt / Step / Task 的终态并持久化。
//
// 它不实现工作流编排：不生成计划、不串联多个步骤、不自动重试、不创建容器，
// 也不决定任务最终是否成功。
type StepExecutor struct {
	store          StepExecutionStore
	containers     StepExecutionContainerPort
	sessions       *SessionService
	git            StepExecutionGitPort
	logger         *slog.Logger
	now            func() time.Time
	defaultTimeout time.Duration
	// running 记录本进程内正在执行的步骤，用于快速拒绝重复请求。
	//
	// 它是第一道防线而不是一致性保证：真正的占用由数据库条件更新完成。
	running *stepExecutionRegistry
	// sequences 记录本进程内正在顺序执行的任务，用于拒绝同一任务的两个顺序执行
	// 请求交错推进。跨进程的并发仍由步骤占用的事务条件更新拒绝。
	sequences *stepExecutionRegistry
	// lifecycle 管理后台服务状态与在途执行，使 Server 停止时能取消并等待收敛。
	lifecycle lifecycleState
}

// NewStepExecutor 构造单步骤执行协调器。
func NewStepExecutor(options StepExecutorOptions) (*StepExecutor, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("步骤执行协调器初始化失败: 存储不能为空")
	}
	if options.Containers == nil {
		return nil, fmt.Errorf("步骤执行协调器初始化失败: 容器能力不能为空")
	}
	if options.Sessions == nil {
		return nil, fmt.Errorf("步骤执行协调器初始化失败: 会话服务不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	timeout := options.DefaultTimeout
	if timeout <= 0 {
		timeout = defaultStepExecutionTimeout
	}
	return &StepExecutor{
		store:          options.Store,
		containers:     options.Containers,
		sessions:       options.Sessions,
		git:            options.Git,
		logger:         logger,
		now:            now,
		defaultTimeout: timeout,
		running:        newStepExecutionRegistry(),
		sequences:      newStepExecutionRegistry(),
	}, nil
}

// ActiveExecutions 返回本进程内正在执行的步骤数量。
func (e *StepExecutor) ActiveExecutions() int {
	return e.running.len()
}

// Execute 执行一个明确指定的步骤。
//
// 执行是同步的：调用方一直等待到尝试进入终态，因此不存在"已经返回成功但执行
// 仍在后台"的中间语义。ctx 取消与服务停止都会传播到实际的 Pi 进程：前者收敛为
// 取消，后者收敛为中断，两者必须可区分。
func (e *StepExecutor) Execute(
	ctx context.Context,
	request StepExecuteRequest,
) (StepExecutionResult, error) {
	taskID, err := parseStepExecutionID("任务", request.TaskID)
	if err != nil {
		return StepExecutionResult{}, err
	}
	stepID, err := parseStepExecutionID("步骤", request.StepID)
	if err != nil {
		return StepExecutionResult{}, err
	}
	task, err := e.loadTask(ctx, taskID)
	if err != nil {
		return StepExecutionResult{}, err
	}
	step, err := e.loadStep(ctx, task, stepID)
	if err != nil {
		return StepExecutionResult{}, err
	}
	if err := domain.CheckStepExecutionStart(task, step); err != nil {
		return StepExecutionResult{}, fmt.Errorf("%w: %w", ErrStepExecutionConflict, err)
	}
	if err := e.checkPrerequisites(ctx, task, step); err != nil {
		return StepExecutionResult{}, err
	}
	role, err := e.loadRole(ctx, step)
	if err != nil {
		return StepExecutionResult{}, err
	}
	container, err := e.requireContainer(ctx, task)
	if err != nil {
		return StepExecutionResult{}, err
	}
	prompt, err := stepExecutionPrompt(task, step, request.Prompt)
	if err != nil {
		return StepExecutionResult{}, err
	}
	timeout := e.executionTimeout(task, request.Timeout)

	runCtx, finishRun, runErr := e.beginRun(ctx)
	if runErr != nil {
		return StepExecutionResult{}, runErr
	}
	defer finishRun()

	release, acquired := e.running.acquire(step.ID)
	if !acquired {
		return StepExecutionResult{}, fmt.Errorf(
			"%w: 步骤 %s 正在执行中",
			ErrStepExecutionConflict,
			step.ID,
		)
	}
	defer release()

	started, err := e.beginExecution(runCtx, task, step, container)
	if err != nil {
		return StepExecutionResult{}, err
	}
	// 执行前快照在占用之后采集：此时步骤已被本次执行占用，其他请求无法并发改动
	// 同一个工作区，因此"执行前"与"执行后"的差异确实来自本次调用。
	before := e.captureGitState(runCtx, task, container, "before", started.Attempt.ID)
	e.logger.InfoContext(
		runCtx,
		"步骤执行已开始",
		slog.String("task_id", task.ID.String()),
		slog.String("step_id", step.ID.String()),
		slog.String("attempt_id", started.Attempt.ID.String()),
		slog.Int("attempt", started.Attempt.AttemptNumber),
		slog.String("step_type", string(step.Type)),
		slog.String("role_id", role.ID.String()),
	)
	result := e.runAttempt(runCtx, runAttemptRequest{
		task:        started.Task,
		step:        started.Step,
		attempt:     started.Attempt,
		role:        role,
		container:   container,
		containerID: started.Attempt.ContainerID,
		dockerID:    started.Attempt.DockerID,
		prompt:      prompt,
		timeout:     timeout,
		gitBefore:   before,
	})
	result.AttemptNumber = started.Attempt.AttemptNumber
	return result, nil
}

// runAttemptRequest 是一次真实 Agent 调用的输入。
type runAttemptRequest struct {
	task        domain.Task
	step        domain.Step
	attempt     domain.Attempt
	role        domain.Role
	container   domain.Container
	containerID string
	dockerID    string
	prompt      string
	timeout     time.Duration
	// gitBefore 是执行前采集到的 Git 工作区状态，为空表示本次没有采集。
	gitBefore *domain.GitWorktreeState
}

// checkPrerequisites 校验指定步骤的全部前序步骤是否已完成。
//
// 规则只在 domain 中定义一次（domain.CheckStepPrerequisites）：这里只负责读取
// 同一任务的步骤列表并把拒绝原因映射为状态冲突。跳过未完成的前序步骤会让后续
// 步骤在错误的代码基础上运行，因此必须拒绝，而不是给出警告。
func (e *StepExecutor) checkPrerequisites(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
) error {
	steps, err := e.store.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("读取计划步骤失败: %w", err)
	}
	if len(steps) == 0 {
		// 没有其他步骤时不存在前置条件：单步骤计划同样可以执行。
		return nil
	}
	if err := domain.CheckStepPrerequisites(steps, step); err != nil {
		return fmt.Errorf("%w: %w", ErrStepExecutionConflict, err)
	}
	return nil
}

// captureGitState 采集一次 Git 工作区状态并写入执行尝试。
//
// 采集失败不会中断执行：Git 状态是诊断信息而不是执行前提，但失败必须被如实记录，
// 不能被当作"工作区干净"。写库失败同样只记录日志——执行事实不因诊断信息缺失而改变。
func (e *StepExecutor) captureGitState(
	ctx context.Context,
	task domain.Task,
	container domain.Container,
	phase string,
	attemptID domain.ID,
) *domain.GitWorktreeState {
	if e.git == nil {
		return nil
	}
	// 采集使用未取消的上下文：取消与超时恰恰是最需要"执行后现场"的场景，采集本身
	// 有独立的超时上限，不会无限期占用执行路径。
	inspectCtx := context.WithoutCancel(ctx)
	state, err := e.git.InspectWorktree(inspectCtx, GitWorktreeRequest{
		ContainerID: container.ID.String(),
		DockerID:    container.DockerID,
		WorkDir:     task.Config.EffectiveWorkDir(),
		TaskID:      task.ID.String(),
	})
	if err != nil {
		e.logger.WarnContext(
			ctx,
			"采集 Git 工作区状态失败",
			slog.String("task_id", task.ID.String()),
			slog.String("attempt_id", attemptID.String()),
			slog.String("phase", phase),
			slog.String("error", errorSummary(err)),
		)
	}
	if !state.StatusRead && state.StatusError == "" {
		state = failedGitState(err)
	}
	persistCtx := context.WithoutCancel(ctx)
	if writeErr := e.storeGitState(persistCtx, attemptID, phase, state); writeErr != nil {
		e.logger.WarnContext(
			persistCtx,
			"写入 Git 工作区状态失败",
			slog.String("task_id", task.ID.String()),
			slog.String("attempt_id", attemptID.String()),
			slog.String("phase", phase),
			slog.String("error", errorSummary(writeErr)),
		)
	}
	return &state
}

// storeGitState 把 Git 工作区状态写入执行尝试。
//
// 写入使用带状态前置条件的更新：尝试已经进入终态时不再改写，迟到的快照不能覆盖
// 已确认的记录。
func (e *StepExecutor) storeGitState(
	ctx context.Context,
	attemptID domain.ID,
	phase string,
	state domain.GitWorktreeState,
) error {
	attempt, err := e.store.GetAttempt(ctx, attemptID)
	if err != nil {
		return fmt.Errorf("读取执行尝试失败: %w", err)
	}
	if phase == "before" {
		if err := attempt.SetGitBefore(state); err != nil {
			return err
		}
	} else {
		if err := attempt.SetGitAfter(state); err != nil {
			return err
		}
	}
	if attempt.Status.IsTerminal() {
		// 终态记录仍允许补写诊断快照：它不改变执行结论，只补充可核验的事实。
		return e.store.UpdateAttempt(ctx, *attempt)
	}
	if err := e.store.UpdateAttemptIfStatus(
		ctx,
		*attempt,
		[]domain.AttemptStatus{domain.AttemptStatusRunning},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return nil
		}
		return fmt.Errorf("写入执行尝试失败: %w", err)
	}
	return nil
}

// loadTask 读取任务，不存在或已删除时返回"不存在"。
func (e *StepExecutor) loadTask(ctx context.Context, id domain.ID) (domain.Task, error) {
	task, err := e.store.GetTask(ctx, id)
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
func (e *StepExecutor) loadStep(
	ctx context.Context,
	task domain.Task,
	id domain.ID,
) (domain.Step, error) {
	step, err := e.store.GetStep(ctx, id)
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

// loadRole 读取并校验步骤关联的角色。
func (e *StepExecutor) loadRole(ctx context.Context, step domain.Step) (domain.Role, error) {
	expected := step.Type.AgentRole()
	stored, err := e.store.GetRole(ctx, step.RoleID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Role{}, fmt.Errorf(
				"%w: 步骤 %s 关联的角色 %s 不存在",
				ErrStepExecutionPrerequisite,
				step.ID,
				step.RoleID,
			)
		}
		return domain.Role{}, fmt.Errorf("读取角色失败: %w", err)
	}
	if stored.Type != expected {
		return domain.Role{}, fmt.Errorf(
			"%w: 角色 %s 的类型是 %s，但步骤 %s 需要 %s",
			ErrStepExecutionPrerequisite,
			stored.ID,
			stored.Type,
			step.ID,
			expected,
		)
	}
	if !stored.Enabled {
		return domain.Role{}, fmt.Errorf(
			"%w: 角色 %s 已被禁用",
			ErrStepExecutionPrerequisite,
			stored.ID,
		)
	}
	if strings.TrimSpace(stored.Model) == "" {
		return domain.Role{}, fmt.Errorf(
			"%w: 角色 %s 未配置模型",
			ErrStepExecutionPrerequisite,
			stored.ID,
		)
	}
	return *stored, nil
}

// requireContainer 核验执行节点与任务容器，确认可以在容器内执行命令。
//
// 本步骤不创建容器、不启动容器，也不回退到本机执行：前置条件缺失时返回明确
// 的可诊断错误，由用户先完成环境准备。节点准入沿用统一规则，因此未通过环境
// 预检或已被禁用的节点无法通过执行接口绕过校验。
func (e *StepExecutor) requireContainer(
	ctx context.Context,
	task domain.Task,
) (domain.Container, error) {
	if err := e.ensureHostUsable(ctx, task); err != nil {
		return domain.Container{}, err
	}
	containerID := strings.TrimSpace(task.ContainerID)
	if containerID == "" {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务 %s 还没有任务容器；请先完成环境准备（创建容器并获取代码）后再执行步骤",
			ErrStepExecutionPrerequisite,
			task.ID,
		)
	}
	detail, err := e.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: containerID,
		Sync:        true,
	})
	if err != nil {
		return domain.Container{}, fmt.Errorf(
			"%w: 核验任务容器失败: %w",
			ErrStepExecutionPrerequisite,
			err,
		)
	}
	if detail.Container.Detached {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 已脱离 AgentFlow 管理，拒绝自动接管",
			ErrStepExecutionPrerequisite,
			detail.Container.ID,
		)
	}
	if detail.Container.State == domain.ContainerStateRemoved {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 已被删除",
			ErrStepExecutionPrerequisite,
			detail.Container.ID,
		)
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 当前未运行，无法执行步骤",
			ErrStepExecutionPrerequisite,
			detail.Container.ID,
		)
	}
	if strings.TrimSpace(detail.Container.DockerID) == "" {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 缺少 Docker 容器标识",
			ErrStepExecutionPrerequisite,
			detail.Container.ID,
		)
	}
	return detail.Container, nil
}

// ensureHostUsable 校验执行节点是否允许执行真实远程操作。
//
// 任务没有绑定执行节点时跳过：该情况会在容器核验阶段以更具体的错误报告。
func (e *StepExecutor) ensureHostUsable(ctx context.Context, task domain.Task) error {
	if task.HostID == nil {
		return nil
	}
	host, err := e.store.GetExecutionHost(ctx, *task.HostID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf(
				"%w: 任务 %s 关联的执行节点 %s 不存在",
				ErrStepExecutionPrerequisite,
				task.ID,
				*task.HostID,
			)
		}
		return fmt.Errorf("读取执行节点失败: %w", err)
	}
	if err := EnsureHostUsableForRemoteTask(*host); err != nil {
		return fmt.Errorf("%w: %w", ErrStepExecutionPrerequisite, err)
	}
	return nil
}

// executionTimeout 返回本次调用的超时时间。
//
// 优先级固定为：请求显式给出 > 任务配置 > 协调器默认值。
func (e *StepExecutor) executionTimeout(task domain.Task, requested time.Duration) time.Duration {
	if requested > 0 {
		return requested
	}
	if seconds := task.Config.AgentTimeoutSeconds; seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return e.defaultTimeout
}

// beginExecution 占用步骤并创建执行尝试。
func (e *StepExecutor) beginExecution(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	container domain.Container,
) (storage.StepExecutionStart, error) {
	started, err := e.store.BeginStepExecution(ctx, storage.StepExecutionStartRequest{
		TaskID:             task.ID,
		StepID:             step.ID,
		ExpectedTaskStatus: task.Status,
		ExpectedStepStatus: step.Status,
		ContainerID:        container.ID.String(),
		DockerID:           container.DockerID,
		InputDigest:        digestStepExecution(task, step),
		At:                 e.now(),
	})
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return storage.StepExecutionStart{}, fmt.Errorf(
				"%w: 步骤 %s 已被其他执行占用或状态已变更",
				ErrStepExecutionConflict,
				step.ID,
			)
		}
		if errors.Is(err, storage.ErrNotFound) {
			return storage.StepExecutionStart{}, fmt.Errorf(
				"%w: 步骤 %s",
				ErrStepExecutionNotFound,
				step.ID,
			)
		}
		return storage.StepExecutionStart{}, fmt.Errorf("占用步骤失败: %w", err)
	}
	if _, err := e.appendEvent(ctx, started.Task, &started.Step.ID, &started.Attempt.ID, nil,
		domain.EventTypeAttemptStarted,
		map[string]any{
			"attempt":   started.Attempt.AttemptNumber,
			"step_type": string(started.Step.Type),
			"source":    "step_execute_api",
		},
	); err != nil {
		// 事件写入失败不回滚占用：执行记录本身已经持久化，缺失的只是时间线条目。
		e.logger.WarnContext(
			ctx,
			"记录执行开始事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("step_id", step.ID.String()),
			slog.String("attempt_id", started.Attempt.ID.String()),
			slog.String("error", err.Error()),
		)
	}
	return started, nil
}

// parseStepExecutionID 解析路径或请求中的标识。
func parseStepExecutionID(label, raw string) (domain.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: %s标识不能为空", ErrStepExecutionInvalidInput, label)
	}
	if strings.Contains(trimmed, "/") {
		return "", fmt.Errorf("%w: %s标识非法", ErrStepExecutionInvalidInput, label)
	}
	return domain.ID(trimmed), nil
}

// stepExecutionRegistry 记录本进程内正在执行的步骤。
//
// 它只用于快速拒绝同一进程内的重复请求与观测在途数量；数据库条件更新才是
// 唯一的一致性保证，跨进程并发同样会被拒绝。
type stepExecutionRegistry struct {
	mu      sync.Mutex
	entries map[domain.ID]struct{}
}

// newStepExecutionRegistry 构造执行登记表。
func newStepExecutionRegistry() *stepExecutionRegistry {
	return &stepExecutionRegistry{entries: make(map[domain.ID]struct{})}
}

// acquire 尝试登记一个正在执行的步骤。
func (r *stepExecutionRegistry) acquire(stepID domain.ID) (func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[domain.ID]struct{})
	}
	if _, exists := r.entries[stepID]; exists {
		return func() {}, false
	}
	r.entries[stepID] = struct{}{}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.entries, stepID)
	}, true
}

// len 返回正在执行的步骤数量。
func (r *stepExecutionRegistry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// stepExecutionRun 描述一次真实调用的原始结果。
type stepExecutionRun struct {
	result  agent.RunResult
	session domain.Session
	// runErr 是执行端口返回的错误，只用于诊断与日志。
	runErr error
}
