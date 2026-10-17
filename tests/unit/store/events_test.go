package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// appendEvent 追加一条测试事件。
func appendEvent(
	t *testing.T,
	db *sqlite.DB,
	taskID domain.ID,
	eventType domain.EventType,
	payload string,
	at time.Time,
) domain.Event {
	t.Helper()
	event, err := domain.NewEvent(taskID, nil, nil, nil, eventType, json.RawMessage(payload), at)
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	stored, err := db.AppendEvent(testContext(t), event)
	if err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
	return stored
}

func TestAppendEventAssignsIncrementalID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	base := time.Now().UTC()

	first := appendEvent(t, db, built.task.ID, domain.EventTypeTaskCreated, `{"source":"manual"}`, base)
	second := appendEvent(
		t,
		db,
		built.task.ID,
		domain.EventTypeTaskStarted,
		`{"pid":4242}`,
		base.Add(time.Second),
	)
	if first.ID <= 0 || second.ID <= first.ID {
		t.Fatalf("事件标识应当自增: %d, %d", first.ID, second.ID)
	}
	if first.Payload == nil || string(first.Payload) != `{"source":"manual"}` {
		t.Fatalf("返回的事件负载不符: %s", first.Payload)
	}
}

func TestListEventsByTaskInInsertionOrder(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	base := time.Now().UTC()
	types := []domain.EventType{
		domain.EventTypeTaskCreated,
		domain.EventTypeTaskStarted,
		domain.EventTypePlanCreated,
		domain.EventTypeStepStarted,
	}
	for index, eventType := range types {
		appendEvent(t, db, built.task.ID, eventType, "{}", base.Add(time.Duration(index)*time.Millisecond))
	}

	events, err := db.ListEventsByTask(ctx, built.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(events) != len(types) {
		t.Fatalf("事件数量不符: %d", len(events))
	}
	for index, eventType := range types {
		if events[index].Type != eventType {
			t.Fatalf("第 %d 条事件类型不符: %s", index, events[index].Type)
		}
		if events[index].ID != int64(index+1) {
			t.Fatalf("第 %d 条事件标识不符: %d", index, events[index].ID)
		}
	}
}

func TestListEventsFilterByStepAndLimit(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	base := time.Now().UTC()

	taskEvent, err := domain.NewEvent(
		built.task.ID, nil, nil, nil, domain.EventTypeTaskCreated, nil, base,
	)
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	storedTaskEvent, err := db.AppendEvent(ctx, taskEvent)
	if err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
	stepEvent, err := domain.NewEvent(
		built.task.ID,
		&stepID,
		&built.attempt.ID,
		nil,
		domain.EventTypeStepStarted,
		json.RawMessage(`{"sequence":1}`),
		base.Add(time.Millisecond),
	)
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	if _, err := db.AppendEvent(ctx, stepEvent); err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}

	filtered, err := db.ListEventsByTask(ctx, built.task.ID, storage.EventFilter{StepID: &stepID})
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("步骤过滤结果不符: %d", len(filtered))
	}
	if filtered[0].StepID == nil || *filtered[0].StepID != stepID {
		t.Fatalf("事件步骤归属不符: %+v", filtered[0])
	}
	if filtered[0].AttemptID == nil || *filtered[0].AttemptID != built.attempt.ID {
		t.Fatalf("事件尝试归属不符: %+v", filtered[0])
	}

	limited, err := db.ListEventsByTask(ctx, built.task.ID, storage.EventFilter{Limit: 1})
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != storedTaskEvent.ID {
		t.Fatalf("限制条数未生效: %+v", limited)
	}
}

func TestEventWithSessionReference(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	session := mustSession(t, db, domain.SessionScope{TaskID: built.task.ID}, built.planner)

	event, err := domain.NewEvent(
		built.task.ID,
		nil,
		nil,
		&session.ID,
		domain.EventTypeSessionStarted,
		json.RawMessage(`{"role":"planner"}`),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	if _, err := db.AppendEvent(ctx, event); err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
	events, err := db.ListEventsByTask(ctx, built.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("事件数量不符: %d", len(events))
	}
	if events[0].SessionID == nil || *events[0].SessionID != session.ID {
		t.Fatalf("会话归属未保存: %+v", events[0].SessionID)
	}
}

func TestEmptyEventPayloadBecomesEmptyObject(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	event := appendEvent(t, db, built.task.ID, domain.EventTypeTaskCreated, "", time.Now().UTC())
	if string(event.Payload) != "{}" {
		t.Fatalf("空负载应当保存为空 JSON 对象: %s", event.Payload)
	}
	events, err := db.ListEventsByTask(ctx, built.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(events) != 1 || string(events[0].Payload) != "{}" {
		t.Fatalf("空负载未正确往返: %s", events[0].Payload)
	}
}

func TestEventRejectsUnknownType(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	if _, err := domain.NewEvent(
		built.task.ID,
		nil,
		nil,
		nil,
		domain.EventType("unknown.event"),
		nil,
		time.Now().UTC(),
	); err == nil {
		t.Fatal("未知事件类型应当被拒绝")
	}
}

func TestEventRejectsInvalidPayload(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	if _, err := domain.NewEvent(
		built.task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskCreated,
		json.RawMessage("not-json"),
		time.Now().UTC(),
	); err == nil {
		t.Fatal("非法 JSON 负载应当被拒绝")
	}
}

func TestEventRequiresExistingTask(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	event, err := domain.NewEvent(
		domain.NewID(),
		nil,
		nil,
		nil,
		domain.EventTypeTaskCreated,
		nil,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	if _, err := db.AppendEvent(testContext(t), event); !errors.Is(err, storage.ErrForeignKey) {
		t.Fatalf("引用不存在的任务应当返回外键错误，实际: %v", err)
	}
}

func TestEventJSONColumnRequiresValidJSON(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	event := appendEvent(t, db, built.task.ID, domain.EventTypeTaskCreated, "{}", time.Now().UTC())
	err := testkit.ExecConstraint(t, rawDB(t, db), `UPDATE events SET payload_json = 'not-json' WHERE id = ?`,
		event.ID,
	)
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("非法 JSON 应当被拒绝，实际: %v", err)
	}
}

// TestStatusUpdateAndEventCommitAtomically 验证业务状态与事件在同一事务内提交。
func TestStatusUpdateAndEventCommitAtomically(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	task := built.task
	if err := task.Transition(domain.TaskStatusPlanning, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	err := db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if err := tx.UpdateTask(ctx, task); err != nil {
			return err
		}
		event, err := domain.NewEvent(
			task.ID,
			nil,
			nil,
			nil,
			domain.EventTypeTaskStarted,
			json.RawMessage(`{"status":"planning"}`),
			time.Now().UTC(),
		)
		if err != nil {
			return err
		}
		_, err = tx.AppendEvent(ctx, event)
		return err
	})
	if err != nil {
		t.Fatalf("事务提交失败: %v", err)
	}
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatusPlanning {
		t.Fatalf("状态未提交: %s", loaded.Status)
	}
	events, err := db.ListEventsByTask(ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("事件未提交: %d", len(events))
	}
}

// TestStatusUpdateAndEventRollBackTogether 验证失败事务不会留下半完成记录。
func TestStatusUpdateAndEventRollBackTogether(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	task := built.task
	if err := task.Transition(domain.TaskStatusPlanning, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	rollback := errors.New("模拟事件写入失败")
	err := db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if err := tx.UpdateTask(ctx, task); err != nil {
			return err
		}
		event, err := domain.NewEvent(
			task.ID,
			nil,
			nil,
			nil,
			domain.EventTypeTaskStarted,
			nil,
			time.Now().UTC(),
		)
		if err != nil {
			return err
		}
		if _, err := tx.AppendEvent(ctx, event); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("事务应当返回业务错误，实际: %v", err)
	}
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatusQueued {
		t.Fatalf("回滚后状态不应改变: %s", loaded.Status)
	}
	events, err := db.ListEventsByTask(ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("回滚后不应留下事件: %d", len(events))
	}
}

// TestFullHistoryChain 验证一次完整执行链路的历史均可追溯。
func TestFullHistoryChain(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID

	developer := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.developer)
	validator := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.validator)
	result, err := domain.NewVerificationResult(
		newVerificationInput(built, &validator.ID, false),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	if err := db.CreateVerificationResult(ctx, result); err != nil {
		t.Fatalf("写入验证结果失败: %v", err)
	}

	// 验证失败后创建新的 Attempt 与新的 Session，旧记录全部保留。
	second := mustAttempt(t, db, built.step, 2)
	secondSession := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &second.ID,
	}, built.developer)

	attempts, err := db.ListAttemptsByStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("查询执行尝试失败: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("尝试历史数量不符: %d", len(attempts))
	}
	firstSessions, err := db.ListSessionsByAttempt(ctx, built.attempt.ID)
	if err != nil {
		t.Fatalf("查询会话失败: %v", err)
	}
	if len(firstSessions) != 2 || firstSessions[0].ID != developer.ID {
		t.Fatalf("第一次尝试的会话历史不符: %+v", firstSessions)
	}
	secondSessions, err := db.ListSessionsByAttempt(ctx, second.ID)
	if err != nil {
		t.Fatalf("查询会话失败: %v", err)
	}
	if len(secondSessions) != 1 || secondSessions[0].ID != secondSession.ID {
		t.Fatalf("第二次尝试的会话历史不符: %+v", secondSessions)
	}
	results, err := db.ListVerificationResultsByAttempt(ctx, built.attempt.ID)
	if err != nil {
		t.Fatalf("查询验证结果失败: %v", err)
	}
	if len(results) != 1 || results[0].Passed {
		t.Fatalf("验证历史不符: %+v", results)
	}
}
