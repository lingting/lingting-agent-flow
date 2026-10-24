package domain

import "strings"

// DefaultMaxAttempts 是未显式配置时的步骤尝试次数上限。
const DefaultMaxAttempts = 3

// RetryPolicy 约束某个步骤允许的最大尝试次数。
//
// 上限判断属于业务规则，重试的编排与执行由 workflow 层负责。
type RetryPolicy struct {
	MaxAttempts int `json:"max_attempts"`
}

// NewRetryPolicy 创建重试策略，未配置时使用默认上限。
func NewRetryPolicy(maxAttempts int) (RetryPolicy, error) {
	policy := RetryPolicy{MaxAttempts: maxAttempts}
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = DefaultMaxAttempts
	}
	if err := policy.Validate(); err != nil {
		return RetryPolicy{}, err
	}
	return policy, nil
}

// Validate 校验重试策略。
func (p RetryPolicy) Validate() error {
	if p.MaxAttempts < 1 {
		return invalidField("RetryPolicy", "max_attempts", "尝试次数上限必须大于 0")
	}
	return nil
}

// CanRetry 判断在已完成 attemptNumber 次尝试后是否仍可重试。
func (p RetryPolicy) CanRetry(attemptNumber int) bool {
	limit := p.MaxAttempts
	if limit < 1 {
		limit = DefaultMaxAttempts
	}
	return attemptNumber < limit
}

// NextAttemptNumber 返回下一次尝试应使用的序号。
func (p RetryPolicy) NextAttemptNumber(attemptNumber int) int {
	return attemptNumber + 1
}

// RepairFailureClass 表示一次验证失败修复循环停止时的原因类别。
//
// 它是纯领域取值，用于把"为什么不再继续修复"从错误文本中分离出来：调用方据此
// 决定终态映射与可观测性，而不需要解析自然语言。
type RepairFailureClass string

// 修复循环的停止原因类别。
const (
	// RepairClassNone 表示循环尚未得出停止结论。
	RepairClassNone RepairFailureClass = ""
	// RepairClassCompleted 表示验证通过，循环按正常成功路径结束。
	RepairClassCompleted RepairFailureClass = "completed"
	// RepairClassAttemptsExhausted 表示已达到步骤允许的尝试次数上限。
	RepairClassAttemptsExhausted RepairFailureClass = "attempts_exhausted"
	// RepairClassRepairDisabled 表示步骤的重试策略不允许验证失败自动修复。
	RepairClassRepairDisabled RepairFailureClass = "repair_disabled"
	// RepairClassUnsupportedStep 表示该步骤类型不适用自动修复（非开发步骤）。
	RepairClassUnsupportedStep RepairFailureClass = "unsupported_step"
	// RepairClassDeveloperFailed 表示开发阶段以失败结束。
	RepairClassDeveloperFailed RepairFailureClass = "developer_failed"
	// RepairClassDeveloperTimedOut 表示开发阶段超时。
	RepairClassDeveloperTimedOut RepairFailureClass = "developer_timed_out"
	// RepairClassDeveloperInterrupted 表示开发阶段因服务停止中断。
	RepairClassDeveloperInterrupted RepairFailureClass = "developer_interrupted"
	// RepairClassDeveloperCancelled 表示开发阶段被取消。
	RepairClassDeveloperCancelled RepairFailureClass = "developer_cancelled"
	// RepairClassVerificationNotRun 表示验证阶段未能启动，没有拿到任何证据。
	RepairClassVerificationNotRun RepairFailureClass = "verification_not_run"
	// RepairClassVerificationInconclusive 表示验证结论不确定，不能当作可修复的代码缺陷。
	RepairClassVerificationInconclusive RepairFailureClass = "verification_inconclusive"
	// RepairClassInfrastructure 表示验证因基础设施异常没有拿到可用证据。
	RepairClassInfrastructure RepairFailureClass = "infrastructure_error"
	// RepairClassCancelled 表示修复循环被取消。
	RepairClassCancelled RepairFailureClass = "cancelled"
	// RepairClassNotPersisted 表示关键记录未能可靠落库，结果无法确认。
	RepairClassNotPersisted RepairFailureClass = "not_persisted"
	// RepairClassFeedbackUnavailable 表示缺少可信的修复依据。
	RepairClassFeedbackUnavailable RepairFailureClass = "feedback_unavailable"
	// RepairClassExecutionActive 表示步骤仍被一次执行占用，不能启动第二个执行者。
	RepairClassExecutionActive RepairFailureClass = "execution_active"
	// RepairClassCycleUnresolved 表示历史执行的周期归属或结果无法确认，必须先核验现场。
	RepairClassCycleUnresolved RepairFailureClass = "cycle_unresolved"
	// RepairClassNewCycleDenied 表示当前不满足开启新执行周期的条件。
	RepairClassNewCycleDenied RepairFailureClass = "new_cycle_denied"
	// RepairClassCycleClosed 表示上一执行周期已结束，继续执行必须先显式开启新周期。
	RepairClassCycleClosed RepairFailureClass = "cycle_closed"
)

// Validate 校验修复停止类别是否属于已知取值。
func (c RepairFailureClass) Validate() error {
	switch c {
	case RepairClassNone,
		RepairClassCompleted,
		RepairClassAttemptsExhausted,
		RepairClassRepairDisabled,
		RepairClassUnsupportedStep,
		RepairClassDeveloperFailed,
		RepairClassDeveloperTimedOut,
		RepairClassDeveloperInterrupted,
		RepairClassDeveloperCancelled,
		RepairClassVerificationNotRun,
		RepairClassVerificationInconclusive,
		RepairClassInfrastructure,
		RepairClassCancelled,
		RepairClassNotPersisted,
		RepairClassFeedbackUnavailable,
		RepairClassExecutionActive,
		RepairClassCycleUnresolved,
		RepairClassNewCycleDenied,
		RepairClassCycleClosed:
		return nil
	default:
		return invalidField("RepairLoop", "class", "未知的修复停止类别 "+string(c))
	}
}

// RepairDecisionKind 是修复循环的下一步动作。
type RepairDecisionKind string

// 修复循环的下一步动作取值。
const (
	// RepairDecisionRetry 表示可以再启动一轮开发与完整验证。
	RepairDecisionRetry RepairDecisionKind = "retry"
	// RepairDecisionCompleted 表示验证通过，循环结束。
	RepairDecisionCompleted RepairDecisionKind = "completed"
	// RepairDecisionStop 表示停止自动修复，按既有规则收口。
	RepairDecisionStop RepairDecisionKind = "stop"
)

// RepairLoopInput 是判断"是否可以再修复一轮"所需的全部结构化证据。
//
// 它只包含持久化事实：本轮开发结论、本轮验证的派生状态与落库情况、反馈是否可靠
// 保存、上下文是否已取消，以及步骤生效的尝试上限与重试策略。判定不依赖自然语言。
type RepairLoopInput struct {
	// StepType 是当前步骤的类型：自动修复只适用于开发步骤。
	StepType StepType
	// Attempts 是本次循环中已确认落库的开发尝试次数（含首轮）。
	//
	// 它由"本轮真实产生了 Attempt 记录"累加，因此计数本身就是持久化事实，
	// 不会因为进程内重试而虚增。
	Attempts int
	// MaxAttempts 是步骤生效的尝试次数上限（含首次执行）。
	MaxAttempts int
	// Retry 是步骤的重试策略：验证失败是否允许自动修复由它表达。
	Retry RetryConfig
	// Outcome 是本轮开发执行的结论。
	Outcome StepExecutionOutcome
	// VerificationRan 表示开发成功后是否真的启动过验证。
	VerificationRan bool
	// Verification 是本轮验证的派生状态。
	Verification StepVerificationState
	// ValidationPersisted 表示验证结论与检查结果是否可靠写入。
	ValidationPersisted bool
	// InfrastructureError 表示验证因基础设施异常没有拿到可用证据。
	InfrastructureError bool
	// FeedbackPersisted 表示本轮失败反馈已可靠保存，可作为下一轮修复依据。
	FeedbackPersisted bool
	// Finalized 表示本轮收口状态是否全部写入成功。
	Finalized bool
	Cancelled bool
	// Budget 是本次循环所属执行周期的持久化预算事实。
	//
	// 它由 DecideRepairBudget 依据 Attempt 的预算作用域计算得出，不是进程内计数：
	// 进程重启后重新读取历史记录会得到同一个值，因此修复上限可以跨进程保持。
	Budget RepairBudget
	// HasBudget 表示 Budget 是否来自已读取的持久化历史。
	//
	// 为 false 时判定必须拒绝自动继续：无法读取预算就不能假定"还有余额"。
	HasBudget bool
	// UnresolvedAttempts 是历史上结果不确定、必须先核验的尝试描述。
	//
	// 非空时不得启动下一轮：unknown / recovery_required / interrupted 都可能存在
	// 未确认的副作用，直接重放会掩盖真实故障。
	UnresolvedAttempts []string
}

// RepairDecision 是修复循环的下一步动作与原因。
type RepairDecision struct {
	// Kind 是下一步动作。
	Kind RepairDecisionKind
	// Class 是停止或结束的原因类别；继续修复时为验证失败类别。
	Class RepairFailureClass
	// Summary 是面向用户的原因说明。
	Summary string
}

// CanRepair 判断本次判定是否允许再启动一轮修复。
func (d RepairDecision) CanRepair() bool {
	return d.Kind == RepairDecisionRetry
}

// Completed 判断本次判定是否为"验证通过"。
func (d RepairDecision) Completed() bool {
	return d.Kind == RepairDecisionCompleted
}

// DecideRepairLoop 依据结构化事实判断验证失败后是否允许自动修复并重新验证。
//
// 规则（顺序固定，不可被 Agent 结论或调用方意愿覆盖）：
//  1. 上下文已取消：停止，类别 cancelled，取消后不得启动下一轮 Agent；
//  2. 开发阶段未成功：按真实结论分类停止，绝不把它当作可修复的代码缺陷；
//  3. 开发成功但没有发起验证：停止（verification_not_run），没有证据就不能修复；
//  4. 验证通过：结束（completed），这是唯一的正常成功路径；
//  5. 验证结论未可靠落库或属于基础设施异常：停止（infrastructure_error），
//     结果无法确认时重试只会掩盖真实故障；
//  6. 验证结论不确定：停止（verification_inconclusive），要求人工复核；
//  7. 验证被取消：停止（cancelled）；
//  8. 其余非 failed 的验证状态：停止（verification_inconclusive），不猜测；
//  9. 收口写入未全部成功：停止（not_persisted）；
//  10. 失败反馈未可靠保存：停止（feedback_unavailable），没有修复依据就不重放调用；
//  11. 非开发步骤：停止（unsupported_step），规划步骤重跑不会产生新的计划；
//  12. 重试策略不允许验证失败自动修复：停止（repair_disabled）；
//  13. 无法从持久化历史确认本周期预算：停止（cycle_unresolved），读不到就不能假定有余额；
//  14. 存在结果不确定的历史尝试：停止（cycle_unresolved），必须先核验现场；
//  15. 本周期仍有剩余预算：允许再修复一轮（retry）；否则停止（attempts_exhausted）。
//
// 该函数是纯函数：不读数据库、不启动进程，因此可以被完整地单元测试。
func DecideRepairLoop(input RepairLoopInput) RepairDecision {
	if input.Cancelled {
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassCancelled,
			Summary: "修复循环已被取消，不再启动新的开发调用",
		}
	}
	if input.Outcome != StepExecutionCompleted {
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   developerRepairClassOf(input.Outcome),
			Summary: "开发阶段未成功: " + string(input.Outcome),
		}
	}
	if !input.VerificationRan {
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassVerificationNotRun,
			Summary: "开发阶段已完成但验证未启动，没有可用于修复的证据",
		}
	}
	if input.Verification == StepVerificationPassed {
		return RepairDecision{
			Kind:    RepairDecisionCompleted,
			Class:   RepairClassCompleted,
			Summary: "验证通过，结束修复循环",
		}
	}
	if !input.ValidationPersisted || input.InfrastructureError {
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassInfrastructure,
			Summary: "验证结论未可靠落库或属于基础设施异常，不自动修复",
		}
	}
	switch input.Verification {
	case StepVerificationInconclusive:
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassVerificationInconclusive,
			Summary: "验证结论不确定（需要人工复核），不自动修复",
		}
	case StepVerificationCancelled:
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassCancelled,
			Summary: "验证被取消，不自动修复",
		}
	case StepVerificationFailed:
	default:
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassVerificationInconclusive,
			Summary: "验证状态未收敛（" + string(input.Verification) + "），不自动修复",
		}
	}
	if !input.Finalized {
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassNotPersisted,
			Summary: "本轮收口状态未能完整写入，结果无法确认",
		}
	}
	if !input.FeedbackPersisted {
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassFeedbackUnavailable,
			Summary: "验证失败反馈未能可靠保存，缺少下一轮修复的可信依据",
		}
	}
	if input.StepType != StepTypeDevelop {
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassUnsupportedStep,
			Summary: "步骤类型 " + string(input.StepType) + " 不适用自动修复",
		}
	}
	if !input.Retry.Allows(RetryClassVerificationFailure) {
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassRepairDisabled,
			Summary: "步骤重试策略未允许验证失败自动修复",
		}
	}
	// 预算必须来自已读取的持久化历史：读不到就不能假定"还有余额"，
	// 否则进程重启后的一次读取失败会退化成"无限重试"。
	if !input.HasBudget {
		return RepairDecision{
			Kind:    RepairDecisionStop,
			Class:   RepairClassCycleUnresolved,
			Summary: "无法从持久化历史确认本执行周期的预算，停止自动修复",
		}
	}
	if len(input.UnresolvedAttempts) > 0 {
		return RepairDecision{
			Kind:  RepairDecisionStop,
			Class: RepairClassCycleUnresolved,
			Summary: "存在结果不确定的历史尝试（" +
				strings.Join(input.UnresolvedAttempts, "、") + "），必须先核验现场",
		}
	}
	budget := input.Budget
	if budget.MaxAttempts < 1 {
		budget.MaxAttempts = input.MaxAttempts
	}
	if budget.Exhausted() {
		return RepairDecision{
			Kind:  RepairDecisionStop,
			Class: RepairClassAttemptsExhausted,
			Summary: "已达到尝试次数上限 " + itoa(budget.limit()) +
				"（本周期已用 " + itoa(budget.Consumed) + " 次），停止自动修复",
		}
	}
	return RepairDecision{
		Kind:  RepairDecisionRetry,
		Class: RepairClassNone,
		Summary: "验证未通过且仍在上限内，进入第 " + itoa(budget.Consumed+1) +
			" 次修复",
	}
}

// limitOrDefault 返回生效的尝试次数上限。
func (p RetryPolicy) limitOrDefault() int {
	if p.MaxAttempts < 1 {
		return DefaultMaxAttempts
	}
	return p.MaxAttempts
}

// developerRepairClassOf 把未成功的开发结论映射为修复停止类别。
func developerRepairClassOf(outcome StepExecutionOutcome) RepairFailureClass {
	switch outcome {
	case StepExecutionTimedOut:
		return RepairClassDeveloperTimedOut
	case StepExecutionInterrupted:
		return RepairClassDeveloperInterrupted
	case StepExecutionCancelled:
		return RepairClassDeveloperCancelled
	default:
		return RepairClassDeveloperFailed
	}
}
