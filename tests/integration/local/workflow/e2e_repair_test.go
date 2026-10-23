// Package workflow_test 验证单任务端到端验收（P1-M5）的四类终局。
//
// 本文件覆盖场景 B：验证失败后修复并重验成功。
//
// 链路固定为：首轮必需确定性检查失败 → 结构化反馈落库 → 修复产生新的 Attempt /
// Session / Validation Run → 重验通过 → 继续推进后续步骤 → 代码保存 → 会话归档 →
// 领域收尾决策 → 任务 completed。
package workflow_test

import (
	"testing"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
)

// e2eRepairThenPassReplies 返回"首轮失败、修复后通过"的 Agent 调用序列。
//
// 第 1 个开发步骤会有两轮闭环：首轮开发 → 验证失败 → 依据反馈修复 → 重验通过。
func e2eRepairThenPassReplies() []e2eAgentReply {
	pass := e2eValidatorPassJSON
	return []e2eAgentReply{
		// Planner 生成计划。
		{status: agent.RunCompleted, finalText: e2ePlanJSON},
		// 规划阶段步骤。
		{status: agent.RunCompleted, finalText: "计划已生成"},
		{status: agent.RunCompleted, finalText: pass},
		// 第 1 个开发步骤：首轮实现（验证失败）。
		{status: agent.RunCompleted, finalText: "第一次实现"},
		// 修复轮：第二次实现（验证通过）。
		{status: agent.RunCompleted, finalText: "根据反馈修复"},
		{status: agent.RunCompleted, finalText: pass},
		// 第 2 个开发步骤。
		{status: agent.RunCompleted, finalText: "测试补齐"},
		{status: agent.RunCompleted, finalText: pass},
	}
}

// e2eFailThenPassChecks 返回"首轮构建失败、之后全部通过"的检查结果序列。
//
// 检查按调用顺序返回：规划阶段步骤、第 1 步首轮（失败）、第 1 步重验、第 2 步。
func e2eFailThenPassChecks() []e2eCheckReply {
	return []e2eCheckReply{
		{exitCode: 0, stdout: "ok", started: true, certain: true},
		{exitCode: 1, stderr: "编译错误: undefined: foo", started: true, certain: true},
		{exitCode: 0, stdout: "ok", started: true, certain: true},
		{exitCode: 0, stdout: "ok", started: true, certain: true},
	}
}

// TestE2ERepairThenReverifyReachesCompleted 验证首轮验证失败后修复重验成功并完成交付。
func TestE2ERepairThenReverifyReachesCompleted(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  e2eRepairThenPassReplies(),
		checkSequence: e2eFailThenPassChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("修复后应完成交付，实际 %s", formatE2ERun(result.Run))
	}
	storedRun := fixture.reloadRun(t, result.Run.RunID)
	if storedRun.Status != domain.WorkflowRunCompleted {
		t.Fatalf("数据库中的执行记录应为 completed，实际 %s", storedRun.Status)
	}
	task := fixture.reloadTask(t)
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("任务应为 completed，实际 %s（%s）", task.Status, task.FailureSummary)
	}
	if task.FinalResult != domain.TaskResultSucceeded {
		t.Fatalf("任务最终结论应为 succeeded，实际 %s", task.FinalResult)
	}
	// 只有最终验证通过后才继续推进：两个开发步骤都必须完成。
	for _, step := range fixture.reloadSteps(t) {
		if step.Status != domain.StepStatusCompleted {
			t.Fatalf("步骤 %d 应为 completed，实际 %s", step.Sequence, step.Status)
		}
	}
	// 后续交付条件必须同时成立：代码保存成功且全部会话已归档。
	records := fixture.codeSaves(t)
	if len(records) != 1 {
		t.Fatalf("修复成功后只应执行一次代码保存，实际 %d 次", len(records))
	}
	if records[0].PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("代码保存必须推送成功，实际 %s", records[0].PushStatus)
	}
	for _, session := range fixture.sessionsOf(t) {
		if session.ArchiveStatus != domain.SessionArchiveSucceeded {
			t.Fatalf("会话 %s 必须归档成功，实际 %s", session.ID, session.ArchiveStatus)
		}
	}
}

// TestE2ERepairKeepsFailedRoundEvidence 验证首轮失败的历史证据全部保留。
func TestE2ERepairKeepsFailedRoundEvidence(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  e2eRepairThenPassReplies(),
		checkSequence: e2eFailThenPassChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	if result := fixture.settle(t); result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("修复后应完成交付，实际 %s", formatE2ERun(result.Run))
	}

	steps := fixture.reloadSteps(t)
	// 第 1 步经历两轮：首轮失败与修复都必须保留为独立 Attempt。
	firstAttempts := fixture.attempts(t, steps[1].ID)
	if len(firstAttempts) != 2 {
		t.Fatalf("第 1 个开发步骤应产生 2 次尝试，实际 %d", len(firstAttempts))
	}
	if firstAttempts[0].ID == firstAttempts[1].ID {
		t.Fatal("历史尝试不得被覆盖")
	}
	if firstAttempts[0].AttemptNumber == firstAttempts[1].AttemptNumber {
		t.Fatalf(
			"尝试序号必须递增: %d 与 %d",
			firstAttempts[0].AttemptNumber,
			firstAttempts[1].AttemptNumber,
		)
	}
	// 首轮验证必须真实记录失败，并绑定该轮的 Attempt 与 Session。
	firstRuns := fixture.validationRuns(t, firstAttempts[0].ID)
	if len(firstRuns) != 1 {
		t.Fatalf("首轮尝试应有 1 条验证记录，实际 %d", len(firstRuns))
	}
	if firstRuns[0].Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("首轮验证结论应为 failed，实际 %s", firstRuns[0].Verdict)
	}
	if firstRuns[0].FailedChecks == 0 {
		t.Fatal("首轮验证必须记录失败检查数")
	}
	if len(firstAttempts[0].SessionIDs) != 1 {
		t.Fatalf("首轮尝试应关联 1 条会话，实际 %d", len(firstAttempts[0].SessionIDs))
	}
	// 修复轮必须产生新的验证记录，且最终通过。
	secondRuns := fixture.validationRuns(t, firstAttempts[1].ID)
	if len(secondRuns) != 1 {
		t.Fatalf("修复轮应有 1 条验证记录，实际 %d", len(secondRuns))
	}
	if secondRuns[0].Verdict != domain.VerificationVerdictPassed {
		t.Fatalf("修复轮验证结论应为 passed，实际 %s", secondRuns[0].Verdict)
	}
	if secondRuns[0].ID == firstRuns[0].ID {
		t.Fatal("修复轮必须创建新的验证记录，不得复用首轮记录")
	}
	// 第 2 步只有 1 次尝试：前面的失败没有污染后续步骤。
	if attempts := fixture.attempts(t, steps[2].ID); len(attempts) != 1 {
		t.Fatalf("第 2 个开发步骤应只产生 1 次尝试，实际 %d", len(attempts))
	}
}

// TestE2ERepairFeedbackLinkedToFailedRun 验证修复反馈与原 Validation Run 正确关联。
func TestE2ERepairFeedbackLinkedToFailedRun(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  e2eRepairThenPassReplies(),
		checkSequence: e2eFailThenPassChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	if result := fixture.settle(t); result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("修复后应完成交付，实际 %s", formatE2ERun(result.Run))
	}

	steps := fixture.reloadSteps(t)
	attempts := fixture.attempts(t, steps[1].ID)
	if len(attempts) != 2 {
		t.Fatalf("第 1 个开发步骤应产生 2 次尝试，实际 %d", len(attempts))
	}
	firstRuns := fixture.validationRuns(t, attempts[0].ID)
	feedback, err := fixture.db.GetValidationFailureFeedback(fixture.ctx, firstRuns[0].ID)
	if err != nil {
		t.Fatalf("读取验证失败反馈失败: %v", err)
	}
	if feedback.ValidationRunID != firstRuns[0].ID {
		t.Fatalf(
			"反馈必须精确引用首轮验证记录: %s != %s",
			feedback.ValidationRunID,
			firstRuns[0].ID,
		)
	}
	if feedback.TaskID != fixture.task.ID || feedback.StepID != steps[1].ID {
		t.Fatalf("反馈归属不正确: %s / %s", feedback.TaskID, feedback.StepID)
	}
	if feedback.DeveloperAttemptID != attempts[0].ID {
		t.Fatalf("反馈必须引用首轮开发尝试: %s != %s", feedback.DeveloperAttemptID, attempts[0].ID)
	}
	if len(feedback.Checks) == 0 {
		t.Fatal("反馈必须包含失败检查清单")
	}
	// 反馈只能来自已完成的失败轮次：修复轮通过后不产生新的失败反馈。
	secondRuns := fixture.validationRuns(t, attempts[1].ID)
	if _, err := fixture.db.GetValidationFailureFeedback(fixture.ctx, secondRuns[0].ID); err == nil {
		t.Fatal("修复轮已通过验证，不应产生失败反馈")
	}
}

// TestE2ERepairPromptCarriesFeedback 验证修复轮提示词确实携带上一轮失败依据。
func TestE2ERepairPromptCarriesFeedback(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:     true,
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  e2eRepairThenPassReplies(),
		checkSequence: e2eFailThenPassChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	if result := fixture.settle(t); result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("修复后应完成交付，实际 %s", formatE2ERun(result.Run))
	}

	prompts := fixture.ports.agentPrompts()
	if len(prompts) != len(e2eRepairThenPassReplies()) {
		t.Fatalf("Agent 调用次数不符: %d", len(prompts))
	}
	// 首轮开发提示词不得声称存在"上一次失败"，修复轮必须携带该依据。
	firstDeveloper := prompts[e2eRepairNoteIndex]
	repairDeveloper := prompts[e2eRepairNoteIndex+1]
	if indexOfE2EText(firstDeveloper, "上一次验证未通过") >= 0 {
		t.Fatal("首轮开发提示词不应包含上一轮失败依据")
	}
	if indexOfE2EText(repairDeveloper, "上一次验证未通过") < 0 {
		t.Fatalf("修复轮提示词必须携带上一轮失败依据: %s", repairDeveloper)
	}
	if indexOfE2EText(repairDeveloper, "undefined: foo") < 0 {
		t.Fatalf("修复轮提示词必须包含真实失败原因: %s", repairDeveloper)
	}
}

// e2eRepairNoteIndex 是第 1 个开发步骤首轮 Developer 调用在提示词序列中的下标。
//
// 序列前 3 项分别是 Planner 计划生成、规划阶段步骤的 Developer 与其 Validator
// 审查，因此第 1 个开发步骤的首轮 Developer 是第 4 项（下标 3）。
const e2eRepairNoteIndex = 3

// indexOfE2EText 返回子串首次出现的字节下标，未出现时返回 -1。
func indexOfE2EText(text, sub string) int {
	for index := 0; index+len(sub) <= len(text); index++ {
		if text[index:index+len(sub)] == sub {
			return index
		}
	}
	return -1
}
