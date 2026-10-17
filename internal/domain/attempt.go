package domain

import (
	"strings"
	"time"
)

// AttemptStatus 表示某次步骤执行尝试的状态。
type AttemptStatus string

// 尝试状态。
const (
	AttemptStatusRunning   AttemptStatus = "running"
	AttemptStatusSucceeded AttemptStatus = "succeeded"
	AttemptStatusFailed    AttemptStatus = "failed"
	AttemptStatusCancelled AttemptStatus = "cancelled"
)

// attemptStatusMachine 定义尝试状态迁移。
//
// 一次尝试只能结束一次，结束后不再迁移。
var attemptStatusMachine = statusMachine{
	entity: "Attempt",
	transitions: map[string][]string{
		string(AttemptStatusRunning): {
			string(AttemptStatusSucceeded),
			string(AttemptStatusFailed),
			string(AttemptStatusCancelled),
		},
	},
	terminal: []string{
		string(AttemptStatusSucceeded),
		string(AttemptStatusFailed),
		string(AttemptStatusCancelled),
	},
}

// Validate 校验尝试状态是否属于已知状态。
func (s AttemptStatus) Validate() error {
	if attemptStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("Attempt", "status", "未知状态 "+string(s))
}

// IsTerminal 判断尝试状态是否为终态。
func (s AttemptStatus) IsTerminal() bool {
	return attemptStatusMachine.isTerminal(string(s))
}

// Attempt 是某个步骤的一次具体执行尝试。
//
// 每次重试都会创建新的 Attempt，并保留此前的执行记录。
type Attempt struct {
	ID             ID            `json:"id"`
	StepID         ID            `json:"step_id"`
	AttemptNumber  int           `json:"attempt_number"`
	Status         AttemptStatus `json:"status"`
	FailureSummary string        `json:"failure_summary,omitempty"`
	StartedAt      time.Time     `json:"started_at"`
	FinishedAt     *time.Time    `json:"finished_at,omitempty"`
}

// NewAttempt 创建并校验执行尝试。
//
// attemptNumber 必须从 1 开始，并由调用方保证同一步骤内递增且不重复。
func NewAttempt(stepID ID, attemptNumber int, at time.Time) (Attempt, error) {
	attempt := Attempt{
		ID:            NewID(),
		StepID:        stepID,
		AttemptNumber: attemptNumber,
		Status:        AttemptStatusRunning,
		StartedAt:     normalizeTime(at),
	}
	if err := attempt.Validate(); err != nil {
		return Attempt{}, err
	}
	return attempt, nil
}

// Validate 校验执行尝试定义是否完整。
func (a Attempt) Validate() error {
	if err := a.ID.Validate(); err != nil {
		return err
	}
	if err := a.StepID.Validate(); err != nil {
		return err
	}
	if a.AttemptNumber < 1 {
		return invalidField("Attempt", "attempt_number", "尝试序号必须从 1 开始")
	}
	return a.Status.Validate()
}

// Succeed 标记尝试成功结束。
func (a *Attempt) Succeed(at time.Time) error {
	return a.finish(AttemptStatusSucceeded, "", at)
}

// Fail 标记尝试失败结束，并记录失败摘要。
func (a *Attempt) Fail(summary string, at time.Time) error {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return invalidField("Attempt", "failure_summary", "失败摘要不能为空")
	}
	return a.finish(AttemptStatusFailed, trimmed, at)
}

// Cancel 标记尝试被取消结束。
func (a *Attempt) Cancel(summary string, at time.Time) error {
	return a.finish(AttemptStatusCancelled, strings.TrimSpace(summary), at)
}

// finish 执行尝试的结束迁移。
func (a *Attempt) finish(status AttemptStatus, summary string, at time.Time) error {
	if a == nil {
		return invalidField("Attempt", "", "实体为空")
	}
	if err := attemptStatusMachine.validate(string(a.Status), string(status)); err != nil {
		return err
	}
	finishedAt := normalizeTime(at)
	a.Status = status
	a.FailureSummary = summary
	a.FinishedAt = &finishedAt
	return nil
}
