// Package workflow_test 验证 B5：执行记录层的取消意图在重启后仍能收敛任务层。
//
// 用例使用真实 SQLite 与正式的 app.TaskWorkflowService：断言的对象是数据库里真实
// 留下的 Task / WorkflowRun 记录与事件，而不是方法的返回值。
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// writeCancelledRun 写入一条"已记录取消请求并已取消"的执行记录。
//
// 它精确复现 B5 的故障窗口：执行记录层已经确认取消生效，任务层却还停在
// developing——取消入口先写执行记录、后写任务层，进程恰好停在两次写入之间。
func writeCancelledRun(
	t *testing.T,
	fixture *restartFixture,
	task domain.Task,
	steps []domain.Step,
) domain.WorkflowRun {
	t.Helper()
	run, err := domain.NewWorkflowRun(task.ID, steps, fixture.now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("dead-worker", time.Minute, fixture.now.Add(-time.Hour)); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	if err := run.RequestCancel(fixture.now.Add(-30 * time.Minute)); err != nil {
		t.Fatalf("记录取消请求失败: %v", err)
	}
	if err := run.Transition(domain.WorkflowRunCancelRequested, fixture.now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("推进到 cancel_requested 失败: %v", err)
	}
	if err := fixture.db.UpdateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("更新执行记录失败: %v", err)
	}
	return run
}

// TestRecoverCancelIntentConvergesTask 验证重启后任务层取消意图被补齐。
//
// 故障现场：执行记录已记录取消请求（任务层尚未同步），任务仍是 developing。
// 修复前 recoverRun 只把执行记录收敛为 cancelled，任务保持 developing，
// 之后还能被 StartWorkflow 重新推进——用户取消意图丢失。
func TestRecoverCancelIntentConvergesTask(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusDeveloping, false)
	steps := fixture.writePlanSteps(t, task, 1)
	writeCancelledRun(t, fixture, task, steps)
	fixture.restart(t)

	fixture.mustStartService(t, nil, nil, nil)

	recovered := fixture.task(t, task.ID)
	if recovered.Status != domain.TaskStatusCancelled {
		t.Fatalf("取消意图必须收敛任务层，实际状态 %s", recovered.Status)
	}
	if recovered.FinalResult != domain.TaskResultCancelled {
		t.Fatalf("收敛为已取消时必须写入取消结论，实际 %q", recovered.FinalResult)
	}
	if !recovered.CancelRequested {
		t.Fatal("任务层必须记录取消请求，否则之后仍会被当作未取消")
	}
	// 执行记录层同样必须收敛为终态。
	for _, stored := range fixture.runs(t, task.ID) {
		if stored.Status == domain.WorkflowRunRunning ||
			stored.Status == domain.WorkflowRunCancelRequested {
			t.Fatalf("执行记录 %s 仍停留在 %s", stored.ID, stored.Status)
		}
	}
	if count := countEvents(fixture.events(t, task.ID), domain.EventTypeTaskCancelled); count != 1 {
		t.Fatalf("收敛只允许写一条取消事件，实际 %d 条", count)
	}
}

// TestRecoverCancelIntentBlocksNewWorkflow 验证收敛后不能再启动新工作流。
//
// 这是"取消意图没有丢失"的真正判据：不仅状态正确，正式入口也必须拒绝继续推进。
func TestRecoverCancelIntentBlocksNewWorkflow(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusDeveloping, false)
	steps := fixture.writePlanSteps(t, task, 1)
	writeCancelledRun(t, fixture, task, steps)
	fixture.restart(t)

	service := fixture.mustStartService(t, nil, nil, nil)
	if _, err := service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: task.ID.String(),
	}); err == nil {
		t.Fatal("任务已记录取消请求时不得再启动工作流")
	}
}

// TestRecoverCancelIntentIsIdempotent 验证重复恢复不会重复写入矛盾结果。
func TestRecoverCancelIntentIsIdempotent(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusDeveloping, false)
	steps := fixture.writePlanSteps(t, task, 1)
	writeCancelledRun(t, fixture, task, steps)
	fixture.restart(t)

	// 连续两次启动：第二次不应再改写状态，也不应重复写入恢复事件。
	fixture.mustStartService(t, nil, nil, nil)
	first := fixture.task(t, task.ID)
	fixture.restart(t)
	fixture.mustStartService(t, nil, nil, nil)
	second := fixture.task(t, task.ID)
	if first.Status != second.Status {
		t.Fatalf("重复恢复不得改变状态: %s → %s", first.Status, second.Status)
	}
	if count := countEvents(fixture.events(t, task.ID), domain.EventTypeTaskCancelled); count != 1 {
		t.Fatalf("重复恢复只允许写一条取消事件，实际 %d 条", count)
	}
}

// TestRecoverCancelIntentKeepsTerminalTask 验证已结束的任务不被恢复流程重新打开。
func TestRecoverCancelIntentKeepsTerminalTask(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusPlanned, false)
	steps := fixture.writePlanSteps(t, task, 1)
	writeCancelledRun(t, fixture, task, steps)
	// 任务已经通过其他路径正常完成：终态比"取消意图"更晚、更权威。
	if err := task.Transition(domain.TaskStatusDeveloping, fixture.now); err != nil {
		t.Fatalf("迁移任务失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusVerifying, fixture.now); err != nil {
		t.Fatalf("迁移任务失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusSaving, fixture.now); err != nil {
		t.Fatalf("迁移任务失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusCompleted, fixture.now); err != nil {
		t.Fatalf("迁移任务失败: %v", err)
	}
	if err := task.Finish(domain.TaskResultSucceeded, "全部完成", fixture.now); err != nil {
		t.Fatalf("写入结论失败: %v", err)
	}
	if err := fixture.db.UpdateTask(fixture.ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	fixture.restart(t)

	fixture.mustStartService(t, nil, nil, nil)
	recovered := fixture.task(t, task.ID)
	if recovered.Status != domain.TaskStatusCompleted {
		t.Fatalf("已结束的任务不得被重新打开，实际 %s", recovered.Status)
	}
}

// TestRecoverCancelIntentFailureBlocksStartup 验证补齐写入失败时启动失败。
//
// 取消意图没有落库时服务继续启动，会让用户以为取消已经生效。
func TestRecoverCancelIntentFailureBlocksStartup(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusDeveloping, false)
	steps := fixture.writePlanSteps(t, task, 1)
	writeCancelledRun(t, fixture, task, steps)
	fixture.restart(t)

	store := &failingStore{
		TaskWorkflowStore: fixture.db,
		updateTaskErr:     errors.New("数据库写入失败"),
	}
	if _, err := fixture.startService(t, store, nil, nil); err == nil {
		t.Fatal("取消意图补齐写入失败时启动必须返回错误")
	}
	if actual := fixture.task(t, task.ID).Status; actual != domain.TaskStatusDeveloping {
		t.Fatalf("写入失败时任务状态不得变化，实际 %s", actual)
	}
}
