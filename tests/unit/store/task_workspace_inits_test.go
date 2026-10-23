// 任务级工作区初始化记录的 SQLite 仓储测试。
//
// 这些用例直接验证仓储写入、状态字段、查询与数据库约束，不需要 Git、SSH 或 Docker。
package sqlite_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 测试使用的固定 commit SHA。
const (
	workspaceBaseCommit = "1111111111111111111111111111111111111111"
	workspaceHeadCommit = "2222222222222222222222222222222222222222"
)

// newWorkspaceInitRecord 构造一条待执行的任务级工作区初始化记录。
func newWorkspaceInitRecord(t *testing.T, taskID, containerID string) domain.TaskWorkspaceInitRecord {
	t.Helper()
	record, err := domain.NewTaskWorkspaceInitRecord(
		"",
		taskID,
		containerID,
		"docker-"+containerID,
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造工作区初始化记录失败: %v", err)
	}
	return record
}

// TestTaskWorkspaceInitLifecycle 校验记录的写入、推进与读取。
func TestTaskWorkspaceInitLifecycle(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newWorkspaceInitRecord(t, "task-a", "container-a")
	if record.Status != domain.TaskWorkspaceInitStatusPending {
		t.Fatalf("新记录状态应为 pending，实际 %s", record.Status)
	}
	if err := db.CreateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("写入工作区初始化记录失败: %v", err)
	}
	if err := record.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行状态失败: %v", err)
	}
	if err := db.UpdateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("更新工作区初始化记录失败: %v", err)
	}
	if err := record.MarkSucceeded(
		"main",
		domain.GitBranchSelectionOriginHead,
		workspaceBaseCommit,
		domain.TaskBranchSourceNew.String(),
		workspaceHeadCommit,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记成功状态失败: %v", err)
	}
	if err := db.UpdateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("更新工作区初始化记录失败: %v", err)
	}
	stored, err := db.GetTaskWorkspaceInit(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取工作区初始化记录失败: %v", err)
	}
	if stored.Status != domain.TaskWorkspaceInitStatusSucceeded {
		t.Fatalf("状态不符: %s", stored.Status)
	}
	if stored.CommitSHA != workspaceHeadCommit {
		t.Fatalf("commit 不符: %s", stored.CommitSHA)
	}
	if !stored.Succeeded() {
		t.Fatal("succeeded 必须表示工作区已就绪")
	}
	if stored.FinishedAt == nil {
		t.Fatal("终态记录必须有结束时间")
	}
	if stored.ContainerID != "container-a" {
		t.Fatalf("容器代次判据不符: %s", stored.ContainerID)
	}
}

// TestTaskWorkspaceInitListedPerTask 校验按任务查询返回最新在前的记录。
func TestTaskWorkspaceInitListedPerTask(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	first := newWorkspaceInitRecord(t, "task-a", "container-a")
	if err := db.CreateTaskWorkspaceInit(ctx, first); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	// 同一任务换容器：两条记录都必须保留，容器是幂等边界。
	second := newWorkspaceInitRecord(t, "task-a", "container-b")
	if err := db.CreateTaskWorkspaceInit(ctx, second); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	records, err := db.ListTaskWorkspaceInitsByTask(ctx, "task-a")
	if err != nil {
		t.Fatalf("查询记录失败: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("记录数应为 2，实际 %d", len(records))
	}
	byContainer, err := db.ListTaskWorkspaceInitsByContainer(ctx, "container-b")
	if err != nil {
		t.Fatalf("按容器查询失败: %v", err)
	}
	if len(byContainer) != 1 || byContainer[0].ID != second.ID {
		t.Fatalf("按容器查询结果不符: %+v", byContainer)
	}
}

// TestTaskWorkspaceInitActiveUnique 校验同任务同容器只允许一条未结束记录。
func TestTaskWorkspaceInitActiveUnique(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	first := newWorkspaceInitRecord(t, "task-a", "container-a")
	if err := first.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := db.CreateTaskWorkspaceInit(ctx, first); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	second := newWorkspaceInitRecord(t, "task-a", "container-a")
	err := db.CreateTaskWorkspaceInit(ctx, second)
	if err == nil {
		t.Fatal("同任务同容器的第二条未结束记录必须被拒绝")
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("应返回状态冲突，实际 %v", err)
	}
	// 终态记录不参与唯一性：同一容器可以有历史终态记录。
	finished := newWorkspaceInitRecord(t, "task-a", "container-a")
	if markErr := finished.MarkRunning(domain.CodeFetchStageProbe, time.Now().UTC()); markErr != nil {
		t.Fatalf("标记运行失败: %v", markErr)
	}
	if markErr := finished.MarkSkipped(workspaceHeadCommit, time.Now().UTC()); markErr != nil {
		t.Fatalf("标记跳过失败: %v", markErr)
	}
	if createErr := db.CreateTaskWorkspaceInit(ctx, finished); createErr != nil {
		t.Fatalf("终态记录应可写入，实际 %v", createErr)
	}
}

// TestTaskWorkspaceInitListInterrupted 校验中断记录查询只返回未结束记录。
func TestTaskWorkspaceInitListInterrupted(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	running := newWorkspaceInitRecord(t, "task-a", "container-a")
	if err := running.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := db.CreateTaskWorkspaceInit(ctx, running); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	pending := newWorkspaceInitRecord(t, "task-b", "container-b")
	if err := db.CreateTaskWorkspaceInit(ctx, pending); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	finished := newWorkspaceInitRecord(t, "task-c", "container-c")
	if err := finished.MarkFailed(
		domain.TaskWorkspaceInitStatusFailed,
		domain.CodeFetchStageClone,
		"code",
		"失败原因",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记失败状态失败: %v", err)
	}
	if err := db.CreateTaskWorkspaceInit(ctx, finished); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	records, err := db.ListInterruptedTaskWorkspaceInits(ctx)
	if err != nil {
		t.Fatalf("查询中断记录失败: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("中断记录数应为 2，实际 %d", len(records))
	}
	for _, record := range records {
		switch record.Status {
		case domain.TaskWorkspaceInitStatusPending, domain.TaskWorkspaceInitStatusRunning:
		default:
			t.Fatalf("查询结果不应包含终态记录: %s", record.Status)
		}
	}
}

// TestTaskWorkspaceInitUnknownIDReturnsNotFound 校验读取不存在的记录。
func TestTaskWorkspaceInitUnknownIDReturnsNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	_, err := db.GetTaskWorkspaceInit(ctx, domain.NewID())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应返回记录不存在，实际 %v", err)
	}
}

// TestTaskWorkspaceInitUpdateMissingReturnsNotFound 校验更新不存在的记录。
func TestTaskWorkspaceInitUpdateMissingReturnsNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newWorkspaceInitRecord(t, "task-a", "container-a")
	err := db.UpdateTaskWorkspaceInit(ctx, record)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应返回记录不存在，实际 %v", err)
	}
}

// TestTaskWorkspaceInitAcceptsUnknownEnumValues 校验未知枚举值可写入并原样读回。
func TestTaskWorkspaceInitAcceptsUnknownEnumValues(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newWorkspaceInitRecord(t, "task-a", "container-a")
	if err := db.CreateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	raw := rawDB(t, db)
	if _, err := raw.ExecContext(
		ctx,
		`UPDATE task_workspace_initializations SET status = ?, stage = ? WHERE id = ?`,
		"awaiting_review",
		"mirror_sync",
		record.ID.String(),
	); err != nil {
		t.Fatalf("写入未知枚举值失败: %v", err)
	}
	stored, err := db.GetTaskWorkspaceInit(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if string(stored.Status) != "awaiting_review" {
		t.Fatalf("未知状态未原样读回: %s", stored.Status)
	}
	if string(stored.Stage) != "mirror_sync" {
		t.Fatalf("未知阶段未原样读回: %s", stored.Stage)
	}
}

// TestTaskWorkspaceInitRequiresCommitForSuccess 校验成功记录必须带 commit。
func TestTaskWorkspaceInitRequiresCommitForSuccess(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newWorkspaceInitRecord(t, "task-a", "container-a")
	if err := db.CreateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	raw := rawDB(t, db)
	_, err := raw.ExecContext(
		ctx,
		`UPDATE task_workspace_initializations SET status = 'succeeded' WHERE id = ?`,
		record.ID.String(),
	)
	if err == nil {
		t.Fatal("没有 commit 的成功状态必须被数据库拒绝")
	}
}

// TestTaskWorkspaceInitSurvivesTaskDeletion 校验任务逻辑删除后记录仍保留。
func TestTaskWorkspaceInitSurvivesTaskDeletion(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newWorkspaceInitRecord(t, "task-a", "container-a")
	if err := db.CreateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	raw := rawDB(t, db)
	if _, err := raw.ExecContext(
		ctx,
		`DELETE FROM task_workspace_initializations WHERE task_id = 'orphan-task'`,
	); err != nil {
		t.Fatalf("删除孤立记录失败: %v", err)
	}
	records, err := db.ListTaskWorkspaceInitsByTask(ctx, "task-a")
	if err != nil {
		t.Fatalf("查询记录失败: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("记录应保留，实际 %d 条", len(records))
	}
}
