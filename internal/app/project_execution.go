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
	"github.com/lingting/agentflow/internal/storage"
)

// 本文件实现项目级执行协调：开始、停止、状态查询与项目内任务顺序。
//
// 职责边界（与调度器严格分工）：
//   - 本服务负责项目执行权的取得 / 停止意图 / 项目状态投影 / 项目内顺序判定；
//   - TaskScheduler 仍负责全局并发名额、任务执行权与统一启动，不重复实现任何启动阶段；
//   - 任何启动都经既有 TaskLaunchService，本服务不准备容器、不生成计划、不执行步骤；
//   - 任务是否完成只读数据库中的 Task / WorkflowRun 事实，不看接口返回或 Agent 结果。
//
// 并发保证分两层，缺一不可：
//   - 进程内按项目串行化"开始 / 停止"（同一 Server 进程）；
//   - 执行权的取得 / 续租 / 释放由带 owner + epoch 条件的 SQL 完成，迟到写入被拒绝。

// 项目执行协调的稳定错误，调用方通过 errors.Is 判断类别。
var (
	// ErrProjectExecutionInvalidInput 表示项目执行请求的参数非法。
	ErrProjectExecutionInvalidInput = errors.New("app: 项目执行参数非法")
	// ErrProjectExecutionNotFound 表示项目不存在或已被逻辑删除。
	ErrProjectExecutionNotFound = errors.New("app: 项目不存在")
	// ErrProjectExecutionBusy 表示项目正在执行，或已存在另一份执行权。
	ErrProjectExecutionBusy = errors.New("app: 项目正在执行")
	// ErrProjectExecutionNotRunning 表示项目当前没有在执行，停止请求没有对象。
	ErrProjectExecutionNotRunning = errors.New("app: 项目当前没有执行")
	// ErrProjectExecutionNoTasks 表示项目没有可执行任务。
	ErrProjectExecutionNoTasks = errors.New("app: 项目没有可执行任务")
	// ErrProjectExecutionBlocked 表示项目被前序任务阻塞，需要人工处置。
	ErrProjectExecutionBlocked = errors.New("app: 项目被前序任务阻塞")
	// ErrProjectExecutionPrerequisite 表示任务的启动前置条件未满足。
	ErrProjectExecutionPrerequisite = errors.New("app: 项目执行前置条件未满足")
	// ErrProjectExecutionUnavailable 表示调度器未装配：取得执行权也没有执行者领取任务。
	//
	// 它避免让项目进入"看起来在执行、实际没有分派能力"的状态。
	ErrProjectExecutionUnavailable = errors.New("app: 项目执行服务未就绪")
)

const (
	// defaultProjectExecutionLeaseTTL 是项目执行权的默认有效期。
	//
	// TTL 只需覆盖"一次扫描与一次启动"的时间：续租周期是它的三分之一，因此进程被强制
	// 终止后最多一个 TTL 就会允许重新取得执行权，而正常运行时不会因为短暂停顿而丢权。
	defaultProjectExecutionLeaseTTL = 2 * time.Minute
	// defaultProjectExecutionRenewInterval 是默认续租与收敛周期。
	defaultProjectExecutionRenewInterval = 40 * time.Second
)

// ProjectTaskCancelPort 是项目停止执行时使用的既有任务取消入口。
//
// 项目执行协调不直接改写任务状态：取消只会记录取消请求并推进到 cancelling，真实停止由
// 当前执行者确认，收敛结果仍由既有工作流路径决定。
type ProjectTaskCancelPort interface {
	CancelTask(ctx context.Context, taskID string) (domain.Task, error)
}

// ProjectExecutionScanTrigger 触发一次全局调度扫描。
//
// 开始项目执行后立即扫描一次，使刚获得执行权的项目不必等待一个完整扫描周期；扫描失败
// 不影响执行权归属，周期扫描会继续重试。
type ProjectExecutionScanTrigger interface {
	Scan(ctx context.Context) error
}

// ProjectExecutionServiceOptions 是构造项目执行协调服务的输入。
type ProjectExecutionServiceOptions struct {
	// Store 是持久化入口，必填。
	Store storage.Store
	// Canceler 是既有任务取消入口，必填。
	//
	// 停止项目执行时只调用它，不直接写任务状态。
	Canceler ProjectTaskCancelPort
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
	// Owner 是执行权持有者标识，为空时按进程生成。
	Owner string
	// LeaseTTL 是执行权有效期，零值使用默认值。
	LeaseTTL time.Duration
	// RenewInterval 是续租与收敛周期，零值使用默认值；必须小于 LeaseTTL。
	RenewInterval time.Duration
}

// ProjectTaskView 是项目内一个任务连同所属需求与执行位置的只读视图。
type ProjectTaskView struct {
	// Task 是项目内的正式任务。
	Task domain.Task
	// Requirement 是任务所属需求。
	Requirement domain.Requirement
	// Position 是任务在项目内的执行位置，从 1 开始。
	Position int
	// Blocking 表示该任务是当前阻塞项目的任务。
	Blocking bool
	// BlockingReason 是阻塞原因的稳定取值。
	BlockingReason string
}

// ProjectExecution 是项目执行状态的只读结果。
type ProjectExecution struct {
	// Project 是项目登记信息。
	Project domain.Project
	// Progress 是项目执行状态与进度统计。
	Progress domain.ProjectProgress
	// Tasks 是按执行顺序排列的项目内任务。
	Tasks []ProjectTaskView
	// State 是持久化的执行意图记录，没有记录时为空。
	State *storage.ProjectExecutionState
}

// ProjectExecutionService 协调项目级执行。
//
// 它不持有任务执行权，也不调用统一启动服务：领取任务仍然由调度器完成，本服务只约束
// "哪个任务有资格被领取"与"项目当前处于什么状态"。
type ProjectExecutionService struct {
	store    storage.Store
	canceler ProjectTaskCancelPort
	logger   *slog.Logger
	now      func() time.Time
	owner    string
	ttl      time.Duration
	renew    time.Duration

	// projects 串行化同一项目的开始与停止请求；不同项目完全并发。
	projects keyedMutex
	// reconcileMu 串行化执行权收敛，避免周期收敛与扫描收敛同时释放同一份执行权。
	reconcileMu sync.Mutex

	mu     sync.Mutex
	scan   ProjectExecutionScanTrigger
	closed bool
}

// NewProjectExecutionService 构造项目执行协调服务。
func NewProjectExecutionService(
	options ProjectExecutionServiceOptions,
) (*ProjectExecutionService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("%w: 存储不能为空", ErrProjectExecutionInvalidInput)
	}
	if options.Canceler == nil {
		return nil, fmt.Errorf("%w: 任务取消入口不能为空", ErrProjectExecutionInvalidInput)
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	owner := strings.TrimSpace(options.Owner)
	if owner == "" {
		// 持有者是进程实例标识：它与业务数据无关，只用于区分不同进程的执行权。
		owner = "server-" + domain.NewID().String()
	}
	if _, err := domain.NormalizeProjectExecutionOwner(owner); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProjectExecutionInvalidInput, err)
	}
	ttl := options.LeaseTTL
	if ttl <= 0 {
		ttl = defaultProjectExecutionLeaseTTL
	}
	renew := options.RenewInterval
	if renew <= 0 {
		renew = defaultProjectExecutionRenewInterval
	}
	if renew >= ttl {
		return nil, fmt.Errorf(
			"%w: 续租周期 %s 必须小于执行权有效期 %s",
			ErrProjectExecutionInvalidInput,
			renew,
			ttl,
		)
	}
	return &ProjectExecutionService{
		store:    options.Store,
		canceler: options.Canceler,
		logger:   logger,
		now:      now,
		owner:    owner,
		ttl:      ttl,
		renew:    renew,
	}, nil
}

// Owner 返回本进程的执行权持有者标识。
func (s *ProjectExecutionService) Owner() string {
	return s.owner
}

// AttachScanTrigger 绑定全局调度扫描入口。
//
// 绑定发生在服务装配阶段（调度器构造之后）：项目执行协调不持有调度器的启动流程，只
// 请求它"现在扫描一次"。
func (s *ProjectExecutionService) AttachScanTrigger(trigger ProjectExecutionScanTrigger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.scan = trigger
}

// Close 拒绝后续的开始请求，并解除已绑定的扫描入口。
//
// 它只影响本服务的内存状态：执行权按 TTL 到期，正常停止路径由 Keeper 负责释放。
func (s *ProjectExecutionService) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.scan = nil
}

// scanAttached 判断是否已经绑定调度扫描入口。
func (s *ProjectExecutionService) scanAttached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scan != nil
}

// Start 开始项目执行：核验现场、取得执行权并触发一次全局扫描。
//
// 顺序固定为：
//  1. 校验项目存在且未被逻辑删除；
//  2. 读取项目内正式任务（排除已删除任务），按领域规则排序并判定队首；
//  3. 拒绝"已有任务在执行""被前序任务阻塞""没有可执行任务"三类现场；
//  4. 核验队首任务的启动前置条件（复用自动调度准入规则，不重复实现）；
//  5. 原子取得项目执行权（同一持有者重复开始是幂等的，不产生第二份执行权）；
//  6. 清除停止意图并触发一次扫描；
//  7. 返回真实的项目执行状态。
func (s *ProjectExecutionService) Start(
	ctx context.Context,
	projectIDValue string,
) (ProjectExecution, error) {
	projectID, err := s.parseProjectID(projectIDValue)
	if err != nil {
		return ProjectExecution{}, err
	}
	if !s.scanAttached() {
		// 没有调度能力时取得执行权只会让项目停在"正在执行"却无人领取任务的状态。
		return ProjectExecution{}, fmt.Errorf(
			"%w: 调度器未装配，项目执行不会领取任何任务",
			ErrProjectExecutionUnavailable,
		)
	}
	unlock := s.projects.Lock(projectID.String())
	defer unlock()

	project, facts, requirementCount, err := s.loadProject(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	now := s.now()
	plan := domain.DecideProjectExecutionPlan(domainFacts(facts))
	state, err := s.loadExecutionState(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	lease, err := s.loadLease(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	if lease != nil && lease.HeldByOwner(s.owner) && lease.ActiveAt(now) && !state.StopRequested() {
		// 同一持有者的重复开始是幂等的：不递增 epoch，也不产生第二份执行权。
		s.triggerScan(ctx)
		return s.executionOf(ctx, project, requirementCount, facts, lease, state, now)
	}
	if plan.Counts.Running > 0 {
		return ProjectExecution{}, fmt.Errorf(
			"%w: 项目 %s 内已有任务在执行，等待其真实收敛后才能重新开始",
			ErrProjectExecutionBusy,
			projectID,
		)
	}
	switch plan.HeadState {
	case domain.ProjectTaskHeadClaimable:
	case domain.ProjectTaskHeadBlocked:
		return ProjectExecution{}, fmt.Errorf(
			"%w: 项目 %s 的队首任务需要人工处置：%s（%s）",
			ErrProjectExecutionBlocked,
			projectID,
			plan.BlockedReason,
			taskIDText(plan.BlockedTaskID),
		)
	default:
		return ProjectExecution{}, fmt.Errorf(
			"%w: 项目 %s 没有待执行任务（已完成 %d / 共 %d）",
			ErrProjectExecutionNoTasks,
			projectID,
			plan.Counts.Completed,
			plan.Counts.Total,
		)
	}
	if err := s.checkClaimPrerequisite(ctx, plan); err != nil {
		return ProjectExecution{}, err
	}
	if _, err := s.acquireLease(ctx, projectID, lease, now); err != nil {
		return ProjectExecution{}, err
	}
	running := storage.ProjectExecutionState{
		ProjectID: projectID,
		State:     domain.ProjectExecutionStateRunning,
		UpdatedAt: now,
	}
	if err := s.store.SetProjectExecutionState(ctx, running); err != nil {
		return ProjectExecution{}, fmt.Errorf("写入项目执行意图失败: %w", err)
	}
	s.triggerScan(ctx)
	acquired, err := s.loadLease(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	return s.executionOf(ctx, project, requirementCount, facts, acquired, &running, now)
}

// Cancel 请求停止项目执行：记录停止意图，并对在途任务调用既有取消入口。
//
// 语义固定为：
//   - 停止意图先落库，因此调度器下一次扫描就不会再领取该项目的新任务；
//   - 在途任务只经既有取消入口推进（cancelling → 真实收敛），不直接改写状态；
//   - 没有在途任务时立即释放执行权并把意图记为 paused；
//   - 有在途任务时保留执行权，等任务真实收敛后由收敛流程释放；
//   - 取消不删除任务、不删除执行记录、不回滚 Git，也不影响其他项目。
func (s *ProjectExecutionService) Cancel(
	ctx context.Context,
	projectIDValue string,
) (ProjectExecution, error) {
	projectID, err := s.parseProjectID(projectIDValue)
	if err != nil {
		return ProjectExecution{}, err
	}
	unlock := s.projects.Lock(projectID.String())
	defer unlock()

	project, facts, requirementCount, err := s.loadProject(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	now := s.now()
	plan := domain.DecideProjectExecutionPlan(domainFacts(facts))
	state, err := s.loadExecutionState(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	lease, err := s.loadLease(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	if plan.Counts.Running == 0 && (lease == nil || !lease.ActiveAt(now)) && state.StopRequested() {
		// 重复停止是幂等的：已经停止且没有在途任务，如实返回当前状态。
		return s.executionOf(ctx, project, requirementCount, facts, lease, state, now)
	}
	if plan.Counts.Running == 0 && (lease == nil || !lease.ActiveAt(now)) {
		return ProjectExecution{}, fmt.Errorf(
			"%w: 项目 %s 当前没有执行中的任务",
			ErrProjectExecutionNotRunning,
			projectID,
		)
	}
	stopRequestedAt := now
	stopping := storage.ProjectExecutionState{
		ProjectID:       projectID,
		State:           domain.ProjectExecutionStateStopping,
		StopRequestedAt: &stopRequestedAt,
		UpdatedAt:       now,
	}
	if err := s.store.SetProjectExecutionState(ctx, stopping); err != nil {
		return ProjectExecution{}, fmt.Errorf("写入项目停止意图失败: %w", err)
	}
	state = &stopping
	activeTaskID := firstActiveTask(plan)
	if activeTaskID != nil {
		if _, err := s.canceler.CancelTask(ctx, activeTaskID.String()); err != nil {
			return ProjectExecution{}, fmt.Errorf(
				"请求取消项目 %s 的在途任务 %s 失败: %w",
				projectID,
				activeTaskID,
				err,
			)
		}
		// 在途任务仍在收敛：执行权保留，等待真实终态。
		return s.executionOf(ctx, project, requirementCount, facts, lease, state, now)
	}
	if err := s.releaseLease(ctx, projectID, lease, now); err != nil {
		return ProjectExecution{}, err
	}
	paused := storage.ProjectExecutionState{
		ProjectID:       projectID,
		State:           domain.ProjectExecutionStatePaused,
		StopRequestedAt: &stopRequestedAt,
		UpdatedAt:       now,
	}
	if err := s.store.SetProjectExecutionState(ctx, paused); err != nil {
		return ProjectExecution{}, fmt.Errorf("写入项目停止意图失败: %w", err)
	}
	return s.executionOf(ctx, project, requirementCount, facts, nil, &paused, now)
}

// Execution 读取项目执行状态，不产生任何写操作。
func (s *ProjectExecutionService) Execution(
	ctx context.Context,
	projectIDValue string,
) (ProjectExecution, error) {
	projectID, err := s.parseProjectID(projectIDValue)
	if err != nil {
		return ProjectExecution{}, err
	}
	project, facts, requirementCount, err := s.loadProject(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	state, err := s.loadExecutionState(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	lease, err := s.loadLease(ctx, projectID)
	if err != nil {
		return ProjectExecution{}, err
	}
	return s.executionOf(ctx, project, requirementCount, facts, lease, state, s.now())
}

// ListTasks 按项目内执行顺序返回正式任务，不产生任何写操作。
func (s *ProjectExecutionService) ListTasks(
	ctx context.Context,
	projectIDValue string,
) ([]ProjectTaskView, error) {
	projectID, err := s.parseProjectID(projectIDValue)
	if err != nil {
		return nil, err
	}
	_, facts, _, err := s.loadProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.taskViews(facts), nil
}

// acquireLease 取得项目执行权，冲突时映射为"项目正在执行"。
func (s *ProjectExecutionService) acquireLease(
	ctx context.Context,
	projectID domain.ID,
	existing *domain.ProjectExecutionLease,
	now time.Time,
) (domain.ProjectExecutionLease, error) {
	expiresAt := now.Add(s.ttl)
	if _, err := domain.DecideProjectLeaseAcquire(existing, s.owner, now, expiresAt); err != nil {
		if errors.Is(err, domain.ErrProjectLeaseHeld) {
			return domain.ProjectExecutionLease{}, fmt.Errorf(
				"%w: %w",
				ErrProjectExecutionBusy,
				err,
			)
		}
		return domain.ProjectExecutionLease{}, fmt.Errorf(
			"%w: %w",
			ErrProjectExecutionInvalidInput,
			err,
		)
	}
	lease, err := s.store.AcquireProjectExecutionLease(
		ctx,
		projectID,
		s.owner,
		now,
		expiresAt,
	)
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return domain.ProjectExecutionLease{}, fmt.Errorf(
				"%w: 项目 %s 的执行权已被其他执行者持有: %w",
				ErrProjectExecutionBusy,
				projectID,
				err,
			)
		}
		return domain.ProjectExecutionLease{}, fmt.Errorf("取得项目执行权失败: %w", err)
	}
	s.logger.InfoContext(
		ctx,
		"已取得项目执行权",
		slog.String("project_id", projectID.String()),
		slog.Int64("epoch", lease.Epoch),
		slog.Time("expires_at", lease.ExpiresAt),
	)
	return lease, nil
}

// checkClaimPrerequisite 核验队首任务的启动前置条件。
//
// 复用自动调度准入规则：项目执行不能因为"点了一次开始"就绕过 V1 已有的启动准入。
func (s *ProjectExecutionService) checkClaimPrerequisite(
	ctx context.Context,
	plan domain.ProjectExecutionPlan,
) error {
	if plan.HeadIndex < 0 || plan.HeadIndex >= len(plan.Tasks) {
		return fmt.Errorf("%w: 项目没有可领取的任务", ErrProjectExecutionNoTasks)
	}
	fact := plan.Tasks[plan.HeadIndex]
	requirement := fact.Requirement
	input := domain.TaskQueueInput{Task: fact.Task, Requirement: &requirement}
	steps, err := s.store.ListStepsByTask(ctx, fact.Task.ID)
	if err != nil {
		return fmt.Errorf("读取任务计划步骤失败: %w", err)
	}
	input.Steps = steps
	run, err := s.store.GetActiveWorkflowRun(ctx, fact.Task.ID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("读取任务执行记录失败: %w", err)
	}
	if err == nil {
		input.ActiveWorkflowRun = run
	}
	records, err := s.store.ListCodeSaveRecordsByTask(ctx, fact.Task.ID.String())
	if err != nil {
		return fmt.Errorf("读取任务代码保存记录失败: %w", err)
	}
	input.CodeSaveRecords = records
	admission := domain.DecideTaskQueueAdmission(input)
	if admission.Admitted {
		return nil
	}
	detail := admission.Summary()
	if admission.Prerequisite != nil {
		detail = detail + "：" + admission.Prerequisite.Error()
	}
	return fmt.Errorf(
		"%w: 任务 %s 不能开始执行：%s",
		ErrProjectExecutionPrerequisite,
		fact.Task.ID,
		detail,
	)
}

// triggerScan 触发一次全局调度扫描。
//
// 扫描失败不改变执行权归属：周期扫描会继续尝试，接口如实返回当前状态。
func (s *ProjectExecutionService) triggerScan(ctx context.Context) {
	s.mu.Lock()
	trigger := s.scan
	s.mu.Unlock()
	if trigger == nil {
		return
	}
	if err := trigger.Scan(ctx); err != nil {
		s.logger.WarnContext(
			ctx,
			"项目执行未触发调度扫描",
			slog.String("error", errorSummary(err)),
		)
	}
}
