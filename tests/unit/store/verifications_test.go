package sqlite_test

import (
	"errors"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// newVerificationInput 构造一个绑定到当前 fixture 的验证输入。
func newVerificationInput(
	built fixture,
	sessionID *domain.ID,
	passed bool,
) domain.VerificationInput {
	checks := []domain.VerificationCheck{
		{Name: "接口实现完整", Passed: passed, Detail: "已检查调用点"},
		{Name: "错误处理覆盖", Passed: true},
	}
	commands := []domain.CommandResult{
		{
			Name:     "go build",
			Command:  "go build ./...",
			Required: true,
			Certain:  true,
			ExitCode: 0,
			Stdout:   "构建成功",
			Duration: 1200 * time.Millisecond,
		},
		{
			Name:     "go test",
			Command:  "go test ./...",
			Required: true,
			Certain:  true,
			Duration: 3400 * time.Millisecond,
		},
	}
	if !passed {
		commands[1].ExitCode = 1
		commands[1].Stderr = "测试失败"
	}
	input := domain.VerificationInput{
		TaskID:             built.task.ID,
		StepID:             built.step.ID,
		AttemptID:          built.attempt.ID,
		ValidatorSessionID: sessionID,
		Summary:            "验证摘要",
		Checks:             checks,
		CommandResults:     commands,
	}
	if !passed {
		input.FailureSummary = "自动化测试失败"
	}
	return input
}

func TestCreateAndGetVerificationResult(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	result, err := domain.NewVerificationResult(newVerificationInput(built, nil, false), time.Now().UTC())
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	if result.Passed {
		t.Fatal("存在失败命令时结果不应通过")
	}
	if err := db.CreateVerificationResult(ctx, result); err != nil {
		t.Fatalf("写入验证结果失败: %v", err)
	}

	loaded, err := db.GetVerificationResult(ctx, result.ID)
	if err != nil {
		t.Fatalf("读取验证结果失败: %v", err)
	}
	if loaded.Passed {
		t.Fatal("通过标记未正确保存")
	}
	if loaded.TaskID != built.task.ID || loaded.StepID != built.step.ID {
		t.Fatalf("验证结果归属不符: %+v", loaded)
	}
	if loaded.AttemptID != built.attempt.ID {
		t.Fatalf("尝试归属不符: %s", loaded.AttemptID)
	}
	if len(loaded.Checks) != 2 {
		t.Fatalf("验收项数量不符: %d", len(loaded.Checks))
	}
	if loaded.Checks[0].Name != "接口实现完整" || loaded.Checks[0].Passed {
		t.Fatalf("验收项内容不符: %+v", loaded.Checks[0])
	}
	if loaded.Checks[0].Detail != "已检查调用点" {
		t.Fatalf("验收项详情不符: %q", loaded.Checks[0].Detail)
	}
	if len(loaded.CommandResults) != 2 {
		t.Fatalf("命令结果数量不符: %d", len(loaded.CommandResults))
	}
	if loaded.CommandResults[1].ExitCode != 1 || loaded.CommandResults[1].Stderr != "测试失败" {
		t.Fatalf("命令失败证据未保存: %+v", loaded.CommandResults[1])
	}
	if loaded.CommandResults[0].Duration != 1200*time.Millisecond {
		t.Fatalf("命令耗时未保存: %s", loaded.CommandResults[0].Duration)
	}
	if loaded.FailureSummary != "自动化测试失败" {
		t.Fatalf("失败摘要未保存: %q", loaded.FailureSummary)
	}
	if !loaded.CreatedAt.Equal(result.CreatedAt) {
		t.Fatalf("创建时间未按毫秒精度保存: %s", loaded.CreatedAt)
	}
}

func TestVerificationResultWithValidatorSession(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	stepID := built.step.ID
	attemptID := built.attempt.ID
	session := mustSession(t, db, domain.SessionScope{
		TaskID:    built.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, built.validator)

	result, err := domain.NewVerificationResult(
		newVerificationInput(built, &session.ID, true),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	if !result.Passed {
		t.Fatal("全部命令成功时结果应当通过")
	}
	if err := db.CreateVerificationResult(ctx, result); err != nil {
		t.Fatalf("写入验证结果失败: %v", err)
	}
	loaded, err := db.GetVerificationResult(ctx, result.ID)
	if err != nil {
		t.Fatalf("读取验证结果失败: %v", err)
	}
	if loaded.ValidatorSessionID == nil || *loaded.ValidatorSessionID != session.ID {
		t.Fatalf("验证会话未保存: %+v", loaded.ValidatorSessionID)
	}
	if !loaded.Passed {
		t.Fatal("通过标记未正确保存")
	}
}

func TestMultipleVerificationResultsKeepHistory(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)

	failed, err := domain.NewVerificationResult(
		newVerificationInput(built, nil, false),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	passed, err := domain.NewVerificationResult(
		newVerificationInput(built, nil, true),
		time.Now().UTC().Add(time.Second),
	)
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	if err := db.CreateVerificationResult(ctx, failed); err != nil {
		t.Fatalf("写入验证结果失败: %v", err)
	}
	if err := db.CreateVerificationResult(ctx, passed); err != nil {
		t.Fatalf("写入验证结果失败: %v", err)
	}

	results, err := db.ListVerificationResultsByAttempt(ctx, built.attempt.ID)
	if err != nil {
		t.Fatalf("查询验证结果失败: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("验证历史数量不符: %d", len(results))
	}
	if results[0].ID != failed.ID || results[1].ID != passed.ID {
		t.Fatalf("验证历史顺序不符: %+v", results)
	}
	if results[0].Passed || !results[1].Passed {
		t.Fatalf("验证历史内容被覆盖: %+v", results)
	}
}

func TestVerificationResultRejectsScopeMismatch(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	otherStep := mustStep(t, db, built.task, built.developer, 2)
	input := newVerificationInput(built, nil, true)
	input.StepID = otherStep.ID
	result, err := domain.NewVerificationResult(input, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	if err := db.CreateVerificationResult(ctx, result); !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("步骤与尝试不一致时应当被拒绝，实际: %v", err)
	}
}

func TestVerificationResultRequiresExistingAttempt(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	input := newVerificationInput(built, nil, true)
	input.AttemptID = domain.NewID()
	result, err := domain.NewVerificationResult(input, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	if err := db.CreateVerificationResult(testContext(t), result); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("引用不存在的尝试应当返回记录不存在，实际: %v", err)
	}
}

func TestVerificationFailureRequiresFailureSummary(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	input := newVerificationInput(built, nil, false)
	input.FailureSummary = ""
	result, err := domain.NewVerificationResult(input, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	if result.Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("必需检查失败时判定必须为 failed: %s", result.Verdict)
	}
	if result.FailureSummary == "" {
		t.Fatal("验证失败时必须给出失败摘要")
	}
}

func TestVerificationPassedConstraintRejectsInconsistentRow(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	result, err := domain.NewVerificationResult(newVerificationInput(built, nil, true), time.Now().UTC())
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	if err := db.CreateVerificationResult(ctx, result); err != nil {
		t.Fatalf("写入验证结果失败: %v", err)
	}
	err = testkit.ExecConstraint(
		t,
		rawDB(t, db),
		`UPDATE verification_results SET passed = 0, verdict = 'failed', failure_summary = NULL
		 WHERE id = ?`,
		result.ID.String(),
	)
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("失败的验证必须带失败摘要，实际: %v", err)
	}
}

func TestVerificationJSONColumnsRequireValidJSON(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	result, err := domain.NewVerificationResult(newVerificationInput(built, nil, true), time.Now().UTC())
	if err != nil {
		t.Fatalf("构造验证结果失败: %v", err)
	}
	if err := db.CreateVerificationResult(ctx, result); err != nil {
		t.Fatalf("写入验证结果失败: %v", err)
	}
	err = testkit.ExecConstraint(t, rawDB(t, db), `UPDATE verification_results SET checks_json = 'not-json' WHERE id = ?`,
		result.ID.String(),
	)
	if !errors.Is(err, storage.ErrInvalidData) {
		t.Fatalf("非法 JSON 应当被拒绝，实际: %v", err)
	}
}

func TestGetVerificationResultNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	_, err := db.GetVerificationResult(testContext(t), domain.NewID())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}
