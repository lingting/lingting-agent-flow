package sqlite_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// validationSpec 构造一份最小的验证配置。
func validationSpec() domain.ValidationSpec {
	return domain.ValidationSpec{
		Checks: []domain.ValidationCheckDefinition{
			{
				ID:       "build",
				Name:     "构建",
				Type:     domain.ValidationCheckTypeCommand,
				Enabled:  true,
				Required: true,
				Sequence: 1,
				Command:  "go",
				Args:     []string{"build", "./..."},
			},
			{
				ID:       "lint",
				Name:     "静态检查",
				Type:     domain.ValidationCheckTypeCommand,
				Enabled:  true,
				Required: false,
				Sequence: 2,
				Command:  "go",
				Args:     []string{"vet", "./..."},
			},
		},
	}
}

// beginValidation 在 fixture 上创建一条运行中的验证记录。
func beginValidation(
	t *testing.T,
	db *sqlite.DB,
	built fixture,
) domain.ValidationRun {
	t.Helper()
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
		At:                 time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("创建验证记录失败: %v", err)
	}
	return run
}

// TestBeginValidationRunCreatesRunningRecord 验证创建验证记录即进入运行状态。
func TestBeginValidationRunCreatesRunningRecord(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	run := beginValidation(t, db, built)
	if run.Status != domain.ValidationRunStatusRunning {
		t.Fatalf("验证状态不符: %s", run.Status)
	}
	if run.Verdict != domain.VerificationVerdictNone {
		t.Fatalf("未完成的验证不应有结论: %s", run.Verdict)
	}
	if run.TotalChecks != 2 {
		t.Fatalf("检查总数不符: %d", run.TotalChecks)
	}
	loaded, err := db.GetValidationRun(testContext(t), run.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if loaded.ExecutionEnv != "container" || loaded.Workspace != "/workspace/project" {
		t.Fatalf("执行环境未保存: %+v", loaded)
	}
	if loaded.ContainerID != "container-1" {
		t.Fatalf("容器关联未保存: %q", loaded.ContainerID)
	}
}

// TestBeginValidationRunRejectsConcurrentStart 验证同一尝试不能被重复占用。
func TestBeginValidationRunRejectsConcurrentStart(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	beginValidation(t, db, built)
	_, err := db.BeginValidationRun(testContext(t), storage.ValidationRunStartRequest{
		TaskID:             built.task.ID,
		StepID:             built.step.ID,
		AttemptID:          built.attempt.ID,
		ExpectedTaskStatus: domain.TaskStatusDeveloping,
		Spec:               validationSpec(),
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复开始验证应返回冲突，实际 %v", err)
	}
}

// TestBeginValidationRunRejectsStaleTaskStatus 验证任务状态变更后拒绝开始验证。
func TestBeginValidationRunRejectsStaleTaskStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	_, err := db.BeginValidationRun(testContext(t), storage.ValidationRunStartRequest{
		TaskID:             built.task.ID,
		StepID:             built.step.ID,
		AttemptID:          built.attempt.ID,
		ExpectedTaskStatus: domain.TaskStatusVerifying,
		Spec:               validationSpec(),
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("任务状态不符时应返回冲突，实际 %v", err)
	}
}

// TestBeginValidationRunRejectsForeignAttempt 验证跨步骤的尝试被拒绝。
func TestBeginValidationRunRejectsForeignAttempt(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	other := newFixture(t, db)
	_, err := db.BeginValidationRun(testContext(t), storage.ValidationRunStartRequest{
		TaskID:             built.task.ID,
		StepID:             built.step.ID,
		AttemptID:          other.attempt.ID,
		ExpectedTaskStatus: built.task.Status,
		Spec:               validationSpec(),
	})
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("尝试不属于步骤时应返回数据非法，实际 %v", err)
	}
}

// TestAppendValidationCheckResultsAndFinish 验证逐项结果与结论写入。
func TestAppendValidationCheckResultsAndFinish(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	run := beginValidation(t, db, built)
	ctx := testContext(t)
	spec := validationSpec()
	results := []domain.ValidationCheckResult{
		newCheckResult(run, spec.Checks[0], domain.ValidationCheckStatusPassed, 0),
		newCheckResult(run, spec.Checks[1], domain.ValidationCheckStatusFailed, 1),
	}
	for _, result := range results {
		if err := db.AppendValidationCheckResult(ctx, result); err != nil {
			t.Fatalf("写入检查结果失败: %v", err)
		}
	}
	loaded, err := db.ListValidationCheckResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("读取检查结果失败: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("检查结果数量不符: %d", len(loaded))
	}
	if loaded[0].CheckID != "build" || loaded[1].CheckID != "lint" {
		t.Fatalf("检查结果顺序不符: %s, %s", loaded[0].CheckID, loaded[1].CheckID)
	}
	if loaded[1].Status != domain.ValidationCheckStatusFailed || loaded[1].ExitCode != 1 {
		t.Fatalf("失败结果未保存: %+v", loaded[1])
	}
	if loaded[0].Definition.Command != "go" {
		t.Fatalf("检查配置快照未保存: %+v", loaded[0].Definition)
	}
	if err := db.FinishValidationRun(ctx, storage.ValidationRunFinishRequest{
		RunID:          run.ID,
		Status:         domain.ValidationRunStatusCompleted,
		Verdict:        domain.VerificationVerdictFailed,
		Summary:        "验证失败: 可选检查未通过",
		FailureSummary: "可选检查未通过",
		PassedChecks:   1,
		FailedChecks:   1,
		Duration:       2 * time.Second,
	}); err != nil {
		t.Fatalf("写入验证结论失败: %v", err)
	}
	finished, err := db.GetValidationRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if finished.Status != domain.ValidationRunStatusCompleted {
		t.Fatalf("验证状态不符: %s", finished.Status)
	}
	if finished.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("验证结论不符: %s", finished.Verdict)
	}
	if finished.FinishedAt == nil {
		t.Fatal("已结束的验证必须记录结束时间")
	}
	if finished.PassedChecks != 1 || finished.FailedChecks != 1 {
		t.Fatalf("统计计数不符: %+v", finished)
	}
	if finished.Duration != 2*time.Second {
		t.Fatalf("耗时未保存: %s", finished.Duration)
	}
}

// TestFinishValidationRunRejectsLateResult 验证迟到的结论不能覆盖已确认终态。
func TestFinishValidationRunRejectsLateResult(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	run := beginValidation(t, db, built)
	ctx := testContext(t)
	request := storage.ValidationRunFinishRequest{
		RunID:        run.ID,
		Status:       domain.ValidationRunStatusCompleted,
		Verdict:      domain.VerificationVerdictPassed,
		Summary:      "通过",
		PassedChecks: 2,
		Duration:     time.Second,
	}
	if err := db.FinishValidationRun(ctx, request); err != nil {
		t.Fatalf("首次写入结论失败: %v", err)
	}
	request.Verdict = domain.VerificationVerdictFailed
	request.FailureSummary = "迟到的失败结论"
	err := db.FinishValidationRun(ctx, request)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复写入结论应返回冲突，实际 %v", err)
	}
	loaded, err := db.GetValidationRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if loaded.Verdict != domain.VerificationVerdictPassed {
		t.Fatalf("已确认的结论被覆盖: %s", loaded.Verdict)
	}
}

// TestFinishValidationRunRequiresFailureSummary 验证未通过的结论必须给出原因。
func TestFinishValidationRunRequiresFailureSummary(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	run := beginValidation(t, db, built)
	err := db.FinishValidationRun(testContext(t), storage.ValidationRunFinishRequest{
		RunID:   run.ID,
		Status:  domain.ValidationRunStatusCompleted,
		Verdict: domain.VerificationVerdictFailed,
	})
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("缺少失败原因时应返回数据非法，实际 %v", err)
	}
}

// TestFailValidationRun 验证失败终态写入。
func TestFailValidationRun(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	run := beginValidation(t, db, built)
	ctx := testContext(t)
	if err := db.FailValidationRun(
		ctx,
		run.ID,
		domain.ValidationRunStatusCancelled,
		"调用方取消验证",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("写入失败终态失败: %v", err)
	}
	loaded, err := db.GetValidationRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if loaded.Status != domain.ValidationRunStatusCancelled {
		t.Fatalf("验证状态不符: %s", loaded.Status)
	}
	if loaded.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("被取消的验证结论必须是失败: %s", loaded.Verdict)
	}
	// 重复写入终态不会成功：已结束的记录不再接受新的终态。
	err = db.FailValidationRun(
		ctx,
		run.ID,
		domain.ValidationRunStatusFailed,
		"再次写入",
		time.Now().UTC(),
	)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("已结束的验证不应接受新的终态，实际 %v", err)
	}
}

// TestValidationRunKeepsUnknownStatus 验证未知状态取值可以写入并原样读回。
func TestValidationRunKeepsUnknownStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	run := beginValidation(t, db, built)
	conn := rawDB(t, db)
	testkit.MustExec(
		t,
		conn,
		`UPDATE validation_runs SET status = 'quarantined_v2' WHERE id = ?`,
		run.ID.String(),
	)
	loaded, err := db.GetValidationRun(testContext(t), run.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if loaded.Status != domain.ValidationRunStatus("quarantined_v2") {
		t.Fatalf("未知状态未原样保留: %s", loaded.Status)
	}
}

// TestListValidationRunsByAttemptKeepsHistory 验证同一尝试的多次验证都保留。
func TestListValidationRunsByAttemptKeepsHistory(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	ctx := testContext(t)
	first := beginValidation(t, db, built)
	if err := db.FailValidationRun(
		ctx,
		first.ID,
		domain.ValidationRunStatusCancelled,
		"第一次验证被取消",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("写入失败终态失败: %v", err)
	}
	second := beginValidation(t, db, built)
	if first.ID == second.ID {
		t.Fatal("重试验证必须创建新的记录")
	}
	runs, err := db.ListValidationRunsByAttempt(ctx, built.attempt.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("历史验证记录数量不符: %d", len(runs))
	}
}

// newCheckResult 构造一条检查结果。
func newCheckResult(
	run domain.ValidationRun,
	definition domain.ValidationCheckDefinition,
	status domain.ValidationCheckStatus,
	exitCode int,
) domain.ValidationCheckResult {
	now := time.Now().UTC()
	return domain.ValidationCheckResult{
		ID:              domain.NewID(),
		ValidationRunID: run.ID,
		CheckID:         definition.ID,
		CheckName:       definition.Name,
		Required:        definition.IsRequired(),
		Sequence:        definition.Sequence,
		Definition:      definition,
		Status:          status,
		ExitCode:        exitCode,
		Certain:         true,
		StartedAt:       now,
		FinishedAt:      now,
		CreatedAt:       now,
	}
}
