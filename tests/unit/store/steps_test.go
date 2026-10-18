package sqlite_test

import (
	"errors"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

func TestCreateAndGetStep(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.TaskID != built.task.ID || loaded.RoleID != built.developer.ID {
		t.Fatalf("步骤归属不符: %+v", loaded)
	}
	if loaded.Sequence != 1 {
		t.Fatalf("步骤序号不符: %d", loaded.Sequence)
	}
	if loaded.Instructions != built.step.Instructions {
		t.Fatalf("执行要求未正确往返: %q", loaded.Instructions)
	}
	if loaded.RoleSnapshot.Name != built.developer.Name {
		t.Fatalf("角色快照未保存: %+v", loaded.RoleSnapshot)
	}
	if len(loaded.VerificationSpec.Commands) != 1 {
		t.Fatalf("验证规格未完整保存: %+v", loaded.VerificationSpec)
	}
	if loaded.VerificationSpec.Commands[0].Command != "go" ||
		len(loaded.VerificationSpec.Commands[0].Args) != 2 {
		t.Fatalf("验证命令不符: %+v", loaded.VerificationSpec.Commands)
	}
	if loaded.Status != domain.StepStatusPending {
		t.Fatalf("步骤初始状态不符: %s", loaded.Status)
	}
	if !loaded.CreatedAt.Equal(built.step.CreatedAt) {
		t.Fatalf("创建时间未按毫秒精度保存: %s", loaded.CreatedAt)
	}
}

func TestStepRoleSnapshotSurvivesRoleUpdate(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	updated := built.developer
	updated.Name = "改名后的开发者"
	updated.Model = "provider/other"
	updated.UpdatedAt = time.Now().UTC()
	if err := db.UpdateRole(ctx, updated); err != nil {
		t.Fatalf("更新角色失败: %v", err)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.RoleSnapshot.Name != built.developer.Name {
		t.Fatalf("角色快照不应随角色修改而改变: %s", loaded.RoleSnapshot.Name)
	}
	if loaded.RoleSnapshot.Model != built.developer.Model {
		t.Fatalf("角色快照模型不应改变: %s", loaded.RoleSnapshot.Model)
	}
}

func TestDuplicateStepSequenceIsRejected(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	duplicate, err := domain.NewStep(
		built.task.ID,
		1,
		"重复序号",
		"目标",
		"要求",
		built.developer,
		domain.VerificationSpec{},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, duplicate); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复序号应当返回唯一约束冲突，实际: %v", err)
	}
	steps, err := db.ListStepsByTask(ctx, built.task.ID)
	if err != nil {
		t.Fatalf("查询步骤失败: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("步骤数量不符: %d", len(steps))
	}
}

func TestDifferentTasksCanShareSequence(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	role := mustRole(t, db, domain.RoleTypeDeveloper, "开发工程师")
	firstTask := mustTask(t, db, requirement.ID)
	secondTask := mustTask(t, db, requirement.ID)
	mustStep(t, db, firstTask, role, 1)
	mustStep(t, db, secondTask, role, 1)

	steps, err := db.ListStepsByTask(ctx, secondTask.ID)
	if err != nil {
		t.Fatalf("查询步骤失败: %v", err)
	}
	if len(steps) != 1 || steps[0].TaskID != secondTask.ID {
		t.Fatalf("跨任务序号应当互不影响: %+v", steps)
	}
}

func TestStepRequiresPositiveSequence(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	if _, err := domain.NewStep(
		built.task.ID,
		0,
		"标题",
		"目标",
		"要求",
		built.developer,
		domain.VerificationSpec{},
		time.Now().UTC(),
	); err == nil {
		t.Fatal("序号必须从 1 开始")
	}
}

func TestCreateStepsIsAtomic(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	role := mustRole(t, db, domain.RoleTypeDeveloper, "开发工程师")
	task := mustTask(t, db, requirement.ID)

	first, err := domain.NewStep(
		task.ID, 1, "第一步", "目标", "要求", role, domain.VerificationSpec{}, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	duplicate, err := domain.NewStep(
		task.ID, 1, "重复", "目标", "要求", role, domain.VerificationSpec{}, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	err = db.CreateSteps(ctx, []domain.Step{first, duplicate})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("批量写入重复序号应当返回冲突，实际: %v", err)
	}
	steps, err := db.ListStepsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("查询步骤失败: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("批量写入失败后不应留下步骤: %d", len(steps))
	}
}

func TestCreateStepsWritesAllInOrder(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	role := mustRole(t, db, domain.RoleTypeDeveloper, "开发工程师")
	task := mustTask(t, db, requirement.ID)
	steps := make([]domain.Step, 0, 3)
	for sequence := 1; sequence <= 3; sequence++ {
		step, err := domain.NewStep(
			task.ID,
			sequence,
			"步骤",
			"目标",
			"要求",
			role,
			domain.VerificationSpec{},
			time.Now().UTC(),
		)
		if err != nil {
			t.Fatalf("构造步骤失败: %v", err)
		}
		steps = append(steps, step)
	}
	if err := db.CreateSteps(ctx, steps); err != nil {
		t.Fatalf("批量写入步骤失败: %v", err)
	}
	loaded, err := db.ListStepsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("查询步骤失败: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("步骤数量不符: %d", len(loaded))
	}
	for index, step := range loaded {
		if step.Sequence != index+1 {
			t.Fatalf("步骤顺序不符: %+v", loaded)
		}
	}
}

func TestStepTransitionPersists(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	step := built.step
	if err := step.Transition(domain.StepStatusDeveloping, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := db.UpdateStep(ctx, step); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.Status != domain.StepStatusDeveloping {
		t.Fatalf("状态未持久化: %s", loaded.Status)
	}
}

func TestStepAcceptsUnknownStatusAndType(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	// 数据库不再限制 step_type / status 的取值集合。
	testkit.MustExec(
		t,
		rawDB(t, db),
		`UPDATE steps SET status = 'awaiting_review', step_type = 'audit' WHERE id = ?`,
		built.step.ID.String(),
	)
	loaded, err := db.GetStep(testContext(t), built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.Status != domain.StepStatus("awaiting_review") {
		t.Fatalf("未知状态应当原样保留，实际: %s", loaded.Status)
	}
	if loaded.Type != domain.StepType("audit") {
		t.Fatalf("未知步骤类型应当原样保留，实际: %s", loaded.Type)
	}
	// 业务状态机仍然拒绝不合法的迁移。
	if err := loaded.Transition(domain.StepStatusCompleted, time.Now().UTC()); err == nil {
		t.Fatal("状态机应当拒绝未知状态的迁移")
	}
}

func TestStepJSONColumnsRequireValidJSON(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	err := testkit.ExecConstraint(t, rawDB(t, db), `UPDATE steps SET verification_spec_json = 'not-json' WHERE id = ?`,
		built.step.ID.String(),
	)
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("非法 JSON 应当被拒绝，实际: %v", err)
	}
}

func TestStepAllowsOrphanRoleID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	missing := built.developer
	missing.ID = domain.NewID()
	step, err := domain.NewStep(
		built.task.ID,
		2,
		"标题",
		"目标",
		"要求",
		missing,
		domain.VerificationSpec{},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	// 数据库不声明外键，孤立角色标识作为普通文本保存。
	if err := db.CreateStep(testContext(t), step); err != nil {
		t.Fatalf("孤立角色标识应当允许保存，实际: %v", err)
	}
	loaded, err := db.GetStep(testContext(t), step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.RoleID != missing.ID {
		t.Fatalf("孤立角色标识应当原样保留: %s", loaded.RoleID)
	}
}

func TestDeleteTaskKeepsStepHistory(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	// 数据库不声明外键：删除任务不会级联删除步骤与尝试，也不会被阻止。
	testkit.MustExec(t, rawDB(t, db), `DELETE FROM tasks WHERE id = ?`, built.task.ID.String())
	if _, err := db.GetTask(ctx, built.task.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("任务应当已删除，实际: %v", err)
	}
	step, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("步骤历史不应因任务删除而丢失: %v", err)
	}
	if step.TaskID != built.task.ID {
		t.Fatalf("步骤应保留原任务标识: %s", step.TaskID)
	}
	if _, err := db.GetAttempt(ctx, built.attempt.ID); err != nil {
		t.Fatalf("尝试历史不应因任务删除而丢失: %v", err)
	}
}
