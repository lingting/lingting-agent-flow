// Package workflow_test 验证单任务端到端验收（P1-M5）的四类终局。
//
// 本文件补充两类必须证明的链路行为：确定性检查全部通过、但 Validator Agent 语义
// 审查判定不通过时，(1) 结论必须被收紧且不得被当作"结果不确定"，(2) 修复循环必须
// 能依据语义审查给出结构化反馈并重新验证，并在上限处停止。
//
// 之所以需要链路级证据：`domain.DecideValidation` 的纯函数用例与
// `app.ValidationService` 的服务级用例都不证明"编排器确实消费了收紧后的结论"。
// 这里断言的对象是数据库中的 WorkflowRun / Step / Attempt / ValidationRun /
// ValidationFailureFeedback，而不是方法返回值。
package workflow_test

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
)

// e2eValidatorFailJSON 是 Validator 判定代码不满足验收条件的评估输出。
//
// 它与 e2eValidatorPassJSON 的唯一差别是 passed / suggest_fix：确定性检查每次都
// 通过，因此任何结论变化都只能来自语义审查。
const e2eValidatorFailJSON = `{"passed":false,"summary":"实现缺少错误处理",` +
	`"evidence":["internal/feature.go"],"suggest_fix":true,"needs_human_review":false}`

// e2eReviewFailReplies 返回"规划阶段审查通过、第一个开发步骤审查不通过"的调用序列。
//
// 调用顺序由生产编排决定：Planner 生成计划后，规划阶段步骤与每个开发步骤各执行
// 一次开发调用与一次语义审查调用。第 2 个开发步骤不会被调用。
func e2eReviewFailReplies() []e2eAgentReply {
	return []e2eAgentReply{
		// Planner 生成计划。
		{status: agent.RunCompleted, finalText: e2ePlanJSON},
		// 规划阶段步骤：开发调用 + 语义审查通过。
		{status: agent.RunCompleted, finalText: "计划已生成"},
		{status: agent.RunCompleted, finalText: e2eValidatorPassJSON},
		// 第 1 个开发步骤：开发调用 + 语义审查不通过。
		{status: agent.RunCompleted, finalText: "实现完成"},
		{status: agent.RunCompleted, finalText: e2eValidatorFailJSON},
	}
}

// e2eReviewFailTwiceReplies 返回"同一开发步骤连续两轮语义审查都不通过"的调用序列。
//
// 用于验证修复循环在"失败只来自语义审查"时同样会带着上一轮结构化反馈重试，
// 并在尝试上限处停止，而不是继续调用 Agent。
func e2eReviewFailTwiceReplies() []e2eAgentReply {
	replies := e2eReviewFailReplies()
	return append(replies,
		// 第 2 轮修复：重新开发 + 重新验证（仍然不通过）。
		e2eAgentReply{status: agent.RunCompleted, finalText: "补齐错误处理"},
		e2eAgentReply{status: agent.RunCompleted, finalText: e2eValidatorFailJSON},
	)
}

// TestE2EValidatorReviewFailureBlocksCompletion 验证语义审查未通过时不得完成任务。
func TestE2EValidatorReviewFailureBlocksCompletion(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver: true,
		// 显式关闭验证失败自动修复：重试类别只声明超时，因此首轮失败即停止。
		retry: domain.RetryConfig{
			MaxAttempts:  1,
			RetryClasses: []domain.RetryErrorClass{domain.RetryClassTimeout},
		},
		agentReplies: e2eReviewFailReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)

	// 结论必须停在可恢复的阻塞状态，而且原因必须是"验证未通过"，
	// 不能把一次确定的审查失败写成"结果无法确认"。
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("语义审查未通过时工作流应阻塞，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.ErrorClass != "verification_failed" {
		t.Fatalf("错误分类应为 verification_failed，实际 %s", result.Run.ErrorClass)
	}
	// 数据库是唯一真相来源：返回值与持久化事实必须一致。
	storedRun := fixture.reloadRun(t, result.Run.RunID)
	if storedRun.Status != domain.WorkflowRunBlocked {
		t.Fatalf("数据库中的执行记录应为 blocked，实际 %s", storedRun.Status)
	}
	if storedRun.ErrorClass != "verification_failed" {
		t.Fatalf("数据库中的错误分类不正确: %s", storedRun.ErrorClass)
	}

	// 任务绝不能因为"确定性检查通过"就被声明成功。
	task := fixture.reloadTask(t)
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("语义审查未通过时任务不得标记为完成")
	}
	if task.FinalResult == domain.TaskResultSucceeded {
		t.Fatalf("语义审查未通过时任务结论不得为 succeeded: %s", task.FinalResult)
	}
	// 交付副作用一律不得被触发。
	if records := fixture.codeSaves(t); len(records) != 0 {
		t.Fatalf("验证未通过时不得产生代码保存记录，实际 %d 条", len(records))
	}

	// 步骤收敛：规划阶段完成，第一个开发步骤验证未通过，第二个步骤未被执行。
	steps := fixture.reloadSteps(t)
	if len(steps) != 3 {
		t.Fatalf("计划应有 1 个规划阶段与 2 个开发步骤，实际 %d 个", len(steps))
	}
	if steps[0].Status != domain.StepStatusCompleted {
		t.Fatalf("规划阶段步骤应完成，实际 %s", steps[0].Status)
	}
	if steps[1].Status != domain.StepStatusFailed {
		t.Fatalf("第一个开发步骤应验证失败，实际 %s", steps[1].Status)
	}
	if steps[1].CurrentAttemptID != nil {
		t.Fatal("验证失败的步骤必须释放执行占用")
	}
	if steps[2].Status != domain.StepStatusPending {
		t.Fatalf("未执行的步骤应保持 pending，实际 %s", steps[2].Status)
	}

	validation := singleValidationRun(t, fixture, steps[1].ID)
	// 验证证据：确定性检查全部通过，结论只由语义审查收紧。
	if validation.Status != domain.ValidationRunStatusCompleted {
		t.Fatalf("确定性检查流程应正常跑完，实际 %s", validation.Status)
	}
	if validation.FailedChecks != 0 || validation.PassedChecks < 1 {
		t.Fatalf("确定性检查应全部通过，实际通过 %d、失败 %d",
			validation.PassedChecks, validation.FailedChecks)
	}
	if validation.ReviewStatus != domain.ReviewStatusFailed {
		t.Fatalf("语义审查状态应为 failed，实际 %s", validation.ReviewStatus)
	}
	if validation.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("最终结论应为 failed，实际 %s", validation.Verdict)
	}
	if validation.Assessment == nil || validation.Assessment.Passed {
		t.Fatalf("未通过的结构化评估必须落库: %+v", validation.Assessment)
	}
	// 审查使用独立会话：它与开发会话都挂在该尝试之下，但必须是两条记录。
	if validation.ValidatorSessionID == nil {
		t.Fatal("语义审查必须留下独立会话标识")
	}
	sessions := fixture.sessionsOf(t)
	attemptSessions := 0
	developerSessionID := domain.ID("")
	developerRoleID := domain.ID("")
	for _, session := range sessions {
		if session.ID == *validation.ValidatorSessionID {
			// 审查会话使用任务声明的验证角色，而不是开发角色。
			if session.RoleID != fixture.reloadTask(t).Config.ValidatorRoleID {
				t.Fatalf("语义审查会话角色不正确: %s", session.RoleID)
			}
			continue
		}
		if session.AttemptID == nil || *session.AttemptID != validation.AttemptID {
			continue
		}
		attemptSessions++
		developerSessionID = session.ID
		developerRoleID = session.RoleID
	}
	if attemptSessions != 1 {
		t.Fatalf("同一尝试下应只有一条开发会话，实际 %d 条", attemptSessions)
	}
	// 两条会话的标识与角色都不同：审查没有复用开发会话。
	if developerSessionID == *validation.ValidatorSessionID {
		t.Fatal("语义审查不得复用开发会话")
	}
	if developerRoleID == fixture.reloadTask(t).Config.ValidatorRoleID {
		t.Fatal("开发会话不应使用验证角色")
	}
	// 循环必须真正停止：没有自动重试，也没有继续调用 Agent。
	_, _, agentCalls, _ := fixture.ports.counts()
	if agentCalls != len(e2eReviewFailReplies()) {
		t.Fatalf("验证未通过且未开启自动修复时不得继续调用 Agent，实际 %d 次", agentCalls)
	}
	// 每次调用都必须留下独立会话记录，且原始会话都已完成归档。
	if len(sessions) != len(e2eReviewFailReplies()) {
		t.Fatalf("每次 Agent 调用都应有独立会话，实际 %d 条", len(sessions))
	}
	for _, session := range sessions {
		if session.ArchiveStatus != domain.SessionArchiveSucceeded {
			t.Fatalf("会话 %s 必须归档成功，实际 %s", session.ID, session.ArchiveStatus)
		}
	}
}

// TestE2EValidatorReviewFailureFeedsRepairLoop 验证语义审查失败也能驱动有上限的修复循环。
//
// 缺口背景：结构化反馈过去只由"未通过的确定性检查"生成。失败只来自语义审查时，
// 反馈不会被写入，修复循环因此在 `feedback_unavailable` 处停止，并被错误地归类为
// "验证证据未可靠写入"。本用例锁定修复后的行为：反馈落库、下一轮开发引用它、
// 上限处停止，且历史尝试全部保留。
func TestE2EValidatorReviewFailureFeedsRepairLoop(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		retry:        domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies: e2eReviewFailTwiceReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)

	// 两轮都用完预算后停止：这是"有上限的闭环"，不是无限重试。
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("达到尝试上限应阻塞，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.ErrorClass != "attempts_exhausted" {
		t.Fatalf("错误分类应为 attempts_exhausted，实际 %s", result.Run.ErrorClass)
	}
	_, _, agentCalls, _ := fixture.ports.counts()
	if agentCalls != len(e2eReviewFailTwiceReplies()) {
		t.Fatalf("上限用尽后不得继续调用 Agent，实际 %d 次", agentCalls)
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("修复未成功时任务不得标记为完成")
	}
	if records := fixture.codeSaves(t); len(records) != 0 {
		t.Fatalf("修复未成功时不得产生代码保存记录，实际 %d 条", len(records))
	}

	// 每轮都必须创建新的 Attempt 与新的验证记录，历史一律保留。
	steps := fixture.reloadSteps(t)
	attempts := fixture.attempts(t, steps[1].ID)
	if len(attempts) != 2 {
		t.Fatalf("两轮修复应留下两次开发尝试，实际 %d 次", len(attempts))
	}
	if attempts[0].ID == attempts[1].ID {
		t.Fatal("修复轮次不得复用同一次尝试")
	}
	for index, attempt := range attempts {
		runs := fixture.validationRuns(t, attempt.ID)
		if len(runs) != 1 {
			t.Fatalf("第 %d 轮应有一条验证记录，实际 %d 条", index+1, len(runs))
		}
		run := runs[0]
		if run.ReviewStatus != domain.ReviewStatusFailed ||
			run.Verdict != domain.VerificationVerdictFailed {
			t.Fatalf("第 %d 轮验证结论应为审查失败，实际 %s/%s",
				index+1, run.ReviewStatus, run.Verdict)
		}
		// 失败依据必须落库：即使没有未通过的检查，结论摘要也是可引用的反馈。
		feedback, err := fixture.db.GetValidationFailureFeedback(fixture.ctx, run.ID)
		if err != nil {
			t.Fatalf("第 %d 轮必须写入验证失败反馈: %v", index+1, err)
		}
		if feedback.Verdict != domain.VerificationVerdictFailed {
			t.Fatalf("第 %d 轮反馈结论应为 failed，实际 %s", index+1, feedback.Verdict)
		}
		if len(feedback.Checks) != 0 {
			t.Fatalf("第 %d 轮没有未通过的检查，反馈不应伪造检查条目", index+1)
		}
		if !strings.Contains(feedback.Summary, "实现缺少错误处理") {
			t.Fatalf("第 %d 轮反馈摘要必须来自语义审查: %q", index+1, feedback.Summary)
		}
	}

	// 第 2 轮开发的提示词必须真的带上第 1 轮的审查结论，而不是空跑一次。
	prompts := fixture.ports.agentPrompts()
	if len(prompts) != len(e2eReviewFailTwiceReplies()) {
		t.Fatalf("提示词数量与 Agent 调用数量不一致: %d", len(prompts))
	}
	secondRound := prompts[len(prompts)-2]
	if !strings.Contains(secondRound, "上一次验证未通过的问题") {
		t.Fatalf("第 2 轮提示词缺少失败反馈段落: %s", secondRound)
	}
	if !strings.Contains(secondRound, "实现缺少错误处理") {
		t.Fatalf("第 2 轮提示词缺少上一轮审查结论: %s", secondRound)
	}
}

// singleValidationRun 读取某步骤唯一一条验证记录。
func singleValidationRun(
	t *testing.T,
	fixture *e2eFixture,
	stepID domain.ID,
) domain.ValidationRun {
	t.Helper()
	attempts := fixture.attempts(t, stepID)
	if len(attempts) != 1 {
		t.Fatalf("应只有一次开发尝试，实际 %d 次", len(attempts))
	}
	runs := fixture.validationRuns(t, attempts[0].ID)
	if len(runs) != 1 {
		t.Fatalf("应只有一条验证记录，实际 %d 条", len(runs))
	}
	return runs[0]
}
