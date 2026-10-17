package pi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/agent"
)

// maxRecordSnippet 是错误信息中保留的原始记录字节上限。
const maxRecordSnippet = 200

// decoder 按顺序解析 JSONL 记录并维护记录序号。
//
// 记录由 process.Supervisor 按 LF 分界切分，这里只负责协议层解码。
type decoder struct {
	index int64
}

// envelope 是记录中协议层的固定字段。
type envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

// decode 解析单行记录。
func (d *decoder) decode(line string) (record, error) {
	d.index++
	index := d.index
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return record{}, &ProtocolError{Record: index, Kind: kindInvalidRecord, Detail: "空记录"}
	}
	if !json.Valid([]byte(trimmed)) {
		return record{}, &ProtocolError{
			Record: index,
			Kind:   kindInvalidJSON,
			Detail: "非法 JSON: " + snippet(trimmed),
		}
	}
	var envelope envelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return record{}, &ProtocolError{
			Record: index,
			Kind:   kindInvalidJSON,
			Detail: "解析记录失败: " + snippet(trimmed),
		}
	}
	if strings.TrimSpace(envelope.Type) == "" {
		return record{}, &ProtocolError{
			Record: index,
			Kind:   kindInvalidRecord,
			Detail: "记录缺少 type: " + snippet(trimmed),
		}
	}
	return record{
		Index:   index,
		ID:      envelope.ID,
		Type:    envelope.Type,
		Command: envelope.Command,
		Success: envelope.Success,
		Error:   envelope.Error,
		Data:    envelope.Data,
		Raw:     json.RawMessage(trimmed),
	}, nil
}

// decodeData 把命令响应的 data 字段解码到目标结构。
func (r record) decodeData(target any) error {
	if len(r.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Data, target); err != nil {
		return &ProtocolError{
			Record: r.Index,
			Kind:   kindInvalidData,
			Detail: fmt.Sprintf("%s 响应数据无法解析: %v", r.Command, err),
		}
	}
	return nil
}

// decodeRecord 把整条记录解码到目标结构。
//
// 会话事件的字段位于记录顶层，不在 data 中，因此必须解码完整记录。
func (r record) decodeRecord(target any) error {
	if err := json.Unmarshal(r.Raw, target); err != nil {
		return &ProtocolError{
			Record: r.Index,
			Kind:   kindInvalidData,
			Detail: fmt.Sprintf("%s 事件无法解析: %v", r.Type, err),
		}
	}
	return nil
}

// snippet 截断原始记录，避免错误信息过长。
func snippet(text string) string {
	return agent.TruncateText(text, maxRecordSnippet)
}
