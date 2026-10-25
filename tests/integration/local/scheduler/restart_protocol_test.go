// 调度器重启与执行权接管的安全协议集成测试。
//
// 用例使用真实 SQLite、正式迁移、真实 app.TaskScheduler / app.TaskLeaseService /
// app.TaskLeaseRecoveryService，只把统一启动端口替换为受控替身。断言对象是数据库里
// 真实留下的任务记录、租约 owner/epoch、工作流执行记录与事件。
//
// 本文件刻意覆盖"不能证明安全时保留现场"的边界：租约过期不等于远程操作已停止，
// 因此调度器既不能清空租约，也不能把任务改回 queued 来恢复吞吐量。
package scheduler_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// moveToFailed 把任务推进到 failed 终局。
func (f *schedulerFixture) moveToFailed(t *testing.T, taskID domain.ID) {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	previous := task.Status
	if err := task.Transition(domain.TaskStatusFailed, f.now); err != nil {
		t.Fatalf("迁移任务到 failed 失败: %v", err)
	}
	if err := task.Finish(domain.TaskResultFailed, "上一次执行失败", f.now); err != nil {
		t.Fatalf("写入失败结论失败: %v", err)
	}
	if err := f.db.UpdateTaskIfStatus(f.ctx, *task, []domain.TaskStatus{previous}); err != nil {
		t.Fatalf("写入失败状态失败: %v", err)
	}
}

// writeActiveRun 写入一条 running 的执行记录。
func (f *schedulerFixture) writeActiveRun(
	t *testing.T,
	task domain.Task,
	steps []domain.Step,
	workerID string,
	expiresAt time.Time,
) domain.WorkflowRun {
	t.Helper()
	run, err := domain.NewWorkflowRun(task.ID, steps, f.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	run.WorkerID = workerID
	run.LeaseExpiresAt = &expiresAt
	run.Status = domain.WorkflowRunRunning
	if err := f.db.CreateWorkflowRun(f.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	return run
}

// TestSchedulerRestartAfterGracefulStop 验证正常关闭后重启仍能接管队列。
//
// 关闭时保留的租约在重启后仍然有效，因此调度器不得抢占它；只有该租约自然过期后
// 才允许新执行者接管，且接管必须建立在新代次之上。
func TestSchedulerRestartAfterGracefulStop(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)

	first := &leaseRecorder{workflowStarted: true}
	firstScheduler := fixture.newScheduler(t, 1, first)
	waitFor(t, 10*time.Second, func() bool {
		return first.launchedCount() == 1
	}, "首次启动应分派任务")

	// 关闭调度器：保留的租约不会被释放。
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := firstScheduler.Stop(stopCtx); err != nil {
		t.Fatalf("停止首个调度器失败: %v", err)
	}
	stored := fixture.task(t, task.ID)
	if !stored.LeaseActive(fixture.now) {
		t.Fatalf("正常关闭后租约必须保留至自然过期: %+v", stored)
	}
	firstEpoch := stored.LeaseEpoch

	// 重启后（同一份数据库）租约仍有效：新调度器不得抢占。
	second := &leaseRecorder{workflowStarted: true}
	restarted := fixture.newScheduler(t, 1, second)
	if err := restarted.Scan(fixture.ctx); err != nil {
		t.Fatalf("重启后的扫描失败: %v", err)
	}
	if second.launchedCount() != 0 {
		t.Fatal("租约仍有效时新调度器不得抢占同一任务")
	}
	if current := fixture.task(t, task.ID); current.LeaseEpoch != firstEpoch {
		t.Fatalf("未接管时不得改变领取代次: %d → %d", firstEpoch, current.LeaseEpoch)
	}
}

// TestSchedulerDoesNotReclaimExpiredLeaseWithUncertainState 验证过期租约不会被自动重放。
//
// 现场：任务停在 developing 且租约已过期，没有未结束执行记录。这类现场属于
// "结论不明"，调度器既不能领取（状态不是 queued），也不能清空租约或改写状态。
func TestSchedulerDoesNotReclaimExpiredLeaseWithUncertainState(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusDeveloping)

	// 一个已退出进程留下的过期租约。
	claim, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "dead-worker",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	})
	if err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	fixture.expireLease(t, task.ID)

	recorder := &leaseRecorder{workflowStarted: true}
	scheduler := fixture.newScheduler(t, 2, recorder)
	if err := scheduler.Scan(fixture.ctx); err != nil {
		t.Fatalf("调度扫描失败: %v", err)
	}
	if recorder.launchedCount() != 0 {
		t.Fatal("结论不明的过期租约任务不得被自动重放")
	}
	stored := fixture.task(t, task.ID)
	if stored.Status != domain.TaskStatusDeveloping {
		t.Fatalf("调度器不得改写任务状态，实际 %s", stored.Status)
	}
	if stored.LeaseOwner != claim.Handle.Owner || stored.LeaseEpoch != claim.Handle.Epoch {
		t.Fatalf("调度器不得清空或改写他人租约: %+v", stored)
	}
}

// TestLeaseRecoveryRecordsResidueWithoutReleasingLease 验证租约残留核验只记录现场。
//
// 关键约束：租约过期只说明持有者没有续租，不证明它发出的远程操作已经停止。
// 因此核验服务必须保留租约与任务状态，只追加可追溯的现场记录。
func TestLeaseRecoveryRecordsResidueWithoutReleasingLease(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusDeveloping)
	if _, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "dead-worker",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	}); err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	fixture.expireLease(t, task.ID)
	before := fixture.task(t, task.ID)

	service, err := app.NewTaskLeaseRecoveryService(app.TaskLeaseRecoveryOptions{
		Store:  fixture.db,
		Logger: fixture.logger.Slog(),
		Now:    func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造租约残留核验服务失败: %v", err)
	}
	if startErr := service.Start(fixture.ctx); startErr != nil {
		t.Fatalf("核验租约残留失败: %v", startErr)
	}

	after := fixture.task(t, task.ID)
	if after.Status != before.Status {
		t.Fatalf("核验不得改写任务状态: %s → %s", before.Status, after.Status)
	}
	if after.LeaseOwner != before.LeaseOwner || after.LeaseEpoch != before.LeaseEpoch {
		t.Fatalf("核验不得释放或改写租约: %+v", after)
	}
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == domain.EventTypeTaskRecoveryRequired {
			found = true
		}
	}
	if !found {
		t.Fatal("租约残留必须留下可追溯的现场记录")
	}
}

// TestLeaseRecoverySkipsLiveLease 验证仍有效的租约不产生残留结论。
func TestLeaseRecoverySkipsLiveLease(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	if _, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "live-worker",
		Statuses: domain.AutomaticSchedulingStatuses(),
	}); err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	service, err := app.NewTaskLeaseRecoveryService(app.TaskLeaseRecoveryOptions{
		Store:  fixture.db,
		Logger: fixture.logger.Slog(),
		Now:    func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造租约残留核验服务失败: %v", err)
	}
	if startErr := service.Start(fixture.ctx); startErr != nil {
		t.Fatalf("核验租约残留失败: %v", startErr)
	}
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	for _, event := range events {
		if event.Type == domain.EventTypeTaskRecoveryRequired {
			t.Fatal("有效租约不得被当作残留现场")
		}
	}
}

// TestLeaseRecoverySkipsActiveRun 验证存在未结束执行记录时不做残留结论。
func TestLeaseRecoverySkipsActiveRun(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusDeveloping)
	if _, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "dead-worker",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	}); err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	fixture.expireLease(t, task.ID)
	step := fixture.writeStep(t, task.ID, 1)
	fixture.writeActiveRun(
		t,
		fixture.task(t, task.ID),
		[]domain.Step{step},
		"run-worker",
		fixture.now.Add(time.Minute),
	)

	service, err := app.NewTaskLeaseRecoveryService(app.TaskLeaseRecoveryOptions{
		Store:  fixture.db,
		Logger: fixture.logger.Slog(),
		Now:    func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造租约残留核验服务失败: %v", err)
	}
	if startErr := service.Start(fixture.ctx); startErr != nil {
		t.Fatalf("核验租约残留失败: %v", startErr)
	}
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	for _, event := range events {
		if event.Type == domain.EventTypeTaskRecoveryRequired {
			t.Fatal("存在未结束执行记录时不得判定为租约残留")
		}
	}
}

// TestLeaseRecoveryFailureBlocksStartup 验证读取失败时启动失败。
//
// 无法判断是否存在残留租约时继续启动，会让调度器在未知现场上做判断。
func TestLeaseRecoveryFailureBlocksStartup(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	service, err := app.NewTaskLeaseRecoveryService(app.TaskLeaseRecoveryOptions{
		Store:  &failingLeaseStore{TaskLeaseRecoveryStore: fixture.db},
		Logger: fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造租约残留核验服务失败: %v", err)
	}
	if startErr := service.Start(fixture.ctx); startErr == nil {
		t.Fatal("读取残留租约失败时启动必须返回错误")
	}
}

// failingLeaseStore 让任务列表读取失败。
type failingLeaseStore struct {
	app.TaskLeaseRecoveryStore
}

// ListTasks 返回受控失败。
func (s *failingLeaseStore) ListTasks(
	ctx context.Context,
	filter storage.TaskFilter,
) ([]domain.Task, error) {
	return nil, errors.New("数据库读取失败")
}

// TestSchedulerAndLeaseRecoveryShareDatabase 验证两个独立连接看到的租约事实一致。
//
// 它证明"恢复先于调度"在数据库层面是有意义的：调度器在另一个连接上读到的仍是
// 未被清空的租约。
func TestSchedulerAndLeaseRecoveryShareDatabase(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	task := fixture.queuedTask(t, domain.PriorityNormal, base)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusDeveloping)
	if _, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "dead-worker",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	}); err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	fixture.expireLease(t, task.ID)

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
	service, err := app.NewTaskLeaseRecoveryService(app.TaskLeaseRecoveryOptions{
		Store:  fixture.db,
		Logger: fixture.logger.Slog(),
		Now:    func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造租约残留核验服务失败: %v", err)
	}
	if startErr := service.Start(fixture.ctx); startErr != nil {
		t.Fatalf("核验租约残留失败: %v", startErr)
	}
	// 核验之后，另一个连接读到的仍是同一个（已过期但未被清除的）租约。
	stored, err := other.GetTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatalf("第二个连接读取任务失败: %v", err)
	}
	if stored.LeaseOwner == "" {
		t.Fatal("核验不得清空租约 owner")
	}
	if stored.LeaseActive(fixture.now) {
		t.Fatal("该租约应当已过期")
	}
	// 过期租约可以被新执行者接管，且必须建立在新代次之上。
	claim, err := otherLeases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "new-worker",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	})
	if err != nil {
		t.Fatalf("过期租约应当可以被接管: %v", err)
	}
	if claim.Handle.Epoch <= stored.LeaseEpoch {
		t.Fatalf("接管后代次必须递增: %d → %d", stored.LeaseEpoch, claim.Handle.Epoch)
	}
}
