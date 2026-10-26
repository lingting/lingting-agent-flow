package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// feedbackQueryFixture 是验证失败反馈查询的现场。
//
// 两条独立的"任务 → 步骤 → 尝试 → 验证记录 → 反馈"链同时存在，用于证明查询会校验
// 完整归属，而不是只凭 runID 读取。
type feedbackQueryFixture struct {
	db        *sqlite.DB
	ctx       context.Context
	service   *app.ExecutionRecordService
	task      domain.Task
	other     domain.Task
	step      domain.Step
	otherStep domain.Step
	attempt   domain.Attempt
	run       domain.ValidationRun
	feedback  domain.ValidationFailureFeedback
}

// newFeedbackQueryFixture 在工作空间中准备两条互不相关的任务链。
func newFeedbackQueryFixture(t *testing.T) *feedbackQueryFixture {
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
	now := time.Now().UTC()
	role := mustFeedbackRole(t, db, ctx, now)
	fixture := &feedbackQueryFixture{db: db, ctx: ctx}
	fixture.task, fixture.step = mustFeedbackTask(t, db, ctx, role, now, "第一个任务")
	fixture.other, fixture.otherStep = mustFeedbackTask(t, db, ctx, role, now, "第二个任务")
	fixture.attempt = mustFeedbackAttempt(t, db, ctx, fixture.step, now)
	fixture.run = mustFeedbackRun(t, db, ctx, fixture.task, fixture.step, fixture.attempt, now)
	fixture.feedback = domain.ValidationFailureFeedback{
		TaskID:             fixture.task.ID,
		StepID:             fixture.step.ID,
		DeveloperAttemptID: fixture.attempt.ID,
		ValidationRunID:    fixture.run.ID,
		Verdict:            domain.VerificationVerdictFailed,
		Summary:            "必需检查未通过: build",
		Checks: []domain.ValidationCheckFeedback{
			{
				CheckID:       "build",
				CheckName:     "构建",
				Required:      true,
				Status:        domain.ValidationCheckStatusFailed,
				ExitCode:      1,
				FailureReason: "退出码 1 不在允许范围内",
				StderrSummary: "编译错误",
				StartedAt:     now,
				FinishedAt:    now,
			},
		},
		CreatedAt: now,
	}
	if err := db.CreateValidationFailureFeedback(ctx, fixture.feedback); err != nil {
		t.Fatalf("写入验证失败反馈失败: %v", err)
	}
	service, err := app.NewExecutionRecordService(db)
	if err != nil {
		t.Fatalf("构造执行记录服务失败: %v", err)
	}
	fixture.service = service
	return fixture
}

// mustFeedbackRole 写入一个开发角色。
func mustFeedbackRole(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		"反馈查询开发角色",
		domain.RoleTypeDeveloper,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// mustFeedbackTask 写入一条任务与它的第一个步骤。
func mustFeedbackTask(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	role domain.Role,
	now time.Time,
	title string,
) (domain.Task, domain.Step) {
	t.Helper()
	requirement, err := domain.NewRequirement(title, title+"正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	step, err := domain.NewStep(
		task.ID,
		1,
		"步骤标题",
		"步骤目标",
		"执行要求",
		role,
		domain.VerificationSpec{},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return task, step
}

// feedbackValidationSpec 构造一份只有一条必需检查的验证规格。
//
// 规格必须真实存在：验证记录的统计计数受"已执行检查数不超过总数"的约束，
// 空规格无法写入一条带失败计数的结论。
func feedbackValidationSpec() domain.ValidationSpec {
	return domain.ValidationSpec{Checks: []domain.ValidationCheckDefinition{
		{
			ID:       "build",
			Name:     "构建",
			Type:     domain.ValidationCheckTypeCommand,
			Enabled:  true,
			Required: true,
			Sequence: 1,
			Command:  "build",
		},
	}}
}

// mustFeedbackAttempt 为步骤创建一次执行尝试。
func mustFeedbackAttempt(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	step domain.Step,
	now time.Time,
) domain.Attempt {
	t.Helper()
	attempt, err := db.StartAttempt(ctx, storage.StartAttemptRequest{
		TaskID: step.TaskID,
		StepID: step.ID,
		At:     now,
	})
	if err != nil {
		t.Fatalf("开始执行尝试失败: %v", err)
	}
	return attempt
}

// mustFeedbackRun 创建一条已结束的失败验证记录。
func mustFeedbackRun(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	attempt domain.Attempt,
	now time.Time,
) domain.ValidationRun {
	t.Helper()
	run, err := db.BeginValidationRun(ctx, storage.ValidationRunStartRequest{
		TaskID:             task.ID,
		StepID:             step.ID,
		AttemptID:          attempt.ID,
		ExpectedTaskStatus: task.Status,
		Spec:               feedbackValidationSpec(),
		SpecSnapshot:       `{"checks":[]}`,
		ExecutionEnv:       "container",
		Workspace:          "/workspace/project",
		ContainerID:        "container-1",
		At:                 now,
	})
	if err != nil {
		t.Fatalf("创建验证记录失败: %v", err)
	}
	if err := db.FinishValidationRun(ctx, storage.ValidationRunFinishRequest{
		RunID:          run.ID,
		Status:         domain.ValidationRunStatusCompleted,
		Verdict:        domain.VerificationVerdictFailed,
		Summary:        "验证失败",
		FailureSummary: "必需检查未通过: build",
		FailedChecks:   1,
		At:             now,
	}); err != nil {
		t.Fatalf("写入验证结论失败: %v", err)
	}
	return run
}

// TestGetValidationFailureFeedbackReturnsPersistedFeedback 校验正常读取。
func TestGetValidationFailureFeedbackReturnsPersistedFeedback(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackQueryFixture(t)
	feedback, err := fixture.service.GetValidationFailureFeedback(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.step.ID.String(),
		fixture.run.ID.String(),
	)
	if err != nil {
		t.Fatalf("读取验证失败反馈失败: %v", err)
	}
	if feedback.ValidationRunID != fixture.run.ID {
		t.Fatalf("反馈未关联验证记录: %s", feedback.ValidationRunID)
	}
	if feedback.TaskID != fixture.task.ID || feedback.StepID != fixture.step.ID {
		t.Fatalf("反馈归属不符: %s / %s", feedback.TaskID, feedback.StepID)
	}
	if feedback.DeveloperAttemptID != fixture.attempt.ID {
		t.Fatalf("反馈未关联开发尝试: %s", feedback.DeveloperAttemptID)
	}
	if feedback.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("反馈结论不符: %s", feedback.Verdict)
	}
	if feedback.InfrastructureError {
		t.Fatal("命令失败不应被标记为基础设施异常")
	}
	if len(feedback.Checks) != 1 {
		t.Fatalf("反馈检查数量不符: %d", len(feedback.Checks))
	}
	check := feedback.Checks[0]
	if check.CheckID != "build" || check.ExitCode != 1 || !check.Required {
		t.Fatalf("检查条目内容不符: %+v", check)
	}
	if check.FailureReason != "退出码 1 不在允许范围内" || check.StderrSummary != "编译错误" {
		t.Fatalf("检查摘要不符: %+v", check)
	}
}

// TestGetValidationFailureFeedbackRejectsDeletedTask 校验逻辑删除的任务不可读反馈。
func TestGetValidationFailureFeedbackRejectsDeletedTask(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackQueryFixture(t)
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if err := task.CancelImmediately(time.Now().UTC()); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if err := fixture.db.UpdateTask(fixture.ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	if err := fixture.db.DeleteTask(
		fixture.ctx,
		fixture.task.ID,
		[]domain.TaskStatus{domain.TaskStatusCancelled},
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	_, err = fixture.service.GetValidationFailureFeedback(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.step.ID.String(),
		fixture.run.ID.String(),
	)
	if !errors.Is(err, app.ErrExecutionRecordNotFound) {
		t.Fatalf("已删除任务应返回 ErrExecutionRecordNotFound: %v", err)
	}
}

// TestGetValidationFailureFeedbackRejectsUnknownStep 校验不存在的步骤返回未找到。
func TestGetValidationFailureFeedbackRejectsUnknownStep(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackQueryFixture(t)
	_, err := fixture.service.GetValidationFailureFeedback(
		fixture.ctx,
		fixture.task.ID.String(),
		domain.NewID().String(),
		fixture.run.ID.String(),
	)
	if !errors.Is(err, app.ErrExecutionRecordNotFound) {
		t.Fatalf("不存在的步骤应返回 ErrExecutionRecordNotFound: %v", err)
	}
}

// TestGetValidationFailureFeedbackRejectsForeignStep 校验跨任务步骤被拒绝。
func TestGetValidationFailureFeedbackRejectsForeignStep(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackQueryFixture(t)
	_, err := fixture.service.GetValidationFailureFeedback(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.otherStep.ID.String(),
		fixture.run.ID.String(),
	)
	if !errors.Is(err, app.ErrExecutionRecordNotFound) {
		t.Fatalf("其他任务的步骤应返回 ErrExecutionRecordNotFound: %v", err)
	}
}

// TestGetValidationFailureFeedbackRejectsUnknownRun 校验不存在的验证记录。
func TestGetValidationFailureFeedbackRejectsUnknownRun(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackQueryFixture(t)
	_, err := fixture.service.GetValidationFailureFeedback(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.step.ID.String(),
		domain.NewID().String(),
	)
	if !errors.Is(err, app.ErrExecutionRecordNotFound) {
		t.Fatalf("不存在的验证记录应返回 ErrExecutionRecordNotFound: %v", err)
	}
}

// TestGetValidationFailureFeedbackRejectsForeignRun 校验跨任务与跨步骤的验证记录。
func TestGetValidationFailureFeedbackRejectsForeignRun(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackQueryFixture(t)
	now := time.Now().UTC()
	// 情形一：路径上的任务与步骤都属于本任务，只有验证记录属于另一条任务链。
	foreignRun := mustFeedbackRun(
		t,
		fixture.db,
		fixture.ctx,
		fixture.other,
		fixture.otherStep,
		mustFeedbackAttempt(t, fixture.db, fixture.ctx, fixture.otherStep, now),
		now,
	)
	_, err := fixture.service.GetValidationFailureFeedback(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.step.ID.String(),
		foreignRun.ID.String(),
	)
	if !errors.Is(err, app.ErrExecutionRecordNotFound) {
		t.Fatalf("其他任务的验证记录应返回 ErrExecutionRecordNotFound: %v", err)
	}
	// 情形二：路径上的步骤与验证记录属于同一条任务链，但任务标识是另一条任务的。
	_, err = fixture.service.GetValidationFailureFeedback(
		fixture.ctx,
		fixture.other.ID.String(),
		fixture.step.ID.String(),
		fixture.run.ID.String(),
	)
	if !errors.Is(err, app.ErrExecutionRecordNotFound) {
		t.Fatalf("跨任务路径应返回 ErrExecutionRecordNotFound: %v", err)
	}
}

// TestGetValidationFailureFeedbackRejectsRunWithoutFeedback 校验没有反馈时返回未找到。
//
// 没有反馈不等于验证通过：调用方必须读取验证记录的结论才能判断结果。
func TestGetValidationFailureFeedbackRejectsRunWithoutFeedback(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackQueryFixture(t)
	now := time.Now().UTC()
	attempt := mustFeedbackAttempt(t, fixture.db, fixture.ctx, fixture.step, now)
	run := mustFeedbackRun(t, fixture.db, fixture.ctx, fixture.task, fixture.step, attempt, now)
	_, err := fixture.service.GetValidationFailureFeedback(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.step.ID.String(),
		run.ID.String(),
	)
	if !errors.Is(err, app.ErrExecutionRecordNotFound) {
		t.Fatalf("没有反馈时应返回 ErrExecutionRecordNotFound: %v", err)
	}
}

// TestGetValidationFailureFeedbackRejectsInvalidIDs 校验非法路径标识按不存在处理。
func TestGetValidationFailureFeedbackRejectsInvalidIDs(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackQueryFixture(t)
	for name, target := range map[string][3]string{
		"空任务标识": {"", fixture.step.ID.String(), fixture.run.ID.String()},
		"空步骤标识": {fixture.task.ID.String(), "", fixture.run.ID.String()},
		"空验证标识": {fixture.task.ID.String(), fixture.step.ID.String(), ""},
		"含斜杠标识": {fixture.task.ID.String(), fixture.step.ID.String(), "a/b"},
	} {
		_, err := fixture.service.GetValidationFailureFeedback(
			fixture.ctx,
			target[0],
			target[1],
			target[2],
		)
		if !errors.Is(err, app.ErrExecutionRecordNotFound) {
			t.Fatalf("%s 应返回 ErrExecutionRecordNotFound: %v", name, err)
		}
	}
}

// TestGetValidationFailureFeedbackStorageFailureIsNotNotFound 校验存储错误不被伪装成未找到。
func TestGetValidationFailureFeedbackStorageFailureIsNotNotFound(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackQueryFixture(t)
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	_, err := fixture.service.GetValidationFailureFeedback(
		fixture.ctx,
		fixture.task.ID.String(),
		fixture.step.ID.String(),
		fixture.run.ID.String(),
	)
	if err == nil {
		t.Fatal("存储不可用时必须返回错误")
	}
	if errors.Is(err, app.ErrExecutionRecordNotFound) {
		t.Fatalf("存储错误不应被归类为未找到: %v", err)
	}
}

// TestExecutionRecordServiceRequiresStore 校验构造需要存储。
func TestExecutionRecordServiceRequiresStore(t *testing.T) {
	t.Parallel()
	if _, err := app.NewExecutionRecordService(nil); err == nil {
		t.Fatal("存储为空时构造应当失败")
	}
}
