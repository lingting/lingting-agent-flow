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

// ListEventsByTask 按 (created_at, id) 升序返回任务的事件时间线。
//
// 排序不是单纯的 id 顺序：事件时间取自业务时钟，`id` 只保证写入先后。两者组合
// 既让时间线与真实发生时间一致，又保证同一毫秒内的多条事件有确定的先后，从而
// 使分页查询不漏行、不重复行。
func (s eventStore) ListEventsByTask(
	ctx context.Context,
	taskID domain.ID,
	filter storage.EventFilter,
) ([]domain.Event, error) {
	if err := taskID.Validate(); err != nil {
		return nil, err
	}
	statement, args := buildEventQuery("SELECT "+eventColumns+" FROM events", taskID, filter)
	var builder strings.Builder
	builder.WriteString(statement)
	builder.WriteString(` ORDER BY created_at, id`)
	queryArgs := append([]any{}, args...)
	queryArgs = appendPaging(&builder, queryArgs, filter.Limit, filter.Offset)
	rows, err := s.q.QueryContext(ctx, builder.String(), queryArgs...)
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

// CountEventsByTask 返回满足筛选条件的事件总数。
//
// 分页响应必须能给出真实总数：前端据它判断"是否还有下一页"，用当前页条数推算
// 会让最后一页的展示与真实总量不一致。
func (s eventStore) CountEventsByTask(
	ctx context.Context,
	taskID domain.ID,
	filter storage.EventFilter,
) (int, error) {
	if err := taskID.Validate(); err != nil {
		return 0, err
	}
	statement, args := buildEventQuery("SELECT COUNT(*) FROM events", taskID, filter)
	var total int
	if err := s.q.QueryRowContext(ctx, statement, args...).Scan(&total); err != nil {
		return 0, wrapError("统计事件数量", err)
	}
	return total, nil
}

// buildEventQuery 构造事件查询的 FROM / WHERE 部分与参数。
//
// 分页与排序由调用方追加：只有列表查询需要它们，统计查询不需要。
func buildEventQuery(prefix string, taskID domain.ID, filter storage.EventFilter) (string, []any) {
	var builder strings.Builder
	builder.WriteString(prefix)
	builder.WriteString(` WHERE task_id = ?`)
	args := []any{toIDParam(taskID)}
	if filter.StepID != nil && !filter.StepID.IsEmpty() {
		builder.WriteString(` AND step_id = ?`)
		args = append(args, toIDParam(*filter.StepID))
	}
	if len(filter.Types) > 0 {
		placeholdersForType := make([]string, 0, len(filter.Types))
		for _, eventType := range filter.Types {
			// 空类型不构成筛选条件：它既不是合法事件类型，也无法命中任何记录。
			if strings.TrimSpace(string(eventType)) == "" {
				continue
			}
			placeholdersForType = append(placeholdersForType, "?")
			args = append(args, string(eventType))
		}
		if len(placeholdersForType) > 0 {
			builder.WriteString(` AND event_type IN (`)
			builder.WriteString(strings.Join(placeholdersForType, ", "))
			builder.WriteString(`)`)
		}
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
