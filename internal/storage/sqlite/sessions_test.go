package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

func TestCreateAndGetSession(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	session := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.developer)

	loaded, err := db.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if loaded.TaskID != built.task.ID || loaded.RoleID != built.developer.ID {
		t.Fatalf("会话归属不符: %+v", loaded)
	}
	if loaded.StepID == nil || *loaded.StepID != stepID {
		t.Fatalf("步骤归属不符: %+v", loaded.StepID)
	}
	if loaded.AttemptID == nil || *loaded.AttemptID != attemptID {
		t.Fatalf("尝试归属不符: %+v", loaded.AttemptID)
	}
	if loaded.Status != domain.SessionStatusStarting {
		t.Fatalf("会话初始状态不符: %s", loaded.Status)
	}
	if loaded.ExternalSessionID != "" || loaded.ProcessID != nil {
		t.Fatalf("外部标识与进程 ID 应当为空: %+v", loaded)
	}
	if loaded.RoleSnapshot.Name != built.developer.Name {
		t.Fatalf("角色快照未保存: %+v", loaded.RoleSnapshot)
	}
	if !loaded.CreatedAt.Equal(session.CreatedAt) {
		t.Fatalf("创建时间未按毫秒精度保存: %s", loaded.CreatedAt)
	}
}

func TestSessionMarkRunningPersistsExternalReference(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	session := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.developer)

	if err := session.MarkRunning("pi-session-1"); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := session.SetProcessID(4242); err != nil {
		t.Fatalf("设置进程 ID 失败: %v", err)
	}
	if err := db.UpdateSession(ctx, session); err != nil {
		t.Fatalf("更新会话失败: %v", err)
	}
	loaded, err := db.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if loaded.Status != domain.SessionStatusRunning {
		t.Fatalf("状态未持久化: %s", loaded.Status)
	}
	if loaded.ExternalSessionID != "pi-session-1" {
		t.Fatalf("外部会话标识未持久化: %q", loaded.ExternalSessionID)
	}
	if loaded.ProcessID == nil || *loaded.ProcessID != 4242 {
		t.Fatalf("进程 ID 未持久化: %+v", loaded.ProcessID)
	}
}

func TestSessionCompletePersistsFinishedAt(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	session := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.developer)

	if err := session.MarkRunning("pi-session-2"); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := session.Complete(time.Now().UTC()); err != nil {
		t.Fatalf("标记完成失败: %v", err)
	}
	if err := db.UpdateSession(ctx, session); err != nil {
		t.Fatalf("更新会话失败: %v", err)
	}
	loaded, err := db.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if loaded.Status != domain.SessionStatusCompleted {
		t.Fatalf("状态未持久化: %s", loaded.Status)
	}
	if loaded.FinishedAt == nil {
		t.Fatal("结束时间应当被记录")
	}
}

func TestPlannerSessionWithoutStepAndAttempt(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	session := mustSession(t, db, domain.SessionScope{TaskID: built.task.ID}, built.planner)

	loaded, err := db.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if loaded.StepID != nil || loaded.AttemptID != nil {
		t.Fatalf("任务级会话不应有步骤与尝试: %+v", loaded)
	}
	if loaded.RoleID != built.planner.ID {
		t.Fatalf("角色不符: %s", loaded.RoleID)
	}
}

func TestAttemptCanHaveDeveloperAndValidatorSessions(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	scope := domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}
	developer := mustSession(t, db, scope, built.developer)
	validator := mustSession(t, db, scope, built.validator)

	sessions, err := db.ListSessionsByAttempt(ctx, attemptID)
	if err != nil {
		t.Fatalf("查询会话失败: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("同一尝试应当有多个会话: %d", len(sessions))
	}
	if sessions[0].ID != developer.ID || sessions[1].ID != validator.ID {
		t.Fatalf("会话顺序不符: %+v", sessions)
	}
	if sessions[0].RoleID != built.developer.ID || sessions[1].RoleID != built.validator.ID {
		t.Fatalf("会话角色不符: %+v", sessions)
	}

	byTask, err := db.ListSessionsByTask(ctx, built.task.ID)
	if err != nil {
		t.Fatalf("查询会话失败: %v", err)
	}
	if len(byTask) != 2 {
		t.Fatalf("任务会话数量不符: %d", len(byTask))
	}
}

func TestSessionScopeMismatchIsRejected(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	otherStep := mustStep(t, db, built.task, built.developer, 2)
	attemptID := built.attempt.ID

	session, err := domain.NewSession(domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &otherStep.ID,
		AttemptID: &attemptID,
	}, built.developer, "pi", "provider/model", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	if err := db.CreateSession(ctx, session); !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("步骤与尝试不一致时应当被拒绝，实际: %v", err)
	}
	sessions, err := db.ListSessionsByTask(ctx, built.task.ID)
	if err != nil {
		t.Fatalf("查询会话失败: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("非法会话不应写入: %d", len(sessions))
	}
}

func TestSessionRequiresExistingAttempt(t *testing.T) {
	db := newTestDB(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	missingAttempt := domain.NewID()
	session, err := domain.NewSession(domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &missingAttempt,
	}, built.developer, "pi", "provider/model", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	if err := db.CreateSession(testContext(t), session); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("引用不存在的尝试应当返回记录不存在，实际: %v", err)
	}
}

func TestSessionRequiresExistingTask(t *testing.T) {
	db := newTestDB(t)
	built := newFixture(t, db)
	session, err := domain.NewSession(
		domain.SessionScope{TaskID: domain.NewID()},
		built.planner,
		"pi",
		"provider/model",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	if err := db.CreateSession(testContext(t), session); !errors.Is(err, storage.ErrForeignKey) {
		t.Fatalf("引用不存在的任务应当返回外键错误，实际: %v", err)
	}
}

func TestSessionAttemptRequiresStep(t *testing.T) {
	db := newTestDB(t)
	built := newFixture(t, db)
	attemptID := built.attempt.ID
	if _, err := domain.NewSession(domain.SessionScope{
		TaskID:    built.task.ID,
		AttemptID: &attemptID,
	}, built.developer, "pi", "provider/model", time.Now().UTC()); err == nil {
		t.Fatal("关联尝试时必须指定步骤")
	}
}

func TestSessionInvalidStatusIsRejected(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	session := mustSession(t, db, domain.SessionScope{TaskID: built.task.ID}, built.planner)
	_, err := db.sql.ExecContext(
		ctx,
		`UPDATE sessions SET status = 'unknown' WHERE id = ?`,
		session.ID.String(),
	)
	if !errors.Is(classify(err), storage.ErrInvalidData) {
		t.Fatalf("非法会话状态应当被拒绝，实际: %v", err)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.GetSession(testContext(t), domain.NewID()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}
