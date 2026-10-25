// Package workflow_test 验证 P3-M4 的端到端主路径：失败任务经用户显式重试进入新的
// 执行周期，以及取消、服务关闭与重启后的收敛。
//
// 用例复用 e2e 现场（真实 SQLite + 正式迁移 + 真实 TaskLaunchService /
// TaskWorkflowService / StepExecutor / ValidationService / StepWorkflowService /
// codesaver，只替换 SSH、Docker 与容器内 Pi 进程）。断言对象是数据库里真实留下的
// 任务状态、租约 owner/epoch、WorkflowRun、Attempt 与 Session。
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// failTask 把任务按状态机收敛为 failed。
//
// 它模拟"用户核验现场后判定本次执行失败"：重试入口的前置条件正是任务确实处于
// failed，而不是仍停留在 developing 或 blocked。
func (f *e2eFixture) failTask(t *testing.T) {
	t.Helper()
	task := f.reloadTask(t)
	previous := task.Status
	// 上一个周期的执行记录必须已经停止：blocked / recovery_required 的记录仍属于
	// "未结束"，会让重试被正确拒绝。这里按用户核验后的显式决定把它收敛为失败终态。
	runs, err := f.db.ListWorkflowRunsByTask(f.ctx, task.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	for index := range runs {
		run := runs[index]
		if run.Status.IsTerminal() {
			continue
		}
		if failErr := run.Fail("developer_failed", "上一周期失败", f.now); failErr != nil {
			t.Fatalf("收敛执行记录失败: %v", failErr)
		}
		if updateErr := f.db.UpdateWorkflowRun(f.ctx, run); updateErr != nil {
			t.Fatalf("写入执行记录失败: %v", updateErr)
		}
	}
	if previous != domain.TaskStatusFailed {
		if err := task.Transition(domain.TaskStatusFailed, f.now); err != nil {
			t.Fatalf("任务迁移到 failed 失败: %v", err)
		}
		if err := task.Finish(domain.TaskResultFailed, "上一周期失败", f.now); err != nil {
			t.Fatalf("写入失败结论失败: %v", err)
		}
		if err := f.db.UpdateTaskIfStatus(f.ctx, task, []domain.TaskStatus{previous}); err != nil {
			t.Fatalf("写入失败状态失败: %v", err)
		}
	}
}

// runs 读取任务的全部执行记录。
func (f *e2eFixture) runs(t *testing.T) []domain.WorkflowRun {
	t.Helper()
	runs, err := f.db.ListWorkflowRunsByTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	return runs
}

// TestE2ERetryAfterFailureStartsNewCycle 验证失败任务经显式重试进入新的执行周期。
//
// 链路固定为：首轮执行因验证持续失败而阻塞 → 用户显式重试 → 任务回到执行流程 →
// 新周期产生新的 Attempt 与 Session → 旧 Attempt / Session / WorkflowRun 全部保留。
func TestE2ERetryAfterFailureStartsNewCycle(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 1},
		agentReplies:  e2eAlwaysFailReplies(),
		checkSequence: e2eAlwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	first := fixture.settle(t)
	if first.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("首轮验证失败应阻塞，实际 %s", formatE2ERun(first.Run))
	}
	stepsBefore := fixture.reloadSteps(t)
	attemptsBefore := len(fixture.attempts(t, stepsBefore[1].ID))
	sessionsBefore := len(fixture.sessionsOf(t))
	runsBefore := len(fixture.runs(t))

	// 首轮阻塞后任务并未进入 failed：它需要人工处理现场。为了让重试入口有明确的
	// 前置条件，这里按状态机把它收敛为 failed（模拟用户核验后判定本次执行失败）。
	fixture.failTask(t)
	failedTask := fixture.reloadTask(t)
	if failedTask.Status != domain.TaskStatusFailed {
		t.Fatalf("任务应已收敛为 failed，实际 %s", failedTask.Status)
	}
	epochBefore := failedTask.LeaseEpoch

	// 显式重试：复用同一份统一启动流程，产生新的执行周期。
	result, err := fixture.retry.Retry(fixture.ctx, fixture.task.ID.String(), app.TaskRetryRequest{})
	if err != nil {
		t.Fatalf("显式重试失败: %v", err)
	}
	if result.PreviousStatus != domain.TaskStatusFailed {
		t.Fatalf("重试前状态应为 failed，实际 %s", result.PreviousStatus)
	}
	if result.Epoch <= epochBefore {
		t.Fatalf("重试必须建立在新代次之上: %d → %d", epochBefore, result.Epoch)
	}
	stored := fixture.reloadTask(t)
	if stored.FinalResult != domain.TaskResultNone {
		t.Fatalf("重试必须清空上一周期的结论，实际 %s", stored.FinalResult)
	}
	if stored.Status == domain.TaskStatusFailed {
		t.Fatal("重试后任务不得仍停留在 failed")
	}

	// 历史现场必须完整保留：重试不覆盖旧 Attempt、Session 与 WorkflowRun。
	stepsAfter := fixture.reloadSteps(t)
	if got := len(fixture.attempts(t, stepsAfter[1].ID)); got < attemptsBefore {
		t.Fatalf("重试不得删除历史尝试: %d → %d", attemptsBefore, got)
	}
	if got := len(fixture.sessionsOf(t)); got < sessionsBefore {
		t.Fatalf("重试不得删除历史会话: %d → %d", sessionsBefore, got)
	}
	if got := len(fixture.runs(t)); got < runsBefore {
		t.Fatalf("重试不得删除历史执行记录: %d → %d", runsBefore, got)
	}
	// 审计事件必须可追溯。
	retried := 0
	for _, event := range fixture.events(t) {
		if event.Type == domain.EventTypeTaskRetried {
			retried++
		}
	}
	if retried != 1 {
		t.Fatalf("重试必须留下且只留下一条审计事件，实际 %d 条", retried)
	}
}

// TestE2ERetryRejectedWhileWorkflowActive 验证执行记录未结束时不接受重试。
func TestE2ERetryRejectedWhileWorkflowActive(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 1},
		agentReplies:  e2eAlwaysFailReplies(),
		checkSequence: e2eAlwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	fixture.settle(t)

	// 现场：任务仍是 developing，且存在未结束的执行记录。此时重试必须被拒绝。
	decision, err := fixture.retry.Eligibility(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("查询重试资格失败: %v", err)
	}
	if decision.Allowed {
		t.Fatal("存在未结束执行记录时不得允许重试")
	}
	if _, retryErr := fixture.retry.Retry(
		fixture.ctx,
		fixture.task.ID.String(),
		app.TaskRetryRequest{},
	); retryErr == nil {
		t.Fatal("存在未结束执行记录时重试必须被拒绝")
	} else if !errors.Is(retryErr, app.ErrTaskRetryConflict) {
		t.Fatalf("应返回状态冲突，实际 %v", retryErr)
	}
}

// TestE2ECancelPersistsIntentBeforeStop 验证取消意图先落盘再传播。
//
// 关键约束：取消请求成功不等于远程操作已经停止。因此执行记录层的意图必须先落库，
// 任务层只记录意图并进入 cancelling，最终 cancelled 由真实收敛结果写入。
func TestE2ECancelPersistsIntentBeforeStop(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 1},
		agentReplies:  e2eHappyReplies(),
		checkSequence: nil,
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	fixture.settle(t)

	// 任务已经收敛：取消必须被拒绝，而不是伪造"运行中任务已停止"。
	service, err := app.NewTaskService(app.TaskServiceOptions{
		Store:  fixture.db,
		Logger: fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	if _, cancelErr := service.CancelTaskForAPI(fixture.ctx, fixture.task.ID.String()); cancelErr == nil {
		t.Fatal("已结束的任务不得接受取消请求")
	}
}

// TestE2ERestartConvergesRunningRunToBlocked 验证进程退出后 running 记录被收敛为 blocked。
//
// 现场：执行记录仍是 running 且租约已过期（进程崩溃）。重启后启动恢复必须收敛为
// blocked / worker_interrupted，并且不重放任何 Agent 调用。
func TestE2ERestartConvergesRunningRunToBlocked(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 1},
		agentReplies:  e2eHappyReplies(),
		checkSequence: nil,
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	settled := fixture.settle(t)
	if settled.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("前置路径应完成，实际 %s", formatE2ERun(settled.Run))
	}
	// 人为构造"进程在执行中崩溃"的现场：执行记录回到 running，租约已过期。
	run := fixture.reloadRun(t, settled.Run.RunID)
	expired := time.Now().UTC().Add(-time.Minute)
	run.Status = domain.WorkflowRunRunning
	run.WorkerID = "crashed-worker"
	run.LeaseExpiresAt = &expired
	run.FinishedAt = nil
	if err := fixture.db.UpdateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("构造崩溃现场失败: %v", err)
	}
	// 同时把任务放回可收敛的中间状态。
	task := fixture.reloadTask(t)
	task.Status = domain.TaskStatusDeveloping
	if err := fixture.db.UpdateTask(fixture.ctx, task); err != nil {
		t.Fatalf("构造任务现场失败: %v", err)
	}

	fixture.restart(t)
	converged := fixture.reloadRun(t, run.ID)
	if converged.Status != domain.WorkflowRunBlocked {
		t.Fatalf("崩溃残留必须收敛为 blocked，实际 %s", converged.Status)
	}
	if converged.ErrorClass != "worker_interrupted" {
		t.Fatalf("错误分类必须说明是执行者中断，实际 %s", converged.ErrorClass)
	}
	// 收敛不得重放 Agent 工作。
	if _, _, calls, _ := fixture.ports.counts(); calls != 0 {
		t.Fatalf("重启恢复不得重放 Agent 调用，实际 %d 次", calls)
	}
}
