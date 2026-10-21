package domain

import (
	"strings"
	"time"
)

// AttemptStatus 表示某次步骤执行尝试的状态。
//
// AttemptStatus 表示某次步骤执行尝试的状态。
type AttemptStatus string

// 尝试状态。
//
// interrupted 表示编排器或宿主进程异常结束，未能给出正常的完成或失败结论；
// unknown 表示远程副作用的结果无法确认，必须先核验再决定下一步。
const (
	AttemptStatusRunning          AttemptStatus = "running"
	AttemptStatusCompleted        AttemptStatus = "completed"
	AttemptStatusFailed           AttemptStatus = "failed"
	AttemptStatusCancelled        AttemptStatus = "cancelled"
	AttemptStatusInterrupted      AttemptStatus = "interrupted"
	AttemptStatusTimedOut         AttemptStatus = "timed_out"
	AttemptStatusUnknown          AttemptStatus = "unknown"
	AttemptStatusRecoveryRequired AttemptStatus = "recovery_required"
)

// attemptStatusMachine 定义尝试状态迁移。
//
// 一次尝试只能结束一次；结束后不再迁移为 running，历史记录不会被覆盖。
// unknown 与 recovery_required 可以在核验后收敛为确定状态。
var attemptStatusMachine = statusMachine{
	entity: "Attempt",
	transitions: map[string][]string{
		string(AttemptStatusRunning): {
			string(AttemptStatusCompleted),
			string(AttemptStatusFailed),
			string(AttemptStatusCancelled),
			string(AttemptStatusInterrupted),
			string(AttemptStatusTimedOut),
			string(AttemptStatusUnknown),
			string(AttemptStatusRecoveryRequired),
		},
		string(AttemptStatusUnknown): {
			string(AttemptStatusCompleted),
			string(AttemptStatusFailed),
			string(AttemptStatusInterrupted),
			string(AttemptStatusRecoveryRequired),
		},
		string(AttemptStatusRecoveryRequired): {
			string(AttemptStatusCompleted),
			string(AttemptStatusFailed),
			string(AttemptStatusInterrupted),
			string(AttemptStatusUnknown),
		},
	},
	terminal: []string{
		string(AttemptStatusCompleted),
		string(AttemptStatusFailed),
		string(AttemptStatusCancelled),
		string(AttemptStatusInterrupted),
		string(AttemptStatusTimedOut),
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

// Succeeded 判断尝试是否成功结束。
func (s AttemptStatus) Succeeded() bool {
	return s == AttemptStatusCompleted
}

// VerificationVerdict 是验证的最终判定。
//
// passed 只在所有必需检查通过且没有阻止成功的问题时成立；failed 表示至少一个
// 必需检查失败或存在阻止成功的问题；inconclusive 表示验证无法完成或结果不确定。
type VerificationVerdict string

// 验证判定取值。
const (
	VerificationVerdictNone         VerificationVerdict = ""
	VerificationVerdictPassed       VerificationVerdict = "passed"
	VerificationVerdictFailed       VerificationVerdict = "failed"
	VerificationVerdictInconclusive VerificationVerdict = "inconclusive"
)

// Validate 校验验证判定是否受支持。
func (v VerificationVerdict) Validate() error {
	switch v {
	case VerificationVerdictNone,
		VerificationVerdictPassed,
		VerificationVerdictFailed,
		VerificationVerdictInconclusive:
		return nil
	default:
		return invalidField("Attempt", "verification_verdict", "未知的验证判定 "+string(v))
	}
}

// Attempt 是某个步骤的一次具体执行尝试。
//
// 每次重试都会创建新的 Attempt，并保留此前的执行记录：历史记录不会被覆盖，
// 也不会被复用为新的执行。
//
// 任务归属与步骤归属同时保存：数据库不声明外键，因此"尝试所属步骤属于该任务"
// 这一关系必须由服务层在写入前显式校验，不能只凭两个标识各自存在就认为合法。
type Attempt struct {
	ID ID `json:"id"`
	// TaskID 是本次尝试所属任务。
	TaskID ID `json:"task_id"`
	// StepID 是本次尝试所属步骤。
	StepID        ID            `json:"step_id"`
	AttemptNumber int           `json:"attempt_number"`
	Status        AttemptStatus `json:"status"`
	// ContainerID 是本次尝试使用的容器登记记录 ID。
	ContainerID string `json:"container_id,omitempty"`
	// DockerID 是本次尝试使用的 Docker 容器 ID。
	DockerID string `json:"docker_id,omitempty"`
	// InputDigest 是本次尝试输入配置的摘要。
	InputDigest string `json:"input_digest,omitempty"`
	// ExitCode 是本次尝试的主执行退出码，未能取得时为 -1。
	ExitCode int `json:"exit_code"`
	// TimedOut 表示本次尝试因超时结束。
	TimedOut bool `json:"timed_out,omitempty"`
	// Cancelled 表示本次尝试被取消。
	Cancelled bool `json:"cancelled,omitempty"`
	// RetryReason 是触发下一次重试的原因。
	RetryReason string `json:"retry_reason,omitempty"`
	// ErrorClass 是失败或重试原因的分类。
	ErrorClass string `json:"error_class,omitempty"`
	// FailureStage 是失败发生的阶段名。
	FailureStage string `json:"failure_stage,omitempty"`
	// FailureSummary 是失败或异常的可诊断摘要。
	FailureSummary string `json:"failure_summary,omitempty"`
	// VerificationVerdict 是本次尝试的验证判定。
	VerificationVerdict VerificationVerdict `json:"verification_verdict,omitempty"`
	// VerificationSummary 是验证结果摘要。
	VerificationSummary string `json:"verification_summary,omitempty"`
	// VerificationID 是最近一次验证结果记录 ID。
	VerificationID *ID `json:"verification_id,omitempty"`
	// SessionIDs 是本次尝试关联的全部 Agent 会话标识。
	SessionIDs []ID `json:"session_ids,omitempty"`
	// CodeSaveID 是本次尝试关联的代码保存记录 ID。
	CodeSaveID string `json:"code_save_id,omitempty"`
	// GitBranch 是本次执行所在的工作区分支名，未采集时为空。
	GitBranch string `json:"git_branch,omitempty"`
	// GitBefore 是执行前采集到的 Git 工作区状态。
	//
	// 为 nil 表示本次没有采集（例如采集能力缺失），与"采集成功且工作区干净"不同。
	GitBefore *GitWorktreeState `json:"git_before,omitempty"`
	// GitAfter 是执行后采集到的 Git 工作区状态。
	GitAfter   *GitWorktreeState `json:"git_after,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

// NewAttempt 创建并校验执行尝试。
//
// attemptNumber 必须从 1 开始，并由调用方保证同一步骤内递增且不重复；
// 序号的并发安全由存储层的 StartAttempt 保证，不能只靠"先查最大值再插入"。
//
// 尝试创建即为 running：Attempt 只在真正开始执行时创建，不存在预先占位的待执行
// 尝试。未调用真实执行器时不得创建 Attempt。
func NewAttempt(taskID, stepID ID, attemptNumber int, at time.Time) (Attempt, error) {
	startedAt := normalizeTime(at)
	attempt := Attempt{
		ID:            NewID(),
		TaskID:        taskID,
		StepID:        stepID,
		AttemptNumber: attemptNumber,
		Status:        AttemptStatusRunning,
		ExitCode:      -1,
		StartedAt:     startedAt,
		CreatedAt:     startedAt,
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
	if err := a.TaskID.Validate(); err != nil {
		return err
	}
	if err := a.StepID.Validate(); err != nil {
		return err
	}
	if a.AttemptNumber < 1 {
		return invalidField("Attempt", "attempt_number", "尝试序号必须从 1 开始")
	}
	if err := a.Status.Validate(); err != nil {
		return err
	}
	if err := a.VerificationVerdict.Validate(); err != nil {
		return err
	}
	if a.VerificationID != nil {
		if err := a.VerificationID.Validate(); err != nil {
			return err
		}
	}
	for _, id := range a.SessionIDs {
		if err := id.Validate(); err != nil {
			return err
		}
	}
	if branch := strings.TrimSpace(a.GitBranch); branch != "" {
		if err := ValidateGitBranchName(branch); err != nil {
			return err
		}
	}
	for _, state := range []*GitWorktreeState{a.GitBefore, a.GitAfter} {
		if state == nil {
			continue
		}
		if err := state.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// SetGitBefore 记录执行前的 Git 工作区状态与分支。
//
// 采集失败的状态同样被记录：未知状态必须可诊断，不能被当成"工作区干净"。
func (a *Attempt) SetGitBefore(state GitWorktreeState) error {
	if a == nil {
		return invalidField("Attempt", "", "实体为空")
	}
	if err := state.Validate(); err != nil {
		return err
	}
	if branch := strings.TrimSpace(state.Branch); branch != "" {
		if err := ValidateGitBranchName(branch); err != nil {
			return err
		}
	}
	copied := state
	a.GitBefore = &copied
	if branch := strings.TrimSpace(state.Branch); branch != "" {
		a.GitBranch = branch
	}
	return nil
}

// SetGitAfter 记录执行后的 Git 工作区状态。
func (a *Attempt) SetGitAfter(state GitWorktreeState) error {
	if a == nil {
		return invalidField("Attempt", "", "实体为空")
	}
	if err := state.Validate(); err != nil {
		return err
	}
	if branch := strings.TrimSpace(state.Branch); branch != "" {
		if err := ValidateGitBranchName(branch); err != nil {
			return err
		}
	}
	copied := state
	a.GitAfter = &copied
	if branch := strings.TrimSpace(state.Branch); branch != "" {
		a.GitBranch = branch
	}
	return nil
}

// Complete 标记尝试成功结束。
func (a *Attempt) Complete(at time.Time) error {
	return a.finish(AttemptStatusCompleted, "", at)
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
	if err := a.finish(AttemptStatusCancelled, strings.TrimSpace(summary), at); err != nil {
		return err
	}
	a.Cancelled = true
	return nil
}

// Interrupt 标记尝试因编排器或宿主进程异常结束。
func (a *Attempt) Interrupt(summary string, at time.Time) error {
	return a.finish(AttemptStatusInterrupted, strings.TrimSpace(summary), at)
}

// Timeout 标记尝试因超时结束。
func (a *Attempt) Timeout(summary string, at time.Time) error {
	if err := a.finish(AttemptStatusTimedOut, strings.TrimSpace(summary), at); err != nil {
		return err
	}
	a.TimedOut = true
	return nil
}

// MarkUnknown 标记尝试的远程结果无法确认。
func (a *Attempt) MarkUnknown(summary string, at time.Time) error {
	return a.finish(AttemptStatusUnknown, strings.TrimSpace(summary), at)
}

// RequireRecovery 标记尝试需要恢复核验。
func (a *Attempt) RequireRecovery(summary string, at time.Time) error {
	return a.finish(AttemptStatusRecoveryRequired, strings.TrimSpace(summary), at)
}

// Resolve 在核验后把不确定的尝试收敛为确定状态。
func (a *Attempt) Resolve(status AttemptStatus, summary string, at time.Time) error {
	switch status {
	case AttemptStatusCompleted, AttemptStatusFailed, AttemptStatusInterrupted:
	default:
		return invalidField("Attempt", "status", "核验只能收敛为 completed、failed 或 interrupted")
	}
	return a.finish(status, strings.TrimSpace(summary), at)
}

// SetVerification 记录本次尝试的验证判定。
//
// 判定只能由工作流引擎依据确定性验证结果与辅助评估结果给出，
// Agent 的自然语言结论不能直接写入该字段。
func (a *Attempt) SetVerification(verdict VerificationVerdict, summary string, resultID *ID) error {
	if a == nil {
		return invalidField("Attempt", "", "实体为空")
	}
	if err := verdict.Validate(); err != nil {
		return err
	}
	if verdict == VerificationVerdictNone {
		return invalidField("Attempt", "verification_verdict", "验证判定不能为空")
	}
	if resultID != nil {
		if err := resultID.Validate(); err != nil {
			return err
		}
	}
	a.VerificationVerdict = verdict
	a.VerificationSummary = strings.TrimSpace(summary)
	a.VerificationID = resultID
	return nil
}

// AddSession 把一次 Agent 调用会话关联到本次尝试。
//
// 同一会话标识重复添加时保持幂等。
func (a *Attempt) AddSession(sessionID ID) error {
	if a == nil {
		return invalidField("Attempt", "", "实体为空")
	}
	if err := sessionID.Validate(); err != nil {
		return err
	}
	for _, existing := range a.SessionIDs {
		if existing == sessionID {
			return nil
		}
	}
	a.SessionIDs = append(a.SessionIDs, sessionID)
	return nil
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
