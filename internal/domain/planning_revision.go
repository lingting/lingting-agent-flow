package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PlanningRevisionSnapshotVersion 是当前程序写入的规划快照结构版本。
//
// 版本号写在每一版 Revision 上而不是只写在代码里：持久化之后，读取方必须能独立判断
// 这一版快照的字段含义，而不是依赖"数据库里出现的都是当前版本"这一假设。
const PlanningRevisionSnapshotVersion = 1

// PlanningRevision 是某一轮规划产生的分析与方案快照。
//
// 每个版本只追加不覆盖：后续轮次产生新的 Revision，历史版本仍然可读，用户确认时
// 指出采纳的是哪一版。分析与方案快照的字段结构由 RequirementAnalysis 与
// ImplementationPlan 定义，快照版本记录在 SnapshotVersion 上；本类型只保证"能落库的
// 一定是一份结构版本明确、JSON 合法的文档"，内部字段的语义校验发生在生成快照之前。
type PlanningRevision struct {
	ID ID `json:"id"`
	// PlanningSessionID 是版本所属规划会话。
	PlanningSessionID ID `json:"planning_session_id"`
	// RevisionNumber 是会话内的版本号，从 1 开始且唯一。
	RevisionNumber int `json:"revision_number"`
	// SnapshotVersion 是这一版快照的结构版本，由写入方固定为当前支持的版本。
	//
	// 它与版本号不同：RevisionNumber 表示"会话内的第几版方案"，SnapshotVersion 表示
	// "这一版 JSON 用哪一套字段结构"，两者都必须落库且不得互相替代。
	SnapshotVersion int `json:"snapshot_version"`
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
		SnapshotVersion:         PlanningRevisionSnapshotVersion,
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
	if r.SnapshotVersion <= 0 {
		return invalidField("PlanningRevision", "snapshot_version", "快照版本必须大于 0")
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

// CheckSnapshotVersion 校验这一版快照的结构版本能否被当前程序解析。
//
// 版本不一致时拒绝解析而不是尽力而为：字段含义随版本变化，按当前结构读旧快照会得到
// 一份"看起来完整但内容错位"的方案，这比明确报错更危险。
func (r PlanningRevision) CheckSnapshotVersion() error {
	if r.SnapshotVersion == PlanningRevisionSnapshotVersion {
		return nil
	}
	return invalidField(
		"PlanningRevision",
		"snapshot_version",
		fmt.Sprintf(
			"不支持的快照版本 %d，当前支持 %d",
			r.SnapshotVersion,
			PlanningRevisionSnapshotVersion,
		),
	)
}

// RequirementAnalysis 解析本版本保存的需求分析快照。
//
// 解析只依赖这一行记录本身：快照版本、JSON 文档与领域校验都在读取路径完成，不依赖
// 产生它的会话状态或进程内缓存。
func (r PlanningRevision) RequirementAnalysis() (RequirementAnalysis, error) {
	if err := r.CheckSnapshotVersion(); err != nil {
		return RequirementAnalysis{}, err
	}
	return ParseRequirementAnalysisSnapshot(r.RequirementAnalysisJSON)
}

// ImplementationPlan 解析本版本保存的实施方案快照。
func (r PlanningRevision) ImplementationPlan() (ImplementationPlan, error) {
	if err := r.CheckSnapshotVersion(); err != nil {
		return ImplementationPlan{}, err
	}
	return ParseImplementationPlanSnapshot(r.ImplementationPlanJSON)
}
