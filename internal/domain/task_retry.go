package domain

import (
	"fmt"
	"strings"
	"time"
)

// 本文件定义"用户显式重试失败任务"的准入规则。
//
// 为什么需要独立规则而不是复用自动调度或启动准入：
//   - 自动调度（`AllowsAutomaticScheduling`）只接受 `queued`，它回答的是"调度器可以
//     自行领取谁"，而重试是用户的显式决定；
//   - 启动准入（`CheckTaskLaunchStart`）不接受 `failed`，它回答的是"已经在执行流程里的
//     任务能否继续推进"，而重试要把一个已结束的任务重新拉回执行流程。
//
// 两者都不能表达"这一次是用户在核验过现场之后主动开启新执行周期"。因此重试资格必须
// 单独定义，并且必须比两者都严格：只有在**执行权已失效、执行结论已确定、没有未核验的
// 外部副作用**时才允许重试。仅凭 `status == failed` 不足以判定，因为失败可能来自
// "结果无法确认"的路径，也可能留下未决的 commit / push / merge。

// TaskRetryRejection 是任务不能进入显式重试的稳定原因分类。
//
// 取值只描述"哪一条规则拒绝了它"，不描述"应该怎么修"。接口字段与事件 payload 直接
// 使用这里的稳定字符串，不解析错误文本。
type TaskRetryRejection string

// 显式重试的拒绝原因取值。
const (
	// TaskRetryRejectionDeleted 表示任务已被逻辑删除。
	TaskRetryRejectionDeleted TaskRetryRejection = "task_deleted"
	// TaskRetryRejectionCancelRequested 表示任务已记录取消请求。
	TaskRetryRejectionCancelRequested TaskRetryRejection = "cancel_requested"
	// TaskRetryRejectionStatusNotRetryable 表示任务状态不属于允许显式重试的集合。
	TaskRetryRejectionStatusNotRetryable TaskRetryRejection = "status_not_retryable"
	// TaskRetryRejectionLeaseHeld 表示任务租约仍被另一个有效持有者持有。
	TaskRetryRejectionLeaseHeld TaskRetryRejection = "lease_held"
	// TaskRetryRejectionActiveWorkflowRun 表示任务仍存在未结束的工作流执行记录。
	TaskRetryRejectionActiveWorkflowRun TaskRetryRejection = "active_workflow_run"
	// TaskRetryRejectionStepOccupied 表示仍有步骤被一次执行占用。
	TaskRetryRejectionStepOccupied TaskRetryRejection = "step_occupied"
	// TaskRetryRejectionRecoveryResidue 表示存在结果不确定、必须先核验的步骤。
	TaskRetryRejectionRecoveryResidue TaskRetryRejection = "recovery_residue"
	// TaskRetryRejectionSideEffectPending 表示仍有未核验的外部副作用。
	TaskRetryRejectionSideEffectPending TaskRetryRejection = "external_side_effect_pending"
	// TaskRetryRejectionPrerequisiteMissing 表示启动所需的前置条件未满足。
	TaskRetryRejectionPrerequisiteMissing TaskRetryRejection = "prerequisite_missing"
	// TaskRetryRejectionNothingToRetry 表示计划中已经没有需要重新执行的步骤。
	TaskRetryRejectionNothingToRetry TaskRetryRejection = "nothing_to_retry"
)

// TaskRetryInput 是判定"该任务能否被显式重试"所需的持久化事实。
//
// 每一项都对应一条真实记录，不使用推测值：ActiveWorkflowRun 为空表示确实没有未结束的
// 执行记录，而不是"暂时没查"；读取失败必须由调用方上抛，不得当作"没有记录"。
type TaskRetryInput struct {
	// Task 是任务记录。
	Task Task
	// ActiveWorkflowRun 是任务当前未结束的工作流执行记录，没有时为空。
	ActiveWorkflowRun *WorkflowRun
	// Steps 是任务已持久化的计划步骤。
	Steps []Step
	// CodeSaveRecords 是该任务的代码保存记录，用于判定是否仍有未核验的外部副作用。
	CodeSaveRecords []CodeSaveRecord
	// Now 是判定租约与占用是否仍然有效的时刻。
	Now time.Time
	// Owner 是本次判定的执行者标识，可为空。
	//
	// 非空时"租约仍由本人持有"不算阻塞：调用方可能在取得执行权之后再次判定现场。
	// 空值表示调用方还没有执行权，此时任何有效租约都属于他人。
	Owner string
}

// TaskRetryDecision 是显式重试的准入结论。
type TaskRetryDecision struct {
	// Allowed 表示允许开启一次新的执行周期。
	Allowed bool
	// Next 是重试时任务要迁移到的状态。
	//
	// 固定为 preparing：它是状态机中 failed 唯一能进入的执行阶段，也表达"本次是一次
	// 新的执行周期的起点"。真正的下一步（复用容器 / 初始化工作区 / 生成计划 / 启动
	// 工作流）仍由 DecideTaskLaunchNext 依据持久化事实决定，不在这里复制。
	Next TaskStatus
	// TaskStatus 是判定时的任务状态，供只读接口直接回显。
	TaskStatus TaskStatus
	// Rejections 是全部拒绝原因，按固定顺序收集，便于诊断时一次看全。
	Rejections []TaskRetryRejection
	// Prerequisite 是前置条件未满足时的原始错误，供日志与接口映射使用。
	Prerequisite error
	// Summary 是可读说明，允许时说明将要做什么。
	Summary string
}

// Reject 判断结论是否因指定原因被拒绝。
func (d TaskRetryDecision) Reject(reason TaskRetryRejection) bool {
	for _, candidate := range d.Rejections {
		if candidate == reason {
			return true
		}
	}
	return false
}

// SummaryText 返回可读的拒绝或准入说明。
func (d TaskRetryDecision) SummaryText() string {
	if d.Summary != "" {
		return d.Summary
	}
	if d.Allowed || len(d.Rejections) == 0 {
		return ""
	}
	parts := make([]string, 0, len(d.Rejections))
	for _, reason := range d.Rejections {
		parts = append(parts, string(reason))
	}
	return "任务不能重试：" + strings.Join(parts, "、")
}

// AllowsExplicitRetry 判断任务状态是否允许用户显式发起新一轮执行。
//
// 当前只开放 failed：其余"已停止但未终结"的状态（paused / recovery_required）已经
// 有显式入口（`POST .../start` 与 `resume-workflow`），它们的现场尚未被判定为失败，
// 重试语义与"继续推进"相同，因此不需要第二套入口。
//
// blocked 是执行记录状态而不是任务状态：任务层此时通常仍是 developing，继续推进走
// `resume-workflow`，不由重试入口承担。
func (s TaskStatus) AllowsExplicitRetry() bool {
	return s == TaskStatusFailed
}

// DecideTaskRetryEligibility 判断任务能否被用户显式重试。
//
// 规则顺序固定，全部拒绝原因都会被收集（不短路），因为用户需要一次看到所有阻塞项：
//  1. 已被逻辑删除 → 拒绝；
//  2. 已请求取消 → 拒绝（取消意图优先于任何推进）；
//  3. 状态不属于 AllowsExplicitRetry → 拒绝；
//  4. 任务租约仍被有效持有 → 拒绝（执行权没失效就不能重试，否则会出现两个执行者）；
//  5. 存在未结束的工作流执行记录 → 拒绝（无法证明旧执行者已经停止）；
//  6. 存在仍被占用的步骤 → 拒绝（旧执行者可能仍在推进）；
//  7. 存在结果不确定的步骤 → 拒绝（P2 门控：必须先核验现场）；
//  8. 仍有未核验的外部副作用 → 拒绝（P2 门控，复用 DecideTaskCodeSaveResume）；
//  9. 启动前置条件未满足 → 拒绝（复用既有启动前置校验，不重复实现）；
//  10. 计划中已经没有需要重新执行的步骤 → 拒绝（重试不会产生任何新执行）。
//
// 该函数是纯函数：不读数据库、不写状态、不产生任何远程副作用。调用方不得在别处复制
// 同一套判定，否则"failed 任务能否重试"会出现第二份实现。
func DecideTaskRetryEligibility(input TaskRetryInput) TaskRetryDecision {
	decision := TaskRetryDecision{
		Next:       TaskStatusPreparing,
		TaskStatus: input.Task.Status,
	}
	task := input.Task
	if task.Deleted() {
		decision.Rejections = append(decision.Rejections, TaskRetryRejectionDeleted)
	}
	if task.CancelRequested {
		decision.Rejections = append(decision.Rejections, TaskRetryRejectionCancelRequested)
	}
	if !task.Status.AllowsExplicitRetry() {
		decision.Rejections = append(decision.Rejections, TaskRetryRejectionStatusNotRetryable)
	}
	if leaseHeldByOther(task, input.Owner, input.Now) {
		// 有效租约说明仍有其他执行者：接管会在未知现场上重放外部操作。
		decision.Rejections = append(decision.Rejections, TaskRetryRejectionLeaseHeld)
	}
	if input.ActiveWorkflowRun != nil {
		decision.Rejections = append(
			decision.Rejections,
			TaskRetryRejectionActiveWorkflowRun,
		)
	}
	if reason, ok := retryStepRejection(input.Steps); ok {
		decision.Rejections = append(decision.Rejections, reason)
	}
	if pendingSideEffect(input.CodeSaveRecords) {
		decision.Rejections = append(decision.Rejections, TaskRetryRejectionSideEffectPending)
	}
	if err := checkTaskLaunchPrerequisites(task); err != nil {
		decision.Prerequisite = err
		decision.Rejections = append(decision.Rejections, TaskRetryRejectionPrerequisiteMissing)
	}
	if !hasPendingExecutableStep(input.Steps) {
		decision.Rejections = append(decision.Rejections, TaskRetryRejectionNothingToRetry)
	}
	decision.Allowed = len(decision.Rejections) == 0
	if decision.Allowed {
		decision.Summary = "允许开启一次新的执行周期：任务将回到 preparing，" +
			"执行权由新的任务租约代次持有，历史执行记录保持不变"
	}
	return decision
}

// leaseHeldByOther 判断任务租约是否仍被其他执行者有效持有。
//
// 判定分两种情况：调用方尚未持有执行权时，任何有效租约都属于他人；调用方已持有
// 执行权时，只有 owner 不同才算"他人"。代次不参与判定——同一 owner 的不同代次仍
// 代表同一次领取流程，而代次正确性由存储层的条件更新保证。
func leaseHeldByOther(task Task, owner string, at time.Time) bool {
	if !task.LeaseActive(at) {
		return false
	}
	trimmed := strings.TrimSpace(owner)
	if trimmed == "" {
		return true
	}
	return task.LeaseOwner != trimmed
}

// retryStepRejection 返回步骤层面的重试拒绝原因。
//
// 与队列准入的区别：重试允许 failed 步骤（它们正是需要重新执行的），但不允许仍被
// 占用的步骤与结果不确定的步骤。
func retryStepRejection(steps []Step) (TaskRetryRejection, bool) {
	for _, step := range steps {
		if step.Status == StepStatusRecoveryRequired {
			return TaskRetryRejectionRecoveryResidue, true
		}
	}
	for _, step := range steps {
		switch step.Status {
		case StepStatusRunning, StepStatusDeveloping, StepStatusVerifying:
			return TaskRetryRejectionStepOccupied, true
		}
	}
	return "", false
}

// hasPendingExecutableStep 判断计划中是否还有需要重新执行的可推进步骤。
//
// 全部步骤都已完成或已取消时重试没有意义：它只会创建一个不产生任何新执行的周期。
func hasPendingExecutableStep(steps []Step) bool {
	for _, step := range steps {
		if step.Type != StepTypePlan && step.Type != StepTypeDevelop {
			continue
		}
		if step.Status.IsTerminal() {
			continue
		}
		return true
	}
	return false
}

// TaskRetrySummary 返回重试准入被拒绝时的可读说明。
func TaskRetrySummary(decision TaskRetryDecision) string {
	if decision.Allowed {
		return decision.Summary
	}
	detail := decision.SummaryText()
	if decision.Prerequisite != nil {
		detail += "；" + decision.Prerequisite.Error()
	}
	return detail
}

// ReopenForRetry 把一个失败任务重新打开为一次新的执行周期。
//
// 语义固定为：
//   - 只有 failed 允许重开，且必须没有取消请求（取消意图优先于重试）；
//   - 状态迁移仍走状态机（failed → preparing），不直接赋值；
//   - 清空上一周期的最终结论与失败摘要：否则新周期会带着 succeeded / failed 的结论
//     进入执行，任务 API 会把它展示成"已结束但仍在执行"；
//   - 清空当前步骤与尝试占用：它们属于上一个周期，残留会让新周期无法占用步骤。
//
// 本方法不改变任务标识，也不删除任何历史记录：执行代次由新的任务租约（`lease_epoch`）
// 与新的 WorkflowRun / Attempt / Session 表达，历史 Attempt 与 Session 全部保留。
func (t *Task) ReopenForRetry(at time.Time) error {
	if t == nil {
		return invalidField("Task", "", "实体为空")
	}
	if t.Deleted() {
		return fmt.Errorf("%w: 已删除的任务不能重试", ErrInvalidState)
	}
	if t.Status != TaskStatusFailed {
		return fmt.Errorf(
			"%w: 只有失败的任务可以显式重试，当前状态 %s",
			ErrInvalidState,
			t.Status,
		)
	}
	if t.CancelRequested {
		return fmt.Errorf("%w: 已请求取消的任务不能重试", ErrInvalidState)
	}
	now := normalizeTime(at)
	if err := t.Transition(TaskStatusPreparing, now); err != nil {
		return err
	}
	t.FinalResult = TaskResultNone
	t.FailureSummary = ""
	t.CurrentStepID = nil
	t.CurrentAttemptID = nil
	return nil
}
