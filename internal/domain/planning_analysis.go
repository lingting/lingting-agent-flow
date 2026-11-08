package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// 规划结构化输出的边界常量。
//
// 这些上限描述"一份规划快照能有多大"，与模型调用超时、容器资源无关：超出上限的输出
// 按不可用处理，而不是把大段文本写进数据库。
const (
	// MaxPlanningOutputBytes 是单个规划角色输出参与解析的最大字节数。
	MaxPlanningOutputBytes = 64 << 10
	// MaxRequirementAnalysisSummaryLength 是需求分析摘要的长度上限。
	MaxRequirementAnalysisSummaryLength = 2000
	// MaxRequirementAnalysisItemLength 是需求分析单条列表项的长度上限。
	MaxRequirementAnalysisItemLength = 500
	// MaxRequirementAnalysisItemCount 是需求分析单个列表的条数上限。
	MaxRequirementAnalysisItemCount = 30
	// MaxImplementationPlanSummaryLength 是实施方案摘要的长度上限。
	MaxImplementationPlanSummaryLength = 2000
	// MaxImplementationPlanTaskCount 是实施方案的任务条数上限。
	MaxImplementationPlanTaskCount = 30
	// MaxImplementationPlanTaskIDLength 是任务草案标识的长度上限。
	MaxImplementationPlanTaskIDLength = 64
	// MaxImplementationPlanTaskTitleLength 是任务草案标题的长度上限。
	MaxImplementationPlanTaskTitleLength = 200
	// MaxImplementationPlanTaskDescriptionLength 是任务草案描述的长度上限。
	MaxImplementationPlanTaskDescriptionLength = 4000
	// MaxImplementationPlanTaskDependencyCount 是单个任务的依赖条数上限。
	MaxImplementationPlanTaskDependencyCount = 20
	// MaxImplementationPlanTaskCriteriaCount 是单个任务验收条件的条数上限。
	MaxImplementationPlanTaskCriteriaCount = 20
)

// 规划结构化输出不可用的两种原因。
//
// 两者都必须让本轮分析失败，但诊断信息不同：前者说明输出根本不是可解析的结构，后者
// 说明结构合法却没有通过领域校验（缺字段、字段超长、任务依赖非法等）。分开表达是为了
// 让调用方能够区分"模型没按协议输出"与"输出违反领域约束"。
var (
	// ErrPlanningOutputUnparseable 表示输出无法解析为结构化规划快照。
	ErrPlanningOutputUnparseable = fmt.Errorf("%w: 规划角色输出无法解析", ErrInvalidValue)
	// ErrPlanningOutputInvalid 表示结构合法但没有通过领域校验。
	ErrPlanningOutputInvalid = fmt.Errorf("%w: 规划角色输出不合法", ErrInvalidValue)
)

// RequirementAnalysis 是需求分析师输出的结构化需求分析。
//
// 它是需求分析师与方案规划师之间的唯一契约：自然语言输出不会被直接交给规划师，必须
// 先解析为该结构并通过 Validate 的确定性校验。八个字段全部必填：摘要与七类列表共同
// 构成"这次要做什么、现状如何、范围到哪里、不做什么、受什么约束、有什么风险、如何算
// 完成"，缺少任何一项都说明本轮分析不完整，不能进入方案规划。
type RequirementAnalysis struct {
	// Summary 是一句话需求摘要。
	Summary string `json:"summary"`
	// Goals 是本次需求要达成的目标。
	Goals []string `json:"goals"`
	// CurrentState 是项目现状：已有实现、相关模块与已知限制。
	CurrentState []string `json:"current_state"`
	// Scope 是本次改动的范围。
	Scope []string `json:"scope"`
	// OutOfScope 是明确不做、不得顺带完成的内容。
	OutOfScope []string `json:"out_of_scope"`
	// Constraints 是必须遵守的约束：技术选型、兼容要求、安全边界等。
	Constraints []string `json:"constraints"`
	// Risks 是风险与未知项。
	Risks []string `json:"risks"`
	// AcceptanceCriteria 是可检查的验收标准。
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

// Validate 校验需求分析是否完整。
//
// 校验只做确定性规则：摘要与每个列表都非空、单条与整体长度受上限约束。它不判断内容
// 是否"合理"，那属于后续的用户确认与验收，而不是解析阶段能下结论的事。
func (a RequirementAnalysis) Validate() error {
	if err := validatePlanningTextField(
		"RequirementAnalysis",
		"summary",
		a.Summary,
		MaxRequirementAnalysisSummaryLength,
	); err != nil {
		return err
	}
	lists := []struct {
		field string
		items []string
	}{
		{"goals", a.Goals},
		{"current_state", a.CurrentState},
		{"scope", a.Scope},
		{"out_of_scope", a.OutOfScope},
		{"constraints", a.Constraints},
		{"risks", a.Risks},
		{"acceptance_criteria", a.AcceptanceCriteria},
	}
	for _, list := range lists {
		if err := validatePlanningList(
			"RequirementAnalysis",
			list.field,
			list.items,
			MaxRequirementAnalysisItemCount,
			MaxRequirementAnalysisItemLength,
		); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot 返回需求分析的规范化 JSON 快照。
//
// 只有校验通过的实体才会生成快照：Revision 保存的是这份规范化文档，读取方可以用
// ParseRequirementAnalysisSnapshot 独立解析，不需要产生它的运行时状态。
func (a RequirementAnalysis) Snapshot() (json.RawMessage, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return marshalPlanningSnapshot("需求分析", a)
}

// ImplementationPlanTask 是方案规划师输出的单个任务草案。
//
// 它只是草案：本阶段不创建正式 Task，也不产生 Step 或 Attempt。字段名与提示词声明的
// 输出格式严格一致，未知字段由解析入口拒绝。
type ImplementationPlanTask struct {
	// ID 是任务草案标识，在同一方案内唯一，只用于表达依赖，不落库为正式任务标识。
	ID string `json:"id"`
	// Title 是任务标题。
	Title string `json:"title"`
	// Description 是任务的具体内容。
	Description string `json:"description"`
	// DependsOn 是前置任务的标识列表，可以为空表示无前置依赖。
	DependsOn []string `json:"depends_on"`
	// AcceptanceCriteria 是该任务的验收条件，必须非空。
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

// ImplementationPlan 是方案规划师输出的结构化实施方案。
//
// 它是方案规划师与编排器之间的唯一契约：任务顺序即建议的执行顺序，依赖必须指向排在
// 前面的任务，因此"拓扑顺序"与"无环"由同一条规则保证。
type ImplementationPlan struct {
	// Summary 是一句话方案摘要。
	Summary string `json:"summary"`
	// Tasks 是任务草案列表，顺序即执行顺序。
	Tasks []ImplementationPlanTask `json:"tasks"`
}

// Validate 校验实施方案是否完整且依赖关系可用。
//
// 规则集中在这里，不分散到调用方：
//   - 摘要非空且受长度上限约束；
//   - 任务数量必须在 1 到上限之间，标识唯一、标题与描述非空、验收条件非空；
//   - 依赖必须指向排在自己前面的任务：这条规则同时保证引用有效、无自依赖、无环，
//     并让任务顺序本身就是可执行顺序；
//   - 同一条依赖不得重复声明，避免依赖集合的含义随解析结果漂移。
func (p ImplementationPlan) Validate() error {
	if err := validatePlanningTextField(
		"ImplementationPlan",
		"summary",
		p.Summary,
		MaxImplementationPlanSummaryLength,
	); err != nil {
		return err
	}
	if len(p.Tasks) == 0 {
		return invalidField("ImplementationPlan", "tasks", "实施方案至少需要一个任务")
	}
	if len(p.Tasks) > MaxImplementationPlanTaskCount {
		return invalidField(
			"ImplementationPlan",
			"tasks",
			fmt.Sprintf("任务数量 %d 超过上限 %d", len(p.Tasks), MaxImplementationPlanTaskCount),
		)
	}
	// declared 记录已经出现过的任务标识：依赖只能指向它，因此任务列表的顺序就是
	// 拓扑顺序，不需要额外的图算法。
	declared := make(map[string]struct{}, len(p.Tasks))
	for index, task := range p.Tasks {
		position := index + 1
		if err := task.validate(position, declared); err != nil {
			return err
		}
		declared[strings.TrimSpace(task.ID)] = struct{}{}
	}
	return nil
}

// Snapshot 返回实施方案的规范化 JSON 快照。
func (p ImplementationPlan) Snapshot() (json.RawMessage, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return marshalPlanningSnapshot("实施方案", p)
}

// validate 校验单个任务草案。
//
// declared 只包含排在前面的任务标识：依赖声明在这里就能同时完成"引用有效""不得自依赖"
// 与"不得成环"三项判断。
func (t ImplementationPlanTask) validate(position int, declared map[string]struct{}) error {
	field := func(name string) string {
		return fmt.Sprintf("tasks[%d].%s", position, name)
	}
	identifier := strings.TrimSpace(t.ID)
	if identifier == "" {
		return invalidField("ImplementationPlan", field("id"), "任务标识不能为空")
	}
	if len(identifier) > MaxImplementationPlanTaskIDLength {
		return invalidField(
			"ImplementationPlan",
			field("id"),
			fmt.Sprintf("任务标识长度 %d 超过上限 %d", len(identifier), MaxImplementationPlanTaskIDLength),
		)
	}
	if _, duplicated := declared[identifier]; duplicated {
		return invalidField(
			"ImplementationPlan",
			field("id"),
			fmt.Sprintf("任务标识 %q 重复", identifier),
		)
	}
	if err := validatePlanningTextField(
		"ImplementationPlan",
		field("title"),
		t.Title,
		MaxImplementationPlanTaskTitleLength,
	); err != nil {
		return err
	}
	if err := validatePlanningTextField(
		"ImplementationPlan",
		field("description"),
		t.Description,
		MaxImplementationPlanTaskDescriptionLength,
	); err != nil {
		return err
	}
	if err := validatePlanningList(
		"ImplementationPlan",
		field("acceptance_criteria"),
		t.AcceptanceCriteria,
		MaxImplementationPlanTaskCriteriaCount,
		MaxRequirementAnalysisItemLength,
	); err != nil {
		return err
	}
	return t.validateDependencies(position, declared)
}

// validateDependencies 校验任务依赖的引用与顺序。
//
// 依赖只能指向排在前面的任务：引用不存在的任务、依赖自己、依赖后面的任务都会让顺序
// 失去可执行性，因此统一按"依赖必须指向更早的任务"拒绝。
func (t ImplementationPlanTask) validateDependencies(
	position int,
	declared map[string]struct{},
) error {
	field := fmt.Sprintf("tasks[%d].depends_on", position)
	if len(t.DependsOn) > MaxImplementationPlanTaskDependencyCount {
		return invalidField(
			"ImplementationPlan",
			field,
			fmt.Sprintf("依赖条数 %d 超过上限 %d", len(t.DependsOn), MaxImplementationPlanTaskDependencyCount),
		)
	}
	seen := make(map[string]struct{}, len(t.DependsOn))
	for index, dependency := range t.DependsOn {
		trimmed := strings.TrimSpace(dependency)
		if trimmed == "" {
			return invalidField(
				"ImplementationPlan",
				field,
				fmt.Sprintf("第 %d 条依赖不能为空", index+1),
			)
		}
		if _, duplicated := seen[trimmed]; duplicated {
			return invalidField(
				"ImplementationPlan",
				field,
				fmt.Sprintf("依赖 %q 重复声明", trimmed),
			)
		}
		seen[trimmed] = struct{}{}
		if _, ok := declared[trimmed]; !ok {
			return invalidField(
				"ImplementationPlan",
				field,
				fmt.Sprintf("依赖 %q 不是排在前面的任务", trimmed),
			)
		}
	}
	return nil
}

// PlanningValidationResult 是一轮分析写进版本快照的结构化校验结论。
//
// 它只记录"这一版快照通过了哪些确定性校验"，不复制模型原文：原文可能超长，也可能
// 包含不应落库的内容。快照版本与 Revision 的 SnapshotVersion 保持一致，读取方据此
// 判断该结论的字段含义。
type PlanningValidationResult struct {
	// SnapshotVersion 是校验结论的结构版本。
	SnapshotVersion int `json:"snapshot_version"`
	// RequirementAnalysisValid 表示需求分析快照通过领域校验。
	RequirementAnalysisValid bool `json:"requirement_analysis_valid"`
	// ImplementationPlanValid 表示实施方案快照通过领域校验。
	ImplementationPlanValid bool `json:"implementation_plan_valid"`
	// TaskCount 是方案中任务草案的条数。
	TaskCount int `json:"task_count"`
}

// Snapshot 返回校验结论的 JSON 快照。
func (v PlanningValidationResult) Snapshot() (json.RawMessage, error) {
	return marshalPlanningSnapshot("校验结论", v)
}

// ParseRequirementAnalysis 把需求分析师的最终输出解析为结构化需求分析。
//
// 解析规则（与 ParseImplementationPlan 共用同一份实现）：
//   - 空输出、超过上限的输出直接拒绝；
//   - 只接受一个完整的 JSON 对象，允许整体被代码块围栏包裹；对象之后的任何内容
//     （说明文字或第二份 JSON 文档）都拒绝；
//   - 未知字段一律拒绝，避免把额外结构当成分析结论；
//   - 解析结果还必须通过 Validate 的领域校验。
//
// 该函数是纯函数：不读数据库、不启动进程。
func ParseRequirementAnalysis(text string) (RequirementAnalysis, error) {
	var analysis RequirementAnalysis
	if err := decodePlanningDocument(text, "需求分析", &analysis); err != nil {
		return RequirementAnalysis{}, err
	}
	if err := analysis.Validate(); err != nil {
		return RequirementAnalysis{}, fmt.Errorf("%w: %w", ErrPlanningOutputInvalid, err)
	}
	return analysis, nil
}

// ParseRequirementAnalysisSnapshot 解析持久化的需求分析快照。
//
// 它与 ParseRequirementAnalysis 使用同一套规则：Revision 保存的是规范化快照，读取
// 路径必须能独立完成解析与校验，不能依赖产生它的运行时状态。
func ParseRequirementAnalysisSnapshot(snapshot json.RawMessage) (RequirementAnalysis, error) {
	return ParseRequirementAnalysis(string(snapshot))
}

// ParseImplementationPlan 把方案规划师的最终输出解析为结构化实施方案。
//
// 规则与 ParseRequirementAnalysis 一致：单份 JSON 文档、拒绝未知字段、拒绝尾随内容，
// 解析后必须通过 ImplementationPlan.Validate 的领域校验。
func ParseImplementationPlan(text string) (ImplementationPlan, error) {
	var plan ImplementationPlan
	if err := decodePlanningDocument(text, "实施方案", &plan); err != nil {
		return ImplementationPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return ImplementationPlan{}, fmt.Errorf("%w: %w", ErrPlanningOutputInvalid, err)
	}
	return plan, nil
}

// ParseImplementationPlanSnapshot 解析持久化的实施方案快照。
func ParseImplementationPlanSnapshot(snapshot json.RawMessage) (ImplementationPlan, error) {
	return ParseImplementationPlan(string(snapshot))
}

// decodePlanningDocument 把一段角色输出解码为单个规划结构。
//
// 解码使用 DisallowUnknownFields：模型多给的字段不会被静默忽略，否则"字段名写错"会
// 变成"字段为空"，最终以缺字段的形式报错，掩盖真正的原因。
func decodePlanningDocument(text, kind string, target any) error {
	document, err := planningDocument(text, kind)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %s不是合法的结构化 JSON: %w", ErrPlanningOutputUnparseable, kind, err)
	}
	// 解码一个完整值之后再读一次：剩余内容无论是说明文字还是第二份 JSON 文档，
	// 都说明模型没有按协议只输出一份结论。
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf(
			"%w: %s输出包含多余内容或第二份 JSON 文档",
			ErrPlanningOutputUnparseable,
			kind,
		)
	}
	return nil
}

// planningDocument 返回待解码的单份 JSON 文档。
//
// 它只做三项前置判断：非空、长度受限、形状是 JSON 对象。围栏剥离是唯一的宽容处理，
// 因为"整体包在代码块里"不改变结论本身。
func planningDocument(text, kind string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", fmt.Errorf("%w: %s输出为空", ErrPlanningOutputUnparseable, kind)
	}
	if len(trimmed) > MaxPlanningOutputBytes {
		return "", fmt.Errorf(
			"%w: %s输出长度 %d 超过上限 %d",
			ErrPlanningOutputUnparseable,
			kind,
			len(trimmed),
			MaxPlanningOutputBytes,
		)
	}
	document := trimPlanningFence(trimmed)
	if document == "null" {
		return "", fmt.Errorf("%w: %s输出不能是 null", ErrPlanningOutputUnparseable, kind)
	}
	if !strings.HasPrefix(document, "{") {
		return "", fmt.Errorf("%w: %s必须是一个 JSON 对象", ErrPlanningOutputUnparseable, kind)
	}
	return document, nil
}

// trimPlanningFence 剥离整体包裹输出的代码块围栏。
//
// 只处理"输出整体被 ``` 围栏包裹"这一种情况：围栏内部必须仍然是完整文档，嵌套围栏
// 或部分包裹都不做猜测，交给后续解码拒绝。
func trimPlanningFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	_, body, found := strings.Cut(text, "\n")
	if !found {
		return text
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "```"))
}

// validatePlanningTextField 校验必填文本字段。
func validatePlanningTextField(entity, field, value string, limit int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return invalidField(entity, field, "不能为空")
	}
	if len(trimmed) > limit {
		return invalidField(
			entity,
			field,
			fmt.Sprintf("长度 %d 超过上限 %d", len(trimmed), limit),
		)
	}
	return nil
}

// validatePlanningList 校验必填文本列表。
func validatePlanningList(entity, field string, items []string, maxCount, maxItemLength int) error {
	if len(items) == 0 {
		return invalidField(entity, field, "列表不能为空")
	}
	if len(items) > maxCount {
		return invalidField(
			entity,
			field,
			fmt.Sprintf("列表条数 %d 超过上限 %d", len(items), maxCount),
		)
	}
	for index, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			return invalidField(entity, field, fmt.Sprintf("第 %d 项不能为空", index+1))
		}
		if len(trimmed) > maxItemLength {
			return invalidField(
				entity,
				field,
				fmt.Sprintf("第 %d 项长度 %d 超过上限 %d", index+1, len(trimmed), maxItemLength),
			)
		}
	}
	return nil
}

// marshalPlanningSnapshot 把已校验的结构编码为 JSON 快照。
func marshalPlanningSnapshot(kind string, value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %s快照序列化失败: %w", ErrPlanningOutputInvalid, kind, err)
	}
	if len(encoded) > MaxPlanningOutputBytes {
		return nil, fmt.Errorf(
			"%w: %s快照长度 %d 超过上限 %d",
			ErrPlanningOutputInvalid,
			kind,
			len(encoded),
			MaxPlanningOutputBytes,
		)
	}
	return json.RawMessage(encoded), nil
}
