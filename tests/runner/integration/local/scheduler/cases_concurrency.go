// 多任务并发与执行权验收：迁移自
// tests/integration/local/scheduler/concurrency_acceptance_test.go。
//
// 覆盖同一服务端进程内的并发场景：并发扫描不超额分派、启动失败只释放自己的执行权、
// 数据库读取失败不伪造空队列、取消与任务推进并发、失败重试与调度竞争。
//
// 用例使用真实 SQLite、正式迁移、真实 app.TaskExecutionCoordinator / app.TaskScheduler /
// app.TaskRetryService / app.TaskService，只把统一启动端口（容器准备 / 计划生成 /
// 工作流启动）替换为受控替身。所有断言都读取进程内执行权登记与数据库里真实留下的任务
// 记录、执行记录与事件，而不是方法的返回值。
//
// 时序构造使用显式屏障与受控端口，不依赖 time.Sleep 猜测竞态窗口；唯一的轮询等待沿用
// 既有 waitFor（等待后台分派收敛），不用于判定竞态是否发生。
package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 原 TestSchedulerDoesNotExceedLimitUnderConcurrentScans 验证并发扫描不超额分派。
//
// 同一调度器的多次并发 Scan 会同时读到"还有空名额"：本进程在途分派必须计入占用，
// 否则并发扫描会把总分派数推过上限。
func runSchedulerDoesNotExceedLimitUnderConcurrentScans(t *testrunner.Context) {
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	const tasks = 6
	for index := 0; index < tasks; index++ {
		fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Duration(index)*time.Second))
	}
	block := make(chan struct{})
	recorder := &dispatchRecorder{
		workflowStarted: true,
		block:           block,
		started:         make(chan domain.ID, tasks),
	}
	// 名额统计与分派必须在同一个临界区内完成。为了让这个窗口确定性地被触发（而不是
	// 依赖 goroutine 调度运气），用受控的存储包装让第一次扫描在读完占用快照之后停在
	// 屏障上；此时其余扫描读到同一份空名额快照。只有串行化才能保证总量不超限。
	barrier := newScanBarrier(fixture.db)
	scheduler, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              barrier,
		Coordination:       fixture.coordinator,
		Launch:             recorder,
		MaxConcurrentTasks: 2,
		ScanInterval:       time.Hour,
		StopTimeout:        10 * time.Second,
		Logger:             fixture.logger.Slog(),
		Now:                func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造调度器失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := scheduler.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止调度器失败: %v", stopErr)
		}
	})

	// 不启动后台循环：只并发调用 Scan。
	const scanners = 4
	var wg sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < scanners; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if scanErr := scheduler.Scan(fixture.ctx); scanErr != nil &&
				!errors.Is(scanErr, app.ErrSchedulerStopped) {
				t.Errorf("并发扫描失败: %v", scanErr)
			}
		}()
	}
	close(start)
	// 第一次扫描停住后，给其余扫描足够时间推进：未串行化时它们会立刻基于同一份空名额
	// 快照分派，从而超限；串行化时它们阻塞在临界区外，不会有任何分派。
	barrier.waitArrived(t)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if recorder.launchedCount() > 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	barrier.release()
	wg.Wait()

	if count := recorder.launchedCount(); count > 2 {
		t.Fatalf("并发扫描不得让分派数超过上限 2，实际 %d", count)
	}
	close(block)
	waitFor(t, 10*time.Second, func() bool {
		return scheduler.ActiveDispatches() == 0
	}, "在途分派应收敛")
}

// scanBarrier 把**第一次**名额统计读取停在屏障上。
//
// 它只拦截 countOccupiedSlots 用的 ListTasks（调用方传入 ExcludeStatuses），
// 不拦截候选读取（调用方传入 Statuses），因此屏障精确落在"名额统计完成、分派尚未
// 开始"的窗口内。
//
// 语义：第一次名额统计读完后停住，其余扫描可以自由推进。若调度器没有把"统计名额 →
// 分派"放在同一临界区，其余扫描会读到同一份空名额快照并各自分派，总量必然超限；
// 只有串行化才会让它们阻塞在临界区外，等到第一次扫描收尾后重新读到已占用的名额。
type scanBarrier struct {
	app.TaskSchedulerStore
	once    sync.Once
	arrived chan struct{}
	gate    chan struct{}
}

// newScanBarrier 构造屏障包装。
func newScanBarrier(store app.TaskSchedulerStore) *scanBarrier {
	return &scanBarrier{
		TaskSchedulerStore: store,
		arrived:            make(chan struct{}),
		gate:               make(chan struct{}),
	}
}

// ListTasks 在首次名额统计读取之后停住，直到测试释放屏障。
func (b *scanBarrier) ListTasks(
	ctx context.Context,
	filter storage.TaskFilter,
) ([]domain.Task, error) {
	tasks, err := b.TaskSchedulerStore.ListTasks(ctx, filter)
	if err != nil || len(filter.Statuses) > 0 {
		return tasks, err
	}
	b.once.Do(func() {
		close(b.arrived)
		<-b.gate
	})
	return tasks, err
}

// waitArrived 等待第一次名额统计读取完成。
func (b *scanBarrier) waitArrived(t *testrunner.Context) {
	t.Helper()
	select {
	case <-b.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("等待名额统计读取超时")
	}
}

// release 释放屏障。
func (b *scanBarrier) release() {
	close(b.gate)
}

// 原 TestSchedulerReadFailureDoesNotFakeEmptyQueue 验证读取失败不被当成空队列。
//
// 候选读取失败时调度器必须返回错误：把读取失败当成"没有候选"会让运维误以为
// 队列已空，而实际是数据库不可用。
func runSchedulerReadFailureDoesNotFakeEmptyQueue(t *testrunner.Context) {
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now.Add(-time.Hour))
	recorder := &dispatchRecorder{workflowStarted: true}
	scheduler, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              &failingTaskListStore{TaskSchedulerStore: fixture.db},
		Coordination:       fixture.coordinator,
		Launch:             recorder,
		MaxConcurrentTasks: 2,
		ScanInterval:       time.Hour,
		StopTimeout:        10 * time.Second,
		Logger:             fixture.logger.Slog(),
		Now:                func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造调度器失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := scheduler.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止调度器失败: %v", stopErr)
		}
	})
	if scanErr := scheduler.Scan(fixture.ctx); scanErr == nil {
		t.Fatal("读取任务列表失败时扫描必须返回错误")
	}
	if recorder.launchedCount() != 0 {
		t.Fatal("读取失败时不得启动任何任务")
	}
	if fixture.coordinator.Held(task.ID) {
		t.Fatal("读取失败时不得取得任务执行权")
	}
}

// failingTaskListStore 让任务列表读取失败。
type failingTaskListStore struct {
	app.TaskSchedulerStore
}

// ListTasks 返回受控失败。
func (s *failingTaskListStore) ListTasks(
	ctx context.Context,
	filter storage.TaskFilter,
) ([]domain.Task, error) {
	return nil, errors.New("数据库读取失败")
}

// 原 TestSchedulerLaunchFailureReleasesOnlyOwnExecution 验证启动失败只释放自己的执行权。
//
// 现场：两个任务同时进入启动阶段，其中一个失败。失败任务释放自己的执行权，另一个
// 任务的执行权必须原样保留——释放他人的执行权会让并发任务失去所有权。
func runSchedulerLaunchFailureReleasesOnlyOwnExecution(t *testrunner.Context) {
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	failing := fixture.queuedTask(t, domain.PriorityHigh, base)
	healthy := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Second))
	block := make(chan struct{})
	recorder := &dispatchRecorder{
		workflowStarted: true,
		block:           block,
		started:         make(chan domain.ID, 4),
		failures:        map[domain.ID]error{failing.ID: errors.New("容器准备失败")},
	}
	scheduler := fixture.newScheduler(t, 2, recorder)
	// 两个任务都进入启动阶段后再放行：失败注入在阻塞之后生效。
	waitFor(t, 10*time.Second, func() bool {
		return recorder.launchedCount() == 2
	}, "两个任务都应进入启动阶段")
	close(block)
	waitFor(t, 10*time.Second, func() bool {
		return scheduler.ActiveDispatches() == 0
	}, "在途分派应收敛")

	if fixture.coordinator.Held(failing.ID) {
		t.Fatal("启动失败必须释放自己的执行权")
	}
	if !fixture.coordinator.Held(healthy.ID) {
		t.Fatal("另一个任务的执行权不得被释放")
	}
	// 失败任务不得被推进状态，也不得写入成功结论。
	if failedTask := fixture.task(t, failing.ID); failedTask.Status != domain.TaskStatusQueued {
		t.Fatalf("启动失败不得推进任务状态，实际 %s", failedTask.Status)
	}
}

// 原 TestCancelAndAdvanceRaceKeepsCancelIntent 验证取消与任务推进并发时取消意图不丢失。
//
// 现场：任务已被推进到 planned，同时用户请求取消。无论两者谁先落库，最终都必须
// 满足"取消意图已记录"且任务进入 cancelling，不允许取消意图被并发写入抹掉。
func runCancelAndAdvanceRaceKeepsCancelIntent(t *testrunner.Context) {
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now.Add(-time.Hour))
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	service, err := app.NewTaskService(app.TaskServiceOptions{Store: fixture.db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	cancelled, err := service.CancelTask(fixture.ctx, task.ID.String())
	if err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	if !cancelled.CancelRequested {
		t.Fatal("取消意图必须被持久化")
	}
	if cancelled.Status != domain.TaskStatusCancelling {
		t.Fatalf("推进中的任务取消后应进入 cancelling，实际 %s", cancelled.Status)
	}
	if cancelled.FinalResult != domain.TaskResultNone || cancelled.FinishedAt != nil {
		t.Fatalf("尚未确认停止时不得写入结论: %+v", cancelled)
	}
	if stored := fixture.task(t, task.ID); !stored.CancelRequested {
		t.Fatal("数据库里必须保留取消意图")
	}
}

// 原 TestRetryRacesWithSchedulerForSameTask 验证重试与调度器竞争同一任务只产生一个周期。
//
// 现场：失败任务同时收到"用户显式重试"与"调度器扫描"。调度器只领取 queued，因此
// 它不得领取失败任务；重试成功后任务进入新周期，调度器也不得再重复分派。
func runRetryRacesWithSchedulerForSameTask(t *testrunner.Context) {
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now.Add(-time.Hour))
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)

	retryRecorder := &retryRecorder{workflowStarted: true}
	retryService := fixture.newRetryService(t, retryRecorder)
	launchRecorder := &dispatchRecorder{workflowStarted: true}
	scheduler := fixture.newScheduler(t, 2, launchRecorder)

	// 先让调度器扫描一次：failed 任务不得被自动分派。
	if scanErr := scheduler.Scan(fixture.ctx); scanErr != nil {
		t.Fatalf("调度扫描失败: %v", scanErr)
	}
	if launchRecorder.launchedCount() != 0 {
		t.Fatalf("调度器不得分派 failed 任务，实际启动 %d 次", launchRecorder.launchedCount())
	}
	if fixture.coordinator.Held(task.ID) {
		t.Fatal("调度器不得给 failed 任务取得执行权")
	}
	// 用户显式重试：必须成功进入新周期，且只调用一次启动流程。
	if _, retryErr := retryService.Retry(
		fixture.ctx,
		task.ID.String(),
		app.TaskRetryRequest{},
	); retryErr != nil {
		t.Fatalf("显式重试失败: %v", retryErr)
	}
	if retryRecorder.count() != 1 {
		t.Fatalf("重试只允许调用一次启动流程，实际 %d 次", retryRecorder.count())
	}
	// 重试后再次扫描：任务已离开 failed，调度器不得重复分派。
	before := fixture.task(t, task.ID)
	if scanErr := scheduler.Scan(fixture.ctx); scanErr != nil {
		t.Fatalf("第二次调度扫描失败: %v", scanErr)
	}
	after := fixture.task(t, task.ID)
	if after.Status != before.Status {
		t.Fatalf("调度器不得推进重试中的任务: %s → %s", before.Status, after.Status)
	}
	if launchRecorder.launchedCount() != 0 {
		t.Fatal("调度器不得对重试中的任务重复启动")
	}
}
