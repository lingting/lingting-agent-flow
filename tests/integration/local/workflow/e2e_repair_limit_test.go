// Package workflow_test 验证单任务端到端验收（P1-M5）的四类终局。
//
// 本文件覆盖场景 C：修复失败或达到上限。
//
// 链路固定为：验证持续失败 → 修复循环在达到上限后停止 → 工作流进入 blocked →
// 任务不进入 completed → 历史 Attempt / Session / Validation Run / 反馈全部可查询 →
// 不执行代码保存 → 不删除失败现场。
package workflow_test

import (
	"testing"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// e2eAlwaysFailReplies 返回"持续失败"的 Agent 调用序列。
//
// 上限为 2 时只会发生 2 轮闭环，因此序列只覆盖规划阶段与第 1 步的两轮开发。
func e2eAlwaysFailReplies() []e2eAgentReply {
	return []e2eAgentReply{
		// Planner 生成计划。
		{status: agent.RunCompleted, finalText: e2ePlanJSON},
		// 规划阶段步骤。
		{status: agent.RunCompleted, finalText: "计划已生成"},
		{status: agent.RunCompleted, finalText: e2eValidatorPassJSON},
		// 第 1 个开发步骤：首轮实现（验证失败）。
		{status: agent.RunCompleted, finalText: "第一次实现"},
		// 修复轮：第二次实现（仍然失败）。
		{status: agent.RunCompleted, finalText: "第二次实现"},
	}
}

// e2eAlwaysFailChecks 返回"规划阶段通过、此后持续失败"的检查结果。
func e2eAlwaysFailChecks() []e2eCheckReply {
	failed := e2eCheckReply{
		exitCode: 1,
		stderr:   "编译错误: undefined: foo",
		started:  true,
		certain:  true,
	}
	return []e2eCheckReply{
		{exitCode: 0, stdout: "ok", started: true, certain: true},
		failed,
		failed,
	}
}

// TestE2ERepairLimitStopsWithoutDelivery 验证达到尝试上限后停止且不进入交付阶段。
func TestE2ERepairLimitStopsWithoutDelivery(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  e2eAlwaysFailReplies(),
		checkSequence: e2eAlwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("达到尝试上限应阻塞，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.ErrorClass != "attempts_exhausted" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	// 数据库中的终态必须与返回值一致。
	storedRun := fixture.reloadRun(t, result.Run.RunID)
	if storedRun.Status != domain.WorkflowRunBlocked {
		t.Fatalf("数据库中的执行记录应为 blocked，实际 %s", storedRun.Status)
	}
	if storedRun.ErrorClass != "attempts_exhausted" {
		t.Fatalf("数据库中的错误分类不正确: %s", storedRun.ErrorClass)
	}
	// 任务不得被伪装为完成。
	task := fixture.reloadTask(t)
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("修复达到上限时任务不得标记为完成")
	}
	if task.FinalResult == domain.TaskResultSucceeded {
		t.Fatalf("修复达到上限时任务结论不得为 succeeded: %s", task.FinalResult)
	}
	// 修复循环必须真正停止，不得无限调用 Agent。
	_, _, agentCalls, checkCalls := fixture.ports.counts()
	if agentCalls != len(e2eAlwaysFailReplies()) {
		t.Fatalf("达到上限后不得继续调用 Agent，实际 %d 次", agentCalls)
	}
	if checkCalls != len(e2eAlwaysFailChecks()) {
		t.Fatalf("达到上限后不得继续执行检查，实际 %d 次", checkCalls)
	}
	// 成功路径的能力一律不得被触发：不保存代码、不产生保存记录。
	if records := fixture.codeSaves(t); len(records) != 0 {
		t.Fatalf("修复失败时不得产生代码保存记录，实际 %d 条", len(records))
	}
}

// TestE2ERepairLimitKeepsFailedHistory 验证失败轮次的执行记录全部可查询。
func TestE2ERepairLimitKeepsFailedHistory(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  e2eAlwaysFailReplies(),
		checkSequence: e2eAlwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	if result := fixture.settle(t); result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("达到尝试上限应阻塞，实际 %s", formatE2ERun(result.Run))
	}

	steps := fixture.reloadSteps(t)
	// 第 1 个开发步骤必须如实收敛为 failed，并释放执行占用。
	if steps[1].Status != domain.StepStatusFailed {
		t.Fatalf("用尽上限的步骤应为 failed，实际 %s", steps[1].Status)
	}
	if steps[1].CurrentAttemptID != nil {
		t.Fatal("failed 步骤必须释放执行占用，允许用户显式重新执行")
	}
	if steps[1].FailureSummary == "" {
		t.Fatal("失败步骤必须留下可解释的失败摘要")
	}
	// 两次失败的 Attempt、Session 与 Validation Run 都必须保留。
	attempts := fixture.attempts(t, steps[1].ID)
	if len(attempts) != 2 {
		t.Fatalf("应保留 2 次失败尝试，实际 %d", len(attempts))
	}
	for index, attempt := range attempts {
		// 开发阶段本身正常结束，因此 Attempt 的执行状态是 completed；本轮失败结论
		// 记录在验证判定与步骤状态上，两者不能混为一个字段。
		if attempt.Status != domain.AttemptStatusCompleted {
			t.Fatalf("尝试 %d 的执行状态应为 completed，实际 %s", index+1, attempt.Status)
		}
		if attempt.VerificationVerdict != domain.VerificationVerdictFailed {
			t.Fatalf("尝试 %d 的验证判定应为 failed，实际 %s", index+1, attempt.VerificationVerdict)
		}
		if len(attempt.SessionIDs) != 1 {
			t.Fatalf("尝试 %d 应关联 1 条会话，实际 %d", index+1, len(attempt.SessionIDs))
		}
		runs := fixture.validationRuns(t, attempt.ID)
		if len(runs) != 1 {
			t.Fatalf("尝试 %d 应保留 1 条验证记录，实际 %d", index+1, len(runs))
		}
		if runs[0].Verdict != domain.VerificationVerdictFailed {
			t.Fatalf("尝试 %d 的验证结论应为 failed，实际 %s", index+1, runs[0].Verdict)
		}
		// 每次失败都必须留下可追溯的反馈，供用户核验与后续显式重试引用。
		if _, err := fixture.db.GetValidationFailureFeedback(fixture.ctx, runs[0].ID); err != nil {
			t.Fatalf("尝试 %d 应保留验证失败反馈: %v", index+1, err)
		}
	}
	// 后续步骤从未启动：不得凭空产生 Attempt 或把步骤标为完成。
	if attempts := fixture.attempts(t, steps[2].ID); len(attempts) != 0 {
		t.Fatalf("未推进到的步骤不得产生尝试，实际 %d", len(attempts))
	}
	if steps[2].Status == domain.StepStatusCompleted {
		t.Fatal("未执行的步骤不得被标记为完成")
	}
	// 全部会话记录保留，且不因为失败而被删除。
	if sessions := fixture.sessionsOf(t); len(sessions) == 0 {
		t.Fatal("失败现场必须保留会话记录")
	}
}

// TestE2ERepairDisabledFailsWithoutRetry 验证未开启自动修复时只执行一轮。
func TestE2ERepairDisabledFailsWithoutRetry(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver: true,
		// 上限为 1，并显式关闭验证失败自动修复：首轮失败即停止。
		//
		// RetryConfig 的默认策略把"未配置任何类别"视为允许验证失败修复，因此
		// 这里必须显式声明一个不包含 verification_failure 的类别列表。
		retry: domain.RetryConfig{
			MaxAttempts:  1,
			RetryClasses: []domain.RetryErrorClass{domain.RetryClassTimeout},
		},
		agentReplies: []e2eAgentReply{
			{status: agent.RunCompleted, finalText: e2ePlanJSON},
			{status: agent.RunCompleted, finalText: "计划已生成"},
			{status: agent.RunCompleted, finalText: e2eValidatorPassJSON},
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		checkSequence: e2eAlwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("未开启自动修复时首轮失败应阻塞，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.ErrorClass != "verification_failed" {
		t.Fatalf("错误分类应为 verification_failed，实际 %s", result.Run.ErrorClass)
	}
	_, _, agentCalls, checkCalls := fixture.ports.counts()
	// 只有 Planner 计划生成、规划阶段步骤、以及第 1 步的一次开发，共 4 次调用。
	if agentCalls != 4 {
		t.Fatalf("未开启修复时不得产生额外 Agent 调用，实际 %d 次", agentCalls)
	}
	// 规划阶段 1 次 + 第 1 步 1 次，共 2 次检查。
	if checkCalls != 2 {
		t.Fatalf("未开启修复时不得重复执行检查，实际 %d 次", checkCalls)
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("验证失败时任务不得标记为完成")
	}
	if records := fixture.codeSaves(t); len(records) != 0 {
		t.Fatalf("验证失败时不得产生代码保存记录，实际 %d 条", len(records))
	}
}

// TestE2ERepairLimitPreservesContainer 验证失败现场的执行环境不被自动删除。
//
// 容器删除只允许由用户显式选择，编排器不得在失败后自动清理：失败现场是人工核验
// 与恢复的唯一依据。
func TestE2ERepairLimitPreservesContainer(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  e2eAlwaysFailReplies(),
		checkSequence: e2eAlwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	if result := fixture.settle(t); result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("达到尝试上限应阻塞，实际 %s", formatE2ERun(result.Run))
	}
	// 任务仍然关联原容器：没有任何链路删除或脱离它。
	task := fixture.reloadTask(t)
	if task.ContainerID != e2eContainerID {
		t.Fatalf("失败后任务必须保留容器关联，实际 %q", task.ContainerID)
	}
	container, err := fixture.db.GetContainer(fixture.ctx, domain.ID(e2eContainerID))
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if container.State != domain.ContainerStateRunning {
		t.Fatalf("失败后容器应仍处于受管理运行态，实际 %s", container.State)
	}
	if container.Detached {
		t.Fatal("失败后不得自动脱离容器管理")
	}
}

// TestE2ERepairLimitSurvivesRestart 验证达到上限后重启不能创建新的执行尝试。
//
// 该用例是 P2-M3 的关键验收点：修复上限必须来自持久化事实。重启后重新打开数据库、
// 重新装配编排器，再通过正式执行入口推进同一任务，Attempt 数量必须保持不变，
// 且步骤必须停在可诊断的停止状态上。
func TestE2ERepairLimitSurvivesRestart(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  e2eAlwaysFailReplies(),
		checkSequence: e2eAlwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	if result := fixture.settle(t); result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("达到尝试上限应阻塞，实际 %s", formatE2ERun(result.Run))
	}
	steps := fixture.reloadSteps(t)
	attemptsBefore := len(fixture.attempts(t, steps[1].ID))
	if attemptsBefore != 2 {
		t.Fatalf("应留下 2 次尝试，实际 %d", attemptsBefore)
	}
	cyclesBefore := e2eCyclesOf(t, fixture, steps[1].ID)
	if len(cyclesBefore) != 1 {
		t.Fatalf("应只有一个执行周期，实际 %d", len(cyclesBefore))
	}
	if cyclesBefore[0].Active() {
		t.Fatal("停止后周期必须结束")
	}
	// 模拟进程退出后重启：同一数据库路径重新打开并重新装配服务。替身端口随之重建，
	// 因此重启后的调用计数从零开始，可以直接断言"重启后没有发生任何调用"。
	fixture.restart(t)

	// 重启后重新推进同一任务：必须被拒绝，不得产生新的 Agent 调用或检查调用。
	if _, err := fixture.workflow.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err == nil {
		t.Fatal("达到上限后重启不得启动新的工作流执行")
	}
	if _, _, agentCallsAfter, checkCallsAfter := fixture.ports.counts(); agentCallsAfter != 0 ||
		checkCallsAfter != 0 {
		t.Fatalf("重启后不得调用 Agent 或执行检查: agent=%d check=%d",
			agentCallsAfter, checkCallsAfter)
	}
	// 数据库事实不变：尝试数量、周期登记与步骤状态都保持原样。
	steps = fixture.reloadSteps(t)
	if attemptsAfter := len(fixture.attempts(t, steps[1].ID)); attemptsAfter != attemptsBefore {
		t.Fatalf("重启后不得创建新尝试: %d → %d", attemptsBefore, attemptsAfter)
	}
	cyclesAfter := e2eCyclesOf(t, fixture, steps[1].ID)
	if len(cyclesAfter) != len(cyclesBefore) {
		t.Fatalf("重启后不得创建新周期: %d → %d", len(cyclesBefore), len(cyclesAfter))
	}
	if cyclesAfter[0].ID != cyclesBefore[0].ID {
		t.Fatalf("重启后周期标识不得变化: %s → %s", cyclesBefore[0].ID, cyclesAfter[0].ID)
	}
}

// e2eCyclesOf 读取步骤的执行周期登记。
func e2eCyclesOf(t *testing.T, fixture *e2eFixture, stepID domain.ID) []domain.RepairCycle {
	t.Helper()
	cycles, err := fixture.db.ListRepairCyclesByStep(fixture.ctx, stepID)
	if err != nil {
		t.Fatalf("读取执行周期失败: %v", err)
	}
	return cycles
}
