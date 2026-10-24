// 恢复相关条件更新与查询的 SQLite 仓储测试。
//
// 这些用例固定的是一个关键事实：恢复路径必须使用"带状态前置条件"的写入，
// 影响行数为 0 时按冲突返回，而不是覆盖其他执行者已经写好的结论。
package sqlite_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// TestUpdateCodeFetchRecordIfStatusRejectsStaleStatus 验证状态前置条件生效。
func TestUpdateCodeFetchRecordIfStatusRejectsStaleStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeFetchRecord(t, "task-1", "attempt-1")
	if err := db.CreateCodeFetchRecord(ctx, record); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	if err := record.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := db.UpdateCodeFetchRecord(ctx, record); err != nil {
		t.Fatalf("更新记录失败: %v", err)
	}
	// 数据库中的状态已是 running：以 pending 为前置条件必须被拒绝。
	stale := record
	stale.ErrorSummary = "过期写入"
	if err := db.UpdateCodeFetchRecordIfStatus(
		ctx,
		stale,
		[]domain.CodeFetchStatus{domain.CodeFetchStatusPending},
	); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("过期状态写入必须返回冲突，实际 %v", err)
	}
	stored, err := db.GetCodeFetchRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if stored.ErrorSummary == "过期写入" {
		t.Fatal("冲突写入不得改数据库记录")
	}
}

// TestUpdateCodeFetchRecordIfStatusAppliesMatchingStatus 验证匹配状态时写入成功。
func TestUpdateCodeFetchRecordIfStatusAppliesMatchingStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeFetchRecord(t, "task-1", "attempt-1")
	if err := db.CreateCodeFetchRecord(ctx, record); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	updated := record
	if err := updated.MarkFailed(
		domain.CodeFetchStatusUncertain,
		domain.CodeFetchStagePersist,
		"interrupted",
		domain.CodeFetchInterruptedSummary(),
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记不确定失败: %v", err)
	}
	if err := db.UpdateCodeFetchRecordIfStatus(
		ctx,
		updated,
		[]domain.CodeFetchStatus{domain.CodeFetchStatusPending},
	); err != nil {
		t.Fatalf("条件更新失败: %v", err)
	}
	stored, err := db.GetCodeFetchRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if stored.Status != domain.CodeFetchStatusUncertain {
		t.Fatalf("状态应为 uncertain，实际 %s", stored.Status)
	}
}

// TestListInterruptedCodeFetchRecordsOnlyActive 验证中断查询只返回活动记录。
func TestListInterruptedCodeFetchRecordsOnlyActive(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	pending := newCodeFetchRecord(t, "task-1", "attempt-1")
	running := newCodeFetchRecord(t, "task-1", "attempt-2")
	succeeded := newCodeFetchRecord(t, "task-1", "attempt-3")
	for _, record := range []domain.CodeFetchRecord{pending, running, succeeded} {
		if err := db.CreateCodeFetchRecord(ctx, record); err != nil {
			t.Fatalf("写入记录失败: %v", err)
		}
	}
	if err := running.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := db.UpdateCodeFetchRecord(ctx, running); err != nil {
		t.Fatalf("更新记录失败: %v", err)
	}
	if err := succeeded.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := succeeded.MarkSucceeded(
		"main",
		domain.GitBranchSelectionExplicit,
		testBaseCommit,
		domain.TaskBranchSourceNew.String(),
		testHeadCommit,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记成功失败: %v", err)
	}
	if err := db.UpdateCodeFetchRecord(ctx, succeeded); err != nil {
		t.Fatalf("更新记录失败: %v", err)
	}
	records, err := db.ListInterruptedCodeFetchRecords(ctx)
	if err != nil {
		t.Fatalf("查询中断记录失败: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("应只返回 2 条活动记录，实际 %d 条", len(records))
	}
	for _, record := range records {
		if record.Status.IsTerminal() {
			t.Fatalf("终态记录不应出现在中断查询中: %s", record.Status)
		}
	}
}

// TestCodeFetchActiveUniqueIndexRejectsSecondActiveRecord 验证活动唯一约束生效。
func TestCodeFetchActiveUniqueIndexRejectsSecondActiveRecord(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	first := newCodeFetchRecord(t, "task-1", "attempt-1")
	if err := db.CreateCodeFetchRecord(ctx, first); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	second := newCodeFetchRecord(t, "task-1", "attempt-1")
	if err := db.CreateCodeFetchRecord(ctx, second); err == nil {
		t.Fatal("同一尝试的第二条活动记录必须被唯一约束拒绝")
	}
	// 前一条进入终态后，同一尝试可以再写入新记录。
	if err := first.MarkFailed(
		domain.CodeFetchStatusFailed,
		domain.CodeFetchStageClone,
		"clone_failed",
		"仓库不可访问",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	if err := db.UpdateCodeFetchRecord(ctx, first); err != nil {
		t.Fatalf("更新记录失败: %v", err)
	}
	if err := db.CreateCodeFetchRecord(ctx, second); err != nil {
		t.Fatalf("终态记录的尝试应允许写入新记录: %v", err)
	}
}

// TestUpdateTaskWorkspaceInitIfStatusRejectsStaleStatus 验证工作区记录的条件更新。
func TestUpdateTaskWorkspaceInitIfStatusRejectsStaleStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record, err := domain.NewTaskWorkspaceInitRecord(
		domain.NewID(),
		"task-1",
		"container-1",
		"docker-container-1",
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造记录失败: %v", err)
	}
	if err := db.CreateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	if err := record.MarkRunning(domain.CodeFetchStageClone, time.Now().UTC()); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := db.UpdateTaskWorkspaceInit(ctx, record); err != nil {
		t.Fatalf("更新记录失败: %v", err)
	}
	stale := record
	stale.ErrorSummary = "过期写入"
	if err := db.UpdateTaskWorkspaceInitIfStatus(
		ctx,
		stale,
		[]domain.TaskWorkspaceInitStatus{domain.TaskWorkspaceInitStatusPending},
	); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("过期状态写入必须返回冲突，实际 %v", err)
	}
	stored, err := db.GetTaskWorkspaceInit(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if stored.ErrorSummary == "过期写入" {
		t.Fatal("冲突写入不得改数据库记录")
	}
}

// TestUpdateContainerIfStateRejectsStaleState 验证容器记录的条件更新。
func TestUpdateContainerIfStateRejectsStaleState(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.40", false)
	container := mustContainer(t, db, host.ID, "agentflow-recovery1")
	if err := container.MarkUnknown("远程状态无法确认", time.Now().UTC()); err != nil {
		t.Fatalf("标记状态不明失败: %v", err)
	}
	if err := db.UpdateContainer(ctx, container); err != nil {
		t.Fatalf("更新容器记录失败: %v", err)
	}
	// 数据库状态已是 unknown：以 creating 为前置条件必须被拒绝。
	stale := container
	stale.ErrorSummary = "过期写入"
	if err := db.UpdateContainerIfState(
		ctx,
		stale,
		[]domain.ContainerState{domain.ContainerStateCreating},
	); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("过期状态写入必须返回冲突，实际 %v", err)
	}
	stored, err := db.GetContainer(ctx, container.ID)
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if stored.ErrorSummary == "过期写入" {
		t.Fatal("冲突写入不得改数据库记录")
	}
}

// TestContainerFilterByTaskAndOrphans 验证按任务与孤儿筛选。
func TestContainerFilterByTaskAndOrphans(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	host := mustExecutionHost(t, db, "节点", "10.0.0.41", false)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	// 容器记录的任务关联只在创建时写入，因此必须带着关联构造。
	current := mustTaskContainer(t, db, host.ID, "agentflow-current1", task.ID)
	// 任务当前引用的容器才是"在用的那一代"。
	task.ContainerID = current.ID.String()
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	orphan := mustTaskContainer(t, db, host.ID, "agentflow-orphan1", task.ID)

	byTask, err := db.ListContainers(ctx, domain.ContainerFilter{TaskID: &task.ID})
	if err != nil {
		t.Fatalf("按任务查询失败: %v", err)
	}
	if len(byTask) != 2 {
		t.Fatalf("按任务查询应返回 2 条，实际 %d 条", len(byTask))
	}
	orphans, err := db.ListContainers(ctx, domain.ContainerFilter{Orphans: true})
	if err != nil {
		t.Fatalf("查询孤儿容器失败: %v", err)
	}
	if len(orphans) != 1 || orphans[0].ID != orphan.ID {
		t.Fatalf("孤儿容器查询不正确: %+v", orphans)
	}
}

// mustTaskContainer 构造并写入一个带任务关联的容器记录。
//
// 任务关联只在创建时写入（更新语句不改这一列），因此需要构造专用记录。
func mustTaskContainer(
	t *testing.T,
	db *sqlite.DB,
	hostID domain.ID, name string,
	taskID domain.ID,
) domain.Container {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	container, err := domain.NewContainer(
		domain.NewID(),
		hostID,
		name,
		ref,
		"v0001",
		strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("b", 64),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	container.TaskID = &taskID
	if err := db.CreateContainer(testContext(t), container); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	return container
}
