package domain

import (
	"strings"
	"time"
)

// TaskStatus 表示需求的一次完整执行所处的阶段。
type TaskStatus string

// 任务状态。
const (
	TaskStatusQueued     TaskStatus = "queued"
	TaskStatusPlanning   TaskStatus = "planning"
	TaskStatusPlanned    TaskStatus = "planned"
	TaskStatusDeveloping TaskStatus = "developing"
	TaskStatusVerifying  TaskStatus = "verifying"
	TaskStatusRetrying   TaskStatus = "retrying"
	TaskStatusPaused     TaskStatus = "paused"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusCancelled  TaskStatus = "cancelled"
)

// taskStatusMachine 定义任务状态迁移。
//
// paused 可以回到任一执行阶段，用于人工介入后继续；failed 允许回到 planning，
// 用于用户显式触发的任务重试。重试必须创建新的 Attempt，不覆盖历史记录。
var taskStatusMachine = statusMachine{
	entity: "Task",
	transitions: map[string][]string{
		string(TaskStatusQueued): {
			string(TaskStatusPlanning),
			string(TaskStatusCancelled),
		},
		string(TaskStatusPlanning): {
			string(TaskStatusPlanned),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelled),
		},
		string(TaskStatusPlanned): {
			string(TaskStatusDeveloping),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelled),
		},
		string(TaskStatusDeveloping): {
			string(TaskStatusVerifying),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelled),
		},
		string(TaskStatusVerifying): {
			string(TaskStatusRetrying),
			string(TaskStatusCompleted),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelled),
		},
		string(TaskStatusRetrying): {
			string(TaskStatusDeveloping),
			string(TaskStatusFailed),
			string(TaskStatusPaused),
			string(TaskStatusCancelled),
		},
		string(TaskStatusPaused): {
			string(TaskStatusPlanning),
			string(TaskStatusDeveloping),
			string(TaskStatusVerifying),
			string(TaskStatusRetrying),
			string(TaskStatusCancelled),
		},
		string(TaskStatusFailed): {
			string(TaskStatusPlanning),
			string(TaskStatusCancelled),
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
	case TaskStatusPlanning, TaskStatusPlanned, TaskStatusDeveloping,
		TaskStatusVerifying, TaskStatusRetrying:
		return true
	default:
		return false
	}
}

// Task 是某个需求的一次完整执行，拥有独立工作区与步骤序列。
type Task struct {
	ID             ID         `json:"id"`
	RequirementID  ID         `json:"requirement_id"`
	Status         TaskStatus `json:"status"`
	RepositoryPath string     `json:"repository_path"`
	BaseRef        string     `json:"base_ref"`
	WorktreePath   string     `json:"worktree_path"`
	CurrentStepID  *ID        `json:"current_step_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// NewTask 创建并校验任务。
//
// repositoryPath 与 baseRef 在创建任务时即确定；worktreePath 在 Worktree 创建成功前可以为空。
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
	return t.Status.Validate()
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
