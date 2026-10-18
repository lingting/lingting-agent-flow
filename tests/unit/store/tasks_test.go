package sqlite_test

import (
	"errors"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

func TestCreateAndGetTask(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)

	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.RequirementID != requirement.ID {
		t.Fatalf("需求归属不符: %s", loaded.RequirementID)
	}
	if loaded.Status != domain.TaskStatusQueued {
		t.Fatalf("任务初始状态不符: %s", loaded.Status)
	}
	if loaded.RepositoryPath != "/repo" || loaded.BaseRef != "main" {
		t.Fatalf("仓库信息不符: %+v", loaded)
	}
	if loaded.WorktreePath != "" {
		t.Fatalf("工作区路径应当为空: %q", loaded.WorktreePath)
	}
	if loaded.CurrentStepID != nil || loaded.StartedAt != nil || loaded.FinishedAt != nil {
		t.Fatalf("可空字段应当为空: %+v", loaded)
	}
	if !loaded.CreatedAt.Equal(task.CreatedAt) || !loaded.UpdatedAt.Equal(task.UpdatedAt) {
		t.Fatalf("时间戳未按毫秒精度保存: %+v", loaded)
	}
}

func TestCreateTaskAllowsOrphanRequirementID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	// 数据库不声明外键，requirement_id 只是普通文本，允许保存孤立标识。
	task, err := domain.NewTask(domain.NewID(), "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := db.CreateTask(testContext(t), task); err != nil {
		t.Fatalf("孤立需求标识应当允许保存，实际: %v", err)
	}
	loaded, err := db.GetTask(testContext(t), task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.RequirementID != task.RequirementID {
		t.Fatalf("孤立标识应当原样保留: %s", loaded.RequirementID)
	}
}

func TestTaskTransitionPersistsWorktreeAndStep(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	task := built.task
	if err := task.Transition(domain.TaskStatusPlanning, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := task.SetWorktreePath("/worktrees/task-1"); err != nil {
		t.Fatalf("设置工作区失败: %v", err)
	}
	stepID := built.step.ID
	if err := task.SetCurrentStep(&stepID); err != nil {
		t.Fatalf("设置当前步骤失败: %v", err)
	}
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatusPlanning {
		t.Fatalf("状态未持久化: %s", loaded.Status)
	}
	if loaded.WorktreePath != "/worktrees/task-1" {
		t.Fatalf("工作区路径未持久化: %q", loaded.WorktreePath)
	}
	if loaded.CurrentStepID == nil || *loaded.CurrentStepID != built.step.ID {
		t.Fatalf("当前步骤未持久化: %+v", loaded.CurrentStepID)
	}
	if loaded.StartedAt == nil {
		t.Fatal("开始时间应当被记录")
	}
}

func TestTaskTerminalStateRecordsFinishedAt(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusPlanning,
		domain.TaskStatusFailed,
	} {
		if err := task.Transition(status, time.Now().UTC()); err != nil {
			t.Fatalf("状态迁移失败: %v", err)
		}
	}
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatusFailed {
		t.Fatalf("状态未持久化: %s", loaded.Status)
	}
	if loaded.FinishedAt == nil {
		t.Fatal("结束时间应当被记录")
	}
}

func TestListTasksFiltersByRequirementAndStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	firstRequirement := mustRequirement(t, db)
	secondRequirement := mustRequirement(t, db)
	firstTask := mustTask(t, db, firstRequirement.ID)
	secondTask := mustTask(t, db, secondRequirement.ID)

	byRequirement, err := db.ListTasks(ctx, storage.TaskFilter{RequirementID: firstRequirement.ID})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(byRequirement) != 1 || byRequirement[0].ID != firstTask.ID {
		t.Fatalf("需求过滤结果不符: %+v", byRequirement)
	}

	byStatus, err := db.ListTasks(ctx, storage.TaskFilter{
		Statuses: []domain.TaskStatus{domain.TaskStatusQueued},
	})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(byStatus) != 2 {
		t.Fatalf("状态过滤结果不符: %d", len(byStatus))
	}

	limited, err := db.ListTasks(ctx, storage.TaskFilter{Limit: 1})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != secondTask.ID {
		t.Fatalf("限制条数未生效或排序不符: %+v", limited)
	}
}

func TestSameRequirementCanHaveMultipleTasks(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	first := mustTask(t, db, requirement.ID)
	second := mustTask(t, db, requirement.ID)

	tasks, err := db.ListTasks(ctx, storage.TaskFilter{RequirementID: requirement.ID})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("同一需求应当允许多次执行记录: %d", len(tasks))
	}
	seen := map[domain.ID]bool{first.ID: true, second.ID: true}
	for _, task := range tasks {
		if !seen[task.ID] {
			t.Fatalf("返回了未知任务: %s", task.ID)
		}
	}
}

func TestTaskAcceptsUnknownStatusAtDatabaseLevel(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	// 数据库不再限制枚举取值：未知状态可以保存，由 Go 层状态机拒绝非法迁移。
	testkit.MustExec(t, rawDB(t, db), `UPDATE tasks SET status = 'recovery_required_v2' WHERE id = ?`,
		task.ID.String(),
	)
	loaded, err := db.GetTask(testContext(t), task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatus("recovery_required_v2") {
		t.Fatalf("未知状态应当原样保留，实际: %s", loaded.Status)
	}
	// 业务状态机仍然拒绝不合法的迁移。
	if err := loaded.Transition(domain.TaskStatusCompleted, time.Now().UTC()); err == nil {
		t.Fatal("状态机应当拒绝未知状态的迁移")
	}
}

func TestTaskRequiresRepositoryPathAndBaseRef(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	requirement := mustRequirement(t, db)
	if _, err := domain.NewTask(requirement.ID, "  ", "main", "", time.Now().UTC()); err == nil {
		t.Fatal("空仓库路径应当被拒绝")
	}
	if _, err := domain.NewTask(requirement.ID, "/repo", " ", "", time.Now().UTC()); err == nil {
		t.Fatal("空基准引用应当被拒绝")
	}
	if _, err := db.GetTask(testContext(t), domain.NewID()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

func TestDeleteRequirementKeepsTaskHistory(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	// 数据库不声明外键：删除需求不会级联删除任务，也不会被阻止。
	testkit.MustExec(t, rawDB(t, db), `DELETE FROM requirements WHERE id = ?`, requirement.ID.String())
	if _, err := db.GetRequirement(ctx, requirement.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("需求应当已删除，实际: %v", err)
	}
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("任务历史不应因需求删除而丢失: %v", err)
	}
	if loaded.RequirementID != requirement.ID {
		t.Fatalf("任务应保留原需求标识: %s", loaded.RequirementID)
	}
}
