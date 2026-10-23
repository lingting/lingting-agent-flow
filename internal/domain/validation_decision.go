package domain

import (
	"fmt"
	"strings"
)

// ValidationDecisionInput 是计算最终验证结论所需的全部证据。
//
// 它只包含结构化事实：检查配置、检查结果与（可选的）Agent 辅助评估。
// 判定不得依赖自然语言结论，也不得依赖调用方"认为"的结论。
type ValidationDecisionInput struct {
	// Spec 是本次验证的配置快照。
	Spec ValidationSpec
	// Results 是已经产生的检查结果，允许少于配置（部分检查未执行）。
	Results []ValidationCheckResult
	// RunStatus 是验证流程状态，用于拒绝为未完成或已中断的验证判定通过。
	RunStatus ValidationRunStatus
	// Assessment 是可选 Validator Agent 辅助评估。
	//
	// 它只能影响"是否需要人工复核"这一侧的判定：Agent 声称通过不得覆盖必需
	// 检查的失败，也不得替代缺失的必需检查。
	Assessment *AgentAssessment
	// ReviewStatus 描述语义审查本身发生了什么，零值等价于"本次不要求语义审查"。
	//
	// 语义审查是可选增强：要求了审查却拿不到可用评估时，结论只能是 inconclusive；
	// 未要求审查时只依据确定性检查判定，不会因为没有 Agent 结论而阻塞。
	ReviewStatus ReviewStatus
}

// ReviewStatus 描述一次 Validator Agent 语义审查的实际结论。
//
// 它与 ValidationRunStatus 严格分离：后者描述确定性检查流程，前者描述语义审查。
// 两者都必须分别持久化，不能用一个字段表达"验证跑完了但审查没跑成"。
type ReviewStatus string

// 语义审查状态取值。
const (
	// ReviewStatusNotRequested 表示本次验证没有要求语义审查。
	ReviewStatusNotRequested ReviewStatus = ""
	// ReviewStatusPassed 表示审查完成并给出可解释的通过评估。
	ReviewStatusPassed ReviewStatus = "passed"
	// ReviewStatusFailed 表示审查完成并给出可解释的未通过评估。
	ReviewStatusFailed ReviewStatus = "failed"
	// ReviewStatusNeedsHumanReview 表示审查完成但要求人工复核。
	ReviewStatusNeedsHumanReview ReviewStatus = "needs_human_review"
	// ReviewStatusUnparseable 表示审查调用了模型，但输出不是可解释的评估。
	ReviewStatusUnparseable ReviewStatus = "unparseable"
	// ReviewStatusInvalidOutput 表示输出结构合法但未通过领域校验（例如条数超限）。
	ReviewStatusInvalidOutput ReviewStatus = "invalid_output"
	// ReviewStatusCallFailed 表示审查调用本身失败（非零退出、启动失败）。
	ReviewStatusCallFailed ReviewStatus = "call_failed"
	// ReviewStatusTimedOut 表示审查调用超时。
	ReviewStatusTimedOut ReviewStatus = "timed_out"
	// ReviewStatusCancelled 表示审查调用被取消。
	ReviewStatusCancelled ReviewStatus = "cancelled"
	// ReviewStatusInterrupted 表示审查调用因服务停止中断。
	ReviewStatusInterrupted ReviewStatus = "interrupted"
	// ReviewStatusNotConfigured 表示任务没有配置验证角色，审查无法进行。
	ReviewStatusNotConfigured ReviewStatus = "not_configured"
)

// Validate 校验语义审查状态是否属于已知取值。
func (s ReviewStatus) Validate() error {
	switch s {
	case ReviewStatusNotRequested,
		ReviewStatusPassed,
		ReviewStatusFailed,
		ReviewStatusNeedsHumanReview,
		ReviewStatusUnparseable,
		ReviewStatusInvalidOutput,
		ReviewStatusCallFailed,
		ReviewStatusTimedOut,
		ReviewStatusCancelled,
		ReviewStatusInterrupted,
		ReviewStatusNotConfigured:
		return nil
	default:
		return invalidField("Review", "status", "未知的语义审查状态 "+string(s))
	}
}

// Reviewed 判断语义审查是否真的执行并给出了可解释结论。
func (s ReviewStatus) Reviewed() bool {
	switch s {
	case ReviewStatusPassed, ReviewStatusFailed, ReviewStatusNeedsHumanReview:
		return true
	default:
		return false
	}
}

// Usable 判断语义审查是否得到了可用于最终判定的结论。
//
// 只有"审查完成且可解释"才是可用结论：调用失败、输出不可解释与未配置都不是。
func (s ReviewStatus) Usable() bool {
	return s.Reviewed()
}

// ValidationDecision 是确定性规则计算出的结论。
type ValidationDecision struct {
	// Verdict 是最终结论。
	Verdict VerificationVerdict
	// FailureSummary 是结论原因，通过时为空。
	FailureSummary string
}

// Passed 判断结论是否为通过。
func (d ValidationDecision) Passed() bool {
	return d.Verdict == VerificationVerdictPassed
}

// DecideValidation 依据确定性规则计算最终验证结论。
//
// 规则（顺序固定，不可被 Agent 结论覆盖）：
//  1. 验证流程必须处于 completed：取消、超时与失败的流程一律不判通过；
//  2. 至少存在一项启用的必需检查；
//  3. 每一项启用的必需检查都必须有结果，且状态为 passed；
//  4. 任一必需检查失败、超时、取消、跳过或错误，结论为 failed；
//  5. 必需检查结果缺失（未执行）同样不得视为通过；
//  6. 可选检查失败不影响结论，但保留在结果中；
//  7. 只有在确定性检查全部通过之后，才允许参考语义审查与 Agent 辅助评估：
//     要求审查却没有拿到可用结论判 inconclusive，评估未通过判 failed，
//     要求人工复核判 inconclusive。
//
// 该函数是纯函数：不读数据库、不启动进程，因此可以被完整地单元测试。
func DecideValidation(input ValidationDecisionInput) ValidationDecision {
	// 未完成的验证不可能给出"通过"：取消、超时与流程失败都按 failed 处理，
	// 因为它们都属于"没有拿到可用证据"，不能伪装成成功。
	if input.RunStatus != ValidationRunStatusCompleted {
		return ValidationDecision{
			Verdict:        VerificationVerdictFailed,
			FailureSummary: describeUnfinishedRun(input.RunStatus),
		}
	}
	if !input.Spec.HasRequiredCheck() {
		return ValidationDecision{
			Verdict:        VerificationVerdictFailed,
			FailureSummary: "没有启用的必需检查，无法判定验证通过",
		}
	}
	results := indexCheckResults(input.Results)
	var (
		missing []string
		failed  []string
		reasons []string
	)
	for _, definition := range input.Spec.Ordered() {
		if !definition.IsRequired() {
			continue
		}
		result, ok := results[definition.ID]
		if !ok {
			missing = append(missing, definition.Name)
			continue
		}
		if result.Status.Passed() {
			continue
		}
		failed = append(failed, definition.Name)
		if reason := describeCheckFailure(result); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	if len(missing) > 0 {
		// 未执行的必需检查不得被视为通过：这是最容易伪装成成功的一类缺陷。
		return ValidationDecision{
			Verdict:        VerificationVerdictFailed,
			FailureSummary: "必需检查未执行: " + strings.Join(missing, ", "),
		}
	}
	if len(failed) > 0 {
		summary := "必需检查未通过: " + strings.Join(failed, ", ")
		if len(reasons) > 0 {
			summary += "（" + strings.Join(reasons, "；") + "）"
		}
		return ValidationDecision{Verdict: VerificationVerdictFailed, FailureSummary: summary}
	}
	// 语义审查是确定性的"证据门槛"：要求了审查却没有拿到可用结论时不能判通过，
	// 否则"审查失败"会被当作"审查通过"。
	if decision, blocked := decideWithReviewStatus(input.ReviewStatus, input.Assessment); blocked {
		return decision
	}
	return decideWithAssessment(input.Assessment)
}

// decideWithReviewStatus 依据语义审查状态决定是否允许继续判定。
//
// 返回值 blocked 为 true 时，decision 就是最终结论：
//   - 未要求审查：不阻塞，继续按确定性检查与评估判定；
//   - 审查完成且可解释：不阻塞，具体结论由评估本身给出；
//   - 审查调用失败、超时、取消、中断、输出不可解释或未配置：一律判 inconclusive，
//     因为它们都属于"要求了审查但没有拿到可用证据"。
//
// 该函数只收紧结论，不判断评估内容：评估的语义在 decideWithAssessment 中处理，
// 两处职责不重叠。
func decideWithReviewStatus(
	status ReviewStatus,
	assessment *AgentAssessment,
) (ValidationDecision, bool) {
	switch status {
	case ReviewStatusNotRequested:
		return ValidationDecision{}, false
	case ReviewStatusPassed:
		// 语义审查声称通过，但缺少可解释的评估结构：没有证据就不能判通过。
		if assessment == nil {
			return ValidationDecision{
				Verdict:        VerificationVerdictInconclusive,
				FailureSummary: "语义审查缺少可解释的评估结果，不能判定通过",
			}, true
		}
		return ValidationDecision{}, false
	case ReviewStatusFailed, ReviewStatusNeedsHumanReview:
		return ValidationDecision{}, false
	}
	return ValidationDecision{
		Verdict:        VerificationVerdictInconclusive,
		FailureSummary: describeReviewFailure(status),
	}, true
}

// describeReviewFailure 说明语义审查为什么没有给出可用结论。
func describeReviewFailure(status ReviewStatus) string {
	switch status {
	case ReviewStatusUnparseable:
		return "语义审查输出无法解析为结构化评估，不能判定通过"
	case ReviewStatusInvalidOutput:
		return "语义审查输出未通过领域校验，不能判定通过"
	case ReviewStatusCallFailed:
		return "语义审查调用失败，不能判定通过"
	case ReviewStatusTimedOut:
		return "语义审查超时，不能判定通过"
	case ReviewStatusCancelled:
		return "语义审查被取消，不能判定通过"
	case ReviewStatusInterrupted:
		return "语义审查因服务停止中断，不能判定通过"
	case ReviewStatusNotConfigured:
		return "任务未配置验证角色，语义审查未执行，不能判定通过"
	default:
		return "语义审查状态 " + string(status) + " 不允许判定通过"
	}
}

// decideWithAssessment 在确定性检查与语义审查都允许继续判定后参考辅助评估。
// 评估只能收紧结论，不能放宽：它没有能力把失败的必需检查改判为通过，
// 因为此处只在必需检查全部通过之后才会被调用。
func decideWithAssessment(assessment *AgentAssessment) ValidationDecision {
	if assessment == nil {
		return ValidationDecision{Verdict: VerificationVerdictPassed}
	}
	if err := assessment.Validate(); err != nil {
		return ValidationDecision{
			Verdict:        VerificationVerdictInconclusive,
			FailureSummary: "辅助评估结果不可解释: " + err.Error(),
		}
	}
	if !assessment.Passed {
		findings := assessment.BlockingFindings()
		summary := strings.TrimSpace(assessment.Summary)
		if summary == "" {
			summary = "辅助评估判定未通过"
		}
		if len(findings) > 0 {
			summary += ": " + strings.Join(findings, "; ")
		}
		return ValidationDecision{Verdict: VerificationVerdictFailed, FailureSummary: summary}
	}
	if assessment.NeedsHumanReview {
		return ValidationDecision{
			Verdict:        VerificationVerdictInconclusive,
			FailureSummary: "辅助评估要求人工复核",
		}
	}
	return ValidationDecision{Verdict: VerificationVerdictPassed}
}

// indexCheckResults 按检查标识索引结果，重复标识保留第一条。
func indexCheckResults(results []ValidationCheckResult) map[string]ValidationCheckResult {
	indexed := make(map[string]ValidationCheckResult, len(results))
	for _, result := range results {
		if _, exists := indexed[result.CheckID]; exists {
			continue
		}
		indexed[result.CheckID] = result
	}
	return indexed
}

// describeUnfinishedRun 说明未完成的验证流程为什么不能判通过。
func describeUnfinishedRun(status ValidationRunStatus) string {
	switch status {
	case ValidationRunStatusCancelled:
		return "验证被取消，不能判定通过"
	case ValidationRunStatusTimedOut:
		return "验证超时，不能判定通过"
	case ValidationRunStatusFailed:
		return "验证流程失败，不能判定通过"
	case ValidationRunStatusPending:
		return "验证尚未执行，不能判定通过"
	case ValidationRunStatusRunning:
		return "验证仍在执行，不能判定通过"
	default:
		return "验证流程状态 " + string(status) + " 不允许判定通过"
	}
}

// describeCheckFailure 生成单项检查失败的可读原因。
func describeCheckFailure(result ValidationCheckResult) string {
	if reason := strings.TrimSpace(result.FailureReason); reason != "" {
		return result.CheckName + ": " + reason
	}
	switch result.Status {
	case ValidationCheckStatusSkipped:
		return result.CheckName + ": 检查被跳过"
	case ValidationCheckStatusTimedOut:
		return result.CheckName + ": 检查超时"
	case ValidationCheckStatusCancelled:
		return result.CheckName + ": 检查被取消"
	case ValidationCheckStatusError:
		return result.CheckName + ": 检查执行失败"
	case ValidationCheckStatusFailed:
		return fmt.Sprintf("%s: 退出码 %d", result.CheckName, result.ExitCode)
	default:
		return result.CheckName + ": 检查未通过"
	}
}

// CountCheckResults 统计检查结果的通过、失败与跳过数量。
//
// 计数只统计已经产生的结果：未执行与执行中的检查不进入任何一类，避免把
// "还没有结果"统计成"通过"。
func CountCheckResults(results []ValidationCheckResult) (passed, failed, skipped int) {
	for _, result := range results {
		switch result.Status {
		case ValidationCheckStatusPassed:
			passed++
		case ValidationCheckStatusSkipped:
			skipped++
		default:
			if result.Status.IsTerminal() {
				failed++
			}
		}
	}
	return passed, failed, skipped
}
