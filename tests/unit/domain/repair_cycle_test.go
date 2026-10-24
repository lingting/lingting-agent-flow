package domain_test

import (
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// timeAt 返回测试使用的固定时间。
func timeAt() time.Time {
	return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
}

// repairCycleFacts 构造一份"预算已用尽、可以开启新周期"的基准输入。
//
// 基准语义：上限 2，本周期已有 2 次确定结束的尝试，步骤未被占用。
func repairCycleFacts() domain.RepairStartInput {
	cycle, err := domain.NewRepairCycle(
		"task-1",
		"step-1",
		1,
		2,
		"first_run",
		timeAt(),
	)
	if err != nil {
		panic(err)
	}
	return domain.RepairStartInput{
		StepID:          "step-1",
		StepType:        domain.StepTypeDevelop,
		StepMaxAttempts: 2,
		Retry:           domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		Cycle:           cycle,
		HasCycle:        true,
		Cycles:          []domain.RepairCycle{cycle},
		Attempts: []domain.RepairAttemptFacts{
			{ID: "attempt-1", Status: domain.AttemptStatusCompleted, BudgetScopeID: cycle.ID},
			{ID: "attempt-2", Status: domain.AttemptStatusCompleted, BudgetScopeID: cycle.ID},
		},
	}
}

// TestDecideRepairBudgetCountsOnlyCurrentCycle 验证预算只统计当前周期的尝试。
func TestDecideRepairBudgetCountsOnlyCurrentCycle(t *testing.T) {
	t.Parallel()
	input := repairCycleFacts()
	budget := domain.DecideRepairBudget(domain.RepairBudgetInput{
		StepID:          input.StepID,
		StepMaxAttempts: input.StepMaxAttempts,
		Cycle:           input.Cycle,
		HasCycle:        true,
		Attempts:        input.Attempts,
	})
	if budget.Consumed != 2 {
		t.Fatalf("已消耗次数不符: %d", budget.Consumed)
	}
	if budget.Remaining() != 0 {
		t.Fatalf("剩余预算应为 0，实际 %d", budget.Remaining())
	}
	if !budget.Exhausted() {
		t.Fatal("上限已用尽时必须判定为 exhausted")
	}
	// 其他周期的尝试不消耗本周期预算：这是"新周期获得新预算"的落点。
	other := domain.RepairAttemptFacts{
		ID:            "attempt-old",
		Status:        domain.AttemptStatusCompleted,
		BudgetScopeID: "cycle-old",
	}
	budget = domain.DecideRepairBudget(domain.RepairBudgetInput{
		StepID:          input.StepID,
		StepMaxAttempts: input.StepMaxAttempts,
		Cycle:           input.Cycle,
		HasCycle:        true,
		Attempts:        append(append([]domain.RepairAttemptFacts{}, input.Attempts...), other),
	})
	if budget.Consumed != 2 {
		t.Fatalf("其他周期的尝试不得计入本周期，实际 %d", budget.Consumed)
	}
}

// TestRepairAttemptConsumesBudget 验证各尝试状态是否消耗预算。
func TestRepairAttemptConsumesBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status   domain.AttemptStatus
		consumes bool
	}{
		{status: domain.AttemptStatusRunning, consumes: true},
		{status: domain.AttemptStatusCompleted, consumes: true},
		{status: domain.AttemptStatusFailed, consumes: true},
		{status: domain.AttemptStatusTimedOut, consumes: true},
		{status: domain.AttemptStatusInterrupted, consumes: true},
		{status: domain.AttemptStatusUnknown, consumes: true},
		{status: domain.AttemptStatusRecoveryRequired, consumes: true},
		{status: domain.AttemptStatusCancelled, consumes: false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(string(testCase.status), func(t *testing.T) {
			t.Parallel()
			facts := domain.RepairAttemptFacts{Status: testCase.status}
			if got := facts.ConsumesBudget(); got != testCase.consumes {
				t.Fatalf("%s: 期望消耗预算=%v，实际 %v", testCase.status, testCase.consumes, got)
			}
		})
	}
}

// TestRepairAttemptUnresolved 验证"结果不确定"的尝试判定。
func TestRepairAttemptUnresolved(t *testing.T) {
	t.Parallel()
	cases := map[domain.AttemptStatus]bool{
		domain.AttemptStatusUnknown:          true,
		domain.AttemptStatusRecoveryRequired: true,
		domain.AttemptStatusInterrupted:      true,
		domain.AttemptStatusCompleted:        false,
		domain.AttemptStatusFailed:           false,
		domain.AttemptStatusTimedOut:         false,
		domain.AttemptStatusCancelled:        false,
		domain.AttemptStatusRunning:          false,
	}
	for status, expected := range cases {
		status, expected := status, expected
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			facts := domain.RepairAttemptFacts{Status: status}
			if got := facts.Unresolved(); got != expected {
				t.Fatalf("%s: 期望 unresolved=%v，实际 %v", status, expected, got)
			}
		})
	}
}

// TestDecideRepairStartContinuesWithinBudget 验证仍在上限内允许开始。
func TestDecideRepairStartContinuesWithinBudget(t *testing.T) {
	t.Parallel()
	input := repairCycleFacts()
	input.Attempts = input.Attempts[:1]
	decision := domain.DecideRepairStart(input)
	if !decision.Allowed {
		t.Fatalf("仍在上限内应允许开始，实际 %s（%s）", decision.Class, decision.Summary)
	}
	if decision.OpenCycle {
		t.Fatal("仍在上限内时不得开启新周期")
	}
	if decision.Budget.Remaining() != 1 {
		t.Fatalf("剩余预算不符: %d", decision.Budget.Remaining())
	}
	if decision.Summary == "" {
		t.Fatal("判定必须给出可读说明")
	}
}

// TestDecideRepairStartStopsAtExhausted 验证预算用尽时停止。
func TestDecideRepairStartStopsAtExhausted(t *testing.T) {
	t.Parallel()
	decision := domain.DecideRepairStart(repairCycleFacts())
	if decision.Allowed {
		t.Fatal("预算用尽时不得继续执行")
	}
	if decision.Class != domain.RepairClassAttemptsExhausted {
		t.Fatalf("停止类别不符: %s", decision.Class)
	}
}

// TestDecideRepairStartStopClasses 验证各类拒绝开始的场景。
func TestDecideRepairStartStopClasses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		mutate   func(input *domain.RepairStartInput)
		expected domain.RepairFailureClass
	}{
		{
			name:     "步骤仍被占用",
			mutate:   func(input *domain.RepairStartInput) { input.StepOccupied = true },
			expected: domain.RepairClassExecutionActive,
		},
		{
			name: "存在中断尝试",
			mutate: func(input *domain.RepairStartInput) {
				input.Attempts = append(input.Attempts, domain.RepairAttemptFacts{
					ID:            "attempt-3",
					Status:        domain.AttemptStatusInterrupted,
					BudgetScopeID: input.Cycle.ID,
				})
			},
			expected: domain.RepairClassCycleUnresolved,
		},
		{
			name: "存在未知结果尝试",
			mutate: func(input *domain.RepairStartInput) {
				input.Attempts = append(input.Attempts, domain.RepairAttemptFacts{
					ID:            "attempt-3",
					Status:        domain.AttemptStatusUnknown,
					BudgetScopeID: input.Cycle.ID,
				})
			},
			expected: domain.RepairClassCycleUnresolved,
		},
		{
			name: "存在需要核验的尝试",
			mutate: func(input *domain.RepairStartInput) {
				input.Attempts = append(input.Attempts, domain.RepairAttemptFacts{
					ID:            "attempt-3",
					Status:        domain.AttemptStatusRecoveryRequired,
					BudgetScopeID: input.Cycle.ID,
				})
			},
			expected: domain.RepairClassCycleUnresolved,
		},
		{
			name: "引用了未登记的周期",
			mutate: func(input *domain.RepairStartInput) {
				input.Attempts = append(input.Attempts, domain.RepairAttemptFacts{
					ID:            "attempt-3",
					Status:        domain.AttemptStatusCompleted,
					BudgetScopeID: "cycle-missing",
				})
			},
			expected: domain.RepairClassCycleUnresolved,
		},
		{
			name: "上一周期已结束但未显式开启新周期",
			mutate: func(input *domain.RepairStartInput) {
				input.HasCycle = false
				input.Cycle = domain.RepairCycle{}
			},
			expected: domain.RepairClassCycleClosed,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := repairCycleFacts()
			input.Attempts = input.Attempts[:1]
			testCase.mutate(&input)
			decision := domain.DecideRepairStart(input)
			if decision.Allowed {
				t.Fatalf("%s: 不得允许开始", testCase.name)
			}
			if decision.Class != testCase.expected {
				t.Fatalf("%s: 期望类别 %s，实际 %s（%s）",
					testCase.name, testCase.expected, decision.Class, decision.Summary)
			}
			if decision.Summary == "" {
				t.Fatalf("%s: 拒绝必须给出可读说明", testCase.name)
			}
			if err := decision.Class.Validate(); err != nil {
				t.Fatalf("%s: 类别必须是合法取值: %v", testCase.name, err)
			}
		})
	}
}

// TestDecideRepairStartOpensNewCycleWhenExhausted 验证预算用尽后允许开启新周期。
func TestDecideRepairStartOpensNewCycleWhenExhausted(t *testing.T) {
	t.Parallel()
	input := repairCycleFacts()
	input.OpenNewCycle = true
	decision := domain.DecideRepairStart(input)
	if !decision.Allowed || !decision.OpenCycle {
		t.Fatalf("预算用尽且已收敛时应允许开启新周期，实际 %s（%s）",
			decision.Class, decision.Summary)
	}
	if decision.Budget.Consumed != 2 {
		t.Fatalf("判定必须报告上一周期已消耗次数，实际 %d", decision.Budget.Consumed)
	}
}

// TestDecideRepairStartRejectsNewCycleWithBudgetLeft 验证仍有余额时拒绝开启新周期。
func TestDecideRepairStartRejectsNewCycleWithBudgetLeft(t *testing.T) {
	t.Parallel()
	input := repairCycleFacts()
	input.Attempts = input.Attempts[:1]
	input.OpenNewCycle = true
	decision := domain.DecideRepairStart(input)
	if decision.Allowed {
		t.Fatal("仍有剩余预算时不得通过开启新周期重置预算")
	}
	if decision.Class != domain.RepairClassNewCycleDenied {
		t.Fatalf("停止类别不符: %s（%s）", decision.Class, decision.Summary)
	}
}

// TestDecideRepairStartAllowsNewCycleWhenRepairDisabled 验证策略关闭时允许开启新周期。
func TestDecideRepairStartAllowsNewCycleWhenRepairDisabled(t *testing.T) {
	t.Parallel()
	input := repairCycleFacts()
	input.Attempts = input.Attempts[:1]
	input.OpenNewCycle = true
	input.Retry = domain.RetryConfig{
		MaxAttempts:  2,
		RetryClasses: []domain.RetryErrorClass{domain.RetryClassAgentFailure},
	}
	decision := domain.DecideRepairStart(input)
	if !decision.Allowed || !decision.OpenCycle {
		t.Fatalf("策略不再允许修复时应允许开启新周期，实际 %s（%s）",
			decision.Class, decision.Summary)
	}
}

// TestDecideRepairStartRejectsNewCycleWithoutHistory 验证首次执行不构成"新周期"。
func TestDecideRepairStartRejectsNewCycleWithoutHistory(t *testing.T) {
	t.Parallel()
	input := repairCycleFacts()
	input.Attempts = nil
	input.HasCycle = false
	input.Cycle = domain.RepairCycle{}
	input.OpenNewCycle = true
	decision := domain.DecideRepairStart(input)
	if decision.Allowed {
		t.Fatal("没有任何历史执行时不存在可重置的周期")
	}
	if decision.Class != domain.RepairClassNewCycleDenied {
		t.Fatalf("停止类别不符: %s", decision.Class)
	}
}

// TestDecideRepairStartUsesLastCycleBudget 验证周期结束后仍能判定上一周期预算。
func TestDecideRepairStartUsesLastCycleBudget(t *testing.T) {
	t.Parallel()
	input := repairCycleFacts()
	input.HasCycle = false
	input.Cycle = domain.RepairCycle{}
	input.LastCycle = input.Cycles[0]
	input.HasLastCycle = true
	input.OpenNewCycle = true
	decision := domain.DecideRepairStart(input)
	if !decision.Allowed || !decision.OpenCycle {
		t.Fatalf("上一周期已用尽预算时应允许开启新周期，实际 %s（%s）",
			decision.Class, decision.Summary)
	}
	// 同一现场下若上一周期仍有余额，则必须拒绝。
	input.Attempts = input.Attempts[:1]
	decision = domain.DecideRepairStart(input)
	if decision.Allowed {
		t.Fatal("上一周期仍有余额时不得开启新周期")
	}
	if decision.Class != domain.RepairClassNewCycleDenied {
		t.Fatalf("停止类别不符: %s", decision.Class)
	}
}

// TestRepairCycleValidateRejectsInvalid 验证周期登记的字段校验。
func TestRepairCycleValidateRejectsInvalid(t *testing.T) {
	t.Parallel()
	if _, err := domain.NewRepairCycle("task-1", "step-1", 0, 3, "user_reopen", timeAt()); err == nil {
		t.Fatal("周期序号必须从 1 开始")
	}
	if _, err := domain.NewRepairCycle("task-1", "step-1", 1, 0, "user_reopen", timeAt()); err == nil {
		t.Fatal("尝试上限必须大于 0")
	}
	if _, err := domain.NewRepairCycle(
		"task-1", "step-1", 1, domain.MaxTaskMaxAttempts+1, "user_reopen", timeAt(),
	); err == nil {
		t.Fatal("尝试上限不得超过领域上限")
	}
	if _, err := domain.NewRepairCycle("task-1", "step-1", 1, 3, "", timeAt()); err == nil {
		t.Fatal("开启来源不能为空")
	}
}

// TestRepairBudgetScopeValidateRejectsUnknown 验证未知预算作用域被拒绝。
func TestRepairBudgetScopeValidateRejectsUnknown(t *testing.T) {
	t.Parallel()
	if err := domain.RepairBudgetScope("unknown").Validate(); err == nil {
		t.Fatal("未知预算作用域必须被拒绝")
	}
	if err := domain.RepairBudgetScopeNone.Validate(); err != nil {
		t.Fatalf("空作用域是合法取值: %v", err)
	}
	if err := domain.RepairBudgetScopeCycle.Validate(); err != nil {
		t.Fatalf("cycle 是合法取值: %v", err)
	}
}

// TestAttemptRejectsHalfBudgetScope 验证作用域与周期标识必须同时出现。
func TestAttemptRejectsHalfBudgetScope(t *testing.T) {
	t.Parallel()
	attempt, err := domain.NewAttempt("task-1", "step-1", 1, timeAt())
	if err != nil {
		t.Fatalf("构造尝试失败: %v", err)
	}
	attempt.BudgetScopeID = "cycle-1"
	if err := attempt.Validate(); err == nil {
		t.Fatal("只有周期标识没有作用域时必须被拒绝")
	}
	attempt.BudgetScopeID = ""
	attempt.BudgetScope = domain.RepairBudgetScopeCycle
	if err := attempt.Validate(); err == nil {
		t.Fatal("作用域为 cycle 但缺少周期标识时必须被拒绝")
	}
	attempt.BudgetScopeID = "cycle-1"
	if err := attempt.Validate(); err != nil {
		t.Fatalf("完整的作用域应当合法: %v", err)
	}
}
