package agent

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxFinalTextLength 是 RunResult.FinalText 保留的最大字节数。
//
// 执行结果只用于摘要与诊断，完整输出应通过事件流与 Session 文件持久化。
const MaxFinalTextLength = 16 << 10

// RunStatus 描述执行器自身的运行结果。
//
// 它不代表业务步骤已经通过验收：Runner 正常完成提示词执行后，
// 校验阶段仍然可能判定失败。
type RunStatus string

// 执行器自身的运行结果。
const (
	// RunCompleted 表示 Agent 正常结束本轮执行。
	RunCompleted RunStatus = "completed"
	// RunFailed 表示执行失败、进程异常退出或 Session 提取失败。
	RunFailed RunStatus = "failed"
	// RunCancelled 表示调用方取消了执行。
	RunCancelled RunStatus = "cancelled"
	// RunTimedOut 表示执行超过限定时间后被终止。
	RunTimedOut RunStatus = "timed_out"
)

// Validate 校验运行状态是否受支持。
func (s RunStatus) Validate() error {
	switch s {
	case RunCompleted, RunFailed, RunCancelled, RunTimedOut:
		return nil
	default:
		return fmt.Errorf("agent: 未知运行状态 %q", string(s))
	}
}

// SessionCompleteness 描述 Session 文件的完整性状态。
//
// 任何情况下都不能假设一定得到完整 Session：超时、取消与异常退出都可能只留下
// 部分记录，缺失时则完全没有可用文件。
type SessionCompleteness string

// Session 完整性状态取值。
const (
	// SessionComplete 表示正常结束且文件已保存并通过基本校验。
	SessionComplete SessionCompleteness = "complete"
	// SessionPartial 表示执行异常但仍有可保留的记录。
	SessionPartial SessionCompleteness = "partial"
	// SessionMissing 表示没有可用的 Session 文件。
	SessionMissing SessionCompleteness = "missing"
	// SessionExportFailed 表示原始 JSONL 已保存，但 HTML 导出失败。
	SessionExportFailed SessionCompleteness = "export_failed"
)

// Validate 校验完整性状态是否受支持。
func (s SessionCompleteness) Validate() error {
	switch s {
	case SessionComplete, SessionPartial, SessionMissing, SessionExportFailed:
		return nil
	default:
		return fmt.Errorf("agent: 未知 Session 完整性状态 %q", string(s))
	}
}

// ExportStatus 描述 Session HTML 导出的结果。
type ExportStatus string

// HTML 导出状态取值。
const (
	// ExportNotAttempted 表示没有尝试导出，通常因为缺少原始 JSONL。
	ExportNotAttempted ExportStatus = "not_attempted"
	// ExportSucceeded 表示 HTML 已生成。
	ExportSucceeded ExportStatus = "succeeded"
	// ExportFailed 表示导出失败，原始 JSONL 不受影响。
	ExportFailed ExportStatus = "failed"
)

// Validate 校验导出状态是否受支持。
func (s ExportStatus) Validate() error {
	switch s {
	case ExportNotAttempted, ExportSucceeded, ExportFailed:
		return nil
	default:
		return fmt.Errorf("agent: 未知导出状态 %q", string(s))
	}
}

// SessionFiles 是一次调用产生的 Session 文件在本机的位置。
//
// 路径都是执行环境内部的绝对路径；本机持久化位置由调用方按同一套布局决定。
type SessionFiles struct {
	// JSONLPath 是原始 Session 文件路径，为空表示没有可用文件。
	JSONLPath string
	// HTMLPath 是导出的 HTML 路径，为空表示没有生成。
	HTMLPath string
	// Completeness 是 Session 文件的完整性状态。
	Completeness SessionCompleteness
	// ExportStatus 是 HTML 导出状态。
	ExportStatus ExportStatus
	// Bytes 是原始 Session 文件的字节数。
	Bytes int64
	// Error 是提取或导出失败的可诊断说明。
	Error string
}

// RunResult 是一次 Agent 执行的最终结果。
type RunResult struct {
	// SessionID 是 AgentFlow 内部会话标识。
	SessionID string
	// ExternalSessionID 是 Agent 运行时侧的会话标识，无法获取时为空。
	ExternalSessionID string
	// Status 是执行器自身的运行结果。
	Status RunStatus
	// FinalText 是最终助手文本的摘要，长度受 MaxFinalTextLength 限制。
	FinalText string
	// StderrSummary 是标准错误输出的末尾摘要，长度受 MaxFinalTextLength 限制。
	StderrSummary string
	// ProcessID 是 Agent 子进程的进程 ID，未能取得时为 0。
	ProcessID int
	// ExitCode 是 Agent 子进程的退出码，未能取得时为 -1。
	ExitCode int
	// Failure 是可诊断的失败原因，仅在 Status 不为 RunCompleted 时可能非空。
	Failure string
	// Session 是本次调用产生的 Session 文件信息。
	Session SessionFiles
	// StartedAt 是执行开始时间。
	StartedAt time.Time
	// FinishedAt 是执行结束时间。
	FinishedAt time.Time
}

// TruncateText 把文本裁剪到指定字节上限，且不切断 UTF-8 字符。
func TruncateText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	truncated := text[:limit]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return strings.TrimRight(truncated, " \t\r\n")
}
