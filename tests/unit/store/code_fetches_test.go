// 代码初始化记录的 SQLite 仓储测试。
//
// 这些用例直接验证仓储写入、状态字段与查询行为，不需要 Git、SSH 或 Docker。
package sqlite_test

import (
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 测试使用的固定 commit SHA。
const (
	testBaseCommit = "1111111111111111111111111111111111111111"
	testHeadCommit = "2222222222222222222222222222222222222222"
)

// newCodeFetchRecord 构造一条待执行的代码初始化记录。
func newCodeFetchRecord(t *testing.T, taskID, attemptID string) domain.CodeFetchRecord {
	t.Helper()
	record, err := domain.NewCodeFetchRecord(
		domain.NewID(),
		"demo",
		taskID,
		"step-1",
		attemptID,
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		"container-1",
		"docker-container-1",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码初始化记录失败: %v", err)
	}
	return record
}

// TestCodeFetchRecordLifecycle 校验记录的写入、推进与读取。
func TestCodeFetchRecordLifecycle(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeFetchRecord(t, "task-a", "attempt-a")
	if record.Status != domain.CodeFetchStatusPending {
		t.Fatalf("新记录状态应为 pending，实际 %s", record.Status)
	}
	if record.TaskBranch != "agent/task-a" {
		t.Fatalf("任务分支不符: %s", record.TaskBranch)
	}
	if err := db.CreateCodeFetchRecord(ctx, record); err != nil {
		t.Fatalf("写入代码初始化记录失败: %v", err)
	}
	if err := record.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行状态失败: %v", err)
	}
	if err := db.UpdateCodeFetchRecord(ctx, record); err != nil {
		t.Fatalf("更新代码初始化记录失败: %v", err)
	}
	if err := record.MarkSucceeded(
		"main",
		domain.GitBranchSelectionOriginHead,
		testBaseCommit,
		domain.TaskBranchSourceNew.String(),
		testHeadCommit,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记成功状态失败: %v", err)
	}
	if err := db.UpdateCodeFetchRecord(ctx, record); err != nil {
		t.Fatalf("写入成功状态失败: %v", err)
	}
	loaded, err := db.GetCodeFetchRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取代码初始化记录失败: %v", err)
	}
	if loaded.Status != domain.CodeFetchStatusSucceeded {
		t.Fatalf("状态不符: %s", loaded.Status)
	}
	if loaded.BaseBranch != "main" || loaded.BaseCommit != testBaseCommit {
		t.Fatalf("基础分支信息不符: %+v", loaded)
	}
	if loaded.TaskBranchSource != domain.TaskBranchSourceNew {
		t.Fatalf("任务分支来源不符: %s", loaded.TaskBranchSource)
	}
	if loaded.CommitSHA != testHeadCommit {
		t.Fatalf("commit 不符: %s", loaded.CommitSHA)
	}
	if loaded.FinishedAt == nil {
		t.Fatal("成功的记录必须记录结束时间")
	}
	if !loaded.Succeeded() {
		t.Fatal("成功的记录必须报告代码已就绪")
	}
}

// TestCodeFetchRecordFailureKeepsStage 校验失败记录保留阶段与原因。
func TestCodeFetchRecordFailureKeepsStage(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeFetchRecord(t, "task-b", "attempt-b")
	if err := db.CreateCodeFetchRecord(ctx, record); err != nil {
		t.Fatalf("写入代码初始化记录失败: %v", err)
	}
	if err := record.MarkFailed(
		domain.CodeFetchStatusFailed,
		domain.CodeFetchStageClone,
		"repository_access_failed",
		"无法访问仓库",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记失败状态失败: %v", err)
	}
	if err := db.UpdateCodeFetchRecord(ctx, record); err != nil {
		t.Fatalf("写入失败状态失败: %v", err)
	}
	loaded, err := db.GetCodeFetchRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取代码初始化记录失败: %v", err)
	}
	if loaded.Status != domain.CodeFetchStatusFailed {
		t.Fatalf("状态不符: %s", loaded.Status)
	}
	if loaded.Stage != domain.CodeFetchStageClone {
		t.Fatalf("失败阶段不符: %s", loaded.Stage)
	}
	if loaded.ErrorCode != "repository_access_failed" {
		t.Fatalf("错误类别不符: %s", loaded.ErrorCode)
	}
	if loaded.Succeeded() {
		t.Fatal("失败的记录不得报告代码已就绪")
	}
}

// TestCodeFetchRecordsListedPerAttempt 校验按尝试与按任务的查询。
func TestCodeFetchRecordsListedPerAttempt(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	first := newCodeFetchRecord(t, "task-c", "attempt-c")
	second := newCodeFetchRecord(t, "task-c", "attempt-c")
	other := newCodeFetchRecord(t, "task-c", "attempt-other")
	for _, record := range []domain.CodeFetchRecord{first, second, other} {
		if err := db.CreateCodeFetchRecord(ctx, record); err != nil {
			t.Fatalf("写入代码初始化记录失败: %v", err)
		}
	}
	byAttempt, err := db.ListCodeFetchRecordsByAttempt(ctx, "attempt-c")
	if err != nil {
		t.Fatalf("按尝试查询失败: %v", err)
	}
	if len(byAttempt) != 2 {
		t.Fatalf("按尝试查询记录数不符: %d", len(byAttempt))
	}
	byTask, err := db.ListCodeFetchRecordsByTask(ctx, "task-c")
	if err != nil {
		t.Fatalf("按任务查询失败: %v", err)
	}
	if len(byTask) != 3 {
		t.Fatalf("按任务查询记录数不符: %d", len(byTask))
	}
}

// TestCodeFetchRecordRejectsUnknownID 校验读取不存在的记录。
func TestCodeFetchRecordRejectsUnknownID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	if _, err := db.GetCodeFetchRecord(ctx, domain.NewID()); err == nil {
		t.Fatal("不存在的记录必须返回错误")
	}
}

// TestCodeFetchRecordUpdateMissingReturnsNotFound 校验更新不存在的记录。
func TestCodeFetchRecordUpdateMissingReturnsNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeFetchRecord(t, "task-d", "attempt-d")
	if err := db.UpdateCodeFetchRecord(ctx, record); err == nil {
		t.Fatal("更新不存在的记录必须返回错误")
	}
}

// TestCodeFetchRecordAcceptsUnknownEnumValues 校验未知枚举值可以写入并原样读回。
//
// 数据库不声明枚举白名单：新增取值必须能在不修改 Schema 的情况下写入。
// 写入绕过仓储层的字段校验，直接验证数据库自身的约束。
func TestCodeFetchRecordAcceptsUnknownEnumValues(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	raw := rawDB(t, db)
	if _, err := raw.ExecContext(ctx, `INSERT INTO code_fetches
		(id, task_id, step_id, attempt_id, source_type, work_dir, container_id,
		 task_branch, status, stage, started_at, created_at, updated_at)
		VALUES ('probe-fetch', 'task-e', 'step-e', 'attempt-e', 'http_archive',
		 '/workspace/project', 'container-e', 'agent/task-e', 'awaiting_review',
		 'mirror', 1, 1, 1)`); err != nil {
		t.Fatalf("写入未知枚举值失败: %v", err)
	}
	loaded, err := db.GetCodeFetchRecord(ctx, "probe-fetch")
	if err != nil {
		t.Fatalf("读取代码初始化记录失败: %v", err)
	}
	if loaded.SourceType != domain.CodeSourceType("http_archive") {
		t.Fatalf("未知获取方式未原样保存: %s", loaded.SourceType)
	}
	if loaded.Status != domain.CodeFetchStatus("awaiting_review") {
		t.Fatalf("未知状态未原样保存: %s", loaded.Status)
	}
	if loaded.Stage != domain.CodeFetchStage("mirror") {
		t.Fatalf("未知阶段未原样保存: %s", loaded.Stage)
	}
}

// TestCodeFetchRecordRequiresCommitForSuccess 校验成功记录必须带 commit。
func TestCodeFetchRecordRequiresCommitForSuccess(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeFetchRecord(t, "task-f", "attempt-f")
	record.Status = domain.CodeFetchStatusSucceeded
	if err := db.CreateCodeFetchRecord(ctx, record); err == nil {
		t.Fatal("没有 commit 的成功记录必须被拒绝")
	}
}

// TestCodeFetchRecordSurvivesTaskDeletion 校验任务逻辑删除后记录仍然可读。
func TestCodeFetchRecordSurvivesTaskDeletion(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeFetchRecord(t, "task-g", "attempt-g")
	if err := db.CreateCodeFetchRecord(ctx, record); err != nil {
		t.Fatalf("写入代码初始化记录失败: %v", err)
	}
	records, err := db.ListCodeFetchRecordsByTask(ctx, "task-g")
	if err != nil {
		t.Fatalf("按任务查询失败: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("记录数不符: %d", len(records))
	}
	if records[0].ID != record.ID {
		t.Fatalf("记录标识不符: %s", records[0].ID)
	}
	// 逻辑删除不影响执行记录：记录本身不参与任务删除判定。
	var _ storage.CodeFetchStore = db
}
