package httpserver_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/httpserver"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// codeSaveServerFixture 是代码保存记录列表接口的真实服务端现场。
type codeSaveServerFixture struct {
	baseURL string
	store   *sqlite.DB
	// first 与 second 是两条互不相关的任务。
	first  domain.Task
	second domain.Task
	// firstRecords 是第一个任务的记录，按真实创建时间从旧到新排列。
	firstRecords []domain.CodeSaveRecord
	secondRecord domain.CodeSaveRecord
}

// startCodeSaveServer 打开真实数据库、写入两条任务链并启动真实 HTTP 服务。
//
// 依赖注入走正式路径：查询服务由真实存储构造，再经 httpserver.Options 传入，
// 因此这里验证的是"路由 + 依赖装配 + 应用层 + 真实 SQLite"的完整链路。
// 注意没有注入任何容器 / SSH 能力：正是为了证明列表查询不需要远程依赖。
func startCodeSaveServer(t *testing.T) *codeSaveServerFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	workspace.Cleanup(store.Close)
	now := time.Now().UTC()
	fixture := &codeSaveServerFixture{store: store}
	fixture.first = codeSaveServerTask(t, store, ctx, now, "第一个任务")
	fixture.second = codeSaveServerTask(t, store, ctx, now, "第二个任务")
	fixture.firstRecords = []domain.CodeSaveRecord{
		codeSaveServerRecord(t, store, ctx, fixture.first, now, "completed"),
		codeSaveServerRecord(t, store, ctx, fixture.first, now.Add(time.Minute), "unknown_push"),
		codeSaveServerRecord(t, store, ctx, fixture.first, now.Add(2*time.Minute), "unconfirmed"),
	}
	fixture.secondRecord = codeSaveServerRecord(
		t,
		store,
		ctx,
		fixture.second,
		now.Add(3*time.Minute),
		"completed",
	)
	queries, err := app.NewCodeSaveQueryService(app.CodeSaveQueryOptions{Store: store})
	if err != nil {
		t.Fatalf("构造代码保存记录查询服务失败: %v", err)
	}
	server, err := httpserver.New(httpserver.Options{
		Address:         "127.0.0.1:0",
		Files:           roleWebFiles(),
		Health:          app.NewHealthService(store),
		SystemInfo:      func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		CodeSaveQueries: queries,
	})
	if err != nil {
		t.Fatalf("构造 HTTP 服务失败: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听随机端口失败: %v", err)
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(runCtx, listener)
	}()
	workspace.Cleanup(func() error {
		runCancel()
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			return nil
		}
	})
	fixture.baseURL = "http://" + listener.Addr().String()
	return fixture
}

// codeSaveServerTask 写入一条需求与一个任务。
func codeSaveServerTask(
	t *testing.T,
	store *sqlite.DB,
	ctx context.Context,
	now time.Time,
	title string,
) domain.Task {
	t.Helper()
	requirement, err := domain.NewRequirement(title, title+"正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := store.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return task
}

// codeSaveServerRecord 写入一条指定阶段组合的代码保存记录。
func codeSaveServerRecord(
	t *testing.T,
	store *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	at time.Time,
	kind string,
) domain.CodeSaveRecord {
	t.Helper()
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		task.ID.String(),
		"container-"+task.ID.String(),
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
	switch kind {
	case "completed":
		if err := record.MarkCommit(
			domain.CommitStatusSucceeded,
			"1111111111111111111111111111111111111111",
			"提交 "+task.ID.String(),
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
	case "unknown_push":
		record.MarkError("push", "push_unconfirmed", "推送结果无法确认", at)
		if err := record.MarkCommit(
			domain.CommitStatusSucceeded,
			"3333333333333333333333333333333333333333",
			"提交 "+task.ID.String(),
			at,
		); err != nil {
			t.Fatalf("记录提交结果失败: %v", err)
		}
		if err := record.MarkPush(domain.PushStatusUnknown, "", at); err != nil {
			t.Fatalf("记录推送结果失败: %v", err)
		}
	case "unconfirmed":
		record.MarkPersistFailure("写入状态失败: 连接中断", at)
	default:
		t.Fatalf("未知的记录类型: %s", kind)
	}
	if err := store.CreateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	stored, err := store.GetCodeSaveRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	return *stored
}

// codeSaveServerPath 返回指定任务的代码保存记录列表路径。
func codeSaveServerPath(taskID string) string {
	return httpapi.PathTasks + "/" + taskID + "/code-saves"
}

// TestCodeSaveListOverRealServer 校验查询结果与真实落盘数据一致。
func TestCodeSaveListOverRealServer(t *testing.T) {
	t.Parallel()
	fixture := startCodeSaveServer(t)
	target := codeSaveServerPath(fixture.first.ID.String())
	status, header, body := get(t, fixture.baseURL, target)
	if status != http.StatusOK {
		t.Fatalf("列表查询应返回 200，实际 %d，响应: %s", status, body)
	}
	if header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("列表查询应为 JSON，实际 %q", header.Get("Content-Type"))
	}
	var response httpapi.CodeSaveRecordListResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("解析列表响应失败: %v，响应: %s", err, body)
	}
	// 与真实落盘数据逐字段对照，而不是只比对固定 JSON。
	persisted, err := fixture.store.ListCodeSaveRecordsByTask(
		context.Background(),
		fixture.first.ID.String(),
	)
	if err != nil {
		t.Fatalf("读取落盘记录失败: %v", err)
	}
	if response.Total != len(persisted) || len(response.Records) != len(persisted) {
		t.Fatalf(
			"记录数量与落盘数据不一致: 响应 %d/%d，落盘 %d",
			len(response.Records),
			response.Total,
			len(persisted),
		)
	}
	for index, record := range persisted {
		item := response.Records[index]
		if item.ID != record.ID.String() {
			t.Fatalf("第 %d 条记录标识不一致: %s / %s", index, item.ID, record.ID)
		}
		if item.TaskID != record.TaskID {
			t.Fatalf("第 %d 条记录归属不一致: %s / %s", index, item.TaskID, record.TaskID)
		}
		if item.TaskBranch != record.TaskBranch || item.ContainerID != record.ContainerID {
			t.Fatalf("第 %d 条记录字段不一致: %+v", index, item)
		}
		if item.CommitStatus != record.CommitStatus.String() ||
			item.PushStatus != record.PushStatus.String() ||
			item.MergeStatus != record.MergeStatus.String() {
			t.Fatalf("第 %d 条记录三阶段状态不一致: %+v", index, item)
		}
		if item.CommitSHA != record.CommitSHA || item.RemoteCommitSHA != record.RemoteCommitSHA {
			t.Fatalf("第 %d 条记录 SHA 不一致: %+v", index, item)
		}
		if item.PersistStatus != record.PersistStatus.Normalized().String() {
			t.Fatalf("第 %d 条记录写入状态不一致: %s", index, item.PersistStatus)
		}
		if item.RecoveryStatus != record.RecoveryStatus.Normalized().String() {
			t.Fatalf("第 %d 条记录恢复结论不一致: %s", index, item.RecoveryStatus)
		}
		if item.ErrorSummary != record.ErrorSummary || item.PersistError != record.PersistError {
			t.Fatalf("第 %d 条记录错误摘要不一致: %+v", index, item)
		}
	}
}

// TestCodeSaveListDoesNotCrossTasksOverRealServer 校验查询不会泄露其他任务的记录。
func TestCodeSaveListDoesNotCrossTasksOverRealServer(t *testing.T) {
	t.Parallel()
	fixture := startCodeSaveServer(t)
	first, _, firstBody := get(t, fixture.baseURL, codeSaveServerPath(fixture.first.ID.String()))
	if first != http.StatusOK {
		t.Fatalf("第一个任务的列表应返回 200，实际 %d，响应: %s", first, firstBody)
	}
	second, _, secondBody := get(t, fixture.baseURL, codeSaveServerPath(fixture.second.ID.String()))
	if second != http.StatusOK {
		t.Fatalf("第二个任务的列表应返回 200，实际 %d，响应: %s", second, secondBody)
	}
	var firstResponse httpapi.CodeSaveRecordListResponse
	if err := json.Unmarshal([]byte(firstBody), &firstResponse); err != nil {
		t.Fatalf("解析第一个任务响应失败: %v", err)
	}
	var secondResponse httpapi.CodeSaveRecordListResponse
	if err := json.Unmarshal([]byte(secondBody), &secondResponse); err != nil {
		t.Fatalf("解析第二个任务响应失败: %v", err)
	}
	if len(firstResponse.Records) != len(fixture.firstRecords) {
		t.Fatalf("第一个任务应有 %d 条记录，实际 %d", len(fixture.firstRecords), len(firstResponse.Records))
	}
	for _, item := range firstResponse.Records {
		if item.TaskID != fixture.first.ID.String() {
			t.Fatalf("第一个任务的响应中混入了其他任务: %s", item.TaskID)
		}
		if item.ID == fixture.secondRecord.ID.String() {
			t.Fatal("第一个任务的响应泄露了第二个任务的记录")
		}
	}
	if len(secondResponse.Records) != 1 {
		t.Fatalf("第二个任务应有 1 条记录，实际 %d", len(secondResponse.Records))
	}
	if secondResponse.Records[0].ID != fixture.secondRecord.ID.String() {
		t.Fatalf("第二个任务的记录标识不符: %s", secondResponse.Records[0].ID)
	}
	for _, record := range fixture.firstRecords {
		if secondResponse.Records[0].ID == record.ID.String() {
			t.Fatal("第二个任务的响应泄露了第一个任务的记录")
		}
	}
}

// TestCodeSaveListDistinguishesMissingTaskFromEmptyList 校验不存在的任务与空列表可明确区分。
func TestCodeSaveListDistinguishesMissingTaskFromEmptyList(t *testing.T) {
	t.Parallel()
	fixture := startCodeSaveServer(t)
	empty := codeSaveServerTask(t, fixture.store, context.Background(), time.Now().UTC(), "空任务")
	status, _, body := get(t, fixture.baseURL, codeSaveServerPath(empty.ID.String()))
	if status != http.StatusOK {
		t.Fatalf("没有记录的任务应返回 200，实际 %d，响应: %s", status, body)
	}
	var listResponse httpapi.CodeSaveRecordListResponse
	if err := json.Unmarshal([]byte(body), &listResponse); err != nil {
		t.Fatalf("解析空列表响应失败: %v，响应: %s", err, body)
	}
	if len(listResponse.Records) != 0 || listResponse.Total != 0 {
		t.Fatalf("空任务应返回空列表，实际 %+v", listResponse)
	}
	missingStatus, _, missingBody := get(
		t,
		fixture.baseURL,
		codeSaveServerPath(domain.NewID().String()),
	)
	if missingStatus != http.StatusNotFound {
		t.Fatalf("不存在的任务应返回 404，实际 %d，响应: %s", missingStatus, missingBody)
	}
	var missingResponse httpapi.ErrorResponse
	if err := json.Unmarshal([]byte(missingBody), &missingResponse); err != nil {
		t.Fatalf("应返回 JSON 错误: %s", missingBody)
	}
	if missingResponse.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码不符: %s", missingResponse.Error.Code)
	}
}

// TestCodeSaveListOverRealServerIsReadOnly 校验查询不会改变数据库内容。
func TestCodeSaveListOverRealServerIsReadOnly(t *testing.T) {
	t.Parallel()
	fixture := startCodeSaveServer(t)
	ctx := context.Background()
	before, err := fixture.store.ListCodeSaveRecordsByTask(ctx, fixture.first.ID.String())
	if err != nil {
		t.Fatalf("读取落盘记录失败: %v", err)
	}
	taskBefore, err := fixture.store.GetTask(ctx, fixture.first.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	target := codeSaveServerPath(fixture.first.ID.String())
	for range 2 {
		status, _, body := get(t, fixture.baseURL, target)
		if status != http.StatusOK {
			t.Fatalf("列表查询应返回 200，实际 %d，响应: %s", status, body)
		}
	}
	after, err := fixture.store.ListCodeSaveRecordsByTask(ctx, fixture.first.ID.String())
	if err != nil {
		t.Fatalf("再次读取落盘记录失败: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("查询不应改动记录条数: %d / %d", len(before), len(after))
	}
	for index := range before {
		if after[index].CommitStatus != before[index].CommitStatus ||
			after[index].PushStatus != before[index].PushStatus ||
			after[index].MergeStatus != before[index].MergeStatus ||
			after[index].PersistStatus != before[index].PersistStatus ||
			after[index].RecoveryStatus != before[index].RecoveryStatus {
			t.Fatalf("查询不应改动第 %d 条记录的状态", index)
		}
		if !after[index].UpdatedAt.Equal(before[index].UpdatedAt) {
			t.Fatalf("查询不应更新第 %d 条记录的时间戳", index)
		}
		if after[index].ID != before[index].ID {
			t.Fatalf("查询不应改变记录顺序: 第 %d 条 %s / %s", index, before[index].ID, after[index].ID)
		}
	}
	taskAfter, err := fixture.store.GetTask(ctx, fixture.first.ID)
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

// TestCodeSaveListWithoutServiceOverRealServer 校验未注入服务时真实服务返回 503。
func TestCodeSaveListWithoutServiceOverRealServer(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)
	status, _, body := get(t, baseURL, codeSaveServerPath(domain.NewID().String()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("依赖缺失应返回 503，实际 %d，响应: %s", status, body)
	}
}
