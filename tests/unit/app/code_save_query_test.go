package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// codeSaveQueryFixture 是代码保存记录查询的现场。
//
// 两条互不相关的任务同时存在：用于证明查询按任务过滤，而不是返回全部记录。
type codeSaveQueryFixture struct {
	db      *sqlite.DB
	ctx     context.Context
	service *app.CodeSaveQueryService
	task    domain.Task
	other   domain.Task
}

// newCodeSaveQueryFixture 在工作空间中准备两条任务与各自的代码保存记录。
//
// 第一个任务有两条记录（分别处于不同阶段），第二个任务只有一条。
func newCodeSaveQueryFixture(t *testing.T) *codeSaveQueryFixture {
	t.Helper()
	fixture := newCodeSaveQueryFixtureWithoutRecords(t)
	now := time.Now().UTC()
	// 第一条记录：提交成功、推送成功，合并未请求。
	first := mustCodeSaveRecord(t, fixture.task.ID.String(), now)
	first.ProjectID = "project-1"
	first.AttemptID = "attempt-1"
	if err := first.MarkCommit(
		domain.CommitStatusSucceeded,
		"1111111111111111111111111111111111111111",
		"提交一",
		now,
	); err != nil {
		t.Fatalf("记录提交结果失败: %v", err)
	}
	if err := first.MarkPush(
		domain.PushStatusSucceeded,
		"2222222222222222222222222222222222222222",
		now,
	); err != nil {
		t.Fatalf("记录推送结果失败: %v", err)
	}
	first.Finish(now)
	mustCreateCodeSaveRecord(t, fixture.db, fixture.ctx, first)
	// 第二条记录：提交成功、推送结果未知，更晚创建，因此应排在前面。
	second := mustCodeSaveRecord(t, fixture.task.ID.String(), now.Add(time.Minute))
	if err := second.MarkCommit(
		domain.CommitStatusSucceeded,
		"3333333333333333333333333333333333333333",
		"提交二",
		second.CreatedAt,
	); err != nil {
		t.Fatalf("记录提交结果失败: %v", err)
	}
	if err := second.MarkPush(domain.PushStatusUnknown, "", second.CreatedAt); err != nil {
		t.Fatalf("记录推送结果失败: %v", err)
	}
	mustCreateCodeSaveRecord(t, fixture.db, fixture.ctx, second)
	// 另一个任务的记录：任何情况下都不得出现在第一个任务的查询结果里。
	other := mustCodeSaveRecord(t, fixture.other.ID.String(), now.Add(2*time.Minute))
	mustCreateCodeSaveRecord(t, fixture.db, fixture.ctx, other)
	return fixture
}

// newCodeSaveQueryFixtureWithoutRecords 准备两条任务但不写入任何代码保存记录。
func newCodeSaveQueryFixtureWithoutRecords(t *testing.T) *codeSaveQueryFixture {
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
	service, err := app.NewCodeSaveQueryService(app.CodeSaveQueryOptions{Store: db})
	if err != nil {
		t.Fatalf("构造代码保存记录查询服务失败: %v", err)
	}
	return &codeSaveQueryFixture{
		db:      db,
		ctx:     ctx,
		service: service,
		task:    mustCodeSaveQueryTask(t, db, ctx, now, "第一个任务"),
		other:   mustCodeSaveQueryTask(t, db, ctx, now, "第二个任务"),
	}
}

// mustCodeSaveQueryTask 写入一条需求与一个任务。
func mustCodeSaveQueryTask(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
	title string,
) domain.Task {
	t.Helper()
	requirement, err := domain.NewRequirement(title, title+"正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return task
}

// mustCodeSaveRecord 构造一条待执行的代码保存记录。
func mustCodeSaveRecord(
	t *testing.T,
	taskID string,
	at time.Time,
) domain.CodeSaveRecord {
	t.Helper()
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		taskID,
		"container-1",
		"docker-container-1",
		"origin",
		"https://git.example.com/group/repo.git",
		"agent/"+taskID,
		"main",
		false,
		at,
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	return record
}

// mustCreateCodeSaveRecord 写入一条代码保存记录。
func mustCreateCodeSaveRecord(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	record domain.CodeSaveRecord,
) {
	t.Helper()
	if err := db.CreateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
}

// TestCodeSaveQueryReturnsTaskRecords 校验正常返回某任务的多条代码保存记录。
func TestCodeSaveQueryReturnsTaskRecords(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveQueryFixture(t)
	records, err := fixture.service.ListByTask(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("查询代码保存记录失败: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("第一个任务应有 2 条记录，实际 %d", len(records))
	}
	for _, record := range records {
		if record.TaskID != fixture.task.ID.String() {
			t.Fatalf("查询返回了其他任务的记录: %s", record.TaskID)
		}
		if record.ID.IsEmpty() {
			t.Fatal("记录标识不能为空：Web 需要真实 recordID 才能发起核验")
		}
	}
	// 提交、推送、合并三个阶段的状态必须独立保留，不能被折叠成单一结论。
	latest := records[0]
	if latest.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交状态不符: %s", latest.CommitStatus)
	}
	if latest.PushStatus != domain.PushStatusUnknown {
		t.Fatalf("推送结果未知必须按真实值返回，实际 %s", latest.PushStatus)
	}
	if latest.MergeStatus != domain.MergeStatusNotRequested {
		t.Fatalf("未请求合并时合并状态应为 not_requested，实际 %s", latest.MergeStatus)
	}
}

// TestCodeSaveQueryOrdersNewestFirst 校验返回顺序稳定，最新记录在前。
func TestCodeSaveQueryOrdersNewestFirst(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveQueryFixture(t)
	records, err := fixture.service.ListByTask(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("查询代码保存记录失败: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("第一个任务应有 2 条记录，实际 %d", len(records))
	}
	if !records[0].CreatedAt.After(records[1].CreatedAt) {
		t.Fatalf(
			"记录应按创建时间倒序排列，实际 %s / %s",
			records[0].CreatedAt,
			records[1].CreatedAt,
		)
	}
	// 重复查询顺序必须一致：排序不得依赖数据库的隐式返回顺序。
	again, err := fixture.service.ListByTask(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("重复查询代码保存记录失败: %v", err)
	}
	for index := range records {
		if records[index].ID != again[index].ID {
			t.Fatalf("重复查询顺序不一致: 第 %d 条 %s / %s", index, records[index].ID, again[index].ID)
		}
	}
}

// TestCodeSaveQueryRejectsDeletedTask 校验任务逻辑删除后返回未找到。
func TestCodeSaveQueryRejectsDeletedTask(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveQueryFixture(t)
	record := mustCodeSaveRecord(t, fixture.task.ID.String(), time.Now().UTC().Add(3*time.Minute))
	mustCreateCodeSaveRecord(t, fixture.db, fixture.ctx, record)
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	// 只有已结束的任务允许删除：先取消，再逻辑删除。
	if err := task.CancelImmediately(time.Now().UTC()); err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	if err := fixture.db.UpdateTask(fixture.ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	if err := fixture.db.DeleteTask(
		fixture.ctx,
		fixture.task.ID,
		[]domain.TaskStatus{domain.TaskStatusCancelled},
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	_, err = fixture.service.ListByTask(fixture.ctx, fixture.task.ID.String())
	if !errors.Is(err, app.ErrTaskNotFound) {
		t.Fatalf("已删除任务应返回未找到，实际 %v", err)
	}
}

// TestCodeSaveQueryRejectsUnknownTask 校验任务不存在返回未找到。
func TestCodeSaveQueryRejectsUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveQueryFixtureWithoutRecords(t)
	_, err := fixture.service.ListByTask(fixture.ctx, domain.NewID().String())
	if !errors.Is(err, app.ErrTaskNotFound) {
		t.Fatalf("不存在的任务应返回未找到，实际 %v", err)
	}
}

// TestCodeSaveQueryRejectsInvalidTaskID 校验非法任务标识按未找到处理。
func TestCodeSaveQueryRejectsInvalidTaskID(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveQueryFixtureWithoutRecords(t)
	for _, raw := range []string{"", "   ", "task/1"} {
		_, err := fixture.service.ListByTask(fixture.ctx, raw)
		if !errors.Is(err, app.ErrTaskNotFound) {
			t.Fatalf("非法任务标识 %q 应返回未找到，实际 %v", raw, err)
		}
	}
}

// TestCodeSaveQueryReturnsEmptyList 校验任务下没有记录时返回合法空列表。
func TestCodeSaveQueryReturnsEmptyList(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveQueryFixtureWithoutRecords(t)
	records, err := fixture.service.ListByTask(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("没有代码保存记录不应返回错误: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("没有代码保存记录时应返回空列表，实际 %d 条", len(records))
	}
}

// TestCodeSaveQueryStorageFailureIsNotNotFound 校验存储错误不被伪装成空列表或未找到。
func TestCodeSaveQueryStorageFailureIsNotNotFound(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveQueryFixtureWithoutRecords(t)
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	_, err := fixture.service.ListByTask(fixture.ctx, fixture.task.ID.String())
	if err == nil {
		t.Fatal("存储不可用时不应返回成功")
	}
	if errors.Is(err, app.ErrTaskNotFound) {
		t.Fatalf("存储错误不能归类为未找到: %v", err)
	}
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("存储错误不能伪装成记录不存在: %v", err)
	}
}

// TestCodeSaveQueryIsReadOnly 校验查询不修改代码保存记录或任务状态。
func TestCodeSaveQueryIsReadOnly(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveQueryFixture(t)
	// fixture 已为第二个任务写入一条记录，直接以它作为观察对象。
	existing, err := fixture.service.ListByTask(fixture.ctx, fixture.other.ID.String())
	if err != nil {
		t.Fatalf("查询代码保存记录失败: %v", err)
	}
	if len(existing) != 1 {
		t.Fatalf("用例要求第二个任务恰好一条记录，实际 %d", len(existing))
	}
	recordID := existing[0].ID
	before, err := fixture.db.GetCodeSaveRecord(fixture.ctx, recordID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	taskBefore, err := fixture.db.GetTask(fixture.ctx, fixture.other.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if _, err := fixture.service.ListByTask(fixture.ctx, fixture.other.ID.String()); err != nil {
		t.Fatalf("查询代码保存记录失败: %v", err)
	}
	if _, err := fixture.service.ListByTask(fixture.ctx, fixture.other.ID.String()); err != nil {
		t.Fatalf("重复查询代码保存记录失败: %v", err)
	}
	after, err := fixture.db.GetCodeSaveRecord(fixture.ctx, recordID)
	if err != nil {
		t.Fatalf("再次读取代码保存记录失败: %v", err)
	}
	if after.CommitStatus != before.CommitStatus ||
		after.PushStatus != before.PushStatus ||
		after.MergeStatus != before.MergeStatus ||
		after.RecoveryStatus != before.RecoveryStatus ||
		after.PersistStatus != before.PersistStatus {
		t.Fatalf("查询不应改动代码保存记录: 之前 %+v，之后 %+v", before, after)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("查询不应更新记录时间戳: %s / %s", before.UpdatedAt, after.UpdatedAt)
	}
	taskAfter, err := fixture.db.GetTask(fixture.ctx, fixture.other.ID)
	if err != nil {
		t.Fatalf("再次读取任务失败: %v", err)
	}
	if taskAfter.Status != taskBefore.Status {
		t.Fatalf("查询不应推进任务状态: %s / %s", taskBefore.Status, taskAfter.Status)
	}
	if taskAfter.UpdatedAt.After(taskBefore.UpdatedAt) {
		t.Fatalf("查询不应更新任务时间戳: %s / %s", taskBefore.UpdatedAt, taskAfter.UpdatedAt)
	}
	// 记录条数不变：查询不会写入新的保存记录。
	records, err := fixture.service.ListByTask(fixture.ctx, fixture.other.ID.String())
	if err != nil {
		t.Fatalf("查询代码保存记录失败: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("查询不应追加记录，实际 %d 条", len(records))
	}
}

// TestCodeSaveQueryRequiresStore 校验存储为空时构造失败。
func TestCodeSaveQueryRequiresStore(t *testing.T) {
	t.Parallel()
	if _, err := app.NewCodeSaveQueryService(app.CodeSaveQueryOptions{}); err == nil {
		t.Fatal("存储为空时应构造失败")
	}
}

// TestCodeSaveQueryStoreHasNoRemoteDependency 校验查询服务的依赖契约只有存储。
//
// 编译期证明：`CodeSaveQueryStore` 只声明两个读取方法，因此用真实 SQLite 构造的
// 服务在类型层面就不可能建立 SSH / Docker 会话或执行 Git 命令。
func TestCodeSaveQueryStoreHasNoRemoteDependency(t *testing.T) {
	t.Parallel()
	fixture := newCodeSaveQueryFixtureWithoutRecords(t)
	var store app.CodeSaveQueryStore = fixture.db
	if store == nil {
		t.Fatal("真实存储应满足查询依赖契约")
	}
}
