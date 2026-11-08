package domain

import (
	"fmt"
	"time"
)

// 本文件定义需求进度与项目执行状态的聚合规则。
//
// 唯一实现在这里：接口、前端、项目执行服务与调度器都读同一份判定结果，任何一方都不得
// 另外实现"已完成比例""阻塞原因"或"当前任务"的推导。输入只有持久化的任务事实、
// 项目执行权与用户的停止意图，不接受模型生成的完成度、自然语言总结或时间戳推断。

// RequirementProgressStatus 是需求进度的投影状态。
//
// 它不是持久化的需求状态（requirements.status 表达业务阶段），而是"依据关联任务真实
// 状态算出的进度结论"：同一个需求的状态字段可能仍是 queued，但进度已经 executing。
type RequirementProgressStatus string

// 需求进度状态取值。
const (
	// RequirementProgressStatusPlanned 表示尚未有任务开始执行。
	RequirementProgressStatusPlanned RequirementProgressStatus = "planned"
	// RequirementProgressStatusExecuting 表示至少一个任务已经开始执行。
	RequirementProgressStatusExecuting RequirementProgressStatus = "executing"
	// RequirementProgressStatusBlocked 表示存在失败、待恢复、取消或暂停的任务。
	RequirementProgressStatusBlocked RequirementProgressStatus = "blocked"
	// RequirementProgressStatusCompleted 表示全部关联任务都真实完成。
	RequirementProgressStatusCompleted RequirementProgressStatus = "completed"
)

// RequirementProgress 是一个需求的进度统计。
//
// Current 与 Completed 同值：进度只按已完成任务数量计算，不使用语义完成度，也不以
// 时间戳或最后更新时间推断。
type RequirementProgress struct {
	// RequirementID 是需求标识。
	RequirementID ID `json:"requirement_id"`
	// Counts 是任务数量统计。
	Counts ProjectTaskCounts `json:"counts"`
	// Current 是当前已完成任务数量。
	Current int `json:"current"`
	// Status 是进度投影状态。
	Status RequirementProgressStatus `json:"status"`
	// Blocked 表示存在阻止完成判定与继续领取的任务。
	Blocked bool `json:"blocked"`
	// BlockedTaskID 是造成阻塞的任务标识。
	BlockedTaskID *ID `json:"blocked_task_id,omitempty"`
	// BlockedReason 是阻塞原因的稳定取值。
	BlockedReason string `json:"blocked_reason,omitempty"`
	// CurrentTaskID 是正在执行的任务标识。
	CurrentTaskID *ID `json:"current_task_id,omitempty"`
	// NextTaskID 是下一个可执行的任务标识。
	NextTaskID *ID `json:"next_task_id,omitempty"`
}

// DecideRequirementProgress 计算一个需求的进度。
//
// 规则固定为：
//  1. 完成：存在任务且全部任务都是 completed；
//  2. 阻塞：存在 failed / recovery_required / cancelled / paused 任务，且不满足完成条件；
//  3. 执行中：有任务仍在执行，或已有任务完成但未全部完成；
//  4. 未开始：其余情况。
//
// 优先级依次递减，因此"既有失败任务又有执行中任务"的历史现场仍然判定为阻塞：完成度
// 不能被执行中的任务掩盖，也不能因为后续任务成功就跳过失败任务。
func DecideRequirementProgress(requirementID ID, facts []ProjectTaskFact) RequirementProgress {
	filtered := make([]ProjectTaskFact, 0, len(facts))
	for _, fact := range facts {
		if fact.Requirement.ID == requirementID {
			filtered = append(filtered, fact)
		}
	}
	plan := DecideProjectExecutionPlan(filtered)
	progress := RequirementProgress{
		RequirementID: requirementID,
		Counts:        plan.Counts,
		Current:       plan.Counts.Completed,
		Blocked:       plan.Blocked,
		BlockedTaskID: plan.BlockedTaskID,
		BlockedReason: plan.BlockedReason,
	}
	switch {
	case plan.Counts.Total > 0 && plan.Counts.Completed == plan.Counts.Total:
		progress.Status = RequirementProgressStatusCompleted
		progress.Blocked = false
		progress.BlockedTaskID = nil
		progress.BlockedReason = ""
	case plan.Counts.Blocking():
		progress.Status = RequirementProgressStatusBlocked
		// 阻塞原因取第一个阻止完成判定的任务：需求内可能同时存在执行中与失败的任务，
		// 只报告队首任务会漏掉真正的阻塞点。
		blockedTaskID, blockedReason := firstBlockingTask(plan)
		progress.Blocked = true
		progress.BlockedTaskID = blockedTaskID
		progress.BlockedReason = blockedReason
	case plan.Counts.Running > 0 || plan.Counts.Completed > 0:
		progress.Status = RequirementProgressStatusExecuting
	default:
		progress.Status = RequirementProgressStatusPlanned
	}
	progress.CurrentTaskID = firstActiveTaskID(plan)
	progress.NextTaskID = plan.Claimable
	return progress
}

// Validate 校验进度统计的一致性。
//
// 它只校验能够从字段本身判断的事实，不重新推导状态：调用方在持久化或转发前用它拦住
// "完成数量超过总数"这类不可能出现的组合。
func (p RequirementProgress) Validate() error {
	if err := p.Counts.Validate(); err != nil {
		return err
	}
	if p.Current != p.Counts.Completed {
		return fmt.Errorf(
			"%w: 需求 %s 的当前进度 %d 与已完成任务数 %d 不一致",
			ErrInvalidState,
			p.RequirementID,
			p.Current,
			p.Counts.Completed,
		)
	}
	return nil
}

// Validate 校验任务数量统计的一致性。
func (c ProjectTaskCounts) Validate() error {
	if c.Total < 0 {
		return fmt.Errorf("%w: 任务总数不能为负", ErrInvalidValue)
	}
	if c.Completed < 0 || c.Running < 0 || c.Pending < 0 ||
		c.Failed < 0 || c.RecoveryRequired < 0 || c.Cancelled < 0 || c.Paused < 0 {
		return fmt.Errorf("%w: 任务数量统计不能为负", ErrInvalidValue)
	}
	sum := c.Completed + c.Running + c.Pending + c.Failed +
		c.RecoveryRequired + c.Cancelled + c.Paused
	if sum > c.Total {
		return fmt.Errorf(
			"%w: 任务数量统计 %d 超过任务总数 %d",
			ErrInvalidValue,
			sum,
			c.Total,
		)
	}
	return nil
}

// ProjectExecutionStatus 是项目执行状态的投影取值。
type ProjectExecutionStatus string

// 项目执行状态取值。
const (
	// ProjectExecutionStatusIdle 表示项目没有在执行。
	ProjectExecutionStatusIdle ProjectExecutionStatus = "idle"
	// ProjectExecutionStatusRunning 表示项目持有执行权或存在执行中的任务。
	ProjectExecutionStatusRunning ProjectExecutionStatus = "running"
	// ProjectExecutionStatusBlocked 表示项目被需要人工处置的任务阻塞。
	ProjectExecutionStatusBlocked ProjectExecutionStatus = "blocked"
	// ProjectExecutionStatusPaused 表示用户已显式停止，且当前没有任务在执行。
	ProjectExecutionStatusPaused ProjectExecutionStatus = "paused"
)

// ProjectExecutionInput 是计算项目执行状态所需的持久化事实。
type ProjectExecutionInput struct {
	// ProjectID 是项目标识。
	ProjectID ID
	// Tasks 是项目内全部正式任务（未删除）。
	Tasks []ProjectTaskFact
	// Requirements 是项目下正式需求数量。
	Requirements int
	// Lease 是项目执行权，没有登记时为空。
	Lease *ProjectExecutionLease
	// Now 是判定时刻。
	Now time.Time
	// StopRequested 表示用户已请求停止领取新任务。
	StopRequested bool
}

// ProjectProgress 是项目执行状态与进度统计。
type ProjectProgress struct {
	// ProjectID 是项目标识。
	ProjectID ID `json:"project_id"`
	// Status 是项目执行状态投影。
	Status ProjectExecutionStatus `json:"status"`
	// Counts 是项目内任务数量统计。
	Counts ProjectTaskCounts `json:"counts"`
	// Requirements 是项目下正式需求数量。
	Requirements int `json:"requirements"`
	// Blocked 表示项目被前序任务阻塞。
	Blocked bool `json:"blocked"`
	// BlockedTaskID 是造成阻塞的任务标识。
	BlockedTaskID *ID `json:"blocked_task_id,omitempty"`
	// BlockedReason 是阻塞原因的稳定取值。
	BlockedReason string `json:"blocked_reason,omitempty"`
	// CurrentTaskID 是正在执行的任务标识。
	CurrentTaskID *ID `json:"current_task_id,omitempty"`
	// NextTaskID 是下一个可执行的任务标识。
	NextTaskID *ID `json:"next_task_id,omitempty"`
	// LeaseActive 表示项目当前持有有效的执行权。
	LeaseActive bool `json:"lease_active"`
	// LeaseEpoch 是当前执行权代数。
	LeaseEpoch int64 `json:"lease_epoch,omitempty"`
	// LeaseExpiresAt 是当前执行权到期时间。
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	// StopRequested 表示用户已请求停止领取。
	StopRequested bool `json:"stop_requested"`
	// Stopping 表示停止请求已记录但仍有任务在执行。
	Stopping bool `json:"stopping"`
}

// DecideProjectExecution 计算项目执行状态。
//
// 状态优先级固定为：
//  1. 执行中：存在执行中的任务，或项目仍持有有效执行权；
//  2. 已停止：用户已请求停止且当前没有任务在执行；
//  3. 阻塞：队首任务需要人工处置；
//  4. 空闲：其余情况。
//
// 阻塞原因与阻塞任务始终如实填写，即使状态是执行中：一个项目在"已有失败任务但另一个
// 任务仍在执行"的历史现场下，既不能显示为已完成，也不能隐藏阻塞事实。
func DecideProjectExecution(input ProjectExecutionInput) ProjectProgress {
	plan := DecideProjectExecutionPlan(input.Tasks)
	progress := ProjectProgress{
		ProjectID:     input.ProjectID,
		Counts:        plan.Counts,
		Requirements:  input.Requirements,
		Blocked:       plan.Blocked,
		BlockedTaskID: plan.BlockedTaskID,
		BlockedReason: plan.BlockedReason,
		NextTaskID:    plan.Claimable,
		StopRequested: input.StopRequested,
	}
	if input.Lease != nil {
		progress.LeaseEpoch = input.Lease.Epoch
		progress.LeaseActive = input.Lease.ActiveAt(input.Now)
		if progress.LeaseActive {
			expiresAt := input.Lease.ExpiresAt
			progress.LeaseExpiresAt = &expiresAt
		}
	}
	progress.CurrentTaskID = firstActiveTaskID(plan)
	progress.Stopping = input.StopRequested && plan.Counts.Running > 0
	switch {
	case plan.Counts.Running > 0 || progress.LeaseActive:
		progress.Status = ProjectExecutionStatusRunning
	case input.StopRequested:
		progress.Status = ProjectExecutionStatusPaused
	case plan.Blocked:
		progress.Status = ProjectExecutionStatusBlocked
	default:
		progress.Status = ProjectExecutionStatusIdle
	}
	return progress
}

// firstBlockingTask 返回按执行顺序第一个阻止完成判定的任务标识与原因。
//
// 它只在"存在阻塞任务"时被调用，不用于领取判定：领取判定只允许队首任务，两者的口径
// 不同，因此不共用 ProjectExecutionPlan.BlockedTaskID。
func firstBlockingTask(plan ProjectExecutionPlan) (*ID, string) {
	for _, fact := range plan.Tasks {
		var reason string
		switch fact.Task.Status {
		case TaskStatusFailed:
			reason = ProjectBlockReasonTaskFailed
		case TaskStatusRecoveryRequired:
			reason = ProjectBlockReasonRecoveryRequired
		case TaskStatusCancelled:
			reason = ProjectBlockReasonTaskCancelled
		case TaskStatusPaused:
			reason = ProjectBlockReasonTaskPaused
		}
		if reason == "" {
			continue
		}
		taskID := fact.Task.ID
		return &taskID, reason
	}
	return nil, ""
}

// firstActiveTaskID 返回计划中第一个仍在执行的任务标识。
func firstActiveTaskID(plan ProjectExecutionPlan) *ID {
	for _, fact := range plan.Tasks {
		if fact.Task.Status.IsActive() {
			taskID := fact.Task.ID
			return &taskID
		}
	}
	return nil
}
