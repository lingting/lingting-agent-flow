package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件是"能否开启新的执行周期"的只读资格查询。
//
// 它存在的理由只有一个：前端无法只凭任务状态与步骤占用判断服务端的完整资格
// （预算是否耗尽、上一周期是否已收敛、历史尝试的周期归属是否可解释），因此在
// 用户点击之前就会拿到一次 409。这里把应用层**已有**的判定结果如实读出来，
// 而不是在查询里另写一套预算算法。
//
// 硬约束：
//   - 只读：不登记周期、不结束周期、不写事件、不改任务或步骤状态、不领取任务租约、
//     不启动 Agent、不产生任何远程副作用；
//   - 判定复用 `DecideRepairStart`（经 `decideRepairStart`）与
//     `domain.CheckStepPrerequisites`，本文件不复制任何预算或准入规则；
//   - 结论只代表读取时刻的现场：写入口 `OpenRepairCycle` 仍会重新读取并完整校验。

// RepairCycleEligibilityBlocker 是"不能开启新的执行周期"的稳定阻塞类别。
//
// 取值有两个来源，且都只描述**哪一条规则拒绝了它**，不承载文案：
//   - 周期判定给出的类别，直接复用 `domain.RepairFailureClass` 的字符串取值
//     （execution_active / cycle_unresolved / cycle_closed / new_cycle_denied 等）；
//   - 任务与前置步骤这两条入口条件由本文件补充，因为它们发生在周期判定之前，
//     领域判定没有对应的类别。
type RepairCycleEligibilityBlocker string

// 入口条件补充的阻塞类别。
const (
	// RepairCycleBlockerCancelRequested 表示任务已记录取消请求。
	//
	// 与显式重试的 `cancel_requested` 是同一个业务事实，因此沿用同一取值。
	RepairCycleBlockerCancelRequested RepairCycleEligibilityBlocker = "cancel_requested"
	// RepairCycleBlockerTaskStatus 表示任务状态不允许推进步骤。
	RepairCycleBlockerTaskStatus RepairCycleEligibilityBlocker = "task_status_not_executable"
	// RepairCycleBlockerPrerequisite 表示前序步骤尚未全部完成。
	RepairCycleBlockerPrerequisite RepairCycleEligibilityBlocker = "prerequisite_incomplete"
	// RepairCycleBlockerCycleOwnershipUnconfirmed 表示持久化的周期登记与步骤归属不一致。
	//
	// 它不同于 `cycle_unresolved`（历史尝试的结果或归属无法解释）：这里是"读到的周期
	// 记录本身不属于该步骤"。两者都必须拒绝，但原因不同，因此不合并成一个类别。
	RepairCycleBlockerCycleOwnershipUnconfirmed RepairCycleEligibilityBlocker = "cycle_ownership_unconfirmed"
)

// RepairCycleEligibilityRequest 是只读资格查询的输入。
//
// 标识来自路径，调用方不能指定周期、上限、尝试列表或任何执行参数。
type RepairCycleEligibilityRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// StepID 是步骤标识。
	StepID string
}

// RepairCycleEligibilityResult 是"能否开启新的执行周期"的只读结论。
//
// 它同时给出结论与判定所用的持久化事实，使调用方不必自己再算一遍预算。
// `CanOpenCycle` 为 true 只表示**预算作用域可以登记**，不代表可以执行开发、
// 验证已经通过或任务即将成功。
type RepairCycleEligibilityResult struct {
	TaskID domain.ID
	StepID domain.ID
	// TaskStatus / StepStatus 是读取时刻的真实状态，供接口直接回显。
	TaskStatus domain.TaskStatus
	StepStatus domain.StepStatus
	// CanOpenCycle 表示当前现场允许显式开启新的执行周期。
	CanOpenCycle bool
	// Blocking 是阻塞类别列表；允许时为空。
	Blocking []RepairCycleEligibilityBlocker
	// Summary 是面向用户的准确说明，允许时说明"当前资格允许开启"。
	Summary string
	// ActiveCycle 表示是否存在活动执行周期。
	ActiveCycle bool
	// CycleSequence 是判定所用周期的序号；没有任何周期登记时为 0。
	CycleSequence int
	// MaxAttempts 是该周期生效的尝试次数上限（含首轮）。
	MaxAttempts int
	// ConsumedAttempts 是该周期已消耗的尝试数（含首轮）。
	ConsumedAttempts int
	// RemainingAttempts 是该周期剩余可用尝试数。
	RemainingAttempts int
	// Attempts 是该步骤的历史尝试总数，用于说明"已执行过多少轮"。
	Attempts int
}

// RepairCycleEligibility 查询当前是否允许开启新的执行周期。
//
// 判定顺序与写入口 `OpenRepairCycle` 一致：任务准入 → 前序步骤 → 周期判定。
// 任一阶段判定为拒绝时立即返回，因此阻塞类别只描述"第一个真实拦路的条件"，
// 而不是一份穷举清单。
//
// 读取失败、周期判定无法完成等内部异常一律返回错误，不会被伪装成
// `CanOpenCycle=false` 的业务结论。
func (s *StepWorkflowService) RepairCycleEligibility(
	ctx context.Context,
	request RepairCycleEligibilityRequest,
) (RepairCycleEligibilityResult, error) {
	taskID, err := parseWorkflowID("任务", request.TaskID)
	if err != nil {
		return RepairCycleEligibilityResult{}, err
	}
	stepID, err := parseWorkflowID("步骤", request.StepID)
	if err != nil {
		return RepairCycleEligibilityResult{}, err
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return RepairCycleEligibilityResult{}, err
	}
	step, err := s.loadStep(ctx, task, stepID)
	if err != nil {
		return RepairCycleEligibilityResult{}, err
	}
	result := RepairCycleEligibilityResult{
		TaskID:     task.ID,
		StepID:     step.ID,
		TaskStatus: task.Status,
		StepStatus: step.Status,
	}

	// 任务层准入只复用既有事实：取消请求与状态白名单。这里不判断容器、执行节点
	// 或验证配置——它们不决定"能否登记新的预算作用域"。
	if block, summary := repairCycleTaskGate(task); block != "" {
		result.Blocking = append(result.Blocking, block)
		result.Summary = summary
		return result, nil
	}
	// 前序步骤规则只在 domain.CheckStepPrerequisites 中定义一次：这里只把拒绝
	// 映射为阻塞类别，读取失败仍然向上返回错误。
	if err := s.checkPrerequisites(ctx, task, step); err != nil {
		if errors.Is(err, ErrStepExecutionConflict) {
			result.Blocking = append(result.Blocking, RepairCycleBlockerPrerequisite)
			result.Summary = "前序步骤尚未全部完成：" +
				"序号更小的步骤必须处于已完成或已跳过状态，否则不能开启新的执行周期"
			return result, nil
		}
		return RepairCycleEligibilityResult{}, err
	}

	// 复用写入口的判定入口（显式开启新周期分支），不复制预算算法。
	decision, facts, err := s.decideRepairStart(ctx, step, true)
	if err != nil {
		if errors.Is(err, ErrStepWorkflowInvalidConfig) {
			// 周期登记与步骤归属不一致：无法确认预算属于谁，只能拒绝并说明原因。
			result.Blocking = append(
				result.Blocking,
				RepairCycleBlockerCycleOwnershipUnconfirmed,
			)
			result.Summary = "执行周期记录与该步骤的归属不一致，无法确认已消耗预算；" +
				"需要先核验持久化记录，不能据此开启新的执行周期"
			return result, nil
		}
		return RepairCycleEligibilityResult{}, err
	}
	// 预算事实全部来自 domain.DecideRepairBudget 的计算结果。
	result.ActiveCycle = facts.HasCycle
	result.CycleSequence = decision.Budget.CycleSequence
	result.MaxAttempts = decision.Budget.MaxAttempts
	result.ConsumedAttempts = decision.Budget.Consumed
	result.RemainingAttempts = decision.Budget.Remaining()
	result.Attempts = len(facts.Attempts)

	if decision.Allowed && decision.OpenCycle {
		result.CanOpenCycle = true
		// 说明直接沿用领域判定给出的文本（它是"为什么允许"的唯一来源），只补充
		// "开启周期不等于执行"这一条边界，避免调用方把资格读成执行授权。
		result.Summary = decision.Summary + "；该动作只登记新的预算作用域，不会执行开发或触发验证"
		return result, nil
	}
	blocker := RepairCycleEligibilityBlocker(decision.Class)
	if blocker == "" {
		// 判定既不允许也没给出类别属于内部不一致：如实报错（映射为 500）而不是
		// 编造一个业务阻塞原因，也不是伪装成"资格不允许"。
		return RepairCycleEligibilityResult{}, fmt.Errorf(
			"执行周期资格判定缺少阻塞类别（步骤 %s）",
			step.ID,
		)
	}
	result.Blocking = append(result.Blocking, blocker)
	result.Summary = decision.Summary
	return result, nil
}

// repairCycleTaskGate 返回任务层的阻塞类别与说明；允许时为两个空值。
//
// 判定条件与写入口一致：已记录取消请求，或任务状态不允许推进步骤。
func repairCycleTaskGate(task domain.Task) (RepairCycleEligibilityBlocker, string) {
	if task.CancelRequested {
		return RepairCycleBlockerCancelRequested,
			"任务已请求取消：取消请求尚未结束，此时不能开启新的执行周期"
	}
	if !task.Status.AllowsStepExecution() {
		return RepairCycleBlockerTaskStatus,
			fmt.Sprintf(
				"当前任务状态（%s）不允许推进步骤，因此不能开启新的执行周期",
				task.Status,
			)
	}
	return "", ""
}
