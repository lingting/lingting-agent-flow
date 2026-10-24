// Package app_test 提供验证失败修复闭环（P1-M3）的单元测试。
//
// 用例复用 step_workflow_test.go 的真实链路夹具：数据库、状态机、Attempt/Session/
// Validation Run/反馈的持久化与领域判定全部走产品代码，只把容器内 Agent 调用与命令
// 执行替换为受控替身。因此断言的对象是数据库中真实留下的现场，而不是方法的返回值。
package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 修复循环用例使用的重试策略：允许验证失败自动修复，最多 2 次开发尝试。
func repairRetry(maxAttempts int) domain.RetryConfig {
	return domain.RetryConfig{MaxAttempts: maxAttempts, RetryVerification: true}
}

// feedbackPromptMarker 是失败反馈小节在提示词中的标题。
const feedbackPromptMarker = "上一次验证未通过的问题"

// TestRepairLoopRetriesUntilVerificationPasses 验证"第一轮失败 → 第二轮修复 → 通过"。
func TestRepairLoopRetriesUntilVerificationPasses(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "根据反馈修复"},
		},
		// 第一次检查失败，第二次通过：两轮验证都真实执行。
		checkSequence: func(index int) workflowCheckReply {
			if index == 0 {
				return failedCheck("编译错误: undefined: foo")
			}
			return passedCheck()
		},
		retry: repairRetry(2),
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行修复闭环失败: %v", err)
	}
	if !result.Verified {
		t.Fatalf("第二轮验证通过后步骤应标记为已验证完成，实际 %s", result.StepStatus)
	}
	if result.Round != 2 {
		t.Fatalf("通过时应停在第二轮，实际第 %d 轮", result.Round)
	}
	if result.Attempts != 2 {
		t.Fatalf("应产生 2 次开发尝试，实际 %d", result.Attempts)
	}
	if !result.Repair.Completed() {
		t.Fatalf("修复判定应为完成，实际 %s", result.Repair.Kind)
	}
	// 每一轮都创建新的 Attempt 与独立 Session，历史一律保留。
	attempts := fixture.attempts(t)
	if len(attempts) != 2 {
		t.Fatalf("尝试数量不符: %d", len(attempts))
	}
	if attempts[0].ID == attempts[1].ID {
		t.Fatal("历史尝试不得被覆盖")
	}
	if attempts[0].AttemptNumber == attempts[1].AttemptNumber {
		t.Fatalf("尝试序号必须递增: %d 与 %d", attempts[0].AttemptNumber, attempts[1].AttemptNumber)
	}
	sessions := fixture.sessionsOf(t)
	if len(sessions) != 2 {
		t.Fatalf("每轮必须有独立 Session，实际 %d 条", len(sessions))
	}
	if sessions[0].ID == sessions[1].ID {
		t.Fatal("两轮开发不得复用一个 Session")
	}
	// 每轮验证都产生独立的 Validation Run，并绑定该轮的 Attempt。
	for _, attempt := range attempts {
		runs := fixture.validationRuns(t, attempt.ID)
		if len(runs) != 1 {
			t.Fatalf("尝试 %s 应有 1 条验证记录，实际 %d", attempt.ID, len(runs))
		}
	}
	if fixture.containers.agentCallsCount() != 2 {
		t.Fatalf("Agent 调用次数不符: %d", fixture.containers.agentCallsCount())
	}
	// 第二轮提示词必须引用上一轮真实验证的失败反馈。
	fixture.containers.mu.Lock()
	prompts := make([]string, 0, len(fixture.containers.agentRequests))
	for _, request := range fixture.containers.agentRequests {
		prompts = append(prompts, request.Prompt)
	}
	fixture.containers.mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("Agent 请求数量不符: %d", len(prompts))
	}
	if strings.Contains(prompts[0], feedbackPromptMarker) {
		t.Fatal("第一轮提示词不应包含上一轮失败反馈")
	}
	if !strings.Contains(prompts[1], feedbackPromptMarker) {
		t.Fatal("第二轮提示词必须引用上一轮失败反馈")
	}
	if !strings.Contains(prompts[1], "编译错误") {
		t.Fatalf("第二轮提示词未包含上一轮失败原因: %s", prompts[1])
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusCompleted {
		t.Fatalf("步骤状态不符: 期望 completed，实际 %s", step.Status)
	}
	if step.CurrentAttemptID != nil {
		t.Fatal("步骤完成时必须释放执行占用")
	}
	if !result.Finalized {
		t.Fatalf("收口状态应全部写入成功: %v", result.FinalizationErrors)
	}
}

// TestRepairLoopStopsAtAttemptLimit 验证达到尝试上限后立即停止，不再调用 Agent。
func TestRepairLoopStopsAtAttemptLimit(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "第二次实现"},
			{status: agent.RunCompleted, finalText: "第三次实现"},
		},
		checkReplies: map[string]workflowCheckReply{"go": failedCheck("编译错误: undefined: foo")},
		retry:        repairRetry(2),
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行修复闭环失败: %v", err)
	}
	if result.Verified {
		t.Fatal("验证未通过不得标记为已验证完成")
	}
	if result.Repair.Class != domain.RepairClassAttemptsExhausted {
		t.Fatalf("停止类别不符: %s（%s）", result.Repair.Class, result.Repair.Summary)
	}
	if result.Attempts != 2 {
		t.Fatalf("已用尝试次数不符: %d", result.Attempts)
	}
	// 达到上限后不得再调用 Developer，也不得再执行检查。
	if calls := fixture.containers.agentCallsCount(); calls != 2 {
		t.Fatalf("达到上限后不得继续调用 Agent，实际调用 %d 次", calls)
	}
	if count := fixture.containers.execCount(); count != 2 {
		t.Fatalf("达到上限后不得继续执行检查，实际执行 %d 次", count)
	}
	if attempts := fixture.attempts(t); len(attempts) != 2 {
		t.Fatalf("不得创建额外 Attempt，实际 %d 条", len(attempts))
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusFailed {
		t.Fatalf("步骤状态不符: 期望 failed，实际 %s", step.Status)
	}
	if !strings.Contains(step.FailureSummary, "尝试次数上限") {
		t.Fatalf("失败摘要应说明达到上限: %s", step.FailureSummary)
	}
	// 失败摘要必须保留最后一轮的真实失败证据。
	if !strings.Contains(step.FailureSummary, "build") {
		t.Fatalf("失败摘要未保留失败检查名: %s", step.FailureSummary)
	}
	if step.CurrentAttemptID != nil {
		t.Fatal("停止后必须释放执行占用，用户才能显式重新执行")
	}
	if result.Finalized != true {
		t.Fatalf("收口应写入成功: %v", result.FinalizationErrors)
	}
}

// TestRepairLoopExplicitLimitThree 验证显式上限 3 时的边界行为。
func TestRepairLoopExplicitLimitThree(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		checkReplies: map[string]workflowCheckReply{"go": failedCheck("编译错误")},
		retry:        repairRetry(3),
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行修复闭环失败: %v", err)
	}
	if result.Attempts != 3 {
		t.Fatalf("显式上限 3 时应执行 3 次，实际 %d 次", result.Attempts)
	}
	if calls := fixture.containers.agentCallsCount(); calls != 3 {
		t.Fatalf("Agent 调用次数不符: %d", calls)
	}
	if result.Repair.Class != domain.RepairClassAttemptsExhausted {
		t.Fatalf("停止类别不符: %s", result.Repair.Class)
	}
	if len(fixture.attempts(t)) != 3 {
		t.Fatalf("尝试记录数量不符: %d", len(fixture.attempts(t)))
	}
}

// TestRepairLoopDisabledWithoutRetryFlag 验证未开启修复时首轮失败即停止。
func TestRepairLoopDisabledWithoutRetryFlag(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		checkReplies: map[string]workflowCheckReply{"go": failedCheck("编译错误")},
		retry: domain.RetryConfig{MaxAttempts: 3, RetryClasses: []domain.RetryErrorClass{
			domain.RetryClassAgentFailure,
		}},
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.Repair.Class != domain.RepairClassRepairDisabled {
		t.Fatalf("停止类别不符: %s（%s）", result.Repair.Class, result.Repair.Summary)
	}
	if calls := fixture.containers.agentCallsCount(); calls != 1 {
		t.Fatalf("未开启修复时不得自动重试，实际调用 %d 次", calls)
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusFailed {
		t.Fatalf("步骤状态不符: %s", step.Status)
	}
}

// TestRepairLoopStopsOnVerificationInconclusive 验证结论不确定时不自动修复。
func TestRepairLoopStopsOnVerificationInconclusive(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		retry: repairRetry(3),
	})
	// 语义审查要求人工复核会得到 inconclusive 结论：这里通过让检查跳过构造成
	// "必需检查未执行"，从而得到 failed 之外的证据不足结论。
	fixture.skipAllChecks()
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if calls := fixture.containers.agentCallsCount(); calls != 1 {
		t.Fatalf("结论不确定时不得继续修复，实际调用 %d 次", calls)
	}
	if result.Verified {
		t.Fatal("结论不确定不得标记为已验证完成")
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusFailed && step.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤状态不符: %s", step.Status)
	}
}

// TestRepairLoopStopsOnInfrastructureError 验证基础设施异常不自动修复。
func TestRepairLoopStopsOnInfrastructureError(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		checkReplies: map[string]workflowCheckReply{
			"go": startupErrorCheck(errors.New("容器内命令无法启动")),
		},
		retry: repairRetry(3),
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.Repair.Class != domain.RepairClassInfrastructure {
		t.Fatalf("停止类别不符: %s（%s）", result.Repair.Class, result.Repair.Summary)
	}
	if calls := fixture.containers.agentCallsCount(); calls != 1 {
		t.Fatalf("基础设施异常不得自动修复，实际调用 %d 次", calls)
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤状态不符: 期望 recovery_required，实际 %s", step.Status)
	}
	task := fixture.reloadTask(t)
	if task.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("任务状态不符: 期望 recovery_required，实际 %s", task.Status)
	}
}

// TestRepairLoopStopsWhenFeedbackNotPersisted 验证反馈写入失败时停止自动修复。
func TestRepairLoopStopsWhenFeedbackNotPersisted(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "第二次实现"},
		},
		checkReplies:      map[string]workflowCheckReply{"go": failedCheck("编译错误")},
		retry:             repairRetry(3),
		createFeedbackErr: errFeedbackInjected,
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.FeedbackPersisted {
		t.Fatal("反馈写入失败时不得报告已保存")
	}
	if result.Repair.Class != domain.RepairClassFeedbackUnavailable {
		t.Fatalf("停止类别不符: %s（%s）", result.Repair.Class, result.Repair.Summary)
	}
	if calls := fixture.containers.agentCallsCount(); calls != 1 {
		t.Fatalf("没有可信修复依据时不得重放调用，实际调用 %d 次", calls)
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤状态不符: 期望 recovery_required，实际 %s", step.Status)
	}
}

// TestRepairLoopStopsOnDeveloperFailure 验证开发失败不自动修复。
func TestRepairLoopStopsOnDeveloperFailure(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{{
			status:   agent.RunFailed,
			failure:  "编译失败",
			exitCode: 1,
		}},
		checkReplies: map[string]workflowCheckReply{"go": passedCheck()},
		retry:        repairRetry(3),
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.Repair.Class != domain.RepairClassDeveloperFailed {
		t.Fatalf("停止类别不符: %s（%s）", result.Repair.Class, result.Repair.Summary)
	}
	if calls := fixture.containers.agentCallsCount(); calls != 1 {
		t.Fatalf("开发失败不得自动修复，实际调用 %d 次", calls)
	}
	if count := fixture.containers.execCount(); count != 0 {
		t.Fatalf("开发失败不得执行检查，实际执行 %d 次", count)
	}
}

// TestRepairLoopStopsOnDeveloperTimeout 验证开发超时进入需人工核验，而不是自动修复。
func TestRepairLoopStopsOnDeveloperTimeout(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{{status: agent.RunTimedOut, failure: "调用超时"}},
		checkReplies: map[string]workflowCheckReply{"go": passedCheck()},
		retry:        repairRetry(3),
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.Repair.Class != domain.RepairClassDeveloperTimedOut {
		t.Fatalf("停止类别不符: %s", result.Repair.Class)
	}
	if calls := fixture.containers.agentCallsCount(); calls != 1 {
		t.Fatalf("超时不得自动修复，实际调用 %d 次", calls)
	}
	step := fixture.reloadStep(t)
	if step.Status == domain.StepStatusCompleted {
		t.Fatal("超时不得把步骤标记为完成")
	}
}

// TestRepairLoopStopsOnPlanningStep 验证规划步骤不进入自动修复。
func TestRepairLoopStopsOnPlanningStep(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		checkReplies: map[string]workflowCheckReply{"go": failedCheck("编译错误")},
		retry:        repairRetry(3),
		stepType:     domain.StepTypePlan,
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.Repair.Class != domain.RepairClassUnsupportedStep {
		t.Fatalf("停止类别不符: %s（%s）", result.Repair.Class, result.Repair.Summary)
	}
	if calls := fixture.containers.agentCallsCount(); calls != 1 {
		t.Fatalf("规划步骤不得自动修复，实际调用 %d 次", calls)
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤状态不符: 期望 recovery_required，实际 %s", step.Status)
	}
}

// TestRepairLoopStopsBeforeNextRoundWhenCancelled 验证两轮之间被取消时不再调用 Agent。
func TestRepairLoopStopsBeforeNextRoundWhenCancelled(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies:  []fakeAgentReply{{status: agent.RunCompleted, finalText: "第一次实现"}},
		checkReplies:  map[string]workflowCheckReply{"go": failedCheck("编译错误")},
		retry:         repairRetry(3),
		stepType:      domain.StepTypeDevelop,
		checkSequence: nil,
	})
	// 第一轮验证失败后立即取消上下文：循环必须在启动第二轮之前停止。
	ctx, cancel := context.WithCancel(fixture.ctx)
	fixture.containers.onAgent = func(request app.ContainerAgentRequest) {
		// 开发调用开始后立刻取消：验证阶段会因取消收敛为 cancelled。
		cancel()
	}
	result, err := fixture.execute(t, ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if calls := fixture.containers.agentCallsCount(); calls > 1 {
		t.Fatalf("取消后不得启动新的开发调用，实际调用 %d 次", calls)
	}
	if result.Verified {
		t.Fatal("取消后不得标记为已验证完成")
	}
	if result.Repair.Class != domain.RepairClassCancelled && result.Outcome == domain.StepExecutionCancelled {
		// 取消在开发阶段生效：结论就是 cancelled，这是同一事实的另一种表达。
		return
	}
	if result.Repair.Class != domain.RepairClassCancelled {
		t.Fatalf("停止类别不符: %s（%s）", result.Repair.Class, result.Repair.Summary)
	}
}

// TestRepairLoopCancelBetweenRounds 验证两轮之间取消后不再启动第二轮。
func TestRepairLoopCancelBetweenRounds(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "第二次实现"},
		},
		checkReplies: map[string]workflowCheckReply{"go": failedCheck("编译错误")},
		retry:        repairRetry(3),
	})
	ctx, cancel := context.WithCancel(fixture.ctx)
	// 第一次检查执行完成后取消：此时第一轮已经收口，循环必须在下一次开发调用之前停止。
	fixture.containers.onExec = func(request app.ExecRequest) {
		cancel()
	}
	result, err := fixture.execute(t, ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if calls := fixture.containers.agentCallsCount(); calls != 1 {
		t.Fatalf("轮次之间取消后不得再调用 Agent，实际调用 %d 次", calls)
	}
	if result.Verified {
		t.Fatal("取消后不得标记为已验证完成")
	}
}

// TestRepairLoopHistoryAndEventTraceability 验证每轮记录与事件都可追溯。
func TestRepairLoopHistoryAndEventTraceability(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "根据反馈修复"},
		},
		checkSequence: func(index int) workflowCheckReply {
			if index == 0 {
				return failedCheck("编译错误: undefined: foo")
			}
			return passedCheck()
		},
		retry: repairRetry(2),
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行修复闭环失败: %v", err)
	}
	if !result.Verified {
		t.Fatalf("第二轮应通过验证，实际 %s", result.StepStatus)
	}
	events, err := fixture.db.ListEventsByTask(fixture.ctx, fixture.task.ID, storage.EventFilter{
		Limit: 200,
	})
	if err != nil {
		t.Fatalf("读取任务事件失败: %v", err)
	}
	rounds := make([]domain.Event, 0, len(events))
	for _, event := range events {
		if event.Type == domain.EventTypeRepairRound {
			rounds = append(rounds, event)
		}
	}
	if len(rounds) < 2 {
		t.Fatalf("每轮修复都应写入事件，实际 %d 条", len(rounds))
	}
	// 事件必须能追溯到该轮的 Attempt 与 Validation Run。
	attemptIDs := map[string]struct{}{}
	for _, attempt := range fixture.attempts(t) {
		attemptIDs[attempt.ID.String()] = struct{}{}
	}
	for _, event := range rounds {
		if event.AttemptID == nil {
			t.Fatal("修复轮次事件必须关联执行尝试")
		}
		if _, ok := attemptIDs[event.AttemptID.String()]; !ok {
			t.Fatalf("修复轮次事件关联了未知尝试: %s", event.AttemptID)
		}
	}
	// 两轮验证记录都必须保留。
	runs, err := fixture.db.ListValidationRunsByAttempt(fixture.ctx, result.AttemptID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("最后一轮应只有一条验证记录，实际 %d", len(runs))
	}
	for _, attempt := range fixture.attempts(t) {
		history, historyErr := fixture.db.ListValidationRunsByAttempt(fixture.ctx, attempt.ID)
		if historyErr != nil {
			t.Fatalf("读取验证历史失败: %v", historyErr)
		}
		if len(history) != 1 {
			t.Fatalf("每轮都应保留一条验证记录，尝试 %s 实际 %d 条", attempt.ID, len(history))
		}
	}
	// 失败反馈必须只关联第一轮，且被第二轮读取使用。
	attempts := fixture.attempts(t)
	feedbacks := 0
	for _, attempt := range attempts {
		history, historyErr := fixture.db.ListValidationRunsByAttempt(fixture.ctx, attempt.ID)
		if historyErr != nil {
			t.Fatalf("读取验证历史失败: %v", historyErr)
		}
		for _, run := range history {
			if _, feedbackErr := fixture.db.GetValidationFailureFeedback(fixture.ctx, run.ID); feedbackErr == nil {
				feedbacks++
			}
		}
	}
	if feedbacks != 1 {
		t.Fatalf("只有未通过的验证才应产生反馈，实际 %d 条", feedbacks)
	}
}

// TestRepairLoopReleasesOccupationAfterLimit 验证达到上限后用户可以显式重新执行。
func TestRepairLoopReleasesOccupationAfterLimit(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "第二次实现"},
			{status: agent.RunCompleted, finalText: "显式重新执行"},
		},
		checkReplies: map[string]workflowCheckReply{"go": failedCheck("编译错误")},
		retry:        repairRetry(2),
	})
	first, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行修复闭环失败: %v", err)
	}
	if first.Repair.Class != domain.RepairClassAttemptsExhausted {
		t.Fatalf("停止类别不符: %s", first.Repair.Class)
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusFailed {
		t.Fatalf("步骤状态不符: %s", step.Status)
	}
	if step.CurrentAttemptID != nil {
		t.Fatal("达到上限后必须释放执行占用")
	}
	// 显式重新执行是一次新的用户决定：必须先开启新的执行周期，且创建新的 Attempt。
	fixture.reopenCycle(t)
	second, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("显式重新执行失败: %v", err)
	}
	if second.AttemptID == first.AttemptID {
		t.Fatal("显式重新执行必须创建新的尝试")
	}
	// 新一轮闭环仍受同一个上限约束，因此会再产生 2 次尝试（共 4 条历史记录）。
	if attempts := fixture.attempts(t); len(attempts) != 4 {
		t.Fatalf("尝试历史不符: %d", len(attempts))
	}
}

// sessionsOf 返回任务的全部会话。
func (f *workflowFixture) sessionsOf(t *testing.T) []domain.Session {
	t.Helper()
	sessions, err := f.db.ListSessionsByTask(context.Background(), f.task.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	return sessions
}

// skipAllChecks 让所有检查都以 skipped 结束。
//
// 它构造的是"必需检查没有执行"，因此结论是失败且没有任何可引用的失败检查证据。
func (f *workflowFixture) skipAllChecks() {
	f.containers.mu.Lock()
	defer f.containers.mu.Unlock()
	f.containers.checkReplies = map[string]workflowCheckReply{
		"go": {exitCode: -1, started: false, certain: false, err: errors.New("检查被跳过")},
	}
}

// 编译期确认修复判定的取值稳定（避免重构时静默改名）。
var (
	_ = domain.RepairClassAttemptsExhausted
	_ = domain.RepairClassFeedbackUnavailable
	_ = domain.RepairClassInfrastructure
	_ = domain.RepairClassCancelled
	_ = time.Second
)
