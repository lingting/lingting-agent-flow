package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RequirementAnalysis 是需求分析师输出的结构化需求分析。
// 所有列表均保留模型给出的顺序；解析时不对原文做静默修复。
type RequirementAnalysis struct {
	Summary            string   `json:"summary"`
	Goals              []string `json:"goals"`
	CurrentState       string   `json:"current_state"`
	Scope              []string `json:"scope"`
	OutOfScope         []string `json:"out_of_scope"`
	Constraints        []string `json:"constraints"`
	Risks              []string `json:"risks"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

// Validate 校验需求分析的必填字段和列表内容。
func (a RequirementAnalysis) Validate() error {
	if err := requirePlanningText("RequirementAnalysis", "summary", a.Summary); err != nil {
		return err
	}
	if err := requirePlanningText("RequirementAnalysis", "current_state", a.CurrentState); err != nil {
		return err
	}
	for _, field := range []struct {
		name string
		list []string
	}{
		{"goals", a.Goals},
		{"scope", a.Scope},
		{"out_of_scope", a.OutOfScope},
		{"constraints", a.Constraints},
		{"risks", a.Risks},
		{"acceptance_criteria", a.AcceptanceCriteria},
	} {
		if err := validatePlanningTextList("RequirementAnalysis", field.name, field.list, true); err != nil {
			return err
		}
	}
	return nil
}

// PlanningTaskDraft 是方案规划师输出的单项草案任务。
// ID 仅用于本版方案内引用，不是正式 Task ID。
type PlanningTaskDraft struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	DependsOn          []string `json:"depends_on,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

// ImplementationPlan 是方案规划师输出的实施方案草案。
type ImplementationPlan struct {
	Summary string               `json:"summary"`
	Tasks   []PlanningTaskDraft  `json:"tasks"`
}

// Validate 校验任务标识唯一、依赖引用有效且依赖项位于被依赖任务之后之前。
// 该顺序约束使任务列表本身就是可读的拓扑执行顺序，并能拒绝循环依赖。
func (p ImplementationPlan) Validate() error {
	if err := requirePlanningText("ImplementationPlan", "summary", p.Summary); err != nil {
		return err
	}
	if len(p.Tasks) == 0 {
		return invalidField("ImplementationPlan", "tasks", "至少需要一个草案任务")
	}
	positions := make(map[string]int, len(p.Tasks))
	for i, task := range p.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			return invalidField("ImplementationPlan", fmt.Sprintf("tasks[%d].id", i), "任务标识不能为空")
		}
		if _, exists := positions[id]; exists {
			return invalidField("ImplementationPlan", fmt.Sprintf("tasks[%d].id", i), "任务标识重复")
		}
		positions[id] = i
		if err := requirePlanningText("ImplementationPlan", fmt.Sprintf("tasks[%d].title", i), task.Title); err != nil {
			return err
		}
		if err := requirePlanningText("ImplementationPlan", fmt.Sprintf("tasks[%d].description", i), task.Description); err != nil {
			return err
		}
		if err := validatePlanningTextList("ImplementationPlan", fmt.Sprintf("tasks[%d].acceptance_criteria", i), task.AcceptanceCriteria, true); err != nil {
			return err
		}
	}
	for i, task := range p.Tasks {
		seen := make(map[string]struct{}, len(task.DependsOn))
		for _, dependency := range task.DependsOn {
			dependency = strings.TrimSpace(dependency)
			position, exists := positions[dependency]
			if !exists {
				return invalidField("ImplementationPlan", fmt.Sprintf("tasks[%d].depends_on", i), "依赖引用不存在: "+dependency)
			}
			if position >= i {
				return invalidField("ImplementationPlan", fmt.Sprintf("tasks[%d].depends_on", i), "依赖任务必须出现在当前任务之前")
			}
			if _, duplicate := seen[dependency]; duplicate {
				return invalidField("ImplementationPlan", fmt.Sprintf("tasks[%d].depends_on", i), "依赖引用重复: "+dependency)
			}
			seen[dependency] = struct{}{}
		}
	}
	return nil
}

// ParseRequirementAnalysis 严格解析需求分析 JSON：拒绝未知字段、多份 JSON 文档和非法字段值。
func ParseRequirementAnalysis(raw []byte) (RequirementAnalysis, error) {
	var value RequirementAnalysis
	if err := decodePlanningJSON(raw, &value); err != nil {
		return RequirementAnalysis{}, fmt.Errorf("解析需求分析失败: %w", err)
	}
	if err := value.Validate(); err != nil {
		return RequirementAnalysis{}, fmt.Errorf("需求分析校验失败: %w", err)
	}
	return value, nil
}

// ParseImplementationPlan 严格解析实施方案 JSON。
func ParseImplementationPlan(raw []byte) (ImplementationPlan, error) {
	var value ImplementationPlan
	if err := decodePlanningJSON(raw, &value); err != nil {
		return ImplementationPlan{}, fmt.Errorf("解析实施方案失败: %w", err)
	}
	if err := value.Validate(); err != nil {
		return ImplementationPlan{}, fmt.Errorf("实施方案校验失败: %w", err)
	}
	return value, nil
}

func decodePlanningJSON(raw []byte, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return invalidField("PlanningOutput", "json", "JSON 输出不能为空")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidValue, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return invalidField("PlanningOutput", "json", "只能包含一个 JSON 文档")
		}
		return fmt.Errorf("%w: %v", ErrInvalidValue, err)
	}
	return nil
}

func requirePlanningText(entity, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalidField(entity, field, "内容不能为空")
	}
	return nil
}

func validatePlanningTextList(entity, field string, values []string, required bool) error {
	if required && len(values) == 0 {
		return invalidField(entity, field, "至少需要一项")
	}
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return invalidField(entity, fmt.Sprintf("%s[%d]", field, i), "内容不能为空")
		}
	}
	return nil
}
