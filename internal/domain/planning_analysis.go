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
//   - 同一条依赖不得重复声明，避免依赖集合的含义随解析结果漂移；
//   - 任务标识与依赖引用都不得带首尾空白：两侧用同一套精确匹配规则，不做裁剪后匹配。
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
	// 标识必须本身就是规范形式：带首尾空白的标识在依赖引用时会被裁剪，从而指向另一个
	// 标识。这里拒绝不规范写法，标识与依赖因此使用同一套比较规则（精确匹配）。
	if identifier != t.ID {
		return invalidField(
			"ImplementationPlan",
			field("id"),
			"任务标识不能包含首尾空白",
		)
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
//
// 依赖写法必须与任务标识一致：不接受带首尾空白的引用，也不做裁剪后匹配，避免"看起来
// 指向 A 实际匹配到 B"。
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
		if trimmed != dependency {
			return invalidField(
				"ImplementationPlan",
				field,
				fmt.Sprintf("第 %d 条依赖不能包含首尾空白", index+1),
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
//   - 只接受一个完整的 JSON 对象，允许整体被一对闭合的代码块围栏包裹；对象之后的任何
//     内容（说明文字或第二份 JSON 文档）都拒绝，未闭合围栏或多层围栏不做猜测修复；
//   - 未知字段、重复 Key、必填字段缺失与 null 一律拒绝：这些事实必须在结构化解码之前
//     判定，落到结构体之后已经无法复原；
//   - 解析结果还必须通过 Validate 的领域校验。
//
// 该函数是纯函数：不读数据库、不启动进程。
func ParseRequirementAnalysis(text string) (RequirementAnalysis, error) {
	var analysis RequirementAnalysis
	if err := decodePlanningDocument(
		text,
		"需求分析",
		&analysis,
		requirementAnalysisJSONShape,
	); err != nil {
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
	if err := decodePlanningDocument(
		text,
		"实施方案",
		&plan,
		implementationPlanJSONShape,
	); err != nil {
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

// planningJSONKind 是必填字段的 JSON 形状类别。
type planningJSONKind int

// 规划文档的必填字段只支持三类形状：单个字符串、字符串列表、对象列表。
const (
	planningJSONString planningJSONKind = iota
	planningJSONStringList
	planningJSONObjectList
)

// planningJSONField 是一份规划文档中的必填字段。
type planningJSONField struct {
	// name 是字段名，必须与结构体标签完全一致。
	name string
	// kind 是字段的 JSON 形状。
	kind planningJSONKind
	// element 是对象列表的元素形状，只有 planningJSONObjectList 使用。
	element *planningJSONShape
}

// planningJSONShape 是一份规划文档的必填字段集合。
//
// 形状先于结构体声明存在：字段存在性、null、重复 Key 与文档完整性都在结构化解码之前
// 判定，因此这些事实不能依赖"解码到 Go map 之后再检查"。
type planningJSONShape struct {
	// name 是文档的中文名称，用于错误说明。
	name string
	// fields 是全部必填字段，缺一不可。
	fields []planningJSONField
}

// field 按名称查找字段定义。
func (s planningJSONShape) field(name string) (planningJSONField, bool) {
	for _, candidate := range s.fields {
		if candidate.name == name {
			return candidate, true
		}
	}
	return planningJSONField{}, false
}

// requirementAnalysisJSONShape 描述需求分析文档的八个必填字段。
var requirementAnalysisJSONShape = planningJSONShape{
	name: "需求分析",
	fields: []planningJSONField{
		{name: "summary", kind: planningJSONString},
		{name: "goals", kind: planningJSONStringList},
		{name: "current_state", kind: planningJSONStringList},
		{name: "scope", kind: planningJSONStringList},
		{name: "out_of_scope", kind: planningJSONStringList},
		{name: "constraints", kind: planningJSONStringList},
		{name: "risks", kind: planningJSONStringList},
		{name: "acceptance_criteria", kind: planningJSONStringList},
	},
}

// implementationPlanTaskJSONShape 描述方案中单个任务草案的五个必填字段。
//
// depends_on 同样是必填字段：没有前置依赖时必须显式给出空数组，缺字段与 null 都按
// 结构错误拒绝，而不是被当成"没有依赖"。
var implementationPlanTaskJSONShape = planningJSONShape{
	name: "任务草案",
	fields: []planningJSONField{
		{name: "id", kind: planningJSONString},
		{name: "title", kind: planningJSONString},
		{name: "description", kind: planningJSONString},
		{name: "depends_on", kind: planningJSONStringList},
		{name: "acceptance_criteria", kind: planningJSONStringList},
	},
}

// implementationPlanJSONShape 描述实施方案文档的必填字段。
var implementationPlanJSONShape = planningJSONShape{
	name: "实施方案",
	fields: []planningJSONField{
		{name: "summary", kind: planningJSONString},
		{
			name:    "tasks",
			kind:    planningJSONObjectList,
			element: &implementationPlanTaskJSONShape,
		},
	},
}

// decodePlanningDocument 把一段角色输出解码为单个规划结构。
//
// 解码分两步，顺序不可交换：
//   - 先用 JSON Token 校验文档形状：重复 Key、未知字段、缺失字段、null、类型与多余内容
//     都在这一步拒绝。重复 Key 只能在 Token 层面发现，解到结构体或 map 之后原始信息
//     已经丢失；
//   - 再按结构体解码，并保留 DisallowUnknownFields：字段名写错不会被静默忽略成"字段为空"。
func decodePlanningDocument(
	text, kind string,
	target any,
	shape planningJSONShape,
) error {
	document, err := planningDocument(text, kind)
	if err != nil {
		return err
	}
	if err := validatePlanningJSONDocument(document, shape); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %s不是合法的结构化 JSON: %w", ErrPlanningOutputUnparseable, kind, err)
	}
	// 解码一个完整值之后再读一次：剩余内容无论是什么，都说明模型没有按协议只输出
	// 一份结论，因此整份输出不可用。
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

// validatePlanningJSONDocument 校验文档只包含一个形状符合声明的 JSON 对象。
func validatePlanningJSONDocument(document string, shape planningJSONShape) error {
	decoder := json.NewDecoder(strings.NewReader(document))
	if err := validatePlanningJSONObject(decoder, shape, shape.name); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf(
			"%w: %s输出包含多余内容或第二份 JSON 文档",
			ErrPlanningOutputUnparseable,
			shape.name,
		)
	}
	return nil
}

// validatePlanningJSONObject 校验一个对象的必填字段、字段类型与 Key 唯一性。
//
// path 是对象在文档中的位置说明，用于嵌套任务对象的错误定位。
func validatePlanningJSONObject(
	decoder *json.Decoder,
	shape planningJSONShape,
	path string,
) error {
	token, err := decoder.Token()
	if err != nil {
		return planningJSONSyntaxError(path, err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("%w: %s必须是一个 JSON 对象", ErrPlanningOutputUnparseable, path)
	}
	seen := make(map[string]struct{}, len(shape.fields))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return planningJSONSyntaxError(path, err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("%w: %s包含非法字段名", ErrPlanningOutputUnparseable, path)
		}
		field, known := shape.field(key)
		if !known {
			return fmt.Errorf("%w: %s包含未知字段 %q", ErrPlanningOutputUnparseable, path, key)
		}
		if _, duplicated := seen[key]; duplicated {
			return fmt.Errorf("%w: %s的字段 %q 重复出现", ErrPlanningOutputUnparseable, path, key)
		}
		seen[key] = struct{}{}
		if err := validatePlanningJSONField(decoder, field, path); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return planningJSONSyntaxError(path, err)
	}
	for _, field := range shape.fields {
		if _, present := seen[field.name]; !present {
			return fmt.Errorf(
				"%w: %s缺少必填字段 %q",
				ErrPlanningOutputUnparseable,
				path,
				field.name,
			)
		}
	}
	return nil
}

// validatePlanningJSONField 校验单个必填字段的形状。
func validatePlanningJSONField(
	decoder *json.Decoder,
	field planningJSONField,
	path string,
) error {
	location := path + " 的字段 " + field.name
	token, err := decoder.Token()
	if err != nil {
		return planningJSONSyntaxError(location, err)
	}
	if field.kind == planningJSONString {
		if token == nil {
			return fmt.Errorf("%w: %s不能是 null", ErrPlanningOutputUnparseable, location)
		}
		if _, ok := token.(string); !ok {
			return fmt.Errorf("%w: %s必须是字符串", ErrPlanningOutputUnparseable, location)
		}
		return nil
	}
	if token == nil {
		return fmt.Errorf(
			"%w: %s不能是 null（没有内容时使用空数组）",
			ErrPlanningOutputUnparseable,
			location,
		)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return fmt.Errorf("%w: %s必须是数组", ErrPlanningOutputUnparseable, location)
	}
	index := 0
	for decoder.More() {
		index++
		position := fmt.Sprintf("%s 的第 %d 项", location, index)
		if field.kind == planningJSONStringList {
			if err := validatePlanningJSONString(decoder, position); err != nil {
				return err
			}
			continue
		}
		if err := validatePlanningJSONObject(decoder, *field.element, position); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return planningJSONSyntaxError(location, err)
	}
	return nil
}

// validatePlanningJSONString 校验数组中的单个字符串项。
func validatePlanningJSONString(decoder *json.Decoder, position string) error {
	token, err := decoder.Token()
	if err != nil {
		return planningJSONSyntaxError(position, err)
	}
	if token == nil {
		return fmt.Errorf("%w: %s不能是 null", ErrPlanningOutputUnparseable, position)
	}
	if _, ok := token.(string); !ok {
		return fmt.Errorf("%w: %s必须是字符串", ErrPlanningOutputUnparseable, position)
	}
	return nil
}

// planningJSONSyntaxError 把 JSON 语法错误归入"无法解析"类别。
func planningJSONSyntaxError(location string, err error) error {
	return fmt.Errorf("%w: %s不是合法的 JSON: %w", ErrPlanningOutputUnparseable, location, err)
}

// planningDocument 返回待解码的单份 JSON 文档。
//
// 它只做三项前置判断：非空、长度受限、形状是 JSON 对象。围栏剥离是唯一的宽容处理，
// 因为"整体包在代码块里"不改变结论本身；部分围栏或多层围栏不做猜测，直接拒绝。
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
	document, err := trimPlanningFence(trimmed, kind)
	if err != nil {
		return "", err
	}
	if document == "" {
		return "", fmt.Errorf("%w: %s输出为空", ErrPlanningOutputUnparseable, kind)
	}
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
// 只处理"输出整体被一对 ``` 围栏包裹"这一种情况：围栏必须闭合，围栏内部不得再出现
// 围栏标记。未闭合、部分包裹与多层文档都不做修复尝试，交给调用方按不可解析拒绝。
func trimPlanningFence(text, kind string) (string, error) {
	if !strings.HasPrefix(text, "```") {
		return text, nil
	}
	_, body, found := strings.Cut(text, "\n")
	if !found {
		return "", fmt.Errorf("%w: %s的代码块围栏没有内容", ErrPlanningOutputUnparseable, kind)
	}
	inner := strings.TrimSpace(body)
	if !strings.HasSuffix(inner, "```") {
		return "", fmt.Errorf("%w: %s的代码块围栏没有闭合", ErrPlanningOutputUnparseable, kind)
	}
	inner = strings.TrimSpace(strings.TrimSuffix(inner, "```"))
	if strings.Contains(inner, "```") {
		return "", fmt.Errorf(
			"%w: %s包含多层或部分代码块围栏",
			ErrPlanningOutputUnparseable,
			kind,
		)
	}
	return inner, nil
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
