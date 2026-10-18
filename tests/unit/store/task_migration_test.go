package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestTaskSoftDeleteMigrationAddsColumnAndIndex 校验增量迁移只新增列与索引。
func TestTaskSoftDeleteMigrationAddsColumnAndIndex(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	conn := rawDB(t, db)
	ctx := testContext(t)

	// deleted_at 列存在且允许为空。
	var notNull int
	var defaultValue any
	if err := conn.QueryRowContext(
		ctx,
		`SELECT "notnull", dflt_value FROM pragma_table_info('tasks') WHERE name = 'deleted_at'`,
	).Scan(&notNull, &defaultValue); err != nil {
		t.Fatalf("读取 tasks.deleted_at 定义失败: %v", err)
	}
	if notNull != 0 {
		t.Fatalf("deleted_at 必须允许为空，实际 notnull=%d", notNull)
	}

	// 部分索引存在，只覆盖未删除记录。
	var indexSQL string
	if err := conn.QueryRowContext(
		ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_tasks_active'`,
	).Scan(&indexSQL); err != nil {
		t.Fatalf("读取 idx_tasks_active 定义失败: %v", err)
	}
	if indexSQL == "" {
		t.Fatal("idx_tasks_active 索引定义不应为空")
	}

	// 原有索引与约束未被破坏。
	var count int
	if err := conn.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_tasks_status'`,
	).Scan(&count); err != nil {
		t.Fatalf("读取原有索引失败: %v", err)
	}
	if count != 1 {
		t.Fatal("迁移不应删除已有索引")
	}
}

// TestTaskSoftDeleteMigrationPreservesExistingRows 校验迁移不丢失已有任务数据。
func TestTaskSoftDeleteMigrationPreservesExistingRows(t *testing.T) {
	t.Parallel()
	path := filepath.Join(testkit.Dir(t), "agentflow.db")
	db := openTestDB(t, path)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 重新打开：迁移已应用，数据必须完整保留，deleted_at 为空。
	reopened := openTestDB(t, path)
	loaded, err := reopened.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("重新打开后读取任务失败: %v", err)
	}
	if loaded.ID != task.ID {
		t.Fatalf("任务标识不符: %s", loaded.ID)
	}
	if loaded.DeletedAt != nil {
		t.Fatalf("历史任务不应被标记删除: %v", loaded.DeletedAt)
	}
	if loaded.RequirementID != requirement.ID {
		t.Fatalf("需求关联丢失: %s", loaded.RequirementID)
	}
}

// TestSoftDeletedTaskIsExcludedFromQueries 校验软删除任务的查询可见性。
func TestSoftDeletedTaskIsExcludedFromQueries(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	kept := mustTask(t, db, requirement.ID)
	removed := mustTask(t, db, requirement.ID)
	for _, task := range []domain.Task{kept, removed} {
		if err := task.Transition(domain.TaskStatusPreparing, time.Now().UTC()); err != nil {
			t.Fatalf("状态迁移失败: %v", err)
		}
		if err := task.Transition(domain.TaskStatusFailed, time.Now().UTC()); err != nil {
			t.Fatalf("状态迁移失败: %v", err)
		}
		if err := db.UpdateTask(ctx, task); err != nil {
			t.Fatalf("更新任务失败: %v", err)
		}
	}
	if err := db.DeleteTask(
		ctx,
		removed.ID,
		[]domain.TaskStatus{domain.TaskStatusFailed},
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}

	page, err := db.ListTaskPage(ctx, storage.TaskFilter{})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if page.Total != 1 || len(page.Tasks) != 1 || page.Tasks[0].ID != kept.ID {
		t.Fatalf("分页结果应只包含未删除任务: %+v", page)
	}

	all, err := db.ListTaskPage(ctx, storage.TaskFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("包含删除记录的查询失败: %v", err)
	}
	if all.Total != 2 {
		t.Fatalf("显式包含删除记录时总数应为 2，实际 %d", all.Total)
	}

	// 按需求筛选时同样默认排除删除记录。
	byRequirement, err := db.ListTasks(ctx, storage.TaskFilter{RequirementID: requirement.ID})
	if err != nil {
		t.Fatalf("按需求查询失败: %v", err)
	}
	if len(byRequirement) != 1 {
		t.Fatalf("按需求查询应排除已删除任务: %d", len(byRequirement))
	}
}

// TestTaskPageCountMatchesFilter 校验总数统计与筛选条件一致。
func TestTaskPageCountMatchesFilter(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	first := mustRequirement(t, db)
	second := mustRequirement(t, db)
	mustTask(t, db, first.ID)
	mustTask(t, db, first.ID)
	mustTask(t, db, second.ID)

	page, err := db.ListTaskPage(ctx, storage.TaskFilter{RequirementID: first.ID})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("按需求筛选的总数应为 2，实际 %d", page.Total)
	}
	if len(page.Tasks) != 2 {
		t.Fatalf("按需求筛选的记录数应为 2，实际 %d", len(page.Tasks))
	}

	limited, err := db.ListTaskPage(ctx, storage.TaskFilter{RequirementID: first.ID, Limit: 1})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if limited.Total != 2 {
		t.Fatalf("分页不应改变总数: %d", limited.Total)
	}
	if len(limited.Tasks) != 1 {
		t.Fatalf("页大小限制未生效: %d", len(limited.Tasks))
	}
}

// TestOpenStoreFailurePropagates 校验数据库访问失败时错误向上传播。
func TestOpenStoreFailurePropagates(t *testing.T) {
	t.Parallel()
	// 用目录路径当数据库文件：打开必然失败，错误必须返回而不是 panic。
	dir := testkit.Dir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: dir})
	if err == nil {
		_ = db.Close()
		t.Fatal("以目录作为数据库路径时必须返回错误")
	}
	if err.Error() == "" {
		t.Fatal("错误信息不能为空")
	}
}

// TestClosedDatabasePropagatesError 校验关闭后的数据库访问返回错误而不是静默成功。
func TestClosedDatabasePropagatesError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(testkit.Dir(t), "agentflow.db")
	db, err := sqlite.Open(testContext(t), sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	if _, err := db.ListTaskPage(testContext(t), storage.TaskFilter{}); err == nil {
		t.Fatal("关闭后查询必须返回错误")
	}
}
