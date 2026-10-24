package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 执行周期与修复预算的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。
var (
	// ErrStepWorkflowCycleDenied 表示当前现场不允许开启新的执行周期。
	//
	// 它是状态冲突的一种：上一周期仍有剩余预算、历史现场未收敛或归属不一致都属于
	// 这一类，调用方应返回 409 而不是把请求当成成功。
	ErrStepWorkflowCycleDenied = errors.New("app: 不允许开启新的执行周期")
)

// 修复周期的开启来源标签。
const (
	// repairCycleOpenedByUser 表示周期由用户显式入口开启。
	repairCycleOpenedByUser = "user_reopen"
	// repairCycleOpenedByFirstRun 表示周期由首次执行自动登记。
	//
	// 首轮执行本身就是一个逻辑周期：登记它使后续的恢复、重复调用与预算统计都有
	// 稳定的归属，而"登记首个周期"不构成"重置预算"——此时没有任何历史尝试。
	repairCycleOpenedByFirstRun = "first_run"
)

// RepairCycleStore 是执行周期与预算判定需要的持久化能力。
//
// 它只读取周期登记与尝试历史，并写入周期登记：预算判定本身仍然只在 domain 中实现。
type RepairCycleStore interface {
	storage.RepairCycleStore
	// ListAttemptsByStep 按尝试序号返回步骤的全部历史尝试。
	ListAttemptsByStep(ctx context.Context, stepID domain.ID) ([]domain.Attempt, error)
}

// repairBudgetFacts 是一次预算判定读取到的持久化事实。
type repairBudgetFacts struct {
	// Cycle 是步骤当前的活动周期；HasCycle 为 false 时为空值。
	Cycle    domain.RepairCycle
	HasCycle bool
	// LastCycle 是最近一个已结束的周期；没有任何周期登记时为空值。
	//
	// 周期结束后"上一周期是否已用尽预算"仍必须可判定，因此需要它参与预算计算。
	LastCycle    domain.RepairCycle
	HasLastCycle bool
	// Cycles 是步骤已登记的全部执行周期（含已结束的）。
	Cycles []domain.RepairCycle
	// Budget 是依据历史尝试计算出的预算。
	Budget domain.RepairBudget
	// Attempts 是全部历史尝试（按序号升序）。
	Attempts []domain.Attempt
	// Unresolved 是结果不确定的尝试描述，非空时禁止继续执行。
	Unresolved []string
}

// loadRepairBudgetFacts 读取步骤的周期登记与全部尝试历史，并计算当前周期预算。
//
// 读取失败一律返回错误：预算无法判定时结论必须是"不能确定是否可重试"，
// 而不是"默认还有余额"。该函数不修改任何状态。
func (s *StepWorkflowService) loadRepairBudgetFacts(
	ctx context.Context,
	step domain.Step,
) (repairBudgetFacts, error) {
	attempts, err := s.cycles.ListAttemptsByStep(ctx, step.ID)
	if err != nil {
		return repairBudgetFacts{}, fmt.Errorf("读取执行尝试历史失败: %w", err)
	}
	facts := repairBudgetFacts{Attempts: attempts}
	cycles, cyclesErr := s.cycles.ListRepairCyclesByStep(ctx, step.ID)
	if cyclesErr != nil {
		return repairBudgetFacts{}, fmt.Errorf("读取执行周期历史失败: %w", cyclesErr)
	}
	facts.Cycles = cycles
	for _, stored := range cycles {
		if stored.TaskID != step.TaskID || stored.StepID != step.ID {
			return repairBudgetFacts{}, fmt.Errorf(
				"%w: 执行周期 %s 与步骤 %s 的归属不一致",
				ErrStepWorkflowInvalidConfig,
				stored.ID,
				step.ID,
			)
		}
		if stored.Active() {
			continue
		}
		facts.LastCycle = stored
		facts.HasLastCycle = true
	}
	cycle, cycleErr := s.cycles.GetActiveRepairCycle(ctx, step.ID)
	switch {
	case cycleErr == nil:
		if cycle.TaskID != step.TaskID || cycle.StepID != step.ID {
			// 数据库不声明外键，归属错配只能由应用层识别：拿错周期会让预算统计
			// 落到别的步骤上。
			return repairBudgetFacts{}, fmt.Errorf(
				"%w: 执行周期 %s 与步骤 %s 的归属不一致",
				ErrStepWorkflowInvalidConfig,
				cycle.ID,
				step.ID,
			)
		}
		facts.Cycle = *cycle
		facts.HasCycle = true
	case errors.Is(cycleErr, storage.ErrNotFound):
		// 没有活动周期：首轮执行或历史数据。
	default:
		return repairBudgetFacts{}, fmt.Errorf("读取执行周期失败: %w", cycleErr)
	}
	attemptFacts := make([]domain.RepairAttemptFacts, 0, len(attempts))
	for _, attempt := range attempts {
		attemptFacts = append(attemptFacts, domain.RepairAttemptFacts{
			ID:            attempt.ID,
			Status:        attempt.Status,
			BudgetScopeID: attempt.BudgetScopeID,
		})
	}
	// 预算作用域与 DecideRepairStart 保持一致：有活动周期时用活动周期，
	// 否则用最近一个已结束周期。
	scope, hasScope := facts.Cycle, facts.HasCycle
	if !hasScope && facts.HasLastCycle {
		scope, hasScope = facts.LastCycle, true
	}
	facts.Budget = domain.DecideRepairBudget(domain.RepairBudgetInput{
		StepID:          step.ID,
		StepMaxAttempts: step.MaxAttempts,
		Cycle:           scope,
		HasCycle:        hasScope,
		Attempts:        attemptFacts,
	})
	facts.Unresolved = unresolvedAttemptSummaries(attempts)
	return facts, nil
}

// unresolvedAttemptSummaries 返回结果不确定的尝试描述。
//
// 判定规则只在 domain.RepairAttemptFacts.Unresolved 中定义，这里只负责格式化。
func unresolvedAttemptSummaries(attempts []domain.Attempt) []string {
	summaries := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		facts := domain.RepairAttemptFacts{ID: attempt.ID, Status: attempt.Status}
		if !facts.Unresolved() {
			continue
		}
		summaries = append(summaries, string(attempt.Status)+"（"+attempt.ID.String()+"）")
	}
	return summaries
}

// decideRepairStart 判断是否允许开始一次开发执行。
//
// 判定完全复用 domain.DecideRepairStart：这里只负责把持久化事实与步骤占用状态
// 转换成它的输入，不复制任何预算或准入规则。
func (s *StepWorkflowService) decideRepairStart(
	ctx context.Context,
	step domain.Step,
	openNewCycle bool,
) (domain.RepairStartDecision, repairBudgetFacts, error) {
	facts, err := s.loadRepairBudgetFacts(ctx, step)
	if err != nil {
		return domain.RepairStartDecision{}, repairBudgetFacts{}, err
	}
	attemptFacts := make([]domain.RepairAttemptFacts, 0, len(facts.Attempts))
	for _, attempt := range facts.Attempts {
		attemptFacts = append(attemptFacts, domain.RepairAttemptFacts{
			ID:            attempt.ID,
			Status:        attempt.Status,
			BudgetScopeID: attempt.BudgetScopeID,
		})
	}
	decision := domain.DecideRepairStart(domain.RepairStartInput{
		StepID:          step.ID,
		StepType:        step.Type,
		StepMaxAttempts: step.MaxAttempts,
		Retry:           step.Retry,
		Cycle:           facts.Cycle,
		HasCycle:        facts.HasCycle,
		LastCycle:       facts.LastCycle,
		HasLastCycle:    facts.HasLastCycle,
		Cycles:          facts.Cycles,
		Attempts:        attemptFacts,
		StepOccupied:    step.CurrentAttemptID != nil || !step.Status.CanStartExecution(),
		OpenNewCycle:    openNewCycle,
	})
	return decision, facts, nil
}

// ensureRepairCycle 保证当前执行有一个可用的周期标识，返回该标识。
//
// 语义固定为：
//   - 已有活动周期：直接复用，因此进程重启、HTTP 重试与 workflow 恢复都消耗同一份预算；
//   - 没有活动周期且没有任何历史尝试：登记首个周期（不构成预算重置）；
//   - 没有活动周期但存在历史尝试：返回错误，不允许在归属不明或已用尽预算的现场
//     自动登记新周期——那等于用恢复入口绕过上限。
func (s *StepWorkflowService) ensureRepairCycle(
	ctx context.Context,
	step domain.Step,
	facts repairBudgetFacts,
) (domain.RepairCycle, error) {
	if facts.HasCycle {
		return facts.Cycle, nil
	}
	if len(facts.Attempts) > 0 {
		return domain.RepairCycle{}, fmt.Errorf(
			"%w: 步骤 %s 已有 %d 次历史执行但没有任何执行周期登记，无法确认预算归属；"+
				"请先核验现场并显式开启新的执行周期",
			ErrStepWorkflowCycleDenied,
			step.ID,
			len(facts.Attempts),
		)
	}
	return s.openRepairCycle(ctx, step, repairCycleOpenedByFirstRun)
}

// openRepairCycle 登记一个新的执行周期。
//
// 写入失败直接返回错误：周期没有落库时不能启动开发调用，否则这次执行的预算将
// 无法在重启后被重新计算。
func (s *StepWorkflowService) openRepairCycle(
	ctx context.Context,
	step domain.Step,
	openedBy string,
) (domain.RepairCycle, error) {
	cycle, err := s.cycles.StartRepairCycle(ctx, storage.StartRepairCycleRequest{
		TaskID:      step.TaskID,
		StepID:      step.ID,
		MaxAttempts: effectiveMaxAttempts(step),
		OpenedBy:    openedBy,
		At:          s.now(),
	})
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return domain.RepairCycle{}, fmt.Errorf(
				"%w: 步骤 %s 已存在活动执行周期，请重新读取现场",
				ErrStepWorkflowCycleDenied,
				step.ID,
			)
		}
		return domain.RepairCycle{}, fmt.Errorf("写入执行周期失败: %w", err)
	}
	return cycle, nil
}

// effectiveMaxAttempts 返回步骤生效的尝试次数上限。
//
// 缺省值与最大值只在 domain 中定义：这里只做读取，不复制第二套边界规则。
func effectiveMaxAttempts(step domain.Step) int {
	if step.MaxAttempts < 1 {
		return domain.DefaultMaxAttempts
	}
	if step.MaxAttempts > domain.MaxTaskMaxAttempts {
		return domain.MaxTaskMaxAttempts
	}
	return step.MaxAttempts
}

// OpenRepairCycleRequest 是通过接口显式开启一次执行周期的输入。
//
// 只接受归属与执行参数：调用方不能指定周期标识、上限或历史尝试归属。
type OpenRepairCycleRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// StepID 是步骤标识。
	StepID string
}

// OpenRepairCycleResult 是一次执行周期开启的结果。
//
// 它只描述周期登记与预算事实：开启周期不等于执行成功，也不代表验证会通过。
type OpenRepairCycleResult struct {
	TaskID domain.ID
	StepID domain.ID
	// CycleID 是新登记的执行周期标识。
	CycleID domain.ID
	// CycleSequence 是该步骤内的周期序号。
	CycleSequence int
	// MaxAttempts 是新周期生效的尝试次数上限（含首轮）。
	MaxAttempts int
	// PreviousConsumed 是上一周期已消耗的尝试数，用于说明"为什么允许重置"。
	PreviousConsumed int
	// OpenedAt 是周期开启时间。
	OpenedAt time.Time
}

// OpenRepairCycle 为用户显式开启一个新的执行周期。
//
// 语义固定为：
//   - 只有满足 domain.DecideRepairStart 的新周期分支时才允许开启；
//   - 已安全收敛且（预算已用尽或策略不再允许修复）才获得新预算；
//   - 上一周期仍有剩余预算时拒绝，避免用"开新周期"绕过正常修复上限；
//   - 结果不确定或周期归属不明时拒绝，必须先核验现场。
//
// 该入口不执行任何 Agent 调用：它只登记预算作用域，随后的执行仍走既有入口。
func (s *StepWorkflowService) OpenRepairCycle(
	ctx context.Context,
	request OpenRepairCycleRequest,
) (OpenRepairCycleResult, error) {
	taskID, err := parseWorkflowID("任务", request.TaskID)
	if err != nil {
		return OpenRepairCycleResult{}, err
	}
	stepID, err := parseWorkflowID("步骤", request.StepID)
	if err != nil {
		return OpenRepairCycleResult{}, err
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return OpenRepairCycleResult{}, err
	}
	step, err := s.loadStep(ctx, task, stepID)
	if err != nil {
		return OpenRepairCycleResult{}, err
	}
	if task.CancelRequested || !task.Status.AllowsStepExecution() {
		return OpenRepairCycleResult{}, fmt.Errorf(
			"%w: 任务 %s 当前状态 %s 不允许开启新的执行周期",
			ErrStepExecutionConflict,
			task.ID,
			task.Status,
		)
	}
	if err := s.checkPrerequisites(ctx, task, step); err != nil {
		return OpenRepairCycleResult{}, err
	}
	decision, facts, err := s.decideRepairStart(ctx, step, true)
	if err != nil {
		return OpenRepairCycleResult{}, err
	}
	if !decision.Allowed || !decision.OpenCycle {
		return OpenRepairCycleResult{}, fmt.Errorf(
			"%w: %s",
			ErrStepWorkflowCycleDenied,
			decision.Summary,
		)
	}
	// 上一周期必须显式结束：活动周期不结束就写入新周期会被部分唯一索引拒绝，
	// 这是"同一时刻只有一个活动周期"的落点。
	if facts.HasCycle {
		if err := s.cycles.CloseRepairCycle(ctx, facts.Cycle.ID, s.now()); err != nil {
			return OpenRepairCycleResult{}, fmt.Errorf("结束上一执行周期失败: %w", err)
		}
	}
	cycle, err := s.openRepairCycle(ctx, step, repairCycleOpenedByUser)
	if err != nil {
		return OpenRepairCycleResult{}, err
	}
	s.appendCloseoutEvent(ctx, ExecuteAndValidateResult{
		TaskID: task.ID,
		StepID: step.ID,
	}, domain.EventTypeRepairCycleOpened, map[string]any{
		"cycle_id":          cycle.ID.String(),
		"cycle_sequence":    cycle.Sequence,
		"max_attempts":      cycle.MaxAttempts,
		"previous_consumed": facts.Budget.Consumed,
		"opened_by":         cycle.OpenedBy,
		"repair_class":      string(decision.Class),
		"repair_summary":    decision.Summary,
	})
	return OpenRepairCycleResult{
		TaskID:           task.ID,
		StepID:           step.ID,
		CycleID:          cycle.ID,
		CycleSequence:    cycle.Sequence,
		MaxAttempts:      cycle.MaxAttempts,
		PreviousConsumed: facts.Budget.Consumed,
		OpenedAt:         cycle.OpenedAt,
	}, nil
}
