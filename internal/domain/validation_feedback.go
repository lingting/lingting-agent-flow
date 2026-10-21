package domain

import (
	"regexp"
	"strings"
	"time"
)

// 失败反馈相关的长度上限。
const (
	// MaxFeedbackOutputSummary 是单条检查输出摘要保留的字节上限。
	//
	// 反馈会进入下一次 Developer 提示词，因此必须比落盘的完整输出更短。
	MaxFeedbackOutputSummary = 1024
	// MaxFeedbackReason 是单条失败原因保留的字节上限。
	MaxFeedbackReason = 512
	// MaxFeedbackSummary 是整体反馈摘要保留的字节上限。
	MaxFeedbackSummary = 2048
	// MaxFeedbackChecks 是反馈中保留的失败检查条数上限。
	MaxFeedbackChecks = 20
)

// 敏感信息占位符。
const redactedPlaceholder = "***已隐藏***"

// sensitivePatterns 是错误摘要中必须过滤的敏感片段。
//
// 只做保守的模式匹配：宁可多隐藏一些，也不能把令牌、私钥或口令写进失败反馈，
// 因为反馈会被拼进下一次 Agent 提示词并持久化到数据库。
var sensitivePatterns = []*regexp.Regexp{
	// Authorization: Bearer xxx / Basic xxx
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)((?:bearer|basic|token)?\s*[^\s,;]+)`),
	// 常见键值形式的凭据：password=、token:、api_key=、secret=
	regexp.MustCompile(`(?i)((?:password|passwd|pwd|token|secret|api[_-]?key|access[_-]?key|private[_-]?key)\s*[:=]\s*)("[^"]*"|'[^']*'|\S+)`),
	// URL 内嵌凭据：scheme://user:pass@host
	regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://[^\s/:@]+):([^\s/@]+)@`),
	// PEM 私钥块
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	// SSH 私钥常见起始行（未闭合块时的兜底）
	regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// RedactSensitiveText 过滤文本中的凭据类片段。
//
// 该函数不识别业务语义，只处理明确的凭据形态；调用方仍需避免把敏感内容写进
// 输出（例如不要把环境变量整体打印出来）。
func RedactSensitiveText(value string) string {
	redacted := value
	for _, pattern := range sensitivePatterns {
		redacted = pattern.ReplaceAllStringFunc(redacted, func(match string) string {
			groups := pattern.FindStringSubmatch(match)
			switch len(groups) {
			case 0:
				return redactedPlaceholder
			case 2:
				// 只有前缀分组（例如 PEM 起始行）：整体替换。
				return redactedPlaceholder
			case 3:
				// 前缀 + 凭据：保留前缀，隐藏凭据。
				return groups[1] + redactedPlaceholder
			default:
				return redactedPlaceholder
			}
		})
	}
	return redacted
}

// ClipFeedbackText 过滤敏感信息并按字节上限裁剪文本。
//
// 裁剪与脱敏的顺序固定为"先脱敏再裁剪"：先裁剪可能把凭据截断成无法识别的片段。
func ClipFeedbackText(value string, limit int) string {
	trimmed := strings.TrimSpace(RedactSensitiveText(value))
	if trimmed == "" {
		return ""
	}
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	truncated := trimmed[:limit]
	for len(truncated) > 0 && !isValidUTF8Boundary(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return strings.TrimRight(truncated, " \t\r\n") + "…（已截断）"
}

// isValidUTF8Boundary 判断字节串是否是合法的 UTF-8 前缀。
func isValidUTF8Boundary(value string) bool {
	for index := len(value) - 1; index >= 0 && index > len(value)-4; index-- {
		if value[index]&0xC0 != 0x80 {
			return isUTF8RuneStart(value[index])
		}
	}
	return true
}

// isUTF8RuneStart 判断字节是否是 UTF-8 字符的起始字节。
func isUTF8RuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

// ValidationFailureFeedback 是一次验证失败的结构化反馈。
//
// 它由真实检查结果生成并随 Validation Run 一起保留，而不是覆盖步骤上的一个字符串
// 字段：同一轮验证的多条失败检查、退出码与输出摘要都需要可追溯。
//
// 反馈只描述"上一轮发生了什么"，不能被当作当前验证结论，也不能替代新的验证。
type ValidationFailureFeedback struct {
	// TaskID / StepID 是反馈所属的任务与步骤。
	TaskID ID `json:"task_id"`
	StepID ID `json:"step_id"`
	// DeveloperAttemptID 是产生待验证代码的那次开发尝试。
	DeveloperAttemptID ID `json:"developer_attempt_id"`
	// ValidationRunID 是产生反馈的验证记录。
	ValidationRunID ID `json:"validation_run_id"`
	// DeveloperSessionIDs 是该次开发尝试关联的会话，便于回溯上下文。
	DeveloperSessionIDs []ID `json:"developer_session_ids,omitempty"`
	// Verdict 是上一轮验证的结论。
	Verdict VerificationVerdict `json:"verdict"`
	// Summary 是上一轮验证的结论摘要。
	Summary string `json:"summary,omitempty"`
	// InfrastructureError 表示上一轮验证因基础设施异常没有拿到可用证据。
	InfrastructureError bool `json:"infrastructure_error,omitempty"`
	// Checks 是未通过的检查，最多保留 MaxFeedbackChecks 条。
	Checks []ValidationCheckFeedback `json:"checks,omitempty"`
	// CreatedAt 是反馈生成时间。
	CreatedAt time.Time `json:"created_at"`
}

// ValidationCheckFeedback 是单条未通过检查的反馈。
type ValidationCheckFeedback struct {
	// CheckID / CheckName 是检查的稳定标识与名称快照。
	CheckID   string `json:"check_id"`
	CheckName string `json:"check_name"`
	// Required 表示该检查是否属于必需检查。
	Required bool `json:"required"`
	// Status 是检查的实际状态。
	Status ValidationCheckStatus `json:"status"`
	// ExitCode 是实际退出码，未能取得时为 -1。
	ExitCode int `json:"exit_code"`
	// FailureReason 是判定原因（已脱敏并裁剪）。
	FailureReason string `json:"failure_reason,omitempty"`
	// ErrorDetail 是执行层错误摘要（已脱敏并裁剪）。
	ErrorDetail string `json:"error_detail,omitempty"`
	// StdoutSummary / StderrSummary 是有长度限制的输出摘要（已脱敏）。
	StdoutSummary string `json:"stdout_summary,omitempty"`
	StderrSummary string `json:"stderr_summary,omitempty"`
	// TimedOut / Cancelled 分别表达超时与取消，二者互不替代。
	TimedOut  bool `json:"timed_out,omitempty"`
	Cancelled bool `json:"cancelled,omitempty"`
	// InfrastructureError 表示该检查没有拿到可用证据（无法启动或结果无法确认）。
	InfrastructureError bool `json:"infrastructure_error,omitempty"`
	// StartedAt / FinishedAt 是检查的实际执行时间。
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// ValidationFeedbackInput 是构建失败反馈所需的输入。
type ValidationFeedbackInput struct {
	TaskID    ID
	StepID    ID
	Attempt   Attempt
	Run       ValidationRun
	Results   []ValidationCheckResult
	Developer []ID
}

// NewValidationFailureFeedback 依据验证记录与检查结果构建失败反馈。
//
// 只有确实存在未通过的检查时才应调用本函数：结论为通过时没有需要修复的问题。
// 输出摘要与原因在这里统一脱敏与裁剪，调用方不需要再次处理。
func NewValidationFailureFeedback(
	input ValidationFeedbackInput,
	at time.Time,
) ValidationFailureFeedback {
	feedback := ValidationFailureFeedback{
		TaskID:              input.TaskID,
		StepID:              input.StepID,
		DeveloperAttemptID:  input.Attempt.ID,
		ValidationRunID:     input.Run.ID,
		DeveloperSessionIDs: append([]ID(nil), input.Developer...),
		Verdict:             input.Run.Verdict,
		Summary:             ClipFeedbackText(input.Run.FailureSummary, MaxFeedbackSummary),
		InfrastructureError: ValidationInfrastructureAnomaly(input.Run.Status, input.Results),
		CreatedAt:           normalizeTime(at),
	}
	if feedback.Summary == "" {
		feedback.Summary = ClipFeedbackText(input.Run.Summary, MaxFeedbackSummary)
	}
	for _, result := range input.Results {
		if result.Status.Passed() {
			continue
		}
		if len(feedback.Checks) >= MaxFeedbackChecks {
			break
		}
		feedback.Checks = append(feedback.Checks, checkFeedbackOf(result))
	}
	return feedback
}

// checkFeedbackOf 把一条检查结果转换为反馈条目。
func checkFeedbackOf(result ValidationCheckResult) ValidationCheckFeedback {
	return ValidationCheckFeedback{
		CheckID:       result.CheckID,
		CheckName:     result.CheckName,
		Required:      result.Required,
		Status:        result.Status,
		ExitCode:      result.ExitCode,
		FailureReason: ClipFeedbackText(result.FailureReason, MaxFeedbackReason),
		ErrorDetail:   ClipFeedbackText(result.ErrorDetail, MaxFeedbackReason),
		StdoutSummary: ClipFeedbackText(result.StdoutSummary, MaxFeedbackOutputSummary),
		StderrSummary: ClipFeedbackText(result.StderrSummary, MaxFeedbackOutputSummary),
		TimedOut:      result.TimedOut,
		Cancelled:     result.Cancelled,
		// 检查状态为 error 表示命令没有真正执行或结果无法确认，属于基础设施问题；
		// 退出码失败与超时都是真实执行后得到的结果。
		InfrastructureError: result.Status == ValidationCheckStatusError,
		StartedAt:           result.StartedAt,
		FinishedAt:          result.FinishedAt,
	}
}

// FailedCheckNames 返回未通过检查的名称列表。
func (f ValidationFailureFeedback) FailedCheckNames() []string {
	names := make([]string, 0, len(f.Checks))
	for _, check := range f.Checks {
		names = append(names, check.CheckName)
	}
	return names
}

// HasBlockingChecks 判断反馈中是否存在阻止成功的必需检查。
func (f ValidationFailureFeedback) HasBlockingChecks() bool {
	for _, check := range f.Checks {
		if check.Required && !check.Status.Passed() {
			return true
		}
	}
	return false
}

// ValidationFeedbackRequest 是显式重试时的反馈引用。
//
// 它只表达"上一次验证失败了哪些检查"，不携带上一轮的结论作为当前结论：新的执行
// 仍然必须创建新的 Attempt 与独立 Session，并通过新的验证。
type ValidationFeedbackRequest struct {
	// Feedback 是上一轮的失败反馈，为空表示没有可引用的反馈。
	Feedback *ValidationFailureFeedback
}
