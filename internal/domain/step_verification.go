package domain

// StepVerificationState 表示某个步骤当前所处的验证状态。
//
// 它是从 Validation Run 派生出来的决策与展示视图，不是新的持久化枚举：数据库仍只
// 保存 validation_runs.status 与 validation_runs.verdict，本类型不写入任何表，也不
// 得被用来替代那两列。这样"验证流程发生了什么"与"现有证据能否支持通过"的分离不
// 会因为多了一个聚合视图而被抹平。
type StepVerificationState string

// 步骤验证状态取值。
const (
	// StepVerificationUnverified 表示该步骤还没有任何验证记录。
	StepVerificationUnverified StepVerificationState = "unverified"
	// StepVerificationRunning 表示验证已经登记但尚未收敛。
	StepVerificationRunning StepVerificationState = "verifying"
	// StepVerificationPassed 表示验证跑完且结论为通过。
	StepVerificationPassed StepVerificationState = "passed"
	// StepVerificationFailed 表示验证跑完且结论为未通过或不确定。
	StepVerificationFailed StepVerificationState = "failed"
	// StepVerificationInconclusive 表示验证跑完但结论不确定（例如必需检查结果无法
	// 确认，或 Validator 要求人工复核）。它与 failed 必须区分：failed 说明已经拿到
	// "没有通过"的证据，inconclusive 说明证据本身不足以判定通过或失败。
	StepVerificationInconclusive StepVerificationState = "inconclusive"
	// StepVerificationCancelled 表示验证被取消。
	StepVerificationCancelled StepVerificationState = "cancelled"
	// StepVerificationInfrastructure 表示验证因基础设施问题没有拿到可用证据。
	StepVerificationInfrastructure StepVerificationState = "infrastructure_error"
)

// Validate 校验步骤验证状态是否属于已知取值。
func (s StepVerificationState) Validate() error {
	switch s {
	case StepVerificationUnverified,
		StepVerificationRunning,
		StepVerificationPassed,
		StepVerificationFailed,
		StepVerificationInconclusive,
		StepVerificationCancelled,
		StepVerificationInfrastructure:
		return nil
	default:
		return invalidField("StepVerification", "state", "未知的验证状态 "+string(s))
	}
}

// Passed 判断验证状态是否为通过。
//
// 只有明确跑完且结论为通过的验证才算通过：取消、基础设施异常与未验证都不是通过。
func (s StepVerificationState) Passed() bool {
	return s == StepVerificationPassed
}

// BlocksCompletion 判断该验证状态是否阻止步骤进入"已验证完成"。
func (s StepVerificationState) BlocksCompletion() bool {
	return !s.Passed()
}

// ValidationInfrastructureAnomaly 判断一次验证是否因基础设施问题无法给出可用证据。
//
// 判定只看两类结构化事实，不看自然语言：
//   - 验证流程本身失败或整体超时：没有跑完，属于基础设施异常；
//   - 任一启用的必需检查状态为 error：命令没有真正启动或结果无法确认，这不是
//     "命令以非零码正常结束"，不能当作代码验证失败。
//
// 单条检查超时、取消与退出码失败都不在此列：它们是真实执行后得到的结果。
func ValidationInfrastructureAnomaly(
	status ValidationRunStatus,
	results []ValidationCheckResult,
) bool {
	switch status {
	case ValidationRunStatusFailed, ValidationRunStatusTimedOut:
		return true
	}
	for _, result := range results {
		if result.Required && result.Status == ValidationCheckStatusError {
			return true
		}
	}
	return false
}

// DeriveStepVerificationState 依据验证记录与检查结果推导步骤验证状态。
//
// run 为 nil 表示该步骤还没有验证记录；results 允许为空（部分检查未执行）。
func DeriveStepVerificationState(
	run *ValidationRun,
	results []ValidationCheckResult,
) StepVerificationState {
	if run == nil {
		return StepVerificationUnverified
	}
	switch run.Status {
	case ValidationRunStatusPending, ValidationRunStatusRunning:
		return StepVerificationRunning
	case ValidationRunStatusCancelled:
		return StepVerificationCancelled
	}
	if run.Verdict == VerificationVerdictPassed {
		return StepVerificationPassed
	}
	if ValidationInfrastructureAnomaly(run.Status, results) {
		return StepVerificationInfrastructure
	}
	// 结论不确定与结论失败必须区分：前者没有拿到可用证据，后者已经拿到"没有通过"
	// 的证据。把两者压平成 failed 会让"要求人工复核"被误读为"代码有问题"。
	if run.Verdict == VerificationVerdictInconclusive {
		return StepVerificationInconclusive
	}
	return StepVerificationFailed
}

// DeriveStepVerificationStateFrom 依据验证流程状态、结论与检查结果推导步骤验证状态。
//
// 它是 DeriveStepVerificationState 的便捷形式：闭环协调器在调用验证引擎后拿到的是
// 结果对象而不是数据库记录，两者必须用同一套规则推导，不能各写一份。
func DeriveStepVerificationStateFrom(
	status ValidationRunStatus,
	verdict VerificationVerdict,
	results []ValidationCheckResult,
) StepVerificationState {
	return DeriveStepVerificationState(&ValidationRun{Status: status, Verdict: verdict}, results)
}

// StepCloseoutInput 是计算步骤收口状态所需的全部证据。
//
// 它只包含结构化事实：开发阶段的执行结论、是否真的发起过验证、以及验证状态。
// 收口不得依赖自然语言结论，也不得依赖调用方"认为"的结果。
type StepCloseoutInput struct {
	// Outcome 是本次开发执行的结论。
	Outcome StepExecutionOutcome
	// VerificationRan 表示开发成功之后是否真的启动过验证。
	VerificationRan bool
	// Verification 是本次验证的派生状态，未发起验证时忽略。
	//
	// 语义审查的缺失已经体现在派生状态里：要求了审查却没有可用结论时，
	// 验证结论是不确定，派生状态因此不可能通过。收口规则不需要第二条判据。
	Verification StepVerificationState
}

// StepCloseoutDecision 是步骤收口结果。
type StepCloseoutDecision struct {
	// Status 是步骤应当进入的状态。
	Status StepStatus
	// Summary 是写入步骤的结果说明。
	Summary string
}

// Verified 判断收口结论是否为"已验证完成"。
func (d StepCloseoutDecision) Verified() bool {
	return d.Status == StepStatusCompleted
}

// DecideStepCloseout 依据开发执行结论与验证状态计算步骤的收口状态。
//
// 规则（顺序固定，不可被 Agent 结论覆盖）：
//  1. 开发阶段未成功：取消 → cancelled，中断 → recovery_required，其余 → failed；
//  2. 开发阶段成功但没有发起验证：recovery_required，绝不默认视为通过；
//  3. 开发阶段成功且验证通过：completed（已验证完成）；
//  4. 开发阶段成功但验证未通过：failed；
//  5. 开发阶段成功但验证被取消：cancelled；
//  6. 开发阶段成功但验证基础设施异常：recovery_required，不伪装成代码失败，也不判通过；
//  7. 开发阶段成功但验证结论不确定（含 Validator 要求人工复核）：recovery_required，
//     既不判通过，也不写成"验证未通过"。
//
// 该函数是纯函数：不读数据库、不启动进程，因此可以被完整地单元测试。
func DecideStepCloseout(input StepCloseoutInput) StepCloseoutDecision {
	if input.Outcome != StepExecutionCompleted {
		return StepCloseoutDecision{
			Status:  closeoutStatusOfOutcome(input.Outcome),
			Summary: "开发阶段未成功: " + string(input.Outcome),
		}
	}
	if !input.VerificationRan {
		return StepCloseoutDecision{
			Status:  StepStatusRecoveryRequired,
			Summary: "开发阶段已完成，但验证阶段未能启动，需要人工核验后重新执行",
		}
	}
	switch input.Verification {
	case StepVerificationPassed:
		return StepCloseoutDecision{
			Status:  StepStatusCompleted,
			Summary: "开发阶段已完成，全部必需检查通过",
		}
	case StepVerificationFailed:
		return StepCloseoutDecision{
			Status:  StepStatusFailed,
			Summary: "开发阶段已完成，但验证未通过",
		}
	case StepVerificationCancelled:
		return StepCloseoutDecision{
			Status:  StepStatusCancelled,
			Summary: "开发阶段已完成，但验证被取消，不能判定通过",
		}
	case StepVerificationInfrastructure:
		return StepCloseoutDecision{
			Status:  StepStatusRecoveryRequired,
			Summary: "开发阶段已完成，但验证因基础设施异常未拿到可用证据，需要人工核验",
		}
	case StepVerificationInconclusive:
		// 不确定与失败必须区分：要求人工复核或评估不可解释时，步骤既不是"通过"，
		// 也不该被当作"代码失败"留下误导性的失败结论。
		return StepCloseoutDecision{
			Status:  StepStatusRecoveryRequired,
			Summary: "开发阶段已完成，但验证结论不确定（需要人工复核），不能判定通过",
		}
	default:
		return StepCloseoutDecision{
			Status:  StepStatusRecoveryRequired,
			Summary: "开发阶段已完成，但验证状态未收敛，不能判定通过",
		}
	}
}

// closeoutStatusOfOutcome 把未成功的开发结论映射为步骤状态。
func closeoutStatusOfOutcome(outcome StepExecutionOutcome) StepStatus {
	switch outcome {
	case StepExecutionCancelled:
		return StepStatusCancelled
	case StepExecutionInterrupted:
		return StepStatusRecoveryRequired
	default:
		return StepStatusFailed
	}
}

// RepairCloseoutInput 是修复循环停止后计算步骤终态所需的证据。
//
// 它与 StepCloseoutInput 表达的是同一件事的不同视角：后者描述"单轮开发—验证如何
// 收口"，前者描述"整条修复循环因为什么停下"。两者都不允许依赖自然语言结论。
type RepairCloseoutInput struct {
	// Decision 是修复循环的停止或结束判定。
	Decision RepairDecision
	// Attempts 是本次循环中已确认落库的开发尝试次数（含首轮）。
	Attempts int
	// MaxAttempts 是步骤生效的尝试次数上限。
	MaxAttempts int
}

// RepairCloseoutDecision 是修复循环的收口结论。
type RepairCloseoutDecision struct {
	// Status 是步骤应当进入的状态。
	Status StepStatus
	// Summary 是写入步骤的结果说明。
	Summary string
	// MaxAttempts 是本次循环生效的尝试次数上限（含首次执行）。
	//
	// 它与 Status 一起写入收口记录，使"为什么停在这里"在展示层可解释：达到上限
	// 与未开启修复都会停在 failed，但原因完全不同。
	MaxAttempts int
}

// Verified 判断收口结论是否为"已验证完成"。
func (d RepairCloseoutDecision) Verified() bool {
	return d.Status == StepStatusCompleted
}

// AttemptsLimitOr 返回收口记录中的尝试上限，缺失时退化为给定值。
//
// 历史记录（本字段引入之前写入的）没有上限信息，展示层不应因此显示 0。
func (d RepairCloseoutDecision) AttemptsLimitOr(fallback int) int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return fallback
}

// DecideRepairCloseout 依据修复循环判定计算步骤的最终状态。
//
// 规则（顺序固定）：
//  1. 验证通过：completed，这是唯一允许按成功路径收口的结论；
//  2. 取消：cancelled；
//  3. 结果无法确认（持久化失败、基础设施异常、验证未启动、反馈不可用）：recovery_required，
//     既不判通过，也不写成"代码有问题"；
//  4. 验证结论不确定或步骤不适用自动修复：recovery_required；
//  5. 开发中断：recovery_required；
//  6. 其余真实失败（开发失败、验证失败且已用尽上限或未开启修复）：failed。
//
// 达到尝试上限属于"真实的代码失败"：所有轮次都拿到了可解释的失败证据，因此步骤
// 进入 failed 而不是 recovery_required，失败摘要说明已用尽的上限。
func DecideRepairCloseout(input RepairCloseoutInput) RepairCloseoutDecision {
	class := input.Decision.Class
	if input.Decision.Completed() {
		return RepairCloseoutDecision{
			Status:      StepStatusCompleted,
			Summary:     "开发阶段已完成，全部必需检查通过",
			MaxAttempts: input.MaxAttempts,
		}
	}
	switch class {
	case RepairClassCancelled:
		return RepairCloseoutDecision{
			Status:      StepStatusCancelled,
			Summary:     "修复循环被取消，不能判定通过",
			MaxAttempts: input.MaxAttempts,
		}
	case RepairClassNotPersisted,
		RepairClassInfrastructure,
		RepairClassVerificationNotRun,
		RepairClassFeedbackUnavailable,
		RepairClassCycleUnresolved,
		RepairClassExecutionActive,
		RepairClassNewCycleDenied,
		RepairClassCycleClosed:
		return RepairCloseoutDecision{
			Status:      StepStatusRecoveryRequired,
			Summary:     "修复循环停止：结果无法确认，需要人工核验: " + input.Decision.Summary,
			MaxAttempts: input.MaxAttempts,
		}
	case RepairClassDeveloperTimedOut, RepairClassDeveloperFailed,
		RepairClassRepairDisabled:
		// 开发阶段的真实失败与"未开启自动修复"都是可解释的失败证据：
		// 它们停在 failed，等待用户显式决定是否重新执行。
		return RepairCloseoutDecision{
			Status:      StepStatusFailed,
			Summary:     "修复循环停止：开发阶段未成功或未开启自动修复: " + input.Decision.Summary,
			MaxAttempts: input.MaxAttempts,
		}
	case RepairClassVerificationInconclusive,
		RepairClassUnsupportedStep,
		RepairClassDeveloperInterrupted:
		return RepairCloseoutDecision{
			Status:      StepStatusRecoveryRequired,
			Summary:     "修复循环停止：需要人工核验: " + input.Decision.Summary,
			MaxAttempts: input.MaxAttempts,
		}
	case RepairClassAttemptsExhausted:
		return RepairCloseoutDecision{
			Status: StepStatusFailed,
			Summary: "已达到尝试次数上限 " + itoa(input.MaxAttempts) +
				"（共 " + itoa(input.Attempts) + " 次开发尝试），验证仍未通过",
			MaxAttempts: input.MaxAttempts,
		}
	default:
		return RepairCloseoutDecision{
			Status:      StepStatusFailed,
			Summary:     "修复循环停止：验证未通过: " + input.Decision.Summary,
			MaxAttempts: input.MaxAttempts,
		}
	}
}
