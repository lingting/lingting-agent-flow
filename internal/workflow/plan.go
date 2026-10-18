package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// PlanStepOutput 是 Planner 输出的单个计划步骤。
//
// 该结构是 Planner 与工作流引擎之间的唯一契约：自然语言输出不会被直接当成
// 可执行计划，必须能够解析为该结构并通过校验。
type PlanStepOutput struct {
	// ID 是计划内的步骤标识，必须唯一。
	ID string `json:"id"`
	// Title 是步骤标题。
	Title string `json:"title"`
	// Objective 是步骤目标。
	Objective string `json:"objective"`
	// Instructions 是交给 Developer 的具体要求。
	Instructions string `json:"instructions"`
	// AcceptanceCriteria 是该步骤的验收条件，必须非空。
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	// VerificationCommands 是该步骤额外的验证命令，可以为空。
	VerificationCommands []PlanCommandOutput `json:"verification_commands,omitempty"`
}

// PlanCommandOutput 是 Planner 输出的验证命令。
type PlanCommandOutput struct {
	Name             string   `json:"name"`
	Command          string   `json:"command"`
	Args             []string `json:"args,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds,omitempty"`
	Optional         bool     `json:"optional,omitempty"`
	AllowedExitCodes []int    `json:"allowed_exit_codes,omitempty"`
}

// PlanOutput 是 Planner 输出的完整计划。
type PlanOutput struct {
	Summary string           `json:"summary"`
	Steps   []PlanStepOutput `json:"steps"`
}

// 计划校验的边界常量。
const (
	// MaxPlanStepTitleLength 是计划步骤标题长度上限。
	MaxPlanStepTitleLength = 200
	// MaxPlanStepObjectiveLength 是计划步骤目标长度上限。
	MaxPlanStepObjectiveLength = 2000
	// MaxPlanStepInstructionsLength 是计划步骤要求长度上限。
	MaxPlanStepInstructionsLength = 8000
)

// ParsePlanOutput 从 Agent 最终文本中解析计划输出。
//
// 解析只接受可被 JSON 解码为 PlanOutput 的内容：允许前后存在少量说明文字，
// 但必须能定位到一个完整的 JSON 对象；否则返回错误，绝不把自由文本当成计划。
func ParsePlanOutput(text string) (PlanOutput, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return PlanOutput{}, fmt.Errorf("规划失败: Planner 没有输出任何内容")
	}
	candidate, err := extractJSONObject(trimmed)
	if err != nil {
		return PlanOutput{}, err
	}
	var output PlanOutput
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return PlanOutput{}, fmt.Errorf("规划失败: 计划不是合法的结构化输出: %w", err)
	}
	return output, nil
}

// extractJSONObject 从文本中提取第一个完整的 JSON 对象。
//
// 只做括号配平扫描，不做语义解释：这样既能容忍代码块围栏，也不会把
// 任意自由文本当作 JSON 接受。
func extractJSONObject(text string) (string, error) {
	start := strings.Index(text, "{")
	if start < 0 {
		return "", fmt.Errorf("规划失败: 输出中找不到 JSON 对象")
	}
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(text); index++ {
		char := text[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : index+1], nil
			}
		}
	}
	return "", fmt.Errorf("规划失败: JSON 对象未闭合")
}

// BuildPlanSteps 把校验过的计划输出转换为领域步骤。
//
// 任务级必需验证命令会被合并进每个开发步骤：Planner 不能通过省略命令来取消
// 任务级的必需检查。
func BuildPlanSteps(
	task domain.Task,
	output PlanOutput,
	role domain.Role,
	taskSpec domain.VerificationSpec,
	at time.Time,
) ([]domain.Step, error) {
	if err := validatePlanOutput(task, output); err != nil {
		return nil, err
	}
	steps := make([]domain.Step, 0, len(output.Steps))
	for index, item := range output.Steps {
		stepSpec := domain.VerificationSpec{
			AcceptanceCriteria: item.AcceptanceCriteria,
			Commands:           planCommands(item.VerificationCommands),
		}
		merged := taskSpec.Merge(stepSpec)
		if err := merged.Validate(); err != nil {
			return nil, fmt.Errorf("规划失败: 步骤 %d 的验证规格非法: %w", index+1, err)
		}
		step, err := domain.NewAgentStep(
			task.ID,
			index+1,
			domain.StepTypeDevelop,
			item.Title,
			item.Objective,
			item.Instructions,
			role,
			merged,
			task.Config.Retry,
			at,
		)
		if err != nil {
			return nil, fmt.Errorf("规划失败: 步骤 %d 非法: %w", index+1, err)
		}
		steps = append(steps, step)
	}
	plan, err := domain.NewPlan(task.ID, output.Summary, steps)
	if err != nil {
		return nil, fmt.Errorf("规划失败: %w", err)
	}
	return plan.Steps, nil
}

// planCommands 把计划中的命令转换为领域命令定义。
func planCommands(items []PlanCommandOutput) []domain.VerificationCommand {
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

// validatePlanOutput 校验计划输出的结构与内容边界。
func validatePlanOutput(task domain.Task, output PlanOutput) error {
	limit := task.Config.PlanStepLimit()
	if len(output.Steps) == 0 {
		return fmt.Errorf("规划失败: 计划至少需要一个步骤")
	}
	if len(output.Steps) > limit {
		return fmt.Errorf("规划失败: 计划步骤数 %d 超过上限 %d", len(output.Steps), limit)
	}
	seen := make(map[string]struct{}, len(output.Steps))
	for index, item := range output.Steps {
		position := index + 1
		identifier := strings.TrimSpace(item.ID)
		if identifier == "" {
			return fmt.Errorf("规划失败: 第 %d 个步骤缺少标识", position)
		}
		if len(identifier) > 128 {
			return fmt.Errorf("规划失败: 第 %d 个步骤的标识过长", position)
		}
		if _, duplicated := seen[identifier]; duplicated {
			return fmt.Errorf("规划失败: 步骤标识重复 %q", identifier)
		}
		seen[identifier] = struct{}{}
		if title := strings.TrimSpace(item.Title); title == "" {
			return fmt.Errorf("规划失败: 第 %d 个步骤缺少标题", position)
		} else if len(title) > MaxPlanStepTitleLength {
			return fmt.Errorf("规划失败: 第 %d 个步骤的标题过长", position)
		}
		if objective := strings.TrimSpace(item.Objective); objective == "" {
			return fmt.Errorf("规划失败: 第 %d 个步骤缺少目标", position)
		} else if len(objective) > MaxPlanStepObjectiveLength {
			return fmt.Errorf("规划失败: 第 %d 个步骤的目标过长", position)
		}
		if len(item.Instructions) > MaxPlanStepInstructionsLength {
			return fmt.Errorf("规划失败: 第 %d 个步骤的要求过长", position)
		}
		if len(item.AcceptanceCriteria) == 0 {
			return fmt.Errorf("规划失败: 第 %d 个步骤缺少验收条件", position)
		}
		for _, criterion := range item.AcceptanceCriteria {
			if strings.TrimSpace(criterion) == "" {
				return fmt.Errorf("规划失败: 第 %d 个步骤包含空的验收条件", position)
			}
		}
	}
	return nil
}

// ParseAssessment 从 Agent 最终文本中解析辅助评估结果。
//
// 与计划解析一致：只接受可被 JSON 解码为 AgentAssessment 的内容，未知字段
// 一律拒绝，避免把自由文本当成结构化结论。
func ParseAssessment(text string) (domain.AgentAssessment, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return domain.AgentAssessment{}, fmt.Errorf("评估失败: 没有输出任何内容")
	}
	candidate, err := extractJSONObject(trimmed)
	if err != nil {
		return domain.AgentAssessment{}, fmt.Errorf("评估失败: %w", err)
	}
	var assessment domain.AgentAssessment
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&assessment); err != nil {
		return domain.AgentAssessment{}, fmt.Errorf("评估失败: 输出不是合法的结构化结果: %w", err)
	}
	if err := assessment.Validate(); err != nil {
		return domain.AgentAssessment{}, fmt.Errorf("评估失败: %w", err)
	}
	return assessment, nil
}
