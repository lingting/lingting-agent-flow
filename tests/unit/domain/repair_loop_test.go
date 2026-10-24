package domain_test

import (
	"errors"
	"testing"

	"github.com/lingting/agentflow/internal/domain"
)

// passingRepairInput 返回一份"验证失败且仍可修复"的基准输入。
func passingRepairInput() domain.RepairLoopInput {
	return domain.RepairLoopInput{
		StepType: domain.StepTypeDevelop,
		// Attempts / MaxAttempts 保留为兼容字段；预算判定只读取 Budget 与 HasBudget。
		Attempts:            1,
		MaxAttempts:         3,
		Budget:              domain.RepairBudget{MaxAttempts: 3, Consumed: 1},
		HasBudget:           true,
		Retry:               domain.RetryConfig{MaxAttempts: 3, RetryVerification: true},
		Outcome:             domain.StepExecutionCompleted,
		VerificationRan:     true,
		Verification:        domain.StepVerificationFailed,
		ValidationPersisted: true,
		FeedbackPersisted:   true,
		Finalized:           true,
	}
}

// TestDecideRepairLoopDefaultsToRetryable 验证基准输入允许再修复一轮。
func TestDecideRepairLoopDefaultsToRetryable(t *testing.T) {
	t.Parallel()
	decision := domain.DecideRepairLoop(passingRepairInput())
	if !decision.CanRepair() {
		t.Fatalf("仍在上限内时应允许修复，实际 %s（%s）", decision.Kind, decision.Class)
	}
	if decision.Class != domain.RepairClassNone {
		t.Fatalf("继续修复时不应给出停止类别: %s", decision.Class)
	}
	if decision.Summary == "" {
		t.Fatal("继续修复时必须给出可读说明")
	}
}

// TestDecideRepairLoopAttemptLimitBoundary 验证尝试上限的边界。
func TestDecideRepairLoopAttemptLimitBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		attempts int
		max      int
		expected bool
	}{
		{name: "首次之后仍可修复", attempts: 1, max: 3, expected: true},
		{name: "上限前一次仍可修复", attempts: 2, max: 3, expected: true},
		{name: "达到上限即停止", attempts: 3, max: 3, expected: false},
		{name: "超过上限也停止", attempts: 4, max: 3, expected: false},
		{name: "上限为 1 时首轮即终局", attempts: 1, max: 1, expected: false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := passingRepairInput()
			input.Attempts = testCase.attempts
			input.MaxAttempts = testCase.max
			input.Budget = domain.RepairBudget{MaxAttempts: testCase.max, Consumed: testCase.attempts}
			decision := domain.DecideRepairLoop(input)
			if decision.CanRepair() != testCase.expected {
				t.Fatalf("%s: 期望可修复=%v，实际 %s（%s）",
					testCase.name, testCase.expected, decision.Kind, decision.Class)
			}
			if !testCase.expected && decision.Class != domain.RepairClassAttemptsExhausted {
				t.Fatalf("%s: 停止类别应为 attempts_exhausted，实际 %s", testCase.name, decision.Class)
			}
		})
	}
}

// TestDecideRepairLoopZeroMaxAttemptsUsesDefault 验证上限缺省时使用领域默认值。
func TestDecideRepairLoopZeroMaxAttemptsUsesDefault(t *testing.T) {
	t.Parallel()
	input := passingRepairInput()
	input.MaxAttempts = 0
	input.Attempts = domain.DefaultMaxAttempts - 1
	input.Budget = domain.RepairBudget{MaxAttempts: 0, Consumed: domain.DefaultMaxAttempts - 1}
	if decision := domain.DecideRepairLoop(input); !decision.CanRepair() {
		t.Fatalf("默认上限 %d 内应允许修复，实际 %s", domain.DefaultMaxAttempts, decision.Class)
	}
	input.Attempts = domain.DefaultMaxAttempts
	input.Budget = domain.RepairBudget{MaxAttempts: 0, Consumed: domain.DefaultMaxAttempts}
	decision := domain.DecideRepairLoop(input)
	if decision.CanRepair() {
		t.Fatalf("默认上限 %d 已用尽时不得继续修复", domain.DefaultMaxAttempts)
	}
	if decision.Class != domain.RepairClassAttemptsExhausted {
		t.Fatalf("停止类别不符: %s", decision.Class)
	}
}

// TestDecideRepairLoopStopClasses 验证各类不可自动重试的停止类别映射。
func TestDecideRepairLoopStopClasses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		mutate   func(input *domain.RepairLoopInput)
		expected domain.RepairFailureClass
	}{
		{
			name:     "取消优先",
			mutate:   func(input *domain.RepairLoopInput) { input.Cancelled = true },
			expected: domain.RepairClassCancelled,
		},
		{
			name: "开发失败",
			mutate: func(input *domain.RepairLoopInput) {
				input.Outcome = domain.StepExecutionFailed
			},
			expected: domain.RepairClassDeveloperFailed,
		},
		{
			name: "开发超时",
			mutate: func(input *domain.RepairLoopInput) {
				input.Outcome = domain.StepExecutionTimedOut
			},
			expected: domain.RepairClassDeveloperTimedOut,
		},
		{
			name: "开发中断",
			mutate: func(input *domain.RepairLoopInput) {
				input.Outcome = domain.StepExecutionInterrupted
			},
			expected: domain.RepairClassDeveloperInterrupted,
		},
		{
			name: "开发取消",
			mutate: func(input *domain.RepairLoopInput) {
				input.Outcome = domain.StepExecutionCancelled
			},
			expected: domain.RepairClassDeveloperCancelled,
		},
		{
			name:     "验证未启动",
			mutate:   func(input *domain.RepairLoopInput) { input.VerificationRan = false },
			expected: domain.RepairClassVerificationNotRun,
		},
		{
			name: "基础设施异常",
			mutate: func(input *domain.RepairLoopInput) {
				input.InfrastructureError = true
			},
			expected: domain.RepairClassInfrastructure,
		},
		{
			name: "结论未落库",
			mutate: func(input *domain.RepairLoopInput) {
				input.ValidationPersisted = false
			},
			expected: domain.RepairClassInfrastructure,
		},
		{
			name: "结论不确定",
			mutate: func(input *domain.RepairLoopInput) {
				input.Verification = domain.StepVerificationInconclusive
			},
			expected: domain.RepairClassVerificationInconclusive,
		},
		{
			name: "验证被取消",
			mutate: func(input *domain.RepairLoopInput) {
				input.Verification = domain.StepVerificationCancelled
			},
			expected: domain.RepairClassCancelled,
		},
		{
			name: "验证状态未收敛",
			mutate: func(input *domain.RepairLoopInput) {
				input.Verification = domain.StepVerificationUnverified
			},
			expected: domain.RepairClassVerificationInconclusive,
		},
		{
			name:     "收口未持久化",
			mutate:   func(input *domain.RepairLoopInput) { input.Finalized = false },
			expected: domain.RepairClassNotPersisted,
		},
		{
			name: "反馈未持久化",
			mutate: func(input *domain.RepairLoopInput) {
				input.FeedbackPersisted = false
			},
			expected: domain.RepairClassFeedbackUnavailable,
		},
		{
			name: "非开发步骤",
			mutate: func(input *domain.RepairLoopInput) {
				input.StepType = domain.StepTypePlan
			},
			expected: domain.RepairClassUnsupportedStep,
		},
		{
			name: "未开启修复",
			mutate: func(input *domain.RepairLoopInput) {
				input.Retry = domain.RetryConfig{
					MaxAttempts:  3,
					RetryClasses: []domain.RetryErrorClass{domain.RetryClassAgentFailure},
				}
			},
			expected: domain.RepairClassRepairDisabled,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := passingRepairInput()
			testCase.mutate(&input)
			decision := domain.DecideRepairLoop(input)
			if decision.CanRepair() {
				t.Fatalf("%s: 不得自动修复", testCase.name)
			}
			if decision.Completed() {
				t.Fatalf("%s: 不得判定为完成", testCase.name)
			}
			if decision.Class != testCase.expected {
				t.Fatalf("%s: 期望类别 %s，实际 %s（%s）",
					testCase.name, testCase.expected, decision.Class, decision.Summary)
			}
			if decision.Summary == "" {
				t.Fatalf("%s: 停止必须给出可读说明", testCase.name)
			}
			if err := decision.Class.Validate(); err != nil {
				t.Fatalf("%s: 类别必须是合法取值: %v", testCase.name, err)
			}
		})
	}
}

// TestDecideRepairLoopCompletedOnlyWhenVerificationPassed 验证只有通过才结束循环。
func TestDecideRepairLoopCompletedOnlyWhenVerificationPassed(t *testing.T) {
	t.Parallel()
	input := passingRepairInput()
	input.Verification = domain.StepVerificationPassed
	input.Attempts = input.MaxAttempts
	input.Budget = domain.RepairBudget{MaxAttempts: input.MaxAttempts, Consumed: input.MaxAttempts}
	decision := domain.DecideRepairLoop(input)
	if !decision.Completed() {
		t.Fatalf("验证通过时必须结束循环，实际 %s（%s）", decision.Kind, decision.Class)
	}
	if decision.CanRepair() {
		t.Fatal("验证通过后不得再修复")
	}
	if decision.Class != domain.RepairClassCompleted {
		t.Fatalf("结束类别不符: %s", decision.Class)
	}
}

// TestDecideRepairLoopCompletedPrecedesUnpersisted 验证通过优先于落库/证据类停止条件。
//
// 规则顺序固定：通过是唯一成功路径，落库与基础设施判定只用于拒绝"未通过却想修复"。
func TestDecideRepairLoopCompletedPrecedesUnpersisted(t *testing.T) {
	t.Parallel()
	input := passingRepairInput()
	input.Verification = domain.StepVerificationPassed
	input.Finalized = false
	input.FeedbackPersisted = false
	if decision := domain.DecideRepairLoop(input); !decision.Completed() {
		t.Fatalf("验证通过时应结束循环，实际 %s（%s）", decision.Kind, decision.Class)
	}
}

// TestDecideRepairLoopCancelledPrecedesEverything 验证取消优先于其他所有条件。
func TestDecideRepairLoopCancelledPrecedesEverything(t *testing.T) {
	t.Parallel()
	input := passingRepairInput()
	input.Cancelled = true
	input.Verification = domain.StepVerificationPassed
	decision := domain.DecideRepairLoop(input)
	if decision.Class != domain.RepairClassCancelled {
		t.Fatalf("取消必须优先，实际类别 %s", decision.Class)
	}
	if decision.CanRepair() {
		t.Fatal("取消后不得启动下一轮")
	}
}

// TestRepairFailureClassValidateRejectsUnknown 验证未知类别被拒绝。
func TestRepairFailureClassValidateRejectsUnknown(t *testing.T) {
	t.Parallel()
	if err := domain.RepairFailureClass("unknown_class").Validate(); err == nil {
		t.Fatal("未知停止类别必须被拒绝")
	} else if !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("错误类别不符: %v", err)
	}
}

// TestDecideRepairCloseoutMapping 验证停止类别到步骤终态的映射。
func TestDecideRepairCloseoutMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		decision domain.RepairDecision
		expected domain.StepStatus
		verified bool
	}{
		{
			name:     "通过",
			decision: domain.RepairDecision{Kind: domain.RepairDecisionCompleted, Class: domain.RepairClassCompleted},
			expected: domain.StepStatusCompleted,
			verified: true,
		},
		{
			name:     "取消",
			decision: domain.RepairDecision{Kind: domain.RepairDecisionStop, Class: domain.RepairClassCancelled},
			expected: domain.StepStatusCancelled,
		},
		{
			name: "达到上限",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassAttemptsExhausted,
			},
			expected: domain.StepStatusFailed,
		},
		{
			name: "未开启修复",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassRepairDisabled,
			},
			expected: domain.StepStatusFailed,
		},
		{
			name: "开发失败",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassDeveloperFailed,
			},
			expected: domain.StepStatusFailed,
		},
		{
			name: "开发超时",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassDeveloperTimedOut,
			},
			expected: domain.StepStatusFailed,
		},
		{
			name: "开发中断",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassDeveloperInterrupted,
			},
			expected: domain.StepStatusRecoveryRequired,
		},
		{
			name: "验证未启动",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassVerificationNotRun,
			},
			expected: domain.StepStatusRecoveryRequired,
		},
		{
			name: "基础设施异常",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassInfrastructure,
			},
			expected: domain.StepStatusRecoveryRequired,
		},
		{
			name: "结论不确定",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassVerificationInconclusive,
			},
			expected: domain.StepStatusRecoveryRequired,
		},
		{
			name: "记录未落库",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassNotPersisted,
			},
			expected: domain.StepStatusRecoveryRequired,
		},
		{
			name: "反馈不可用",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassFeedbackUnavailable,
			},
			expected: domain.StepStatusRecoveryRequired,
		},
		{
			name: "步骤不适用",
			decision: domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassUnsupportedStep,
			},
			expected: domain.StepStatusRecoveryRequired,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			closeout := domain.DecideRepairCloseout(domain.RepairCloseoutInput{
				Decision:    testCase.decision,
				Attempts:    2,
				MaxAttempts: 2,
			})
			if closeout.Status != testCase.expected {
				t.Fatalf("%s: 期望状态 %s，实际 %s（%s）",
					testCase.name, testCase.expected, closeout.Status, closeout.Summary)
			}
			if closeout.Verified() != testCase.verified {
				t.Fatalf("%s: Verified 不符", testCase.name)
			}
			if closeout.Summary == "" {
				t.Fatalf("%s: 收口必须给出说明", testCase.name)
			}
			if closeout.MaxAttempts != 2 {
				t.Fatalf("%s: 上限必须写入收口记录，实际 %d", testCase.name, closeout.MaxAttempts)
			}
		})
	}
}

// TestDecideRepairCloseoutExhaustedMentionsLimit 验证达到上限的摘要说明用尽次数。
func TestDecideRepairCloseoutExhaustedMentionsLimit(t *testing.T) {
	t.Parallel()
	closeout := domain.DecideRepairCloseout(domain.RepairCloseoutInput{
		Decision: domain.RepairDecision{
			Kind:  domain.RepairDecisionStop,
			Class: domain.RepairClassAttemptsExhausted,
		},
		Attempts:    3,
		MaxAttempts: 3,
	})
	if closeout.Status != domain.StepStatusFailed {
		t.Fatalf("状态不符: %s", closeout.Status)
	}
	for _, fragment := range []string{"尝试次数上限", "3"} {
		if !containsFragment(closeout.Summary, fragment) {
			t.Fatalf("摘要缺少 %q: %s", fragment, closeout.Summary)
		}
	}
}

// TestRepairCloseoutAttemptsLimitOr 验证上限缺失时退化到给定值。
func TestRepairCloseoutAttemptsLimitOr(t *testing.T) {
	t.Parallel()
	empty := domain.RepairCloseoutDecision{}
	if got := empty.AttemptsLimitOr(4); got != 4 {
		t.Fatalf("缺失上限时应退化到给定值，实际 %d", got)
	}
	filled := domain.RepairCloseoutDecision{MaxAttempts: 3}
	if got := filled.AttemptsLimitOr(4); got != 3 {
		t.Fatalf("已记录上限时应使用记录值，实际 %d", got)
	}
}

// TestRetryConfigAllowsVerificationFailure 验证修复开关的语义。
func TestRetryConfigAllowsVerificationFailure(t *testing.T) {
	t.Parallel()
	if !(domain.RetryConfig{}).Allows(domain.RetryClassVerificationFailure) {
		t.Fatal("默认策略应允许验证失败修复")
	}
	explicit := domain.RetryConfig{
		RetryClasses: []domain.RetryErrorClass{domain.RetryClassAgentFailure},
	}
	if explicit.Allows(domain.RetryClassVerificationFailure) {
		t.Fatal("显式排除验证失败时不得修复")
	}
	if !(domain.RetryConfig{RetryVerification: true}).Allows(domain.RetryClassVerificationFailure) {
		t.Fatal("显式开启修复时应允许")
	}
}

// containsFragment 判断文本是否包含片段。
func containsFragment(value, fragment string) bool {
	return len(fragment) == 0 || indexOf(value, fragment) >= 0
}

// indexOf 返回子串位置，未找到返回 -1。
func indexOf(value, fragment string) int {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return index
		}
	}
	return -1
}
