package domain

import (
	"strings"
	"time"
)

// Priority 表示需求在队列中的相对优先级。
type Priority string

// 第一版支持的优先级。
const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

// Validate 校验优先级是否受支持。
func (p Priority) Validate() error {
	switch p {
	case PriorityLow, PriorityNormal, PriorityHigh:
		return nil
	default:
		return invalidField("Requirement", "priority", "不支持的优先级 "+string(p))
	}
}

// weight 返回优先级的排序权重，数值越小越优先。
func (p Priority) weight() int {
	switch p {
	case PriorityHigh:
		return 0
	case PriorityLow:
		return 2
	default:
		return 1
	}
}

// RequirementStatus 表示需求在队列中的状态。
type RequirementStatus string

// 需求状态。
const (
	RequirementStatusPending   RequirementStatus = "pending"
	RequirementStatusActive    RequirementStatus = "active"
	RequirementStatusCompleted RequirementStatus = "completed"
	RequirementStatusCancelled RequirementStatus = "cancelled"
)

// requirementStatusMachine 定义需求状态迁移。
var requirementStatusMachine = statusMachine{
	entity: "Requirement",
	transitions: map[string][]string{
		string(RequirementStatusPending): {
			string(RequirementStatusActive),
			string(RequirementStatusCancelled),
		},
		string(RequirementStatusActive): {
			string(RequirementStatusCompleted),
			string(RequirementStatusCancelled),
		},
	},
	terminal: []string{
		string(RequirementStatusCompleted),
		string(RequirementStatusCancelled),
	},
}

// Validate 校验需求状态是否属于已知状态。
func (s RequirementStatus) Validate() error {
	if requirementStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("Requirement", "status", "未知状态 "+string(s))
}

// IsTerminal 判断需求状态是否为终态。
func (s RequirementStatus) IsTerminal() bool {
	return requirementStatusMachine.isTerminal(string(s))
}

// Requirement 是用户提交的原始需求。
type Requirement struct {
	ID          ID                `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Priority    Priority          `json:"priority"`
	Status      RequirementStatus `json:"status"`
	CreatedAt   time.Time         `json:"created_at"`
}

// NewRequirement 创建并校验需求。
func NewRequirement(title, description string, priority Priority, at time.Time) (Requirement, error) {
	requirement := Requirement{
		ID:          NewID(),
		Title:       strings.TrimSpace(title),
		Description: strings.TrimSpace(description),
		Priority:    priority,
		Status:      RequirementStatusPending,
		CreatedAt:   normalizeTime(at),
	}
	if err := requirement.Validate(); err != nil {
		return Requirement{}, err
	}
	return requirement, nil
}

// Validate 校验需求定义是否完整。
func (r Requirement) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Title) == "" {
		return invalidField("Requirement", "title", "标题不能为空")
	}
	if strings.TrimSpace(r.Description) == "" {
		return invalidField("Requirement", "description", "描述不能为空")
	}
	if err := r.Priority.Validate(); err != nil {
		return err
	}
	return r.Status.Validate()
}

// Transition 将需求迁移到下一个状态。
func (r *Requirement) Transition(next RequirementStatus) error {
	if r == nil {
		return invalidField("Requirement", "", "实体为空")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := requirementStatusMachine.validate(string(r.Status), string(next)); err != nil {
		return err
	}
	r.Status = next
	return nil
}

// Less 判断当前需求是否应排在另一个需求之前，用于队列排序。
func (r Requirement) Less(other Requirement) bool {
	if r.Priority.weight() != other.Priority.weight() {
		return r.Priority.weight() < other.Priority.weight()
	}
	if !r.CreatedAt.Equal(other.CreatedAt) {
		return r.CreatedAt.Before(other.CreatedAt)
	}
	return r.ID.String() < other.ID.String()
}
