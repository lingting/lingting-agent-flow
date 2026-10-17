package pi

import (
	"encoding/json"
	"fmt"
)

// Pi RPC 命令类型，取值以官方 rpc-commands 文档为准。
const (
	// commandGetState 请求当前会话状态。
	commandGetState = "get_state"
	// commandPrompt 提交一次提示词。
	commandPrompt = "prompt"
	// commandAbort 中止当前运行。
	commandAbort = "abort"
)

// recordTypeResponse 是命令响应的记录类型。
const recordTypeResponse = "response"

// prompt 响应的 disposition 取值。
const (
	// dispositionStarted 表示 Pi 已接受提示词并开始运行。
	dispositionStarted = "started"
	// dispositionQueued 表示 Pi 将提示词排入队列。
	dispositionQueued = "queued"
	// dispositionHandled 表示提示词被扩展命令消费，不会启动 Agent 运行。
	dispositionHandled = "handled"
)

// 助手消息的终态 stopReason 取值，仅列出需要特殊处理的两种。
const (
	// stopReasonError 表示该轮以错误结束。
	stopReasonError = "error"
	// stopReasonAborted 表示该轮被中止。
	stopReasonAborted = "aborted"
)

// ProtocolError 的类别取值。
const (
	// kindInvalidJSON 表示记录不是合法 JSON。
	kindInvalidJSON = "invalid_json"
	// kindInvalidRecord 表示记录缺少必要字段。
	kindInvalidRecord = "invalid_record"
	// kindUnexpectedResponse 表示收到无法关联到请求的响应。
	kindUnexpectedResponse = "unexpected_response"
	// kindCommandFailed 表示命令返回失败响应。
	kindCommandFailed = "command_failed"
	// kindInvalidData 表示响应或事件的数据结构不符合预期。
	kindInvalidData = "invalid_data"
)

// command 是发往 Pi stdin 的一条 RPC 命令。
type command struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

// record 是 Pi RPC 的一条 JSONL 记录。
//
// 命令响应与会话事件共用同一行格式，通过 Type 区分。
type record struct {
	// Index 是记录在本次执行中的序号，从 1 开始，用于错误定位。
	Index int64
	// ID 是命令请求 ID，会话事件通常为空。
	ID string
	// Type 是记录类型，命令响应固定为 response。
	Type string
	// Command 是响应所对应的命令名。
	Command string
	// Success 表示命令是否成功，仅对响应有效。
	Success bool
	// Error 是失败响应的错误说明。
	Error string
	// Data 是响应数据的原始 JSON。
	Data json.RawMessage
	// Raw 是完整原始记录，事件映射直接在其上解码。
	Raw json.RawMessage
}

// isResponse 判断记录是否为命令响应。
func (r record) isResponse() bool {
	return r.Type == recordTypeResponse
}

// stateData 是 get_state 的响应数据。
type stateData struct {
	// SessionID 是 Pi 侧的会话标识。
	SessionID string `json:"sessionId"`
}

// promptData 是 prompt 的响应数据。
type promptData struct {
	// Disposition 表示提示词被接受、排队还是被扩展命令消费。
	Disposition string `json:"disposition"`
}

// ProtocolError 描述 Pi RPC 协议层错误。
//
// 错误信息只包含记录序号、类别与截断后的原始记录，不包含凭据。
type ProtocolError struct {
	// Record 是出错记录的序号，从 1 开始。
	Record int64
	// Kind 是错误类别。
	Kind string
	// Detail 是可诊断的说明。
	Detail string
}

// Error 实现 error 接口。
func (e *ProtocolError) Error() string {
	return fmt.Sprintf("pi: RPC 协议错误（第 %d 条记录，%s）: %s", e.Record, e.Kind, e.Detail)
}
