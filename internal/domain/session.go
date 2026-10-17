package domain

import (
	"strings"
	"time"
)

// SessionStatus 表示一次 Pi Agent 会话的状态。
type SessionStatus string

// 会话状态。
const (
	SessionStatusStarting SessionStatus = "starting"
	SessionStatusRunning  SessionStatus = "running"
	SessionStatusClosed   SessionStatus = "closed"
	SessionStatusFailed   SessionStatus = "failed"
	SessionStatusAborted  SessionStatus = "aborted"
)

// sessionStatusMachine 定义会话状态迁移。
//
// starting 覆盖进程启动与握手阶段，任何异常退出都必须落到 failed 或 aborted。
var sessionStatusMachine = statusMachine{
	entity: "Session",
	transitions: map[string][]string{
		string(SessionStatusStarting): {
			string(SessionStatusRunning),
			string(SessionStatusFailed),
			string(SessionStatusAborted),
		},
		string(SessionStatusRunning): {
			string(SessionStatusClosed),
			string(SessionStatusFailed),
			string(SessionStatusAborted),
		},
	},
	terminal: []string{
		string(SessionStatusClosed),
		string(SessionStatusFailed),
		string(SessionStatusAborted),
	},
}

// Validate 校验会话状态是否属于已知状态。
func (s SessionStatus) Validate() error {
	if sessionStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("Session", "status", "未知状态 "+string(s))
}

// IsTerminal 判断会话状态是否为终态。
func (s SessionStatus) IsTerminal() bool {
	return sessionStatusMachine.isTerminal(string(s))
}

// Session 是一次具体的 Pi Agent 会话。
//
// 每次 Agent 执行都创建独立 Session，默认不在不同步骤之间复用会话历史。
type Session struct {
	ID                 ID            `json:"id"`
	AttemptID          ID            `json:"attempt_id"`
	RoleID             ID            `json:"role_id"`
	Provider           string        `json:"provider"`
	Model              string        `json:"model"`
	PiSessionReference string        `json:"pi_session_reference,omitempty"`
	Status             SessionStatus `json:"status"`
	StartedAt          time.Time     `json:"started_at"`
	FinishedAt         *time.Time    `json:"finished_at,omitempty"`
}

// NewSession 创建并校验会话。
func NewSession(attemptID, roleID ID, provider, model string, at time.Time) (Session, error) {
	session := Session{
		ID:        NewID(),
		AttemptID: attemptID,
		RoleID:    roleID,
		Provider:  strings.TrimSpace(provider),
		Model:     strings.TrimSpace(model),
		Status:    SessionStatusStarting,
		StartedAt: normalizeTime(at),
	}
	if err := session.Validate(); err != nil {
		return Session{}, err
	}
	return session, nil
}

// Validate 校验会话定义是否完整。
func (s Session) Validate() error {
	if err := s.ID.Validate(); err != nil {
		return err
	}
	if err := s.AttemptID.Validate(); err != nil {
		return err
	}
	if err := s.RoleID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(s.Provider) == "" {
		return invalidField("Session", "provider", "Provider 不能为空")
	}
	if strings.TrimSpace(s.Model) == "" {
		return invalidField("Session", "model", "模型不能为空")
	}
	return s.Status.Validate()
}

// MarkRunning 标记会话已进入运行状态，并记录 Pi 侧会话引用。
func (s *Session) MarkRunning(reference string) error {
	if s == nil {
		return invalidField("Session", "", "实体为空")
	}
	if err := sessionStatusMachine.validate(string(s.Status), string(SessionStatusRunning)); err != nil {
		return err
	}
	s.Status = SessionStatusRunning
	s.PiSessionReference = strings.TrimSpace(reference)
	return nil
}

// Close 标记会话正常结束。
func (s *Session) Close(at time.Time) error {
	return s.finish(SessionStatusClosed, at)
}

// Fail 标记会话异常结束。
func (s *Session) Fail(at time.Time) error {
	return s.finish(SessionStatusFailed, at)
}

// Abort 标记会话被用户或编排器中止。
func (s *Session) Abort(at time.Time) error {
	return s.finish(SessionStatusAborted, at)
}

// finish 执行会话的结束迁移。
func (s *Session) finish(status SessionStatus, at time.Time) error {
	if s == nil {
		return invalidField("Session", "", "实体为空")
	}
	if err := sessionStatusMachine.validate(string(s.Status), string(status)); err != nil {
		return err
	}
	finishedAt := normalizeTime(at)
	s.Status = status
	s.FinishedAt = &finishedAt
	return nil
}
