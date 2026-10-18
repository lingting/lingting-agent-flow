package sqlite_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestListTaskPageReturnsTotalAndSlice 校验分页查询返回总数与当前页记录。
func TestListTaskPageReturnsTotalAndSlice(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	created := make([]domain.Task, 0, 5)
	for index := 0; index < 5; index++ {
		created = append(created, mustTask(t, db, requirement.ID))
	}

	first, err := db.ListTaskPage(ctx, storage.TaskFilter{Limit: 2})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if first.Total != len(created) {
		t.Fatalf("总数不符: 期望 %d，实际 %d", len(created), first.Total)
	}
	if len(first.Tasks) != 2 {
		t.Fatalf("首页记录数不符: %d", len(first.Tasks))
	}

	second, err := db.ListTaskPage(ctx, storage.TaskFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("第二页查询失败: %v", err)
	}
	if second.Total != len(created) {
		t.Fatalf("分页不应改变总数: %d", second.Total)
	}
	if len(second.Tasks) != 2 {
		t.Fatalf("第二页记录数不符: %d", len(second.Tasks))
	}
	// 分页必须按创建时间倒序且不重复：两页的标识集合不相交。
	for _, left := range first.Tasks {
		for _, right := range second.Tasks {
			if left.ID == right.ID {
				t.Fatalf("分页结果出现重复记录: %s", left.ID)
			}
		}
	}

	last, err := db.ListTaskPage(ctx, storage.TaskFilter{Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("末页查询失败: %v", err)
	}
	if len(last.Tasks) != 1 {
		t.Fatalf("末页记录数不符: %d", len(last.Tasks))
	}

	empty, err := db.ListTaskPage(ctx, storage.TaskFilter{Limit: 2, Offset: 100})
	if err != nil {
		t.Fatalf("越界分页查询失败: %v", err)
	}
	if empty.Total != len(created) || len(empty.Tasks) != 0 {
		t.Fatalf("越界分页结果不符: %+v", empty)
	}
}

// TestListTaskPageFiltersByStatus 校验状态筛选同时作用于记录与总数。
func TestListTaskPageFiltersByStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	pending := mustTask(t, db, requirement.ID)
	finished := mustTask(t, db, requirement.ID)
	if err := finished.Transition(domain.TaskStatusPlanning, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := finished.Transition(domain.TaskStatusFailed, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := db.UpdateTask(ctx, finished); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	page, err := db.ListTaskPage(ctx, storage.TaskFilter{
		Statuses: []domain.TaskStatus{domain.TaskStatusQueued},
	})
	if err != nil {
		t.Fatalf("按状态查询失败: %v", err)
	}
	if page.Total != 1 || len(page.Tasks) != 1 || page.Tasks[0].ID != pending.ID {
		t.Fatalf("状态筛选结果不符: %+v", page)
	}
}

// TestDeleteTaskMarksDeletedAndKeepsHistory 校验逻辑删除只标记时间并保留历史。
func TestDeleteTaskMarksDeletedAndKeepsHistory(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	if err := task.Transition(domain.TaskStatusPlanning, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusFailed, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	deletedAt := time.Now().UTC()
	if err := db.DeleteTask(
		ctx,
		task.ID,
		[]domain.TaskStatus{domain.TaskStatusFailed},
		deletedAt,
	); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}

	// 记录本身仍在，deleted_at 已写入。
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("删除后记录不应消失: %v", err)
	}
	if loaded.DeletedAt == nil {
		t.Fatal("deleted_at 应当被写入")
	}
	if loaded.Status != domain.TaskStatusFailed {
		t.Fatalf("删除不应改变状态: %s", loaded.Status)
	}

	// 列表默认不包含已删除记录，显式包含时可以读到。
	visible, err := db.ListTasks(ctx, storage.TaskFilter{})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("已删除任务不应出现在默认列表中: %d", len(visible))
	}
	all, err := db.ListTasks(ctx, storage.TaskFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("查询含删除任务失败: %v", err)
	}
	if len(all) != 1 || all[0].ID != task.ID {
		t.Fatalf("显式包含删除记录时应当读到: %+v", all)
	}

	// 需求记录不受影响。
	if _, err := db.GetRequirement(ctx, requirement.ID); err != nil {
		t.Fatalf("删除任务不应影响需求: %v", err)
	}
}

// TestDeleteTaskRejectsUnexpectedStatus 校验状态前置条件不满足时返回冲突。
func TestDeleteTaskRejectsUnexpectedStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)

	err := db.DeleteTask(
		ctx,
		task.ID,
		[]domain.TaskStatus{domain.TaskStatusFailed},
		time.Now().UTC(),
	)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("状态不匹配时应返回冲突，实际: %v", err)
	}
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.DeletedAt != nil {
		t.Fatal("冲突时不应写入 deleted_at")
	}
}

// TestDeleteTaskIsNotIdempotentAtStorageLevel 校验重复删除返回冲突。
//
// 幂等语义由业务层决定：存储层只保证同一记录不会被两次成功删除。
func TestDeleteTaskIsNotIdempotentAtStorageLevel(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	if err := task.Transition(domain.TaskStatusPlanning, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusFailed, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	expected := []domain.TaskStatus{domain.TaskStatusFailed}
	if err := db.DeleteTask(ctx, task.ID, expected, time.Now().UTC()); err != nil {
		t.Fatalf("首次删除失败: %v", err)
	}
	if err := db.DeleteTask(ctx, task.ID, expected, time.Now().UTC()); !errors.Is(
		err,
		storage.ErrConflict,
	) {
		t.Fatalf("重复删除应返回冲突，实际: %v", err)
	}
}

// TestDeleteTaskRejectsEmptyExpectedStatuses 校验缺少状态前置条件时拒绝执行。
func TestDeleteTaskRejectsEmptyExpectedStatuses(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	err := db.DeleteTask(ctx, task.ID, nil, time.Now().UTC())
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("缺少状态集合时应返回数据非法，实际: %v", err)
	}
}

// TestTaskDeletedAtPersistsAndRoundTrips 校验 deleted_at 在读写间保持毫秒精度。
func TestTaskDeletedAtPersistsAndRoundTrips(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	deletedAt := time.Date(2025, 3, 4, 5, 6, 7, 123_000_000, time.UTC)
	task.DeletedAt = &deletedAt
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.DeletedAt == nil || !loaded.DeletedAt.Equal(deletedAt) {
		t.Fatalf("deleted_at 未按毫秒精度往返: %+v", loaded.DeletedAt)
	}
}

// TestUnknownStatusSurvivesSoftDelete 校验未知状态值在逻辑删除后仍原样保留。
func TestUnknownStatusSurvivesSoftDelete(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	// 数据库不使用枚举白名单：未知状态可以写入（仓储写入前会做领域校验，
	// 因此这里直接用 SQL 制造未知状态，与既有审计测试保持一致）。
	testkit.MustExec(
		t,
		rawDB(t, db),
		`UPDATE tasks SET status = 'awaiting_review_v2' WHERE id = ?`,
		task.ID.String(),
	)
	if err := db.DeleteTask(
		ctx,
		task.ID,
		[]domain.TaskStatus{"awaiting_review_v2"},
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("按未知状态删除失败: %v", err)
	}
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatus("awaiting_review_v2") {
		t.Fatalf("未知状态应原样保留: %s", loaded.Status)
	}
	if loaded.DeletedAt == nil {
		t.Fatal("deleted_at 应当被写入")
	}
}

// TestConcurrentDeleteTaskOnlyOneSucceeds 校验并发删除同一任务只有一个成功。
func TestConcurrentDeleteTaskOnlyOneSucceeds(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	if err := task.Transition(domain.TaskStatusPlanning, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusFailed, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	const workers = 8
	results := make(chan error, workers)
	expected := []domain.TaskStatus{domain.TaskStatusFailed}
	for index := 0; index < workers; index++ {
		go func() {
			results <- db.DeleteTask(ctx, task.ID, expected, time.Now().UTC())
		}()
	}
	succeeded := 0
	for index := 0; index < workers; index++ {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, storage.ErrConflict):
			// 其余请求必须明确失败，而不是静默成功。
		default:
			t.Fatalf("并发删除返回了非预期错误: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("并发删除应当只有一个成功，实际 %d", succeeded)
	}
}
