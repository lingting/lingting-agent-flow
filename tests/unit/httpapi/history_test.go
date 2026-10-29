package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// historyAPIFixture 是事件时间线与修复周期历史接口的测试环境。
type historyAPIFixture struct {
	handler   http.Handler
	db        *sqlite.DB
	dbPath    string
	ctx       context.Context
	task      domain.Task
	otherTask domain.Task
	step      domain.Step
	otherStep domain.Step
	// shutdown 是实时事件流的关闭信号：由用例自己关闭，用来确定性地结束推送循环，
	// 而不是依赖超时或等待客户端掉线。
	shutdown chan struct{}
}

// newHistoryAPIFixture 准备两条任务链，并构造装配了历史查询服务的处理器。
func newHistoryAPIFixture(t *testing.T) *historyAPIFixture {
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
	fixture := &historyAPIFixture{ctx: ctx, db: db, dbPath: dbPath}
	role := historyAPIRole(t, db, ctx, now)
	fixture.task, fixture.step = historyAPITask(t, db, ctx, now, role, "第一个任务")
	fixture.otherTask, fixture.otherStep = historyAPITask(t, db, ctx, now, role, "第二个任务")

	history, err := app.NewHistoryQueryService(db)
	if err != nil {
		t.Fatalf("构造历史查询服务失败: %v", err)
	}
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	shutdown := make(chan struct{})
	fixture.shutdown = shutdown
	fixture.handler = httpapi.NewHandler(httpapi.Deps{
		Health:     app.NewHealthService(db),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:      tasks,
		History:    history,
		Shutdown:   shutdown,
	})
	return fixture
}

// historyAPIRole 写入一个测试角色。
func historyAPIRole(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
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

// historyAPITask 写入需求、任务与一个步骤，并返回任务与步骤。
func historyAPITask(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
	role domain.Role,
	title string,
) (domain.Task, domain.Step) {
	t.Helper()
	requirement, err := domain.NewRequirement(title, title+"正文", domain.PriorityNormal, now)
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

// appendEvent 追加一条事件。
func (f *historyAPIFixture) appendEvent(
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

// get 发送 GET 请求。
func (f *historyAPIFixture) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

// eventsPath 返回任务事件时间线路径。
func (f *historyAPIFixture) eventsPath(taskID string) string {
	return httpapi.PathTasks + "/" + taskID + "/events"
}

// streamPath 返回任务事件实时流路径。
func (f *historyAPIFixture) streamPath(taskID string) string {
	return f.eventsPath(taskID) + "/stream"
}

// repairCyclesPath 返回步骤修复周期历史路径。
func (f *historyAPIFixture) repairCyclesPath(taskID, stepID string) string {
	return httpapi.PathTasks + "/" + taskID + "/steps/" + stepID + "/repair-cycles"
}

// TestListEventsReturnsJSONContract 校验成功响应与 JSON 契约。
func TestListEventsReturnsJSONContract(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	base := time.Now().UTC()
	first := fixture.appendEvent(
		t, fixture.task.ID, nil, domain.EventTypeTaskCreated, `{}`, base,
	)
	second := fixture.appendEvent(
		t,
		fixture.task.ID,
		&fixture.step.ID,
		domain.EventTypeTaskFailed,
		`{"summary":"token=secret-value","status":"failed"}`,
		base.Add(time.Second),
	)

	recorder := fixture.get(t, fixture.eventsPath(fixture.task.ID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("事件时间线应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.EventTimelineResponse
	decodeBody(t, recorder, &response)
	if response.TaskID != fixture.task.ID.String() {
		t.Fatalf("任务标识不符: %s", response.TaskID)
	}
	if response.Total != 2 || len(response.Records) != 2 {
		t.Fatalf("事件数量不符: %+v", response)
	}
	if response.Size != app.DefaultTaskPageSize || response.Offset != 0 {
		t.Fatalf("分页回显不符: size=%d offset=%d", response.Size, response.Offset)
	}
	if response.Records[0].ID != first.ID || response.Records[1].ID != second.ID {
		t.Fatalf("事件顺序不符: %+v", response.Records)
	}
	if response.Records[0].EventType != string(domain.EventTypeTaskCreated) {
		t.Fatalf("事件类型不符: %s", response.Records[0].EventType)
	}
	if response.Records[0].EventLabel != "任务创建" {
		t.Fatalf("已知事件类型应有中文标签，实际 %q", response.Records[0].EventLabel)
	}
	if !response.Records[0].TypeKnown {
		t.Fatal("已知事件类型应标记为已定义")
	}
	if response.Records[0].StepID != "" {
		t.Fatalf("任务级事件不应带步骤标识，实际 %q", response.Records[0].StepID)
	}
	if response.Records[1].StepID != fixture.step.ID.String() {
		t.Fatalf("步骤归属不符: %s", response.Records[1].StepID)
	}
	// 原始负载不得出现在 JSON 中：字段不存在比"值为空"更能防止误用。
	if strings.Contains(recorder.Body.String(), "payload") {
		t.Fatalf("响应不得包含 payload 字段: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "secret-value") {
		t.Fatalf("响应泄露了凭据: %s", recorder.Body.String())
	}
}

// TestListEventsRejectsUnknownTask 校验不存在的任务返回统一 404。
func TestListEventsRejectsUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	recorder := fixture.get(t, fixture.eventsPath(domain.NewID().String()))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的任务应返回 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	decodeBody(t, recorder, &response)
	if response.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误码不符: %s", response.Error.Code)
	}
}

// TestListEventsRejectsInvalidPaging 校验非法分页参数返回 400。
func TestListEventsRejectsInvalidPaging(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	cases := []string{
		"size=0",
		"size=-1",
		"size=abc",
		"size=100000",
		"offset=-1",
		"offset=notnumber",
	}
	for _, query := range cases {
		recorder := fixture.get(
			t,
			fixture.eventsPath(fixture.task.ID.String())+"?"+query,
		)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("参数 %q 应返回 400，实际 %d: %s", query, recorder.Code, recorder.Body.String())
		}
	}
}

// TestListEventsSupportsPagingAndFilters 校验分页元数据、类型筛选与空列表。
func TestListEventsSupportsPagingAndFilters(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	base := time.Now().UTC()
	for index := 0; index < 3; index++ {
		fixture.appendEvent(
			t,
			fixture.task.ID,
			nil,
			domain.EventTypeTaskCreated,
			`{}`,
			base.Add(time.Duration(index)*time.Second),
		)
	}
	fixture.appendEvent(t, fixture.task.ID, nil, domain.EventTypeTaskFailed, `{}`, base.Add(time.Minute))

	page := fixture.get(
		t,
		fixture.eventsPath(fixture.task.ID.String())+"?size=2&offset=2",
	)
	if page.Code != http.StatusOK {
		t.Fatalf("分页查询应返回 200，实际 %d: %s", page.Code, page.Body.String())
	}
	var paged httpapi.EventTimelineResponse
	decodeBody(t, page, &paged)
	if paged.Total != 4 || len(paged.Records) != 2 || paged.Size != 2 || paged.Offset != 2 {
		t.Fatalf("分页结果不符: %+v", paged)
	}

	filtered := fixture.get(
		t,
		fixture.eventsPath(fixture.task.ID.String())+"?type=task.failed",
	)
	if filtered.Code != http.StatusOK {
		t.Fatalf("类型筛选应返回 200，实际 %d: %s", filtered.Code, filtered.Body.String())
	}
	var onlyFailed httpapi.EventTimelineResponse
	decodeBody(t, filtered, &onlyFailed)
	if onlyFailed.Total != 1 || len(onlyFailed.Records) != 1 {
		t.Fatalf("类型筛选结果不符: %+v", onlyFailed)
	}

	empty := fixture.get(
		t,
		fixture.eventsPath(fixture.task.ID.String())+"?type=verification.passed",
	)
	if empty.Code != http.StatusOK {
		t.Fatalf("空结果应返回 200，实际 %d: %s", empty.Code, empty.Body.String())
	}
	var noEvents httpapi.EventTimelineResponse
	decodeBody(t, empty, &noEvents)
	if noEvents.Total != 0 || len(noEvents.Records) != 0 {
		t.Fatalf("空结果应为空列表: %+v", noEvents)
	}
	if noEvents.Records == nil {
		t.Fatal("空结果必须序列化为 []，而不是 null")
	}
}

// TestListEventsShowsUnknownTypeSafely 校验未知事件类型的安全展示契约。
func TestListEventsShowsUnknownTypeSafely(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	raw := testkit.OpenSQLite(t, fixture.dbPath)
	testkit.MustExec(
		t,
		raw,
		`INSERT INTO events (task_id, step_id, event_type, payload_json, created_at)
		 VALUES (?, NULL, 'legacy.removed', '{"work_dir":"/workspace/project"}', ?)`,
		fixture.task.ID.String(),
		time.Now().UTC().UnixMilli(),
	)

	recorder := fixture.get(t, fixture.eventsPath(fixture.task.ID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("查询应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.EventTimelineResponse
	decodeBody(t, recorder, &response)
	if len(response.Records) != 1 {
		t.Fatalf("事件数量不符: %d", len(response.Records))
	}
	record := response.Records[0]
	if record.EventType != "legacy.removed" {
		t.Fatalf("未知类型必须原样返回，实际 %q", record.EventType)
	}
	if record.TypeKnown {
		t.Fatal("未知类型不应标记为已定义")
	}
	if record.EventLabel != "" {
		t.Fatalf("未知类型没有中文标签，实际 %q", record.EventLabel)
	}
	if record.Summary != "" {
		t.Fatalf("没有白名单字段时摘要应为空，实际 %q", record.Summary)
	}
	if strings.Contains(recorder.Body.String(), "/workspace/project") {
		t.Fatalf("响应泄露了内部工作目录: %s", recorder.Body.String())
	}
}

// TestListEventsRejectsNonGetMethod 校验只读接口拒绝写方法。
func TestListEventsRejectsNonGetMethod(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	request := httptest.NewRequest(
		http.MethodPost,
		fixture.eventsPath(fixture.task.ID.String()),
		nil,
	)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应返回 405，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestTaskEventStreamRejectsNonGetMethod 校验实时流只接受 GET，并声明 Allow。
func TestTaskEventStreamRejectsNonGetMethod(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	request := httptest.NewRequest(
		http.MethodPost,
		fixture.streamPath(fixture.task.ID.String()),
		nil,
	)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应返回 405，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if allow := recorder.Header().Get("Allow"); allow != "GET" {
		t.Fatalf("405 响应必须声明只允许 GET，实际 Allow=%q", allow)
	}
	var response httpapi.ErrorResponse
	decodeBody(t, recorder, &response)
	if response.Error.Code != httpapi.CodeMethodNotAllowed {
		t.Fatalf("错误码不符: %s", response.Error.Code)
	}
}

// TestTaskEventStreamRejectsInvalidCursor 校验非法游标在写响应头之前返回统一 400。
//
// 三种情况都必须被拒绝而不是被当成 0：非整数、负数，以及大于该任务最新事件标识的
// 游标。后者的语义是"不可能来自本任务"，静默等待会表现成"连接正常但永远没有事件"。
func TestTaskEventStreamRejectsInvalidCursor(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	latest := fixture.appendEvent(
		t, fixture.task.ID, nil, domain.EventTypeTaskCreated, `{}`, time.Now().UTC(),
	)
	cases := []string{
		"last_event_id=abc",
		"last_event_id=-1",
		"last_event_id=" + strconv.FormatInt(latest.ID+1, 10),
	}
	for _, query := range cases {
		recorder := fixture.get(
			t,
			fixture.streamPath(fixture.task.ID.String())+"?"+query,
		)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("游标 %q 应返回 400，实际 %d: %s", query, recorder.Code, recorder.Body.String())
		}
		var response httpapi.ErrorResponse
		decodeBody(t, recorder, &response)
		if response.Error.Code != httpapi.CodeInvalidRequest {
			t.Fatalf("游标 %q 的错误码不符: %s", query, response.Error.Code)
		}
	}
}

// TestTaskEventStreamRejectsUnknownTask 校验不存在的任务返回 404，而不是空事件流。
//
// 空事件流会让调用方以为"这个任务还没有事件"，与"任务根本不存在"混为一谈。
func TestTaskEventStreamRejectsUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	recorder := fixture.get(t, fixture.streamPath(domain.NewID().String()))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的任务应返回 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	decodeBody(t, recorder, &response)
	if response.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误码不符: %s", response.Error.Code)
	}
}

// streamFrame 是一条 SSE 消息帧，只保留协议字段。
type streamFrame struct {
	id   string
	data string
}

// readEventStreamFrames 解析 SSE 帧：消息帧交给 frames，结束时关闭 frames 并写出原因。
//
// 心跳是 SSE 注释行（": ping"），不产生消息帧；游标由帧里的 `id:` 携带。
func readEventStreamFrames(body io.Reader, frames chan<- streamFrame, done chan<- error) {
	defer close(frames)
	reader := bufio.NewReader(body)
	var (
		id   string
		data []string
	)
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "":
			if id != "" && len(data) > 0 {
				frames <- streamFrame{data: strings.Join(data, "\n"), id: id}
			}
			id, data = "", nil
		case strings.HasPrefix(line, ":"):
			// 心跳注释帧不携带业务数据。
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		}
		if err != nil {
			done <- err
			return
		}
	}
}

// nextStreamFrame 等待下一帧；流提前结束或超时都会让用例失败。
func nextStreamFrame(t *testing.T, frames <-chan streamFrame, done <-chan error) streamFrame {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if !ok {
			t.Fatal("事件流在推送预期事件之前就结束了")
		}
		return frame
	case err := <-done:
		t.Fatalf("读取事件流失败，未收到预期事件: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("等待事件流推送超时")
	}
	return streamFrame{}
}

// TestTaskEventStreamPushesPersistedEvents 校验实时流推送已持久化事件并按游标续传。
//
// 这是唯一需要真实流式连接的用例：响应头写出后处理器进入推送循环，因此用
// httptest.Server 而不是 ResponseRecorder，并由 fixture 的关闭信号确定性地结束循环，
// 不依赖超时，也不要求用例等待客户端掉线。
func TestTaskEventStreamPushesPersistedEvents(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	base := time.Now().UTC()
	first := fixture.appendEvent(
		t,
		fixture.task.ID,
		nil,
		domain.EventTypeTaskCreated,
		`{"summary":"事件一"}`,
		base,
	)

	server := httptest.NewServer(fixture.handler)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	openStream := func(t *testing.T, query string) *http.Response {
		t.Helper()
		target := server.URL + fixture.streamPath(fixture.task.ID.String()) + "?" + query
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			t.Fatalf("构造事件流请求失败: %v", err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("建立事件流失败: %v", err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		if response.StatusCode != http.StatusOK {
			t.Fatalf("事件流应返回 200，实际 %d", response.StatusCode)
		}
		return response
	}

	// 游标取 0：已持久化的事件必须立即补发，验证"连接建立后立即读取一次"的行为。
	response := openStream(t, "last_event_id=0")
	if got := response.Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("响应类型不符: %q", got)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-cache, no-transform" {
		t.Fatalf("事件流必须禁止缓存与转换，实际 %q", got)
	}
	if got := response.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("事件流必须关闭代理缓冲，实际 %q", got)
	}

	frames := make(chan streamFrame, 8)
	done := make(chan error, 1)
	go readEventStreamFrames(response.Body, frames, done)

	firstFrame := nextStreamFrame(t, frames, done)
	if firstFrame.id != strconv.FormatInt(first.ID, 10) {
		t.Fatalf("首帧游标不符: %q", firstFrame.id)
	}
	var firstEvent httpapi.EventResponse
	if err := json.Unmarshal([]byte(firstFrame.data), &firstEvent); err != nil {
		t.Fatalf("解析首帧事件失败: %v", err)
	}
	if firstEvent.ID != first.ID || firstEvent.TaskID != fixture.task.ID.String() {
		t.Fatalf("首帧事件归属不符: %+v", firstEvent)
	}
	if firstEvent.EventType != string(domain.EventTypeTaskCreated) || firstEvent.Summary != "事件一" {
		t.Fatalf("首帧事件字段不符: %+v", firstEvent)
	}
	if strings.Contains(firstFrame.data, `"payload"`) {
		t.Fatalf("推送帧不得包含原始负载: %s", firstFrame.data)
	}

	// 流建立之后写入的事件必须被推送，说明增量读取确实按游标继续推进。
	second := fixture.appendEvent(
		t,
		fixture.task.ID,
		&fixture.step.ID,
		domain.EventTypeStepStarted,
		`{}`,
		base.Add(time.Second),
	)
	secondFrame := nextStreamFrame(t, frames, done)
	if secondFrame.id != strconv.FormatInt(second.ID, 10) {
		t.Fatalf("增量帧游标不符: %q", secondFrame.id)
	}
	var secondEvent httpapi.EventResponse
	if err := json.Unmarshal([]byte(secondFrame.data), &secondEvent); err != nil {
		t.Fatalf("解析增量帧事件失败: %v", err)
	}
	if secondEvent.ID != second.ID || secondEvent.StepID != fixture.step.ID.String() {
		t.Fatalf("增量帧事件归属不符: %+v", secondEvent)
	}

	// 关闭信号让推送循环正常收敛：流以 EOF 结束，而不是一直挂到客户端超时。
	close(fixture.shutdown)
	select {
	case frame, ok := <-frames:
		if ok {
			t.Fatalf("关闭后不应再推送事件: %+v", frame)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("关闭信号未能结束事件流")
	}
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("关闭后事件流应以 EOF 结束，实际 %v", err)
	}

	// 用已送达的游标重连：已经送到的事件不得重放（关闭信号已生效，因此立即结束）。
	resumed := openStream(t, "last_event_id="+strconv.FormatInt(second.ID, 10))
	resumeFrames := make(chan streamFrame, 8)
	resumeDone := make(chan error, 1)
	go readEventStreamFrames(resumed.Body, resumeFrames, resumeDone)
	select {
	case frame, ok := <-resumeFrames:
		if ok {
			t.Fatalf("用已送达游标重连时不得重放事件: %+v", frame)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("用已送达游标重连的事件流未能结束")
	}
	if err := <-resumeDone; !errors.Is(err, io.EOF) {
		t.Fatalf("重连事件流应以 EOF 结束，实际 %v", err)
	}
}

// TestListRepairCyclesReturnsHistory 校验成功响应与 JSON 契约。
func TestListRepairCyclesReturnsHistory(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	base := time.Now().UTC()
	first := historyAPICycle(t, fixture, fixture.task, fixture.step, 3, base)
	if err := fixture.db.CloseRepairCycle(fixture.ctx, first.ID, base.Add(time.Minute)); err != nil {
		t.Fatalf("关闭周期失败: %v", err)
	}
	second := historyAPICycle(t, fixture, fixture.task, fixture.step, 5, base.Add(2*time.Minute))

	recorder := fixture.get(
		t,
		fixture.repairCyclesPath(fixture.task.ID.String(), fixture.step.ID.String()),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("修复周期历史应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.RepairCycleHistoryResponse
	decodeBody(t, recorder, &response)
	if response.TaskID != fixture.task.ID.String() || response.StepID != fixture.step.ID.String() {
		t.Fatalf("归属不符: %+v", response)
	}
	if response.Total != 2 || len(response.Records) != 2 {
		t.Fatalf("周期数量不符: %+v", response)
	}
	closed := response.Records[0]
	if closed.ID != first.ID.String() || closed.Sequence != 1 || closed.MaxAttempts != 3 {
		t.Fatalf("第一个周期字段不符: %+v", closed)
	}
	if closed.ClosedAt == nil {
		t.Fatal("已关闭的周期必须返回结束时间")
	}
	if closed.OpenedBy != "user_reopen" {
		t.Fatalf("开启来源不符: %q", closed.OpenedBy)
	}
	active := response.Records[1]
	if active.ID != second.ID.String() || active.Sequence != 2 || active.MaxAttempts != 5 {
		t.Fatalf("第二个周期字段不符: %+v", active)
	}
	if active.ClosedAt != nil {
		t.Fatalf("未关闭的周期不得返回结束时间: %+v", active)
	}
	if strings.Contains(recorder.Body.String(), `"closed_at":null`) {
		t.Fatalf("未记录的结束时间应省略字段而不是 null: %s", recorder.Body.String())
	}
}

// TestListRepairCyclesReturnsEmptyList 校验没有周期时返回空数组而不是 null。
func TestListRepairCyclesReturnsEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	recorder := fixture.get(
		t,
		fixture.repairCyclesPath(fixture.task.ID.String(), fixture.step.ID.String()),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("空历史应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.RepairCycleHistoryResponse
	decodeBody(t, recorder, &response)
	if response.Total != 0 || len(response.Records) != 0 {
		t.Fatalf("空历史应为空列表: %+v", response)
	}
	if response.Records == nil {
		t.Fatal("空列表必须序列化为 []，而不是 null")
	}
}

// TestListRepairCyclesRejectsForeignStep 校验步骤与任务归属不一致时拒绝查询。
func TestListRepairCyclesRejectsForeignStep(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	historyAPICycle(t, fixture, fixture.otherTask, fixture.otherStep, 2, time.Now().UTC())
	recorder := fixture.get(
		t,
		fixture.repairCyclesPath(fixture.task.ID.String(), fixture.otherStep.ID.String()),
	)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("跨任务读取应返回 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestListRepairCyclesRejectsUnknownTargets 校验任务与步骤都不存在时返回 404。
func TestListRepairCyclesRejectsUnknownTargets(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	unknownTask := fixture.get(
		t,
		fixture.repairCyclesPath(domain.NewID().String(), fixture.step.ID.String()),
	)
	if unknownTask.Code != http.StatusNotFound {
		t.Fatalf("不存在的任务应返回 404，实际 %d", unknownTask.Code)
	}
	unknownStep := fixture.get(
		t,
		fixture.repairCyclesPath(fixture.task.ID.String(), domain.NewID().String()),
	)
	if unknownStep.Code != http.StatusNotFound {
		t.Fatalf("不存在的步骤应返回 404，实际 %d", unknownStep.Code)
	}
}

// TestHistoryEndpointsUnavailableWithoutService 校验服务未装配时返回 503 而不是空列表。
func TestHistoryEndpointsUnavailableWithoutService(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	handler := httpapi.NewHandler(httpapi.Deps{
		Health:     app.NewHealthService(fixture.db),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
	})
	for _, target := range []string{
		fixture.eventsPath(fixture.task.ID.String()),
		fixture.repairCyclesPath(fixture.task.ID.String(), fixture.step.ID.String()),
		fixture.streamPath(fixture.task.ID.String()),
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s 应返回 503，实际 %d: %s", target, recorder.Code, recorder.Body.String())
		}
	}
}

// TestHistoryEndpointsAreReadOnly 校验只读请求不改变任何持久化状态。
func TestHistoryEndpointsAreReadOnly(t *testing.T) {
	t.Parallel()
	fixture := newHistoryAPIFixture(t)
	base := time.Now().UTC()
	cycle := historyAPICycle(t, fixture, fixture.task, fixture.step, 2, base)
	fixture.appendEvent(t, fixture.task.ID, &fixture.step.ID, domain.EventTypeStepStarted, `{}`, base)

	taskBefore, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	stepBefore, err := fixture.db.GetStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}

	for round := 0; round < 3; round++ {
		if recorder := fixture.get(t, fixture.eventsPath(fixture.task.ID.String())); recorder.Code != http.StatusOK {
			t.Fatalf("事件查询失败: %d", recorder.Code)
		}
		if recorder := fixture.get(
			t,
			fixture.repairCyclesPath(fixture.task.ID.String(), fixture.step.ID.String()),
		); recorder.Code != http.StatusOK {
			t.Fatalf("周期查询失败: %d", recorder.Code)
		}
	}

	taskAfter, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if taskAfter.Status != taskBefore.Status || !taskAfter.UpdatedAt.Equal(taskBefore.UpdatedAt) {
		t.Fatalf("只读接口改写了任务: %+v -> %+v", taskBefore, taskAfter)
	}
	stepAfter, err := fixture.db.GetStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stepAfter.Status != stepBefore.Status || !stepAfter.UpdatedAt.Equal(stepBefore.UpdatedAt) {
		t.Fatalf("只读接口改写了步骤: %+v -> %+v", stepBefore, stepAfter)
	}
	cycles, err := fixture.db.ListRepairCyclesByStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取周期失败: %v", err)
	}
	if len(cycles) != 1 || cycles[0].ID != cycle.ID {
		t.Fatalf("只读接口改写了周期: %+v", cycles)
	}
	attempts, err := fixture.db.ListAttemptsByStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("只读接口创建了执行尝试: %d", len(attempts))
	}
	events, err := fixture.db.ListEventsByTask(fixture.ctx, fixture.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("只读接口追加了事件: %d", len(events))
	}
}

// historyAPICycle 开启一个修复周期。
func historyAPICycle(
	t *testing.T,
	fixture *historyAPIFixture,
	task domain.Task,
	step domain.Step,
	maxAttempts int,
	at time.Time,
) domain.RepairCycle {
	t.Helper()
	cycle, err := fixture.db.StartRepairCycle(fixture.ctx, storage.StartRepairCycleRequest{
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
