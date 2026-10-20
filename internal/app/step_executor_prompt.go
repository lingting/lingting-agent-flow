package app

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// 提示词中各类上下文的长度上限。
//
// 提示词只包含与当前步骤相关的上下文：不把完整日志、历史会话或凭据塞进模型请求。
const (
	// maxExecuteGoalInPrompt 是任务目标在提示词中保留的长度上限。
	maxExecuteGoalInPrompt = 4000
	// maxExecuteStepObjective 是步骤目标保留的长度上限。
	maxExecuteStepObjective = 2000
	// maxExecuteInstructions 是步骤要求保留的长度上限。
	maxExecuteInstructions = 8000
	// maxExecuteCriteria 是验收条件保留的条数上限。
	maxExecuteCriteria = 20
	// maxExecuteCommandLine 是单条命令说明保留的长度上限。
	maxExecuteCommandLine = 512
)

// stepExecutionPrompt 构造单步骤执行使用的提示词。
//
// 提示词由编排器生成：调用方给出的补充说明只能追加在最后，不能替换任务目标、
// 当前步骤与验收条件，避免通过请求体绕过业务上下文。
func stepExecutionPrompt(
	task domain.Task,
	step domain.Step,
	additional string,
) (string, error) {
	var builder strings.Builder
	builder.WriteString("你是本次任务的 " + roleLabel(step.Type) + "。请直接在当前工作区完成本次步骤。\n\n")
	builder.WriteString("## 任务目标\n")
	builder.WriteString(clipPromptText(task.Config.Goal, maxExecuteGoalInPrompt))
	builder.WriteString("\n\n## 当前步骤\n")
	builder.WriteString("标题: " + step.Title + "\n")
	builder.WriteString("类型: " + string(step.Type) + "\n")
	builder.WriteString("目标: " + clipPromptText(step.Objective, maxExecuteStepObjective) + "\n")
	if instructions := strings.TrimSpace(step.Instructions); instructions != "" {
		builder.WriteString("要求:\n" + clipPromptText(instructions, maxExecuteInstructions) + "\n")
	}
	if criteria := step.VerificationSpec.AcceptanceCriteria; len(criteria) > 0 {
		builder.WriteString("\n## 验收条件\n")
		for _, item := range clipPromptList(criteria, maxExecuteCriteria) {
			builder.WriteString("- " + item + "\n")
		}
	}
	if task.Config.Verification.HasCommands() {
		builder.WriteString("\n## 任务级必需检查命令（由编排器执行，你无法跳过）\n")
		for _, command := range task.Config.Verification.Commands {
			builder.WriteString("- " + describeVerificationCommand(command) + "\n")
		}
	}
	builder.WriteString("\n## 执行约束\n")
	builder.WriteString("- 只在当前工作区内修改代码，不要创建新的 Git 仓库或切换分支\n")
	builder.WriteString("- 不要执行 git push 或 git merge，代码保存由编排器负责\n")
	builder.WriteString("- 不要输出凭据、令牌或环境变量内容\n")
	builder.WriteString("- 完成后用简短文字总结你做了什么，以及还有哪些未完成的事项\n")
	additional = strings.TrimSpace(additional)
	if additional != "" {
		builder.WriteString("\n## 调用方补充说明\n")
		builder.WriteString(clipPromptText(additional, maxExecuteInstructions))
		builder.WriteString("\n")
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

// roleLabel 返回步骤类型对应的角色说明。
func roleLabel(stepType domain.StepType) string {
	switch stepType.AgentRole() {
	case domain.RoleTypePlanner:
		return "规划角色"
	case domain.RoleTypeValidator:
		return "验证角色"
	default:
		return "开发角色"
	}
}

// describeVerificationCommand 生成一条验证命令的可读说明。
func describeVerificationCommand(command domain.VerificationCommand) string {
	parts := append([]string{command.Command}, command.Args...)
	line := command.Name + ": " + strings.Join(parts, " ")
	return clipPromptText(line, maxExecuteCommandLine)
}

// clipPromptText 按字符数上限裁剪文本，超出时追加省略标记。
func clipPromptText(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "…（已截断）"
}

// clipPromptList 按条数与单条长度上限裁剪列表。
func clipPromptList(values []string, limit int) []string {
	if limit > 0 && len(values) > limit {
		values = values[:limit]
	}
	clipped := make([]string, 0, len(values))
	for _, value := range values {
		clipped = append(clipped, strings.TrimSpace(value))
	}
	return clipped
}
