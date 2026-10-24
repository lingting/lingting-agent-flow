package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 本文件定义任务队列、领取、调度名额与执行权交接的业务语义。
//
// 它只包含纯类型与纯函数：不读数据库、不启动进程、不创建 goroutine、不涉及 HTTP
// 与后台服务生命周期。真正的原子领取、调度循环与名额控制属于应用层与存储层
// （P3-M2），本文件只回答"谁符合条件""谁排在前面""谁还持有执行权"。
//
// 与既有规则的关系（不得重复定义同一业务决策）：
//   - 自动调度资格（本文件）严格强于启动资格（CheckTaskLaunchStart）：通过本文件
//     判定后仍然必须通过 CheckTaskLaunchStart，反向不成立；
//   - TaskStatus.CanStart 表达"允许工作流引擎推进"，不是队列准入规则，本文件不复用
//     也不修改它；
//   - 执行记录层的执行权仍由 WorkflowRun 的状态机与 LeaseActive 表达，本文件只在
//     交接与名额归属处引用它们，不重新实现。

// TaskQueueRejection 是任务不能进入自动调度或不能领取的稳定原因分类。
//
// 取值只描述"哪一条规则拒绝了它"，不描述"应该怎么修"。诊断日志、事件 payload 与
// 接口字段直接使用这里的稳定字符串，不解析错误文本。
type TaskQueueRejection string

// 队列准入与领取的拒绝原因取值。
const (
	// TaskQueueRejectionDeleted 表示任务已被逻辑删除。
	TaskQueueRejectionDeleted TaskQueueRejection = "task_deleted"
	// TaskQueueRejectionCancelRequested 表示任务已记录取消请求。
	TaskQueueRejectionCancelRequested TaskQueueRejection = "cancel_requested"
	// TaskQueueRejectionStatusNotQueueable 表示任务状态不属于自动调度集合。
	TaskQueueRejectionStatusNotQueueable TaskQueueRejection = "status_not_queueable"
	// TaskQueueRejectionLeaseHeld 表示任务租约仍被另一个有效持有者持有。
	TaskQueueRejectionLeaseHeld TaskQueueRejection = "lease_held"
	// TaskQueueRejectionActiveWorkflowRun 表示任务存在未结束的工作流执行记录。
	TaskQueueRejectionActiveWorkflowRun TaskQueueRejection = "active_workflow_run"
	// TaskQueueRejectionStepOccupied 表示存在仍被占用的步骤。
	TaskQueueRejectionStepOccupied TaskQueueRejection = "step_occupied"
	// TaskQueueRejectionRecoveryResidue 表示存在结果不确定、必须先核验的步骤。
	TaskQueueRejectionRecoveryResidue TaskQueueRejection = "recovery_residue"
	// TaskQueueRejectionSideEffectPending 表示仍有未核验的外部副作用。
	TaskQueueRejectionSideEffectPending TaskQueueRejection = "external_side_effect_pending"
	// TaskQueueRejectionPriorityUnknown 表示需求优先级无法解析，排序规则不成立。
	TaskQueueRejectionPriorityUnknown TaskQueueRejection = "priority_unknown"
	// TaskQueueRejectionPrerequisiteMissing 表示启动所需的前置条件未满足。
	TaskQueueRejectionPrerequisiteMissing TaskQueueRejection = "prerequisite_missing"
)

// AllowsAutomaticScheduling 判断任务状态是否属于自动调度的候选状态集合。
//
// 该集合只有一个取值 queued，与 TaskStatus.CanStart 有意不同：CanStart 表达
// "允许工作流引擎推进"（含 failed / paused / recovery_required 与进程中断后残留的
// 中间状态），这些状态只能由用户显式决定或由 P2 的恢复流程处理。若把它们纳入
// 自动调度，调度器就会在未经核验的现场上重放 Agent、Clone、commit、push 或 merge，
// 直接绕过 P2 建立的恢复门控。
//
// 自动调度与用户显式启动的职责边界因此固定为：
//   - 自动调度只领取 queued：从未开始执行的新任务；
//   - preparing / planned / retrying / paused / recovery_required 只能经
//     TaskLaunchService.Launch（用户显式入口）重新推进；
//   - failed 目前没有任何自动入口，用户显式重试需要独立入口（P3-M2 之后决定）。
func (s TaskStatus) AllowsAutomaticScheduling() bool {
	return s == TaskStatusQueued
}

// TaskQueueInput 是判定"该任务能否进入自动调度"所需的持久化事实。
//
// 每一项都对应一条真实记录，不使用推测值：Requirement 为空表示优先级真的缺失，
// ActiveWorkflowRun 为空表示真的没有未结束的执行记录，而不是"暂时没查"。
type TaskQueueInput struct {
	// Task 是任务记录。
	Task Task
	// Requirement 是任务所属需求，提供队列排序使用的优先级。
	Requirement *Requirement
	// ActiveWorkflowRun 是任务当前未结束的工作流执行记录，没有时为空。
	ActiveWorkflowRun *WorkflowRun
	// Steps 是任务已持久化的计划步骤。
	Steps []Step
	// CodeSaveRecords 是该任务的代码保存记录，用于判定是否仍有未核验的外部副作用。
	CodeSaveRecords []CodeSaveRecord
	// Now 是判定租约与占用是否仍然有效的时刻。
	Now time.Time
}

// TaskQueueAdmission 是自动调度准入结论。
type TaskQueueAdmission struct {
	// Admitted 表示该任务可以作为自动调度候选。
	Admitted bool
	// Rejections 是全部拒绝原因，按固定顺序收集，便于诊断时一次看全。
	Rejections []TaskQueueRejection
	// Prerequisite 是启动前置条件未满足时的原始错误，供日志与接口映射使用。
	Prerequisite error
}

// Reject 判断准入结论是否因指定原因被拒绝。
func (a TaskQueueAdmission) Reject(reason TaskQueueRejection) bool {
	for _, candidate := range a.Rejections {
		if candidate == reason {
			return true
		}
	}
	return false
}

// Summary 返回可读的拒绝说明，准入通过时为空。
func (a TaskQueueAdmission) Summary() string {
	if a.Admitted || len(a.Rejections) == 0 {
		return ""
	}
	parts := make([]string, 0, len(a.Rejections))
	for _, reason := range a.Rejections {
		parts = append(parts, string(reason))
	}
	return "任务不能进入自动调度：" + strings.Join(parts, "、")
}

// DecideTaskQueueAdmission 判断任务是否可以作为自动调度候选。
//
// 规则顺序固定，全部拒绝原因都会被收集（不短路），因为调度扫描需要一次看到
// 一条任务被排除的全部理由：
//  1. 已被逻辑删除 → 拒绝；
//  2. 已请求取消 → 拒绝（取消意图跨层生效，调度不得抢先推进）；
//  3. 状态不属于 AllowsAutomaticScheduling 集合 → 拒绝；
//  4. 需求优先级无法解析 → 拒绝（排序规则不成立时不得默认 normal）；
//  5. 存在未结束的工作流执行记录 → 拒绝；
//  6. 存在仍被占用的步骤 → 拒绝；
//  7. 存在结果不确定、必须先核验的步骤 → 拒绝（P2 门控）；
//  8. 仍有未核验的外部副作用 → 拒绝（P2 门控，复用 DecideTaskCodeSaveResume）；
//  9. 启动前置条件未满足 → 拒绝（复用 CheckTaskLaunchStart，不重复实现）。
//
// 租约归属不参与准入：它属于领取仲裁（DecideTaskClaim）。业务准入回答"该任务是否
// 属于队列候选池"，领取仲裁回答"这一次由谁真正拿到执行权"，两者混在一起会让
// "同一执行者重复领取"（租约由他自己持有）无法表达。
//
// 该函数是纯函数：不读数据库、不写状态、不产生任何远程副作用。调用方不得在别处
// 复制同一套准入判定，否则"谁能被调度"会出现第二份实现。
func DecideTaskQueueAdmission(input TaskQueueInput) TaskQueueAdmission {
	admission := TaskQueueAdmission{}
	task := input.Task
	if task.Deleted() {
		admission.Rejections = append(admission.Rejections, TaskQueueRejectionDeleted)
	}
	if task.CancelRequested {
		admission.Rejections = append(admission.Rejections, TaskQueueRejectionCancelRequested)
	}
	if !task.Status.AllowsAutomaticScheduling() {
		admission.Rejections = append(
			admission.Rejections,
			TaskQueueRejectionStatusNotQueueable,
		)
	}
	if err := checkQueuePriority(input.Requirement); err != nil {
		admission.Rejections = append(admission.Rejections, TaskQueueRejectionPriorityUnknown)
	}
	// 租约持有不在这里判定：它是领取仲裁（DecideTaskClaim）的职责。准入回答
	// "这个任务是否属于队列候选池"，仲裁回答"这一次由谁真正拿到执行权"。
	// 把租约混进准入会让"同一执行者重复领取"无法表达（租约由他自己持有）。
	if input.ActiveWorkflowRun != nil {
		admission.Rejections = append(
			admission.Rejections,
			TaskQueueRejectionActiveWorkflowRun,
		)
	}
	if reason, ok := queueStepRejection(input.Steps); ok {
		admission.Rejections = append(admission.Rejections, reason)
	}
	if pendingSideEffect(input.CodeSaveRecords) {
		admission.Rejections = append(
			admission.Rejections,
			TaskQueueRejectionSideEffectPending,
		)
	}
	// 启动前置条件复用既有规则：队列准入严格强于启动准入，反向不成立。
	// 容器可用性与工作区现场不由该函数判定，因此这里只传它真正读取的字段。
	if err := CheckTaskLaunchStart(TaskLaunchInput{
		Task:                 task,
		Steps:                input.Steps,
		HasActiveWorkflowRun: input.ActiveWorkflowRun != nil,
	}); err != nil {
		admission.Prerequisite = err
		admission.Rejections = append(
			admission.Rejections,
			TaskQueueRejectionPrerequisiteMissing,
		)
	}
	admission.Admitted = len(admission.Rejections) == 0
	return admission
}

// checkQueuePriority 校验队列排序所依赖的优先级是否可以解析。
func checkQueuePriority(requirement *Requirement) error {
	if requirement == nil {
		return fmt.Errorf("%w: 任务缺少需求记录，无法确定队列优先级", ErrInvalidState)
	}
	return requirement.Priority.Validate()
}

// queueStepRejection 返回步骤层面的队列拒绝原因。
//
// 先判"结果不确定"再判"仍被占用"：前者要求人工核验，后者说明有执行者可能仍在推进，
// 两者的处置方式不同，不能合并成一个原因。
func queueStepRejection(steps []Step) (TaskQueueRejection, bool) {
	for _, step := range steps {
		if step.Status == StepStatusRecoveryRequired {
			return TaskQueueRejectionRecoveryResidue, true
		}
	}
	for _, step := range steps {
		switch step.Status {
		case StepStatusRunning, StepStatusDeveloping, StepStatusVerifying:
			return TaskQueueRejectionStepOccupied, true
		}
	}
	return "", false
}

// pendingSideEffect 判断是否仍有未核验的外部副作用。
//
// 判定复用 domain.DecideTaskCodeSaveResume：它是"该任务的代码保存能否继续推进"的
// 唯一定义，队列准入不重新实现 push / merge 的核验规则。
func pendingSideEffect(records []CodeSaveRecord) bool {
	if len(records) == 0 {
		return false
	}
	return DecideTaskCodeSaveResume(records).Action != CodeSaveResumeProceed
}

// TaskQueueOrderKey 是队列排序使用的稳定键。
type TaskQueueOrderKey struct {
	// TaskID 是任务标识，作为最终稳定次序。
	TaskID ID
	// Priority 是需求优先级，排序映射只来自 Priority.Rank。
	Priority Priority
	// CreatedAt 是任务创建时间，精度为毫秒。
	CreatedAt time.Time
}

// NewTaskQueueOrderKey 从队列判定输入构造排序键。
func NewTaskQueueOrderKey(input TaskQueueInput) (TaskQueueOrderKey, error) {
	if err := input.Task.ID.Validate(); err != nil {
		return TaskQueueOrderKey{}, err
	}
	if err := checkQueuePriority(input.Requirement); err != nil {
		return TaskQueueOrderKey{}, err
	}
	return TaskQueueOrderKey{
		TaskID:    input.Task.ID,
		Priority:  input.Requirement.Priority,
		CreatedAt: normalizeTime(input.Task.CreatedAt),
	}, nil
}

// CompareTaskQueueOrder 比较两个候选任务的先后次序，返回值小于 0 表示 a 先于 b。
//
// 规则固定为三级，且不使用任何未被持久化的字段：
//  1. 优先级：Priority.Rank 数值小的先（high → normal → low）；
//  2. 同优先级按创建时间先到先服务（FIFO）；
//  3. 时间相同时按任务标识升序，给出确定的最终次序。
//
// 第三级不是业务语义而是稳定性保证：时间戳在数据库中只有毫秒精度，同批创建的
// 任务必然出现相同时间；没有最终键时排序结果取决于输入顺序，同一个队列两次扫描
// 会得到不同次序。任务标识是随机十六进制，因此第三级只保证确定，不表达偏好。
//
// 任务列表的排序语义（created_at DESC, rowid DESC）不适用于调度队列：它按最新
// 优先返回，与"先到先服务"相反，调度器不得直接复用它。
func CompareTaskQueueOrder(a, b TaskQueueOrderKey) int {
	if a.Priority.Rank() != b.Priority.Rank() {
		if a.Priority.Rank() < b.Priority.Rank() {
			return -1
		}
		return 1
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		if a.CreatedAt.Before(b.CreatedAt) {
			return -1
		}
		return 1
	}
	return strings.Compare(a.TaskID.String(), b.TaskID.String())
}

// SortTaskQueueOrderKeys 按 CompareTaskQueueOrder 就地排序。
//
// 比较函数是全序（最终键唯一），因此普通排序与稳定排序结果一致；这里仍然使用
// 稳定排序，避免将来修改比较函数时引入不确定次序。
func SortTaskQueueOrderKeys(keys []TaskQueueOrderKey) {
	sort.SliceStable(keys, func(i, j int) bool {
		return CompareTaskQueueOrder(keys[i], keys[j]) < 0
	})
}

// TaskClaimDecision 是"当前执行者能否领取该任务"的结论。
type TaskClaimDecision struct {
	// Allowed 表示可以尝试领取。
	//
	// 允许不等于成功：真正的仲裁由存储层的原子条件更新完成，成功与否只能由
	// 重新读取后的 TaskHoldsExecution 判定。两个执行者在同一份快照上可以都被
	// 允许尝试，但不可能都成为持有者。
	Allowed bool
	// Takeover 表示本次领取建立在"上一个持有者的租约已过期"之上。
	Takeover bool
	// Rejection 是拒绝原因，允许时为空。
	Rejection TaskQueueRejection
	// Admission 是业务准入结论，始终填写，便于区分"准入不通过"与"租约被抢占"。
	Admission TaskQueueAdmission
}

// DecideTaskClaim 判断指定执行者能否领取该任务。
//
// 领取的前置条件 = 业务准入通过 + 执行者标识非空 + 租约空闲 / 已过期 / 已由本人持有。
// 领取失败不得推进任务状态，也不得启动工作流：本函数不修改任何状态，真正的占用
// 必须由存储层在单个条件更新内完成。
func DecideTaskClaim(
	input TaskQueueInput,
	owner string,
) (TaskClaimDecision, error) {
	trimmed := strings.TrimSpace(owner)
	if trimmed == "" {
		return TaskClaimDecision{}, invalidField(
			"TaskClaim",
			"owner",
			"执行者标识不能为空",
		)
	}
	admission := DecideTaskQueueAdmission(input)
	decision := TaskClaimDecision{Admission: admission}
	if !admission.Admitted {
		decision.Rejection = admission.Rejections[0]
		return decision, nil
	}
	now := input.Now
	switch {
	case input.Task.LeaseActive(now) && input.Task.LeaseOwner == trimmed:
		// 同一执行者重复领取是幂等的：租约仍然有效，不需要换手。
		decision.Allowed = true
	case input.Task.LeaseActive(now):
		decision.Rejection = TaskQueueRejectionLeaseHeld
	default:
		// 租约为空或已过期：可以领取。过期说明原持有者没有续租，本次是接管。
		decision.Allowed = true
		decision.Takeover = input.Task.LeaseOwner != "" || input.Task.LeaseExpiresAt != nil
	}
	return decision, nil
}

// TaskHoldsExecution 判断指定执行者在给定时刻是否仍持有该任务的执行权。
//
// 这是任务级 fencing 判据：领取成功之后，每一次可能产生副作用或状态迁移的推进
// 都必须先通过它。执行者标识不匹配（执行权已转移）或租约已过期（原持有者没有
// 续租）时一律返回 false，原执行者必须立即停止推进，而不是继续写入。
//
// 它只读记录本身，不读数据库：调用方必须先重新读取数据库中的最新记录再判定，
// 用本地内存对象自检没有意义。
func TaskHoldsExecution(task Task, owner string, at time.Time) bool {
	trimmed := strings.TrimSpace(owner)
	if trimmed == "" || task.LeaseOwner != trimmed {
		return false
	}
	return task.LeaseActive(at)
}

// TaskExecutionHandover 是"任务领取 → 工作流执行"交接的判定结论。
type TaskExecutionHandover struct {
	// Allowed 表示允许为该任务建立或恢复工作流执行记录。
	Allowed bool
	// Reason 是拒绝原因的稳定字符串，允许时为空。
	Reason TaskQueueRejection
	// Detail 是可读说明，允许时为空。
	Detail string
}

// DecideTaskExecutionHandover 判断当前执行者能否把任务领取交接给工作流执行。
//
// 交接规则固定为：
//  1. 任务已被删除 → 拒绝；
//  2. 任务已请求取消 → 拒绝（取消意图优先于推进）；
//  3. 执行者不再持有有效任务租约 → 拒绝（租约是调度名额与执行权的唯一令牌）；
//  4. 存在由其他执行者持有有效执行权的工作流执行记录 → 拒绝；
//  5. 任务状态不允许推进步骤 → 拒绝（复用 TaskStatus.AllowsStepExecution）。
//
// 第 3 条是本契约的核心：WorkflowRun 的执行租约不是独立的所有权来源，它是任务
// 租约在执行阶段的续期视图。当前代码尚未建立这条关联（StartWorkflow 不检查任务
// 租约），P3-M2 必须补上，否则"任务领取"与"工作流推进"会是两套互不相干的所有权。
func DecideTaskExecutionHandover(
	task Task,
	activeRun *WorkflowRun,
	owner string,
	at time.Time,
) TaskExecutionHandover {
	if task.Deleted() {
		return TaskExecutionHandover{
			Reason: TaskQueueRejectionDeleted,
			Detail: "任务已被逻辑删除，不能建立工作流执行",
		}
	}
	if task.CancelRequested {
		return TaskExecutionHandover{
			Reason: TaskQueueRejectionCancelRequested,
			Detail: "任务已请求取消，不能建立工作流执行",
		}
	}
	if !TaskHoldsExecution(task, owner, at) {
		return TaskExecutionHandover{
			Reason: TaskQueueRejectionLeaseHeld,
			Detail: "当前执行者不持有有效任务租约，不能建立工作流执行",
		}
	}
	if activeRun != nil && !activeRun.HoldsExecution(owner, at) &&
		taskQueueRunHoldsExecution(activeRun, at) {
		return TaskExecutionHandover{
			Reason: TaskQueueRejectionActiveWorkflowRun,
			Detail: "任务已有其他执行者持有有效执行权的工作流执行记录",
		}
	}
	if !task.Status.AllowsStepExecution() {
		return TaskExecutionHandover{
			Reason: TaskQueueRejectionStatusNotQueueable,
			Detail: "任务当前状态 " + string(task.Status) + " 不允许推进步骤",
		}
	}
	return TaskExecutionHandover{Allowed: true}
}

// taskQueueRunHoldsExecution 判断工作流执行记录在给定时刻是否仍持有执行权。
//
// 与 task_launch.go 的 workflowRunOwnsExecution 的区别：cancel_requested 也算
// 持有执行权。取消请求已记录但实际停止尚未确认时，执行者仍然在推进收尾，此时
// 抢占同一任务会与取消收敛互相覆盖。
func taskQueueRunHoldsExecution(run *WorkflowRun, at time.Time) bool {
	if run == nil || !run.LeaseActive(at) {
		return false
	}
	return run.Status == WorkflowRunRunning || run.Status == WorkflowRunCancelRequested
}

// TaskSlotState 是任务在调度名额上的归属状态。
//
// 它只回答"是否占用一个调度名额"，不回答"能否被领取"：名额为空闲不代表通过
// 准入（例如 queued 但缺少规划角色），准入结论必须由 DecideTaskQueueAdmission 给出。
type TaskSlotState string

// 调度名额状态取值。
const (
	// TaskSlotIdle 表示不占名额，且不需要人工处理（新任务、已结束任务）。
	TaskSlotIdle TaskSlotState = "idle"
	// TaskSlotClaimed 表示已领取但尚未建立工作流执行记录，占用一个名额。
	TaskSlotClaimed TaskSlotState = "claimed"
	// TaskSlotExecuting 表示工作流执行记录持有有效执行权，占用一个名额。
	TaskSlotExecuting TaskSlotState = "executing"
	// TaskSlotFinalizing 表示已进入收尾阶段且仍持有执行权，占用一个名额。
	TaskSlotFinalizing TaskSlotState = "finalizing"
	// TaskSlotAwaitingHuman 表示等待人工处理，不占名额也不得自动领取。
	TaskSlotAwaitingHuman TaskSlotState = "awaiting_human"
	// TaskSlotIndeterminate 表示租约已失效但结论无法确认，不占名额也不得自动重试。
	TaskSlotIndeterminate TaskSlotState = "indeterminate"
)

// OccupiesSlot 判断该状态是否占用一个调度名额。
//
// 名额只由"仍持有执行权的执行"占用：领取阶段的令牌是任务租约，执行阶段的令牌是
// 任务租约与工作流执行租约。等待人工与结论不明的任务不占名额，否则调度器会被
// 一堆无法自动推进的任务占满。
func (s TaskSlotState) OccupiesSlot() bool {
	switch s {
	case TaskSlotClaimed, TaskSlotExecuting, TaskSlotFinalizing:
		return true
	default:
		return false
	}
}

// AllowsAutomaticClaim 判断该状态是否允许调度器自动领取。
//
// 只有 idle 允许。awaiting_human 与 indeterminate 都必须人工介入：前者等待用户
// 决定，后者要求先核验现场，两者都不能被调度器当作普通候选重试。
func (s TaskSlotState) AllowsAutomaticClaim() bool {
	return s == TaskSlotIdle
}

// TaskSlotInput 是判定名额归属所需的持久化事实。
type TaskSlotInput struct {
	// Task 是任务记录。
	Task Task
	// ActiveWorkflowRun 是任务当前未结束的工作流执行记录，没有时为空。
	ActiveWorkflowRun *WorkflowRun
	// Now 是判定租约是否仍然有效的时刻。
	Now time.Time
}

// DecideTaskSlotState 依据持久化事实计算任务的名额归属状态。
//
// 规则顺序固定：
//  1. 已被逻辑删除或已进入终态 → 空闲；
//  2. 任务租约有效 → 已领取（这是"领取成功但尚未建立执行记录"的名额表达）；
//  3. 工作流执行记录持有有效执行权 → 执行中；
//  4. 任务处于收尾阶段（verifying / saving）且仍持有执行权 → 收尾中；
//  5. 执行记录处于 blocked / recovery_required，或任务处于 paused / failed /
//     recovery_required → 等待人工；
//  6. 其余情况（租约失效但仍处于中间状态、pending 记录超窗、步骤残留占用）
//     → 结论不明，必须人工核验，不得自动重试。
//
// 第 6 条是本函数与"失败"的分界线：结果无法确认既不是成功也不是失败，把它当作
// 普通失败任务立即重试会重放未经核验的外部副作用。
func DecideTaskSlotState(input TaskSlotInput) TaskSlotState {
	task := input.Task
	if task.Deleted() || task.Status.IsTerminal() {
		return TaskSlotIdle
	}
	now := input.Now
	if task.LeaseActive(now) {
		switch task.Status {
		case TaskStatusVerifying, TaskStatusSaving:
			return TaskSlotFinalizing
		default:
			return TaskSlotClaimed
		}
	}
	if taskQueueRunHoldsExecution(input.ActiveWorkflowRun, now) {
		return TaskSlotExecuting
	}
	if input.ActiveWorkflowRun != nil {
		switch input.ActiveWorkflowRun.Status {
		case WorkflowRunBlocked, WorkflowRunRecoveryRequired:
			return TaskSlotAwaitingHuman
		}
	}
	switch task.Status {
	case TaskStatusPaused, TaskStatusFailed, TaskStatusRecoveryRequired:
		return TaskSlotAwaitingHuman
	case TaskStatusQueued:
		return TaskSlotIdle
	default:
		return TaskSlotIndeterminate
	}
}

// 显式入口的可领取状态集合。
//
// 自动调度与显式入口的区别在于"谁表达了推进意图"：自动调度只领取 queued，而用户
// 显式启动、生成计划、启动或恢复工作流时，任务可能停在进程中断留下的中间状态。
// 这些集合因此是"允许显式推进"的状态，而**不含 failed**：失败任务的重新开始属于
// 独立的用户显式重试入口，不由这些接口隐式承担。
//
// 三个集合都由状态机派生，不另建映射：新增状态或修改迁移时只需改状态机一处。

// AllowsExplicitLaunch 判断任务状态是否允许用户显式发起统一启动。
func (s TaskStatus) AllowsExplicitLaunch() bool {
	return s.CanStart() && s != TaskStatusFailed
}

// AllowsExplicitPlanning 判断任务状态是否允许用户显式生成计划。
func (s TaskStatus) AllowsExplicitPlanning() bool {
	return s.CanStartPlanning() && s != TaskStatusFailed
}

// AllowsExplicitWorkflowStart 判断任务状态是否允许用户显式启动或恢复工作流。
//
// 复用 AllowsStepExecution：工作流只能从允许推进步骤的状态开始。
func (s TaskStatus) AllowsExplicitWorkflowStart() bool {
	return s.AllowsStepExecution()
}

// allTaskStatuses 是状态机中定义的全部任务状态。
func allTaskStatuses() []TaskStatus {
	return []TaskStatus{
		TaskStatusQueued,
		TaskStatusPreparing,
		TaskStatusPlanning,
		TaskStatusPlanned,
		TaskStatusDeveloping,
		TaskStatusVerifying,
		TaskStatusSaving,
		TaskStatusCancelling,
		TaskStatusRetrying,
		TaskStatusPaused,
		TaskStatusCompleted,
		TaskStatusFailed,
		TaskStatusCancelled,
		TaskStatusRecoveryRequired,
	}
}

// ExplicitLaunchStatuses 返回用户显式启动允许领取的任务状态集合。
func ExplicitLaunchStatuses() []TaskStatus {
	return filterTaskStatuses(TaskStatus.AllowsExplicitLaunch)
}

// ExplicitPlanningStatuses 返回用户显式生成计划允许领取的任务状态集合。
func ExplicitPlanningStatuses() []TaskStatus {
	return filterTaskStatuses(TaskStatus.AllowsExplicitPlanning)
}

// ExplicitWorkflowStatuses 返回用户显式启动或恢复工作流允许领取的任务状态集合。
func ExplicitWorkflowStatuses() []TaskStatus {
	return filterTaskStatuses(TaskStatus.AllowsExplicitWorkflowStart)
}

// AutomaticSchedulingStatuses 返回自动调度允许领取的任务状态集合。
//
// 它只有一个取值：调度器不得使用任何更宽的集合，否则 failed / paused /
// recovery_required 的任务会被自动重放。
func AutomaticSchedulingStatuses() []TaskStatus {
	return filterTaskStatuses(TaskStatus.AllowsAutomaticScheduling)
}

// filterTaskStatuses 按判定函数过滤全部任务状态。
func filterTaskStatuses(allow func(TaskStatus) bool) []TaskStatus {
	statuses := make([]TaskStatus, 0, 4)
	for _, status := range allTaskStatuses() {
		if allow(status) {
			statuses = append(statuses, status)
		}
	}
	return statuses
}
