package domain

import (
	"strings"
	"time"
)

// TaskStatus 表示需求的一次完整执行所处的阶段。
type TaskStatus string

// 任务状态。
const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

// taskStatusMachine 定义任务状态迁移。
//
// failed 允许回到 running，用于用户显式触发的任务重试。
var taskStatusMachine = statusMachine{
	entity: "Task",
	transitions: map[string][]string{
		string(TaskStatusPending): {
			string(TaskStatusRunning),
			string(TaskStatusCancelled),
		},
		string(TaskStatusRunning): {
			string(TaskStatusCompleted),
			string(TaskStatusFailed),
			string(TaskStatusCancelled),
		},
		string(TaskStatusFailed): {
			string(TaskStatusRunning),
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
	return s == TaskStatusRunning
}

// Task 是某个需求的一次完整执行，拥有独立工作区与步骤序列。
type Task struct {
	ID            ID         `json:"id"`
	RequirementID ID         `json:"requirement_id"`
	Status        TaskStatus `json:"status"`
	WorktreePath  string     `json:"worktree_path"`
	CurrentStepID *ID        `json:"current_step_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// NewTask 创建并校验任务。
//
// 第一版默认一个需求对应一个任务，但模型本身不限制两者一对一。
func NewTask(requirementID ID, worktreePath string, at time.Time) (Task, error) {
	task := Task{
		ID:            NewID(),
		RequirementID: requirementID,
		Status:        TaskStatusPending,
		WorktreePath:  strings.TrimSpace(worktreePath),
		CreatedAt:     normalizeTime(at),
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
	t.Status = next
	t.applyTimestamps(next, normalizeTime(at))
	return nil
}

// applyTimestamps 根据迁移后的状态维护开始与结束时间。
func (t *Task) applyTimestamps(status TaskStatus, at time.Time) {
	switch status {
	case TaskStatusRunning:
		if t.StartedAt == nil {
			t.StartedAt = &at
		}
		t.FinishedAt = nil
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		t.FinishedAt = &at
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
