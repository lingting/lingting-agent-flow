package app

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// 两阶段分析提示词的长度上限。
//
// 提示词只包含与本次分析相关的上下文：不把完整日志、历史会话或凭据塞进模型请求。多轮
// 累积的补充消息按"保留最近轮次"裁剪，裁剪事实写进提示词本身，不静默丢弃。
const (
	// maxPlanningPromptMessage 是单条历史消息保留的长度上限。
	maxPlanningPromptMessage = 2000
	// maxPlanningPromptMessages 是提示词中保留的历史消息条数上限。
	maxPlanningPromptMessages = 20
	// maxPlanningPromptBytes 是单个提示词的长度上限。
	maxPlanningPromptBytes = 24 << 10
	// planningStageFailureLength 是阶段失败说明保留的字节上限。
	planningStageFailureLength = 1024
)

// analystPrompt 构造需求分析师的提示词。
//
// 它给出只读的项目上下文、本次会话的用户需求与补充说明，以及严格的输出格式。分析师
// 不能修改项目内容，也不能通过自然语言旁路结构化输出：真正的契约是解析后的
// domain.RequirementAnalysis。
func analystPrompt(
	project domain.Project,
	sourceCommit string,
	messages []domain.PlanningMessage,
) (string, error) {
	var builder strings.Builder
	builder.WriteString("你是需求分析师。请只输出一个 JSON 对象，不要输出多余解释或代码块围栏。\n\n")
	writePlanningProjectContext(&builder, project, sourceCommit)
	builder.WriteString("\n## 用户需求与补充说明\n")
	writePlanningRequirements(&builder, messages)
	builder.WriteString("\n## 输出格式\n")
	builder.WriteString(`{"summary":"一句话需求摘要","goals":["要达成的目标"],`)
	builder.WriteString(`"current_state":["项目现状，包含已有实现与限制"],"scope":["本次改动范围"],`)
	builder.WriteString(`"out_of_scope":["明确不做的内容"],"constraints":["必须遵守的约束"],`)
	builder.WriteString(`"risks":["风险与未知项"],"acceptance_criteria":["可检查的验收标准"]}`)
	builder.WriteString("\n\n约束：\n")
	builder.WriteString("- 八个字段全部必填，每个列表至少一条；不得省略非目标与风险\n")
	builder.WriteString("- 只读取项目内容，不修改任何文件，也不执行写入类命令\n")
	builder.WriteString("- 现状描述只依据项目内可见的事实，无法确认的内容写入 risks\n")
	builder.WriteString("- 不要输出与需求无关的泛化建议，也不要承诺尚未确认的改动\n")
	return checkPlanningPrompt(builder.String(), PlanningAnalysisStageRequirementAnalysis)
}

// plannerPrompt 构造方案规划师的提示词。
//
// 提示词包含完整的需求分析快照，而不是摘要：约束、非目标与验收标准必须原样进入规划
// 师的上下文，规划师不得通过省略它们来简化任务拆分。
func plannerPrompt(
	project domain.Project,
	sourceCommit string,
	analysisSnapshot []byte,
	messages []domain.PlanningMessage,
) (string, error) {
	var builder strings.Builder
	builder.WriteString("你是方案规划师。请只输出一个 JSON 对象，不要输出多余解释或代码块围栏。\n\n")
	writePlanningProjectContext(&builder, project, sourceCommit)
	builder.WriteString("\n## 需求分析结果（由需求分析师产出，不得改写）\n")
	builder.WriteString(strings.TrimSpace(string(analysisSnapshot)))
	builder.WriteString("\n")
	builder.WriteString("\n## 用户需求与补充说明\n")
	writePlanningRequirements(&builder, messages)
	builder.WriteString("\n## 输出格式\n")
	builder.WriteString(`{"summary":"一句话方案摘要","tasks":[{"id":"task-1","title":"任务标题",`)
	builder.WriteString(`"description":"任务的具体内容","depends_on":["前置任务 id"],`)
	builder.WriteString(`"acceptance_criteria":["该任务的验收条件"]}]}`)
	builder.WriteString("\n\n约束：\n")
	builder.WriteString("- tasks 至少一个；每个任务的 id 唯一，title、description 与 acceptance_criteria 均不得为空\n")
	builder.WriteString("- depends_on 只能引用排在它前面的任务 id，表示真实的前置关系；无前置时给空数组\n")
	builder.WriteString("- 需求分析中的 constraints 与 acceptance_criteria 必须体现在任务里，不得覆盖或静默丢弃\n")
	builder.WriteString("- 需求分析中 out_of_scope 的内容不得拆成任务\n")
	builder.WriteString("- 只规划、不执行：只读取项目内容，不修改任何文件，不创建正式任务\n")
	return checkPlanningPrompt(builder.String(), PlanningAnalysisStageImplementationPlan)
}

// writePlanningProjectContext 写入只读的项目上下文。
//
// 这些字段都是数据库中的项目事实：工作目录与固定 commit 决定模型读的是哪一份代码，
// 因此必须显式给出，而不是让模型自行猜测。
func writePlanningProjectContext(
	builder *strings.Builder,
	project domain.Project,
	sourceCommit string,
) {
	builder.WriteString("## 项目只读上下文\n")
	builder.WriteString("- 项目标识: " + project.ID + "\n")
	builder.WriteString("- 项目名称: " + project.Name + "\n")
	builder.WriteString("- 仓库地址: " + project.RepositoryURL + "\n")
	builder.WriteString("- 基础分支: " + project.BaseBranch + "\n")
	builder.WriteString("- 代码目录（容器内只读）: " + project.EffectiveWorkDir() + "\n")
	if commit := strings.TrimSpace(sourceCommit); commit != "" {
		builder.WriteString("- 固定代码快照: " + commit + "\n")
	}
}

// writePlanningRequirements 写入用户需求与补充说明。
//
// 只保留用户消息：角色输出属于历史结论，重跑时重新产生，不需要再次喂给模型。条数与单条
// 长度都受上限约束，裁剪时显式写出省略了多少条，避免"看起来完整其实缺了一部分"。
func writePlanningRequirements(builder *strings.Builder, messages []domain.PlanningMessage) {
	requirements := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.SenderType != domain.PlanningSenderUser {
			continue
		}
		if trimmed := strings.TrimSpace(message.Content); trimmed != "" {
			requirements = append(requirements, trimmed)
		}
	}
	if len(requirements) == 0 {
		builder.WriteString("（本次会话没有用户需求消息）\n")
		return
	}
	omitted := 0
	if len(requirements) > maxPlanningPromptMessages {
		omitted = len(requirements) - maxPlanningPromptMessages
		requirements = requirements[len(requirements)-maxPlanningPromptMessages:]
	}
	if omitted > 0 {
		builder.WriteString(fmt.Sprintf("（已省略更早的 %d 条需求消息）\n", omitted))
	}
	for index, requirement := range requirements {
		builder.WriteString(fmt.Sprintf("%d. %s\n", index+1+omitted,
			clipPromptText(requirement, maxPlanningPromptMessage)))
	}
	if omitted > 0 {
		builder.WriteString("（保留的是最近的补充说明，按时间顺序递增）\n")
	}
}

// checkPlanningPrompt 校验提示词长度。
//
// 超限直接失败而不是截断：截断提示词会让模型在缺失上下文的条件下产出结论，这比明确
// 失败更难排查。
func checkPlanningPrompt(prompt string, stage PlanningAnalysisStage) (string, error) {
	if len(prompt) > maxPlanningPromptBytes {
		return "", fmt.Errorf(
			"%w: %s提示词长度 %d 超过上限 %d",
			ErrPlanningAnalysisInvalidInput,
			stage.Label(),
			len(prompt),
			maxPlanningPromptBytes,
		)
	}
	return prompt, nil
}
