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

// Rank 返回优先级的持久化排序值，数值越小越优先。
//
// 数据库以整数保存排序值，本方法是与存储层共享的唯一映射来源。
func (p Priority) Rank() int {
	switch p {
	case PriorityHigh:
		return 0
	case PriorityLow:
		return 2
	default:
		return 1
	}
}

// ParsePriorityRank 把持久化的排序值还原为优先级。
func ParsePriorityRank(rank int) (Priority, error) {
	switch rank {
	case 0:
		return PriorityHigh, nil
	case 1:
		return PriorityNormal, nil
	case 2:
		return PriorityLow, nil
	default:
		return "", invalidField("Requirement", "priority", "不支持的优先级排序值")
	}
}

// weight 返回优先级的排序权重，数值越小越优先。
func (p Priority) weight() int {
	return p.Rank()
}

// RequirementStatus 表示需求在队列中的状态。
type RequirementStatus string

// 需求状态。
const (
	RequirementStatusQueued     RequirementStatus = "queued"
	RequirementStatusProcessing RequirementStatus = "processing"
	RequirementStatusCompleted  RequirementStatus = "completed"
	RequirementStatusFailed     RequirementStatus = "failed"
	RequirementStatusCancelled  RequirementStatus = "cancelled"
)

// requirementStatusMachine 定义需求状态迁移。
//
// failed 允许回到 queued，用于用户显式重新排队。
var requirementStatusMachine = statusMachine{
	entity: "Requirement",
	transitions: map[string][]string{
		string(RequirementStatusQueued): {
			string(RequirementStatusProcessing),
			string(RequirementStatusCancelled),
		},
		string(RequirementStatusProcessing): {
			string(RequirementStatusCompleted),
			string(RequirementStatusFailed),
			string(RequirementStatusCancelled),
		},
		string(RequirementStatusFailed): {
			string(RequirementStatusQueued),
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

// RequirementSource 表示需求的来源渠道。
type RequirementSource string

// 第一版支持的来源渠道。
const (
	// RequirementSourceManual 表示需求由用户在本地手工提交。
	RequirementSourceManual RequirementSource = "manual"
)

// Validate 校验需求来源是否受支持。
func (s RequirementSource) Validate() error {
	switch s {
	case RequirementSourceManual:
		return nil
	default:
		return invalidField("Requirement", "source", "不支持的来源 "+string(s))
	}
}

// Requirement 是用户提交的原始需求。
type Requirement struct {
	ID          ID                `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Priority    Priority          `json:"priority"`
	Status      RequirementStatus `json:"status"`
	Source      RequirementSource `json:"source"`
	SourceID    string            `json:"source_id,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// NewRequirement 创建并校验需求。
func NewRequirement(title, description string, priority Priority, at time.Time) (Requirement, error) {
	createdAt := normalizeTime(at)
	requirement := Requirement{
		ID:          NewID(),
		Title:       strings.TrimSpace(title),
		Description: strings.TrimSpace(description),
		Priority:    priority,
		Status:      RequirementStatusQueued,
		Source:      RequirementSourceManual,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
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
	if err := r.Status.Validate(); err != nil {
		return err
	}
	return r.Source.Validate()
}

// Transition 将需求迁移到下一个状态，并记录更新时间。
func (r *Requirement) Transition(next RequirementStatus, at time.Time) error {
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
	r.UpdatedAt = normalizeTime(at)
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
