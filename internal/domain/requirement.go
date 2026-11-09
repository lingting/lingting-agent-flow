package domain

import (
	"encoding/json"
	"fmt"
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
//
// 确认生成的需求（字段 ProjectID / PlanningSessionID / Final*JSON / ConfirmedAt 有值）
// 记录的是"用户采纳了哪一版规划"，因此这些列可空：历史的手工需求没有规划来源，读取层
// 不得为空值补造默认项目或默认快照。
type Requirement struct {
	ID          ID                `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Priority    Priority          `json:"priority"`
	Status      RequirementStatus `json:"status"`
	Source      RequirementSource `json:"source"`
	SourceID    string            `json:"source_id,omitempty"`
	// ProjectID 是需求所属项目标识；空值表示该需求不是由确认流程生成。
	ProjectID string `json:"project_id,omitempty"`
	// PlanningSessionID 是产生该需求的规划会话标识；空值表示没有规划来源。
	PlanningSessionID *ID `json:"planning_session_id,omitempty"`
	// FinalAnalysisJSON 是用户确认那一版 Revision 的需求分析快照。
	FinalAnalysisJSON json.RawMessage `json:"final_analysis_json,omitempty"`
	// FinalPlanJSON 是用户确认那一版 Revision 的实施方案快照。
	FinalPlanJSON json.RawMessage `json:"final_plan_json,omitempty"`
	// ConfirmedAt 是用户确认方案的时间；只有确认生成的需求才有值。
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	// DeletedAt 是逻辑删除时间，空值表示未删除。
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// NewRequirement 创建并校验手工提交的需求。
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

// ConfirmedRequirementInput 是一次"采纳方案并生成正式需求"的输入。
//
// 输入只包含服务端依据已确认 Revision 与项目计算出的事实：需求标识、初始状态、来源与
// 时间戳由领域层决定，调用方无法指定任务列表、任务顺序或任务配置。
type ConfirmedRequirementInput struct {
	// ProjectID 是需求所属项目标识。
	ProjectID string
	// PlanningSessionID 是产生该需求的规划会话标识。
	PlanningSessionID ID
	// Title 是需求标题，由最终需求分析的摘要确定。
	Title string
	// Description 是需求描述，由最终需求分析的目标、范围、约束与验收标准确定。
	Description string
	// Priority 是需求优先级，确认流程使用 normal。
	Priority Priority
	// AnalysisJSON 是最终需求分析快照。
	AnalysisJSON json.RawMessage
	// PlanJSON 是最终实施方案快照。
	PlanJSON json.RawMessage
	// At 是确认时间。
	At time.Time
}

// NewConfirmedRequirement 创建并校验确认生成的需求。
//
// 初始状态沿用需求领域的既有约定（queued）：本阶段不新增第二套需求状态机，需求进度
// 聚合属于后续里程碑。确认时间同时写入 ConfirmedAt。
func NewConfirmedRequirement(input ConfirmedRequirementInput) (Requirement, error) {
	confirmedAt := normalizeTime(input.At)
	sessionID := input.PlanningSessionID
	requirement := Requirement{
		ID:                NewID(),
		Title:             strings.TrimSpace(input.Title),
		Description:       strings.TrimSpace(input.Description),
		Priority:          input.Priority,
		Status:            RequirementStatusQueued,
		Source:            RequirementSourceManual,
		ProjectID:         strings.ToLower(strings.TrimSpace(input.ProjectID)),
		PlanningSessionID: &sessionID,
		FinalAnalysisJSON: input.AnalysisJSON,
		FinalPlanJSON:     input.PlanJSON,
		ConfirmedAt:       &confirmedAt,
		CreatedAt:         confirmedAt,
		UpdatedAt:         confirmedAt,
	}
	if requirement.Priority == "" {
		requirement.Priority = PriorityNormal
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
	if err := r.Source.Validate(); err != nil {
		return err
	}
	return r.validateConfirmation()
}

// validateConfirmation 校验"确认生成"相关字段的组合一致性。
//
// 这些列在数据库里全部可空（历史手工需求没有规划来源），但只要有规划来源，就必须能
// 指出完整的确认事实：所属项目、需求分析快照、实施方案快照与确认时间缺一不可。
func (r Requirement) validateConfirmation() error {
	projectID := strings.ToLower(strings.TrimSpace(r.ProjectID))
	if projectID != r.ProjectID {
		return invalidField("Requirement", "project_id", "项目标识必须是小写规范形式")
	}
	if err := ValidateProjectID(projectID); err != nil {
		return err
	}
	if r.PlanningSessionID != nil {
		if err := r.PlanningSessionID.Validate(); err != nil {
			return err
		}
	}
	if err := validateRequirementSnapshot("final_analysis_json", r.FinalAnalysisJSON); err != nil {
		return err
	}
	if err := validateRequirementSnapshot("final_plan_json", r.FinalPlanJSON); err != nil {
		return err
	}
	hasPlanningSource := r.PlanningSessionID != nil
	if !hasPlanningSource {
		if projectID != "" || len(r.FinalAnalysisJSON) > 0 || len(r.FinalPlanJSON) > 0 || r.ConfirmedAt != nil {
			return invalidField(
				"Requirement",
				"planning_session_id",
				"只有确认生成的需求才能记录项目、规划快照与确认时间",
			)
		}
		return nil
	}
	if projectID == "" {
		return invalidField("Requirement", "project_id", "确认生成的需求必须关联项目")
	}
	if len(strings.TrimSpace(string(r.FinalAnalysisJSON))) == 0 {
		return invalidField("Requirement", "final_analysis_json", "确认生成的需求必须保存需求分析快照")
	}
	if len(strings.TrimSpace(string(r.FinalPlanJSON))) == 0 {
		return invalidField("Requirement", "final_plan_json", "确认生成的需求必须保存实施方案快照")
	}
	if r.ConfirmedAt == nil {
		return invalidField("Requirement", "confirmed_at", "确认生成的需求必须记录确认时间")
	}
	if r.ConfirmedAt.IsZero() {
		return invalidField("Requirement", "confirmed_at", "确认时间不能为空")
	}
	return nil
}

// validateRequirementSnapshot 校验一段需求快照的结构。
//
// 只做结构约束：快照内部字段的语义由产生它的 Revision 解析路径负责，这里不解释内容。
func validateRequirementSnapshot(field string, value json.RawMessage) error {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		return nil
	}
	if !json.Valid([]byte(trimmed)) {
		return invalidField("Requirement", field, "必须是合法 JSON")
	}
	if trimmed == "null" {
		return invalidField("Requirement", field, "JSON 快照不能是 null")
	}
	return nil
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

// Deleted 判断需求是否已被逻辑删除。
func (r Requirement) Deleted() bool {
	return r.DeletedAt != nil
}

// MarkDeleted 记录需求的逻辑删除时间。
//
// 删除只写 DeletedAt 与 UpdatedAt：需求的业务状态、确认快照、规划来源与确认时间全部
// 保持不变，Step / Attempt / Session / Event / CodeSave 等执行历史也不会被清理。
// 重复删除返回状态冲突，调用方必须把它当作"需求已经不在默认视图里"，而不是幂等成功。
func (r *Requirement) MarkDeleted(at time.Time) error {
	if r == nil {
		return invalidField("Requirement", "", "实体为空")
	}
	if r.DeletedAt != nil {
		return fmt.Errorf("%w: 需求已被删除", ErrInvalidState)
	}
	now := normalizeTime(at)
	r.DeletedAt = &now
	r.UpdatedAt = now
	return nil
}
