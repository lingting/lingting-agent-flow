package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EventType 是 AgentFlow 内部的事件类型。
//
// 内部事件与 Pi 的原始输出解耦：适配器负责映射，业务层只消费这里的类型。
// 非交互式 CLI 执行没有结构化的消息增量，事件只描述进程生命周期、
// 输出摘要与 Session 持久化结果。
type EventType string

// 第一版支持的事件类型。
const (
	// EventRunStarted 表示一次 Agent 执行已开始。
	EventRunStarted EventType = "run.started"
	// EventRunOutput 表示进程输出了一段内容，用于流式日志。
	EventRunOutput EventType = "run.output"
	// EventSessionSaved 表示 Session 原始文件已保存。
	EventSessionSaved EventType = "session.saved"
	// EventSessionExported 表示 Session 的 HTML 导出已结束。
	EventSessionExported EventType = "session.exported"
	// EventRunSettled 表示本轮执行已正常收敛。
	EventRunSettled EventType = "run.settled"
	// EventRunFailed 表示执行失败或异常结束。
	EventRunFailed EventType = "run.failed"
	// EventRunCancelled 表示执行被调用方取消。
	EventRunCancelled EventType = "run.cancelled"
	// EventRunTimedOut 表示执行超时并被终止。
	EventRunTimedOut EventType = "run.timed_out"
)

// knownEventTypes 是当前受支持的内部事件类型。
var knownEventTypes = []EventType{
	EventRunStarted,
	EventRunOutput,
	EventSessionSaved,
	EventSessionExported,
	EventRunSettled,
	EventRunFailed,
	EventRunCancelled,
	EventRunTimedOut,
}

// Validate 校验事件类型是否受支持。
func (t EventType) Validate() error {
	name := strings.TrimSpace(string(t))
	for _, candidate := range knownEventTypes {
		if string(candidate) == name {
			return nil
		}
	}
	return fmt.Errorf("agent: 未知事件类型 %q", name)
}

// Event 是 AgentFlow 内部的统一事件。
//
// Data 是事件负载的 JSON 编码，保留原始字段以便后续调试协议变化。
type Event struct {
	SessionID string
	Type      EventType
	Timestamp time.Time
	Data      json.RawMessage
}

// RunStartedData 是 run.started 的负载。
type RunStartedData struct {
	Model      string `json:"model"`
	RoleType   string `json:"role_type,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	SessionDir string `json:"session_dir,omitempty"`
	PID        int    `json:"pid,omitempty"`
}

// 输出流标识。
const (
	// StreamStdout 表示标准输出。
	StreamStdout = "stdout"
	// StreamStderr 表示标准错误。
	StreamStderr = "stderr"
)

// RunOutputData 是 run.output 的负载。
type RunOutputData struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// SessionSavedData 是 session.saved 的负载。
type SessionSavedData struct {
	Path         string              `json:"path"`
	Bytes        int64               `json:"bytes"`
	Completeness SessionCompleteness `json:"completeness"`
}

// SessionExportedData 是 session.exported 的负载。
type SessionExportedData struct {
	Path   string       `json:"path,omitempty"`
	Status ExportStatus `json:"status"`
	Error  string       `json:"error,omitempty"`
}

// RunSettledData 是 run.settled 的负载。
type RunSettledData struct {
	ExitCode int `json:"exit_code"`
}

// RunFailedData 是 run.failed 的负载。
type RunFailedData struct {
	Reason   string `json:"reason"`
	Message  string `json:"message,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// RunCancelledData 是 run.cancelled 的负载。
type RunCancelledData struct {
	Reason string `json:"reason,omitempty"`
}

// RunTimedOutData 是 run.timed_out 的负载。
type RunTimedOutData struct {
	Timeout  string `json:"timeout"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// NewEvent 构造内部事件，负载会被编码为 JSON。
//
// payload 为 nil 时保存空 JSON 对象，避免下游需要区分空负载。
func NewEvent(sessionID string, eventType EventType, payload any, at time.Time) (Event, error) {
	if err := eventType.Validate(); err != nil {
		return Event{}, err
	}
	data := json.RawMessage("{}")
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return Event{}, fmt.Errorf("agent: 编码事件负载失败: %w", err)
		}
		data = encoded
	}
	timestamp := at
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	return Event{
		SessionID: sessionID,
		Type:      eventType,
		Timestamp: timestamp,
		Data:      data,
	}, nil
}
