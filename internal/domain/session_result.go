package domain

import (
	"strings"
	"time"
)

// SessionRunStatus 描述一次 Agent 执行自身的运行结果。
//
// 它不等于业务步骤的验收结果：执行正常结束只说明提示词已跑完，
// 步骤是否通过仍由验证阶段决定。
type SessionRunStatus string

// 执行运行结果取值。
const (
	// SessionRunCompleted 表示 Agent 进程正常结束。
	SessionRunCompleted SessionRunStatus = "completed"
	// SessionRunFailed 表示执行失败或进程异常退出。
	SessionRunFailed SessionRunStatus = "failed"
	// SessionRunCancelled 表示调用方取消了执行。
	SessionRunCancelled SessionRunStatus = "cancelled"
	// SessionRunTimedOut 表示执行超时后被终止。
	SessionRunTimedOut SessionRunStatus = "timed_out"
)

// Validate 校验运行结果取值是否受支持。
func (s SessionRunStatus) Validate() error {
	switch s {
	case SessionRunCompleted, SessionRunFailed, SessionRunCancelled, SessionRunTimedOut:
		return nil
	default:
		return invalidField("Session", "run_status", "未知运行结果 "+string(s))
	}
}

// SessionCompleteness 描述 Session 文件的完整性状态。
//
// 不能假设任何情况下都能得到完整 Session：超时、取消与异常退出都可能只留下
// 部分记录，缺失时则完全没有可用文件。
type SessionCompleteness string

// Session 完整性取值。
const (
	// SessionCompletenessComplete 表示正常结束且文件已保存并通过基本校验。
	SessionCompletenessComplete SessionCompleteness = "complete"
	// SessionCompletenessPartial 表示执行异常但仍有可保留的记录。
	SessionCompletenessPartial SessionCompleteness = "partial"
	// SessionCompletenessMissing 表示没有可用的 Session 文件。
	SessionCompletenessMissing SessionCompleteness = "missing"
	// SessionCompletenessExportFailed 表示原始 JSONL 已保存，但 HTML 导出失败。
	SessionCompletenessExportFailed SessionCompleteness = "export_failed"
)

// Validate 校验完整性取值是否受支持。
func (s SessionCompleteness) Validate() error {
	switch s {
	case SessionCompletenessComplete,
		SessionCompletenessPartial,
		SessionCompletenessMissing,
		SessionCompletenessExportFailed:
		return nil
	default:
		return invalidField("Session", "completeness", "未知完整性状态 "+string(s))
	}
}

// HasJSONL 判断该完整性状态是否保留了可用的原始 JSONL。
func (s SessionCompleteness) HasJSONL() bool {
	switch s {
	case SessionCompletenessComplete,
		SessionCompletenessPartial,
		SessionCompletenessExportFailed:
		return true
	default:
		return false
	}
}

// SessionExportStatus 描述 Session HTML 导出的结果。
type SessionExportStatus string

// HTML 导出状态取值。
const (
	// SessionExportNotAttempted 表示没有尝试导出，通常因为缺少原始 JSONL。
	SessionExportNotAttempted SessionExportStatus = "not_attempted"
	// SessionExportSucceeded 表示 HTML 已生成。
	SessionExportSucceeded SessionExportStatus = "succeeded"
	// SessionExportFailed 表示导出失败，原始 JSONL 不受影响。
	SessionExportFailed SessionExportStatus = "failed"
)

// Validate 校验导出状态是否受支持。
func (s SessionExportStatus) Validate() error {
	switch s {
	case SessionExportNotAttempted, SessionExportSucceeded, SessionExportFailed:
		return nil
	default:
		return invalidField("Session", "export_status", "未知导出状态 "+string(s))
	}
}

// SessionArtifacts 是一次会话执行产生的文件与结果元数据。
//
// 路径是执行环境内部的路径；本机持久化位置由调用方按同一套布局决定。
type SessionArtifacts struct {
	// JSONLPath 是原始 Session 文件路径，为空表示没有可用文件。
	JSONLPath string
	// HTMLPath 是导出的 HTML 路径，为空表示没有生成。
	HTMLPath string
	// RunStatus 是执行运行结果。
	RunStatus SessionRunStatus
	// Completeness 是 Session 文件完整性。
	Completeness SessionCompleteness
	// ExportStatus 是 HTML 导出状态。
	ExportStatus SessionExportStatus
	// ExitCode 是 Agent 进程退出码，未能取得时为 -1。
	ExitCode int
	// FailureSummary 是失败或异常的可诊断摘要。
	FailureSummary string
}

// Validate 校验会话产出元数据是否完整。
func (a SessionArtifacts) Validate() error {
	if err := a.RunStatus.Validate(); err != nil {
		return err
	}
	if err := a.Completeness.Validate(); err != nil {
		return err
	}
	if err := a.ExportStatus.Validate(); err != nil {
		return err
	}
	if a.Completeness.HasJSONL() && strings.TrimSpace(a.JSONLPath) == "" {
		return invalidField("Session", "session_jsonl_path", "保留 Session 时必须给出原始文件路径")
	}
	if a.ExportStatus == SessionExportSucceeded && strings.TrimSpace(a.HTMLPath) == "" {
		return invalidField("Session", "session_html_path", "导出成功时必须给出 HTML 路径")
	}
	return nil
}

// ApplyArtifacts 记录会话的产出元数据，并按运行结果结束会话。
//
// 会话状态与产出元数据一起写入，避免出现"状态已结束但文件信息缺失"的中间态。
func (s *Session) ApplyArtifacts(artifacts SessionArtifacts, at time.Time) error {
	if s == nil {
		return invalidField("Session", "", "实体为空")
	}
	if err := artifacts.Validate(); err != nil {
		return err
	}
	next := statusForRun(artifacts.RunStatus)
	if !s.Status.IsTerminal() {
		if err := s.finish(next, at); err != nil {
			return err
		}
	}
	s.JSONLPath = strings.TrimSpace(artifacts.JSONLPath)
	s.HTMLPath = strings.TrimSpace(artifacts.HTMLPath)
	s.RunStatus = artifacts.RunStatus
	s.Completeness = artifacts.Completeness
	s.ExportStatus = artifacts.ExportStatus
	exitCode := artifacts.ExitCode
	s.ExitCode = &exitCode
	s.FailureSummary = strings.TrimSpace(artifacts.FailureSummary)
	return nil
}

// statusForRun 把运行结果映射为会话状态。
func statusForRun(run SessionRunStatus) SessionStatus {
	switch run {
	case SessionRunCompleted:
		return SessionStatusCompleted
	case SessionRunCancelled:
		return SessionStatusCancelled
	default:
		return SessionStatusFailed
	}
}
