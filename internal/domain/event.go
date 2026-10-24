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
	EventTypeVerificationStarted  EventType = "verification.started"
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
	// 任务级工作流编排事件。
	//
	// 编排器只追加事件，不通过事件推进状态：事件用于事后定位"停在哪一步、为什么停"，
	// 状态本身仍以 workflow_runs 与 tasks 的持久化字段为准。
	EventTypeWorkflowStarted    EventType = "workflow.started"
	EventTypeWorkflowResumed    EventType = "workflow.resumed"
	EventTypeWorkflowStepPassed EventType = "workflow.step_passed"
	EventTypeWorkflowBlocked    EventType = "workflow.blocked"
	EventTypeWorkflowCanceled   EventType = "workflow.cancel_requested"
	EventTypeWorkflowCompleted  EventType = "workflow.completed"
	EventTypeWorkflowFailed     EventType = "workflow.failed"
	EventTypeWorkflowRecovery   EventType = "workflow.recovery_required"
	EventTypeWorkflowCodeSaved  EventType = "workflow.code_saved"
	// 修复循环事件。
	//
	// 每一轮修复都会追加一条事件，记录轮次、尝试上限、对应的 Attempt 与 Validation
	// Run、以及"为什么继续或停止"。它只用于追溯，不参与状态推进。
	EventTypeRepairRound EventType = "repair.round"
	// 执行周期开启事件。
	//
	// 它记录"哪一次执行周期被开启、上一周期已消耗多少尝试、为什么允许重置"。开启
	// 周期是用户显式决定，因此必须留下可追溯的审计记录，不能只改一行状态。
	EventTypeRepairCycleOpened EventType = "repair.cycle_opened"
	// 代码保存恢复待处理事件。
	//
	// 服务启动扫描到"仍有结果不明的外部 Git 副作用"时追加：它只记录现场与需要的
	// 动作，不推进任务状态，也不重放 commit / push / merge。
	EventTypeCodeSaveRecoveryRequired EventType = "code_save.recovery_required"
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
	EventTypeVerificationStarted,
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
	EventTypeWorkflowStarted,
	EventTypeWorkflowResumed,
	EventTypeWorkflowStepPassed,
	EventTypeWorkflowBlocked,
	EventTypeWorkflowCanceled,
	EventTypeWorkflowCompleted,
	EventTypeWorkflowFailed,
	EventTypeWorkflowRecovery,
	EventTypeWorkflowCodeSaved,
	EventTypeRepairRound,
	EventTypeRepairCycleOpened,
	EventTypeCodeSaveRecoveryRequired,
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
