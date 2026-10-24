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

// 调度器的错误类别与默认参数。
var (
	// ErrSchedulerInvalidConfig 表示调度器构造参数不合法。
	ErrSchedulerInvalidConfig = errors.New("app: 调度器配置非法")
	// ErrSchedulerStopped 表示调度器已停止，不再接受新的分派。
	ErrSchedulerStopped = errors.New("app: 调度器已停止")
)

const (
	// defaultSchedulerScanInterval 是默认的队列扫描间隔。
	//
	// 轮询而不是事件驱动：第一版需要的是"可证明正确"，而事件驱动会引入"事件丢失
	// 就永远不再扫描"的故障面。轮询的代价是空扫描，代价可控且可观测。
	defaultSchedulerScanInterval = time.Second
	// defaultSchedulerStopTimeout 是等待在途分派收敛的默认时限。
	defaultSchedulerStopTimeout = 30 * time.Second
)

// TaskSchedulerStore 是调度器依赖的只读事实来源与租约写入能力。
//
// 它只包含"读取候选所需的事实"与"领取 / 续租 / 释放"：调度器不推进任务状态、
// 不创建工作流执行记录，这些仍由 TaskLaunchService 与 TaskWorkflowService 承担。
type TaskSchedulerStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	// ListTasks 返回满足筛选条件的任务，用于扫描候选与统计名额占用。
	ListTasks(ctx context.Context, filter storage.TaskFilter) ([]domain.Task, error)
	GetRequirement(ctx context.Context, id domain.ID) (*domain.Requirement, error)
	GetActiveWorkflowRun(ctx context.Context, taskID domain.ID) (*domain.WorkflowRun, error)
	ListStepsByTask(ctx context.Context, taskID domain.ID) ([]domain.Step, error)
	ListCodeSaveRecordsByTask(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error)
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
	RenewTaskLease(ctx context.Context, request storage.TaskLeaseRenewRequest) error
}

// TaskSchedulerLaunchPort 是调度器对统一启动能力的需求。
//
// 调度器只调用已有的启动流程：它不准备容器、不初始化工作区、不生成计划、不执行
// 步骤、不保存代码。
type TaskSchedulerLaunchPort interface {
	Launch(ctx context.Context, request TaskLaunchRequest) (TaskLaunchResult, error)
}

// TaskSchedulerLeasePort 是调度器对任务租约能力的需求。
type TaskSchedulerLeasePort interface {
	Claim(ctx context.Context, request TaskClaimRequest) (TaskClaim, error)
	Renew(ctx context.Context, handle domain.TaskLeaseHandle) error
	Release(ctx context.Context, handle domain.TaskLeaseHandle) error
	RenewInterval() time.Duration
}

// TaskSchedulerOptions 是构造任务调度器的输入。
type TaskSchedulerOptions struct {
	// Store 是持久化入口，必填。
	Store TaskSchedulerStore
	// Leases 提供任务领取与租约维护，必填。
	Leases TaskSchedulerLeasePort
	// Launch 提供统一任务启动能力，必填。
	Launch TaskSchedulerLaunchPort
	// MaxConcurrentTasks 是并发上限，必须大于 0。
	//
	// 它直接来自 cfg.Scheduler.MaxConcurrentTasks，不新增第二套并发配置。
	MaxConcurrentTasks int
	// ScanInterval 是队列扫描间隔，零值使用默认值。
	ScanInterval time.Duration
	// StopTimeout 是等待在途分派收敛的时限，零值使用默认值。
	StopTimeout time.Duration
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
}

// TaskScheduler 是任务队列的有界并发调度器。
//
// 职责边界：
//   - 周期扫描 queued 任务，用 domain.DecideTaskQueueAdmission 判定候选资格；
//   - 用 domain.NewTaskQueueOrderKey / CompareTaskQueueOrder 排序候选；
//   - 用 domain.DecideTaskSlotState 统计真实持有执行权的任务，受配置上限约束；
//   - 领取成功后调用既有 TaskLaunchService.Launch，不重复实现任何启动阶段；
//   - 在途分派期间维持任务租约，并在结束时按真实结果释放。
//
// 它不创建、启动、删除或接管容器，不生成计划，不执行步骤，不保存代码，也不把
// "Launch 返回 nil"当作任务完成：任务结论只能来自持久化状态。
type TaskScheduler struct {
	store   TaskSchedulerStore
	leases  TaskSchedulerLeasePort
	launch  TaskSchedulerLaunchPort
	logger  *slog.Logger
	now     func() time.Time
	limit   int
	scan    time.Duration
	timeout time.Duration

	mu sync.Mutex
	// dispatched 记录本进程正在分派或启动中的任务。
	//
	// 它是第一道防线而不是一致性保证：跨进程仍由任务租约的条件更新拒绝重复领取。
	dispatched map[domain.ID]*schedulerDispatch
	started    bool
	stopped    bool
	stopSignal context.Context
	stopOnce   context.CancelFunc
	runs       sync.WaitGroup
	scanning   sync.WaitGroup
}

// schedulerDispatch 是一次在途分派。
type schedulerDispatch struct {
	handle domain.TaskLeaseHandle
	cancel context.CancelFunc
}

// NewTaskScheduler 构造任务调度器。
func NewTaskScheduler(options TaskSchedulerOptions) (*TaskScheduler, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("%w: 存储不能为空", ErrSchedulerInvalidConfig)
	}
	if options.Leases == nil {
		return nil, fmt.Errorf("%w: 任务租约服务不能为空", ErrSchedulerInvalidConfig)
	}
	if options.Launch == nil {
		return nil, fmt.Errorf("%w: 启动服务不能为空", ErrSchedulerInvalidConfig)
	}
	if options.MaxConcurrentTasks <= 0 {
		return nil, fmt.Errorf("%w: 并发上限必须大于 0", ErrSchedulerInvalidConfig)
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	scan := options.ScanInterval
	if scan <= 0 {
		scan = defaultSchedulerScanInterval
	}
	timeout := options.StopTimeout
	if timeout <= 0 {
		timeout = defaultSchedulerStopTimeout
	}
	return &TaskScheduler{
		store:      options.Store,
		leases:     options.Leases,
		launch:     options.Launch,
		logger:     logger,
		now:        now,
		limit:      options.MaxConcurrentTasks,
		scan:       scan,
		timeout:    timeout,
		dispatched: make(map[domain.ID]*schedulerDispatch),
	}, nil
}

// Name 返回后台服务名称。
func (s *TaskScheduler) Name() string {
	return "task_scheduler"
}

// Limit 返回并发上限。
func (s *TaskScheduler) Limit() int {
	return s.limit
}

// Start 启动调度循环。
//
// 只有数据库、任务租约、启动服务与工作流编排器都已成功启动后，Server 才会调用它；
// 因此恢复与前置收敛一定先于第一次扫描。
func (s *TaskScheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("调度器已经启动")
	}
	s.started = true
	stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.stopSignal = stopSignal
	s.stopOnce = stop
	s.mu.Unlock()

	s.runs.Add(1)
	go func() {
		defer s.runs.Done()
		s.loop(stopSignal)
	}()
	return nil
}

// Stop 取消调度扫描并等待在途分派收敛。
//
// 停止后不再领取新任务；在途分派的启动上下文被取消，由启动服务按真实结果收敛。
// 因服务停止而中断的启动**不释放租约**：无法确认远程操作是否已经发生，盲目释放会
// 让其他执行者立刻在未知现场上重放。租约按有效期自然过期，由 P2 的恢复语义处理。
func (s *TaskScheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	stop := s.stopOnce
	dispatches := make([]*schedulerDispatch, 0, len(s.dispatched))
	for _, dispatch := range s.dispatched {
		dispatches = append(dispatches, dispatch)
	}
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	for _, dispatch := range dispatches {
		dispatch.cancel()
	}
	done := make(chan struct{})
	go func() {
		s.runs.Wait()
		s.scanning.Wait()
		close(done)
	}()
	timeout := s.timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("调度器停止被取消: %w", ctx.Err())
	case <-time.After(timeout):
		return fmt.Errorf(
			"调度器停止超时: 仍有 %d 个任务在分派中",
			s.ActiveDispatches(),
		)
	}
}

// ActiveDispatches 返回本进程内正在分派的任务数量。
func (s *TaskScheduler) ActiveDispatches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dispatched)
}

// loop 是调度循环。
func (s *TaskScheduler) loop(ctx context.Context) {
	// 启动后立即扫描一次：新入队任务不应等待一个完整周期。
	s.runScan(ctx)
	ticker := time.NewTicker(s.scan)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runScan(ctx)
		}
	}
}

// runScan 执行一次扫描并等待其完成，保证停止时不会遗留扫描 goroutine。
func (s *TaskScheduler) runScan(ctx context.Context) {
	s.scanning.Add(1)
	defer s.scanning.Done()
	if err := s.Scan(ctx); err != nil && !errors.Is(err, ErrSchedulerStopped) {
		s.logger.WarnContext(ctx, "调度扫描失败", slog.String("error", errorSummary(err)))
	}
}

// Scan 执行一次完整的队列扫描与分派。
//
// 语义固定为：
//  1. 统计真实占用名额的任务，达到上限时不再领取；
//  2. 只读取 queued 任务作为候选，逐条调用领域准入判定；
//  3. 通过准入的候选按领域排序规则排序后依次尝试领取；
//  4. 领取成功后登记在途分派并异步启动，启动不阻塞后续候选。
//
// 读取失败不会伪造"可调度"：无法判断候选资格时跳过该候选，并记录日志，不让一个
// 有问题的候选阻塞整条队列。
func (s *TaskScheduler) Scan(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrSchedulerStopped
	}
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ErrSchedulerStopped
	}
	now := s.now()
	occupied, err := s.countOccupiedSlots(ctx, now)
	if err != nil {
		return err
	}
	available := s.limit - occupied
	if available <= 0 {
		return nil
	}
	candidates, err := s.loadCandidates(ctx, now)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if available <= 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return ErrSchedulerStopped
		}
		claimed, claimErr := s.dispatch(ctx, candidate, now)
		if claimErr != nil {
			if !errors.Is(claimErr, ErrTaskLeaseConflict) {
				s.logger.WarnContext(
					ctx,
					"调度器领取任务失败",
					slog.String("task_id", candidate.Task.ID.String()),
					slog.String("error", errorSummary(claimErr)),
				)
			}
			continue
		}
		if claimed {
			available--
		}
	}
	return nil
}

// schedulerCandidate 是通过准入的候选。
type schedulerCandidate struct {
	// Task 是候选任务。
	Task domain.Task
	// key 是领域排序键。
	key domain.TaskQueueOrderKey
	// input 是判定该候选时加载的持久化事实。
	//
	// 领取后必须用它重新仲裁：只带 Task 的空输入会让准入以 priority_unknown 拒绝，
	// 从而把一次真实领取误判为"现场已变化"。
	input domain.TaskQueueInput
}

// loadCandidates 读取 queued 任务并筛选出通过准入的候选，按领域排序规则排序。
func (s *TaskScheduler) loadCandidates(
	ctx context.Context,
	now time.Time,
) ([]schedulerCandidate, error) {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		Statuses: domain.AutomaticSchedulingStatuses(),
	})
	if err != nil {
		return nil, fmt.Errorf("读取调度候选失败: %w", err)
	}
	candidates := make([]schedulerCandidate, 0, len(tasks))
	for _, task := range tasks {
		input, inputErr := s.loadQueueInput(ctx, task, now)
		if inputErr != nil {
			// 无法判断候选资格时跳过：不得伪造"可调度"，也不让单个任务阻塞队列。
			s.logger.WarnContext(
				ctx,
				"调度器无法判断候选资格，跳过该任务",
				slog.String("task_id", task.ID.String()),
				slog.String("error", errorSummary(inputErr)),
			)
			continue
		}
		admission := domain.DecideTaskQueueAdmission(input)
		if !admission.Admitted {
			continue
		}
		key, keyErr := domain.NewTaskQueueOrderKey(input)
		if keyErr != nil {
			continue
		}
		candidates = append(candidates, schedulerCandidate{
			Task:  task,
			key:   key,
			input: input,
		})
	}
	keys := make([]domain.TaskQueueOrderKey, 0, len(candidates))
	index := make(map[domain.ID]int, len(candidates))
	for position, candidate := range candidates {
		keys = append(keys, candidate.key)
		index[candidate.Task.ID] = position
	}
	domain.SortTaskQueueOrderKeys(keys)
	sorted := make([]schedulerCandidate, 0, len(candidates))
	for _, key := range keys {
		sorted = append(sorted, candidates[index[key.TaskID]])
	}
	return sorted, nil
}

// loadQueueInput 读取计算队列准入与排序所需的持久化事实。
func (s *TaskScheduler) loadQueueInput(
	ctx context.Context,
	task domain.Task,
	now time.Time,
) (domain.TaskQueueInput, error) {
	input := domain.TaskQueueInput{Task: task, Now: now}
	requirement, err := s.store.GetRequirement(ctx, task.RequirementID)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return domain.TaskQueueInput{}, fmt.Errorf("读取需求失败: %w", err)
		}
		// 需求缺失时留空：领域准入会以 priority_unknown 拒绝，不会伪造优先级。
	} else {
		input.Requirement = requirement
	}
	steps, err := s.store.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return domain.TaskQueueInput{}, fmt.Errorf("读取计划步骤失败: %w", err)
	}
	input.Steps = steps
	run, err := s.store.GetActiveWorkflowRun(ctx, task.ID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return domain.TaskQueueInput{}, fmt.Errorf("读取执行记录失败: %w", err)
	}
	if err == nil {
		input.ActiveWorkflowRun = run
	}
	records, err := s.store.ListCodeSaveRecordsByTask(ctx, task.ID.String())
	if err != nil {
		return domain.TaskQueueInput{}, fmt.Errorf("读取代码保存记录失败: %w", err)
	}
	input.CodeSaveRecords = records
	return input, nil
}

// countOccupiedSlots 统计真实占用调度名额的任务数量。
//
// 事实来源是数据库而不是内存：领取后尚未建立 WorkflowRun 的窗口由任务租约表达
// （TaskSlotClaimed），执行阶段由执行记录租约表达。本进程正在分派的任务也计入，
// 避免同一进程的并发扫描超额领取。
//
// 说明：这是**基于数据库事实的进程级上限**。多个服务进程共享同一数据库时，任务租约
// 能防止同一任务被重复领取，但不提供跨进程的全局容量仲裁：两个进程可能各自看到
// 同一份占用快照后各领一个任务，使总量短暂超过上限。全局配额需要数据库仲裁，属于
// 后续里程碑。
func (s *TaskScheduler) countOccupiedSlots(ctx context.Context, now time.Time) (int, error) {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		ExcludeStatuses: []domain.TaskStatus{
			domain.TaskStatusCompleted,
			domain.TaskStatusCancelled,
		},
	})
	if err != nil {
		return 0, fmt.Errorf("统计调度名额失败: %w", err)
	}
	occupied := 0
	for _, task := range tasks {
		input := domain.TaskSlotInput{Task: task, Now: now}
		// 租约有效时判定不需要执行记录：省掉一次查询，也避免把"读取失败"当成
		// "没有执行记录"。
		if !task.LeaseActive(now) {
			run, runErr := s.store.GetActiveWorkflowRun(ctx, task.ID)
			if runErr != nil && !errors.Is(runErr, storage.ErrNotFound) {
				return 0, fmt.Errorf("读取任务 %s 的执行记录失败: %w", task.ID, runErr)
			}
			if runErr == nil {
				input.ActiveWorkflowRun = run
			}
		}
		if domain.DecideTaskSlotState(input).OccupiesSlot() {
			occupied++
		}
	}
	// 本进程已登记但尚未写回租约的分派同样占名额：它们即将成为 claimed。
	s.mu.Lock()
	for taskID := range s.dispatched {
		found := false
		for _, task := range tasks {
			if task.ID == taskID && task.LeaseActive(now) {
				found = true
				break
			}
		}
		if !found {
			occupied++
		}
	}
	s.mu.Unlock()
	return occupied, nil
}

// dispatch 领取并启动一个候选任务。
//
// 返回 claimed 表示本次真的取得了执行权并开始了启动；返回错误时不会留下"看起来
// 已领取"的状态。
func (s *TaskScheduler) dispatch(
	ctx context.Context,
	candidate schedulerCandidate,
	now time.Time,
) (bool, error) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return false, ErrSchedulerStopped
	}
	if _, exists := s.dispatched[candidate.Task.ID]; exists {
		// 本进程已经在分派同一任务：不重复领取。
		s.mu.Unlock()
		return false, nil
	}
	s.mu.Unlock()

	claim, err := s.leases.Claim(ctx, TaskClaimRequest{
		TaskID: candidate.Task.ID,
		Owner:  "scheduler-" + domain.NewID().String(),
		// 自动调度只领取 queued：不得依赖存储层的默认集合。
		Statuses: domain.AutomaticSchedulingStatuses(),
	})
	if err != nil {
		return false, err
	}
	// 领取后再次核验领域仲裁：并发场景下"领域允许"与"存储成功"之间可能有状态变化。
	// 输入沿用候选加载时的事实，只把任务替换为领取后回读的记录（其中已包含租约），
	// 因此准入判定与排序依据与领取前完全一致。
	input := candidate.input
	input.Task = claim.Task
	input.Now = now
	decision, decisionErr := domain.DecideTaskClaim(input, claim.Handle.Owner)
	if decisionErr != nil || !decision.Allowed {
		// 领域判定拒绝说明现场已经变化：释放自己刚拿到的租约并如实返回。
		if releaseErr := s.leases.Release(context.WithoutCancel(ctx), claim.Handle); releaseErr != nil {
			return false, fmt.Errorf(
				"%w: 领取后领域判定拒绝且释放租约失败: %v",
				ErrTaskLeaseConflict,
				releaseErr,
			)
		}
		return false, nil
	}
	if !s.register(candidate.Task.ID, claim.Handle) {
		// 停止或已有分派：释放刚取得的租约，避免占住名额。
		if releaseErr := s.leases.Release(context.WithoutCancel(ctx), claim.Handle); releaseErr != nil {
			return false, fmt.Errorf(
				"%w: 登记分派失败且释放租约失败: %v",
				ErrTaskLeaseConflict,
				releaseErr,
			)
		}
		return false, ErrSchedulerStopped
	}
	s.appendEvent(ctx, candidate.Task.ID, domain.EventTypeTaskClaimed, map[string]any{
		"owner":      claim.Handle.Owner,
		"epoch":      claim.Handle.Epoch,
		"takeover":   claim.Takeover,
		"claimed_at": claim.ClaimedAt.Format(time.RFC3339Nano),
		"expires_at": claim.ExpiresAt.Format(time.RFC3339Nano),
		"source":     "scheduler",
	})
	s.runs.Add(1)
	go s.run(claim)
	return true, nil
}

// register 登记一次在途分派，返回是否登记成功。
//
// 登记只记录租约身份：取消函数由 run 在创建启动上下文后写回，Stop 与 run 之间
// 通过同一个 map 条目共享该函数。
func (s *TaskScheduler) register(
	taskID domain.ID,
	handle domain.TaskLeaseHandle,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	if _, exists := s.dispatched[taskID]; exists {
		return false
	}
	s.dispatched[taskID] = &schedulerDispatch{handle: handle}
	return true
}

// setDispatchCancel 为已登记的分派写回取消函数。
func (s *TaskScheduler) setDispatchCancel(
	taskID domain.ID,
	cancel context.CancelFunc,
) {
	s.mu.Lock()
	if dispatch, ok := s.dispatched[taskID]; ok {
		dispatch.cancel = cancel
	}
	s.mu.Unlock()
}

// unregister 注销一次在途分派。
func (s *TaskScheduler) unregister(taskID domain.ID) {
	s.mu.Lock()
	dispatch, ok := s.dispatched[taskID]
	if ok {
		delete(s.dispatched, taskID)
	}
	s.mu.Unlock()
	if ok && dispatch.cancel != nil {
		dispatch.cancel()
	}
}

// run 执行一次分派：在租约有效期内调用既有启动流程，并按真实结果收尾。
func (s *TaskScheduler) run(claim TaskClaim) {
	defer s.runs.Done()
	defer s.unregister(claim.Handle.TaskID)

	base := s.stopSignal
	if base == nil {
		base = context.Background()
	}
	runCtx, cancel := context.WithCancel(base)
	s.setDispatchCancel(claim.Handle.TaskID, cancel)
	defer cancel()

	// 启动阶段可能持续数十分钟：租约必须在整个过程中保持有效，否则任务会在准备
	// 过程中被其他执行者接管。
	stopHeartbeat := s.startLeaseHeartbeat(runCtx, claim.Handle)
	result, launchErr := s.launch.Launch(runCtx, TaskLaunchRequest{
		TaskID: claim.Handle.TaskID.String(),
		Grant: WorkflowExecutionGrant{
			Lease: claim.Handle,
			TTL:   s.leases.RenewInterval() * 3,
		},
	})
	stopHeartbeat()

	if launchErr != nil {
		s.settleFailedDispatch(context.WithoutCancel(runCtx), claim.Handle, result, launchErr)
		return
	}
	// 启动成功不等于任务完成：工作流执行记录在后台推进，名额由它自己占用。
	// 只有确实没有启动工作流时才由调度器释放租约。
	if !result.WorkflowStarted {
		if releaseErr := s.leases.Release(context.WithoutCancel(runCtx), claim.Handle); releaseErr != nil {
			s.logger.WarnContext(
				runCtx,
				"启动未建立工作流时释放任务租约失败，名额将受租约有效期约束",
				slog.String("task_id", claim.Handle.TaskID.String()),
				slog.String("error", errorSummary(releaseErr)),
			)
		}
		return
	}
	s.logger.InfoContext(
		context.WithoutCancel(runCtx),
		"调度器已启动任务工作流",
		slog.String("task_id", claim.Handle.TaskID.String()),
		slog.String("stage", result.Stage.String()),
		slog.String("status", string(result.TaskStatus)),
	)
}

// settleFailedDispatch 处理启动失败的分派。
//
// 语义固定为：
//   - 服务停止导致的取消不释放租约：无法确认远程操作是否已经发生，盲目释放会让其他
//     执行者在未知现场上重放，租约按有效期自然过期；
//   - 其他失败释放本次领取的租约，并记录真实失败阶段；释放失败必须如实报告，
//     不得伪造"名额已空出"。
func (s *TaskScheduler) settleFailedDispatch(
	ctx context.Context,
	handle domain.TaskLeaseHandle,
	result TaskLaunchResult,
	cause error,
) {
	if s.isServiceStop(cause) {
		s.logger.InfoContext(
			ctx,
			"调度器停止导致启动中断，保留租约等待自然过期",
			slog.String("task_id", handle.TaskID.String()),
			slog.String("stage", result.Stage.String()),
			slog.String("error", errorSummary(cause)),
		)
		s.appendEvent(ctx, handle.TaskID, domain.EventTypeTaskRecoveryRequired, map[string]any{
			"stage":       "scheduler_dispatch",
			"error_class": "scheduler_interrupted",
			"summary":     "服务停止中断了任务启动，租约保留至自然过期",
			"owner":       handle.Owner,
			"epoch":       handle.Epoch,
		})
		return
	}
	s.appendEvent(ctx, handle.TaskID, domain.EventTypeTaskFailed, map[string]any{
		"stage":         "scheduler_dispatch",
		"failure_stage": result.FailureStage,
		"summary":       errorSummary(cause),
		"owner":         handle.Owner,
		"epoch":         handle.Epoch,
	})
	if releaseErr := s.leases.Release(ctx, handle); releaseErr != nil {
		s.logger.WarnContext(
			ctx,
			"启动失败后释放任务租约失败，名额将受租约有效期约束",
			slog.String("task_id", handle.TaskID.String()),
			slog.String("error", errorSummary(releaseErr)),
		)
		return
	}
	s.logger.InfoContext(
		ctx,
		"启动失败后已释放任务租约",
		slog.String("task_id", handle.TaskID.String()),
		slog.String("failure_stage", result.FailureStage),
		slog.String("error", errorSummary(cause)),
	)
}

// isServiceStop 判断错误是否来自服务停止。
//
// 服务停止与用户取消必须区分：前者不是用户的决定，因此不能推进任务到取消，也不能
// 触发任何自动重试。
func (s *TaskScheduler) isServiceStop(err error) bool {
	return errors.Is(err, ErrTaskLaunchStopped) ||
		errors.Is(err, ErrWorkflowStopped) ||
		errors.Is(err, ErrStepExecutorStopped) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, ErrSchedulerStopped)
}

// startLeaseHeartbeat 在启动期间周期性续租，返回停止函数。
func (s *TaskScheduler) startLeaseHeartbeat(
	ctx context.Context,
	handle domain.TaskLeaseHandle,
) func() {
	interval := s.leases.RenewInterval()
	if interval <= 0 {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.leases.Renew(ctx, handle); err != nil {
					s.logger.WarnContext(
						ctx,
						"调度器任务租约续期失败，停止推进",
						slog.String("task_id", handle.TaskID.String()),
						slog.String("error", errorSummary(err)),
					)
					return
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// appendEvent 追加调度事件，失败只记录日志。
func (s *TaskScheduler) appendEvent(
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
			"构造调度事件失败",
			slog.String("task_id", taskID.String()),
			slog.String("event", string(eventType)),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(ctx, event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入调度事件失败",
			slog.String("task_id", taskID.String()),
			slog.String("event", string(eventType)),
			slog.String("error", errorSummary(err)),
		)
	}
}

// SchedulerStatus 是调度器的只读状态快照。
type SchedulerStatus struct {
	// Limit 是并发上限。
	Limit int
	// Active 是本进程正在分派的任务数量。
	Active int
}

// Status 返回调度器的只读状态快照。
func (s *TaskScheduler) Status() SchedulerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SchedulerStatus{
		Limit:  s.limit,
		Active: len(s.dispatched),
	}
}

// ensure interface satisfaction
var _ interface {
	Name() string
	Start(context.Context) error
	Stop(context.Context) error
} = (*TaskScheduler)(nil)
