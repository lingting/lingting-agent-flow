package domain

// 本文件只保留确定性验证与语义审查共用的结构化辅助评估类型。
//
// 历史上这里还定义了一套独立的验证结果模型（VerificationResult / VerificationInput /
// NewVerificationResult / decideVerdict），与 validation_runs 并存构成第二套判定规则。
// 生产链路的验证结论现在只由 DecideValidation 计算，那套模型已删除；历史表
// verification_results 仅作为已冻结的遗留数据保留，不再有运行时读写方。

// AgentAssessment 是 Validator Agent 的结构化辅助评估结果。
//
// 它是辅助证据，不能覆盖必需确定性检查的失败结果，也不能直接决定任务状态。
type AgentAssessment struct {
	// Passed 是 Agent 自述的评估结论。
	Passed bool `json:"passed"`
	// Summary 是评估摘要。
	Summary string `json:"summary,omitempty"`
	// Findings 是发现的问题。
	Findings []string `json:"findings,omitempty"`
	// Evidence 是证据引用，例如文件路径或命令名称。
	Evidence []string `json:"evidence,omitempty"`
	// SuggestFix 表示建议进行修复型重试。
	SuggestFix bool `json:"suggest_fix,omitempty"`
	// NeedsHumanReview 表示需要人工检查。
	NeedsHumanReview bool `json:"needs_human_review,omitempty"`
}

// Validate 校验辅助评估结果。
func (a AgentAssessment) Validate() error {
	if len(a.Findings) > MaxAcceptanceCriteria {
		return invalidField("AgentAssessment", "findings", "问题数量超出上限")
	}
	if len(a.Evidence) > MaxAcceptanceCriteria {
		return invalidField("AgentAssessment", "evidence", "证据数量超出上限")
	}
	return nil
}

// BlockingFindings 返回阻止成功的问题数量。
//
// 只有 Agent 明确声明评估未通过时，其问题才作为阻止成功的证据；评估通过时
// 发现的问题作为独立证据保留，由调用方按既定规则判断。
func (a AgentAssessment) BlockingFindings() []string {
	if a.Passed {
		return nil
	}
	return a.Findings
}
