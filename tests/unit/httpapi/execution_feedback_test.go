package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// feedbackAPIFixture 是验证失败反馈接口的测试环境。
//
// 两条独立的任务链同时存在，用于验证接口不会跨任务泄露验证记录与反馈。
type feedbackAPIFixture struct {
	handler   http.Handler
	db        *sqlite.DB
	ctx       context.Context
	developer domain.Role
	task      domain.Task
	step      domain.Step
	attempt   domain.Attempt
	run       domain.ValidationRun
	otherTask domain.Task
	otherStep domain.Step
	otherRun  domain.ValidationRun
}

// newFeedbackAPIFixture 准备两条任务链与各自的验证反馈，并构造处理器。
func newFeedbackAPIFixture(t *testing.T) *feedbackAPIFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	now := time.Now().UTC()
	fixture := &feedbackAPIFixture{db: db, ctx: ctx}
	fixture.developer = apiRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	fixture.task = apiTaskWithStep(t, db, ctx, now, fixture.developer, &fixture.step, 1)
	fixture.otherTask = apiTaskWithStep(t, db, ctx, now, fixture.developer, &fixture.otherStep, 1)

	fixture.attempt = apiAttempt(t, db, ctx, fixture.step, now)
	fixture.run = apiFailedValidationRun(t, db, ctx, fixture.task, fixture.step, fixture.attempt, now)
	apiFeedback(t, db, ctx, fixture.task, fixture.step, fixture.attempt, fixture.run, now, "第一次验证失败")

	otherAttempt := apiAttempt(t, db, ctx, fixture.otherStep, now)
	fixture.otherRun = apiFailedValidationRun(
		t,
		db,
		ctx,
		fixture.otherTask,
		fixture.otherStep,
		otherAttempt,
		now,
	)
	apiFeedback(
		t,
		db,
		ctx,
		fixture.otherTask,
		fixture.otherStep,
		otherAttempt,
		fixture.otherRun,
		now,
		"另一个任务的验证失败",
	)

	records, err := app.NewExecutionRecordService(db)
	if err != nil {
		t.Fatalf("构造执行记录服务失败: %v", err)
	}
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	fixture.handler = httpapi.NewHandler(httpapi.Deps{
		Health:           app.NewHealthService(db),
		SystemInfo:       func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:            tasks,
		ExecutionRecords: records,
	})
	return fixture
}

// apiAttempt 为步骤创建一次执行尝试。
func apiAttempt(
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

// apiValidationSpec 构造一份只有一条必需检查的验证规格。
func apiValidationSpec() domain.ValidationSpec {
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
		{
			ID:       "lint",
			Name:     "静态检查",
			Type:     domain.ValidationCheckTypeCommand,
			Enabled:  true,
			Required: false,
			Sequence: 2,
			Command:  "lint",
		},
	}}
}

// apiFailedValidationRun 创建一条已结束的失败验证记录。
func apiFailedValidationRun(
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
		Spec:               apiValidationSpec(),
		SpecSnapshot:       `{"checks":["build","lint"]}`,
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
		Summary:        "验证未通过",
		FailureSummary: "必需检查未通过: build",
		FailedChecks:   1,
		At:             now,
	}); err != nil {
		t.Fatalf("写入验证结论失败: %v", err)
	}
	return run
}

// apiFeedback 写入一条包含多条失败检查的验证失败反馈。
func apiFeedback(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	attempt domain.Attempt,
	run domain.ValidationRun,
	now time.Time,
	summary string,
) {
	t.Helper()
	feedback := domain.ValidationFailureFeedback{
		TaskID:              task.ID,
		StepID:              step.ID,
		DeveloperAttemptID:  attempt.ID,
		ValidationRunID:     run.ID,
		DeveloperSessionIDs: []domain.ID{"session-1"},
		Verdict:             domain.VerificationVerdictFailed,
		Summary:             summary,
		Checks: []domain.ValidationCheckFeedback{
			{
				CheckID:       "build",
				CheckName:     "构建",
				Required:      true,
				Status:        domain.ValidationCheckStatusFailed,
				ExitCode:      1,
				FailureReason: "退出码 1 不在允许范围内",
				StderrSummary: "编译错误: main.go:10",
				StdoutSummary: "构建开始",
				StartedAt:     now,
				FinishedAt:    now,
			},
			{
				CheckID:       "lint",
				CheckName:     "静态检查",
				Required:      false,
				Status:        domain.ValidationCheckStatusTimedOut,
				ExitCode:      -1,
				FailureReason: "检查超时",
				TimedOut:      true,
				StartedAt:     now,
				FinishedAt:    now,
			},
		},
		CreatedAt: now,
	}
	if err := db.CreateValidationFailureFeedback(ctx, feedback); err != nil {
		t.Fatalf("写入验证失败反馈失败: %v", err)
	}
}

// feedbackPath 返回指定验证运行的失败反馈路径。
func (f *feedbackAPIFixture) feedbackPath(taskID, stepID, runID string) string {
	return httpapi.PathTasks + "/" + taskID + "/steps/" + stepID +
		"/validations/" + runID + "/feedback"
}

// ownPath 返回本任务本步骤指定验证运行的反馈路径。
func (f *feedbackAPIFixture) ownPath(runID string) string {
	return f.feedbackPath(f.task.ID.String(), f.step.ID.String(), runID)
}

// get 发送 GET 请求。
func (f *feedbackAPIFixture) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

// TestValidationFeedbackReturnsStructuredChecks 校验成功返回结构化反馈与全部检查字段。
func TestValidationFeedbackReturnsStructuredChecks(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	recorder := fixture.get(t, fixture.ownPath(fixture.run.ID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("反馈查询应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ValidationFailureFeedbackResponse
	decodeBody(t, recorder, &response)
	if response.TaskID != fixture.task.ID.String() {
		t.Fatalf("任务归属不符: %s", response.TaskID)
	}
	if response.StepID != fixture.step.ID.String() {
		t.Fatalf("步骤归属不符: %s", response.StepID)
	}
	if response.AttemptID != fixture.attempt.ID.String() {
		t.Fatalf("开发尝试归属不符: %s", response.AttemptID)
	}
	if response.ValidationRunID != fixture.run.ID.String() {
		t.Fatalf("验证记录归属不符: %s", response.ValidationRunID)
	}
	if response.Verdict != string(domain.VerificationVerdictFailed) {
		t.Fatalf("反馈结论不符: %s", response.Verdict)
	}
	if response.Summary != "第一次验证失败" {
		t.Fatalf("反馈摘要不符: %s", response.Summary)
	}
	if response.InfrastructureError {
		t.Fatal("命令失败不应被标记为基础设施异常")
	}
	// 开发会话标识在领域对象中存在，但当前存储层没有对应列（迁移 0017 未包含该列），
	// 因此读取回来的一律为空。接口如实返回空列表，不补造数据。
	if len(response.DeveloperSessionIDs) != 0 {
		t.Fatalf("开发会话列表不应被补造: %+v", response.DeveloperSessionIDs)
	}
	if response.CreatedAt.IsZero() {
		t.Fatal("反馈创建时间不应为空")
	}
	if len(response.Checks) != 2 {
		t.Fatalf("反馈检查数量不符: %d", len(response.Checks))
	}
	build := response.Checks[0]
	if build.CheckID != "build" || build.CheckName != "构建" || !build.Required {
		t.Fatalf("必需检查条目不符: %+v", build)
	}
	if build.Status != string(domain.ValidationCheckStatusFailed) || build.ExitCode != 1 {
		t.Fatalf("必需检查状态与退出码不符: %+v", build)
	}
	if build.FailureReason != "退出码 1 不在允许范围内" {
		t.Fatalf("失败原因不符: %s", build.FailureReason)
	}
	if build.StderrSummary != "编译错误: main.go:10" || build.StdoutSummary != "构建开始" {
		t.Fatalf("输出摘要不符: %+v", build)
	}
	if build.TimedOut || build.Cancelled || build.InfrastructureError {
		t.Fatalf("失败检查不应带超时、取消或基础设施标记: %+v", build)
	}
	lint := response.Checks[1]
	if lint.CheckID != "lint" || lint.Required {
		t.Fatalf("可选检查条目不符: %+v", lint)
	}
	if !lint.TimedOut || lint.Status != string(domain.ValidationCheckStatusTimedOut) {
		t.Fatalf("超时检查标记不符: %+v", lint)
	}
	if lint.ExitCode != -1 {
		t.Fatalf("超时检查退出码应为 -1，实际 %d", lint.ExitCode)
	}
	if build.StartedAt.IsZero() || build.FinishedAt.IsZero() {
		t.Fatal("检查执行时间不应为空")
	}
}

// TestValidationFeedbackRejectsForeignScope 校验跨任务、跨步骤与跨验证运行都被拒绝。
func TestValidationFeedbackRejectsForeignScope(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	targets := map[string]string{
		"其他任务的验证记录": fixture.feedbackPath(
			fixture.task.ID.String(),
			fixture.step.ID.String(),
			fixture.otherRun.ID.String(),
		),
		"其他任务的步骤": fixture.feedbackPath(
			fixture.task.ID.String(),
			fixture.otherStep.ID.String(),
			fixture.run.ID.String(),
		),
		"其他任务路径": fixture.feedbackPath(
			fixture.otherTask.ID.String(),
			fixture.step.ID.String(),
			fixture.run.ID.String(),
		),
	}
	for name, target := range targets {
		recorder := fixture.get(t, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
		var response httpapi.ErrorResponse
		decodeBody(t, recorder, &response)
		if response.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码不符: %s", name, response.Error.Code)
		}
		if response.Error.Message == "" {
			t.Fatalf("%s 应给出可读说明", name)
		}
	}
}

// TestValidationFeedbackRejectsMissingRecords 校验不存在的任务、步骤与验证记录返回 404。
func TestValidationFeedbackRejectsMissingRecords(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	missing := domain.NewID().String()
	targets := map[string]string{
		"任务不存在": fixture.feedbackPath(
			missing,
			fixture.step.ID.String(),
			fixture.run.ID.String(),
		),
		"步骤不存在": fixture.feedbackPath(
			fixture.task.ID.String(),
			missing,
			fixture.run.ID.String(),
		),
		"验证记录不存在": fixture.feedbackPath(
			fixture.task.ID.String(),
			fixture.step.ID.String(),
			missing,
		),
	}
	for name, target := range targets {
		recorder := fixture.get(t, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

// TestValidationFeedbackRejectsRunWithoutFeedback 校验没有反馈时返回 404 而不是空对象。
func TestValidationFeedbackRejectsRunWithoutFeedback(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	now := time.Now().UTC()
	attempt := apiAttempt(t, fixture.db, fixture.ctx, fixture.step, now)
	run := apiFailedValidationRun(t, fixture.db, fixture.ctx, fixture.task, fixture.step, attempt, now)
	recorder := fixture.get(t, fixture.ownPath(run.ID.String()))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("没有反馈时应返回 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	decodeBody(t, recorder, &response)
	if response.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
}

// TestValidationFeedbackRejectsUnsupportedMethods 校验只读接口拒绝写方法。
func TestValidationFeedbackRejectsUnsupportedMethods(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	target := fixture.ownPath(fixture.run.ID.String())
	for _, method := range []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	} {
		request := httptest.NewRequest(method, target, nil)
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 应返回 405，实际 %d: %s", method, recorder.Code, recorder.Body.String())
		}
		if allow := recorder.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Fatalf("%s 的 Allow 头不符: %q", method, allow)
		}
		var response httpapi.ErrorResponse
		decodeBody(t, recorder, &response)
		if response.Error.Code != httpapi.CodeMethodNotAllowed {
			t.Fatalf("%s 错误代码不符: %s", method, response.Error.Code)
		}
	}
}

// TestValidationFeedbackRejectsUnknownShapes 校验不符合形状的路径返回 JSON 404。
func TestValidationFeedbackRejectsUnknownShapes(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	base := httpapi.PathTasks + "/" + fixture.task.ID.String() + "/steps/" +
		fixture.step.ID.String() + "/validations/" + fixture.run.ID.String()
	targets := map[string]string{
		"缺少反馈段": base,
		"多余路径段": base + "/feedback/extra",
		"未知动作段": base + "/unknown",
		"验证段拼错": httpapi.PathTasks + "/" + fixture.task.ID.String() + "/steps/" +
			fixture.step.ID.String() + "/validation/" + fixture.run.ID.String() + "/feedback",
		// 步骤级"最近一次反馈"入口是有意不提供的：该语义已由验证历史列表覆盖，
		// 新增第二个入口会让同一语义出现两条路径。
		"步骤级最近反馈": httpapi.PathTasks + "/" + fixture.task.ID.String() + "/steps/" +
			fixture.step.ID.String() + "/validation-feedback/latest",
	}
	for name, target := range targets {
		recorder := fixture.get(t, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
		var response httpapi.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s 应返回 JSON 错误: %s", name, recorder.Body.String())
		}
		if response.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码不符: %s", name, response.Error.Code)
		}
	}
}

// TestValidationFeedbackRequiresService 校验依赖缺失时返回 503 而不是 404 或空反馈。
func TestValidationFeedbackRequiresService(t *testing.T) {
	t.Parallel()
	handler := httpapi.NewHandler(httpapi.Deps{})
	target := httpapi.PathTasks + "/" + domain.NewID().String() + "/steps/" +
		domain.NewID().String() + "/validations/" + domain.NewID().String() + "/feedback"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("依赖缺失应返回 503，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestValidationFeedbackStorageFailureIsNotNotFound 校验存储错误映射为 500 且不泄露内部细节。
func TestValidationFeedbackStorageFailureIsNotNotFound(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	recorder := fixture.get(t, fixture.ownPath(fixture.run.ID.String()))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("存储错误应返回 500，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	decodeBody(t, recorder, &response)
	if response.Error.Code != httpapi.CodeInternal {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
	if response.Error.Message != "服务内部错误" {
		t.Fatalf("内部错误应脱敏，实际 %q", response.Error.Message)
	}
}

// TestValidationFeedbackQueryIsReadOnly 校验查询不创建反馈、不推进验证状态。
func TestValidationFeedbackQueryIsReadOnly(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	before, err := fixture.db.GetValidationRun(fixture.ctx, fixture.run.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	first := fixture.get(t, fixture.ownPath(fixture.run.ID.String()))
	if first.Code != http.StatusOK {
		t.Fatalf("反馈查询应返回 200，实际 %d: %s", first.Code, first.Body.String())
	}
	second := fixture.get(t, fixture.ownPath(fixture.run.ID.String()))
	if second.Code != http.StatusOK {
		t.Fatalf("重复查询应返回 200，实际 %d: %s", second.Code, second.Body.String())
	}
	after, err := fixture.db.GetValidationRun(fixture.ctx, fixture.run.ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if after.Status != before.Status || after.Verdict != before.Verdict {
		t.Fatalf(
			"查询不应改动验证记录: 之前 %s/%s，之后 %s/%s",
			before.Status,
			before.Verdict,
			after.Status,
			after.Verdict,
		)
	}
	// 反馈仍然只有一条：查询不会追加新的反馈记录。
	feedback, err := fixture.db.GetValidationFailureFeedback(fixture.ctx, fixture.run.ID)
	if err != nil {
		t.Fatalf("读取反馈失败: %v", err)
	}
	if feedback.Summary != "第一次验证失败" {
		t.Fatalf("反馈被查询改写: %s", feedback.Summary)
	}
}

// TestValidationFeedbackKeepsRunsIndependent 校验同一步骤的不同验证运行不会串用反馈。
func TestValidationFeedbackKeepsRunsIndependent(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	now := time.Now().UTC().Add(time.Minute)
	secondAttempt := apiAttempt(t, fixture.db, fixture.ctx, fixture.step, now)
	secondRun := apiFailedValidationRun(
		t,
		fixture.db,
		fixture.ctx,
		fixture.task,
		fixture.step,
		secondAttempt,
		now,
	)
	apiFeedback(
		t,
		fixture.db,
		fixture.ctx,
		fixture.task,
		fixture.step,
		secondAttempt,
		secondRun,
		now,
		"第二次验证失败",
	)
	first := fixture.get(t, fixture.ownPath(fixture.run.ID.String()))
	if first.Code != http.StatusOK {
		t.Fatalf("第一次反馈应返回 200，实际 %d: %s", first.Code, first.Body.String())
	}
	var firstResponse httpapi.ValidationFailureFeedbackResponse
	decodeBody(t, first, &firstResponse)
	if firstResponse.ValidationRunID != fixture.run.ID.String() {
		t.Fatalf("第一次反馈关联了错误的验证记录: %s", firstResponse.ValidationRunID)
	}
	if firstResponse.Summary != "第一次验证失败" {
		t.Fatalf("第一次反馈摘要不符: %s", firstResponse.Summary)
	}
	second := fixture.get(t, fixture.ownPath(secondRun.ID.String()))
	if second.Code != http.StatusOK {
		t.Fatalf("第二次反馈应返回 200，实际 %d: %s", second.Code, second.Body.String())
	}
	var secondResponse httpapi.ValidationFailureFeedbackResponse
	decodeBody(t, second, &secondResponse)
	if secondResponse.ValidationRunID != secondRun.ID.String() {
		t.Fatalf("第二次反馈关联了错误的验证记录: %s", secondResponse.ValidationRunID)
	}
	if secondResponse.Summary != "第二次验证失败" {
		t.Fatalf("第二次反馈摘要不符: %s", secondResponse.Summary)
	}
	if secondResponse.AttemptID != secondAttempt.ID.String() {
		t.Fatalf("第二次反馈的开发尝试不符: %s", secondResponse.AttemptID)
	}
}

// TestValidationFeedbackOmitsUndefinedFields 校验响应不输出领域对象未定义的字段。
func TestValidationFeedbackOmitsUndefinedFields(t *testing.T) {
	t.Parallel()
	fixture := newFeedbackAPIFixture(t)
	recorder := fixture.get(t, fixture.ownPath(fixture.run.ID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("反馈查询应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	decodeBody(t, recorder, &payload)
	for _, forbidden := range []string{
		"spec",
		"spec_snapshot",
		"stdout_path",
		"stderr_path",
		"checks_json",
		"validation_spec",
		"prompt",
	} {
		if _, exists := payload[forbidden]; exists {
			t.Fatalf("响应不应包含字段 %q: %+v", forbidden, payload)
		}
	}
	checks, ok := payload["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("响应应包含检查数组: %+v", payload["checks"])
	}
	first, ok := checks[0].(map[string]any)
	if !ok {
		t.Fatalf("检查条目形状不符: %+v", checks[0])
	}
	for _, forbidden := range []string{"stdout_path", "stderr_path", "sequence", "duration_ms"} {
		if _, exists := first[forbidden]; exists {
			t.Fatalf("检查条目不应包含字段 %q: %+v", forbidden, first)
		}
	}
}

// TestValidationFeedbackErrorIsCategoryBased 校验错误映射来自错误类别而非文本匹配。
func TestValidationFeedbackErrorIsCategoryBased(t *testing.T) {
	t.Parallel()
	if errors.Is(app.ErrExecutionRecordNotFound, app.ErrExecutionRecordInvalidInput) {
		t.Fatal("执行记录错误类别不应互相包含")
	}
	if errors.Is(storage.ErrNotFound, app.ErrExecutionRecordNotFound) {
		t.Fatal("存储错误与接口错误类别应相互独立")
	}
}
