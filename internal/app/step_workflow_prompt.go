package app

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// 失败反馈在提示词中的长度上限。
const (
	// maxFeedbackChecksInPrompt 是提示词中列出的失败检查条数上限。
	maxFeedbackChecksInPrompt = 10
	// maxFeedbackSummaryInPrompt 是单条失败摘要保留的长度上限。
	maxFeedbackSummaryInPrompt = 800
	// maxFeedbackOutputInPrompt 是单条检查输出摘要保留的长度上限。
	maxFeedbackOutputInPrompt = 600
)

// stepWorkflowPrompt 构造闭环模式下的开发提示词。
//
// 与单步骤执行的提示词相比，它额外引用上一轮验证的失败反馈：失败检查、退出码、预期
// 条件与验证记录引用。反馈只用于说明"上次需要修复什么"，不会被当作当前验证结论，
// 也不会让新的执行跳过验证。
func stepWorkflowPrompt(
	task domain.Task,
	step domain.Step,
	additional string,
	feedback *domain.ValidationFailureFeedback,
) (string, error) {
	base, err := stepExecutionPrompt(task, step, additional)
	if err != nil {
		return "", err
	}
	if feedback == nil || len(feedback.Checks) == 0 {
		return base, nil
	}
	var builder strings.Builder
	builder.WriteString(base)
	builder.WriteString("\n## 上一次验证未通过的问题（仅供修复参考）\n")
	builder.WriteString("以下内容来自上一轮真实验证，只说明需要修复什么；它不是本轮验证结论，" +
		"本轮仍然必须重新执行全部检查。\n")
	builder.WriteString("验证记录: " + feedback.ValidationRunID.String() + "\n")
	if summary := clipFeedbackPromptText(feedback.Summary, maxFeedbackSummaryInPrompt); summary != "" {
		builder.WriteString("结论摘要: " + summary + "\n")
	}
	for index, check := range feedback.Checks {
		if index >= maxFeedbackChecksInPrompt {
			builder.WriteString("- 其余失败检查已省略\n")
			break
		}
		builder.WriteString(describeFailedCheck(check))
	}
	prompt := builder.String()
	if len(prompt) > maxStepExecutionPrompt {
		return "", fmt.Errorf(
			"%w: 提示词长度超过上限 %d",
			ErrStepExecutionInvalidInput,
			maxStepExecutionPrompt,
		)
	}
	return prompt, nil
}

// describeFailedCheck 生成单条失败检查的提示词条目。
func describeFailedCheck(check domain.ValidationCheckFeedback) string {
	required := "必需"
	if !check.Required {
		required = "可选"
	}
	var builder strings.Builder
	builder.WriteString("- [" + required + "] " + check.CheckName + "（状态 " + string(check.Status))
	if check.ExitCode >= 0 {
		builder.WriteString("，退出码 " + itoaForPrompt(check.ExitCode))
	}
	builder.WriteString("）\n")
	if reason := clipFeedbackPromptText(check.FailureReason, maxFeedbackSummaryInPrompt); reason != "" {
		builder.WriteString("  原因: " + reason + "\n")
	}
	if detail := clipFeedbackPromptText(check.ErrorDetail, maxFeedbackSummaryInPrompt); detail != "" {
		builder.WriteString("  错误: " + detail + "\n")
	}
	if stderr := clipFeedbackPromptText(check.StderrSummary, maxFeedbackOutputInPrompt); stderr != "" {
		builder.WriteString("  标准错误摘要: " + oneLine(stderr) + "\n")
	}
	if stdout := clipFeedbackPromptText(check.StdoutSummary, maxFeedbackOutputInPrompt); stdout != "" {
		builder.WriteString("  标准输出摘要: " + oneLine(stdout) + "\n")
	}
	if check.InfrastructureError {
		builder.WriteString("  说明: 上一轮该检查未能真实执行，本轮仍需重新执行\n")
	}
	return builder.String()
}

// clipFeedbackPromptText 按长度裁剪反馈文本。
//
// 数据库中的反馈已经脱敏，这里只做长度裁剪并保持单行可读性。
func clipFeedbackPromptText(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	truncated := trimmed[:limit]
	for len(truncated) > 0 && !isUTF8StartBoundary(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return strings.TrimRight(truncated, " \t\r\n") + "…（已截断）"
}

// isUTF8StartBoundary 判断字节串是否以合法 UTF-8 字符起始位置结尾。
func isUTF8StartBoundary(value string) bool {
	if value == "" {
		return true
	}
	last := value[len(value)-1]
	return last&0xC0 != 0x80
}

// oneLine 把多行文本压缩为单行，避免提示词被大段日志淹没。
func oneLine(value string) string {
	replaced := strings.NewReplacer("\r\n", " ⏎ ", "\n", " ⏎ ", "\r", " ⏎ ").Replace(value)
	return strings.TrimSpace(replaced)
}

// itoaForPrompt 把整数转换为十进制字符串。
func itoaForPrompt(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buffer := make([]byte, 0, 8)
	for value > 0 {
		buffer = append([]byte{byte('0' + value%10)}, buffer...)
		value /= 10
	}
	if negative {
		return "-" + string(buffer)
	}
	return string(buffer)
}
