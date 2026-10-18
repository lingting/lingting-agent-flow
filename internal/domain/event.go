package domain

import (
	"encoding/json"
	"strings"
	"time"
)

// EventType 是工作流关键事件的类型。
//
// 事件只追加写入，不提供修改或覆盖已有事件的业务操作。
type EventType string

// 第一版支持的关键事件类型。
const (
	EventTypeTaskCreated          EventType = "task.created"
	EventTypeTaskStarted          EventType = "task.started"
	EventTypePlanCreated          EventType = "plan.created"
	EventTypeStepStarted          EventType = "step.started"
	EventTypeAttemptStarted       EventType = "attempt.started"
	EventTypeSessionStarted       EventType = "session.started"
	EventTypeSessionCompleted     EventType = "session.completed"
	EventTypeVerificationPassed   EventType = "verification.passed"
	EventTypeVerificationFailed   EventType = "verification.failed"
	EventTypeAttemptFailed        EventType = "attempt.failed"
	EventTypeStepCompleted        EventType = "step.completed"
	EventTypeTaskCompleted        EventType = "task.completed"
	EventTypeTaskFailed           EventType = "task.failed"
	EventTypeTaskCancelled        EventType = "task.cancelled"
	EventTypeRequirementSubmitted EventType = "requirement.submitted"
	EventTypeTaskCancelRequested  EventType = "task.cancel_requested"
	EventTypeTaskRecoveryRequired EventType = "task.recovery_required"
	EventTypeTaskRetrying         EventType = "task.retrying"
	EventTypeContainerCreated     EventType = "container.created"
	EventTypeContainerCleaned     EventType = "container.cleaned"
	EventTypeContainerRetained    EventType = "container.retained"
	EventTypeSourceFetched        EventType = "source.fetched"
	EventTypeVerificationSkipped  EventType = "verification.skipped"
	EventTypeCodeSaved            EventType = "code.saved"
	EventTypeAttemptInterrupted   EventType = "attempt.interrupted"
)

// knownEventTypes 是当前受支持的事件类型集合。
var knownEventTypes = []EventType{
	EventTypeTaskCreated,
	EventTypeTaskStarted,
	EventTypePlanCreated,
	EventTypeStepStarted,
	EventTypeAttemptStarted,
	EventTypeSessionStarted,
	EventTypeSessionCompleted,
	EventTypeVerificationPassed,
	EventTypeVerificationFailed,
	EventTypeAttemptFailed,
	EventTypeStepCompleted,
	EventTypeTaskCompleted,
	EventTypeTaskFailed,
	EventTypeTaskCancelled,
	EventTypeRequirementSubmitted,
	EventTypeTaskCancelRequested,
	EventTypeTaskRecoveryRequired,
	EventTypeTaskRetrying,
	EventTypeContainerCreated,
	EventTypeContainerCleaned,
	EventTypeContainerRetained,
	EventTypeSourceFetched,
	EventTypeVerificationSkipped,
	EventTypeCodeSaved,
	EventTypeAttemptInterrupted,
}

// Validate 校验事件类型是否受支持。
func (t EventType) Validate() error {
	name := strings.TrimSpace(string(t))
	for _, candidate := range knownEventTypes {
		if string(candidate) == name {
			return nil
		}
	}
	return invalidField("Event", "event_type", "未知事件类型 "+name)
}

// Event 是任务工作流中的一个关键事件。
//
// ID 由数据库自增分配，插入前为零值。
type Event struct {
	ID        int64           `json:"id"`
	TaskID    ID              `json:"task_id"`
	StepID    *ID             `json:"step_id,omitempty"`
	AttemptID *ID             `json:"attempt_id,omitempty"`
	SessionID *ID             `json:"session_id,omitempty"`
	Type      EventType       `json:"event_type"`
	Payload   json.RawMessage `json:"payload_json,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// NewEvent 创建并校验事件。
//
// payload 为空时保存空 JSON 对象，避免下游需要区分 NULL 与空对象。
func NewEvent(
	taskID ID,
	stepID, attemptID, sessionID *ID,
	eventType EventType,
	payload json.RawMessage,
	at time.Time,
) (Event, error) {
	event := Event{
		TaskID:    taskID,
		StepID:    stepID,
		AttemptID: attemptID,
		SessionID: sessionID,
		Type:      eventType,
		Payload:   normalizePayload(payload),
		CreatedAt: normalizeTime(at),
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// Validate 校验事件定义是否完整。
func (e Event) Validate() error {
	if e.ID < 0 {
		return invalidField("Event", "id", "事件标识不能为负数")
	}
	if err := e.TaskID.Validate(); err != nil {
		return err
	}
	for _, id := range []*ID{e.StepID, e.AttemptID, e.SessionID} {
		if id == nil {
			continue
		}
		if err := id.Validate(); err != nil {
			return err
		}
	}
	if err := e.Type.Validate(); err != nil {
		return err
	}
	return validatePayload(e.Payload)
}

// normalizePayload 把空负载统一为空 JSON 对象。
func normalizePayload(payload json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(payload))) == 0 {
		return json.RawMessage("{}")
	}
	return payload
}

// validatePayload 校验负载是否为合法 JSON。
func validatePayload(payload json.RawMessage) error {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return invalidField("Event", "payload_json", "事件内容不能为空")
	}
	if !json.Valid([]byte(trimmed)) {
		return invalidField("Event", "payload_json", "事件内容必须是合法 JSON")
	}
	return nil
}
