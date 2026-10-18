package domain

import (
	"strings"
	"time"
)

// TaskStatus 表示需求的一次完整执行所处的阶段。
//
// 状态语义与工作流阶段的对应关系：
//   - queued      等待执行；
//   - preparing   准备执行环境（创建任务容器）；
//   - planning    正在由 Planner 生成计划；
//   - planned     计划已确认，等待执行开发步骤；
//   - developing  正在执行开发步骤（对应"running"）；
//   - verifying   正在执行验证（对应"validating"）；
//   - saving      正在保存代码（Commit / Push / 可选 Merge）；
//   - cancelling  已请求取消，正在停止当前操作并处理现场；
//   - completed   所有必需阶段成功（对应"succeeded"）；
//   - failed      任务最终失败；
//   - cancelled   取消流程已完成；
//   - retrying    按重试策略准备新的尝试；
//   - paused      人工介入后暂停；
//   - recovery_required 实际执行结果或资源状态无法自动确认，需要恢复核验。
type TaskStatus string

// 任务状态。
const (
	TaskStatusQueued           TaskStatus = "queued"
	TaskStatusPreparing        TaskStatus = "preparing"
	TaskStatusPlanning         TaskStatus = "planning"
	TaskStatusPlanned          TaskStatus = "planned"
	TaskStatusDeveloping       TaskStatus = "developing"
	TaskStatusVerifying        TaskStatus = "verifying"
	TaskStatusSaving           TaskStatus = "saving"
	TaskStatusCancelling       TaskStatus = "cancelling"
	TaskStatusRetrying         TaskStatus = "retrying"
	TaskStatusPaused           TaskStatus = "paused"
	TaskStatusCompleted        TaskStatus = "completed"
	TaskStatusFailed           TaskStatus = "failed"
	TaskStatusCancelled        TaskStatus = "cancelled"
	TaskStatusRecoveryRequired TaskStatus = "recovery_required"
)

// taskStatusMachine 定义任务状态迁移。
//
// 关键约束：
//   - completed 与 cancelled 是终态，重复执行不会重新进入执行阶段；
//   - failed 允许回到 preparing 或 planning，用于用户显式触发的任务重试；
//   - recovery_required 只允许在核验后回到确定的执行阶段或结束；
//   - cancelling 只能收敛为 cancelled、failed 或 recovery_required。
var taskStatusMachine = statusMachine{
	entity: "Task",
	transitions: map[string][]string{
		string(TaskStatusQueued): {
			string(TaskStatusPreparing),
			string(TaskStatusPlanning),
			string(TaskStatusCancelling),
			string(TaskStatusFailed),
			// 恢复核验发现等待中的任务实际状态无法确认时，允许直接标记。
			string(TaskStatusRecoveryRequired),
		},
		string(TaskStatusPreparing): {
			string(TaskStatusPlanning),
			string(TaskStatusDeveloping),
			string(TaskStatusFailed),
			string(TaskStatusCancelling),
			string(TaskStatusRecoveryRequired),
		},
		string(TaskStatusPlanning): {
			string(TaskStatusPlanned),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelling),
			string(TaskStatusRecoveryRequired),
		},
		string(TaskStatusPlanned): {
			string(TaskStatusDeveloping),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelling),
			string(TaskStatusRecoveryRequired),
		},
		string(TaskStatusDeveloping): {
			string(TaskStatusVerifying),
			string(TaskStatusRetrying),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelling),
			string(TaskStatusRecoveryRequired),
		},
		string(TaskStatusVerifying): {
			string(TaskStatusRetrying),
			string(TaskStatusSaving),
			string(TaskStatusCompleted),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelling),
			string(TaskStatusRecoveryRequired),
		},
		string(TaskStatusSaving): {
			string(TaskStatusCompleted),
			string(TaskStatusFailed),
			string(TaskStatusCancelling),
			string(TaskStatusRecoveryRequired),
		},
		string(TaskStatusRetrying): {
			string(TaskStatusPreparing),
			string(TaskStatusDeveloping),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelling),
			string(TaskStatusRecoveryRequired),
		},
		string(TaskStatusPaused): {
			string(TaskStatusPreparing),
			string(TaskStatusPlanning),
			string(TaskStatusDeveloping),
			string(TaskStatusVerifying),
			string(TaskStatusSaving),
			string(TaskStatusRetrying),
			string(TaskStatusCancelling),
			string(TaskStatusFailed),
		},
		string(TaskStatusCancelling): {
			string(TaskStatusCancelled),
			string(TaskStatusFailed),
			string(TaskStatusRecoveryRequired),
		},
		string(TaskStatusRecoveryRequired): {
			string(TaskStatusPreparing),
			string(TaskStatusPlanning),
			string(TaskStatusDeveloping),
			string(TaskStatusVerifying),
			string(TaskStatusSaving),
			string(TaskStatusCancelling),
			string(TaskStatusFailed),
		},
		string(TaskStatusFailed): {
			string(TaskStatusPreparing),
			string(TaskStatusPlanning),
			string(TaskStatusCancelling),
		},
	},
	terminal: []string{
		string(TaskStatusCompleted),
		string(TaskStatusCancelled),
	},
}

// Validate 校验任务状态是否属于已知状态。
func (s TaskStatus) Validate() error {
	if taskStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("Task", "status", "未知状态 "+string(s))
}

// IsTerminal 判断任务状态是否为终态。
func (s TaskStatus) IsTerminal() bool {
	return taskStatusMachine.isTerminal(string(s))
}

// IsActive 判断任务是否正在执行。
func (s TaskStatus) IsActive() bool {
	switch s {
	case TaskStatusPreparing, TaskStatusPlanning, TaskStatusPlanned,
		TaskStatusDeveloping, TaskStatusVerifying, TaskStatusSaving,
		TaskStatusRetrying, TaskStatusCancelling:
		return true
	default:
		return false
	}
}

// CanStart 判断任务是否允许由工作流引擎推进。
//
// 只有等待执行、失败重试、暂停恢复与恢复核验后的任务可以开始推进；
// 已结束的任务不会被重复推进。
func (s TaskStatus) CanStart() bool {
	switch s {
	case TaskStatusQueued, TaskStatusFailed, TaskStatusPaused, TaskStatusRecoveryRequired,
		// 进程中断后可能停留在中间状态：允许重新推进，由引擎按持久化状态继续。
		TaskStatusPreparing, TaskStatusPlanned, TaskStatusRetrying:
		return true
	default:
		return false
	}
}

// TaskResult 是任务的最终结论。
//
// 它与容器生命周期结束原因区分开：任务结论表达业务结果，容器处置表达资源策略。
type TaskResult string

// 任务最终结论取值。
const (
	// TaskResultNone 表示任务尚未结束。
	TaskResultNone TaskResult = ""
	// TaskResultSucceeded 表示任务成功。
	TaskResultSucceeded TaskResult = "succeeded"
	// TaskResultFailed 表示任务失败。
	TaskResultFailed TaskResult = "failed"
	// TaskResultCancelled 表示任务被取消。
	TaskResultCancelled TaskResult = "cancelled"
)

// Validate 校验任务结论是否属于已知取值。
func (r TaskResult) Validate() error {
	switch r {
	case TaskResultNone, TaskResultSucceeded, TaskResultFailed, TaskResultCancelled:
		return nil
	default:
		return invalidField("Task", "final_result", "未知的任务结论 "+string(r))
	}
}

// Task 是某个需求的一次完整执行，拥有独立的容器、步骤序列与执行尝试。
type Task struct {
	ID            ID         `json:"id"`
	RequirementID ID         `json:"requirement_id"`
	Status        TaskStatus `json:"status"`
	// HostID 是执行节点；本机执行时为空。
	HostID *ID `json:"host_id,omitempty"`
	// Config 是任务的工作流配置，不包含任何凭据。
	Config TaskConfig `json:"config"`
	// RepositoryPath 是仓库位置：远程任务保存脱敏后的仓库地址，本机任务保存本地路径。
	RepositoryPath string `json:"repository_path"`
	// BaseRef 是基础分支。
	BaseRef string `json:"base_ref"`
	// WorktreePath 是工作区路径：远程任务为容器内目录。
	WorktreePath string `json:"worktree_path"`
	// ContainerID 是当前任务容器登记记录 ID。
	ContainerID string `json:"container_id,omitempty"`
	// CurrentStepID 是当前正在执行的步骤。
	CurrentStepID *ID `json:"current_step_id,omitempty"`
	// CurrentAttemptID 是当前正在执行的尝试。
	CurrentAttemptID *ID `json:"current_attempt_id,omitempty"`
	// CancelRequested 表示已请求取消。
	CancelRequested bool `json:"cancel_requested"`
	// CancelRequestedAt 是取消请求时间。
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
	// FinalResult 是任务结论，仅在接受最终状态时写入。
	FinalResult TaskResult `json:"final_result,omitempty"`
	// FailureSummary 是最终失败原因摘要。
	FailureSummary string `json:"failure_summary,omitempty"`
	// IdempotencyKey 是客户端请求标识，用于阻止同一请求重复创建任务。
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// LeaseOwner 是当前持有任务租约的执行者标识。
	LeaseOwner string `json:"lease_owner,omitempty"`
	// LeaseExpiresAt 是租约到期时间，过期后允许其他执行者接管。
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// NewTask 创建并校验任务。
//
// repositoryPath 与 baseRef 在创建任务时即确定；worktreePath 在工作区创建成功前可以为空。
// 同一个需求可以拥有多次执行记录，模型不限制两者一对一。
func NewTask(requirementID ID, repositoryPath, baseRef, worktreePath string, at time.Time) (Task, error) {
	createdAt := normalizeTime(at)
	task := Task{
		ID:             NewID(),
		RequirementID:  requirementID,
		Status:         TaskStatusQueued,
		RepositoryPath: strings.TrimSpace(repositoryPath),
		BaseRef:        strings.TrimSpace(baseRef),
		WorktreePath:   strings.TrimSpace(worktreePath),
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}
	if err := task.Validate(); err != nil {
		return Task{}, err
	}
	return task, nil
}

// Validate 校验任务定义是否完整。
func (t Task) Validate() error {
	if err := t.ID.Validate(); err != nil {
		return err
	}
	if err := t.RequirementID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(t.RepositoryPath) == "" {
		return invalidField("Task", "repository_path", "仓库路径不能为空")
	}
	if strings.TrimSpace(t.BaseRef) == "" {
		return invalidField("Task", "base_ref", "基准引用不能为空")
	}
	if t.HostID != nil {
		if err := t.HostID.Validate(); err != nil {
			return err
		}
	}
	if !t.Config.IsZero() {
		if err := t.Config.Validate(); err != nil {
			return err
		}
	}
	if err := t.Status.Validate(); err != nil {
		return err
	}
	if err := t.FinalResult.Validate(); err != nil {
		return err
	}
	if t.CurrentStepID != nil {
		if err := t.CurrentStepID.Validate(); err != nil {
			return err
		}
	}
	if t.CurrentAttemptID != nil {
		if err := t.CurrentAttemptID.Validate(); err != nil {
			return err
		}
		if t.CurrentStepID == nil {
			return invalidField("Task", "current_attempt_id", "关联尝试时必须同时指定步骤")
		}
	}
	if t.CancelRequested && t.CancelRequestedAt == nil {
		return invalidField("Task", "cancel_requested_at", "取消请求必须记录时间")
	}
	if t.LeaseOwner != "" && t.LeaseExpiresAt == nil {
		return invalidField("Task", "lease_expires_at", "持有租约时必须记录到期时间")
	}
	return nil
}

// Transition 将任务迁移到下一个状态。
func (t *Task) Transition(next TaskStatus, at time.Time) error {
	if t == nil {
		return invalidField("Task", "", "实体为空")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := taskStatusMachine.validate(string(t.Status), string(next)); err != nil {
		return err
	}
	now := normalizeTime(at)
	t.Status = next
	t.UpdatedAt = now
	t.applyTimestamps(next, now)
	return nil
}

// applyTimestamps 根据迁移后的状态维护开始与结束时间。
func (t *Task) applyTimestamps(status TaskStatus, at time.Time) {
	if status.IsActive() || status == TaskStatusPaused {
		if t.StartedAt == nil {
			t.StartedAt = &at
		}
	}
	switch status {
	case TaskStatusQueued:
		t.StartedAt = nil
		t.FinishedAt = nil
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		t.FinishedAt = &at
	default:
		t.FinishedAt = nil
	}
}

// SetCurrentStep 记录任务当前正在执行的步骤。
func (t *Task) SetCurrentStep(stepID *ID) error {
	if t == nil {
		return invalidField("Task", "", "实体为空")
	}
	if stepID != nil {
		if err := stepID.Validate(); err != nil {
			return err
		}
	}
	t.CurrentStepID = stepID
	if stepID == nil {
		t.CurrentAttemptID = nil
	}
	return nil
}

// SetCurrentAttempt 记录任务当前正在执行的尝试。
func (t *Task) SetCurrentAttempt(attemptID *ID) error {
	if t == nil {
		return invalidField("Task", "", "实体为空")
	}
	if attemptID == nil {
		t.CurrentAttemptID = nil
		return nil
	}
	if err := attemptID.Validate(); err != nil {
		return err
	}
	if t.CurrentStepID == nil {
		return invalidField("Task", "current_attempt_id", "关联尝试时必须同时指定步骤")
	}
	t.CurrentAttemptID = attemptID
	return nil
}

// SetWorktreePath 记录任务工作区路径。
func (t *Task) SetWorktreePath(path string) error {
	if t == nil {
		return invalidField("Task", "", "实体为空")
	}
	if strings.TrimSpace(path) == "" {
		return invalidField("Task", "worktree_path", "工作区路径不能为空")
	}
	t.WorktreePath = strings.TrimSpace(path)
	return nil
}

// SetContainer 记录任务当前容器登记记录 ID。
func (t *Task) SetContainer(containerID string) error {
	if t == nil {
		return invalidField("Task", "", "实体为空")
	}
	t.ContainerID = strings.TrimSpace(containerID)
	return nil
}

// RequestCancel 记录取消请求。
//
// 取消请求先持久化，再由执行者把任务推进到 cancelling；本方法不直接改变状态。
func (t *Task) RequestCancel(at time.Time) error {
	if t == nil {
		return invalidField("Task", "", "实体为空")
	}
	if t.Status.IsTerminal() {
		return invalidField("Task", "status", "已结束的任务不能请求取消")
	}
	if t.CancelRequested {
		return nil
	}
	now := normalizeTime(at)
	t.CancelRequested = true
	t.CancelRequestedAt = &now
	t.UpdatedAt = now
	return nil
}

// Finish 写入任务最终结论。
//
// 只有完成必要业务判断后才可以调用：结论与状态必须同时由工作流引擎给出。
func (t *Task) Finish(result TaskResult, summary string, at time.Time) error {
	if t == nil {
		return invalidField("Task", "", "实体为空")
	}
	if err := result.Validate(); err != nil {
		return err
	}
	if result == TaskResultNone {
		return invalidField("Task", "final_result", "最终结论不能为空")
	}
	now := normalizeTime(at)
	t.FinalResult = result
	t.FailureSummary = strings.TrimSpace(summary)
	t.UpdatedAt = now
	if t.FinishedAt == nil {
		t.FinishedAt = &now
	}
	return nil
}

// LeaseActive 判断任务租约在给定时刻是否仍然有效。
func (t Task) LeaseActive(at time.Time) bool {
	if strings.TrimSpace(t.LeaseOwner) == "" || t.LeaseExpiresAt == nil {
		return false
	}
	return t.LeaseExpiresAt.After(normalizeTime(at))
}
