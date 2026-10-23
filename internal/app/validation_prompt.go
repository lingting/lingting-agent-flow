package app

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// 语义审查提示词中各类上下文的长度上限。
//
// 提示词只包含与本次审查相关的上下文：不把完整日志、历史会话或凭据塞进模型请求。
const (
	// maxReviewGoal 是任务目标在审查提示词中保留的长度上限。
	maxReviewGoal = 4000
	// maxReviewStepObjective 是步骤目标保留的长度上限。
	maxReviewStepObjective = 2000
	// maxReviewCriteria 是验收条件保留的条数上限。
	maxReviewCriteria = 20
	// maxReviewCheckLines 是检查结果摘要保留的条数上限。
	maxReviewCheckLines = 30
	// maxReviewCheckLine 是单条检查结果摘要保留的长度上限。
	maxReviewCheckLine = 400
	// maxReviewAdditional 是调用方补充说明保留的长度上限。
	maxReviewAdditional = 8000
)

// validatorPrompt 构造语义审查提示词。
//
// 提示词只声明"要审查什么"与"如何输出"：确定性检查的命令与结果由编排器给出，
// Validator 不能修改它们，也不能通过自然语言宣告验证通过。真正的结论由
// domain.DecideValidation 依据结构化评估与检查事实计算。
func validatorPrompt(
	task domain.Task,
	step domain.Step,
	results []domain.ValidationCheckResult,
	additional string,
) (string, error) {
	var builder strings.Builder
	builder.WriteString("你是本次任务的验证角色。请只输出一个 JSON 对象，不要输出多余解释。\n\n")
	builder.WriteString("## 任务目标\n")
	builder.WriteString(clipPromptText(task.Config.Goal, maxReviewGoal))
	builder.WriteString("\n\n## 当前步骤\n")
	builder.WriteString("标题: " + step.Title + "\n")
	builder.WriteString("目标: " + clipPromptText(step.Objective, maxReviewStepObjective) + "\n")
	if instructions := strings.TrimSpace(step.Instructions); instructions != "" {
		builder.WriteString("要求:\n" + clipPromptText(instructions, maxExecuteInstructions) + "\n")
	}
	if criteria := step.VerificationSpec.AcceptanceCriteria; len(criteria) > 0 {
		builder.WriteString("\n## 验收条件\n")
		for _, item := range clipPromptList(criteria, maxReviewCriteria) {
			builder.WriteString("- " + item + "\n")
		}
	}
	builder.WriteString("\n## 确定性检查结果（由编排器执行，你无法修改或跳过）\n")
	lines := 0
	for _, result := range results {
		if lines >= maxReviewCheckLines {
			builder.WriteString("- 其余检查结果已省略\n")
			break
		}
		lines++
		builder.WriteString(describeReviewedCheck(result))
	}
	builder.WriteString("\n## 执行环境约束\n")
	builder.WriteString("- 代码仓库位于容器内目录: " + task.Config.EffectiveWorkDir() + "\n")
	builder.WriteString("- 只在当前工作区内读取代码，不要修改任何文件\n")
	builder.WriteString("- 不要执行 git push 或 git merge\n")
	builder.WriteString("- 不要输出凭据、令牌或环境变量内容\n")
	builder.WriteString("\n## 输出格式\n")
	builder.WriteString(`{"passed":true,"summary":"一句话结论","findings":["发现的问题"],`)
	builder.WriteString(`"evidence":["证据引用，例如文件路径或命令名"],`)
	builder.WriteString(`"suggest_fix":false,"needs_human_review":false}`)
	builder.WriteString("\n\n约束：\n")
	builder.WriteString("- passed 必须显式给出：true 表示需求确实被满足，false 表示未满足\n")
	builder.WriteString("- 只依据代码与确定性检查事实判断，不要假设未执行的检查已经通过\n")
	builder.WriteString("- 无法判断或证据不足时把 needs_human_review 设为 true\n")
	if additional = strings.TrimSpace(additional); additional != "" {
		builder.WriteString("\n## 调用方补充说明\n")
		builder.WriteString(clipPromptText(additional, maxReviewAdditional))
		builder.WriteString("\n")
	}
	prompt := builder.String()
	if len(prompt) > maxStepExecutionPrompt {
		return "", fmt.Errorf(
			"%w: 提示词长度超过上限 %d",
			ErrValidationInvalidInput,
			maxStepExecutionPrompt,
		)
	}
	return prompt, nil
}

// describeReviewedCheck 生成单条检查结果的可读说明。
//
// 输出摘要直接来自真实执行结果，因此必须沿用统一的脱敏与裁剪规则。
func describeReviewedCheck(result domain.ValidationCheckResult) string {
	required := "必需"
	if !result.Required {
		required = "可选"
	}
	var builder strings.Builder
	builder.WriteString("- [" + required + "] " + result.CheckName + "（状态 " + string(result.Status))
	if result.ExitCode >= 0 {
		builder.WriteString("，退出码 " + itoaForPrompt(result.ExitCode))
	}
	builder.WriteString("）\n")
	if reason := clipReviewText(result.FailureReason, maxReviewCheckLine); reason != "" {
		builder.WriteString("  原因: " + reason + "\n")
	}
	if stderr := clipReviewText(result.StderrSummary, maxReviewCheckLine); stderr != "" {
		builder.WriteString("  标准错误摘要: " + oneLine(stderr) + "\n")
	}
	if stdout := clipReviewText(result.StdoutSummary, maxReviewCheckLine); stdout != "" {
		builder.WriteString("  标准输出摘要: " + oneLine(stdout) + "\n")
	}
	return builder.String()
}

// clipReviewText 先脱敏再裁剪审查提示词文本。
//
// 顺序固定为"先脱敏再裁剪"：先裁剪可能把凭据截断成无法识别的片段。
func clipReviewText(value string, limit int) string {
	return domain.ClipFeedbackText(value, limit)
}
