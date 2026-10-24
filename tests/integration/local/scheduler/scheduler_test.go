// 调度器集成测试。
//
// 用例使用真实 SQLite、正式迁移、真实 app.TaskLeaseService 与真实 app.TaskScheduler，
// 只把统一启动端口（容器准备 / 计划生成 / 工作流启动）替换为受控替身：候选筛选、
// 领域准入与排序、原子领取、名额统计、租约维护与释放全部走生产代码，因此断言的是
// 数据库里真实留下的任务记录、租约与事件，而不是方法的返回值。
package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// schedulerFixture 是一套调度器测试现场。
type schedulerFixture struct {
	ctx       context.Context
	db        *sqlite.DB
	path      string
	now       time.Time
	logger    *logging.Logger
	leases    *app.TaskLeaseService
	workspace *testkit.Workspace
}

// newSchedulerFixture 在工作空间中创建数据库与租约服务。
func newSchedulerFixture(t *testing.T) *schedulerFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	workspace := testkit.New(t)
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	path := filepath.Join(workspace.Root(), "agentflow.db")
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %s: %v", path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	leases, err := app.NewTaskLeaseService(app.TaskLeaseOptions{
		Store:  db,
		Logger: logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造任务租约服务失败: %v", err)
	}
	return &schedulerFixture{
		ctx:       ctx,
		db:        db,
		path:      path,
		now:       time.Now().UTC(),
		logger:    logger,
		leases:    leases,
		workspace: workspace,
	}
}

// queuedTask 写入一个 queued 任务并返回它。
//
// 优先级决定调度顺序，创建时间与任务标识提供稳定次序；用例通过显式时间构造
// "同优先级先到先服务"的真实现场。
func (f *schedulerFixture) queuedTask(
	t *testing.T,
	priority domain.Priority,
	createdAt time.Time,
) domain.Task {
	t.Helper()
	requirement, err := domain.NewRequirement(
		"需求 "+string(priority),
		"需求正文",
		priority,
		createdAt,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := f.db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(
		requirement.ID,
		"ssh://git.example.com/team/repo",
		"main",
		"",
		createdAt,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	// 队列准入复用 CheckTaskLaunchStart：缺少执行节点、语言环境与角色会被拒绝。
	hostID := domain.NewID()
	task.HostID = &hostID
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   "planner",
		DeveloperRoleID: "developer",
		Verification: domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
	}
	if err := f.db.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return task
}

// task 读取任务的最新记录。
func (f *schedulerFixture) task(t *testing.T, id domain.ID) domain.Task {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, id)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *task
}

// leaseRecorder 是统一启动端口的受控替身。
//
// 它记录被启动的任务与顺序，并可按任务注入失败：调度器的候选筛选、领取、名额统计
// 与释放全部是生产代码，替身只替换"容器准备 / 计划生成 / 工作流启动"这一段。
type leaseRecorder struct {
	mu sync.Mutex
	// launched 是按调用顺序记录的任务标识。
	launched []domain.ID
	// failures 是按任务注入的错误。
	failures map[domain.ID]error
	// workflowStarted 决定替身是否声明工作流已启动。
	workflowStarted bool
	// block 非空时启动调用阻塞到它被关闭，用于构造"在途分派"现场。
	block chan struct{}
	// started 在每个启动调用开始时被关闭一次（用于同步用例）。
	started chan domain.ID
}

// Launch 记录一次启动并返回受控结果。
func (r *leaseRecorder) Launch(
	ctx context.Context,
	request app.TaskLaunchRequest,
) (app.TaskLaunchResult, error) {
	taskID := domain.ID(request.TaskID)
	r.mu.Lock()
	r.launched = append(r.launched, taskID)
	failure := r.failures[taskID]
	block := r.block
	started := r.started
	r.mu.Unlock()
	if started != nil {
		select {
		case started <- taskID:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return app.TaskLaunchResult{TaskID: taskID}, ctx.Err()
		}
	}
	if failure != nil {
		return app.TaskLaunchResult{
			TaskID:       taskID,
			Stage:        domain.TaskLaunchStageContainer,
			FailureStage: domain.TaskLaunchStageContainer.String(),
			Failure:      failure.Error(),
		}, failure
	}
	r.mu.Lock()
	startedWorkflow := r.workflowStarted
	r.mu.Unlock()
	return app.TaskLaunchResult{
		TaskID:          taskID,
		Stage:           domain.TaskLaunchStageWorkflow,
		TaskStatus:      domain.TaskStatusPlanned,
		WorkflowStarted: startedWorkflow,
	}, nil
}

// launchedOrder 返回启动顺序的快照。
func (r *leaseRecorder) launchedOrder() []domain.ID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.ID(nil), r.launched...)
}

// launchedCount 返回启动调用次数。
func (r *leaseRecorder) launchedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.launched)
}

// newScheduler 构造并启动一个真实调度器。
func (f *schedulerFixture) newScheduler(
	t *testing.T,
	limit int,
	launch app.TaskSchedulerLaunchPort,
) *app.TaskScheduler {
	t.Helper()
	scheduler, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              f.db,
		Leases:             f.leases,
		Launch:             launch,
		MaxConcurrentTasks: limit,
		// 扫描间隔拉长：Start 会立即扫描一次，之后不再自动扫描，用例可以显式
		// 调用 Scan 观察"名额释放后继续处理队列"这类跨扫描行为。
		ScanInterval: time.Hour,
		StopTimeout:  10 * time.Second,
		Logger:       f.logger.Slog(),
		Now:          func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatalf("构造调度器失败: %v", err)
	}
	if err := scheduler.Start(f.ctx); err != nil {
		t.Fatalf("启动调度器失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := scheduler.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止调度器失败: %v", stopErr)
		}
	})
	return scheduler
}

// waitFor 在超时前轮询条件，用于等待后台分派收敛。
func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", message)
}

// TestSchedulerDispatchesInQueueOrder 验证候选按优先级、创建时间与任务标识排序。
func TestSchedulerDispatchesInQueueOrder(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	// 先创建低优先级任务，再创建高优先级任务：领取顺序必须由优先级决定，
	// 而不是由创建顺序决定。
	low := fixture.queuedTask(t, domain.PriorityLow, base)
	normalLater := fixture.queuedTask(t, domain.PriorityNormal, base.Add(2*time.Second))
	normalEarlier := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Second))
	high := fixture.queuedTask(t, domain.PriorityHigh, base.Add(3*time.Second))

	recorder := &leaseRecorder{workflowStarted: true}
	fixture.newScheduler(t, 4, recorder)
	waitFor(t, 10*time.Second, func() bool {
		return recorder.launchedCount() == 4
	}, "四个候选都应被分派")
	// 断言领取顺序而不是启动顺序：启动在独立 goroutine 中执行，到达顺序取决于调度，
	// 而领取事件在分派路径上同步追加，因此事件顺序才是真实的调度顺序。
	order := fixture.claimOrder(t, []domain.ID{low.ID, normalLater.ID, normalEarlier.ID, high.ID})
	want := []domain.ID{high.ID, normalEarlier.ID, normalLater.ID, low.ID}
	for index, expected := range want {
		if order[index] != expected {
			t.Fatalf("第 %d 个领取任务不符: 期望 %s，实际 %s（完整顺序 %v）",
				index+1, expected, order[index], order)
		}
	}
}

// claimOrder 返回给定任务按领取事件先后排列的顺序。
func (f *schedulerFixture) claimOrder(t *testing.T, taskIDs []domain.ID) []domain.ID {
	t.Helper()
	type entry struct {
		id     int64
		taskID domain.ID
	}
	entries := make([]entry, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		events, err := f.db.ListEventsByTask(f.ctx, taskID, storage.EventFilter{})
		if err != nil {
			t.Fatalf("读取任务 %s 的事件失败: %v", taskID, err)
		}
		first := int64(0)
		for _, event := range events {
			if event.Type != domain.EventTypeTaskClaimed {
				continue
			}
			if first == 0 || event.ID < first {
				first = event.ID
			}
		}
		if first == 0 {
			t.Fatalf("任务 %s 没有领取事件", taskID)
		}
		entries = append(entries, entry{id: first, taskID: taskID})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })
	order := make([]domain.ID, 0, len(entries))
	for _, item := range entries {
		order = append(order, item.taskID)
	}
	return order
}

// TestSchedulerRespectsConcurrencyLimit 验证并发上限生效。
func TestSchedulerRespectsConcurrencyLimit(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	tasks := make([]domain.Task, 0, 4)
	for index := 0; index < 4; index++ {
		tasks = append(tasks, fixture.queuedTask(
			t,
			domain.PriorityNormal,
			base.Add(time.Duration(index)*time.Second),
		))
	}
	block := make(chan struct{})
	recorder := &leaseRecorder{
		workflowStarted: true,
		block:           block,
		started:         make(chan domain.ID, 8),
	}
	scheduler := fixture.newScheduler(t, 2, recorder)
	// 等待两个分派真的进入启动阶段：此时名额已被占满。
	received := 0
	for received < 2 {
		select {
		case <-recorder.started:
			received++
		case <-time.After(10 * time.Second):
			t.Fatalf("等待分派启动超时，已收到 %d 个", received)
		}
	}
	// 上限为 2 时第三次扫描不得再领取。
	if err := scheduler.Scan(fixture.ctx); err != nil {
		t.Fatalf("第二次调度扫描失败: %v", err)
	}
	if count := recorder.launchedCount(); count != 2 {
		t.Fatalf("并发上限为 2 时不得领取更多任务，实际 %d", count)
	}
	// 领取窗口占名额：两个任务都有有效租约，即使工作流尚未建立。
	occupied := 0
	for _, task := range tasks {
		stored := fixture.task(t, task.ID)
		if stored.LeaseActive(fixture.now) {
			occupied++
		}
	}
	if occupied != 2 {
		t.Fatalf("领取窗口必须占用名额，实际持有租约的任务 %d 个", occupied)
	}
	close(block)
	waitFor(t, 10*time.Second, func() bool {
		return scheduler.ActiveDispatches() == 0
	}, "在途分派应收敛")
}

// TestSchedulerResumesAfterSlotReleased 验证名额释放后继续处理队列。
func TestSchedulerResumesAfterSlotReleased(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	first := fixture.queuedTask(t, domain.PriorityHigh, base)
	second := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Second))

	recorder := &leaseRecorder{workflowStarted: true}
	scheduler := fixture.newScheduler(t, 1, recorder)
	waitFor(t, 10*time.Second, func() bool {
		return recorder.launchedCount() == 1
	}, "上限为 1 时应只分派一个任务")
	if recorder.launchedOrder()[0] != first.ID {
		t.Fatalf("应先分派高优先级任务，实际 %s", recorder.launchedOrder()[0])
	}
	// 第一个任务的工作流已经建立（替身声明已启动），但它的租约仍由工作流持有；
	// 显式释放它模拟"工作流结束"，名额随即释放。
	waitFor(t, 10*time.Second, func() bool {
		return scheduler.ActiveDispatches() == 0
	}, "首个分派应收敛")
	stored := fixture.task(t, first.ID)
	if !stored.LeaseActive(fixture.now) {
		// 替身声明工作流已启动，调度器不得提前释放租约。
		t.Fatal("工作流启动后调度器不得提前释放任务租约")
	}
	handle := domain.TaskLeaseHandle{
		TaskID: stored.ID,
		Owner:  stored.LeaseOwner,
		Epoch:  stored.LeaseEpoch,
	}
	if err := fixture.leases.Release(fixture.ctx, handle); err != nil {
		t.Fatalf("释放首个任务的租约失败: %v", err)
	}
	// 名额释放后下一次扫描应领取下一个候选。第一个任务的状态仍是 queued（替身没有
	// 推进状态），因此它依然通过准入并排在最前——这正是"先到先服务"的体现。
	if err := scheduler.Scan(fixture.ctx); err != nil {
		t.Fatalf("第二次扫描失败: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		return recorder.launchedCount() == 2
	}, fmt.Sprintf("名额释放后应继续处理队列（实际启动 %d 次，首个任务 %+v，第二个任务 %+v）",
		recorder.launchedCount(), fixture.task(t, first.ID), fixture.task(t, second.ID)))
	if recorder.launchedOrder()[1] != first.ID {
		t.Fatalf("释放名额后应再次分派队列首位任务，实际 %s", recorder.launchedOrder()[1])
	}
	_ = second
}

// TestSchedulerReleasesLeaseOnLaunchFailure 验证启动失败释放自己的租约。
func TestSchedulerReleasesLeaseOnLaunchFailure(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	failure := errors.New("容器准备失败")
	recorder := &leaseRecorder{
		workflowStarted: true,
		failures:        map[domain.ID]error{task.ID: failure},
	}
	scheduler := fixture.newScheduler(t, 2, recorder)
	waitFor(t, 10*time.Second, func() bool {
		return scheduler.ActiveDispatches() == 0 && recorder.launchedCount() == 1
	}, "失败分派应收敛")
	stored := fixture.task(t, task.ID)
	if stored.LeaseActive(fixture.now) {
		t.Fatalf("启动失败后必须释放租约，实际仍持有: %+v", stored)
	}
	if stored.LeaseOwner != "" {
		t.Fatalf("释放后不得留下 owner: %+v", stored)
	}
	// 失败必须留下可追溯事件，且不得把任务写成完成。
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	failed := false
	for _, event := range events {
		if event.Type == domain.EventTypeTaskFailed {
			failed = true
		}
	}
	if !failed {
		t.Fatal("启动失败必须追加可追溯的失败事件")
	}
	if stored.Status != domain.TaskStatusQueued {
		t.Fatalf("启动失败不得推进任务状态，实际 %s", stored.Status)
	}
}

// TestSchedulerKeepsLeaseOnStop 验证服务停止不释放无法确认的租约。
//
// 服务停止与用户取消不同：无法确认远程操作是否已经发生，盲目释放租约会让其他执行者
// 立刻在未知现场上重放，因此租约必须保留到自然过期。
func TestSchedulerKeepsLeaseOnStop(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	block := make(chan struct{})
	recorder := &leaseRecorder{
		workflowStarted: true,
		block:           block,
		started:         make(chan domain.ID, 4),
	}
	scheduler, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              fixture.db,
		Leases:             fixture.leases,
		Launch:             recorder,
		MaxConcurrentTasks: 1,
		ScanInterval:       time.Hour,
		StopTimeout:        5 * time.Second,
		Logger:             fixture.logger.Slog(),
		Now:                func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造调度器失败: %v", err)
	}
	if err := scheduler.Start(fixture.ctx); err != nil {
		t.Fatalf("启动调度器失败: %v", err)
	}
	select {
	case <-recorder.started:
	case <-time.After(10 * time.Second):
		t.Fatal("等待分派启动超时")
	}
	// 服务停止：在途启动被取消，但租约必须保留到自然过期——无法确认远程操作是否
	// 已经发生，盲目释放会让其他执行者在未知现场上重放。
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := scheduler.Stop(stopCtx); err != nil {
		t.Fatalf("停止调度器失败: %v", err)
	}
	close(block)
	stored := fixture.task(t, task.ID)
	if !stored.LeaseActive(fixture.now) {
		t.Fatalf("服务停止后不得释放无法确认的租约: %+v", stored)
	}
	// 停止后不再领取新任务。
	second := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Second))
	if scanErr := scheduler.Scan(fixture.ctx); !errors.Is(scanErr, app.ErrSchedulerStopped) {
		t.Fatalf("停止后的扫描必须返回已停止，实际 %v", scanErr)
	}
	if after := fixture.task(t, second.ID); after.LeaseOwner != "" {
		t.Fatal("停止后不得领取新任务")
	}
}

// TestSchedulerSkipsFailedAndPausedTasks 验证非 queued 任务不会被自动领取。
func TestSchedulerSkipsFailedAndPausedTasks(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	queued := fixture.queuedTask(t, domain.PriorityNormal, base)
	failedTask := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Second))
	pausedTask := fixture.queuedTask(t, domain.PriorityNormal, base.Add(2*time.Second))

	for _, item := range []struct {
		task domain.Task
		path []domain.TaskStatus
	}{
		{task: failedTask, path: []domain.TaskStatus{domain.TaskStatusFailed}},
		{
			task: pausedTask,
			path: []domain.TaskStatus{
				domain.TaskStatusPreparing,
				domain.TaskStatusPlanning,
				domain.TaskStatusPaused,
			},
		},
	} {
		stored := fixture.task(t, item.task.ID)
		previous := stored.Status
		for _, hop := range item.path {
			if err := stored.Transition(hop, fixture.now); err != nil {
				t.Fatalf("迁移任务状态失败: %v", err)
			}
		}
		if err := fixture.db.UpdateTaskIfStatus(
			fixture.ctx,
			stored,
			[]domain.TaskStatus{previous},
		); err != nil {
			t.Fatalf("写入任务状态失败: %v", err)
		}
	}

	recorder := &leaseRecorder{workflowStarted: true}
	fixture.newScheduler(t, 4, recorder)
	waitFor(t, 10*time.Second, func() bool {
		return recorder.launchedCount() == 1
	}, "只有 queued 任务应被分派")
	if recorder.launchedOrder()[0] != queued.ID {
		t.Fatalf("被分派的任务不符: %s", recorder.launchedOrder()[0])
	}
	// 非 queued 任务不得被写入任何租约。
	for _, item := range []domain.Task{failedTask, pausedTask} {
		if stored := fixture.task(t, item.ID); stored.LeaseOwner != "" {
			t.Fatalf("状态 %s 的任务不得被自动领取: %+v", stored.Status, stored)
		}
	}
}

// TestTwoSchedulersCannotClaimSameTask 验证两个调度器实例不会重复领取同一任务。
func TestTwoSchedulersCannotClaimSameTask(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)

	// 两个调度器共享同一数据库，但各自持有独立连接：领取互斥必须来自数据库。
	other, err := sqlite.Open(fixture.ctx, sqlite.Options{Path: fixture.path})
	if err != nil {
		t.Fatalf("打开第二个数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := other.Close(); closeErr != nil {
			t.Errorf("关闭第二个连接失败: %v", closeErr)
		}
	})
	otherLeases, err := app.NewTaskLeaseService(app.TaskLeaseOptions{
		Store:  other,
		Logger: fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造第二个租约服务失败: %v", err)
	}
	block := make(chan struct{})
	firstRecorder := &leaseRecorder{
		workflowStarted: true,
		block:           block,
		started:         make(chan domain.ID, 4),
	}
	secondRecorder := &leaseRecorder{workflowStarted: true}
	first := fixture.newScheduler(t, 1, firstRecorder)
	second, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              other,
		Leases:             otherLeases,
		Launch:             secondRecorder,
		MaxConcurrentTasks: 1,
		ScanInterval:       time.Hour,
		StopTimeout:        10 * time.Second,
		Logger:             fixture.logger.Slog(),
		Now:                func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造第二个调度器失败: %v", err)
	}
	// 第二个调度器不启动后台循环：Start 会立即扫描一次，可能抢在第一个之前领取，
	// 使用例退化成"谁先启动谁赢"的时序竞争。这里只显式调用 Scan，验证的仍然是
	// 数据库层的领取互斥。
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := second.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止第二个调度器失败: %v", stopErr)
		}
	})

	select {
	case <-firstRecorder.started:
	case <-time.After(10 * time.Second):
		t.Fatal("等待第一个调度器分派超时")
	}
	// 第二个调度器此时扫描：租约已被第一个持有，不得重复领取。
	if err := second.Scan(fixture.ctx); err != nil {
		t.Fatalf("第二个调度器扫描失败: %v", err)
	}
	if count := secondRecorder.launchedCount(); count != 0 {
		t.Fatalf("同一任务不得被第二个调度器重复领取，实际启动 %d 次", count)
	}
	close(block)
	waitFor(t, 10*time.Second, func() bool {
		return first.ActiveDispatches() == 0
	}, "首个分派应收敛")
	stored := fixture.task(t, task.ID)
	if stored.LeaseEpoch != 1 {
		t.Fatalf("同一任务只应领取一次，实际代次 %d", stored.LeaseEpoch)
	}
}

// TestSchedulerLeaseEventIsTraceable 验证调度领取留下可追溯事件。
func TestSchedulerLeaseEventIsTraceable(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	recorder := &leaseRecorder{workflowStarted: true}
	fixture.newScheduler(t, 1, recorder)
	waitFor(t, 10*time.Second, func() bool {
		return recorder.launchedCount() == 1
	}, "任务应被分派")
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	claimed := 0
	for _, event := range events {
		if event.Type == domain.EventTypeTaskClaimed {
			claimed++
		}
	}
	if claimed < 1 {
		t.Fatal("调度领取必须留下领取事件")
	}
	stored := fixture.task(t, task.ID)
	if stored.LeaseEpoch < 1 {
		t.Fatalf("领取后必须写入代次，实际 %d", stored.LeaseEpoch)
	}
}

// TestSchedulerCountsExistingActiveTasks 验证已有活跃任务占用名额。
func TestSchedulerCountsExistingActiveTasks(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	active := fixture.queuedTask(t, domain.PriorityHigh, base)
	waiting := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Second))
	// 让已有任务持有有效租约：它占用一个名额，即使本进程没有在分派它。
	handle, err := fixture.db.AcquireTaskLease(fixture.ctx, storage.TaskLeaseRequest{
		TaskID:   active.ID,
		Owner:    "external-worker",
		TTL:      time.Hour,
		Statuses: []domain.TaskStatus{domain.TaskStatusQueued},
		At:       fixture.now,
	})
	if err != nil {
		t.Fatalf("预置租约失败: %v", err)
	}
	if handle.Epoch != 1 {
		t.Fatalf("预置租约代次应为 1，实际 %d", handle.Epoch)
	}
	recorder := &leaseRecorder{workflowStarted: true}
	fixture.newScheduler(t, 1, recorder)
	// 给首次扫描留出完成时间：名额已被外部持有者占用，不得领取第二个任务。
	time.Sleep(200 * time.Millisecond)
	if count := recorder.launchedCount(); count != 0 {
		t.Fatalf("名额被占用时不得领取新任务，实际启动 %d 次", count)
	}
	if stored := fixture.task(t, waiting.ID); stored.LeaseOwner != "" {
		t.Fatalf("名额耗尽时不得写入租约: %+v", stored)
	}
}

// TestSchedulerSkipsTaskWithActiveWorkflowRun 验证存在未结束执行记录时不重复领取。
func TestSchedulerSkipsTaskWithActiveWorkflowRun(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	// 写入一条未结束的执行记录：队列准入必须拒绝该候选。
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现步骤",
		"按需求实现",
		"",
		domain.Role{
			ID:           "developer",
			Name:         "开发",
			Type:         domain.RoleTypeDeveloper,
			Runner:       "pi",
			Model:        "group/free",
			SystemPrompt: "开发提示词",
			Enabled:      true,
		},
		task.Config.Verification,
		domain.RetryConfig{MaxAttempts: 1},
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := fixture.db.CreateSteps(fixture.ctx, []domain.Step{step}); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	run, err := domain.NewWorkflowRun(task.ID, []domain.Step{step}, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	recorder := &leaseRecorder{workflowStarted: true}
	fixture.newScheduler(t, 2, recorder)
	// 给后台分派留出机会：如果错误地领取了，这里会观察到启动调用。
	time.Sleep(200 * time.Millisecond)
	if count := recorder.launchedCount(); count != 0 {
		t.Fatalf("存在未结束执行记录时不得领取，实际启动 %d 次", count)
	}
}

// TestSchedulerRequiresPositiveLimit 验证并发上限必须为正。
func TestSchedulerRequiresPositiveLimit(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	_, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              fixture.db,
		Leases:             fixture.leases,
		Launch:             &leaseRecorder{},
		MaxConcurrentTasks: 0,
	})
	if !errors.Is(err, app.ErrSchedulerInvalidConfig) {
		t.Fatalf("并发上限为零时必须返回配置错误，实际 %v", err)
	}
}

// TestSchedulerStatusReportsLimit 验证状态快照报告真实上限。
func TestSchedulerStatusReportsLimit(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	scheduler := fixture.newScheduler(t, 3, &leaseRecorder{})
	status := scheduler.Status()
	if status.Limit != 3 {
		t.Fatalf("状态上限不符: %d", status.Limit)
	}
	if status.Active != 0 {
		t.Fatalf("空闲时不应有在途分派: %d", status.Active)
	}
	if fmt.Sprintf("%s", scheduler.Name()) != "task_scheduler" {
		t.Fatalf("服务名称不符: %s", scheduler.Name())
	}
}
