package domain

import (
	"fmt"
	"strings"
)

// 方案确认阶段的边界常量。
//
// 确认只做"把已通过结构化校验的方案转换成正式实体"这一步，因此这里的上限只约束
// 生成时的确定性事实（需求标题与描述的组成方式），不新增第二套方案校验规则。
const (
	// MaxConfirmedRequirementTitleLength 是确认生成的需求标题长度上限。
	MaxConfirmedRequirementTitleLength = MaxRequirementAnalysisSummaryLength
)

// PlannedTask 是确认生成时的一个正式任务草案。
//
// Sequence 是任务在需求内的执行顺序，从 1 开始连续递增。方案中的任务数组顺序就是权威
// 顺序（M3 的结构化契约没有独立的 sequence 字段），因此顺序由数组索引推导，不需要为
// 一个可确定推导的值改造已经落库的 Revision JSON 契约。
type PlannedTask struct {
	// Sequence 是任务在需求内的顺序，等于任务在方案数组中的位置。
	Sequence int
	// Draft 是方案中的原始任务草案，包含标题、描述与验收条件。
	Draft ImplementationPlanTask
}

// ConfirmableTasks 校验方案并通过数组顺序推导正式任务顺序。
//
// 校验规则完全复用 ImplementationPlan.Validate：至少一个任务、任务标识唯一、标题与
// 描述非空、验收条件非空、依赖指向更早的任务（因此不存在自依赖与循环依赖）。本方法
// 只在其之上补充"顺序连续且唯一"这一条推导规则：顺序等于数组索引 + 1。
//
// 该函数是纯函数：不读数据库、不创建任何记录。
func (p ImplementationPlan) ConfirmableTasks() ([]PlannedTask, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	tasks := make([]PlannedTask, 0, len(p.Tasks))
	for index, draft := range p.Tasks {
		tasks = append(tasks, PlannedTask{Sequence: index + 1, Draft: draft})
	}
	return tasks, nil
}

// RequirementTitleFromAnalysis 返回确认生成需求的标题。
//
// 标题固定取最终需求分析的摘要：它是这一版分析对"这次要做什么"的一句话结论，已经通过
// 结构化校验，不接受模型生成的任意非结构化文本覆盖。
func RequirementTitleFromAnalysis(analysis RequirementAnalysis) (string, error) {
	title := strings.TrimSpace(analysis.Summary)
	if title == "" {
		return "", invalidField("Requirement", "title", "需求分析摘要不能为空")
	}
	if len(title) > MaxConfirmedRequirementTitleLength {
		return "", invalidField(
			"Requirement",
			"title",
			fmt.Sprintf("标题长度 %d 超过上限 %d", len(title), MaxConfirmedRequirementTitleLength),
		)
	}
	return title, nil
}

// RequirementDescriptionFromAnalysis 返回确认生成需求的描述。
//
// 描述是确定性摘要：摘要本身，加上目标、范围、约束与验收标准四个结构化列表，逐条列出
// 并保持原始顺序。它不引入任何模型生成的新文本，因此同一份分析快照永远得到同一段描述。
func RequirementDescriptionFromAnalysis(analysis RequirementAnalysis) (string, error) {
	title, err := RequirementTitleFromAnalysis(analysis)
	if err != nil {
		return "", err
	}
	sections := []struct {
		title string
		items []string
	}{
		{title: "目标", items: analysis.Goals},
		{title: "范围", items: analysis.Scope},
		{title: "约束", items: analysis.Constraints},
		{title: "验收标准", items: analysis.AcceptanceCriteria},
	}
	var builder strings.Builder
	builder.WriteString(title)
	for _, section := range sections {
		builder.WriteString("\n\n")
		builder.WriteString(section.title)
		builder.WriteString("：")
		for _, item := range section.items {
			builder.WriteString("\n- ")
			builder.WriteString(strings.TrimSpace(item))
		}
	}
	description := builder.String()
	if len(strings.TrimSpace(description)) == 0 {
		return "", invalidField("Requirement", "description", "需求描述不能为空")
	}
	return description, nil
}
