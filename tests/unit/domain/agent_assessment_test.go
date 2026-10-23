// Package domain_test 提供 Validator 语义审查相关领域规则的单元测试。
//
// 语义审查与结论计算都是纯函数：不读数据库、不启动进程，因此这里直接构造结构化
// 事实，覆盖"评估只能收紧、不能放宽"与"要求审查但拿不到可用结论"的全部分支。
package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/domain"
)

// passedAssessment 构造一个可解释的通过评估。
func passedAssessment() *domain.AgentAssessment {
	return &domain.AgentAssessment{Passed: true, Summary: "需求已满足"}
}

// TestParseAgentAssessmentAcceptsStructuredOutput 验证结构化输出可以被可靠解析。
func TestParseAgentAssessmentAcceptsStructuredOutput(t *testing.T) {
	t.Parallel()
	text := "以下是评估结果：\n```json\n" +
		`{"passed":true,"summary":"实现完整","findings":["无"],"evidence":["internal/app/x.go"],` +
		`"suggest_fix":false,"needs_human_review":false}` +
		"\n```\n以上。"
	assessment, err := domain.ParseAgentAssessment(text)
	if err != nil {
		t.Fatalf("解析结构化评估失败: %v", err)
	}
	if !assessment.Passed {
		t.Fatalf("passed 字段解析错误: %+v", assessment)
	}
	if assessment.Summary != "实现完整" {
		t.Fatalf("summary 解析错误: %q", assessment.Summary)
	}
	if len(assessment.Evidence) != 1 || assessment.Evidence[0] != "internal/app/x.go" {
		t.Fatalf("evidence 解析错误: %+v", assessment.Evidence)
	}
}

// TestParseAgentAssessmentRejectsUnusableOutput 验证不可解释的输出一律被拒绝。
func TestParseAgentAssessmentRejectsUnusableOutput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "空输出", text: "   "},
		{name: "纯自然语言", text: "看起来正确，实现没有问题。"},
		{name: "缺少 passed", text: `{"summary":"没有结论"}`},
		{name: "JSON 未闭合", text: `{"passed":true`},
		{name: "未知字段", text: `{"passed":true,"confidence":0.9}`},
		{name: "字段类型不符", text: `{"passed":"yes"}`},
		{
			name: "问题数量超限",
			text: `{"passed":false,"findings":[` +
				strings.Repeat(`"问题",`, domain.MaxAcceptanceCriteria) + `"最后一条"]}`,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := domain.ParseAgentAssessment(testCase.text); err == nil {
				t.Fatalf("不可解释的输出必须被拒绝: %s", testCase.text)
			} else if !errors.Is(err, domain.ErrInvalidValue) {
				t.Fatalf("错误类别不符: %v", err)
			}
		})
	}
}

// TestDecideValidationReviewCannotOverrideRequiredFailure 验证审查结论不能覆盖必需检查失败。
func TestDecideValidationReviewCannotOverrideRequiredFailure(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:         spec,
		Results:      []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusFailed, 1)},
		RunStatus:    domain.ValidationRunStatusCompleted,
		ReviewStatus: domain.ReviewStatusPassed,
		Assessment:   passedAssessment(),
	})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("必需检查失败时审查不得改写结论，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationRequiresUsableReview 验证要求审查却没有可用结论时不能判通过。
func TestDecideValidationRequiresUsableReview(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	results := []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusPassed, 0)}
	// 这些状态都属于"要求了审查但没有拿到可用证据"。
	unusable := []domain.ReviewStatus{
		domain.ReviewStatusUnparseable,
		domain.ReviewStatusInvalidOutput,
		domain.ReviewStatusCallFailed,
		domain.ReviewStatusTimedOut,
		domain.ReviewStatusCancelled,
		domain.ReviewStatusInterrupted,
		domain.ReviewStatusNotConfigured,
	}
	for _, status := range unusable {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			decision := domain.DecideValidation(domain.ValidationDecisionInput{
				Spec:         spec,
				Results:      results,
				RunStatus:    domain.ValidationRunStatusCompleted,
				ReviewStatus: status,
			})
			if decision.Verdict != domain.VerificationVerdictInconclusive {
				t.Fatalf("审查状态 %s 应判为不确定，实际 %s", status, decision.Verdict)
			}
			if decision.FailureSummary == "" {
				t.Fatal("不确定结论必须给出原因")
			}
		})
	}
}

// TestDecideValidationPassedRequiresAssessment 验证"审查声称通过但缺少评估"不能判通过。
func TestDecideValidationPassedRequiresAssessment(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	results := []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusPassed, 0)}
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:         spec,
		Results:      results,
		RunStatus:    domain.ValidationRunStatusCompleted,
		ReviewStatus: domain.ReviewStatusPassed,
	})
	if decision.Verdict != domain.VerificationVerdictInconclusive {
		t.Fatalf("缺少评估时不能判通过，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationReviewTightens 验证确定性通过 + 审查未通过判失败。
func TestDecideValidationReviewTightens(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:         spec,
		Results:      []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusPassed, 0)},
		RunStatus:    domain.ValidationRunStatusCompleted,
		ReviewStatus: domain.ReviewStatusFailed,
		Assessment: &domain.AgentAssessment{
			Passed:   false,
			Findings: []string{"缺少错误处理"},
		},
	})
	if decision.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("审查未通过应判失败，实际 %s", decision.Verdict)
	}
	if !strings.Contains(decision.FailureSummary, "缺少错误处理") {
		t.Fatalf("失败摘要应包含审查发现: %q", decision.FailureSummary)
	}
}

// TestDecideValidationReviewNeedsHumanReview 验证要求人工复核时判不确定。
func TestDecideValidationReviewNeedsHumanReview(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:         spec,
		Results:      []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusPassed, 0)},
		RunStatus:    domain.ValidationRunStatusCompleted,
		ReviewStatus: domain.ReviewStatusNeedsHumanReview,
		Assessment:   &domain.AgentAssessment{Passed: true, NeedsHumanReview: true},
	})
	if decision.Verdict != domain.VerificationVerdictInconclusive {
		t.Fatalf("要求人工复核应判不确定，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationReviewPassedWithAssessment 验证确定性通过 + 审查通过才判通过。
func TestDecideValidationReviewPassedWithAssessment(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:         spec,
		Results:      []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusPassed, 0)},
		RunStatus:    domain.ValidationRunStatusCompleted,
		ReviewStatus: domain.ReviewStatusPassed,
		Assessment:   passedAssessment(),
	})
	if decision.Verdict != domain.VerificationVerdictPassed {
		t.Fatalf("确定性通过且审查通过应判通过，实际 %s", decision.Verdict)
	}
}

// TestDecideValidationUnfinishedRunIgnoresReview 验证流程未跑完时审查无效。
func TestDecideValidationUnfinishedRunIgnoresReview(t *testing.T) {
	t.Parallel()
	build := requiredCheck("build", 1)
	spec := domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{build}}
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:         spec,
		Results:      []domain.ValidationCheckResult{checkResult(build, domain.ValidationCheckStatusPassed, 0)},
		RunStatus:    domain.ValidationRunStatusTimedOut,
		ReviewStatus: domain.ReviewStatusPassed,
		Assessment:   passedAssessment(),
	})
	if decision.Verdict == domain.VerificationVerdictPassed {
		t.Fatal("流程超时时审查不得把结论改为通过")
	}
}

// TestReviewStatusValidate 验证审查状态取值校验。
func TestReviewStatusValidate(t *testing.T) {
	t.Parallel()
	known := []domain.ReviewStatus{
		domain.ReviewStatusNotRequested,
		domain.ReviewStatusPassed,
		domain.ReviewStatusFailed,
		domain.ReviewStatusNeedsHumanReview,
		domain.ReviewStatusUnparseable,
		domain.ReviewStatusInvalidOutput,
		domain.ReviewStatusCallFailed,
		domain.ReviewStatusTimedOut,
		domain.ReviewStatusCancelled,
		domain.ReviewStatusInterrupted,
		domain.ReviewStatusNotConfigured,
	}
	for _, status := range known {
		if err := status.Validate(); err != nil {
			t.Fatalf("已知状态 %q 校验失败: %v", status, err)
		}
	}
	if err := domain.ReviewStatus("approved_v2").Validate(); err == nil {
		t.Fatal("未知审查状态必须被拒绝")
	}
	if domain.ReviewStatusNotRequested.Usable() {
		t.Fatal("未要求审查不构成可用结论")
	}
	if !domain.ReviewStatusPassed.Usable() {
		t.Fatal("审查通过应构成可用结论")
	}
}
