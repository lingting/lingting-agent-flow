package pi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
)

// Pi 会话事件类型，取值以官方 json 文档为准。
const (
	eventAgentStart         = "agent_start"
	eventAgentSettled       = "agent_settled"
	eventMessageUpdate      = "message_update"
	eventMessageEnd         = "message_end"
	eventToolExecutionStart = "tool_execution_start"
	eventToolExecutionUpd   = "tool_execution_update"
	eventToolExecutionEnd   = "tool_execution_end"
)

// message_update 内部事件的类型。
const (
	assistantEventTextDelta = "text_delta"
	assistantEventError     = "error"
)

// messageRoleAssistant 是助手消息的角色名。
const messageRoleAssistant = "assistant"

// piEvent 是会话事件的顶层结构。
//
// 只声明内部事件映射需要的字段，其余字段由 encoding/json 忽略。
type piEvent struct {
	Type                  string                 `json:"type"`
	Message               *piMessage             `json:"message"`
	AssistantMessageEvent *assistantMessageEvent `json:"assistantMessageEvent"`
	ToolCallID            string                 `json:"toolCallId"`
	ToolName              string                 `json:"toolName"`
	Args                  json.RawMessage        `json:"args"`
	PartialResult         json.RawMessage        `json:"partialResult"`
	Result                json.RawMessage        `json:"result"`
	IsError               bool                   `json:"isError"`
	Aborted               bool                   `json:"aborted"`
}

// piMessage 是 Pi 的 AgentMessage 中与结果判断相关的字段。
type piMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
}

// assistantMessageEvent 是 message_update 中的内容块增量。
type assistantMessageEvent struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
	Delta        string `json:"delta"`
	Reason       string `json:"reason"`
}

// text 提取消息中的纯文本内容。
//
// 内容既可能是字符串，也可能是内容块数组；非文本块被忽略。
func (m *piMessage) text() string {
	if m == nil || len(m.Content) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(m.Content, &plain); err == nil {
		return plain
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

// translator 把 Pi 记录映射为内部事件，并跟踪本次执行的结果信号。
//
// translator 不做并发保护：它只在读取 stdout 的单一 goroutine 中调用。
type translator struct {
	sessionID string
	emit      func(agent.Event)

	// finalText 保存最后一条助手消息的文本。
	finalText strings.Builder
	// stopReason 保存最后一条助手消息的终态原因。
	stopReason string
	// errorMessage 保存最后一条助手消息的错误说明。
	errorMessage string
	// streamError 保存 message_update 中出现的流式错误原因。
	streamError string
	// settled 表示已收到 agent_settled。
	settled bool
	// aborted 表示 agent_settled 声明本次运行被中止。
	aborted bool
}

// newTranslator 构造事件映射器，emit 为 nil 时只更新状态。
func newTranslator(sessionID string, emit func(agent.Event)) *translator {
	return &translator{sessionID: sessionID, emit: emit}
}

// handle 处理一条会话事件记录。
//
// 未识别的事件类型被忽略，保证协议新增事件不会中断执行。
func (t *translator) handle(rec record, at time.Time) error {
	var event piEvent
	if err := rec.decodeRecord(&event); err != nil {
		return err
	}
	switch event.Type {
	case eventAgentStart:
		// run.started 由 Runner 在进程启动成功后发出，这里不重复发出。
		return nil
	case eventMessageUpdate:
		return t.handleMessageUpdate(rec, event, at)
	case eventMessageEnd:
		return t.handleMessageEnd(event, at)
	case eventToolExecutionStart:
		return t.emitEvent(agent.EventToolStarted, agent.ToolStartedData{
			ToolCallID: event.ToolCallID,
			ToolName:   event.ToolName,
			Arguments:  event.Args,
		}, at)
	case eventToolExecutionUpd:
		return t.emitEvent(agent.EventToolUpdated, agent.ToolUpdatedData{
			ToolCallID:    event.ToolCallID,
			ToolName:      event.ToolName,
			PartialResult: event.PartialResult,
		}, at)
	case eventToolExecutionEnd:
		return t.emitEvent(agent.EventToolCompleted, agent.ToolCompletedData{
			ToolCallID: event.ToolCallID,
			ToolName:   event.ToolName,
			IsError:    event.IsError,
			Result:     event.Result,
		}, at)
	case eventAgentSettled:
		t.settled = true
		t.aborted = event.Aborted
		return t.emitEvent(agent.EventRunSettled, agent.RunSettledData{
			Aborted:      event.Aborted,
			StopReason:   t.stopReason,
			ErrorMessage: t.errorMessage,
		}, at)
	default:
		return nil
	}
}

// handleMessageUpdate 处理消息内容块增量。
func (t *translator) handleMessageUpdate(rec record, event piEvent, at time.Time) error {
	update := event.AssistantMessageEvent
	if update == nil {
		return &ProtocolError{
			Record: rec.Index,
			Kind:   kindInvalidData,
			Detail: "message_update 缺少 assistantMessageEvent",
		}
	}
	switch update.Type {
	case assistantEventTextDelta:
		return t.emitEvent(agent.EventMessageDelta, agent.MessageDeltaData{
			ContentIndex: update.ContentIndex,
			Text:         update.Delta,
		}, at)
	case assistantEventError:
		t.streamError = update.Reason
		return nil
	default:
		// thinking_delta、toolcall_delta 等增量不进入内部事件模型。
		return nil
	}
}

// handleMessageEnd 处理消息完成事件。
func (t *translator) handleMessageEnd(event piEvent, at time.Time) error {
	message := event.Message
	if message == nil {
		return nil
	}
	text := message.text()
	if message.Role == messageRoleAssistant {
		t.finalText.Reset()
		t.finalText.WriteString(text)
		t.stopReason = message.StopReason
		t.errorMessage = message.ErrorMessage
	}
	return t.emitEvent(agent.EventMessageCompleted, agent.MessageCompletedData{
		Role:         message.Role,
		Text:         agent.TruncateText(text, agent.MaxFinalTextLength),
		StopReason:   message.StopReason,
		ErrorMessage: message.ErrorMessage,
	}, at)
}

// emitEvent 编码并发出内部事件。
func (t *translator) emitEvent(eventType agent.EventType, payload any, at time.Time) error {
	if t.emit == nil {
		return nil
	}
	event, err := agent.NewEvent(t.sessionID, eventType, payload, at)
	if err != nil {
		return fmt.Errorf("pi: 构造内部事件失败: %w", err)
	}
	t.emit(event)
	return nil
}

// failureReason 返回本次执行应判定为失败的原因，空字符串表示未发现失败信号。
//
// 成功响应只代表命令被接受，Provider 错误与中止可能只出现在事件流中，
// 因此必须综合助手消息终态与流式错误判断。
func (t *translator) failureReason() string {
	switch t.stopReason {
	case stopReasonError:
		if t.errorMessage != "" {
			return "助手消息以错误结束: " + t.errorMessage
		}
		return "助手消息以错误结束"
	case stopReasonAborted:
		return "助手消息被中止"
	}
	if t.errorMessage != "" {
		return "助手消息报告错误: " + t.errorMessage
	}
	if t.streamError != "" {
		return "事件流报告错误: " + t.streamError
	}
	return ""
}
