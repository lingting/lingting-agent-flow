package domain

import (
	"fmt"
	"strings"
	"time"
)

// SessionStatus 表示一次 Pi Agent 会话的状态。
type SessionStatus string

// 会话状态。
//
// starting 覆盖进程启动与握手阶段；interrupted 表示编排器或宿主进程异常结束。
const (
	SessionStatusStarting    SessionStatus = "starting"
	SessionStatusRunning     SessionStatus = "running"
	SessionStatusCompleted   SessionStatus = "completed"
	SessionStatusFailed      SessionStatus = "failed"
	SessionStatusCancelled   SessionStatus = "cancelled"
	SessionStatusInterrupted SessionStatus = "interrupted"
)

// sessionStatusMachine 定义会话状态迁移。
//
// starting 覆盖进程启动与握手阶段，任何异常退出都必须落到 failed、cancelled 或 interrupted。
var sessionStatusMachine = statusMachine{
	entity: "Session",
	transitions: map[string][]string{
		string(SessionStatusStarting): {
			string(SessionStatusRunning),
			string(SessionStatusFailed),
			string(SessionStatusCancelled),
			string(SessionStatusInterrupted),
		},
		string(SessionStatusRunning): {
			string(SessionStatusCompleted),
			string(SessionStatusFailed),
			string(SessionStatusCancelled),
			string(SessionStatusInterrupted),
		},
	},
	terminal: []string{
		string(SessionStatusCompleted),
		string(SessionStatusFailed),
		string(SessionStatusCancelled),
		string(SessionStatusInterrupted),
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

// SessionScope 描述会话在任务、步骤与尝试上的归属。
//
// Planner 等任务级会话没有步骤与尝试；Developer 与 Validator 会话必须同时给出
// 步骤与尝试。归属关系是否与尝试一致，由存储层在事务内校验。
type SessionScope struct {
	TaskID    ID
	StepID    *ID
	AttemptID *ID
}

// Validate 校验会话归属是否自洽。
func (s SessionScope) Validate() error {
	if err := s.TaskID.Validate(); err != nil {
		return err
	}
	if s.StepID != nil {
		if err := s.StepID.Validate(); err != nil {
			return err
		}
	}
	if s.AttemptID != nil {
		if err := s.AttemptID.Validate(); err != nil {
			return err
		}
		if s.StepID == nil {
			return invalidField("Session", "step_id", "关联尝试时必须同时指定步骤")
		}
	}
	return nil
}

// Session 是一次具体的 Pi Agent 会话。
//
// 每次 Agent 执行都创建独立 Session，默认不在不同步骤之间复用会话历史。
// 外部 Pi 会话标识不得代替 AgentFlow 内部主键。
type Session struct {
	ID                ID            `json:"id"`
	TaskID            ID            `json:"task_id"`
	StepID            *ID           `json:"step_id,omitempty"`
	AttemptID         *ID           `json:"attempt_id,omitempty"`
	RoleID            ID            `json:"role_id"`
	RoleSnapshot      RoleSnapshot  `json:"role_snapshot"`
	Provider          string        `json:"provider"`
	Model             string        `json:"model"`
	ExternalSessionID string        `json:"external_session_id,omitempty"`
	ProcessID         *int          `json:"process_id,omitempty"`
	Status            SessionStatus `json:"status"`
	// JSONLPath 是原始 Session 文件在本机的持久化路径。
	JSONLPath string `json:"session_jsonl_path,omitempty"`
	// HTMLPath 是 Session 导出的 HTML 在本机的持久化路径。
	HTMLPath string `json:"session_html_path,omitempty"`
	// RunStatus 是本次执行自身的运行结果。
	RunStatus SessionRunStatus `json:"run_status,omitempty"`
	// Completeness 是 Session 文件的完整性状态。
	Completeness SessionCompleteness `json:"completeness,omitempty"`
	// ExportStatus 是 HTML 导出状态。
	ExportStatus SessionExportStatus `json:"export_status,omitempty"`
	// ArchiveStatus 是原始 Session 文件的归档状态。
	//
	// 归档独立于执行状态与导出状态：原始文件没保存下来不代表 Agent 调用失败，
	// HTML 导出失败也不影响归档成功的事实。
	ArchiveStatus SessionArchiveStatus `json:"archive_status,omitempty"`
	// ArchivePath 是归档后的原始 Session 文件位置，未归档时为空。
	ArchivePath string `json:"archive_path,omitempty"`
	// StdoutPath 是标准输出日志在本机的位置，为空表示未落盘。
	StdoutPath string `json:"stdout_path,omitempty"`
	// StderrPath 是标准错误日志在本机的位置，为空表示未落盘。
	StderrPath string `json:"stderr_path,omitempty"`
	// ExitCode 是 Agent 进程退出码，未能取得时为空。
	ExitCode       *int   `json:"exit_code,omitempty"`
	FailureSummary string `json:"failure_summary,omitempty"`
	// InvocationID 是本次 Agent 调用的唯一标识，用于区分同一会话下的多次调用。
	InvocationID string `json:"invocation_id,omitempty"`
	// ContainerID 是执行本次调用的容器登记记录 ID，本机执行时为空。
	ContainerID string `json:"container_id,omitempty"`
	// DockerID 是执行本次调用的 Docker 容器 ID，本机执行时为空。
	DockerID string `json:"docker_id,omitempty"`
	// StartedAt 是执行开始时间。
	StartedAt time.Time
	// FinishedAt 是执行结束时间。
	FinishedAt *time.Time
	// CreatedAt 是记录创建时间。
	CreatedAt time.Time
}

// NewSession 创建并校验会话。
//
// role 是实际执行时采用的角色，其快照会随会话一并持久化。
func NewSession(scope SessionScope, role Role, provider, model string, at time.Time) (Session, error) {
	startedAt := normalizeTime(at)
	session := Session{
		ID:           NewID(),
		TaskID:       scope.TaskID,
		StepID:       scope.StepID,
		AttemptID:    scope.AttemptID,
		RoleID:       role.ID,
		RoleSnapshot: role.Snapshot(),
		Provider:     strings.TrimSpace(provider),
		Model:        strings.TrimSpace(model),
		Status:       SessionStatusStarting,
		StartedAt:    startedAt,
		CreatedAt:    startedAt,
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
	scope := SessionScope{TaskID: s.TaskID, StepID: s.StepID, AttemptID: s.AttemptID}
	if err := scope.Validate(); err != nil {
		return err
	}
	if err := s.RoleID.Validate(); err != nil {
		return err
	}
	if err := s.RoleSnapshot.Validate(); err != nil {
		return err
	}
	if s.RoleSnapshot.RoleID != s.RoleID {
		return invalidField("Session", "role_snapshot", "角色快照与角色标识不一致")
	}
	if strings.TrimSpace(s.Provider) == "" {
		return invalidField("Session", "provider", "Provider 不能为空")
	}
	if strings.TrimSpace(s.Model) == "" {
		return invalidField("Session", "model", "模型不能为空")
	}
	if err := s.Status.Validate(); err != nil {
		return err
	}
	return s.validateArtifacts()
}

// validateArtifacts 校验可选的产出元数据：未记录时跳过，记录时必须自洽。
func (s Session) validateArtifacts() error {
	// 未记录归档结果时跳过：空值表示"还没有归档结论"，不是非法取值。
	if s.ArchiveStatus != "" {
		if err := s.ArchiveStatus.Validate(); err != nil {
			return err
		}
	}
	// 归档成功必须给出归档位置，否则无法定位原始会话文件。
	if s.ArchiveStatus == SessionArchiveSucceeded && strings.TrimSpace(s.ArchivePath) == "" {
		return invalidField("Session", "archive_path", "归档成功时必须给出归档位置")
	}
	if s.RunStatus == "" && s.Completeness == "" && s.ExportStatus == "" {
		return nil
	}
	return SessionArtifacts{
		JSONLPath:      s.JSONLPath,
		HTMLPath:       s.HTMLPath,
		RunStatus:      s.RunStatus,
		Completeness:   s.Completeness,
		ExportStatus:   s.ExportStatus,
		ExitCode:       exitCodeOrMinusOne(s.ExitCode),
		FailureSummary: s.FailureSummary,
	}.Validate()
}

// exitCodeOrMinusOne 把可空退出码转换为退出码，未知时返回 -1。
func exitCodeOrMinusOne(code *int) int {
	if code == nil {
		return -1
	}
	return *code
}

// MarkRunning 标记会话已进入运行状态，并记录 Pi 侧会话标识。
func (s *Session) MarkRunning(reference string) error {
	if s == nil {
		return invalidField("Session", "", "实体为空")
	}
	if err := sessionStatusMachine.validate(string(s.Status), string(SessionStatusRunning)); err != nil {
		return err
	}
	s.Status = SessionStatusRunning
	s.ExternalSessionID = strings.TrimSpace(reference)
	return nil
}

// SetProcessID 记录承载会话的外部进程 ID。
func (s *Session) SetProcessID(processID int) error {
	if s == nil {
		return invalidField("Session", "", "实体为空")
	}
	if processID <= 0 {
		return invalidField("Session", "process_id", "进程 ID 必须大于 0")
	}
	s.ProcessID = &processID
	return nil
}

// Complete 标记会话正常结束。
func (s *Session) Complete(at time.Time) error {
	return s.finish(SessionStatusCompleted, at)
}

// Fail 标记会话异常结束。
func (s *Session) Fail(at time.Time) error {
	return s.finish(SessionStatusFailed, at)
}

// Cancel 标记会话被用户或编排器取消。
func (s *Session) Cancel(at time.Time) error {
	return s.finish(SessionStatusCancelled, at)
}

// Interrupt 标记会话因编排器或宿主进程异常结束。
func (s *Session) Interrupt(at time.Time) error {
	return s.finish(SessionStatusInterrupted, at)
}

// ApplyArchive 记录原始会话文件的归档结果。
//
// 归档可以在会话结束之后单独推进（例如先保存原始 JSONL，稍后再完成 HTML 导出），
// 因此它不参与会话状态迁移：归档失败不会把已成功的调用改写为失败，归档成功也不会
// 把失败的调用改写为成功。
//
// 归档结果只允许从"未归档"推进到成功或失败；已有结论不重复改写，避免迟到的归档
// 任务覆盖已经确认的事实。
func (s *Session) ApplyArchive(status SessionArchiveStatus, path string) error {
	if s == nil {
		return invalidField("Session", "", "实体为空")
	}
	if err := status.Validate(); err != nil {
		return err
	}
	if status == SessionArchiveNotAttempted {
		return invalidField("Session", "archive_status", "归档结果不能写回未归档")
	}
	if s.ArchiveStatus != "" && s.ArchiveStatus != SessionArchiveNotAttempted {
		return fmt.Errorf(
			"%w: 会话 %s 的归档结果已确认为 %s，不能改写为 %s",
			ErrInvalidState,
			s.ID,
			s.ArchiveStatus,
			status,
		)
	}
	trimmed := strings.TrimSpace(path)
	if status == SessionArchiveSucceeded && trimmed == "" {
		return invalidField("Session", "archive_path", "归档成功时必须给出归档位置")
	}
	s.ArchiveStatus = status
	s.ArchivePath = trimmed
	return nil
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
