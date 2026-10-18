package workflow

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// promptLimits 约束提示词中各类上下文的长度。
//
// 提示词只包含与当前阶段相关的上下文，不把完整日志或整份 Session 塞进模型请求。
const (
	// maxGoalInPrompt 是任务目标在提示词中保留的长度上限。
	maxGoalInPrompt = 4000
	// maxStepObjectiveInPrompt 是步骤目标保留的长度上限。
	maxStepObjectiveInPrompt = 2000
	// maxInstructionsInPrompt 是步骤要求保留的长度上限。
	maxInstructionsInPrompt = 8000
	// maxCriteriaInPrompt 是验收条件保留的条数上限。
	maxCriteriaInPrompt = 20
	// maxEvidenceInPrompt 是失败证据保留的条数上限。
	maxEvidenceInPrompt = 10
	// maxEvidenceLineInPrompt 是单条证据保留的字符数上限。
	maxEvidenceLineInPrompt = 400
)

// planPrompt 构造 Planner 的提示词。
//
// 提示词明确要求结构化输出，并说明输出必须满足的字段约束：Planner 的自由文本
// 不会被直接当成可执行计划。
func planPrompt(task domain.Task) string {
	var builder strings.Builder
	builder.WriteString("你是本次任务的规划角色。请只输出一个 JSON 对象，不要输出多余解释。\n\n")
	builder.WriteString("## 任务目标\n")
	builder.WriteString(clip(task.Config.Goal, maxGoalInPrompt))
	builder.WriteString("\n\n## 执行环境约束\n")
	builder.WriteString("- 代码仓库已克隆到容器内目录: " + task.Config.EffectiveWorkDir() + "\n")
	builder.WriteString("- 任务分支: agent/" + task.ID.String() + "\n")
	builder.WriteString("- 基础分支: " + task.BaseRef + "\n")
	builder.WriteString(fmt.Sprintf("- 计划步骤数上限: %d\n", task.Config.PlanStepLimit()))
	builder.WriteString("- 不要修改必需验证命令的配置，也不要假设命令已经通过\n")
	if spec := task.Config.Verification; spec.HasCommands() {
		builder.WriteString("\n## 任务级必需验证命令（每个步骤都会执行，不可省略）\n")
		for _, command := range spec.Commands {
			builder.WriteString("- " + command.Name + ": " + describeCommand(command) + "\n")
		}
	}
	if criteria := task.Config.Verification.AcceptanceCriteria; len(criteria) > 0 {
		builder.WriteString("\n## 任务级验收条件\n")
		for _, item := range clipList(criteria, maxCriteriaInPrompt) {
			builder.WriteString("- " + item + "\n")
		}
	}
	builder.WriteString("\n## 输出格式\n")
	builder.WriteString(`{"summary":"一句话计划摘要","steps":[{"id":"step-1","title":"步骤标题",`)
	builder.WriteString(`"objective":"步骤目标","instructions":"给开发者的具体要求",`)
	builder.WriteString(`"acceptance_criteria":["可检查的验收条件"],`)
	builder.WriteString(`"verification_commands":[{"name":"build","command":"go","args":["build","./..."]}]}]}`)
	builder.WriteString("\n\n约束：\n")
	builder.WriteString("- 每个步骤必须有唯一的 id、非空 title、objective 与至少一条 acceptance_criteria\n")
	builder.WriteString("- verification_commands 只能使用容器内已安装的命令，参数必须逐项给出\n")
	builder.WriteString("- 计划必须覆盖任务目标，不允许出现重复步骤\n")
	return builder.String()
}

// developPrompt 构造 Developer 的提示词。
//
// evidence 是上一次验证失败的证据摘要，用于修复型重试；首次执行时为空。
func developPrompt(task domain.Task, step domain.Step, evidence []string) string {
	var builder strings.Builder
	builder.WriteString("你是本次任务的开发角色。请在容器内的 Git 工作区直接完成代码修改。\n\n")
	builder.WriteString("## 任务目标\n")
	builder.WriteString(clip(task.Config.Goal, maxGoalInPrompt))
	builder.WriteString("\n\n## 当前步骤\n")
	builder.WriteString("标题: " + step.Title + "\n")
	builder.WriteString("目标: " + clip(step.Objective, maxStepObjectiveInPrompt) + "\n")
	if instructions := strings.TrimSpace(step.Instructions); instructions != "" {
		builder.WriteString("要求:\n" + clip(instructions, maxInstructionsInPrompt) + "\n")
	}
	if criteria := step.VerificationSpec.AcceptanceCriteria; len(criteria) > 0 {
		builder.WriteString("\n## 验收条件\n")
		for _, item := range clipList(criteria, maxCriteriaInPrompt) {
			builder.WriteString("- " + item + "\n")
		}
	}
	if step.VerificationSpec.HasCommands() {
		builder.WriteString("\n## 完成后会执行的必需检查命令\n")
		builder.WriteString("这些命令由编排器执行，你无法跳过；请在提交前自行确认它们能够通过。\n")
		for _, command := range step.VerificationSpec.Commands {
			required := "必需"
			if !command.IsRequired() {
				required = "可选"
			}
			builder.WriteString("- [" + required + "] " + command.Name + ": " + describeCommand(command) + "\n")
		}
	}
	if len(evidence) > 0 {
		builder.WriteString("\n## 上一次验证失败证据\n")
		builder.WriteString("请针对以下问题修复，不要只解释原因：\n")
		for _, item := range clipList(evidence, maxEvidenceInPrompt) {
			builder.WriteString("- " + clip(item, maxEvidenceLineInPrompt) + "\n")
		}
	}
	builder.WriteString("\n## 执行约束\n")
	builder.WriteString("- 只在当前工作区内修改代码，不要创建新的 Git 仓库或切换分支\n")
	builder.WriteString("- 不要执行 git push 或 git merge，代码保存由编排器负责\n")
	builder.WriteString("- 完成修改后用简短文字总结你做了什么，不要输出凭据或环境变量内容\n")
	return builder.String()
}

// reviewPrompt 构造 Validator 的提示词。
//
// 提示词包含确定性命令结果，但明确说明：Agent 的结论不能覆盖必需检查的失败。
func reviewPrompt(
	task domain.Task,
	step domain.Step,
	results []domain.CommandResult,
) string {
	var builder strings.Builder
	builder.WriteString("你是本次任务的验证角色。你的结论是辅助证据，不能覆盖必需检查的失败结果。\n\n")
	builder.WriteString("## 任务目标\n")
	builder.WriteString(clip(task.Config.Goal, maxGoalInPrompt))
	builder.WriteString("\n\n## 当前步骤\n")
	builder.WriteString("标题: " + step.Title + "\n")
	builder.WriteString("目标: " + clip(step.Objective, maxStepObjectiveInPrompt) + "\n")
	if criteria := step.VerificationSpec.AcceptanceCriteria; len(criteria) > 0 {
		builder.WriteString("\n## 验收条件\n")
		for _, item := range clipList(criteria, maxCriteriaInPrompt) {
			builder.WriteString("- " + item + "\n")
		}
	}
	builder.WriteString("\n## 已执行的确定性检查结果\n")
	if len(results) == 0 {
		builder.WriteString("（没有可用的检查结果）\n")
	}
	for _, result := range results {
		status := "通过"
		if !result.Passed() {
			status = "未通过"
		}
		builder.WriteString(fmt.Sprintf(
			"- [%s] %s（退出码 %d）\n",
			status,
			result.Name,
			result.ExitCode,
		))
		if summary := summarizeCommandOutput(result.Stderr, result.Stdout); summary != "" {
			builder.WriteString("  输出摘要: " + summary + "\n")
		}
	}
	builder.WriteString("\n## 输出格式\n")
	builder.WriteString(`{"passed":true,"summary":"评估摘要","findings":["发现的问题"],`)
	builder.WriteString(`"evidence":["文件路径或命令名称"],"suggest_fix":false,"needs_human_review":false}`)
	builder.WriteString("\n\n约束：\n")
	builder.WriteString("- 只输出一个 JSON 对象，不要输出多余解释\n")
	builder.WriteString("- 需要查看代码时请自行读取文件，不要假设修改已经存在\n")
	builder.WriteString("- 不要修改代码，不要执行提交、推送或合并\n")
	builder.WriteString("- 如果必需检查已经失败，请在 findings 中说明，不要声称全部通过\n")
	return builder.String()
}

// clip 按字符数上限裁剪文本，超出时追加省略标记。
func clip(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "…（已截断）"
}

// clipList 按条数与单条长度上限裁剪列表。
func clipList(values []string, limit int) []string {
	if len(values) > limit {
		values = values[:limit]
	}
	clipped := make([]string, 0, len(values))
	for _, value := range values {
		clipped = append(clipped, strings.TrimSpace(value))
	}
	return clipped
}
