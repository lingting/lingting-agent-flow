// Package domain_test 提供验证结论计算规则的单元测试。
//
// 结论计算是纯函数：不读数据库、不启动进程，因此这里直接构造结构化事实，
// 覆盖必需检查失败、可选检查失败、超时、取消、启动失败、跳过与结果缺失等分支。
package domain_test

import (
	"testing"

	"github.com/lingting/agentflow/internal/domain"
)

// requiredCheck 构造一项启用的必需命令检查。
func requiredCheck(id string, sequence int) domain.ValidationCheckDefinition {
	return domain.ValidationCheckDefinition{
		ID:       id,
		Name:     id,
		Type:     domain.ValidationCheckTypeCommand,
		Enabled:  true,
		Required: true,
		Sequence: sequence,
		Command:  "go",
		Args:     []string{"test", "./..."},
	}
}

// optionalCheck 构造一项启用的可选命令检查。
func optionalCheck(id string, sequence int) domain.ValidationCheckDefinition {
	check := requiredCheck(id, sequence)
	check.Required = false
	return check
}

// checkResult 构造一条检查结果。
func checkResult(
	definition domain.ValidationCheckDefinition,
	status domain.ValidationCheckStatus,
	exitCode int,
) domain.ValidationCheckResult {
	return domain.ValidationCheckResult{
		CheckID:   definition.ID,
		CheckName: definition.Name,
		Required:  definition.IsRequired(),
		Sequence:  definition.Sequence,
		Status:    status,
		ExitCode:  exitCode,
	}
}

// decideCompleted 以"流程已完成"为前置状态计算结论。
func decideCompleted(
	spec domain.ValidationSpec,
	results []domain.ValidationCheckResult,
) domain.ValidationDecision {
	return domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:      spec,
		Results:   results,
		RunStatus: domain.ValidationRunStatusCompleted,
	})
}

// TestDecideValidationAllRequiredPassed 验证全部必需检查通过时判定通过。
func TestDecideValidationAllRequiredPassed(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	test := requiredCheck("test", 2)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build, test}}
	decision := decideCompleted(spec, []domain.ValidationCheckResult{
		checkResult(build, domain.ValidationCheckStatusPassed, 0),
		checkResult(test, domain.ValidationCheckStatusPassed, 0),
	})
	if !decision.Passed() {
		t.Fatalf("必需检查全部通过时应判定通过，实际 %s: %s", decision.Verdict, decision.FailureSummary)
	}
	if decision.FailureSummary != "" {
		t.Fatalf("通过时不应给出失败摘要: %q", decision.FailureSummary)
	}
}

// TestDecideValidationRequiredExitCodeNonZero 验证必需检查非零退出码判定失败。
func TestDecideValidationRequiredExitCodeNonZero(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := decideCompleted(spec, []domain.ValidationCheckResult{
		checkResult(build, domain.ValidationCheckStatusFailed, 1),
	})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("必需检查非零退出码应判定失败，实际 %s", decision.Verdict)
	}
	if decision.FailureSummary == "" {
		t.Fatal("失败时必须给出原因")
	}
}

// TestDecideValidationOptionalFailureKeepsPassed 验证可选检查失败不影响结论。
func TestDecideValidationOptionalFailureKeepsPassed(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	lint := optionalCheck("lint", 2)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build, lint}}
	decision := decideCompleted(spec, []domain.ValidationCheckResult{
		checkResult(build, domain.ValidationCheckStatusPassed, 0),
		checkResult(lint, domain.ValidationCheckStatusFailed, 1),
	})
	if !decision.Passed() {
		t.Fatalf("可选检查失败不应阻止通过，实际 %s: %s", decision.Verdict, decision.FailureSummary)
	}
}

// TestDecideValidationRequiredTimedOut 验证必需检查超时判定失败。
//
// 超时意味着没有拿到可用证据，不能伪装成通过；按既定策略一律判 failed。
func TestDecideValidationRequiredTimedOut(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	result := checkResult(build, domain.ValidationCheckStatusTimedOut, -1)
	result.TimedOut = true
	decision := decideCompleted(spec, []domain.ValidationCheckResult{result})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("必需检查超时应判定失败，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationRequiredCancelled 验证必需检查被取消判定失败。
func TestDecideValidationRequiredCancelled(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	result := checkResult(build, domain.ValidationCheckStatusCancelled, -1)
	result.Cancelled = true
	decision := decideCompleted(spec, []domain.ValidationCheckResult{result})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("必需检查被取消应判定失败，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationStartFailed 验证检查启动失败判定失败。
func TestDecideValidationStartFailed(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	result := checkResult(build, domain.ValidationCheckStatusError, -1)
	result.Certain = false
	decision := decideCompleted(spec, []domain.ValidationCheckResult{result})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("必需检查启动失败应判定失败，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationAllSkipped 验证所有检查被跳过时不能判定通过。
func TestDecideValidationAllSkipped(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := decideCompleted(spec, []domain.ValidationCheckResult{
		checkResult(build, domain.ValidationCheckStatusSkipped, -1),
	})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("全部检查被跳过时不能判定通过，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationNoRequiredCheck 验证没有启用的必需检查时不能判定通过。
func TestDecideValidationNoRequiredCheck(t *testing.T) {
	t.Parallel()
	lint := optionalCheck("lint", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{lint}}
	decision := decideCompleted(spec, []domain.ValidationCheckResult{
		checkResult(lint, domain.ValidationCheckStatusPassed, 0),
	})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("没有必需检查时不能判定通过，实际 %s", decision.Verdict)
	}

	// 禁用的必需检查同样不构成门槛。
	disabled := requiredCheck("build", 1)
	disabled.Enabled = false
	disabledSpec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{disabled}}
	disabledDecision := decideCompleted(disabledSpec, nil)
	if disabledDecision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("只有禁用的必需检查时不能判定通过，实际 %s", disabledDecision.Verdict)
	}
}

// TestDecideValidationAgentCannotOverrideRequiredFailure 验证 Agent 结论不能覆盖必需检查失败。
func TestDecideValidationAgentCannotOverrideRequiredFailure(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:    spec,
		Results: []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusFailed, 1)},
		// 即使流程完成且 Agent 声称通过，也不能改写必需检查的失败。
		RunStatus:  domain.ValidationRunStatusCompleted,
		Assessment: &domain.AgentAssessment{Passed: true, Summary: "我认为没有问题"},
	})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("Agent 结论不得覆盖必需检查失败，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationAgentFailureTightens 验证 Agent 未通过可以把通过收紧为失败。
func TestDecideValidationAgentFailureTightens(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:       spec,
		Results:    []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusPassed, 0)},
		RunStatus:  domain.ValidationRunStatusCompleted,
		Assessment: &domain.AgentAssessment{Passed: false, Findings: []string{"缺少错误处理"}},
	})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("Agent 未通过应收紧为失败，实际 %s", decision.Verdict)
	}
	if decision.FailureSummary == "" {
		t.Fatal("收紧为失败时必须给出原因")
	}
}

// TestDecideValidationAgentNeedsHumanReview 验证要求人工复核时判为不确定。
func TestDecideValidationAgentNeedsHumanReview(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:       spec,
		Results:    []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusPassed, 0)},
		RunStatus:  domain.ValidationRunStatusCompleted,
		Assessment: &domain.AgentAssessment{Passed: true, NeedsHumanReview: true},
	})
	if decision.Verdict != domain.VerificationVerdictInconclusive {
		t.Fatalf("要求人工复核时应判为不确定，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationIncompleteResults 验证必需检查结果缺失时不得判定通过。
func TestDecideValidationIncompleteResults(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	test := requiredCheck("test", 2)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build, test}}
	decision := decideCompleted(spec, []domain.ValidationCheckResult{
		checkResult(build, domain.ValidationCheckStatusPassed, 0),
	})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("必需检查结果缺失时不能判定通过，实际 %s", decision.Verdict)
	}
	if decision.FailureSummary == "" {
		t.Fatal("结果缺失时必须给出原因")
	}
}

// TestDecideValidationUnfinishedRun 验证未完成的流程一律不能判定通过。
func TestDecideValidationUnfinishedRun(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	results := []domain.ValidationCheckResult{
		checkResult(build, domain.ValidationCheckStatusPassed, 0),
	}
	for _, status := range []domain.ValidationRunStatus{
		domain.ValidationRunStatusPending,
		domain.ValidationRunStatusRunning,
		domain.ValidationRunStatusCancelled,
		domain.ValidationRunStatusTimedOut,
		domain.ValidationRunStatusFailed,
	} {
		decision := domain.DecideValidation(domain.ValidationDecisionInput{
			Spec:      spec,
			Results:   results,
			RunStatus: status,
		})
		if decision.Verdict != domain.VerificationVerdictFailed {
			t.Fatalf("流程状态 %s 时不能判定通过，实际 %s", status, decision.Verdict)
		}
	}
}

// TestCountCheckResultsIgnoresUnfinished 验证计数不把未完成结果算成通过或失败。
func TestCountCheckResultsIgnoresUnfinished(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	test := requiredCheck("test", 2)
	lint := optionalCheck("lint", 3)
	passed, failed, skipped := domain.CountCheckResults([]domain.ValidationCheckResult{
		checkResult(build, domain.ValidationCheckStatusPassed, 0),
		checkResult(test, domain.ValidationCheckStatusFailed, 1),
		checkResult(lint, domain.ValidationCheckStatusSkipped, -1),
		{CheckID: "pending", Status: domain.ValidationCheckStatusPending},
		{CheckID: "running", Status: domain.ValidationCheckStatusRunning},
	})
	if passed != 1 || failed != 1 || skipped != 1 {
		t.Fatalf("计数不符: passed=%d failed=%d skipped=%d", passed, failed, skipped)
	}
}

// TestValidationSpecOrdered 验证执行顺序由 Sequence 决定并过滤禁用项。
func TestValidationSpecOrdered(t *testing.T) {
	t.Parallel()
	third := requiredCheck("third", 3)
	first := requiredCheck("first", 1)
	disabled := requiredCheck("disabled", 2)
	disabled.Enabled = false
	spec := domain.ValidationSpec{
		Checks: []domain.ValidationCheckDefinition{third, first, disabled},
	}
	ordered := spec.Ordered()
	if len(ordered) != 2 {
		t.Fatalf("启用检查数量不符: %d", len(ordered))
	}
	if ordered[0].ID != "first" || ordered[1].ID != "third" {
		t.Fatalf("执行顺序不符: %s, %s", ordered[0].ID, ordered[1].ID)
	}
	if !spec.HasRequiredCheck() {
		t.Fatal("存在启用的必需检查时 HasRequiredCheck 应为 true")
	}
}

// TestValidationCheckDefinitionRejectsShellTokens 验证命令不允许包含空白与 shell 拼接。
func TestValidationCheckDefinitionRejectsShellTokens(t *testing.T) {
	t.Parallel()
	definition := requiredCheck("build", 1)
	definition.Command = "go test ./..."
	if err := definition.Validate(); err == nil {
		t.Fatal("把整条命令当作可执行文件名应当被拒绝")
	}
	definition.Command = "go"
	definition.Args = []string{"test", "./...", "&&", "rm", "-rf", "/"}
	// 参数本身允许任意内容：它们逐项传递，不经过 shell 解释。
	if err := definition.Validate(); err != nil {
		t.Fatalf("参数数组不应被当作 shell 语法校验: %v", err)
	}
	definition.WorkDir = "/tmp"
	if err := definition.Validate(); err == nil {
		t.Fatal("容器外工作目录应当被拒绝")
	}
}
