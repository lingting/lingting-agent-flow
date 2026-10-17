package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// eventColumns 是事件表的完整列顺序。
const eventColumns = `id, task_id, step_id, attempt_id, session_id, event_type,
	payload_json, created_at`

// eventStore 实现 storage.EventStore。
//
// 事件只提供追加与查询，不提供修改或删除，保证时间线不可篡改。
type eventStore struct {
	q queryer
}

// AppendEvent 追加事件并返回带自增标识的记录。
func (s eventStore) AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error) {
	if err := event.Validate(); err != nil {
		return domain.Event{}, err
	}
	payload, err := marshalJSON("Event.payload", normalizeEventPayload(event.Payload))
	if err != nil {
		return domain.Event{}, err
	}
	const statement = `INSERT INTO events
		(task_id, step_id, attempt_id, session_id, event_type, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(event.TaskID),
		toIDPtrParam(event.StepID),
		toIDPtrParam(event.AttemptID),
		toIDPtrParam(event.SessionID),
		string(event.Type),
		payload,
		toMillis(event.CreatedAt),
	)
	if err != nil {
		return domain.Event{}, wrapError("追加事件", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.Event{}, wrapError("读取事件标识", err)
	}
	stored := event
	stored.ID = id
	stored.Payload = json.RawMessage(payload)
	return stored, nil
}

// ListEventsByTask 按写入顺序返回任务的事件时间线。
func (s eventStore) ListEventsByTask(
	ctx context.Context,
	taskID domain.ID,
	filter storage.EventFilter,
) ([]domain.Event, error) {
	if err := taskID.Validate(); err != nil {
		return nil, err
	}
	statement, args := buildEventQuery(taskID, filter)
	rows, err := s.q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, wrapError("查询事件", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	events := make([]domain.Event, 0, 16)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, wrapError("读取事件", err)
		}
		events = append(events, *event)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询事件", err)
	}
	return events, nil
}

// buildEventQuery 构造事件查询语句与参数。
func buildEventQuery(taskID domain.ID, filter storage.EventFilter) (string, []any) {
	var builder strings.Builder
	builder.WriteString(`SELECT `)
	builder.WriteString(eventColumns)
	builder.WriteString(` FROM events WHERE task_id = ?`)
	args := []any{toIDParam(taskID)}
	if filter.StepID != nil && !filter.StepID.IsEmpty() {
		builder.WriteString(` AND step_id = ?`)
		args = append(args, toIDParam(*filter.StepID))
	}
	builder.WriteString(` ORDER BY id`)
	if filter.Limit > 0 {
		builder.WriteString(` LIMIT ?`)
		args = append(args, filter.Limit)
	}
	return builder.String(), args
}

// scanEvent 从一行结果读取事件。
func scanEvent(row scanner) (*domain.Event, error) {
	var (
		event       domain.Event
		taskID      string
		stepID      sql.NullString
		attemptID   sql.NullString
		sessionID   sql.NullString
		eventType   string
		payloadJSON string
		createdAt   int64
	)
	err := row.Scan(
		&event.ID,
		&taskID,
		&stepID,
		&attemptID,
		&sessionID,
		&eventType,
		&payloadJSON,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}
	event.TaskID = domain.ID(taskID)
	event.StepID = fromIDPtr(stepID)
	event.AttemptID = fromIDPtr(attemptID)
	event.SessionID = fromIDPtr(sessionID)
	event.Type = domain.EventType(eventType)
	event.Payload = json.RawMessage(payloadJSON)
	event.CreatedAt = fromMillis(createdAt)
	return &event, nil
}

// normalizeEventPayload 把空负载统一为空 JSON 对象。
func normalizeEventPayload(payload json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(payload))) == 0 {
		return json.RawMessage("{}")
	}
	return payload
}
