package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// 启动失败后的取消现场收敛用例。
//
// 背景：取消入口只记录取消请求并把任务推进到 cancelling，真实收敛由执行者确认。启动
// 流程在计划阶段失败时会释放执行权；此时若任务已被取消请求推进到 cancelling，且没有
// 活动执行记录，就没有任何执行者会去收敛它——任务会永久停在 cancelling，需求无法删除。
// 修复后启动失败路径必须按既有恢复规则收口，且不得覆盖仍有在途执行记录的任务。

// cancellationFixture 构造一个"已进入计划阶段"的启动现场。
func cancellationFixture(t *testing.T) *launchFixture {
	t.Helper()
	fixture := newLaunchFixture(t)
	fixture.setContainer("container-cancel")
	fixture.store.workspaceRecords = []domain.TaskWorkspaceInitRecord{
		launchSucceededWorkspaceRecord(t, fixture.store.task.ID, "container-cancel"),
	}
	return fixture
}

// markTaskCancelling 把受控任务推进到 cancelling，模拟用户在计划生成期间请求取消。
func markTaskCancelling(t *testing.T, fixture *launchFixture, cancelRequested bool) {
	t.Helper()
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	task := fixture.store.task
	now := time.Now().UTC()
	if err := task.Transition(domain.TaskStatusPreparing, now); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if cancelRequested {
		if err := task.RequestCancel(now); err != nil {
			t.Fatalf("记录取消请求失败: %v", err)
		}
	}
	if err := task.Transition(domain.TaskStatusCancelling, now); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	fixture.store.task = task
}

// TestLaunchSettlesCancellingTaskWithoutActiveRun 校验启动失败后 cancelling 现场被真实收敛。
func TestLaunchSettlesCancellingTaskWithoutActiveRun(t *testing.T) {
	t.Parallel()
	fixture := cancellationFixture(t)
	// 模拟真实竞争：计划生成期间用户请求取消，任务被推进到 cancelling，随后计划写入失败。
	fixture.planner.onGenerate = func() { markTaskCancelling(t, fixture, true) }
	fixture.planner.err = errors.New("工作流状态冲突: 任务状态已变更，计划未写入")

	if _, err := fixture.service.Launch(context.Background(), appLaunchRequest(fixture)); err == nil {
		t.Fatal("计划生成失败时启动应返回错误")
	}

	task := fixture.loadTask(t)
	if task.Status != domain.TaskStatusCancelled {
		t.Fatalf("取消现场应收敛为 cancelled，实际 %s", task.Status)
	}
	if task.FinalResult != domain.TaskResultCancelled {
		t.Fatalf("收敛时必须写入取消结论，实际 %q", task.FinalResult)
	}
	if !hasTaskEvent(fixture, domain.EventTypeTaskCancelled) {
		t.Fatalf("收敛后应写入取消事件，实际事件 %v", fixture.store.events)
	}
}

// TestLaunchKeepsCancellingTaskWithActiveRun 校验仍有在途执行记录时不覆盖任务状态。
func TestLaunchKeepsCancellingTaskWithActiveRun(t *testing.T) {
	t.Parallel()
	fixture := cancellationFixture(t)
	// 在途执行记录在计划生成期间才出现：启动流程已经决定进入计划阶段，此时另一个
	// 执行入口的记录仍未结束，收敛责任属于执行者。
	fixture.planner.onGenerate = func() {
		fixture.store.mu.Lock()
		fixture.store.activeRun = true
		fixture.store.mu.Unlock()
		markTaskCancelling(t, fixture, true)
	}
	fixture.planner.err = errors.New("工作流状态冲突: 任务状态已变更，计划未写入")

	if _, err := fixture.service.Launch(context.Background(), appLaunchRequest(fixture)); err == nil {
		t.Fatal("计划生成失败时启动应返回错误")
	}
	task := fixture.loadTask(t)
	if task.Status != domain.TaskStatusCancelling {
		t.Fatalf("仍有在途执行记录时不得改写任务状态，实际 %s", task.Status)
	}
}

// TestLaunchSettlesCancellingTaskAsRecoveryWhenCancelMissing 校验没有取消请求时收敛为待核验。
func TestLaunchSettlesCancellingTaskAsRecoveryWhenCancelMissing(t *testing.T) {
	t.Parallel()
	fixture := cancellationFixture(t)
	fixture.planner.onGenerate = func() { markTaskCancelling(t, fixture, false) }
	fixture.planner.err = errors.New("工作流状态冲突: 任务状态已变更，计划未写入")

	if _, err := fixture.service.Launch(context.Background(), appLaunchRequest(fixture)); err == nil {
		t.Fatal("计划生成失败时启动应返回错误")
	}
	task := fixture.loadTask(t)
	if task.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("没有取消请求时应收敛为 recovery_required，实际 %s", task.Status)
	}
}

// appLaunchRequest 返回一次只带任务标识的启动请求。
func appLaunchRequest(fixture *launchFixture) app.TaskLaunchRequest {
	return app.TaskLaunchRequest{TaskID: fixture.store.task.ID.String()}
}

// loadTask 读取受控任务记录。
func (f *launchFixture) loadTask(t *testing.T) domain.Task {
	t.Helper()
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return f.store.task
}

// hasTaskEvent 判断受控事件列表里是否包含指定事件类型。
func hasTaskEvent(fixture *launchFixture, eventType domain.EventType) bool {
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	for _, recorded := range fixture.store.events {
		if recorded == eventType {
			return true
		}
	}
	return false
}
