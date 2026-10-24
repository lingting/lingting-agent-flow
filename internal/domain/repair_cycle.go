package domain

import (
	"fmt"
	"strings"
	"time"
)

// 修复执行周期相关的长度与取值约束。
const (
	// MaxRepairCycleLabel 是修复周期来源标签的长度上限。
	//
	// 标签只用于诊断（例如 "user_reopen"），不参与预算判定。
	MaxRepairCycleLabel = 64
)

// RepairBudgetScope 表示一次开发执行尝试属于哪种预算作用域。
//
// 它是"这一次 Attempt 的预算算在哪一次逻辑执行周期里"的持久化表达：
//   - 同一次逻辑周期的恢复（进程重启、HTTP 重试、重复调用、workflow resume）
//     必须复用同一个周期，因此继续消耗原有预算；
//   - 用户显式开启的新周期必须写入新的周期标识，因此得到一份新预算。
//
// 只有用户显式入口才能创建新周期；任何自动恢复路径都不得改变该作用域。
type RepairBudgetScope string

// 修复预算作用域取值。
const (
	// RepairBudgetScopeNone 表示该尝试不属于任何显式登记的执行周期。
	//
	// 本字段引入之前写入的历史尝试都是这个取值：它们的周期归属无法从持久化
	// 事实确定，因此预算判定必须拒绝自动继续（见 RepairClassCycleUnresolved）。
	RepairBudgetScopeNone RepairBudgetScope = ""
	// RepairBudgetScopeCycle 表示该尝试属于某个已登记的修复执行周期。
	RepairBudgetScopeCycle RepairBudgetScope = "cycle"
)

// Validate 校验修复预算作用域是否属于已知取值。
func (s RepairBudgetScope) Validate() error {
	switch s {
	case RepairBudgetScopeNone, RepairBudgetScopeCycle:
		return nil
	default:
		return invalidField("RepairCycle", "budget_scope", "未知的预算作用域 "+string(s))
	}
}

// RepairCycle 是一次逻辑开发执行周期的持久化登记。
//
// 语义固定为：
//   - 一个 Step 在其生命周期内可以有多个周期，但同一时刻只有一个活动周期；
//   - 周期标识写入 Attempt 的 budget_scope_id，预算只统计同一周期的 Attempt；
//   - 周期只由用户显式入口开启（app.StepWorkflowService.OpenRepairCycle），
//     进程重启、HTTP 重试、workflow resume 都不会创建新周期。
type RepairCycle struct {
	// ID 是周期标识，同时作为 Attempt.BudgetScopeID 的取值。
	ID ID `json:"id"`
	// TaskID / StepID 是周期归属，写入前必须由应用层校验一致。
	TaskID ID `json:"task_id"`
	StepID ID `json:"step_id"`
	// Sequence 是该步骤内的周期序号，从 1 开始。
	Sequence int `json:"sequence"`
	// MaxAttempts 是本周期开启时生效的尝试次数上限（含首轮）。
	//
	// 它是快照而不是引用：之后修改步骤配置不会让已开启的周期获得更多预算。
	MaxAttempts int `json:"max_attempts"`
	// OpenedBy 是开启来源的稳定标签（例如 user_reopen）。
	OpenedBy string `json:"opened_by"`
	// OpenedAt 是开启时间。
	OpenedAt time.Time `json:"opened_at"`
	// ClosedAt 是周期结束时间；为空表示仍是活动周期。
	ClosedAt *time.Time `json:"closed_at,omitempty"`
}

// NewRepairCycle 创建并校验一次执行周期登记。
func NewRepairCycle(
	taskID, stepID ID,
	sequence, maxAttempts int,
	openedBy string,
	at time.Time,
) (RepairCycle, error) {
	cycle := RepairCycle{
		ID:          NewID(),
		TaskID:      taskID,
		StepID:      stepID,
		Sequence:    sequence,
		MaxAttempts: maxAttempts,
		OpenedBy:    strings.TrimSpace(openedBy),
		OpenedAt:    normalizeTime(at),
	}
	if err := cycle.Validate(); err != nil {
		return RepairCycle{}, err
	}
	return cycle, nil
}

// Validate 校验周期登记是否完整。
func (c RepairCycle) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	if err := c.TaskID.Validate(); err != nil {
		return err
	}
	if err := c.StepID.Validate(); err != nil {
		return err
	}
	if c.Sequence < 1 {
		return invalidField("RepairCycle", "sequence", "周期序号必须从 1 开始")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > MaxTaskMaxAttempts {
		return invalidField(
			"RepairCycle",
			"max_attempts",
			fmt.Sprintf("尝试次数上限必须在 1 到 %d 之间", MaxTaskMaxAttempts),
		)
	}
	label := strings.TrimSpace(c.OpenedBy)
	if label == "" {
		return invalidField("RepairCycle", "opened_by", "开启来源不能为空")
	}
	if len(label) > MaxRepairCycleLabel {
		return invalidField("RepairCycle", "opened_by", "开启来源过长")
	}
	if c.OpenedAt.IsZero() {
		return invalidField("RepairCycle", "opened_at", "开启时间不能为空")
	}
	if c.ClosedAt != nil && c.ClosedAt.Before(c.OpenedAt) {
		return invalidField("RepairCycle", "closed_at", "结束时间不能早于开启时间")
	}
	return nil
}

// Active 判断该周期是否仍是活动周期。
func (c RepairCycle) Active() bool {
	return c.ClosedAt == nil
}

// Close 标记周期已结束。
func (c *RepairCycle) Close(at time.Time) error {
	if c == nil {
		return invalidField("RepairCycle", "", "实体为空")
	}
	if c.ClosedAt != nil {
		return nil
	}
	now := normalizeTime(at)
	c.ClosedAt = &now
	return nil
}

// RepairAttemptFacts 是一次 Attempt 对预算判定有意义的持久化事实。
//
// 它刻意只包含"读到的记录本身"，不包含任何派生或推测值：预算判定必须是
// 持久化事实的纯函数，才能跨进程得到一致结论。
type RepairAttemptFacts struct {
	// ID 是尝试标识，仅用于诊断。
	ID ID
	// Status 是尝试的执行状态。
	Status AttemptStatus
	// BudgetScopeID 是该尝试登记的预算作用域标识。
	//
	// 为空表示周期归属无法从持久化事实确定（历史数据）。
	BudgetScopeID ID
	// Scope 是该尝试登记的预算作用域取值；为空表示周期归属无法确定。
	Scope RepairBudgetScope
}

// ConsumesBudget 判断该尝试是否消耗执行周期预算。
//
// 规则：一次真实的开发执行尝试一旦被创建就已经消耗了预算，与它最终如何结束无关。
// 因此所有状态（含 running / interrupted / unknown / recovery_required）都计入，
// 只有 cancelled 例外：用户取消的执行不应挤占后续修复预算。
func (f RepairAttemptFacts) ConsumesBudget() bool {
	if f.Status == AttemptStatusCancelled {
		return false
	}
	return true
}

// Unresolved 判断该尝试是否表示"结果不确定、必须先核验"的现场。
//
// running 不在此列：它由执行占用（Step.CurrentAttemptID + 租约）负责表达，
// 并由占用准入拒绝第二个执行者。
func (f RepairAttemptFacts) Unresolved() bool {
	switch f.Status {
	case AttemptStatusUnknown, AttemptStatusRecoveryRequired, AttemptStatusInterrupted:
		return true
	default:
		return false
	}
}

// RepairBudget 是一次执行周期的预算事实。
//
// 它只由持久化记录计算得出：Consumed 是同一周期内已消耗的尝试数，
// MaxAttempts 是周期开启时的上限快照。
type RepairBudget struct {
	// CycleID 是当前活动周期的标识；为空表示该步骤还没有登记周期。
	CycleID ID
	// CycleSequence 是周期序号，从 1 开始；没有周期时为 0。
	CycleSequence int
	// MaxAttempts 是本周期生效的尝试次数上限（含首轮）。
	MaxAttempts int
	// Consumed 是本周期已消耗的尝试数（含首轮）。
	Consumed int
}

// Remaining 返回本周期剩余可用尝试数。
func (b RepairBudget) Remaining() int {
	remaining := b.limit() - b.Consumed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Exhausted 判断本周期预算是否已用尽。
func (b RepairBudget) Exhausted() bool {
	return b.Remaining() == 0
}

// limit 返回生效的尝试上限。
func (b RepairBudget) limit() int {
	if b.MaxAttempts < 1 {
		return DefaultMaxAttempts
	}
	return b.MaxAttempts
}

// Validate 校验预算事实是否自洽。
func (b RepairBudget) Validate() error {
	if b.Consumed < 0 {
		return invalidField("RepairBudget", "consumed", "已消耗次数不能为负数")
	}
	if b.MaxAttempts > MaxTaskMaxAttempts {
		return invalidField("RepairBudget", "max_attempts", "尝试次数上限超出允许范围")
	}
	if b.CycleSequence < 0 {
		return invalidField("RepairBudget", "cycle_sequence", "周期序号不能为负数")
	}
	return nil
}

// RepairBudgetInput 是计算一次执行周期预算所需的全部持久化事实。
//
// 调用方只负责读取记录，不负责判断"哪些算预算"：规则只在本文件内定义一次。
type RepairBudgetInput struct {
	// StepID 是目标步骤，仅用于诊断。
	StepID ID
	// StepMaxAttempts 是步骤当前配置的尝试次数上限，用于没有登记周期时的兜底。
	StepMaxAttempts int
	// Cycle 是步骤当前的活动周期登记；没有登记时为空值。
	Cycle RepairCycle
	// HasCycle 表示 Cycle 是否有效。
	HasCycle bool
	// Attempts 是该步骤的全部历史尝试，按尝试序号升序。
	Attempts []RepairAttemptFacts
}

// DecideRepairBudget 依据持久化事实计算当前执行周期的预算。
//
// 规则（顺序固定）：
//  1. 存在已登记的活动周期：预算只统计 BudgetScopeID 等于该周期标识的尝试，
//     上限取周期开启时的快照；
//  2. 没有登记周期：预算上限取步骤配置值，只统计没有周期归属的尝试；
//  3. 任何无法确定周期归属的混合情况都不在此处判定——是否允许继续由
//     DecideRepairStart 依据 UnresolvedAttempts 拒绝。
//
// 该函数是纯函数：不读数据库、不启动进程。
func DecideRepairBudget(input RepairBudgetInput) RepairBudget {
	budget := RepairBudget{
		MaxAttempts: input.StepMaxAttempts,
	}
	if input.HasCycle {
		budget.CycleID = input.Cycle.ID
		budget.CycleSequence = input.Cycle.Sequence
		budget.MaxAttempts = input.Cycle.MaxAttempts
	}
	for _, attempt := range input.Attempts {
		if !attempt.ConsumesBudget() {
			continue
		}
		if attempt.BudgetScopeID != budget.CycleID {
			// 其他周期的尝试不消耗本周期预算：这是"新周期获得新预算"的落点。
			continue
		}
		budget.Consumed++
	}
	return budget
}

// RepairStartInput 是判断"是否允许开始一次开发执行"所需的全部持久化事实。
//
// 它只包含持久化事实：步骤配置、当前周期登记、历史尝试记录、步骤占用状态与
// 调用方是否显式要求开启新周期。判定不依赖自然语言，也不读取数据库。
type RepairStartInput struct {
	// StepID 是目标步骤，仅用于诊断与说明文本。
	StepID ID
	// StepType 是步骤类型：自动修复只适用于开发步骤。
	StepType StepType
	// StepMaxAttempts 是步骤配置的尝试次数上限。
	StepMaxAttempts int
	// Retry 是步骤的重试策略。
	Retry RetryConfig
	// Cycle 是步骤当前的活动周期登记；没有登记时为空值。
	Cycle RepairCycle
	// HasCycle 表示 Cycle 是否有效。
	HasCycle bool
	// LastCycle 是最近一个已结束的周期登记；没有任何周期登记时为空值。
	//
	// 周期已结束时预算必须按它计算，否则"上一周期是否还有余额"会退化成按步骤
	// 配置重新计算，使"用尽预算后开启新周期"这条规则永远无法成立。
	LastCycle RepairCycle
	// HasLastCycle 表示 LastCycle 是否有效。
	HasLastCycle bool
	// Cycles 是该步骤已登记的全部执行周期（含已结束的）。
	//
	// 它用于判定"某次尝试引用的周期是否真实存在"：引用了不存在的周期说明周期
	// 记录缺失，归属无法解释，必须先核验。
	Cycles []RepairCycle
	// Attempts 是该步骤的全部历史尝试，按尝试序号升序。
	Attempts []RepairAttemptFacts
	// StepOccupied 表示步骤当前被一次执行占用（running / developing / verifying）。
	StepOccupied bool
	// OpenNewCycle 表示调用方显式要求开启新的执行周期。
	OpenNewCycle bool
}

// RepairStartDecision 是"是否允许开始一次开发执行"的结论。
type RepairStartDecision struct {
	// Allowed 表示是否允许开始。
	Allowed bool
	// Class 是不允许开始时的原因类别；允许时为空。
	Class RepairFailureClass
	// Summary 是面向用户的原因说明。
	Summary string
	// Budget 是判定时使用的预算事实。
	Budget RepairBudget
	// OpenCycle 表示本次判定要求开启一个新的执行周期。
	OpenCycle bool
}

// RepairCycleDecision 判断本次判定是否要求开启新周期。
func (d RepairStartDecision) RepairCycleDecision() bool {
	return d.OpenCycle
}

// DecideRepairStart 判断是否允许开始一次开发执行（含开启新的执行周期）。
//
// 规则（顺序固定，不可被调用方意愿或 Agent 结论覆盖）：
//  1. 步骤仍被一次执行占用：停止（execution_active），不得启动第二个执行者；
//  2. 存在结果不确定的历史尝试（unknown / recovery_required / interrupted）：
//     停止（cycle_unresolved），必须先核验现场，不能直接消耗下一轮预算；
//  3. 没有活动周期但已有历史尝试：上一周期已结束，停止（cycle_closed），
//     继续执行必须由用户显式开启新的执行周期；
//  4. 显式要求开启新周期：
//     a. 存在无法确定周期归属的历史尝试：停止（cycle_unresolved）；
//     b. 历史尝试的周期归属混合（既有当前周期又有其他周期）：停止（cycle_unresolved）；
//     c. 上一周期仍有剩余预算且策略仍允许修复：停止（new_cycle_denied），
//     不允许用"开新周期"绕过正常修复预算；
//     d. 允许开启新周期（OpenCycle = true）；
//  5. 未要求开启新周期：当前周期仍有剩余预算时允许开始（retry）；
//     否则停止（attempts_exhausted）。
//
// 注意：本函数只判断"能否开始一次开发执行"，不判断步骤类型。规划步骤同样需要
// 开始执行（它只是不允许自动修复，该限制由 DecideRepairLoop 表达）。
//
// 该函数是纯函数：不读数据库、不启动进程，因此可以被完整地单元测试。
func DecideRepairStart(input RepairStartInput) RepairStartDecision {
	// 预算的作用域是"当前活动周期"，没有活动周期时是"最近一个已结束周期"：
	// 后者使"上一周期是否已用尽预算"在周期结束后仍然可判定。
	scope, hasScope := input.Cycle, input.HasCycle
	if !hasScope && input.HasLastCycle {
		scope, hasScope = input.LastCycle, true
	}
	budget := DecideRepairBudget(RepairBudgetInput{
		StepID:          input.StepID,
		StepMaxAttempts: input.StepMaxAttempts,
		Cycle:           scope,
		HasCycle:        hasScope,
		Attempts:        input.Attempts,
	})
	decision := RepairStartDecision{Budget: budget}
	if input.StepOccupied {
		decision.Class = RepairClassExecutionActive
		decision.Summary = "步骤仍被一次执行占用，不能启动第二个执行者"
		return decision
	}
	if unresolved := unresolvedAttemptsOf(input.Attempts); len(unresolved) > 0 {
		decision.Class = RepairClassCycleUnresolved
		decision.Summary = "存在结果不确定的历史尝试（" + strings.Join(unresolved, "、") +
			"），必须先核验现场后才能继续执行"
		return decision
	}
	if !input.HasCycle && len(input.Attempts) > 0 && !input.OpenNewCycle {
		decision.Class = RepairClassCycleClosed
		decision.Summary = "上一执行周期已结束（已用 " + itoa(len(input.Attempts)) +
			" 次尝试），继续执行必须显式开启新的执行周期"
		return decision
	}
	if hasUnknownCycleOwnership(input.Attempts, input.Cycles) {
		// 既有登记了周期的尝试，又有周期归属不明的尝试：任何统计口径都会漏算或
		// 多算预算，因此必须先人工核验，不能猜测。
		decision.Class = RepairClassCycleUnresolved
		decision.Summary = "历史尝试中同时存在已登记周期与归属不明的记录，无法确认已消耗预算"
		return decision
	}
	if input.OpenNewCycle {
		return decideNewCycle(input, decision)
	}
	if budget.Exhausted() {
		decision.Class = RepairClassAttemptsExhausted
		decision.Summary = "已达到尝试次数上限 " + itoa(budget.limit()) +
			"（共 " + itoa(budget.Consumed) + " 次开发尝试），停止自动修复"
		return decision
	}
	decision.Allowed = true
	decision.Summary = "仍在上限内，进入第 " + itoa(budget.Consumed+1) + " 次开发尝试"
	return decision
}

// decideNewCycle 判断是否允许开启新的执行周期。
//
// 它是 DecideRepairStart 的新周期分支，单独成函数只是为了让规则顺序可读；
// 判定仍然只有这一处实现。
func decideNewCycle(input RepairStartInput, decision RepairStartDecision) RepairStartDecision {
	if len(input.Attempts) == 0 {
		// 从未执行过的步骤没有"新周期"可言：首轮执行本身就是第一个周期。
		decision.Class = RepairClassNewCycleDenied
		decision.Summary = "该步骤还没有任何执行记录，首轮执行本身就是第一个执行周期"
		return decision
	}
	// 全部历史尝试都没有周期归属（本字段引入之前的数据）属于单一隐式周期：
	// 它不算"归属不明"，但也只有在其预算确实用尽或策略不再允许修复时，才允许
	// 用户开启新的显式周期获得新预算。
	if !decision.Budget.Exhausted() && input.Retry.Allows(RetryClassVerificationFailure) {
		decision.Class = RepairClassNewCycleDenied
		decision.Summary = "当前执行周期仍有剩余预算（已用 " + itoa(decision.Budget.Consumed) +
			" / " + itoa(decision.Budget.limit()) + "），不允许通过开启新周期重置预算"
		return decision
	}
	decision.Allowed = true
	decision.OpenCycle = true
	decision.Summary = "允许开启新的执行周期：上一周期已用 " + itoa(decision.Budget.Consumed) +
		" 次尝试，新周期上限 " + itoa(input.StepMaxAttempts)
	return decision
}

// unresolvedAttemptsOf 返回结果不确定的尝试描述，用于说明文本与判定。
func unresolvedAttemptsOf(attempts []RepairAttemptFacts) []string {
	descriptions := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		if !attempt.Unresolved() {
			continue
		}
		descriptions = append(descriptions, string(attempt.Status)+"（"+attempt.ID.String()+"）")
	}
	return descriptions
}

// hasUnknownCycleOwnership 判断是否有尝试引用了未登记的周期。
//
// 判据只有一条持久化事实：尝试记录了周期标识，但对应周期登记不存在。此时"这次
// 尝试消耗了哪一次周期的预算"无法解释，必须先核验，不能猜测。
//
// 没有周期标识的历史尝试（本字段引入之前的数据）不属于这一类：它们属于隐式周期，
// 由 DecideRepairBudget 的"无周期"分支统一统计，不会被重复计入任何显式周期。
func hasUnknownCycleOwnership(attempts []RepairAttemptFacts, cycles []RepairCycle) bool {
	known := make(map[ID]struct{}, len(cycles))
	for _, cycle := range cycles {
		known[cycle.ID] = struct{}{}
	}
	for _, attempt := range attempts {
		if attempt.BudgetScopeID.IsEmpty() {
			continue
		}
		if _, ok := known[attempt.BudgetScopeID]; !ok {
			return true
		}
	}
	return false
}
