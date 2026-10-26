package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// codeSaveAPIFixture 是代码保存记录列表接口的测试环境。
//
// 两条独立任务同时存在：用于验证接口按任务过滤，且不会跨任务泄露记录。
// 第一个任务有三条记录，分别覆盖"提交/推送/合并成功"、"成功/未知/未请求"与
// "持久化未确认 + 未核验" 三种互不相同的现场。
type codeSaveAPIFixture struct {
	handler   http.Handler
	db        *sqlite.DB
	ctx       context.Context
	developer domain.Role
	task      domain.Task
	otherTask domain.Task
	emptyTask domain.Task
	// records 是第一个任务的记录，按真实创建时间从旧到新排列。
	records []domain.CodeSaveRecord
	// otherRecord 是第二个任务的记录。
	otherRecord domain.CodeSaveRecord
}

// newCodeSaveAPIFixture 准备三条任务与各自的代码保存记录，并构造处理器。
func newCodeSaveAPIFixture(t *testing.T) *codeSaveAPIFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.SQLitePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	now := time.Now().UTC()
	fixture := &codeSaveAPIFixture{db: db, ctx: ctx}
	fixture.developer = apiRole(t, db, ctx, domain.RoleTypeDeveloper, "代码保存开发角色", now)
	fixture.task = apiTaskWithStep(t, db, ctx, now, fixture.developer, &domain.Step{}, 1)
	fixture.otherTask = apiTaskWithStep(t, db, ctx, now, fixture.developer, &domain.Step{}, 1)
	fixture.emptyTask = apiTaskWithStep(t, db, ctx, now, fixture.developer, &domain.Step{}, 1)

	fixture.records = []domain.CodeSaveRecord{
		apiCompletedCodeSave(t, db, ctx, fixture.task, now),
		apiPendingPushCodeSave(t, db, ctx, fixture.task, now.Add(time.Minute)),
		apiUnconfirmedCodeSave(t, db, ctx, fixture.task, now.Add(2*time.Minute)),
	}
	fixture.otherRecord = apiCompletedCodeSave(t, db, ctx, fixture.otherTask, now.Add(3*time.Minute))

	queries, err := app.NewCodeSaveQueryService(app.CodeSaveQueryOptions{Store: db})
	if err != nil {
		t.Fatalf("构造代码保存记录查询服务失败: %v", err)
	}
	fixture.handler = httpapi.NewHandler(httpapi.Deps{
		// 任务服务也要注入：未知子路径最终由任务处理器判定 404，真实装配同样如此。
		Tasks:           codeSaveTestTaskService(t, db),
		Health:          app.NewHealthService(db),
		SystemInfo:      func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		CodeSaveQueries: queries,
	})
	return fixture
}

// codeSaveTestTaskService 构造任务管理服务。
//
// 未知任务子路径最终由任务处理器判定 404，因此真实装配里任务服务总是存在的；
// 这里注入它与生产装配保持一致，避免用例把"任务服务缺失"误当作"接口不存在"。
func codeSaveTestTaskService(t *testing.T, db *sqlite.DB) *app.TaskService {
	t.Helper()
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	return tasks
}

// apiCodeSaveRecord 构造一条未执行的代码保存记录。
func apiCodeSaveRecord(
	t *testing.T,
	task domain.Task,
	at time.Time,
) domain.CodeSaveRecord {
	t.Helper()
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		task.ID.String(),
		"container-1",
		"docker-container-1",
		"origin",
		"https://git.example.com/group/repo.git",
		"agent/"+task.ID.String(),
		"main",
		false,
		at,
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	return record
}

// apiCompletedCodeSave 写入一条提交与推送都成功的记录。
func apiCompletedCodeSave(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	at time.Time,
) domain.CodeSaveRecord {
	t.Helper()
	record := apiCodeSaveRecord(t, task, at)
	record.ProjectID = "project-1"
	record.AttemptID = "attempt-1"
	if err := record.MarkCommit(
		domain.CommitStatusSucceeded,
		"1111111111111111111111111111111111111111",
		"提交一",
		at,
	); err != nil {
		t.Fatalf("记录提交结果失败: %v", err)
	}
	if err := record.MarkPush(
		domain.PushStatusSucceeded,
		"2222222222222222222222222222222222222222",
		at,
	); err != nil {
		t.Fatalf("记录推送结果失败: %v", err)
	}
	record.Finish(at)
	return apiPersistCodeSave(t, db, ctx, record)
}

// apiPendingPushCodeSave 写入一条推送结果未知的记录。
func apiPendingPushCodeSave(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	at time.Time,
) domain.CodeSaveRecord {
	t.Helper()
	record := apiCodeSaveRecord(t, task, at)
	record.MarkError("push", "push_unconfirmed", "推送结果无法确认", at)
	if err := record.MarkCommit(
		domain.CommitStatusSucceeded,
		"3333333333333333333333333333333333333333",
		"提交二",
		at,
	); err != nil {
		t.Fatalf("记录提交结果失败: %v", err)
	}
	if err := record.MarkPush(domain.PushStatusUnknown, "", at); err != nil {
		t.Fatalf("记录推送结果失败: %v", err)
	}
	return apiPersistCodeSave(t, db, ctx, record)
}

// apiUnconfirmedCodeSave 写入一条记录写入未确认、也尚未核验的记录。
//
// 该记录覆盖两个历史兼容语义：`persist_status=unconfirmed` 必须如实返回，
// `recovery_status` 为空时必须归一化为 unverified。
func apiUnconfirmedCodeSave(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	at time.Time,
) domain.CodeSaveRecord {
	t.Helper()
	record := apiCodeSaveRecord(t, task, at)
	record.MarkPersistFailure("写入状态失败: 连接中断", at)
	return apiPersistCodeSave(t, db, ctx, record)
}

// apiPersistCodeSave 写入一条代码保存记录并返回落盘后的内容。
func apiPersistCodeSave(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	record domain.CodeSaveRecord,
) domain.CodeSaveRecord {
	t.Helper()
	if err := db.CreateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	stored, err := db.GetCodeSaveRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	return *stored
}

// listPath 返回指定任务的代码保存记录列表路径。
func (f *codeSaveAPIFixture) listPath(taskID string) string {
	return httpapi.PathTasks + "/" + taskID + "/code-saves"
}

// get 发送 GET 请求。
func (f *codeSaveAPIFixture) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

// list 请求列表并解析响应。
func (f *codeSaveAPIFixture) list(
	t *testing.T,
	taskID string,
) httpapi.CodeSaveRecordListResponse {
	t.Helper()
	recorder := f.get(t, f.listPath(taskID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表查询应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.CodeSaveRecordListResponse
	decodeBody(t, recorder, &response)
	return response
}

// TestCodeSaveListReturnsTaskRecords 校验成功返回列表、真实记录 ID 与正确的记录数。
func TestCodeSaveListReturnsTaskRecords(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	recorder := fixture.get(t, fixture.listPath(fixture.task.ID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表查询应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.CodeSaveRecordListResponse
	decodeBody(t, recorder, &response)
	if response.Total != len(fixture.records) {
		t.Fatalf("total 应为 %d，实际 %d", len(fixture.records), response.Total)
	}
	if len(response.Records) != len(fixture.records) {
		t.Fatalf("records 数量应为 %d，实际 %d", len(fixture.records), len(response.Records))
	}
	// 记录 ID 必须真实存在且与落盘记录一一对应：Web 需要它发起只读核验。
	persisted := make(map[string]domain.CodeSaveRecord, len(fixture.records))
	for _, record := range fixture.records {
		persisted[record.ID.String()] = record
	}
	for _, item := range response.Records {
		stored, ok := persisted[item.ID]
		if !ok {
			t.Fatalf("响应中出现未知记录标识: %s", item.ID)
		}
		if item.TaskID != fixture.task.ID.String() {
			t.Fatalf("记录归属不符: %s", item.TaskID)
		}
		if item.TaskBranch != stored.TaskBranch || item.Repository != stored.Repository {
			t.Fatalf("记录字段与落盘数据不一致: %+v / %+v", item, stored)
		}
		if item.ContainerID != stored.ContainerID || item.RemoteName != stored.RemoteName {
			t.Fatalf("记录字段与落盘数据不一致: %+v / %+v", item, stored)
		}
	}
}

// TestCodeSaveListOrdersNewestFirst 校验多条记录的顺序正确。
func TestCodeSaveListOrdersNewestFirst(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	response := fixture.list(t, fixture.task.ID.String())
	if len(response.Records) < 2 {
		t.Fatalf("用例需要至少两条记录，实际 %d", len(response.Records))
	}
	for index := 1; index < len(response.Records); index++ {
		if response.Records[index-1].CreatedAt.Before(response.Records[index].CreatedAt) {
			t.Fatalf(
				"记录应按创建时间倒序排列，第 %d 条 %s 早于第 %d 条 %s",
				index-1,
				response.Records[index-1].CreatedAt,
				index,
				response.Records[index].CreatedAt,
			)
		}
	}
	// 最新一条应是最晚创建的那条未确认记录。
	latest := response.Records[0]
	if latest.PersistStatus != string(domain.PersistStatusUnconfirmed) {
		t.Fatalf("最新记录应是最晚创建的未确认记录，实际 %+v", latest)
	}
}

// TestCodeSaveListKeepsStagesIndependent 校验提交、推送、合并三阶段状态独立返回。
func TestCodeSaveListKeepsStagesIndependent(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	response := fixture.list(t, fixture.task.ID.String())
	byID := make(map[string]httpapi.CodeSaveRecordListEntryResponse, len(response.Records))
	for _, item := range response.Records {
		byID[item.ID] = item
	}
	// 全部成功的记录：三阶段分别成功，合并未请求。
	completed := byID[fixture.records[0].ID.String()]
	if completed.CommitStatus != string(domain.CommitStatusSucceeded) ||
		completed.PushStatus != string(domain.PushStatusSucceeded) ||
		completed.MergeStatus != string(domain.MergeStatusNotRequested) {
		t.Fatalf("完成记录的三阶段状态不符: %+v", completed)
	}
	if completed.CommitSHA != "1111111111111111111111111111111111111111" ||
		completed.RemoteCommitSHA != "2222222222222222222222222222222222222222" {
		t.Fatalf("完成记录的 SHA 不符: %+v", completed)
	}
	if completed.MergeEnabled {
		t.Fatal("未请求合并时 merge_enabled 应为 false")
	}
	if completed.CommitMessage != "提交一" {
		t.Fatalf("提交信息摘要不符: %s", completed.CommitMessage)
	}
	// 推送未知的记录：提交成功但推送 status 必须是 unknown，不能被归并为失败或成功。
	pending := byID[fixture.records[1].ID.String()]
	if pending.CommitStatus != string(domain.CommitStatusSucceeded) {
		t.Fatalf("推送未知的记录提交状态不符: %+v", pending)
	}
	if pending.PushStatus != string(domain.PushStatusUnknown) {
		t.Fatalf("推送结果未知必须按真实值返回，实际 %s", pending.PushStatus)
	}
	if pending.RemoteCommitSHA != "" {
		t.Fatalf("推送未知时不得伪造远程 commit: %s", pending.RemoteCommitSHA)
	}
	if pending.Stage != "push" || pending.ErrorCode != "push_unconfirmed" {
		t.Fatalf("阶段与错误分类不符: %+v", pending)
	}
	if pending.ErrorSummary != "推送结果无法确认" {
		t.Fatalf("失败摘要不符: %s", pending.ErrorSummary)
	}
}

// TestCodeSaveListKeepsPersistAndRecoverySemantics 校验历史兼容语义不被错误改写。
func TestCodeSaveListKeepsPersistAndRecoverySemantics(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	response := fixture.list(t, fixture.task.ID.String())
	byID := make(map[string]httpapi.CodeSaveRecordListEntryResponse, len(response.Records))
	for _, item := range response.Records {
		byID[item.ID] = item
	}
	// 未确认记录：persist_status 如实返回 unconfirmed，persist_error 一并输出。
	unconfirmed := byID[fixture.records[2].ID.String()]
	if unconfirmed.PersistStatus != string(domain.PersistStatusUnconfirmed) {
		t.Fatalf("记录写入未确认必须如实返回，实际 %q", unconfirmed.PersistStatus)
	}
	if unconfirmed.PersistError != "写入状态失败: 连接中断" {
		t.Fatalf("写入失败摘要不符: %s", unconfirmed.PersistError)
	}
	// 从未核验（落盘为空）的 recovery_status 必须归一化为 unverified，而不是空字符串。
	for _, item := range response.Records {
		if item.RecoveryStatus != string(domain.RecoveryStatusUnverified) {
			t.Fatalf("空恢复结论应归一化为 unverified，实际 %q", item.RecoveryStatus)
		}
	}
	// 历史记录的 persist_status 空值必须归一化为 persisted，不得输出空字符串。
	completed := byID[fixture.records[0].ID.String()]
	if completed.PersistStatus != string(domain.PersistStatusPersisted) {
		t.Fatalf("空记录写入状态应归一化为 persisted，实际 %q", completed.PersistStatus)
	}
}

// TestCodeSaveListReturnsEmptyList 校验空列表返回合法响应。
func TestCodeSaveListReturnsEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	recorder := fixture.get(t, fixture.listPath(fixture.emptyTask.ID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("空列表应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.CodeSaveRecordListResponse
	decodeBody(t, recorder, &response)
	if response.Records == nil {
		t.Fatal("records 应为空数组而不是 null")
	}
	if len(response.Records) != 0 || response.Total != 0 {
		t.Fatalf("空任务的列表应为空，实际 %+v", response)
	}
}

// TestCodeSaveListRejectsUnknownTasks 校验任务不存在或已删除返回 404。
func TestCodeSaveListRejectsUnknownTasks(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	task, err := fixture.db.GetTask(fixture.ctx, fixture.otherTask.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if err := task.CancelImmediately(time.Now().UTC()); err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	if err := fixture.db.UpdateTask(fixture.ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	if err := fixture.db.DeleteTask(
		fixture.ctx,
		fixture.otherTask.ID,
		[]domain.TaskStatus{domain.TaskStatusCancelled},
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	targets := map[string]string{
		"任务不存在":   fixture.listPath(domain.NewID().String()),
		"任务已逻辑删除": fixture.listPath(fixture.otherTask.ID.String()),
	}
	for name, target := range targets {
		recorder := fixture.get(t, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
		var response httpapi.ErrorResponse
		decodeBody(t, recorder, &response)
		if response.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码不符: %s", name, response.Error.Code)
		}
	}
}

// TestCodeSaveListRequiresService 校验依赖缺失返回 503 而不是 404 或空列表。
func TestCodeSaveListRequiresService(t *testing.T) {
	t.Parallel()
	handler := httpapi.NewHandler(httpapi.Deps{})
	target := httpapi.PathTasks + "/" + domain.NewID().String() + "/code-saves"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("依赖缺失应返回 503，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	decodeBody(t, recorder, &response)
	if response.Error.Code != httpapi.CodeInternal {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
}

// TestCodeSaveListStorageFailureIsNotNotFound 校验存储错误返回 500 而不是空列表。
func TestCodeSaveListStorageFailureIsNotNotFound(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	recorder := fixture.get(t, fixture.listPath(fixture.task.ID.String()))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("存储错误应返回 500，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	decodeBody(t, recorder, &response)
	if response.Error.Code != httpapi.CodeInternal {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
	if response.Error.Message != "服务内部错误" {
		t.Fatalf("内部错误应脱敏，实际 %q", response.Error.Message)
	}
}

// TestCodeSaveListRejectsUnsupportedMethods 校验写方法返回 405 且不产生副作用。
func TestCodeSaveListRejectsUnsupportedMethods(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	before := fixture.list(t, fixture.task.ID.String())
	target := fixture.listPath(fixture.task.ID.String())
	for _, method := range []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	} {
		request := httptest.NewRequest(method, target, nil)
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 应返回 405，实际 %d: %s", method, recorder.Code, recorder.Body.String())
		}
		if allow := recorder.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Fatalf("%s 的 Allow 头不符: %q", method, allow)
		}
		var response httpapi.ErrorResponse
		decodeBody(t, recorder, &response)
		if response.Error.Code != httpapi.CodeMethodNotAllowed {
			t.Fatalf("%s 错误代码不符: %s", method, response.Error.Code)
		}
	}
	after := fixture.list(t, fixture.task.ID.String())
	if len(after.Records) != len(before.Records) {
		t.Fatalf("写方法不应改动记录条数: %d / %d", len(before.Records), len(after.Records))
	}
}

// TestCodeSaveListRejectsUnknownShapes 校验未知路径不会误命中本接口。
func TestCodeSaveListRejectsUnknownShapes(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	base := httpapi.PathTasks + "/" + fixture.task.ID.String()
	targets := map[string]string{
		"缺少记录标识但多一段": base + "/code-saves/extra",
		"集合段拼错":      httpapi.PathTasks + "/" + fixture.task.ID.String() + "/code-save",
		"单条记录查询未提供":  base + "/code-saves/" + fixture.records[0].ID.String(),
		"集合路径后多段":    base + "/code-saves/" + fixture.records[0].ID.String() + "/extra",
	}
	for name, target := range targets {
		recorder := fixture.get(t, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
		var response httpapi.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s 应返回 JSON 错误: %s", name, recorder.Body.String())
		}
		if response.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码不符: %s", name, response.Error.Code)
		}
	}
}

// TestCodeSaveListQueryIsReadOnly 校验查询不修改任何持久化数据。
func TestCodeSaveListQueryIsReadOnly(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveAPIFixture(t)
	before := make(map[string]domain.CodeSaveRecord, len(fixture.records))
	for _, record := range fixture.records {
		stored, err := fixture.db.GetCodeSaveRecord(fixture.ctx, record.ID)
		if err != nil {
			t.Fatalf("读取代码保存记录失败: %v", err)
		}
		before[record.ID.String()] = *stored
	}
	taskBefore, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if recorder := fixture.get(t, fixture.listPath(fixture.task.ID.String())); recorder.Code != http.StatusOK {
		t.Fatalf("列表查询应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.get(t, fixture.listPath(fixture.task.ID.String())); recorder.Code != http.StatusOK {
		t.Fatalf("重复查询应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	for id, previous := range before {
		current, readErr := fixture.db.GetCodeSaveRecord(fixture.ctx, domain.ID(id))
		if readErr != nil {
			t.Fatalf("再次读取代码保存记录失败: %v", readErr)
		}
		if current.CommitStatus != previous.CommitStatus ||
			current.PushStatus != previous.PushStatus ||
			current.MergeStatus != previous.MergeStatus ||
			current.PersistStatus != previous.PersistStatus ||
			current.RecoveryStatus != previous.RecoveryStatus {
			t.Fatalf("查询不应改动代码保存记录: %s", id)
		}
		if !current.UpdatedAt.Equal(previous.UpdatedAt) {
			t.Fatalf("查询不应更新记录时间戳: %s", id)
		}
	}
	taskAfter, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("再次读取任务失败: %v", err)
	}
	if taskAfter.Status != taskBefore.Status {
		t.Fatalf("查询不应推进任务状态: %s / %s", taskBefore.Status, taskAfter.Status)
	}
	if taskAfter.UpdatedAt.After(taskBefore.UpdatedAt) {
		t.Fatalf("查询不应更新任务时间戳: %s / %s", taskBefore.UpdatedAt, taskAfter.UpdatedAt)
	}
}
