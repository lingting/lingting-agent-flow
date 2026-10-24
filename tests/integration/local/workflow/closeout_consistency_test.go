// 任务收尾一致性的集成测试。
//
// 这些用例验证的是"任务结论不会被伪造成功"这一条不变量，断言对象是数据库里的真实
// 记录与真实的写入顺序，而不是方法的返回值。外部执行（容器内 Agent、检查命令、
// 代码保存、会话归档）由受控替身提供，业务判定与持久化路径全部走生产代码。
package workflow_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// TestCloseoutWritesRunBeforeTask 验证收尾写入顺序是"先执行记录、后任务"。
//
// 顺序是不变量而不是实现细节：只有执行记录的条件写入成功（栅栏通过），才允许把
// 任务标记为成功。反过来（先写任务）会在收尾失败时留下"任务成功、执行记录未完成"
// 的现场，而且因为 completed 是终态，任何恢复都无法修正它。
func TestCloseoutWritesRunBeforeTask(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver: true,
	})
	fixture.start(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("成功链路应完成，实际 %s（%s）", result.Run.Status, result.Run.ErrorClass)
	}
	order := fixture.store.writeOrder()
	if !firstBeforeLast(order, "run:completed", "task:completed") {
		t.Fatalf("执行记录的 completed 必须在任务 completed 之前写入，实际顺序: %v", order)
	}
	task := fixture.reloadTask(t)
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("任务应完成，实际 %s", task.Status)
	}
	if task.FinalResult != domain.TaskResultSucceeded {
		t.Fatalf("任务结论应为成功，实际 %s", task.FinalResult)
	}
}

// TestCloseoutTaskWriteFailsAfterRunCompleted 验证任务写入失败不伪造成功。
func TestCloseoutTaskWriteFailsAfterRunCompleted(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver:            true,
		failTaskUpdateStatus: domain.TaskStatusCompleted,
	})
	fixture.start(t)
	result := fixture.settle(t)

	// 执行记录必须已经确认完成：写入顺序把栅栏放在任务之前。
	stored := fixture.reloadRun(t, result.Run.RunID)
	if stored.Status != domain.WorkflowRunCompleted {
		t.Fatalf("执行记录应已完成，实际 %s", stored.Status)
	}
	// 任务绝不能是 completed：这是本用例的核心断言。
	task := fixture.reloadTask(t)
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("任务状态写入失败时不得留下 completed")
	}
	if task.FinalResult == domain.TaskResultSucceeded {
		t.Fatal("任务结论写入失败时不得留下成功结论")
	}
	// 收尾失败后任务必须收敛为可继续处理的状态，而不是停在 verifying / saving。
	if task.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("任务应收敛为 recovery_required，实际 %s", task.Status)
	}
	// 对外派生结论必须是"未完成"，不能因为执行记录完成就宣称成功。
	closeout := taskCloseoutState(t, fixture, task)
	if closeout == domain.TaskCloseoutConfirmed {
		t.Fatal("任务结论未落盘时派生结论不得是 confirmed")
	}
}

// TestCloseoutRunWriteFailsBeforeTaskWrite 验证执行记录写入失败时不写任务成功。
func TestCloseoutRunWriteFailsBeforeTaskWrite(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver: true,
	})
	// 只让"收尾写入 completed"这一步失败：阶段推进与任务状态写入都成功。
	fixture.store.failUpdateWhenStatus = domain.WorkflowRunCompleted
	fixture.start(t)
	result := fixture.settle(t)
	if result.Run.Status == domain.WorkflowRunCompleted {
		t.Fatal("执行记录写入失败时不得报告完成")
	}
	if result.Run.ErrorClass != "closeout_failed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	// 栅栏未通过：任务不得被写成 completed。
	task := fixture.reloadTask(t)
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("执行记录写入失败时任务不得标记为完成")
	}
	if task.FinalResult == domain.TaskResultSucceeded {
		t.Fatal("执行记录写入失败时不得写入成功结论")
	}
	stored := fixture.reloadRun(t, result.Run.RunID)
	if stored.Status == domain.WorkflowRunCompleted {
		t.Fatal("数据库中同样不得出现 completed 的执行记录")
	}
}

// TestRestartConvergesTaskStuckInSaving 验证停滞在 saving 的任务被启动恢复收敛。
//
// 该用例覆盖此前的一个真实阻塞：verifying / saving 既不在 CheckTaskLaunchStart 的
// 准入集合里，也不在 AllowsStepExecution 里，任务会永久无法启动也无法恢复。
func TestRestartConvergesTaskStuckInSaving(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver: true,
	})
	// 构造现场：任务已进入 saving，执行记录停在 blocked（进程在收尾前退出）。
	stuck := fixture.task
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusDeveloping,
		domain.TaskStatusVerifying,
		domain.TaskStatusSaving,
	} {
		if err := stuck.Transition(status, fixture.now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", status, err)
		}
	}
	if err := fixture.db.UpdateTask(fixture.ctx, stuck); err != nil {
		t.Fatalf("写入停滞任务失败: %v", err)
	}
	run, err := domain.NewWorkflowRun(stuck.ID, fixture.steps, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("worker-crashed", time.Minute, fixture.now.Add(-time.Hour)); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}

	// 重新启动编排器：启动恢复必须把停滞任务收敛为可继续处理的状态。
	restarted := fixture.restartWorkflow(t)
	recovered, err := fixture.db.GetTask(fixture.ctx, stuck.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if recovered.Status == domain.TaskStatusSaving || recovered.Status == domain.TaskStatusVerifying {
		t.Fatalf("停滞任务必须被收敛，实际仍是 %s", recovered.Status)
	}
	if recovered.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("无完成证据时应收敛为 recovery_required，实际 %s", recovered.Status)
	}
	// 收敛后任务必须可以被显式继续：检查不再被"状态不允许"挡住。
	if _, err := restarted.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: stuck.ID.String(),
	}); err != nil && strings.Contains(err.Error(), "不允许启动工作流执行") {
		t.Fatalf("收敛后的任务仍被状态拒绝: %v", err)
	}
}

// TestRestartCompletesTaskFromCompletedRun 验证证据齐备时补齐成功终态。
func TestRestartCompletesTaskFromCompletedRun(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver: true,
	})
	// 现场：步骤全部完成、执行记录已完成、代码保存与会话归档齐备，只有任务停在 saving。
	for index := range fixture.steps {
		step := fixture.steps[index]
		if err := step.Transition(domain.StepStatusRunning, fixture.now); err != nil {
			t.Fatalf("步骤迁移失败: %v", err)
		}
		if err := step.Transition(domain.StepStatusCompleted, fixture.now); err != nil {
			t.Fatalf("步骤迁移失败: %v", err)
		}
		if err := fixture.db.UpdateStep(fixture.ctx, step); err != nil {
			t.Fatalf("更新步骤失败: %v", err)
		}
	}
	completedSession := completeDeliverySession(t, fixture)
	record := deliveryCodeSaveRecord()
	record.TaskID = fixture.task.ID.String()
	// 任务分支必须与任务标识一致：这是代码保存记录的基本校验。
	record.TaskBranch = "agent/" + fixture.task.ID.String()
	record.RemoteCommitSHA = record.CommitSHA
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	_ = completedSession
	stuck := fixture.task
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusDeveloping,
		domain.TaskStatusVerifying,
		domain.TaskStatusSaving,
	} {
		if err := stuck.Transition(status, fixture.now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", status, err)
		}
	}
	if err := fixture.db.UpdateTask(fixture.ctx, stuck); err != nil {
		t.Fatalf("写入停滞任务失败: %v", err)
	}
	run, err := domain.NewWorkflowRun(stuck.ID, fixture.steps, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("worker", time.Hour, fixture.now); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := run.Complete("全部必需步骤已通过验证", fixture.now); err != nil {
		t.Fatalf("标记完成失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}

	_ = fixture.restartWorkflow(t)
	recovered, err := fixture.db.GetTask(fixture.ctx, stuck.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if recovered.Status != domain.TaskStatusCompleted {
		t.Fatalf("证据齐备时应补齐完成终态，实际 %s（%s）", recovered.Status, recovered.FailureSummary)
	}
	if recovered.FinalResult != domain.TaskResultSucceeded {
		t.Fatalf("任务结论应为成功，实际 %s", recovered.FinalResult)
	}
	// 执行记录不得被重复改写：它已是终态。
	stored, err := fixture.db.GetWorkflowRun(fixture.ctx, run.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if stored.Status != domain.WorkflowRunCompleted {
		t.Fatalf("执行记录应保持 completed，实际 %s", stored.Status)
	}
}

// TestRestartFlagsLegacyCompletedWithRecoveryRun 验证遗留不一致只被报告不被改写。
func TestRestartFlagsLegacyCompletedWithRecoveryRun(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver: true,
	})
	// 构造遗留现场：任务被标记为 completed，执行记录却是 recovery_required。
	legacy := fixture.task
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusDeveloping,
		domain.TaskStatusVerifying,
		domain.TaskStatusSaving,
		domain.TaskStatusCompleted,
	} {
		if err := legacy.Transition(status, fixture.now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", status, err)
		}
	}
	if err := legacy.Finish(domain.TaskResultSucceeded, "历史结论", fixture.now); err != nil {
		t.Fatalf("写入任务结论失败: %v", err)
	}
	if err := fixture.db.UpdateTask(fixture.ctx, legacy); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	run, err := domain.NewWorkflowRun(legacy.ID, fixture.steps, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("worker", time.Hour, fixture.now); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := run.Stop(
		domain.WorkflowRunRecoveryRequired,
		"closeout_failed",
		"收尾写入失败",
		fixture.now,
	); err != nil {
		t.Fatalf("收敛执行记录失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}

	_ = fixture.restartWorkflow(t)
	// 任务状态不得被改写：completed 是终态，回退会让"已完成"不再可信。
	stored, err := fixture.db.GetTask(fixture.ctx, legacy.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.Status != domain.TaskStatusCompleted {
		t.Fatalf("遗留任务的终态不得被恢复改写，实际 %s", stored.Status)
	}
	// 但派生结论必须表达"不一致"，并且事件里留下可追溯记录。
	closeout := taskCloseoutState(t, fixture, *stored)
	if closeout != domain.TaskCloseoutInconsistent {
		t.Fatalf("派生结论应为 inconsistent，实际 %s", closeout)
	}
	found := false
	for _, event := range fixture.events(t) {
		if event.Type == domain.EventTypeTaskRecoveryRequired &&
			strings.Contains(string(event.Payload), "closeout_reconcile") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("遗留不一致现场必须留下可追溯事件")
	}
}

// firstBeforeLast 判断 before 的首次出现是否早于 after 的首次出现。
//
// 收尾会多次写入执行记录（阶段推进与终态），因此只能按"首次出现"比较：
// 阶段写入天然早于任务收尾，断言的重点是终态写入的先后。
func firstBeforeLast(order []string, before, after string) bool {
	beforeIndex := -1
	afterIndex := -1
	for index, write := range order {
		if write == before && beforeIndex < 0 {
			beforeIndex = index
		}
		if write == after && afterIndex < 0 {
			afterIndex = index
		}
	}
	if beforeIndex < 0 || afterIndex < 0 {
		return false
	}
	return beforeIndex < afterIndex
}

// taskCloseoutState 通过真实应用服务派生任务结论的闭环状态。
func taskCloseoutState(
	t *testing.T,
	fixture *deliveryFixture,
	task domain.Task,
) domain.TaskCloseoutState {
	t.Helper()
	service, err := app.NewTaskService(app.TaskServiceOptions{
		Store:  fixture.db,
		Logger: fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	views, err := service.TaskCloseoutViews(fixture.ctx, []domain.Task{task})
	if err != nil {
		t.Fatalf("派生收尾结论失败: %v", err)
	}
	return views[task.ID]
}

// completeDeliverySession 写入一条满足归档要求的会话记录。
func completeDeliverySession(t *testing.T, fixture *deliveryFixture) domain.Session {
	t.Helper()
	role, err := fixture.db.GetRole(fixture.ctx, fixture.task.Config.DeveloperRoleID)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	session, err := domain.NewSession(
		domain.SessionScope{TaskID: fixture.task.ID},
		*role,
		"pi",
		role.Model,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	if err := session.MarkRunning("pi-session-closeout"); err != nil {
		t.Fatalf("标记会话运行失败: %v", err)
	}
	session.Completeness = domain.SessionCompletenessComplete
	session.ExportStatus = domain.SessionExportNotAttempted
	session.JSONLPath = "sessions/session.jsonl"
	session.ArchiveStatus = domain.SessionArchiveSucceeded
	session.ArchivePath = "sessions/archive.jsonl"
	session.RunStatus = domain.SessionRunCompleted
	if err := fixture.db.CreateSession(fixture.ctx, session); err != nil {
		t.Fatalf("写入会话失败: %v", err)
	}
	if err := fixture.db.UpdateSession(fixture.ctx, session); err != nil {
		t.Fatalf("更新会话失败: %v", err)
	}
	return session
}
