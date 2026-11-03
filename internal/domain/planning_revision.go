package domain

import (
	"encoding/json"
	"strings"
	"time"
)

// PlanningRevision 是某一轮规划产生的分析与方案快照。
//
// 每个版本只追加不覆盖：后续轮次产生新的 Revision，历史版本仍然可读，用户确认时
// 指出采纳的是哪一版。本阶段只保存结构化 JSON 快照，不解析内部分析字段与任务拆分
// 规则；字段结构由后续里程碑定义，数据库侧只约束 JSON 结构合法。
type PlanningRevision struct {
	ID ID `json:"id"`
	// PlanningSessionID 是版本所属规划会话。
	PlanningSessionID ID `json:"planning_session_id"`
	// RevisionNumber 是会话内的版本号，从 1 开始且唯一。
	RevisionNumber int `json:"revision_number"`
	// RequirementAnalysisJSON 是需求分析师的结构化分析快照。
	RequirementAnalysisJSON json.RawMessage `json:"requirement_analysis_json"`
	// ImplementationPlanJSON 是方案规划师的结构化方案快照。
	ImplementationPlanJSON json.RawMessage `json:"implementation_plan_json"`
	// ValidationResultJSON 是这一版方案的校验结果，未校验时为空。
	ValidationResultJSON json.RawMessage `json:"validation_result_json,omitempty"`
	// CreatedAt 是版本生成时间。
	CreatedAt time.Time `json:"created_at"`
}

// NewPlanningRevision 创建并校验一个规划版本。
//
// 版本号作为参数传入，因为它由存储层在事务内分配，调用方不能自带版本号覆盖已产生
// 的快照。
func NewPlanningRevision(
	sessionID ID,
	number int,
	analysis, plan, validation json.RawMessage,
	at time.Time,
) (PlanningRevision, error) {
	revision := PlanningRevision{
		ID:                      NewID(),
		PlanningSessionID:       sessionID,
		RevisionNumber:          number,
		RequirementAnalysisJSON: analysis,
		ImplementationPlanJSON:  plan,
		ValidationResultJSON:    validation,
		CreatedAt:               normalizeTime(at),
	}
	if err := revision.Validate(); err != nil {
		return PlanningRevision{}, err
	}
	return revision, nil
}

// Validate 校验规划版本是否完整。
//
// 分析与方案必须是合法 JSON 文档，校验结果可以缺省；这里只做结构检查，不解释 JSON
// 内部字段，也不为内部字段建立取值白名单。
func (r PlanningRevision) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if err := r.PlanningSessionID.Validate(); err != nil {
		return err
	}
	if r.RevisionNumber <= 0 {
		return invalidField("PlanningRevision", "revision_number", "版本号必须大于 0")
	}
	if err := validatePlanningJSON("requirement_analysis_json", r.RequirementAnalysisJSON, true); err != nil {
		return err
	}
	if err := validatePlanningJSON("implementation_plan_json", r.ImplementationPlanJSON, true); err != nil {
		return err
	}
	if err := validatePlanningJSON("validation_result_json", r.ValidationResultJSON, false); err != nil {
		return err
	}
	if r.CreatedAt.IsZero() {
		return invalidField("PlanningRevision", "created_at", "创建时间不能为空")
	}
	return nil
}

// validatePlanningJSON 校验一段 JSON 快照的结构。
//
// required 为 false 时允许为空（表示本次没有该项快照）；不允许把字面量 null 当作
// "有快照"，否则"未校验"与"校验结果为 null"无法区分。
func validatePlanningJSON(field string, value json.RawMessage, required bool) error {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		if required {
			return invalidField("PlanningRevision", field, "JSON 快照不能为空")
		}
		return nil
	}
	if !json.Valid([]byte(trimmed)) {
		return invalidField("PlanningRevision", field, "必须是合法 JSON")
	}
	if trimmed == "null" {
		return invalidField("PlanningRevision", field, "JSON 快照不能是 null")
	}
	return nil
}
