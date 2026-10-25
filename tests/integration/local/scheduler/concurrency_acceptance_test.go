// P3-M5 多任务并发与执行权验收测试。
//
// 本文件补齐 P3-M5 §4.1 与 §4.2 的验收场景：多调度器竞争名额、领取与启动之间的
// 部分失败、数据库读写失败不伪造结论、取消与任务推进并发、失败重试与执行权竞争。
//
// 用例使用真实 SQLite、正式迁移、真实 app.TaskLeaseService / app.TaskScheduler /
// app.TaskRetryService / app.TaskService，只把统一启动端口（容器准备 / 计划生成 /
// 工作流启动）替换为受控替身。所有断言都读取数据库里真实留下的任务记录、租约
// owner/epoch、执行记录与事件，而不是方法的返回值。
//
// 时序构造使用显式屏障与受控端口，不依赖 time.Sleep 猜测竞态窗口；唯一的轮询
// 等待沿用既有 waitFor（等待后台分派收敛），不用于判定竞态是否发生。
package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// openSecondConnection 在同一个数据库文件上再打开一条独立连接。
//
// 并发安全必须来自数据库：只有独立连接上的竞争才能证明条件更新真的生效。
func (f *schedulerFixture) openSecondConnection(t *testing.T) *sqlite.DB {
	t.Helper()
	other, err := sqlite.Open(f.ctx, sqlite.Options{Path: f.path})
	if err != nil {
		t.Fatalf("打开第二个数据库连接失败: %s: %v", f.path, err)
	}
	t.Cleanup(func() {
		if closeErr := other.Close(); closeErr != nil {
			t.Errorf("关闭第二个数据库连接失败: %v", closeErr)
		}
	})
	return other
}

// secondScheduler 在独立连接上构造一个不启动后台循环的调度器。
//
// 不调用 Start：Start 会立即扫描一次，可能抢在用例显式构造的时序之前领取任务，
// 使断言退化成"谁先启动谁赢"。用例改为显式调用 Scan 观察每一次领取。
func (f *schedulerFixture) secondScheduler(
	t *testing.T,
	db *sqlite.DB,
	limit int,
	launch app.TaskSchedulerLaunchPort,
) *app.TaskScheduler {
	t.Helper()
	leases, err := app.NewTaskLeaseService(app.TaskLeaseOptions{
		Store:  db,
		Logger: f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造第二个租约服务失败: %v", err)
	}
	scheduler, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              db,
		Leases:             leases,
		Launch:             launch,
		MaxConcurrentTasks: limit,
		ScanInterval:       time.Hour,
		StopTimeout:        10 * time.Second,
		Logger:             f.logger.Slog(),
		Now:                func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatalf("构造第二个调度器失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := scheduler.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止第二个调度器失败: %v", stopErr)
		}
	})
	return scheduler
}

// occupiedSlotCount 统计数据库事实里真实占用名额的任务数量。
//
// 名额判定复用领域规则 DecideTaskSlotState，因此本函数不会另建一套"什么算占用"。
func (f *schedulerFixture) occupiedSlotCount(t *testing.T) int {
	t.Helper()
	tasks, err := f.db.ListTasks(f.ctx, storage.TaskFilter{
		ExcludeStatuses: []domain.TaskStatus{
			domain.TaskStatusCompleted,
			domain.TaskStatusCancelled,
		},
	})
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	occupied := 0
	for _, task := range tasks {
		input := domain.TaskSlotInput{Task: task, Now: f.now}
		if !task.LeaseActive(f.now) {
			run, runErr := f.db.GetActiveWorkflowRun(f.ctx, task.ID)
			if runErr != nil && !errors.Is(runErr, storage.ErrNotFound) {
				t.Fatalf("读取执行记录失败: %v", runErr)
			}
			if runErr == nil {
				input.ActiveWorkflowRun = run
			}
		}
		if domain.DecideTaskSlotState(input).OccupiesSlot() {
			occupied++
		}
	}
	return occupied
}

// TestTwoSchedulersClaimDistinctTasksWithinCapacity 验证两个调度器竞争不同任务时的容量语义。
//
// 产品契约（P3-M2 §5.3 与本文档 §3）是**进程级**上限：并发上限统计的是
// "本进程看到的数据库占用快照 + 本进程在途分派"。因此本用例断言两件真实成立的事：
//
//  1. 两个调度器在同一份队列上不会领取同一个任务（跨进程互斥来自数据库条件更新）；
//  2. 两个进程各自的上限都成立，且总量不超过"进程数 × 上限"。
//
// 用例同时明确记录**未**断言的边界：总量可能大于单进程上限，这是已知取舍而不是
// 缺陷，跨进程全局配额需要数据库仲裁（P3-M5 §3 记为后续决策）。
func TestTwoSchedulersClaimDistinctTasksWithinCapacity(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	const tasks = 6
	taskIDs := make([]domain.ID, 0, tasks)
	for index := 0; index < tasks; index++ {
		task := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Duration(index)*time.Second))
		taskIDs = append(taskIDs, task.ID)
	}

	// 两个调度器各限 2：启动替身阻塞在启动阶段，名额因此停在"已领取"窗口。
	block := make(chan struct{})
	firstRecorder := &leaseRecorder{
		workflowStarted: true,
		block:           block,
		started:         make(chan domain.ID, tasks),
	}
	secondRecorder := &leaseRecorder{
		workflowStarted: true,
		block:           block,
		started:         make(chan domain.ID, tasks),
	}
	other := fixture.openSecondConnection(t)
	first := fixture.newScheduler(t, 2, firstRecorder)
	second := fixture.secondScheduler(t, other, 2, secondRecorder)

	// 第一个调度器完成一次扫描：领取到自己的上限 2。
	if err := first.Scan(fixture.ctx); err != nil {
		t.Fatalf("第一个调度器扫描失败: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		return firstRecorder.launchedCount() == 2
	}, "第一个调度器应领取到自己的上限")
	// 第二个调度器在独立连接上扫描：它看到的数据库占用快照已经包含前 2 个名额，
	// 因此**不会**再领取任何任务——名额统计来自数据库事实，而不是本进程内存。
	if err := second.Scan(fixture.ctx); err != nil {
		t.Fatalf("第二个调度器扫描失败: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		return second.ActiveDispatches() == 0
	}, "第二个调度器的在途分派应收敛")

	// 1. 同一任务不得被两个调度器重复领取。
	seen := make(map[domain.ID]struct{}, tasks)
	for _, id := range firstRecorder.launchedOrder() {
		seen[id] = struct{}{}
	}
	for _, id := range secondRecorder.launchedOrder() {
		if _, exists := seen[id]; exists {
			t.Fatalf("任务 %s 被两个调度器重复领取", id)
		}
		seen[id] = struct{}{}
	}
	// 2. 数据库里每个任务的领取代次都必须是 1：重复领取会让代次递增。
	for _, id := range taskIDs {
		stored := fixture.task(t, id)
		if stored.LeaseEpoch > 1 {
			t.Fatalf("任务 %s 被重复领取，代次 %d", id, stored.LeaseEpoch)
		}
	}
	// 3. 名额由数据库事实仲裁：总量等于单进程上限，而不是"进程数 × 上限"。
	//
	// 这是本用例确认的产品语义：上限统计的是同一份数据库里真实占用名额的任务，
	// 因此多个调度器共享数据库时不会各自领满一份。它不提供跨数据库的全局配额，
	// 也不保证"两个进程分别持有独立数据库时总量有界"（P3-M5 §3 记录的已知边界）。
	if len(seen) != 2 {
		t.Fatalf("数据库已记录 2 个占名额任务时第二个调度器不得再领取，实际共 %d 个", len(seen))
	}
	if occupied := fixture.occupiedSlotCount(t); occupied != 2 {
		t.Fatalf("数据库应记录 2 个占名额的任务，实际 %d", occupied)
	}
	// 4. 第二个调度器没有领取任何任务，且没有写入任何租约。
	if secondRecorder.launchedCount() != 0 {
		t.Fatalf("名额已被占满时第二个调度器不得领取任务，实际启动 %d 次",
			secondRecorder.launchedCount())
	}
	close(block)
	waitFor(t, 10*time.Second, func() bool {
		return first.ActiveDispatches() == 0
	}, "第一个调度器的在途分派应收敛")
}

// TestSchedulerDoesNotExceedLimitUnderConcurrentScans 验证并发扫描不超额领取。
//
// 同一调度器的多次并发 Scan 会同时读到"还有空名额"：本进程在途分派必须计入占用，
// 否则并发扫描会把总领取数推过上限。
func TestSchedulerDoesNotExceedLimitUnderConcurrentScans(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	const tasks = 6
	for index := 0; index < tasks; index++ {
		fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Duration(index)*time.Second))
	}
	block := make(chan struct{})
	recorder := &leaseRecorder{
		workflowStarted: true,
		block:           block,
		started:         make(chan domain.ID, tasks),
	}
	// 名额统计与领取必须在同一个临界区内完成。为了让这个窗口确定性地被触发（而不是
	// 依赖 goroutine 调度运气），用受控的存储包装让第一次扫描在读完占用快照之后停在
	// 屏障上；此时其余扫描读到同一份空名额快照。只有串行化才能保证总量不超限。
	barrier := newScanBarrier(fixture.db)
	scheduler, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              barrier,
		Leases:             fixture.leases,
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
	// 快照领取，从而超限；串行化时它们阻塞在临界区外，不会有任何领取。
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
		t.Fatalf("并发扫描不得让领取数超过上限 2，实际 %d", count)
	}
	// 每个被领取的任务都必须恰好领取一次。
	if occupied := fixture.occupiedSlotCount(t); occupied > 2 {
		t.Fatalf("数据库记录的占用名额不得超过上限 2，实际 %d", occupied)
	}
	close(block)
	waitFor(t, 10*time.Second, func() bool {
		return scheduler.ActiveDispatches() == 0
	}, "在途分派应收敛")
}

// scanBarrier 把**第一次**名额统计读取停在屏障上。
//
// 它只拦截 countOccupiedSlots 用的 ListTasks（调用方传入 ExcludeStatuses），
// 不拦截候选读取（调用方传入 Statuses），因此屏障精确落在"名额统计完成、领取尚未
// 开始"的窗口内。
//
// 语义：第一次名额统计读完后停住，其余扫描可以自由推进。若调度器没有把"统计名额 →
// 领取"放在同一临界区，其余扫描会读到同一份空名额快照并各自领取，总量必然超限；
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

// ListTasks 让第一次名额统计读取在返回前停在屏障上。
func (b *scanBarrier) ListTasks(
	ctx context.Context,
	filter storage.TaskFilter,
) ([]domain.Task, error) {
	tasks, err := b.TaskSchedulerStore.ListTasks(ctx, filter)
	if len(filter.ExcludeStatuses) == 0 {
		// 候选读取：不拦截。
		return tasks, err
	}
	first := false
	b.once.Do(func() {
		first = true
		close(b.arrived)
	})
	if first {
		<-b.gate
	}
	return tasks, err
}

// waitArrived 等待第一次名额统计读取完成。
func (b *scanBarrier) waitArrived(t *testing.T) {
	t.Helper()
	select {
	case <-b.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("等待第一次名额统计超时")
	}
}

// release 放行被屏障拦住的第一次扫描。
func (b *scanBarrier) release() {
	close(b.gate)
}

// TestSchedulerReadFailureDoesNotFakeEmptyQueue 验证读取失败不被当成空队列。
//
// 候选读取失败时调度器必须返回错误：把读取失败当成"没有候选"会让运维误以为
// 队列已空，而实际是数据库不可用。
func TestSchedulerReadFailureDoesNotFakeEmptyQueue(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now.Add(-time.Hour))
	recorder := &leaseRecorder{workflowStarted: true}
	scheduler, err := app.NewTaskScheduler(app.TaskSchedulerOptions{
		Store:              &failingTaskListStore{TaskSchedulerStore: fixture.db},
		Leases:             fixture.leases,
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
	if stored := fixture.task(t, task.ID); stored.LeaseOwner != "" {
		t.Fatalf("读取失败时不得写入租约: %+v", stored)
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

// TestSchedulerKeepsSlotConsistentWhenClaimLosesRace 验证"读到空名额但领取失败"的名额一致性。
//
// 现场：调度器读到仍有空名额，但候选任务的租约已被另一个执行者抢先持有。此时
// 领取失败，调度器既不能凭空占用名额，也不能留下租约痕迹。
func TestSchedulerKeepsSlotConsistentWhenClaimLosesRace(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	// 另一个执行者先持有该任务的租约。
	holder, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "other-process",
		Statuses: domain.AutomaticSchedulingStatuses(),
	})
	if err != nil {
		t.Fatalf("预置租约失败: %v", err)
	}
	recorder := &leaseRecorder{workflowStarted: true}
	scheduler := fixture.newScheduler(t, 2, recorder)
	if scanErr := scheduler.Scan(fixture.ctx); scanErr != nil {
		t.Fatalf("调度扫描失败: %v", scanErr)
	}
	if recorder.launchedCount() != 0 {
		t.Fatal("租约已被他人持有时不得启动任务")
	}
	stored := fixture.task(t, task.ID)
	if stored.LeaseOwner != holder.Handle.Owner || stored.LeaseEpoch != holder.Handle.Epoch {
		t.Fatalf("领取失败不得改写他人租约: %+v", stored)
	}
	// 名额仍由真实持有者占用，调度器不得额外占用。
	if occupied := fixture.occupiedSlotCount(t); occupied != 1 {
		t.Fatalf("名额统计必须只反映真实持有者，实际 %d", occupied)
	}
}

// TestSchedulerLaunchFailureReleasesOnlyOwnSlot 验证启动失败只释放自己的名额。
//
// 现场：两个任务同时进入启动阶段，其中一个失败。失败任务释放自己的租约，另一个
// 任务的租约必须原样保留——释放他人租约会让并发任务失去执行权。
func TestSchedulerLaunchFailureReleasesOnlyOwnSlot(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	failing := fixture.queuedTask(t, domain.PriorityHigh, base)
	healthy := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Second))
	block := make(chan struct{})
	recorder := &leaseRecorder{
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

	failedTask := fixture.task(t, failing.ID)
	if failedTask.LeaseOwner != "" || failedTask.LeaseActive(fixture.now) {
		t.Fatalf("启动失败必须释放自己的租约: %+v", failedTask)
	}
	healthyTask := fixture.task(t, healthy.ID)
	if !healthyTask.LeaseActive(fixture.now) {
		t.Fatalf("另一个任务的租约不得被释放: %+v", healthyTask)
	}
	if healthyTask.LeaseOwner == "" {
		t.Fatal("另一个任务必须仍持有租约 owner")
	}
	// 失败任务不得被推进状态，也不得写入成功结论。
	if failedTask.Status != domain.TaskStatusQueued {
		t.Fatalf("启动失败不得推进任务状态，实际 %s", failedTask.Status)
	}
}

// TestCancelAndAdvanceRaceKeepsCancelIntent 验证取消与任务推进并发时取消意图不丢失。
//
// 现场：调度器持有租约并把任务推进到 developing，同时用户请求取消。无论两者谁
// 先落库，最终都必须同时满足"取消意图已记录"与"任务处于 cancelling 或已被执行者
// 收敛"，不允许取消意图被并发写入抹掉。
func TestCancelAndAdvanceRaceKeepsCancelIntent(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now.Add(-time.Hour))
	// 让另一个执行者持有执行权并停在执行阶段，模拟"正在推进"。
	claim, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "running-worker",
		Statuses: domain.AutomaticSchedulingStatuses(),
	})
	if err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
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
	stored := fixture.task(t, task.ID)
	// 取消不释放租约：旧执行者仍受 owner + epoch 约束，停止由它自己确认。
	if stored.LeaseOwner != claim.Handle.Owner || stored.LeaseEpoch != claim.Handle.Epoch {
		t.Fatalf("取消不得抹掉并发持有者的租约: %+v", stored)
	}
	if !stored.CancelRequested {
		t.Fatal("数据库里必须保留取消意图")
	}
	// 取消意图落盘后不得再被领取。
	if _, claimErr := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "late-worker",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	}); claimErr == nil {
		t.Fatal("已请求取消的任务不得被再次领取")
	}
}

// TestCancelDoesNotClobberConcurrentLeaseClaim 验证取消不会覆盖并发领取的租约。
//
// 这是 P3-M5 §4.2 的关键断言：取消写入必须带状态前置条件。无条件整行写入会把
// 同一时刻由调度器写入的 lease_owner / lease_epoch 覆盖为空，使新执行者失去执行权
// 却继续推进现场。
func TestCancelDoesNotClobberConcurrentLeaseClaim(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	store := &racingClaimStore{
		DB:      fixture.db,
		fixture: fixture,
		taskID:  task.ID,
		t:       t,
	}
	service, err := app.NewTaskService(app.TaskServiceOptions{Store: store})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	// 读取任务之后立即发生一次并发领取。取消写入只覆盖取消相关列，因此这一次
	// 取消会成功落盘，但**不得**抹掉并发领取写入的 lease_owner / lease_epoch。
	if _, cancelErr := service.CancelTask(fixture.ctx, task.ID.String()); cancelErr != nil {
		t.Fatalf("取消任务失败: %v", cancelErr)
	}
	stored := fixture.task(t, task.ID)
	if stored.LeaseOwner != "concurrent-scheduler" {
		t.Fatalf("并发领取的租约被取消覆盖: owner=%q epoch=%d", stored.LeaseOwner, stored.LeaseEpoch)
	}
	if stored.LeaseEpoch != 1 {
		t.Fatalf("并发领取的代次必须保留，实际 %d", stored.LeaseEpoch)
	}
	// 取消意图与租约必须同时成立：这正是"用户取消不需要执行权、也不交出执行权"。
	if !stored.CancelRequested {
		t.Fatal("取消意图必须被持久化")
	}
	if stored.Status != domain.TaskStatusCancelled {
		t.Fatalf("queued 任务取消后应直接收敛为 cancelled，实际 %s", stored.Status)
	}
}

// racingClaimStore 在读取任务之后、写入任务之前插入一次并发领取。
type racingClaimStore struct {
	*sqlite.DB
	fixture *schedulerFixture
	taskID  domain.ID
	t       *testing.T
	once    sync.Once
}

// GetTask 在首次读取目标任务后触发一次并发领取。
func (s *racingClaimStore) GetTask(ctx context.Context, id domain.ID) (*domain.Task, error) {
	task, err := s.DB.GetTask(ctx, id)
	if err != nil || id != s.taskID {
		return task, err
	}
	s.once.Do(func() {
		if _, claimErr := s.fixture.leases.Claim(ctx, app.TaskClaimRequest{
			TaskID:   id,
			Owner:    "concurrent-scheduler",
			Statuses: domain.AutomaticSchedulingStatuses(),
		}); claimErr != nil {
			s.t.Fatalf("构造并发领取失败: %v", claimErr)
		}
	})
	return task, err
}

var _ storage.Store = (*racingClaimStore)(nil)

// TestRetryRacesWithSchedulerForSameTask 验证重试与调度器竞争同一任务只产生一个周期。
//
// 现场：失败任务同时收到"用户显式重试"与"调度器扫描"。调度器只领取 queued，因此
// 它不得领取失败任务；重试成功后任务进入新周期，调度器也不得再重复领取。
func TestRetryRacesWithSchedulerForSameTask(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now.Add(-time.Hour))
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)

	retryRecorder := &retryRecorder{workflowStarted: true}
	retryService := fixture.newRetryService(t, retryRecorder)
	launchRecorder := &leaseRecorder{workflowStarted: true}
	scheduler := fixture.newScheduler(t, 2, launchRecorder)

	// 先让调度器扫描一次：failed 任务不得被自动领取。
	if scanErr := scheduler.Scan(fixture.ctx); scanErr != nil {
		t.Fatalf("调度扫描失败: %v", scanErr)
	}
	if launchRecorder.launchedCount() != 0 {
		t.Fatalf("调度器不得领取 failed 任务，实际启动 %d 次", launchRecorder.launchedCount())
	}
	if stored := fixture.task(t, task.ID); stored.LeaseOwner != "" {
		t.Fatalf("调度器不得给 failed 任务写入租约: %+v", stored)
	}
	// 用户显式重试：必须成功进入新周期，且只调用一次启动流程。
	if _, retryErr := retryService.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{}); retryErr != nil {
		t.Fatalf("显式重试失败: %v", retryErr)
	}
	if retryRecorder.count() != 1 {
		t.Fatalf("重试只允许调用一次启动流程，实际 %d 次", retryRecorder.count())
	}
	// 重试后再次扫描：任务已离开 failed 且被重试周期持有，调度器不得重复领取。
	before := fixture.task(t, task.ID)
	if scanErr := scheduler.Scan(fixture.ctx); scanErr != nil {
		t.Fatalf("第二次调度扫描失败: %v", scanErr)
	}
	after := fixture.task(t, task.ID)
	if after.LeaseEpoch != before.LeaseEpoch {
		t.Fatalf("调度器不得重复领取重试中的任务: 代次 %d → %d",
			before.LeaseEpoch, after.LeaseEpoch)
	}
	if launchRecorder.launchedCount() != 0 {
		t.Fatal("调度器不得对重试中的任务重复启动")
	}
}
