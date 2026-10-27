package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// historyFixture 是事件时间线与修复周期历史查询的现场。
//
// 两条互不相关的任务同时存在：用于证明查询严格按任务过滤，而不是返回全部记录。
type historyFixture struct {
	db *sqlite.DB
	// dbPath 是数据库文件路径：需要写入当前版本无法构造的历史事件时，用它打开
	// 一条独立连接直接插入记录，从而验证真实的兼容性而不是伪造领域对象。
	dbPath  string
	ctx     context.Context
	service *app.HistoryQueryService
	task    domain.Task
	step    domain.Step
	// otherTask / otherStep 属于另一条任务链，用于证明归属校验生效。
	otherTask domain.Task
	otherStep domain.Step
	role      domain.Role
}

// newHistoryFixture 在工作空间中准备两个任务、各自的步骤与角色。
func newHistoryFixture(t *testing.T) *historyFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	dbPath := workspace.DatabasePath()
	db, err := sqlite.Open(ctx, sqlite.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	now := time.Now().UTC()
	fixture := &historyFixture{ctx: ctx, db: db, dbPath: dbPath}
	fixture.role = createHistoryRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	fixture.task, fixture.step = createHistoryTask(t, db, ctx, now, fixture.role, "第一个任务")
	fixture.otherTask, fixture.otherStep = createHistoryTask(
		t, db, ctx, now, fixture.role, "第二个任务",
	)
	service, err := app.NewHistoryQueryService(db)
	if err != nil {
		t.Fatalf("构造历史查询服务失败: %v", err)
	}
	fixture.service = service
	return fixture
}

// createHistoryRole 写入一个测试角色。
func createHistoryRole(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	roleType domain.RoleType,
	name string,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		name,
		roleType,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(roleType),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// createHistoryTask 写入需求、任务与一个步骤，并返回任务与步骤。
func createHistoryTask(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
	role domain.Role,
	title string,
) (domain.Task, domain.Step) {
	t.Helper()
	requirement, err := domain.NewRequirement(
		title,
		title+"正文",
		domain.PriorityNormal,
		now,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	step, err := domain.NewStep(
		task.ID,
		1,
		title+"步骤",
		title+"步骤目标",
		"执行要求",
		role,
		domain.VerificationSpec{},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return task, step
}

// historyEvent 追加一条事件并返回带自增标识的记录。
func (f *historyFixture) historyEvent(
	t *testing.T,
	taskID domain.ID,
	stepID *domain.ID,
	eventType domain.EventType,
	payload string,
	at time.Time,
) domain.Event {
	t.Helper()
	event, err := domain.NewEvent(
		taskID,
		stepID,
		nil,
		nil,
		eventType,
		json.RawMessage(payload),
		at,
	)
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	stored, err := f.db.AppendEvent(f.ctx, event)
	if err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
	return stored
}

// startCycle 开启一个修复周期。
func (f *historyFixture) startCycle(
	t *testing.T,
	task domain.Task,
	step domain.Step,
	maxAttempts int,
	at time.Time,
) domain.RepairCycle {
	t.Helper()
	cycle, err := f.db.StartRepairCycle(f.ctx, storage.StartRepairCycleRequest{
		TaskID:      task.ID,
		StepID:      step.ID,
		MaxAttempts: maxAttempts,
		OpenedBy:    "user_reopen",
		At:          at,
	})
	if err != nil {
		t.Fatalf("开启修复周期失败: %v", err)
	}
	return cycle
}

// TestListTaskEventsReturnsOnlyTaskEvents 校验查询严格按任务隔离。
func TestListTaskEventsReturnsOnlyTaskEvents(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	base := time.Now().UTC()
	fixture.historyEvent(t, fixture.task.ID, nil, domain.EventTypeTaskCreated, `{}`, base)
	fixture.historyEvent(t, fixture.task.ID, nil, domain.EventTypeTaskStarted, `{}`, base)
	fixture.historyEvent(t, fixture.otherTask.ID, nil, domain.EventTypeTaskCreated, `{}`, base)

	page, err := fixture.service.ListTaskEvents(
		fixture.ctx,
		fixture.task.ID.String(),
		app.EventTimelineRequest{},
	)
	if err != nil {
		t.Fatalf("查询任务事件失败: %v", err)
	}
	if page.Total != 2 || len(page.Events) != 2 {
		t.Fatalf("事件数量不符: total=%d len=%d", page.Total, len(page.Events))
	}
	for _, summary := range page.Events {
		if summary.Event.TaskID != fixture.task.ID {
			t.Fatalf("查询结果混入了其他任务的事件: %s", summary.Event.TaskID)
		}
	}
}

// TestListTaskEventsOrdersByCreatedAtThenID 校验稳定排序，包括同一时间戳的事件。
func TestListTaskEventsOrdersByCreatedAtThenID(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	base := time.Now().UTC()
	// 故意按"时间倒序"写入：排序必须由查询决定，而不是由写入顺序碰巧正确。
	later := fixture.historyEvent(
		t, fixture.task.ID, nil, domain.EventTypeStepCompleted, `{}`, base.Add(time.Minute),
	)
	sameFirst := fixture.historyEvent(
		t, fixture.task.ID, nil, domain.EventTypeAttemptStarted, `{}`, base,
	)
	sameSecond := fixture.historyEvent(
		t, fixture.task.ID, nil, domain.EventTypeSessionStarted, `{}`, base,
	)

	page, err := fixture.service.ListTaskEvents(
		fixture.ctx,
		fixture.task.ID.String(),
		app.EventTimelineRequest{},
	)
	if err != nil {
		t.Fatalf("查询任务事件失败: %v", err)
	}
	want := []int64{sameFirst.ID, sameSecond.ID, later.ID}
	if len(page.Events) != len(want) {
		t.Fatalf("事件数量不符: %d", len(page.Events))
	}
	for index, id := range want {
		if page.Events[index].Event.ID != id {
			t.Fatalf("第 %d 条事件顺序不符: 期望 %d 实际 %d", index, id, page.Events[index].Event.ID)
		}
	}
}

// TestListTaskEventsPaginatesStably 校验分页边界不重不漏。
func TestListTaskEventsPaginatesStably(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	base := time.Now().UTC()
	ids := make([]int64, 0, 5)
	for index := 0; index < 5; index++ {
		event := fixture.historyEvent(
			t,
			fixture.task.ID,
			nil,
			domain.EventTypeStepStarted,
			`{}`,
			base.Add(time.Duration(index)*time.Second),
		)
		ids = append(ids, event.ID)
	}

	first, err := fixture.service.ListTaskEvents(fixture.ctx, fixture.task.ID.String(), app.EventTimelineRequest{
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("查询第一页失败: %v", err)
	}
	if first.Total != 5 || len(first.Events) != 2 || first.Limit != 2 || first.Offset != 0 {
		t.Fatalf("第一页元数据不符: %+v", first)
	}
	second, err := fixture.service.ListTaskEvents(fixture.ctx, fixture.task.ID.String(), app.EventTimelineRequest{
		Limit:  2,
		Offset: 2,
	})
	if err != nil {
		t.Fatalf("查询第二页失败: %v", err)
	}
	if len(second.Events) != 2 || second.Offset != 2 {
		t.Fatalf("第二页元数据不符: %+v", second)
	}
	third, err := fixture.service.ListTaskEvents(fixture.ctx, fixture.task.ID.String(), app.EventTimelineRequest{
		Limit:  2,
		Offset: 4,
	})
	if err != nil {
		t.Fatalf("查询第三页失败: %v", err)
	}
	if len(third.Events) != 1 {
		t.Fatalf("第三页数量不符: %d", len(third.Events))
	}

	got := make([]int64, 0, 5)
	for _, page := range []app.EventTimelinePage{first, second, third} {
		for _, summary := range page.Events {
			got = append(got, summary.Event.ID)
		}
	}
	if len(got) != len(ids) {
		t.Fatalf("分页结果数量不符: %d", len(got))
	}
	for index, id := range ids {
		if got[index] != id {
			t.Fatalf("分页顺序不符: 第 %d 条期望 %d 实际 %d", index, id, got[index])
		}
	}
}

// TestListTaskEventsRejectsOversizedPage 校验超过上限的页大小被拒绝而不是截断。
func TestListTaskEventsRejectsOversizedPage(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	_, err := fixture.service.ListTaskEvents(
		fixture.ctx,
		fixture.task.ID.String(),
		app.EventTimelineRequest{Limit: app.MaxTaskPageSize + 1},
	)
	if !errors.Is(err, app.ErrHistoryInvalidInput) {
		t.Fatalf("超过上限的页大小应返回参数错误，实际 %v", err)
	}
}

// TestListTaskEventsFiltersByType 校验事件类型筛选，包括历史未知类型。
func TestListTaskEventsFiltersByType(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	base := time.Now().UTC()
	fixture.historyEvent(t, fixture.task.ID, nil, domain.EventTypeTaskCreated, `{}`, base)
	fixture.historyEvent(t, fixture.task.ID, nil, domain.EventTypeTaskStarted, `{}`, base)
	// 当前版本不再定义的类型：必须仍可被按类型查询出来，不能被静默丢弃。
	// 领域构造器会拒绝未知类型，因此直接写入数据库，模拟真实的历史数据。
	raw := testkit.OpenSQLite(t, fixture.dbPath)
	testkit.MustExec(
		t,
		raw,
		`INSERT INTO events (task_id, step_id, event_type, payload_json, created_at)
		 VALUES (?, NULL, 'legacy.removed', '{}', ?)`,
		fixture.task.ID.String(),
		base.Add(time.Second).UnixMilli(),
	)

	page, err := fixture.service.ListTaskEvents(fixture.ctx, fixture.task.ID.String(), app.EventTimelineRequest{
		Types: []string{"task.created"},
	})
	if err != nil {
		t.Fatalf("按类型查询失败: %v", err)
	}
	if page.Total != 1 || len(page.Events) != 1 {
		t.Fatalf("类型筛选结果不符: %+v", page)
	}
	if page.Events[0].Event.Type != domain.EventTypeTaskCreated {
		t.Fatalf("类型筛选返回了错误的事件: %s", page.Events[0].Event.Type)
	}

	legacy, err := fixture.service.ListTaskEvents(fixture.ctx, fixture.task.ID.String(), app.EventTimelineRequest{
		Types: []string{"legacy.removed"},
	})
	if err != nil {
		t.Fatalf("按历史类型查询失败: %v", err)
	}
	if legacy.Total != 1 {
		t.Fatalf("历史类型应仍可查询，实际 total=%d", legacy.Total)
	}
	if legacy.Events[0].KnownType {
		t.Fatal("历史类型不应被标记为当前版本定义的类型")
	}
	if legacy.Events[0].Label != "" {
		t.Fatalf("历史类型没有中文标签，实际 %q", legacy.Events[0].Label)
	}
	if string(legacy.Events[0].Event.Type) != "legacy.removed" {
		t.Fatalf("历史类型必须原样保留，实际 %q", legacy.Events[0].Event.Type)
	}
}

// TestListTaskEventsRejectsUnknownTask 校验任务不存在时返回不可区分的 404 类别。
func TestListTaskEventsRejectsUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	_, err := fixture.service.ListTaskEvents(
		fixture.ctx,
		domain.NewID().String(),
		app.EventTimelineRequest{},
	)
	if !errors.Is(err, app.ErrHistoryNotFound) {
		t.Fatalf("不存在的任务应返回 ErrHistoryNotFound，实际 %v", err)
	}
}

// TestListTaskEventsHidesUnscreenedPayload 校验敏感字段不进入摘要。
func TestListTaskEventsHidesUnscreenedPayload(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	payload := `{"summary":"token=supersecret","work_dir":"/workspace/project",` +
		`"repository":"https://user:pass@example.com/repo.git","status":"failed"}`
	fixture.historyEvent(t, fixture.task.ID, nil, domain.EventTypeTaskFailed, payload, time.Now().UTC())

	page, err := fixture.service.ListTaskEvents(
		fixture.ctx,
		fixture.task.ID.String(),
		app.EventTimelineRequest{},
	)
	if err != nil {
		t.Fatalf("查询任务事件失败: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("事件数量不符: %d", len(page.Events))
	}
	summary := page.Events[0]
	if len(summary.Event.Payload) != 0 {
		t.Fatalf("对外事件不得携带原始负载: %s", summary.Event.Payload)
	}
	for _, forbidden := range []string{"supersecret", "/workspace/project", "user:pass"} {
		if strings.Contains(summary.Summary, forbidden) {
			t.Fatalf("摘要泄露了未经审查的内容 %q: %s", forbidden, summary.Summary)
		}
	}
	if !strings.Contains(summary.Summary, "***已隐藏***") {
		t.Fatalf("摘要中的凭据必须被脱敏，实际 %q", summary.Summary)
	}
	if !strings.Contains(summary.Summary, "failed") {
		t.Fatalf("白名单字段应出现在摘要中，实际 %q", summary.Summary)
	}
}

// TestListTaskEventsHandlesEmptyPayload 校验空负载与不可解析负载都给出空摘要。
func TestListTaskEventsHandlesEmptyPayload(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	base := time.Now().UTC()
	fixture.historyEvent(t, fixture.task.ID, nil, domain.EventTypeTaskCreated, `{}`, base)
	// 只有白名单之外的字段：摘要为空，由调用方显示中性占位符。
	fixture.historyEvent(
		t, fixture.task.ID, nil, domain.EventTypeTaskStarted,
		`{"container_id":"c-1","work_dir":"/workspace/project"}`, base,
	)

	page, err := fixture.service.ListTaskEvents(
		fixture.ctx,
		fixture.task.ID.String(),
		app.EventTimelineRequest{},
	)
	if err != nil {
		t.Fatalf("查询任务事件失败: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("事件数量不符: %d", len(page.Events))
	}
	for _, summary := range page.Events {
		if summary.Summary != "" {
			t.Fatalf("没有可公开字段时摘要应为空，实际 %q", summary.Summary)
		}
	}
}

// TestListStepRepairCyclesReturnsHistory 校验多个周期的稳定顺序。
func TestListStepRepairCyclesReturnsHistory(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	base := time.Now().UTC()
	first := fixture.startCycle(t, fixture.task, fixture.step, 3, base)
	// 同一步骤同一时刻只能有一个活动周期（部分唯一索引保证）：开启第二个周期前
	// 必须先结束第一个。这里不绕过约束，而是按真实业务顺序构造历史。
	if err := fixture.db.CloseRepairCycle(fixture.ctx, first.ID, base.Add(30*time.Second)); err != nil {
		t.Fatalf("关闭第一个修复周期失败: %v", err)
	}
	second := fixture.startCycle(t, fixture.task, fixture.step, 5, base.Add(time.Minute))

	history, err := fixture.service.ListStepRepairCycles(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	)
	if err != nil {
		t.Fatalf("查询修复周期历史失败: %v", err)
	}
	if history.StepID != fixture.step.ID || history.TaskID != fixture.task.ID {
		t.Fatalf("归属不符: %+v", history)
	}
	if len(history.Cycles) != 2 {
		t.Fatalf("周期数量不符: %d", len(history.Cycles))
	}
	if history.Cycles[0].ID != first.ID || history.Cycles[1].ID != second.ID {
		t.Fatalf("周期顺序不符: %+v", history.Cycles)
	}
	if history.Cycles[0].Sequence != 1 || history.Cycles[1].Sequence != 2 {
		t.Fatalf("周期序号不符: %+v", history.Cycles)
	}
	if history.Cycles[0].ClosedAt == nil {
		t.Fatalf("第一个周期已关闭，必须有结束时间: %+v", history.Cycles[0])
	}
	// 未关闭的周期必须如实表达 closed_at 为空，不能由序号或尝试数推断。
	if history.Cycles[1].ClosedAt != nil {
		t.Fatalf("未关闭的周期 closed_at 必须为空: %+v", history.Cycles[1])
	}
	if !history.Cycles[1].Active() {
		t.Fatal("未关闭的周期必须是活动周期")
	}
}

// TestListStepRepairCyclesReportsClosedCycle 校验已关闭周期保留真实结束时间。
func TestListStepRepairCyclesReportsClosedCycle(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	base := time.Now().UTC()
	cycle := fixture.startCycle(t, fixture.task, fixture.step, 2, base)
	closedAt := base.Add(30 * time.Minute)
	if err := fixture.db.CloseRepairCycle(fixture.ctx, cycle.ID, closedAt); err != nil {
		t.Fatalf("关闭修复周期失败: %v", err)
	}

	history, err := fixture.service.ListStepRepairCycles(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	)
	if err != nil {
		t.Fatalf("查询修复周期历史失败: %v", err)
	}
	if len(history.Cycles) != 1 {
		t.Fatalf("周期数量不符: %d", len(history.Cycles))
	}
	stored := history.Cycles[0]
	if stored.ClosedAt == nil {
		t.Fatal("已关闭的周期必须有结束时间")
	}
	// 时间以 UTC 毫秒持久化，因此按毫秒比较而不是纳秒。
	if stored.ClosedAt.UnixMilli() != closedAt.UnixMilli() {
		t.Fatalf("结束时间不符: 期望 %v 实际 %v", closedAt, *stored.ClosedAt)
	}
	if stored.Active() {
		t.Fatal("已关闭的周期不应是活动周期")
	}
}

// TestListStepRepairCyclesReturnsEmptyForStepWithoutCycles 校验没有周期时返回空列表而非错误。
func TestListStepRepairCyclesReturnsEmptyForStepWithoutCycles(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	history, err := fixture.service.ListStepRepairCycles(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	)
	if err != nil {
		t.Fatalf("查询修复周期历史失败: %v", err)
	}
	if len(history.Cycles) != 0 {
		t.Fatalf("没有周期时应返回空列表，实际 %+v", history.Cycles)
	}
}

// TestListStepRepairCyclesRejectsForeignStep 校验跨任务访问被拒绝。
func TestListStepRepairCyclesRejectsForeignStep(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	fixture.startCycle(t, fixture.otherTask, fixture.otherStep, 3, time.Now().UTC())

	_, err := fixture.service.ListStepRepairCycles(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.otherStep.ID.String(),
	)
	if !errors.Is(err, app.ErrHistoryNotFound) {
		t.Fatalf("跨任务读取步骤周期必须被拒绝，实际 %v", err)
	}
}

// TestListStepRepairCyclesRejectsUnknownStep 校验步骤不存在时返回 404 类别。
func TestListStepRepairCyclesRejectsUnknownStep(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	_, err := fixture.service.ListStepRepairCycles(
		fixture.ctx,
		fixture.task.ID.String(),
		domain.NewID().String(),
	)
	if !errors.Is(err, app.ErrHistoryNotFound) {
		t.Fatalf("不存在的步骤应返回 ErrHistoryNotFound，实际 %v", err)
	}
}

// TestHistoryQueriesDoNotMutateState 校验历史查询不产生任何副作用。
func TestHistoryQueriesDoNotMutateState(t *testing.T) {
	t.Parallel()
	fixture := newHistoryFixture(t)
	base := time.Now().UTC()
	cycle := fixture.startCycle(t, fixture.task, fixture.step, 2, base)
	fixture.historyEvent(t, fixture.task.ID, &fixture.step.ID, domain.EventTypeStepStarted, `{}`, base)

	taskBefore, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	stepBefore, err := fixture.db.GetStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}

	for round := 0; round < 3; round++ {
		if _, err := fixture.service.ListTaskEvents(
			fixture.ctx,
			fixture.task.ID.String(),
			app.EventTimelineRequest{},
		); err != nil {
			t.Fatalf("查询任务事件失败: %v", err)
		}
		if _, err := fixture.service.ListStepRepairCycles(
			fixture.ctx,
			fixture.task.ID.String(),
			fixture.step.ID.String(),
		); err != nil {
			t.Fatalf("查询修复周期历史失败: %v", err)
		}
	}

	taskAfter, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if taskAfter.Status != taskBefore.Status || !taskAfter.UpdatedAt.Equal(taskBefore.UpdatedAt) {
		t.Fatalf("历史查询改写了任务: %+v -> %+v", taskBefore, taskAfter)
	}
	stepAfter, err := fixture.db.GetStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stepAfter.Status != stepBefore.Status || !stepAfter.UpdatedAt.Equal(stepBefore.UpdatedAt) {
		t.Fatalf("历史查询改写了步骤: %+v -> %+v", stepBefore, stepAfter)
	}
	cycles, err := fixture.db.ListRepairCyclesByStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取周期失败: %v", err)
	}
	if len(cycles) != 1 || cycles[0].ID != cycle.ID || cycles[0].ClosedAt != nil {
		t.Fatalf("历史查询改写了周期记录: %+v", cycles)
	}
	events, err := fixture.db.ListEventsByTask(fixture.ctx, fixture.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("历史查询追加了事件: %d", len(events))
	}
}

// TestEventTypeLabelLeavesUnknownTypesEmpty 校验未知事件类型没有中文标签。
func TestEventTypeLabelLeavesUnknownTypesEmpty(t *testing.T) {
	t.Parallel()
	if label := app.EventTypeLabel(domain.EventTypeTaskCreated); label == "" {
		t.Fatal("已知事件类型应有中文标签")
	}
	if label := app.EventTypeLabel(domain.EventType("legacy.removed")); label != "" {
		t.Fatalf("未知事件类型不应有中文标签，实际 %q", label)
	}
}
