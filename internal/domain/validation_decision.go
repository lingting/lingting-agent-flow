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
//  7. 只有上述条件全部满足后，才允许参考 Agent 辅助评估：
//     Agent 声称未通过判 failed，要求人工复核判 inconclusive。
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
	return decideWithAssessment(input.Assessment)
}

// decideWithAssessment 在确定性检查全部通过后参考 Agent 辅助评估。
//
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
