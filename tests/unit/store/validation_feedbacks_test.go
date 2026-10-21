package sqlite_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// feedbackFixture 是一套可用于写入验证失败反馈的数据。
type feedbackFixture struct {
	built fixture
	run   domain.ValidationRun
}

// newFeedbackFixture 创建任务、步骤、尝试与一条已结束的验证记录。
func newFeedbackFixture(t *testing.T) (*sqlite.DB, feedbackFixture) {
	t.Helper()
	db := newTestDB(t)
	built := newFixture(t, db)
	run := beginValidation(t, db, built)
	finish := storage.ValidationRunFinishRequest{
		RunID:          run.ID,
		Status:         domain.ValidationRunStatusCompleted,
		Verdict:        domain.VerificationVerdictFailed,
		Summary:        "验证失败",
		FailureSummary: "必需检查未通过: build",
		FailedChecks:   1,
		At:             time.Now().UTC(),
	}
	if err := db.FinishValidationRun(testContext(t), finish); err != nil {
		t.Fatalf("写入验证结论失败: %v", err)
	}
	return db, feedbackFixture{built: built, run: run}
}

// newFeedback 构造一条验证失败反馈。
func newFeedback(
	built fixture,
	run domain.ValidationRun,
	summary string,
) domain.ValidationFailureFeedback {
	now := time.Now().UTC()
	return domain.ValidationFailureFeedback{
		TaskID:             built.task.ID,
		StepID:             built.step.ID,
		DeveloperAttemptID: built.attempt.ID,
		ValidationRunID:    run.ID,
		Verdict:            domain.VerificationVerdictFailed,
		Summary:            summary,
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
}

// TestCreateValidationFailureFeedback 验证反馈可以写入并按验证记录读取。
func TestCreateValidationFailureFeedback(t *testing.T) {
	t.Parallel()
	db, fixtureData := newFeedbackFixture(t)
	feedback := newFeedback(fixtureData.built, fixtureData.run, "需要修复编译错误")
	if err := db.CreateValidationFailureFeedback(testContext(t), feedback); err != nil {
		t.Fatalf("写入验证失败反馈失败: %v", err)
	}
	loaded, err := db.GetValidationFailureFeedback(testContext(t), fixtureData.run.ID)
	if err != nil {
		t.Fatalf("读取验证失败反馈失败: %v", err)
	}
	if loaded.DeveloperAttemptID != fixtureData.built.attempt.ID {
		t.Fatalf("反馈未关联开发尝试: %s", loaded.DeveloperAttemptID)
	}
	if loaded.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("反馈结论不符: %s", loaded.Verdict)
	}
	if loaded.Summary != "需要修复编译错误" {
		t.Fatalf("反馈摘要不符: %s", loaded.Summary)
	}
	if len(loaded.Checks) != 1 {
		t.Fatalf("反馈检查数量不符: %d", len(loaded.Checks))
	}
	if loaded.Checks[0].ExitCode != 1 {
		t.Fatalf("反馈退出码不符: %d", loaded.Checks[0].ExitCode)
	}
	if loaded.Checks[0].StderrSummary != "编译错误" {
		t.Fatalf("反馈输出摘要不符: %s", loaded.Checks[0].StderrSummary)
	}
	if loaded.CreatedAt.IsZero() {
		t.Fatalf("反馈创建时间不应为空")
	}
}

// TestValidationFailureFeedbackIsAppendOnly 验证同一轮验证不能重复写入反馈。
func TestValidationFailureFeedbackIsAppendOnly(t *testing.T) {
	t.Parallel()
	db, fixtureData := newFeedbackFixture(t)
	feedback := newFeedback(fixtureData.built, fixtureData.run, "第一次反馈")
	if err := db.CreateValidationFailureFeedback(testContext(t), feedback); err != nil {
		t.Fatalf("写入验证失败反馈失败: %v", err)
	}
	second := newFeedback(fixtureData.built, fixtureData.run, "第二次反馈")
	err := db.CreateValidationFailureFeedback(testContext(t), second)
	if err == nil {
		t.Fatalf("同一轮验证不应重复写入反馈")
	}
	if !errors.Is(err, storage.ErrConflict) && !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("错误类别不符: %v", err)
	}
	loaded, err := db.GetValidationFailureFeedback(testContext(t), fixtureData.run.ID)
	if err != nil {
		t.Fatalf("读取验证失败反馈失败: %v", err)
	}
	if loaded.Summary != "第一次反馈" {
		t.Fatalf("历史反馈不得被覆盖: %s", loaded.Summary)
	}
}

// TestLatestValidationFailureFeedbackByStep 验证按步骤取最近一次反馈。
func TestLatestValidationFailureFeedbackByStep(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	base := time.Now().UTC()
	var latest domain.ValidationRun
	for index := 0; index < 2; index++ {
		run, err := db.BeginValidationRun(testContext(t), storage.ValidationRunStartRequest{
			TaskID:             built.task.ID,
			StepID:             built.step.ID,
			AttemptID:          built.attempt.ID,
			ExpectedTaskStatus: built.task.Status,
			Spec:               validationSpec(),
			SpecSnapshot:       `{"checks":[]}`,
			ExecutionEnv:       "container",
			Workspace:          "/workspace/project",
			ContainerID:        "container-1",
			At:                 base.Add(time.Duration(index) * time.Minute),
		})
		if err != nil {
			t.Fatalf("创建验证记录失败: %v", err)
		}
		at := base.Add(time.Duration(index) * time.Minute)
		if err := db.FinishValidationRun(testContext(t), storage.ValidationRunFinishRequest{
			RunID:          run.ID,
			Status:         domain.ValidationRunStatusCompleted,
			Verdict:        domain.VerificationVerdictFailed,
			Summary:        "验证失败",
			FailureSummary: "必需检查未通过: build",
			FailedChecks:   1,
			At:             at,
		}); err != nil {
			t.Fatalf("写入验证结论失败: %v", err)
		}
		feedback := newFeedback(built, run, "第"+string(rune('1'+index))+"次反馈")
		feedback.CreatedAt = at
		if err := db.CreateValidationFailureFeedback(testContext(t), feedback); err != nil {
			t.Fatalf("写入验证失败反馈失败: %v", err)
		}
		latest = run
	}
	loaded, err := db.LatestValidationFailureFeedbackByStep(testContext(t), built.step.ID)
	if err != nil {
		t.Fatalf("读取最近一次反馈失败: %v", err)
	}
	if loaded.ValidationRunID != latest.ID {
		t.Fatalf("应返回最近一次反馈: %s", loaded.ValidationRunID)
	}
	if loaded.Summary != "第2次反馈" {
		t.Fatalf("最近一次反馈摘要不符: %s", loaded.Summary)
	}
}

// TestValidationFailureFeedbackNotFound 验证没有反馈时返回"不存在"。
func TestValidationFailureFeedbackNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	if _, err := db.LatestValidationFailureFeedbackByStep(
		testContext(t),
		built.step.ID,
	); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("没有反馈时应返回 ErrNotFound: %v", err)
	}
}
