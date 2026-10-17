package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EventType 是 AgentFlow 内部的事件类型。
//
// 内部事件与 Pi 的原始事件解耦：适配器负责映射，业务层只消费这里的类型。
type EventType string

// 第一版支持的事件类型。
const (
	// EventRunStarted 表示一次 Agent 执行已开始。
	EventRunStarted EventType = "run.started"
	// EventMessageDelta 表示助手消息的文本增量。
	EventMessageDelta EventType = "message.delta"
	// EventMessageCompleted 表示一条消息已完成。
	EventMessageCompleted EventType = "message.completed"
	// EventToolStarted 表示工具调用开始。
	EventToolStarted EventType = "tool.started"
	// EventToolUpdated 表示工具调用产生了增量输出。
	EventToolUpdated EventType = "tool.updated"
	// EventToolCompleted 表示工具调用结束。
	EventToolCompleted EventType = "tool.completed"
	// EventRunSettled 表示 Agent 不再自动继续，本轮执行已收敛。
	EventRunSettled EventType = "run.settled"
	// EventRunFailed 表示执行失败或异常结束。
	EventRunFailed EventType = "run.failed"
	// EventRunCancelled 表示执行被调用方取消。
	EventRunCancelled EventType = "run.cancelled"
)

// knownEventTypes 是当前受支持的内部事件类型。
var knownEventTypes = []EventType{
	EventRunStarted,
	EventMessageDelta,
	EventMessageCompleted,
	EventToolStarted,
	EventToolUpdated,
	EventToolCompleted,
	EventRunSettled,
	EventRunFailed,
	EventRunCancelled,
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
	WorkingDir string `json:"working_dir,omitempty"`
	PID        int    `json:"pid,omitempty"`
}

// MessageDeltaData 是 message.delta 的负载。
type MessageDeltaData struct {
	ContentIndex int    `json:"content_index"`
	Text         string `json:"text"`
}

// MessageCompletedData 是 message.completed 的负载。
type MessageCompletedData struct {
	Role         string `json:"role"`
	Text         string `json:"text,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// ToolStartedData 是 tool.started 的负载。
type ToolStartedData struct {
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
}

// ToolUpdatedData 是 tool.updated 的负载。
type ToolUpdatedData struct {
	ToolCallID    string          `json:"tool_call_id"`
	ToolName      string          `json:"tool_name"`
	PartialResult json.RawMessage `json:"partial_result,omitempty"`
}

// ToolCompletedData 是 tool.completed 的负载。
type ToolCompletedData struct {
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	IsError    bool            `json:"is_error"`
	Result     json.RawMessage `json:"result,omitempty"`
}

// RunSettledData 是 run.settled 的负载。
type RunSettledData struct {
	Aborted      bool   `json:"aborted"`
	StopReason   string `json:"stop_reason,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
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
