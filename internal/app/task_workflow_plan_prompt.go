package app

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// 规划提示词中各类上下文的长度上限。
//
// 提示词只包含与规划相关的上下文：不把完整日志、历史会话或凭据塞进模型请求。
const (
	// maxPlanGoal 是任务目标在提示词中保留的长度上限。
	maxPlanGoal = 4000
	// maxPlanCriteria 是任务级验收条件保留的条数上限。
	maxPlanCriteria = 20
	// maxPlanCommandLine 是单条验证命令说明保留的长度上限。
	maxPlanCommandLine = 512
	// maxPlanAdditional 是调用方补充说明保留的长度上限。
	maxPlanAdditional = 8000
)

// planPrompt 构造 Planner 的提示词。
//
// 提示词明确要求结构化输出，并说明输出必须满足的字段约束：Planner 的自由文本
// 不会被直接当成可执行计划。additional 只能追加在最后，不能替换任务目标、
// 执行环境约束与输出格式要求。
func planPrompt(task domain.Task, additional string) (string, error) {
	var builder strings.Builder
	builder.WriteString("你是本次任务的规划角色。请只输出一个 JSON 对象，不要输出多余解释。\n\n")
	builder.WriteString("## 任务目标\n")
	builder.WriteString(clipPromptText(task.Config.Goal, maxPlanGoal))
	builder.WriteString("\n\n## 执行环境约束\n")
	builder.WriteString("- 代码仓库位于容器内目录: " + task.Config.EffectiveWorkDir() + "\n")
	builder.WriteString("- 任务分支: agent/" + task.ID.String() + "\n")
	builder.WriteString("- 基础分支: " + task.BaseRef + "\n")
	builder.WriteString(fmt.Sprintf("- 计划步骤数上限: %d\n", task.Config.PlanStepLimit()))
	builder.WriteString("- 不要修改必需验证命令的配置，也不要假设命令已经通过\n")
	builder.WriteString("- 每个步骤都会由开发者按顺序执行，并执行合并后的必需检查\n")
	if spec := task.Config.Verification; spec.HasCommands() {
		builder.WriteString("\n## 任务级必需验证命令（每个步骤都会执行，不可省略）\n")
		for _, command := range spec.Commands {
			builder.WriteString("- " + describeVerificationCommand(command) + "\n")
		}
	}
	if criteria := task.Config.Verification.AcceptanceCriteria; len(criteria) > 0 {
		builder.WriteString("\n## 任务级验收条件\n")
		for _, item := range clipPromptList(criteria, maxPlanCriteria) {
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
	if additional = strings.TrimSpace(additional); additional != "" {
		builder.WriteString("\n## 调用方补充说明\n")
		builder.WriteString(clipPromptText(additional, maxPlanAdditional))
		builder.WriteString("\n")
	}
	prompt := builder.String()
	if len(prompt) > maxStepExecutionPrompt {
		return "", fmt.Errorf(
			"%w: 提示词长度超过上限 %d",
			ErrWorkflowInvalidInput,
			maxStepExecutionPrompt,
		)
	}
	return prompt, nil
}
