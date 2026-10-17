package agent_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
)

// TestNewEventEncodesPayload 验证事件负载被编码为 JSON 并保留会话标识。
func TestNewEventEncodesPayload(t *testing.T) {
	at := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	event, err := agent.NewEvent("session-1", agent.EventMessageDelta, agent.MessageDeltaData{
		ContentIndex: 0,
		Text:         "READY",
	}, at)
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	if event.SessionID != "session-1" {
		t.Fatalf("会话标识不符: %s", event.SessionID)
	}
	if !event.Timestamp.Equal(at) {
		t.Fatalf("时间戳不符: %s", event.Timestamp)
	}
	var payload agent.MessageDeltaData
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		t.Fatalf("解析负载失败: %v", err)
	}
	if payload.Text != "READY" {
		t.Fatalf("负载内容不符: %+v", payload)
	}
}

// TestNewEventWithoutPayload 验证空负载保存为空 JSON 对象。
func TestNewEventWithoutPayload(t *testing.T) {
	event, err := agent.NewEvent("session-1", agent.EventRunStarted, nil, time.Now())
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	if string(event.Data) != "{}" {
		t.Fatalf("空负载应为空对象，实际 %s", event.Data)
	}
}

// TestNewEventRejectsUnknownType 验证未知事件类型被拒绝。
func TestNewEventRejectsUnknownType(t *testing.T) {
	if _, err := agent.NewEvent("session-1", agent.EventType("run.unknown"), nil, time.Now()); err == nil {
		t.Fatal("未知事件类型应返回错误")
	}
}

// TestEventTypeValidate 验证第一版事件类型均受支持。
func TestEventTypeValidate(t *testing.T) {
	types := []agent.EventType{
		agent.EventRunStarted,
		agent.EventMessageDelta,
		agent.EventMessageCompleted,
		agent.EventToolStarted,
		agent.EventToolUpdated,
		agent.EventToolCompleted,
		agent.EventRunSettled,
		agent.EventRunFailed,
		agent.EventRunCancelled,
	}
	for _, eventType := range types {
		if err := eventType.Validate(); err != nil {
			t.Fatalf("事件类型 %s 应受支持: %v", eventType, err)
		}
	}
}

// TestRunStatusValidate 验证运行状态的取值约束。
func TestRunStatusValidate(t *testing.T) {
	for _, status := range []agent.RunStatus{
		agent.RunCompleted,
		agent.RunFailed,
		agent.RunCancelled,
	} {
		if err := status.Validate(); err != nil {
			t.Fatalf("运行状态 %s 应受支持: %v", status, err)
		}
	}
	if err := agent.RunStatus("done").Validate(); err == nil {
		t.Fatal("未知运行状态应返回错误")
	}
}

// TestRunRequestValidate 验证执行输入的必需字段。
func TestRunRequestValidate(t *testing.T) {
	valid := agent.RunRequest{SessionID: "session-1", Model: "provider/model", Prompt: "只回答 READY"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("完整请求应通过校验: %v", err)
	}
	cases := map[string]agent.RunRequest{
		"缺少会话标识": {Model: "provider/model", Prompt: "x"},
		"缺少模型":   {SessionID: "session-1", Prompt: "x"},
		"缺少提示词":  {SessionID: "session-1", Model: "provider/model"},
	}
	for name, request := range cases {
		if err := request.Validate(); err == nil {
			t.Fatalf("%s 应返回错误", name)
		}
	}
}

// TestTruncateText 验证文本裁剪不切断多字节字符。
func TestTruncateText(t *testing.T) {
	if got := agent.TruncateText("READY", 100); got != "READY" {
		t.Fatalf("未超限时不应裁剪: %q", got)
	}
	text := strings.Repeat("长", 100)
	got := agent.TruncateText(text, 31)
	if len(got) > 31 {
		t.Fatalf("裁剪结果超过上限: %d", len(got))
	}
	if !json.Valid([]byte(`"` + got + `"`)) {
		t.Fatalf("裁剪结果不是合法 UTF-8: %q", got)
	}
}
