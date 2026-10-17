package sqlite_test

import (
	"errors"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

func TestCreateAndGetAttempt(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	loaded, err := db.GetAttempt(ctx, built.attempt.ID)
	if err != nil {
		t.Fatalf("读取执行尝试失败: %v", err)
	}
	if loaded.StepID != built.step.ID {
		t.Fatalf("步骤归属不符: %s", loaded.StepID)
	}
	if loaded.AttemptNumber != 1 {
		t.Fatalf("尝试序号不符: %d", loaded.AttemptNumber)
	}
	if loaded.Status != domain.AttemptStatusRunning {
		t.Fatalf("初始状态不符: %s", loaded.Status)
	}
	if loaded.FailureSummary != "" || loaded.FinishedAt != nil {
		t.Fatalf("未结束的尝试不应有失败摘要与结束时间: %+v", loaded)
	}
	if !loaded.StartedAt.Equal(built.attempt.StartedAt) ||
		!loaded.CreatedAt.Equal(built.attempt.CreatedAt) {
		t.Fatalf("时间戳未按毫秒精度保存: %+v", loaded)
	}
}

func TestMultipleAttemptsPerStepKeepHistory(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	second := mustAttempt(t, db, built.step, 2)
	if err := built.attempt.Fail("第一次失败", time.Now().UTC()); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	if err := db.UpdateAttempt(ctx, built.attempt); err != nil {
		t.Fatalf("更新执行尝试失败: %v", err)
	}

	attempts, err := db.ListAttemptsByStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("查询执行尝试失败: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("尝试数量不符: %d", len(attempts))
	}
	if attempts[0].ID != built.attempt.ID || attempts[1].ID != second.ID {
		t.Fatalf("尝试顺序不符: %+v", attempts)
	}
	if attempts[0].Status != domain.AttemptStatusFailed {
		t.Fatalf("第一次尝试状态不符: %s", attempts[0].Status)
	}
	if attempts[0].FailureSummary != "第一次失败" {
		t.Fatalf("失败摘要未保存: %q", attempts[0].FailureSummary)
	}
	if attempts[0].FinishedAt == nil {
		t.Fatal("已失败的尝试应当有结束时间")
	}
	if attempts[1].Status != domain.AttemptStatusRunning {
		t.Fatalf("第二次尝试应当仍在运行: %s", attempts[1].Status)
	}
}

func TestDuplicateAttemptNumberIsRejected(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	duplicate, err := domain.NewAttempt(built.step.ID, 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造执行尝试失败: %v", err)
	}
	if err := db.CreateAttempt(ctx, duplicate); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复尝试序号应当返回冲突，实际: %v", err)
	}
	if _, err := db.GetAttempt(ctx, built.attempt.ID); err != nil {
		t.Fatalf("原有尝试不应被覆盖: %v", err)
	}
}

func TestAttemptRequiresPositiveNumber(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	if _, err := domain.NewAttempt(built.step.ID, 0, time.Now().UTC()); err == nil {
		t.Fatal("尝试序号必须大于 0")
	}
}

func TestNextAttemptNumber(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	next, err := db.NextAttemptNumber(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("查询下一尝试序号失败: %v", err)
	}
	if next != 2 {
		t.Fatalf("下一尝试序号不符: %d", next)
	}
	mustAttempt(t, db, built.step, 2)
	next, err = db.NextAttemptNumber(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("查询下一尝试序号失败: %v", err)
	}
	if next != 3 {
		t.Fatalf("下一尝试序号不符: %d", next)
	}
}

func TestNextAttemptNumberOnEmptyStep(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	role := mustRole(t, db, domain.RoleTypeDeveloper, "开发工程师")
	task := mustTask(t, db, requirement.ID)
	step := mustStep(t, db, task, role, 1)

	next, err := db.NextAttemptNumber(ctx, step.ID)
	if err != nil {
		t.Fatalf("查询下一尝试序号失败: %v", err)
	}
	if next != 1 {
		t.Fatalf("空步骤的下一尝试序号应为 1，实际: %d", next)
	}
}

func TestAttemptInterruptedStatusPersists(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	attempt := built.attempt
	if err := attempt.Interrupt("进程异常退出", time.Now().UTC()); err != nil {
		t.Fatalf("标记中断失败: %v", err)
	}
	if err := db.UpdateAttempt(ctx, attempt); err != nil {
		t.Fatalf("更新执行尝试失败: %v", err)
	}
	loaded, err := db.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("读取执行尝试失败: %v", err)
	}
	if loaded.Status != domain.AttemptStatusInterrupted {
		t.Fatalf("中断状态未持久化: %s", loaded.Status)
	}
	if loaded.FailureSummary != "进程异常退出" {
		t.Fatalf("中断摘要未持久化: %q", loaded.FailureSummary)
	}
}

func TestAttemptInvalidStatusIsRejected(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	err := testkit.ExecConstraint(t, rawDB(t, db), `UPDATE attempts SET status = 'unknown' WHERE id = ?`,
		built.attempt.ID.String(),
	)
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("非法尝试状态应当被拒绝，实际: %v", err)
	}
}

func TestAttemptRequiresExistingStep(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	attempt, err := domain.NewAttempt(domain.NewID(), 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造执行尝试失败: %v", err)
	}
	if err := db.CreateAttempt(testContext(t), attempt); !errors.Is(err, storage.ErrForeignKey) {
		t.Fatalf("引用不存在的步骤应当返回外键错误，实际: %v", err)
	}
}

func TestGetAttemptNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	if _, err := db.GetAttempt(testContext(t), domain.NewID()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

func TestDeleteStepWithAttemptsIsRejected(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	err := testkit.ExecConstraint(t, rawDB(t, db), `DELETE FROM steps WHERE id = ?`, built.step.ID.String())
	if !errors.Is(err, storage.ErrForeignKey) {
		t.Fatalf("被尝试引用的步骤不应被删除，实际: %v", err)
	}
	if _, err := db.GetStep(ctx, built.step.ID); err != nil {
		t.Fatalf("步骤不应被删除: %v", err)
	}
}
