// Package app_test 验证残留收敛服务对 Attempt / Session / ValidationRun 的处理。
//
// 用例使用真实 SQLite 与正式的 app.ExecutionResidueService：断言的对象是数据库里
// 真实留下的 attempts / sessions / validation_runs 记录，而不是方法的返回值。
package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// residueFixture 是一套残留收敛测试现场。
type residueFixture struct {
	ctx       context.Context
	db        *sqlite.DB
	logger    *logging.Logger
	now       time.Time
	workspace *testkit.Workspace
}

// newResidueFixture 在工作空间中准备数据库与角色。
func newResidueFixture(t *testing.T) *residueFixture {
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
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	return &residueFixture{
		ctx:       ctx,
		db:        db,
		logger:    logger,
		now:       time.Now().UTC(),
		workspace: workspace,
	}
}

// service 构造残留收敛服务。
func (f *residueFixture) service(t *testing.T) *app.ExecutionResidueService {
	t.Helper()
	service, err := app.NewExecutionResidueService(app.ExecutionResidueOptions{
		Store:  f.db,
		Logger: f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造残留收敛服务失败: %v", err)
	}
	return service
}

// seed 写入任务、步骤与一次 running 尝试，返回任务与尝试。
func (f *residueFixture) seed(t *testing.T) (domain.Task, domain.Step, domain.Attempt) {
	t.Helper()
	developer := mustValidationRole(t, f.db, f.ctx, domain.RoleTypeDeveloper, f.now)
	host := mustValidationHost(t, f.db, f.ctx, f.now)
	requirement := mustValidationRequirement(t, f.db, f.ctx, f.now)
	task := mustValidationTask(
		t,
		f.db,
		f.ctx,
		requirement,
		host,
		developer,
		developer,
		f.now,
	)
	step := mustValidationStep(t, f.db, f.ctx, task, developer, requiredSpec("build"), f.now)
	attempt := mustValidationAttempt(t, f.db, f.ctx, step, f.now)
	return task, step, attempt
}

// writeOrphanedRun 写入一条"租约已过期"的执行记录。
//
// 它表达"原执行者已经退出"：只有这种情况才允许收敛其残留记录。
func (f *residueFixture) writeOrphanedRun(
	t *testing.T,
	task domain.Task,
	steps []domain.Step,
) domain.WorkflowRun {
	t.Helper()
	run, err := domain.NewWorkflowRun(task.ID, steps, f.now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("dead-worker", time.Minute, f.now.Add(-time.Hour)); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := f.db.CreateWorkflowRun(f.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	return run
}

// writeLiveRun 写入一条"租约仍然有效"的执行记录。
func (f *residueFixture) writeLiveRun(
	t *testing.T,
	task domain.Task,
	steps []domain.Step,
) domain.WorkflowRun {
	t.Helper()
	run, err := domain.NewWorkflowRun(task.ID, steps, f.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("live-worker", time.Hour, f.now); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := f.db.CreateWorkflowRun(f.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	return run
}

// TestResidueConvergesAttemptToInterrupted 验证残留 running 尝试收敛为 interrupted。
//
// 收敛目标固定为 interrupted：它表达的正是"编排器或宿主进程异常结束，未能给出
// 正常结论"。残留记录绝不被改回 running，也绝不被改成 completed。
func TestResidueConvergesAttemptToInterrupted(t *testing.T) {
	t.Parallel()
	fixture := newResidueFixture(t)
	task, step, attempt := fixture.seed(t)
	fixture.writeOrphanedRun(t, task, []domain.Step{step})

	if err := fixture.service(t).Start(fixture.ctx); err != nil {
		t.Fatalf("收敛残留失败: %v", err)
	}
	stored, err := fixture.db.GetAttempt(fixture.ctx, attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if stored.Status != domain.AttemptStatusInterrupted {
		t.Fatalf("残留尝试应收敛为 interrupted，实际 %s", stored.Status)
	}
	if stored.FinishedAt == nil {
		t.Fatal("收敛后必须记录结束时间")
	}
	if stored.FailureSummary == "" {
		t.Fatal("收敛后必须写入可读摘要")
	}
	if stored.FailureStage != "recover" {
		t.Fatalf("失败阶段应标记为 recover，实际 %q", stored.FailureStage)
	}
}

// TestResidueConvergesSessionToInterrupted 验证残留会话收敛为 interrupted。
//
// 关键约束：收敛不得删除已有的产物路径，它们是唯一的现场。
func TestResidueConvergesSessionToInterrupted(t *testing.T) {
	t.Parallel()
	fixture := newResidueFixture(t)
	task, step, _ := fixture.seed(t)
	fixture.writeOrphanedRun(t, task, []domain.Step{step})

	developer, err := fixture.db.GetRole(fixture.ctx, task.Config.DeveloperRoleID)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	session, err := domain.NewSession(
		domain.SessionScope{TaskID: task.ID, StepID: &step.ID},
		*developer,
		"pi",
		"group/free",
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	if err := session.MarkRunning("pi-session-1"); err != nil {
		t.Fatalf("标记会话运行失败: %v", err)
	}
	session.JSONLPath = "sessions/raw.jsonl"
	session.StdoutPath = "sessions/stdout.log"
	session.StderrPath = "sessions/stderr.log"
	// 三个维度必须同时给出取值：只填其中一项会让会话记录校验失败。
	session.RunStatus = domain.SessionRunFailed
	session.Completeness = domain.SessionCompletenessPartial
	session.ExportStatus = domain.SessionExportNotAttempted
	session.ArchiveStatus = domain.SessionArchiveSucceeded
	session.ArchivePath = "sessions/archive.jsonl"
	processID := 4242
	session.ProcessID = &processID
	if err := fixture.db.CreateSession(fixture.ctx, session); err != nil {
		t.Fatalf("写入会话失败: %v", err)
	}

	if err := fixture.service(t).Start(fixture.ctx); err != nil {
		t.Fatalf("收敛残留失败: %v", err)
	}
	stored, err := fixture.db.GetSession(fixture.ctx, session.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if stored.Status != domain.SessionStatusInterrupted {
		t.Fatalf("残留会话应收敛为 interrupted，实际 %s", stored.Status)
	}
	if stored.RunStatus != domain.SessionRunFailed {
		t.Fatalf("运行结果应为 failed，实际 %s", stored.RunStatus)
	}
	// 现场必须原样保留：收敛只改执行状态，不改产物路径与归档事实。
	if stored.JSONLPath != "sessions/raw.jsonl" {
		t.Fatalf("原始会话路径被改写: %q", stored.JSONLPath)
	}
	if stored.StdoutPath != "sessions/stdout.log" || stored.StderrPath != "sessions/stderr.log" {
		t.Fatal("标准输出/错误日志路径被改写")
	}
	if stored.ProcessID == nil || *stored.ProcessID != processID {
		t.Fatal("进程标识被改写")
	}
	if stored.ArchiveStatus != domain.SessionArchiveSucceeded ||
		stored.ArchivePath != "sessions/archive.jsonl" {
		t.Fatal("归档事实被改写：归档成功仍然是归档成功")
	}
	if stored.Completeness != domain.SessionCompletenessPartial {
		t.Fatalf("完整性状态被改写: %s", stored.Completeness)
	}
}

// TestResidueConvergesValidationRunToFailed 验证残留验证收敛为 failed 且不判通过。
func TestResidueConvergesValidationRunToFailed(t *testing.T) {
	t.Parallel()
	fixture := newResidueFixture(t)
	task, step, attempt := fixture.seed(t)
	fixture.writeOrphanedRun(t, task, []domain.Step{step})

	run, err := fixture.db.BeginValidationRun(fixture.ctx, storage.ValidationRunStartRequest{
		TaskID:             task.ID,
		StepID:             step.ID,
		AttemptID:          attempt.ID,
		ExpectedTaskStatus: task.Status,
		Spec:               requiredSpec("build"),
		SpecSnapshot:       "{}",
		ExecutionEnv:       "container",
		Workspace:          "/workspace/project",
		ContainerID:        "container-1",
		At:                 fixture.now,
	})
	if err != nil {
		t.Fatalf("创建验证记录失败: %v", err)
	}

	if err := fixture.service(t).Start(fixture.ctx); err != nil {
		t.Fatalf("收敛残留失败: %v", err)
	}
	stored, err := fixture.db.GetValidationRun(fixture.ctx, run.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if stored.Status != domain.ValidationRunStatusFailed {
		t.Fatalf("残留验证应收敛为 failed，实际 %s", stored.Status)
	}
	if stored.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("结论必须是不通过，实际 %s", stored.Verdict)
	}
	// 中断导致无法验证，不得写成"必需检查未通过"。
	if containsCodeFailureWording(stored.FailureSummary) {
		t.Fatalf("收敛说明不得暗示代码检查失败: %q", stored.FailureSummary)
	}
	// 收敛后该尝试可以再次验证：旧记录不再永久阻塞。
	if _, err := fixture.db.BeginValidationRun(fixture.ctx, storage.ValidationRunStartRequest{
		TaskID:             task.ID,
		StepID:             step.ID,
		AttemptID:          attempt.ID,
		ExpectedTaskStatus: task.Status,
		Spec:               requiredSpec("build"),
		SpecSnapshot:       "{}",
		ExecutionEnv:       "container",
		Workspace:          "/workspace/project",
		ContainerID:        "container-1",
		At:                 fixture.now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("收敛后必须允许重新验证: %v", err)
	}
}

// TestResidueKeepsLiveLeaseResidue 验证仍有有效执行权时不收敛任何残留。
func TestResidueKeepsLiveLeaseResidue(t *testing.T) {
	t.Parallel()
	fixture := newResidueFixture(t)
	task, step, attempt := fixture.seed(t)
	fixture.writeLiveRun(t, task, []domain.Step{step})

	if err := fixture.service(t).Start(fixture.ctx); err != nil {
		t.Fatalf("收敛残留失败: %v", err)
	}
	stored, err := fixture.db.GetAttempt(fixture.ctx, attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if stored.Status != domain.AttemptStatusRunning {
		t.Fatalf("有效执行权存在时不得收敛残留，实际 %s", stored.Status)
	}
}

// TestResidueConvergesResidueWithoutRun 验证没有执行记录的任务残留同样被收敛。
//
// 场景：进程在计划生成阶段（尚未创建 WorkflowRun）退出，Attempt / Session 已经
// 写入。这类残留同样没有任何执行者，必须被收敛。
func TestResidueConvergesResidueWithoutRun(t *testing.T) {
	t.Parallel()
	fixture := newResidueFixture(t)
	_, _, attempt := fixture.seed(t)

	if err := fixture.service(t).Start(fixture.ctx); err != nil {
		t.Fatalf("收敛残留失败: %v", err)
	}
	stored, err := fixture.db.GetAttempt(fixture.ctx, attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if stored.Status != domain.AttemptStatusInterrupted {
		t.Fatalf("没有执行记录的残留同样应被收敛，实际 %s", stored.Status)
	}
}

// TestResidueKeepsTerminalRecords 验证已终结的记录不被改写。
func TestResidueKeepsTerminalRecords(t *testing.T) {
	t.Parallel()
	fixture := newResidueFixture(t)
	task, step, attempt := fixture.seed(t)
	fixture.writeOrphanedRun(t, task, []domain.Step{step})

	// 尝试已经正常完成：收敛不得把它改写为 interrupted。
	completed := attempt
	if err := completed.Complete(fixture.now); err != nil {
		t.Fatalf("标记尝试完成失败: %v", err)
	}
	if err := fixture.db.UpdateAttemptIfStatus(
		fixture.ctx,
		completed,
		[]domain.AttemptStatus{domain.AttemptStatusRunning},
	); err != nil {
		t.Fatalf("写入尝试终态失败: %v", err)
	}

	if err := fixture.service(t).Start(fixture.ctx); err != nil {
		t.Fatalf("收敛残留失败: %v", err)
	}
	stored, err := fixture.db.GetAttempt(fixture.ctx, attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if stored.Status != domain.AttemptStatusCompleted {
		t.Fatalf("已终结的尝试不得被改写，实际 %s", stored.Status)
	}
}

// TestResidueIsIdempotent 验证重复收敛不会产生矛盾结果。
func TestResidueIsIdempotent(t *testing.T) {
	t.Parallel()
	fixture := newResidueFixture(t)
	task, step, attempt := fixture.seed(t)
	fixture.writeOrphanedRun(t, task, []domain.Step{step})

	for round := 0; round < 2; round++ {
		if err := fixture.service(t).Start(fixture.ctx); err != nil {
			t.Fatalf("第 %d 次收敛残留失败: %v", round+1, err)
		}
	}
	stored, err := fixture.db.GetAttempt(fixture.ctx, attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if stored.Status != domain.AttemptStatusInterrupted {
		t.Fatalf("重复收敛不得改变结果，实际 %s", stored.Status)
	}
}
