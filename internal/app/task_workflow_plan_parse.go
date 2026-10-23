package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// planOutput 是 Planner 输出的结构化计划。
//
// 它是 Planner 与编排器之间的唯一契约：字段名与提示词中声明的输出格式严格一致，
// 未知字段一律拒绝，避免把自由文本或额外结构当成可执行计划。
type planOutput struct {
	Summary string           `json:"summary"`
	Steps   []planStepOutput `json:"steps"`
}

// planStepOutput 是 Planner 输出的单个计划步骤。
type planStepOutput struct {
	ID                   string              `json:"id"`
	Title                string              `json:"title"`
	Objective            string              `json:"objective"`
	Instructions         string              `json:"instructions"`
	AcceptanceCriteria   []string            `json:"acceptance_criteria"`
	VerificationCommands []planCommandOutput `json:"verification_commands,omitempty"`
}

// planCommandOutput 是 Planner 输出的验证命令。
type planCommandOutput struct {
	Name             string   `json:"name"`
	Command          string   `json:"command"`
	Args             []string `json:"args,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds,omitempty"`
	Optional         bool     `json:"optional,omitempty"`
	AllowedExitCodes []int    `json:"allowed_exit_codes,omitempty"`
}

// ParsePlanOutput 从 Agent 最终文本中解析计划草稿。
//
// 解析只接受可被 JSON 解码为计划结构的内容：允许前后存在少量说明文字与代码块围栏，
// 但必须能定位到一个完整的 JSON 对象；否则返回 ErrPlanInvalidOutput，绝不把自由
// 文本当成计划。结构合法性之外的业务校验由 domain.BuildPlan 完成。
func ParsePlanOutput(text string) (domain.PlanDraft, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return domain.PlanDraft{}, fmt.Errorf("%w: Planner 没有输出任何内容", ErrPlanInvalidOutput)
	}
	candidate, err := extractPlanJSONObject(trimmed)
	if err != nil {
		return domain.PlanDraft{}, err
	}
	var output planOutput
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return domain.PlanDraft{}, fmt.Errorf(
			"%w: 计划不是合法的结构化输出: %w",
			ErrPlanInvalidOutput,
			err,
		)
	}
	return output.toDraft(), nil
}

// toDraft 把解析结果转换为领域层计划草稿。
//
// 转换只做结构映射，不做业务判断：长度、数量、验收条件与命令合法性都由
// domain.BuildPlan 统一校验，避免两处规则各自演化。
func (o planOutput) toDraft() domain.PlanDraft {
	steps := make([]domain.PlanStepDraft, 0, len(o.Steps))
	for _, item := range o.Steps {
		steps = append(steps, domain.PlanStepDraft{
			Identifier:           item.ID,
			Title:                item.Title,
			Objective:            item.Objective,
			Instructions:         item.Instructions,
			AcceptanceCriteria:   item.AcceptanceCriteria,
			VerificationCommands: planCommands(item.VerificationCommands),
		})
	}
	return domain.PlanDraft{Summary: o.Summary, Steps: steps}
}

// planCommands 把计划中的命令转换为领域命令定义。
//
// 必需性只由 optional 决定：未声明 optional 的命令默认必需，因此 Planner 不能
// 通过省略字段把必需检查变成可忽略的检查。
func planCommands(items []planCommandOutput) []domain.VerificationCommand {
	commands := make([]domain.VerificationCommand, 0, len(items))
	for _, item := range items {
		commands = append(commands, domain.VerificationCommand{
			Name:             strings.TrimSpace(item.Name),
			Command:          strings.TrimSpace(item.Command),
			Args:             item.Args,
			Optional:         item.Optional,
			Required:         !item.Optional,
			TimeoutSeconds:   item.TimeoutSeconds,
			AllowedExitCodes: item.AllowedExitCodes,
		})
	}
	return commands
}

// extractPlanJSONObject 从文本中提取第一个完整的 JSON 对象。
//
// 括号配平扫描只在领域层实现一次（domain.ExtractJSONObject）：这里只把它的
// 通用错误包装为本服务的输出错误类别，避免两处扫描规则各自演化。
func extractPlanJSONObject(text string) (string, error) {
	candidate, err := domain.ExtractJSONObject(text)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPlanInvalidOutput, err)
	}
	return candidate, nil
}
