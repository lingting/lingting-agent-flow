package agent

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxFinalTextLength 是 RunResult.FinalText 保留的最大字节数。
//
// 执行结果只用于摘要与诊断，完整输出应通过事件流持久化。
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
	// RunFailed 表示执行失败、协议异常或进程异常退出。
	RunFailed RunStatus = "failed"
	// RunCancelled 表示调用方取消了执行。
	RunCancelled RunStatus = "cancelled"
)

// Validate 校验运行状态是否受支持。
func (s RunStatus) Validate() error {
	switch s {
	case RunCompleted, RunFailed, RunCancelled:
		return nil
	default:
		return fmt.Errorf("agent: 未知运行状态 %q", string(s))
	}
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
	// ExitCode 是 Agent 子进程的退出码，未能取得时为 -1。
	ExitCode int
	// Failure 是可诊断的失败原因，仅在 Status 为 RunFailed 时可能非空。
	Failure string
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
