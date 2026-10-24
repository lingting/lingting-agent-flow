// Package app_test 验证 B6：租约续期使长任务不被误判为已退出。
//
// 用例使用可注入的短租约与阻塞的闭环替身：续租与误判都在毫秒级完成，不依赖
// 长时间 Sleep 或概率性调度。断言的对象是数据库里的 workflow_runs 记录。
package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// TestWorkflowHeartbeatKeepsLeaseAlive 验证单次调用跨越原始租约时不丢失执行权。
//
// 故障现场：默认租约 10 分钟、单步默认超时 30 分钟。修复前租约只写入一次，
// 长调用期间租约过期，另一个进程启动后会把仍在推进的记录收敛为 blocked。
func TestWorkflowHeartbeatKeepsLeaseAlive(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 1,
		lease:     300 * time.Millisecond,
		replies:   []taskWorkflowReply{{kind: taskReplyBlocked}},
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	// 等待编排器真正进入步骤执行：此时租约已写入，续租开始生效。
	waitTaskWorkflowStatus(t, fixture.service, fixture.task.ID, func(result app.WorkflowStatusResult) bool {
		return result.Run.CurrentStepID != nil
	})
	run := fixture.activeRun(t)
	firstExpiry := run.LeaseExpiresAt
	if firstExpiry == nil {
		t.Fatal("推进中的执行记录必须持有租约")
	}

	// 等待超过原始租约时长：如果没有续租，租约早已过期。
	time.Sleep(4 * time.Second)

	run = fixture.activeRun(t)
	if run.Status != domain.WorkflowRunRunning {
		t.Fatalf("仍在推进的执行不得被改写状态，实际 %s", run.Status)
	}
	if run.LeaseExpiresAt == nil {
		t.Fatal("续租期间租约字段不得被清空")
	}
	if !run.LeaseExpiresAt.After(*firstExpiry) {
		t.Fatalf(
			"租约必须被延长：原 %s，当前 %s",
			firstExpiry.Format(time.RFC3339Nano),
			run.LeaseExpiresAt.Format(time.RFC3339Nano),
		)
	}
	// 当前时刻租约仍然有效：其他进程不得把它判为已退出。
	if !run.LeaseActive(time.Now().UTC()) {
		t.Fatalf("续租后租约必须仍然有效，实际到期于 %s", run.LeaseExpiresAt)
	}
}

// TestWorkflowHeartbeatDoesNotReviveTerminalRun 验证终态记录不会被续租复活。
func TestWorkflowHeartbeatDoesNotReviveTerminalRun(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:      1,
		lease:          100 * time.Millisecond,
		codeSaver:      true,
		sessions:       true,
		codeSaveRecord: succeededCodeSaveRecord(),
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		func(status domain.WorkflowRunStatus) bool { return status.IsTerminal() })
	stored, err := fixture.db.GetWorkflowRun(fixture.ctx, result.Run.RunID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if stored.WorkerID != "" {
		t.Fatalf("终态记录不得保留执行者，实际 %q", stored.WorkerID)
	}
	if stored.LeaseExpiresAt != nil {
		t.Fatal("终态记录不得保留租约")
	}
	// 等待超过租约时长：任何残留心跳都不能给终态记录重新延长租约。
	time.Sleep(500 * time.Millisecond)
	after, err := fixture.db.GetWorkflowRun(fixture.ctx, result.Run.RunID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if after.LeaseExpiresAt != nil || after.WorkerID != "" {
		t.Fatal("终态记录被续租复活")
	}
	if after.Status != stored.Status {
		t.Fatalf("终态记录状态被改写: %s → %s", stored.Status, after.Status)
	}
}

// TestWorkflowRecoveryKeepsRenewedLease 验证续租后的租约阻止其他进程抢占。
//
// 两个实例共享同一数据库：第二个实例启动时，第一个实例的租约仍然有效，
// 因此不得把仍在推进的记录收敛为 blocked。
func TestWorkflowRecoveryKeepsRenewedLease(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 1,
		lease:     300 * time.Millisecond,
		replies:   []taskWorkflowReply{{kind: taskReplyBlocked}},
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	waitTaskWorkflowStatus(t, fixture.service, fixture.task.ID, func(result app.WorkflowStatusResult) bool {
		return result.Run.CurrentStepID != nil
	})
	time.Sleep(1500 * time.Millisecond)

	// 第二个实例只做启动恢复：租约仍然有效，不得抢占。
	other := fixture.secondService(t)
	if err := other.Start(fixture.ctx); err != nil {
		t.Fatalf("第二个实例启动失败: %v", err)
	}
	run := fixture.activeRun(t)
	if run.Status != domain.WorkflowRunRunning {
		t.Fatalf("有效租约存在时不得抢占，实际 %s", run.Status)
	}
	if run.ErrorClass != "" {
		t.Fatalf("未被抢占的记录不得写入停止原因，实际 %q", run.ErrorClass)
	}
}

// TestWorkflowExpiredLeaseAllowsRecovery 验证真正过期的租约仍允许后续收敛。
//
// 续租不能阻止"进程真的退出"这一场景被正确处理：没有心跳后租约自然过期，
// 其他服务必须能据此收敛残留。
func TestWorkflowExpiredLeaseAllowsRecovery(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 1,
		lease:     200 * time.Millisecond,
		existingRun: func(t *testing.T, task domain.Task, steps []domain.Step) domain.WorkflowRun {
			t.Helper()
			run, err := domain.NewWorkflowRun(task.ID, steps, time.Now().UTC().Add(-time.Hour))
			if err != nil {
				t.Fatalf("构造执行记录失败: %v", err)
			}
			// 模拟已退出的执行者：租约早已过期，没有任何心跳。
			if err := run.Start("dead-worker", time.Minute, time.Now().UTC().Add(-time.Hour)); err != nil {
				t.Fatalf("登记执行权失败: %v", err)
			}
			return run
		},
	})
	// fixture 启动时已执行启动恢复：过期租约必须被收敛为 blocked。
	run := fixture.activeRun(t)
	if run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("过期租约必须被收敛为 blocked，实际 %s", run.Status)
	}
	if run.ErrorClass != "worker_interrupted" {
		t.Fatalf("收敛原因应为 worker_interrupted，实际 %q", run.ErrorClass)
	}
	if run.LeaseExpiresAt != nil || run.WorkerID != "" {
		t.Fatal("收敛后的记录不得保留执行权")
	}
}

// activeRun 返回任务当前未结束的执行记录。
func (f *taskWorkflowFixture) activeRun(t *testing.T) domain.WorkflowRun {
	t.Helper()
	run, err := f.db.GetActiveWorkflowRun(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取未结束执行记录失败: %v", err)
	}
	return *run
}

// secondService 构造第二个共享同一数据库的编排器实例。
//
// 它只用于验证启动恢复的抢占判定，不启动任何后台推进。
func (f *taskWorkflowFixture) secondService(t *testing.T) *app.TaskWorkflowService {
	t.Helper()
	service, err := app.NewTaskWorkflowService(app.TaskWorkflowOptions{
		Store:      f.db,
		Steps:      &taskWorkflowRunner{db: f.db},
		Containers: &taskWorkflowContainer{running: true},
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造第二个编排器实例失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("停止第二个编排器实例失败: %v", err)
		}
	})
	return service
}
