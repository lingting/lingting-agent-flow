package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 本文件定义"项目内任务顺序与领取资格"的业务语义。
//
// 唯一实现在这里：调度器（自动领取）与项目执行服务（开始 / 停止 / 状态查询）都按同一
// 份判定决定"下一个该执行谁""谁挡在前面""项目是否被阻塞"。它们不各自筛选 queued 就
// 认定前序任务已经完成，也不通过创建时间、接口返回或 Agent 调用结果推断任务结论。

// ProjectTaskFact 是项目内一个正式任务连同所属需求的持久化事实。
//
// 归属项目只有一条路径：requirements.project_id。历史手工任务没有项目关联，不属于任何
// 项目，因此不会出现在项目执行计划里，也不参与项目内顺序与阻塞判定。
type ProjectTaskFact struct {
	// Requirement 是任务所属需求的持久化记录。
	Requirement Requirement
	// Task 是项目内的正式任务。
	Task Task
}

// ProjectTaskHeadState 是项目执行计划中队首任务的分类。
type ProjectTaskHeadState string

// 队首任务的分类取值。
const (
	// ProjectTaskHeadClaimable 表示队首任务可以自动领取（queued 且未请求取消）。
	ProjectTaskHeadClaimable ProjectTaskHeadState = "claimable"
	// ProjectTaskHeadRunning 表示队首任务仍在执行（含退避、验证与取消收敛）。
	ProjectTaskHeadRunning ProjectTaskHeadState = "running"
	// ProjectTaskHeadBlocked 表示队首任务需要人工处置，后续任务不得越过它。
	ProjectTaskHeadBlocked ProjectTaskHeadState = "blocked"
	// ProjectTaskHeadCompleted 表示项目内任务全部真实完成（或项目没有任务）。
	ProjectTaskHeadCompleted ProjectTaskHeadState = "completed"
)

// 项目任务阻塞原因的稳定取值。
//
// 它们用于日志、事件与接口字段，不解析错误文本；调用方按稳定字符串判断类别。
const (
	// ProjectBlockReasonTaskFailed 表示前序任务已失败。
	ProjectBlockReasonTaskFailed = "task_failed"
	// ProjectBlockReasonRecoveryRequired 表示前序任务结果不确定，必须先核验。
	ProjectBlockReasonRecoveryRequired = "task_recovery_required"
	// ProjectBlockReasonTaskCancelled 表示前序任务已被取消，不再视为完成。
	ProjectBlockReasonTaskCancelled = "task_cancelled"
	// ProjectBlockReasonTaskPaused 表示前序任务处于人工暂停。
	ProjectBlockReasonTaskPaused = "task_paused"
	// ProjectBlockReasonTaskInFlight 表示前序任务停在中间状态但没有执行记录，现场未收敛。
	ProjectBlockReasonTaskInFlight = "task_in_flight"
	// ProjectBlockReasonCancelRequested 表示前序任务已记录取消请求，等待真实收敛。
	ProjectBlockReasonCancelRequested = "cancel_requested"
)

// ProjectExecutionPlan 是项目内执行顺序、队首分类与可领取任务的判定结果。
type ProjectExecutionPlan struct {
	// Tasks 是按执行顺序排列的项目内正式任务（只含未逻辑删除的记录）。
	Tasks []ProjectTaskFact
	// HeadIndex 是队首任务在 Tasks 中的下标，-1 表示没有挡在队首的任务。
	HeadIndex int
	// HeadState 是队首任务的分类；项目没有任务或全部完成时为 completed。
	HeadState ProjectTaskHeadState
	// Claimable 是当前唯一允许自动领取的任务标识，没有可领取任务时为空。
	Claimable *ID
	// Blocked 表示项目被前序任务阻塞：这些任务既不允许自动领取，也不允许被跳过。
	Blocked bool
	// BlockedTaskID 是造成阻塞的任务标识。
	BlockedTaskID *ID
	// BlockedReason 是阻塞原因的稳定取值。
	BlockedReason string
	// Counts 是项目内任务的数量统计。
	Counts ProjectTaskCounts
}

// BlockedSummary 返回可读的阻塞说明，没有阻塞时为空。
func (p ProjectExecutionPlan) BlockedSummary() string {
	if !p.Blocked || p.BlockedTaskID == nil {
		return ""
	}
	return fmt.Sprintf("任务 %s 阻塞了项目：%s", *p.BlockedTaskID, p.BlockedReason)
}

// ProjectTaskCounts 是任务数量统计。
//
// 口径固定：completed 只统计真实 completed；cancelled 不并入 failed，两者都会阻止完成
// 判定但原因不同，前端与接口必须能分别展示。
type ProjectTaskCounts struct {
	// Total 是任务总数（只含未删除记录）。
	Total int
	// Completed 是已完成任务数。
	Completed int
	// Running 是仍在执行的任务数（含取消收敛）。
	Running int
	// Pending 是尚未开始执行的任务数。
	Pending int
	// Failed 是失败任务数。
	Failed int
	// RecoveryRequired 是结果不确定、需要核验的任务数。
	RecoveryRequired int
	// Cancelled 是已取消任务数。
	Cancelled int
	// Paused 是人工暂停任务数。
	Paused int
}

// Blocking 判断统计结果中是否存在阻止"项目已完成"的任务。
func (c ProjectTaskCounts) Blocking() bool {
	return c.Failed > 0 || c.RecoveryRequired > 0 || c.Cancelled > 0 || c.Paused > 0
}

// DecideProjectExecutionPlan 计算项目内执行顺序、队首分类与可领取任务。
//
// 排序规则固定为四级，全部来自持久化事实，不依赖 SQLite 的返回顺序：
//  1. 需求创建时间升序（先确认的需求先执行）；
//  2. 需求内顺序 requirement_sequence 升序，历史任务没有该事实时排在后面；
//  3. 任务创建时间升序；
//  4. 任务标识升序，作为确定性最终键。
//
// 队首扫描规则固定为"第一个非 completed 的任务决定结论"：
//   - queued 且未请求取消 → 可领取；
//   - 仍在执行（TaskStatus.IsActive，含 cancelling）→ 正在执行，后续任务不得越过；
//   - failed / recovery_required / paused / cancelled → 阻塞，后续任务不得越过；
//   - 其他中间状态（无执行记录的 preparing / planned 等）→ 现场未收敛，按阻塞处理。
//
// 该函数是纯函数：不读数据库、不写状态、不产生任何远程副作用。
func DecideProjectExecutionPlan(facts []ProjectTaskFact) ProjectExecutionPlan {
	ordered := make([]ProjectTaskFact, len(facts))
	copy(ordered, facts)
	SortProjectTasks(ordered)

	plan := ProjectExecutionPlan{HeadIndex: -1, HeadState: ProjectTaskHeadCompleted}
	plan.Tasks = ordered
	plan.Counts = countProjectTasks(ordered)
	if len(ordered) == 0 {
		return plan
	}
	for index, fact := range ordered {
		if fact.Task.Status == TaskStatusCompleted {
			continue
		}
		plan.HeadIndex = index
		taskID := fact.Task.ID
		plan.BlockedTaskID = &taskID
		switch {
		case fact.Task.CancelRequested:
			plan.HeadState = ProjectTaskHeadBlocked
			plan.Blocked = true
			plan.BlockedReason = ProjectBlockReasonCancelRequested
		case fact.Task.Status == TaskStatusQueued:
			plan.HeadState = ProjectTaskHeadClaimable
			plan.BlockedTaskID = nil
			claimable := taskID
			plan.Claimable = &claimable
		case fact.Task.Status.IsActive():
			plan.HeadState = ProjectTaskHeadRunning
			plan.BlockedTaskID = nil
		case fact.Task.Status == TaskStatusFailed:
			plan.HeadState = ProjectTaskHeadBlocked
			plan.Blocked = true
			plan.BlockedReason = ProjectBlockReasonTaskFailed
		case fact.Task.Status == TaskStatusRecoveryRequired:
			plan.HeadState = ProjectTaskHeadBlocked
			plan.Blocked = true
			plan.BlockedReason = ProjectBlockReasonRecoveryRequired
		case fact.Task.Status == TaskStatusCancelled:
			plan.HeadState = ProjectTaskHeadBlocked
			plan.Blocked = true
			plan.BlockedReason = ProjectBlockReasonTaskCancelled
		case fact.Task.Status == TaskStatusPaused:
			plan.HeadState = ProjectTaskHeadBlocked
			plan.Blocked = true
			plan.BlockedReason = ProjectBlockReasonTaskPaused
		default:
			plan.HeadState = ProjectTaskHeadBlocked
			plan.Blocked = true
			plan.BlockedReason = ProjectBlockReasonTaskInFlight
		}
		return plan
	}
	// 走到这里说明全部任务都是 completed。
	plan.HeadState = ProjectTaskHeadCompleted
	return plan
}

// countProjectTasks 统计项目内任务数量。
func countProjectTasks(facts []ProjectTaskFact) ProjectTaskCounts {
	counts := ProjectTaskCounts{Total: len(facts)}
	for _, fact := range facts {
		switch fact.Task.Status {
		case TaskStatusCompleted:
			counts.Completed++
		case TaskStatusQueued:
			counts.Pending++
		case TaskStatusFailed:
			counts.Failed++
		case TaskStatusRecoveryRequired:
			counts.RecoveryRequired++
		case TaskStatusCancelled:
			counts.Cancelled++
		case TaskStatusPaused:
			counts.Paused++
		default:
			if fact.Task.Status.IsActive() {
				counts.Running++
			}
		}
	}
	return counts
}

// SortProjectTasks 按项目内执行顺序就地排序。
func SortProjectTasks(facts []ProjectTaskFact) {
	sort.SliceStable(facts, func(left, right int) bool {
		return CompareProjectTasks(facts[left], facts[right]) < 0
	})
}

// CompareProjectTasks 比较两个项目内任务的执行先后，返回值小于 0 表示 left 先执行。
func CompareProjectTasks(left, right ProjectTaskFact) int {
	if order := compareProjectRequirement(left.Requirement, right.Requirement); order != 0 {
		return order
	}
	if order := compareRequirementSequence(left.Task.RequirementSequence, right.Task.RequirementSequence); order != 0 {
		return order
	}
	leftCreated := normalizeTime(left.Task.CreatedAt)
	rightCreated := normalizeTime(right.Task.CreatedAt)
	if !leftCreated.Equal(rightCreated) {
		if leftCreated.Before(rightCreated) {
			return -1
		}
		return 1
	}
	return strings.Compare(left.Task.ID.String(), right.Task.ID.String())
}

// compareProjectRequirement 比较两个需求的先后。
//
// 创建时间相同的需求按标识升序：时间戳只有毫秒精度，没有最终键时同一个项目两次扫描
// 会得到不同次序。
func compareProjectRequirement(left, right Requirement) int {
	leftCreated := normalizeTime(left.CreatedAt)
	rightCreated := normalizeTime(right.CreatedAt)
	if !leftCreated.Equal(rightCreated) {
		if leftCreated.Before(rightCreated) {
			return -1
		}
		return 1
	}
	return strings.Compare(left.ID.String(), right.ID.String())
}

// compareRequirementSequence 比较需求内顺序：空值表示历史任务没有该事实，排在后面。
func compareRequirementSequence(left, right *int) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return 1
	case right == nil:
		return -1
	case *left < *right:
		return -1
	case *left > *right:
		return 1
	default:
		return 0
	}
}

// ProjectExecutionTaskCheckInput 是"某个任务能否被某个项目执行权领取"的判定输入。
//
// Lease 为空表示项目没有执行权；StopRequested 为真表示用户已请求停止领取。两者的结论
// 不同：没有执行权时任务不得被自动领取，停止请求同样禁止领取，但项目状态仍然如实展示。
type ProjectExecutionTaskCheckInput struct {
	// TaskID 是待判定的任务标识。
	TaskID ID
	// Plan 是项目执行计划。
	Plan ProjectExecutionPlan
	// Lease 是项目执行权，没有登记时为空。
	Lease *ProjectExecutionLease
	// Owner 是本进程的持有者标识。
	Owner string
	// Now 是判定时刻。
	Now time.Time
	// StopRequested 表示用户已请求停止领取新任务。
	StopRequested bool
}

// ProjectExecutionClaim 是任务领取判定结论。
type ProjectExecutionClaim struct {
	// Allowed 表示该任务可以作为自动分派候选。
	Allowed bool
	// Reason 是拒绝原因的稳定取值，允许时为空。
	Reason string
	// Detail 是可读说明，允许时为空。
	Detail string
}

// 领取拒绝原因的稳定取值。
const (
	// ProjectClaimReasonNoLease 表示项目没有有效执行权。
	ProjectClaimReasonNoLease = "project_execution_not_held"
	// ProjectClaimReasonLeaseExpired 表示项目执行权已过期。
	ProjectClaimReasonLeaseExpired = "project_execution_expired"
	// ProjectClaimReasonLeaseLost 表示项目执行权已属于其他持有者或新的 epoch。
	ProjectClaimReasonLeaseLost = "project_execution_lost"
	// ProjectClaimReasonStopRequested 表示用户已请求停止领取。
	ProjectClaimReasonStopRequested = "project_execution_stopped"
	// ProjectClaimReasonNotHead 表示该任务不是项目内下一个该执行的任务。
	ProjectClaimReasonNotHead = "project_task_not_head"
	// ProjectClaimReasonBlocked 表示项目被前序任务阻塞。
	ProjectClaimReasonBlocked = "project_task_blocked"
	// ProjectClaimReasonRunning 表示项目内已有任务在执行。
	ProjectClaimReasonRunning = "project_task_running"
	// ProjectClaimReasonCompleted 表示项目内任务已经全部完成。
	ProjectClaimReasonCompleted = "project_completed"
)

// DecideProjectExecutionClaim 判断某个任务能否被本次项目执行权领取。
//
// 规则顺序固定，先判执行权再判顺序：执行权失效时不再讨论顺序，因为此时项目已经不属于
// 本持有者；顺序判定只允许 Plan.Claimable 指向的那个任务，任何后续任务都不能被提前领取。
func DecideProjectExecutionClaim(input ProjectExecutionTaskCheckInput) ProjectExecutionClaim {
	if input.Lease == nil {
		return ProjectExecutionClaim{
			Reason: ProjectClaimReasonNoLease,
			Detail: "项目没有执行权，任务不能被自动领取",
		}
	}
	if !input.Lease.ActiveAt(input.Now) {
		return ProjectExecutionClaim{
			Reason: ProjectClaimReasonLeaseExpired,
			Detail: "项目执行权已过期，任务不能被自动领取",
		}
	}
	if !input.Lease.HeldByOwner(input.Owner) {
		return ProjectExecutionClaim{
			Reason: ProjectClaimReasonLeaseLost,
			Detail: "项目执行权已属于其他持有者",
		}
	}
	if input.StopRequested {
		return ProjectExecutionClaim{
			Reason: ProjectClaimReasonStopRequested,
			Detail: "用户已请求停止项目执行，不再领取新任务",
		}
	}
	switch input.Plan.HeadState {
	case ProjectTaskHeadClaimable:
		if input.Plan.Claimable == nil || *input.Plan.Claimable != input.TaskID {
			return ProjectExecutionClaim{
				Reason: ProjectClaimReasonNotHead,
				Detail: "该任务不是项目内下一个该执行的任务",
			}
		}
		return ProjectExecutionClaim{Allowed: true}
	case ProjectTaskHeadRunning:
		return ProjectExecutionClaim{
			Reason: ProjectClaimReasonRunning,
			Detail: "项目内已有任务在执行，后续任务不得越过",
		}
	case ProjectTaskHeadBlocked:
		return ProjectExecutionClaim{
			Reason: ProjectClaimReasonBlocked,
			Detail: input.Plan.BlockedSummary(),
		}
	default:
		return ProjectExecutionClaim{
			Reason: ProjectClaimReasonCompleted,
			Detail: "项目内任务已经全部完成",
		}
	}
}
