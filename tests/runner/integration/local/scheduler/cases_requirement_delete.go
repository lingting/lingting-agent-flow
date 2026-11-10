// 需求聚合删除与调度领取之间的不变量：迁移自
// tests/integration/local/scheduler/requirement_delete_test.go。
//
// 断言依据数据库事实与协调器登记：调度器的候选筛选、进程内执行权与名额统计全部走生产
// 代码，只有统一启动端口是受控替身，因此用例可以直接观察"任务是否被领取"。
package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/internal/testrunner"
)

// newRequirementService 构造与调度器共用同一个执行协调器的需求服务。
func (f *schedulerFixture) newRequirementService(t *testrunner.Context) *app.RequirementService {
	t.Helper()
	service, err := app.NewRequirementService(app.RequirementServiceOptions{
		Coordination: f.coordinator,
		Store:        f.db,
	})
	if err != nil {
		t.Fatalf("构造需求服务失败: %v", err)
	}
	return service
}

// 原 TestSchedulerSkipsTasksOfDeletedRequirement 校验已删除需求的任务不会被领取。
func runSchedulerSkipsTasksOfDeletedRequirement(t *testrunner.Context) {
	fixture := newSchedulerFixture(t)
	base := fixture.now.Add(-time.Hour)
	deleted := fixture.queuedTask(t, domain.PriorityHigh, base)
	kept := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Second))

	service := fixture.newRequirementService(t)
	if _, err := service.Delete(fixture.ctx, deleted.RequirementID.String()); err != nil {
		t.Fatalf("删除需求失败: %v", err)
	}

	recorder := &dispatchRecorder{workflowStarted: true}
	fixture.newScheduler(t, 4, recorder)
	waitFor(t, 5*time.Second, func() bool {
		return recorder.launchedCount() == 1
	}, "调度器应当只领取未被删除需求的任务")

	launched := recorder.launchedOrder()
	if len(launched) != 1 || launched[0] != kept.ID {
		t.Fatalf("已删除需求的任务不得被领取: %v", launched)
	}
	persisted := fixture.task(t, deleted.ID)
	if persisted.DeletedAt == nil {
		t.Fatal("被删除的任务必须保持逻辑删除")
	}
	if persisted.Status != domain.TaskStatusQueued {
		t.Fatalf("删除不得改写任务状态: %s", persisted.Status)
	}
	if fixture.coordinator.Held(deleted.ID) {
		t.Fatal("已删除任务不得留下执行权登记")
	}
}

// 原 TestDeleteRejectsWhileSchedulerDispatchInFlight 校验在途分派期间删除失败关闭。
func runDeleteRejectsWhileSchedulerDispatchInFlight(t *testrunner.Context) {
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now.Add(-time.Hour))
	service := fixture.newRequirementService(t)

	recorder := &dispatchRecorder{
		// 未启动工作流时调度器在分派结束后自行释放执行权，因此这里的 workflowStarted
		// 必须为 false：用例要观察的是执行权在分派期间被持有、分派收敛后被释放。
		workflowStarted: false,
		block:           make(chan struct{}),
		started:         make(chan domain.ID, 1),
	}
	fixture.newScheduler(t, 4, recorder)

	select {
	case started := <-recorder.started:
		if started != task.ID {
			t.Fatalf("分派的任务不符: %s", started)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待调度器开始分派超时")
	}

	if _, err := service.Delete(
		fixture.ctx,
		task.RequirementID.String(),
	); !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("在途分派时应拒绝删除，实际: %v", err)
	}
	persisted := fixture.task(t, task.ID)
	if persisted.DeletedAt != nil {
		t.Fatal("被拒绝时不得写入任务删除时间")
	}
	requirement, err := fixture.db.GetRequirement(fixture.ctx, task.RequirementID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if requirement.DeletedAt != nil {
		t.Fatal("被拒绝时不得写入需求删除时间")
	}

	close(recorder.block)
	waitFor(t, 5*time.Second, func() bool {
		return !fixture.coordinator.Held(task.ID)
	}, "分派结束后必须释放执行权")

	if _, err := service.Delete(fixture.ctx, task.RequirementID.String()); err != nil {
		t.Fatalf("分派收敛后应允许删除需求: %v", err)
	}
	if fixture.task(t, task.ID).DeletedAt == nil {
		t.Fatal("删除成功后任务必须一并软删除")
	}
}

// deleteAwareRecorder 在领取时回查数据库，记录"领取时任务已被软删除"的现场。
//
// 它复用调度器的真实候选筛选与执行权登记，只替换统一启动端口；额外的一次读库使用例能够
// 区分"已删除任务被领取"与"任务先被领取、随后被合法删除"两种情况。
type deleteAwareRecorder struct {
	db *sqlite.DB

	mu sync.Mutex
	// launched 是按调用顺序记录的任务标识。
	launched []domain.ID
	// claimedDeleted 记录领取时已经被逻辑删除的任务，非空即违反不变量。
	claimedDeleted []domain.ID
	// block 非空时阻塞启动调用，用于构造在途分派现场。
	block   chan struct{}
	started chan domain.ID
}

// Launch 记录一次启动，并在调用时回查任务的删除状态。
func (r *deleteAwareRecorder) Launch(
	ctx context.Context,
	request app.TaskLaunchRequest,
) (app.TaskLaunchResult, error) {
	taskID := domain.ID(request.TaskID)
	task, err := r.db.GetTask(ctx, taskID)
	r.mu.Lock()
	r.launched = append(r.launched, taskID)
	if err == nil && task.DeletedAt != nil {
		r.claimedDeleted = append(r.claimedDeleted, taskID)
	}
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
	return app.TaskLaunchResult{
		TaskID:     taskID,
		Stage:      domain.TaskLaunchStageWorkflow,
		TaskStatus: domain.TaskStatusPlanned,
	}, nil
}

// deletedClaims 返回领取时已经被删除的任务标识快照。
func (r *deleteAwareRecorder) deletedClaims() []domain.ID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.ID(nil), r.claimedDeleted...)
}

// 原 TestConcurrentDeleteAndSchedulerScanKeepFacts 校验并发删除与调度扫描后现场一致。
func runConcurrentDeleteAndSchedulerScanKeepFacts(t *testrunner.Context) {
	fixture := newSchedulerFixture(t)
	service := fixture.newRequirementService(t)
	recorder := &deleteAwareRecorder{db: fixture.db}
	scheduler := fixture.newScheduler(t, 4, recorder)

	// deleteAllowed 是需求聚合删除允许的状态集合：删除只写软删除标记，不改写状态。
	deleteAllowed := map[domain.TaskStatus]bool{
		domain.TaskStatusQueued:    true,
		domain.TaskStatusCompleted: true,
		domain.TaskStatusFailed:    true,
		domain.TaskStatusCancelled: true,
	}

	base := fixture.now.Add(-time.Hour)
	for round := 0; round < 10; round++ {
		task := fixture.queuedTask(t, domain.PriorityNormal, base.Add(time.Duration(round)*time.Second))
		start := make(chan struct{})
		var scanErr error
		var deleteErr error
		done := make(chan struct{}, 2)
		go func() {
			<-start
			scanErr = scheduler.Scan(fixture.ctx)
			done <- struct{}{}
		}()
		go func() {
			<-start
			_, deleteErr = service.Delete(fixture.ctx, task.RequirementID.String())
			done <- struct{}{}
		}()
		close(start)
		<-done
		<-done
		if scanErr != nil && !errors.Is(scanErr, app.ErrSchedulerStopped) {
			t.Fatalf("第 %d 轮：调度扫描失败: %v", round, scanErr)
		}

		persisted := fixture.task(t, task.ID)
		requirement, err := fixture.db.GetRequirement(fixture.ctx, task.RequirementID)
		if err != nil {
			t.Fatalf("第 %d 轮：读取需求失败: %v", round, err)
		}
		deleted := requirement.DeletedAt != nil
		if deleted != (persisted.DeletedAt != nil) {
			t.Fatalf(
				"第 %d 轮：需求与任务的删除状态必须一致（需求已删除=%t，任务已删除=%t）",
				round,
				deleted,
				persisted.DeletedAt != nil,
			)
		}
		if persisted.DeletedAt != nil {
			if deleteErr != nil {
				t.Fatalf("第 %d 轮：任务已删除但删除返回错误: %v", round, deleteErr)
			}
			if !deleteAllowed[persisted.Status] {
				t.Fatalf("第 %d 轮：删除改写了任务状态: %s", round, persisted.Status)
			}
			continue
		}
		if !errors.Is(deleteErr, app.ErrRequirementStateConflict) {
			t.Fatalf("第 %d 轮：任务仍可见时删除必须只以冲突失败，实际: %v", round, deleteErr)
		}
	}

	if claims := recorder.deletedClaims(); len(claims) != 0 {
		t.Fatalf("调度器不得领取已删除任务: %v", claims)
	}
	for _, task := range recorder.launchedOrderSnapshot() {
		if fixture.coordinator.Held(task) {
			t.Fatalf("分派结束后不得残留执行权: %s", task)
		}
	}
}

// launchedOrderSnapshot 返回已领取任务标识的快照。
func (r *deleteAwareRecorder) launchedOrderSnapshot() []domain.ID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.ID(nil), r.launched...)
}
