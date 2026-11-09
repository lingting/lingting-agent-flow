package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// 本文件验证需求删除与并发执行入口、任务取消、项目执行权之间的不变量。
//
// 所有用例都使用真实 SQLite、真实仓储与真实协调器，断言的是数据库里留下的记录与协调器
// 登记，而不是方法返回值。并发用例使用屏障而不是 sleep 猜测竞争窗口。

// deleteRaceCanceler 是项目停止流程使用的取消入口替身：只记录调用，不改写任务状态。
type deleteRaceCanceler struct {
	mu  sync.Mutex
	ids []domain.ID
}

// CancelTask 记录一次取消调用。
func (c *deleteRaceCanceler) CancelTask(
	ctx context.Context,
	taskID string,
) (domain.Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, domain.ID(taskID))
	return domain.Task{}, nil
}

// TestDeleteAndCancelConcurrentlyKeepFacts 校验删除与任务取消竞争后现场一致。
func TestDeleteAndCancelConcurrentlyKeepFacts(t *testing.T) {
	t.Parallel()
	fixture := newDeleteFixture(t)
	taskService, err := app.NewTaskService(app.TaskServiceOptions{Store: fixture.base.db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}

	createdIDs := make([]domain.ID, 0, 20)
	// deleteAllowed 是需求聚合删除允许的状态集合：删除只写软删除标记，不改写状态。
	deleteAllowed := map[domain.TaskStatus]bool{
		domain.TaskStatusQueued:    true,
		domain.TaskStatusCompleted: true,
		domain.TaskStatusFailed:    true,
		domain.TaskStatusCancelled: true,
	}
	for round := 0; round < 20; round++ {
		confirmed := fixture.confirm(t, 1)
		taskID := confirmed.Tasks[0].ID
		createdIDs = append(createdIDs, taskID)
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		var deleteErr error
		var cancelErr error
		go func() {
			defer wait.Done()
			<-start
			_, deleteErr = fixture.service.Delete(
				fixture.base.ctx,
				confirmed.Requirement.ID.String(),
			)
		}()
		go func() {
			defer wait.Done()
			<-start
			_, cancelErr = taskService.CancelTask(fixture.base.ctx, taskID.String())
		}()
		close(start)
		wait.Wait()

		requirement := fixture.requirement(t, confirmed.Requirement.ID)
		task := fixture.task(t, taskID)
		deleted := requirement.DeletedAt != nil
		if deleted != (task.DeletedAt != nil) {
			t.Fatalf(
				"第 %d 轮：需求与任务的删除状态必须一致（需求已删除=%t，任务已删除=%t）",
				round,
				deleted,
				task.DeletedAt != nil,
			)
		}
		if task.DeletedAt != nil {
			if deleteErr != nil {
				t.Fatalf("第 %d 轮：任务已删除但删除返回错误: %v", round, deleteErr)
			}
			// 删除只写软删除标记，不改写状态：留下的状态必须是删除准入集合内的原状态。
			if !deleteAllowed[task.Status] {
				t.Fatalf("第 %d 轮：删除改写了任务状态: %s", round, task.Status)
			}
			continue
		}
		if cancelErr != nil {
			t.Fatalf("第 %d 轮：取消任务失败: %v", round, cancelErr)
		}
		if !errors.Is(deleteErr, app.ErrRequirementStateConflict) {
			t.Fatalf("第 %d 轮：任务仍可见时删除必须只以冲突失败，实际: %v", round, deleteErr)
		}
		if task.Status == domain.TaskStatusCancelling && !task.CancelRequested {
			t.Fatalf("第 %d 轮：取消中的任务必须保留取消请求", round)
		}
	}

	for _, taskID := range createdIDs {
		if fixture.coordination.Held(taskID) {
			t.Fatalf("并发结束后不得残留任务执行权: %s", taskID)
		}
	}
}

// TestDeleteReleasesEarlierGrantsWhenLaterTaskIsHeld 校验多任务取权中途失败时全部释放。
func TestDeleteReleasesEarlierGrantsWhenLaterTaskIsHeld(t *testing.T) {
	t.Parallel()
	fixture := newDeleteFixture(t)
	confirmed := fixture.confirm(t, 3)
	held := confirmed.Tasks[1].ID
	grant, acquired := fixture.coordination.TryAcquire(held)
	if !acquired {
		t.Fatal("测试应能取得目标任务执行权")
	}

	if _, err := fixture.service.Delete(
		fixture.base.ctx,
		confirmed.Requirement.ID.String(),
	); !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("任务执行权被占用时应拒绝删除，实际: %v", err)
	}
	fixture.requireNothingDeleted(t, confirmed)

	for _, created := range confirmed.Tasks {
		if created.ID == held {
			if !fixture.coordination.Held(held) {
				t.Fatal("测试自身持有的执行权不得被删除流程释放")
			}
			continue
		}
		reacquired, ok := fixture.coordination.TryAcquire(created.ID)
		if !ok {
			t.Fatalf("取权中途失败时必须释放已取得的执行权: %s", created.ID)
		}
		fixture.coordination.Release(reacquired)
	}
	fixture.coordination.Release(grant)
}

// TestDeleteWithCancelledContextKeepsFacts 校验事务中途取消时不留半成品。
func TestDeleteWithCancelledContextKeepsFacts(t *testing.T) {
	t.Parallel()
	fixture := newDeleteFixture(t)
	confirmed := fixture.confirm(t, 2)
	ctx, cancel := context.WithCancel(fixture.base.ctx)
	cancel()

	if _, err := fixture.service.Delete(ctx, confirmed.Requirement.ID.String()); err == nil {
		t.Fatal("上下文已取消时删除必须失败")
	}
	fixture.requireNothingDeleted(t, confirmed)
	for _, created := range confirmed.Tasks {
		if fixture.coordination.Held(created.ID) {
			t.Fatalf("被取消的删除不得残留执行权: %s", created.ID)
		}
	}
}

// TestDeleteAndProjectStopKeepFacts 校验删除需求与服务停止释放执行权不会互相破坏事实。
func TestDeleteAndProjectStopKeepFacts(t *testing.T) {
	t.Parallel()
	fixture := newDeleteFixture(t)
	confirmed := fixture.confirm(t, 2)
	fixture.base.ensureProject(t)
	now := time.Now().UTC()
	owner := "server-delete-race"
	if _, err := fixture.base.db.AcquireProjectExecutionLease(
		fixture.base.ctx,
		domain.ID(confirmProjectID),
		owner,
		now,
		now.Add(time.Hour),
	); err != nil {
		t.Fatalf("取得项目执行权失败: %v", err)
	}
	service, err := app.NewProjectExecutionService(app.ProjectExecutionServiceOptions{
		Store:    fixture.base.db,
		Canceler: &deleteRaceCanceler{},
		Owner:    owner,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("构造项目执行服务失败: %v", err)
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var deleteErr error
	var stopErr error
	go func() {
		defer wait.Done()
		<-start
		_, deleteErr = fixture.service.Delete(
			fixture.base.ctx,
			confirmed.Requirement.ID.String(),
		)
	}()
	go func() {
		defer wait.Done()
		<-start
		stopErr = service.ReleaseOnStop(fixture.base.ctx)
	}()
	close(start)
	wait.Wait()

	if stopErr != nil {
		t.Fatalf("停止时释放项目执行权失败: %v", stopErr)
	}
	lease, err := fixture.base.db.GetProjectExecutionLease(
		fixture.base.ctx,
		domain.ID(confirmProjectID),
	)
	if err != nil {
		t.Fatalf("读取项目执行权失败: %v", err)
	}
	if lease.ActiveAt(now) {
		t.Fatal("没有在途任务时停止必须释放项目执行权")
	}

	requirement := fixture.requirement(t, confirmed.Requirement.ID)
	if requirement.DeletedAt == nil {
		if deleteErr != nil {
			t.Fatalf("需求仍可见时删除不应报错: %v", deleteErr)
		}
		fixture.requireNothingDeleted(t, confirmed)
		return
	}
	if deleteErr != nil {
		t.Fatalf("需求已删除但删除返回错误: %v", deleteErr)
	}
	for _, created := range confirmed.Tasks {
		if fixture.task(t, created.ID).DeletedAt == nil {
			t.Fatalf("需求已删除时任务 %s 必须一并软删除", created.ID)
		}
	}
}

// TestReleaseOnStopKeepsLeaseOfRunningTask 校验还有在途任务时停止不释放执行权。
func TestReleaseOnStopKeepsLeaseOfRunningTask(t *testing.T) {
	t.Parallel()
	fixture := newDeleteFixture(t)
	confirmed := fixture.confirm(t, 1)
	fixture.base.ensureProject(t)
	now := time.Now().UTC()
	owner := "server-stop-running"
	acquired, err := fixture.base.db.AcquireProjectExecutionLease(
		fixture.base.ctx,
		domain.ID(confirmProjectID),
		owner,
		now,
		now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("取得项目执行权失败: %v", err)
	}
	fixture.moveTask(t, confirmed.Tasks[0].ID, domain.TaskStatusDeveloping)

	service, err := app.NewProjectExecutionService(app.ProjectExecutionServiceOptions{
		Store:    fixture.base.db,
		Canceler: &deleteRaceCanceler{},
		Owner:    owner,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("构造项目执行服务失败: %v", err)
	}
	if err := service.ReleaseOnStop(fixture.base.ctx); err != nil {
		t.Fatalf("停止时释放项目执行权失败: %v", err)
	}

	lease, err := fixture.base.db.GetProjectExecutionLease(
		fixture.base.ctx,
		domain.ID(confirmProjectID),
	)
	if err != nil {
		t.Fatalf("读取项目执行权失败: %v", err)
	}
	if !lease.ActiveAt(now) {
		t.Fatal("仍有在途任务时必须保留执行权直到到期")
	}
	if lease.Epoch != acquired.Epoch || lease.Owner != owner {
		t.Fatalf("保留的执行权不得被改写: %+v", lease)
	}
}
