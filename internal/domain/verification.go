package domain

import (
	"strings"
	"time"
)

// VerificationCheck 是一条人工可读的验收项检查结果。
type VerificationCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
	// Source 标记检查来源：deterministic 或 agent。
	Source string `json:"source,omitempty"`
}

// Validate 校验验收项检查结果。
func (c VerificationCheck) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return invalidField("VerificationCheck", "name", "检查项名称不能为空")
	}
	return nil
}

// 检查来源取值。
const (
	// CheckSourceDeterministic 表示由确定性检查命令得出。
	CheckSourceDeterministic = "deterministic"
	// CheckSourceAgent 表示由 Validator Agent 辅助评估得出。
	CheckSourceAgent = "agent"
)

// CommandResult 是一条验证命令的执行结果。
//
// 输出保留用于失败证据，不作为业务状态的唯一记录。退出码、超时标记与执行是否
// 确认结束是判定依据，不允许被自然语言结论覆盖。
type CommandResult struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	// Required 表示该命令是否必需：可选命令失败不影响必需检查门槛。
	Required bool `json:"required"`
	// AllowTimeoutPass 表示超时也视为通过。
	AllowTimeoutPass bool `json:"allow_timeout_pass,omitempty"`
	// AllowedExitCodes 是视为通过的退出码集合，空值等价于只允许 0。
	AllowedExitCodes []int  `json:"allowed_exit_codes,omitempty"`
	ExitCode         int    `json:"exit_code"`
	Stdout           string `json:"stdout,omitempty"`
	Stderr           string `json:"stderr,omitempty"`
	TimedOut         bool   `json:"timed_out,omitempty"`
	Cancelled        bool   `json:"cancelled,omitempty"`
	// Certain 表示可以确认命令已经结束；为 false 时结果不确定。
	Certain  bool          `json:"certain"`
	Duration time.Duration `json:"duration,omitempty"`
	// Truncated 表示输出因超过上限被截断。
	Truncated bool `json:"truncated,omitempty"`
}

// Passed 判断命令是否成功结束。
//
// 结果不确定（Certain 为 false）、被取消或超时（且未声明允许超时通过）都不算通过。
func (r CommandResult) Passed() bool {
	if !r.Certain || r.Cancelled {
		return false
	}
	if r.TimedOut {
		return r.AllowTimeoutPass
	}
	if len(r.Command) == 0 {
		return false
	}
	if len(r.AllowedExitCodes) == 0 {
		return r.ExitCode == 0
	}
	for _, code := range r.AllowedExitCodes {
		if code == r.ExitCode {
			return true
		}
	}
	return false
}

// BlocksSuccess 判断该命令是否阻止验证通过。
func (r CommandResult) BlocksSuccess() bool {
	return r.Required && !r.Passed()
}

// Uncertain 判断命令结果是否不确定。
func (r CommandResult) Uncertain() bool {
	return !r.Certain || r.Cancelled
}

// Validate 校验命令执行结果。
func (r CommandResult) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return invalidField("CommandResult", "name", "命令名称不能为空")
	}
	if strings.TrimSpace(r.Command) == "" {
		return invalidField("CommandResult", "command", "命令内容不能为空")
	}
	if r.Duration < 0 {
		return invalidField("CommandResult", "duration", "耗时不能为负数")
	}
	return nil
}

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

// VerificationResult 是某次验证的结构化结果。
//
// 每次验证都产生独立记录，不允许用更新字段的方式覆盖历史。
type VerificationResult struct {
	ID                 ID                  `json:"id"`
	TaskID             ID                  `json:"task_id"`
	StepID             ID                  `json:"step_id"`
	AttemptID          ID                  `json:"attempt_id"`
	ValidatorSessionID *ID                 `json:"validator_session_id,omitempty"`
	Passed             bool                `json:"passed"`
	Verdict            VerificationVerdict `json:"verdict"`
	Summary            string              `json:"summary,omitempty"`
	Checks             []VerificationCheck `json:"checks,omitempty"`
	CommandResults     []CommandResult     `json:"command_results,omitempty"`
	Assessment         *AgentAssessment    `json:"assessment,omitempty"`
	// ReviewSkipped 表示因必需检查失败而跳过辅助评估。
	ReviewSkipped  bool      `json:"review_skipped,omitempty"`
	FailureSummary string    `json:"failure_summary,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// VerificationInput 是构建验证结果所需的输入。
type VerificationInput struct {
	TaskID             ID
	StepID             ID
	AttemptID          ID
	ValidatorSessionID *ID
	Summary            string
	Checks             []VerificationCheck
	CommandResults     []CommandResult
	Assessment         *AgentAssessment
	// ReviewRequested 表示本次验证请求了 Validator Agent 辅助评估。
	//
	// 请求了辅助评估却没有可解释的结果时判定为 inconclusive；未请求时只依据
	// 确定性检查与验收项判定，不会因为没有 Agent 结论而阻塞。
	ReviewRequested bool
	ReviewSkipped   bool
	FailureSummary  string
}

// NewVerificationResult 根据检查项、命令结果与辅助评估构建验证结果。
//
// 判定规则（不可被 Agent 结论覆盖）：
//   - 任意必需命令失败，判定为 failed；
//   - 任意必需命令结果不确定或取消，判定为 inconclusive；
//   - 必需命令未执行时，不会被默认视为通过：缺少必需命令结果即为 inconclusive；
//   - 必需命令全部通过后，才允许参考 Agent 评估；
//   - Agent 评估未通过或要求人工复核时，判定为 failed 或 inconclusive；
//   - Agent 输出不可解释（评估缺失但要求评估）由调用方标记为 inconclusive。
func NewVerificationResult(input VerificationInput, at time.Time) (VerificationResult, error) {
	verdict, failure := decideVerdict(input)
	result := VerificationResult{
		ID:                 NewID(),
		TaskID:             input.TaskID,
		StepID:             input.StepID,
		AttemptID:          input.AttemptID,
		ValidatorSessionID: input.ValidatorSessionID,
		Passed:             verdict == VerificationVerdictPassed,
		Verdict:            verdict,
		Summary:            strings.TrimSpace(input.Summary),
		Checks:             input.Checks,
		CommandResults:     input.CommandResults,
		Assessment:         input.Assessment,
		ReviewSkipped:      input.ReviewSkipped,
		FailureSummary:     strings.TrimSpace(failure),
		CreatedAt:          normalizeTime(at),
	}
	// 调用方给出的失败摘要优先：它通常是更精确的证据说明。
	if verdict != VerificationVerdictPassed {
		if provided := strings.TrimSpace(input.FailureSummary); provided != "" {
			result.FailureSummary = provided
		}
	}
	if err := result.Validate(); err != nil {
		return VerificationResult{}, err
	}
	return result, nil
}

// decideVerdict 依据确定性检查与辅助评估给出最终判定。
func decideVerdict(input VerificationInput) (VerificationVerdict, string) {
	uncertain := false
	failed := make([]string, 0, len(input.CommandResults))
	for _, command := range input.CommandResults {
		if !command.Required || command.Passed() {
			continue
		}
		if command.Uncertain() {
			uncertain = true
			continue
		}
		failed = append(failed, command.Name)
	}
	if len(failed) > 0 {
		return VerificationVerdictFailed, "必需检查未通过: " + strings.Join(failed, ", ")
	}
	if uncertain {
		return VerificationVerdictInconclusive, "必需检查结果无法确认"
	}
	for _, check := range input.Checks {
		if !check.Passed && check.Source != CheckSourceAgent {
			return VerificationVerdictFailed, "检查项未通过: " + check.Name
		}
	}
	if input.Assessment == nil {
		if input.ReviewSkipped || !input.ReviewRequested {
			return VerificationVerdictPassed, ""
		}
		// 请求了辅助评估但没有可解释的结果：不能静默判为通过。
		return VerificationVerdictInconclusive, "辅助评估结果缺失或不可解释"
	}
	if err := input.Assessment.Validate(); err != nil {
		return VerificationVerdictInconclusive, "辅助评估结果不可解释: " + err.Error()
	}
	if !input.Assessment.Passed {
		findings := input.Assessment.BlockingFindings()
		summary := strings.TrimSpace(input.Assessment.Summary)
		if summary == "" {
			summary = "辅助评估判定未通过"
		}
		if len(findings) > 0 {
			summary += ": " + strings.Join(findings, "; ")
		}
		return VerificationVerdictFailed, summary
	}
	if input.Assessment.NeedsHumanReview {
		return VerificationVerdictInconclusive, "辅助评估要求人工复核"
	}
	return VerificationVerdictPassed, ""
}

// Validate 校验验证结果定义是否完整。
func (r VerificationResult) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if err := r.TaskID.Validate(); err != nil {
		return err
	}
	if err := r.StepID.Validate(); err != nil {
		return err
	}
	if err := r.AttemptID.Validate(); err != nil {
		return err
	}
	if r.ValidatorSessionID != nil {
		if err := r.ValidatorSessionID.Validate(); err != nil {
			return err
		}
	}
	if err := r.Verdict.Validate(); err != nil {
		return err
	}
	if r.Verdict == VerificationVerdictNone {
		return invalidField("VerificationResult", "verdict", "验证判定不能为空")
	}
	if r.Passed != (r.Verdict == VerificationVerdictPassed) {
		return invalidField("VerificationResult", "passed", "passed 必须与验证判定一致")
	}
	for _, check := range r.Checks {
		if err := check.Validate(); err != nil {
			return err
		}
	}
	for _, command := range r.CommandResults {
		if err := command.Validate(); err != nil {
			return err
		}
		if r.Passed && command.BlocksSuccess() {
			return invalidField(
				"VerificationResult",
				"command_results",
				"必需检查失败时验证结果不能为通过: "+command.Name,
			)
		}
	}
	if r.Assessment != nil {
		if err := r.Assessment.Validate(); err != nil {
			return err
		}
		if r.Passed && !r.Assessment.Passed {
			return invalidField(
				"VerificationResult",
				"assessment",
				"辅助评估未通过时验证结果不能为通过",
			)
		}
	}
	if !r.Passed && strings.TrimSpace(r.FailureSummary) == "" {
		return invalidField("VerificationResult", "failure_summary", "验证失败时必须提供失败摘要")
	}
	return nil
}

// FailedChecks 返回未通过的检查项名称。
func (r VerificationResult) FailedChecks() []string {
	names := make([]string, 0, len(r.Checks))
	for _, check := range r.Checks {
		if !check.Passed {
			names = append(names, check.Name)
		}
	}
	return names
}

// FailedCommands 返回未成功的命令结果名称。
func (r VerificationResult) FailedCommands() []string {
	names := make([]string, 0, len(r.CommandResults))
	for _, command := range r.CommandResults {
		if !command.Passed() {
			names = append(names, command.Name)
		}
	}
	return names
}

// RequiredCommandsPassed 判断所有必需命令是否都通过。
func (r VerificationResult) RequiredCommandsPassed() bool {
	for _, command := range r.CommandResults {
		if command.BlocksSuccess() {
			return false
		}
	}
	return true
}

// HasRequiredCommands 判断结果中是否存在必需命令。
func (r VerificationResult) HasRequiredCommands() bool {
	for _, command := range r.CommandResults {
		if command.Required {
			return true
		}
	}
	return false
}
