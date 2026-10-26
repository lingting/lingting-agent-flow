package httpserver_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/httpserver"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// feedbackServerFixture 是验证失败反馈接口的真实服务端现场。
type feedbackServerFixture struct {
	baseURL string
	store   *sqlite.DB
	// first 与 second 是两条互不相关的任务链。
	first  feedbackChain
	second feedbackChain
}

// feedbackChain 是一条完整的"任务 → 步骤 → 尝试 → 验证记录 → 反馈"现场。
type feedbackChain struct {
	task     domain.Task
	step     domain.Step
	attempt  domain.Attempt
	run      domain.ValidationRun
	feedback domain.ValidationFailureFeedback
}

// startFeedbackServer 打开真实数据库、写入两条任务链并启动真实 HTTP 服务。
//
// 依赖注入走正式路径：执行记录服务由真实存储构造，再经 httpserver.Options 传入，
// 因此这里验证的是"路由 + 依赖装配 + 应用层 + 真实 SQLite"的完整链路。
func startFeedbackServer(t *testing.T) *feedbackServerFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	workspace.Cleanup(store.Close)
	now := time.Now().UTC()
	role := feedbackServerRole(t, store, ctx, now)
	fixture := &feedbackServerFixture{store: store}
	fixture.first = feedbackServerChain(t, store, ctx, role, now, "第一个任务", "第一次验证失败")
	fixture.second = feedbackServerChain(t, store, ctx, role, now, "第二个任务", "另一个任务的验证失败")

	records, err := app.NewExecutionRecordService(store)
	if err != nil {
		t.Fatalf("构造执行记录服务失败: %v", err)
	}
	// 执行记录路由挂在任务路径之下，任务处理器在判定路由前要求任务服务可用：
	// 真实装配同样会注入它，因此这里必须与生产装配保持一致。
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: store})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	server, err := httpserver.New(httpserver.Options{
		Address:          "127.0.0.1:0",
		Files:            roleWebFiles(),
		Health:           app.NewHealthService(store),
		SystemInfo:       func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:            tasks,
		ExecutionRecords: records,
	})
	if err != nil {
		t.Fatalf("构造 HTTP 服务失败: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听随机端口失败: %v", err)
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(runCtx, listener)
	}()
	workspace.Cleanup(func() error {
		runCancel()
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			return nil
		}
	})
	fixture.baseURL = "http://" + listener.Addr().String()
	return fixture
}

// feedbackServerRole 写入一个开发角色。
func feedbackServerRole(
	t *testing.T,
	store *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		"反馈接口开发角色",
		domain.RoleTypeDeveloper,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := store.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// feedbackServerChain 写入一条完整的任务链与其验证失败反馈。
func feedbackServerChain(
	t *testing.T,
	store *sqlite.DB,
	ctx context.Context,
	role domain.Role,
	now time.Time,
	title, summary string,
) feedbackChain {
	t.Helper()
	requirement, err := domain.NewRequirement(title, title+"正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := store.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	step, err := domain.NewStep(
		task.ID,
		1,
		title+"步骤",
		title+"目标",
		title+"执行要求",
		role,
		domain.VerificationSpec{},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := store.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	attempt, err := store.StartAttempt(ctx, storage.StartAttemptRequest{
		TaskID: task.ID,
		StepID: step.ID,
		At:     now,
	})
	if err != nil {
		t.Fatalf("开始执行尝试失败: %v", err)
	}
	run, err := store.BeginValidationRun(ctx, storage.ValidationRunStartRequest{
		TaskID:             task.ID,
		StepID:             step.ID,
		AttemptID:          attempt.ID,
		ExpectedTaskStatus: task.Status,
		Spec:               feedbackServerSpec(),
		SpecSnapshot:       `{"checks":["build"]}`,
		ExecutionEnv:       "container",
		Workspace:          "/workspace/project",
		ContainerID:        "container-1",
		At:                 now,
	})
	if err != nil {
		t.Fatalf("创建验证记录失败: %v", err)
	}
	if err := store.FinishValidationRun(ctx, storage.ValidationRunFinishRequest{
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
	feedback := domain.ValidationFailureFeedback{
		TaskID:             task.ID,
		StepID:             step.ID,
		DeveloperAttemptID: attempt.ID,
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
	if err := store.CreateValidationFailureFeedback(ctx, feedback); err != nil {
		t.Fatalf("写入验证失败反馈失败: %v", err)
	}
	return feedbackChain{
		task:     task,
		step:     step,
		attempt:  attempt,
		run:      run,
		feedback: feedback,
	}
}

// feedbackServerSpec 构造一份只有一条必需检查的验证规格。
func feedbackServerSpec() domain.ValidationSpec {
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

// feedbackServerPath 返回指定验证运行的反馈路径。
func feedbackServerPath(taskID, stepID, runID string) string {
	return httpapi.PathTasks + "/" + taskID + "/steps/" + stepID +
		"/validations/" + runID + "/feedback"
}

// TestValidationFeedbackOverRealServer 校验真实服务上的反馈查询与落盘数据一致。
func TestValidationFeedbackOverRealServer(t *testing.T) {
	t.Parallel()
	fixture := startFeedbackServer(t)
	target := feedbackServerPath(
		fixture.first.task.ID.String(),
		fixture.first.step.ID.String(),
		fixture.first.run.ID.String(),
	)
	status, header, body := get(t, fixture.baseURL, target)
	if status != http.StatusOK {
		t.Fatalf("反馈查询应返回 200，实际 %d，响应: %s", status, body)
	}
	if header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("反馈查询应为 JSON，实际 %q", header.Get("Content-Type"))
	}
	var response httpapi.ValidationFailureFeedbackResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("解析反馈响应失败: %v，响应: %s", err, body)
	}
	// 与真实落盘数据逐字段对照，而不是只比对固定 JSON。
	persisted, err := fixture.store.GetValidationFailureFeedback(
		context.Background(),
		fixture.first.run.ID,
	)
	if err != nil {
		t.Fatalf("读取落盘反馈失败: %v", err)
	}
	if response.TaskID != persisted.TaskID.String() ||
		response.StepID != persisted.StepID.String() ||
		response.AttemptID != persisted.DeveloperAttemptID.String() ||
		response.ValidationRunID != persisted.ValidationRunID.String() {
		t.Fatalf("反馈归属与落盘数据不一致: %+v", response)
	}
	if response.Verdict != string(persisted.Verdict) || response.Summary != persisted.Summary {
		t.Fatalf("反馈结论与落盘数据不一致: %+v", response)
	}
	if len(response.Checks) != len(persisted.Checks) {
		t.Fatalf("检查数量与落盘数据不一致: %d / %d", len(response.Checks), len(persisted.Checks))
	}
	if response.Checks[0].CheckID != persisted.Checks[0].CheckID ||
		response.Checks[0].ExitCode != persisted.Checks[0].ExitCode ||
		response.Checks[0].FailureReason != persisted.Checks[0].FailureReason ||
		response.Checks[0].StderrSummary != persisted.Checks[0].StderrSummary {
		t.Fatalf("检查条目与落盘数据不一致: %+v", response.Checks[0])
	}
	if !response.Checks[0].Required {
		t.Fatal("落盘的必需检查标记丢失")
	}
}

// TestValidationFeedbackDoesNotCrossTasksOverRealServer 校验无法通过替换路径 ID 读取他人反馈。
func TestValidationFeedbackDoesNotCrossTasksOverRealServer(t *testing.T) {
	t.Parallel()
	fixture := startFeedbackServer(t)
	targets := map[string]string{
		"本任务路径读他人验证记录": feedbackServerPath(
			fixture.first.task.ID.String(),
			fixture.first.step.ID.String(),
			fixture.second.run.ID.String(),
		),
		"他人任务路径读本任务验证记录": feedbackServerPath(
			fixture.second.task.ID.String(),
			fixture.first.step.ID.String(),
			fixture.first.run.ID.String(),
		),
		"本任务路径读他人步骤": feedbackServerPath(
			fixture.first.task.ID.String(),
			fixture.second.step.ID.String(),
			fixture.second.run.ID.String(),
		),
	}
	for name, target := range targets {
		status, _, body := get(t, fixture.baseURL, target)
		if status != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d，响应: %s", name, status, body)
		}
		var response httpapi.ErrorResponse
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatalf("%s 应返回 JSON 错误: %s", name, body)
		}
		if response.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码不符: %s", name, response.Error.Code)
		}
	}
}

// TestValidationFeedbackKeepsRunsSeparateOverRealServer 校验同一步骤不同验证运行不串用反馈。
func TestValidationFeedbackKeepsRunsSeparateOverRealServer(t *testing.T) {
	t.Parallel()
	fixture := startFeedbackServer(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(time.Minute)
	attempt, err := fixture.store.StartAttempt(ctx, storage.StartAttemptRequest{
		TaskID: fixture.first.task.ID,
		StepID: fixture.first.step.ID,
		At:     now,
	})
	if err != nil {
		t.Fatalf("开始执行尝试失败: %v", err)
	}
	run, err := fixture.store.BeginValidationRun(ctx, storage.ValidationRunStartRequest{
		TaskID:             fixture.first.task.ID,
		StepID:             fixture.first.step.ID,
		AttemptID:          attempt.ID,
		ExpectedTaskStatus: fixture.first.task.Status,
		Spec:               feedbackServerSpec(),
		SpecSnapshot:       `{"checks":["build"]}`,
		ExecutionEnv:       "container",
		Workspace:          "/workspace/project",
		ContainerID:        "container-1",
		At:                 now,
	})
	if err != nil {
		t.Fatalf("创建验证记录失败: %v", err)
	}
	if err := fixture.store.FinishValidationRun(ctx, storage.ValidationRunFinishRequest{
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
	if err := fixture.store.CreateValidationFailureFeedback(ctx, domain.ValidationFailureFeedback{
		TaskID:             fixture.first.task.ID,
		StepID:             fixture.first.step.ID,
		DeveloperAttemptID: attempt.ID,
		ValidationRunID:    run.ID,
		Verdict:            domain.VerificationVerdictFailed,
		Summary:            "第二次验证失败",
		CreatedAt:          now,
	}); err != nil {
		t.Fatalf("写入验证失败反馈失败: %v", err)
	}
	firstStatus, _, firstBody := get(t, fixture.baseURL, feedbackServerPath(
		fixture.first.task.ID.String(),
		fixture.first.step.ID.String(),
		fixture.first.run.ID.String(),
	))
	if firstStatus != http.StatusOK {
		t.Fatalf("第一次反馈应返回 200，实际 %d，响应: %s", firstStatus, firstBody)
	}
	var first httpapi.ValidationFailureFeedbackResponse
	if err := json.Unmarshal([]byte(firstBody), &first); err != nil {
		t.Fatalf("解析第一次反馈失败: %v", err)
	}
	if first.ValidationRunID != fixture.first.run.ID.String() || first.Summary != "第一次验证失败" {
		t.Fatalf("第一次反馈被串用: %+v", first)
	}
	secondStatus, _, secondBody := get(t, fixture.baseURL, feedbackServerPath(
		fixture.first.task.ID.String(),
		fixture.first.step.ID.String(),
		run.ID.String(),
	))
	if secondStatus != http.StatusOK {
		t.Fatalf("第二次反馈应返回 200，实际 %d，响应: %s", secondStatus, secondBody)
	}
	var second httpapi.ValidationFailureFeedbackResponse
	if err := json.Unmarshal([]byte(secondBody), &second); err != nil {
		t.Fatalf("解析第二次反馈失败: %v", err)
	}
	if second.ValidationRunID != run.ID.String() || second.Summary != "第二次验证失败" {
		t.Fatalf("第二次反馈被串用: %+v", second)
	}
	if second.AttemptID != attempt.ID.String() {
		t.Fatalf("第二次反馈的开发尝试不符: %s", second.AttemptID)
	}
}

// TestValidationFeedbackNotFoundOverRealServer 校验不存在反馈时返回明确的未找到结果。
func TestValidationFeedbackNotFoundOverRealServer(t *testing.T) {
	t.Parallel()
	fixture := startFeedbackServer(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(2 * time.Minute)
	attempt, err := fixture.store.StartAttempt(ctx, storage.StartAttemptRequest{
		TaskID: fixture.first.task.ID,
		StepID: fixture.first.step.ID,
		At:     now,
	})
	if err != nil {
		t.Fatalf("开始执行尝试失败: %v", err)
	}
	run, err := fixture.store.BeginValidationRun(ctx, storage.ValidationRunStartRequest{
		TaskID:             fixture.first.task.ID,
		StepID:             fixture.first.step.ID,
		AttemptID:          attempt.ID,
		ExpectedTaskStatus: fixture.first.task.Status,
		Spec:               feedbackServerSpec(),
		SpecSnapshot:       `{"checks":["build"]}`,
		ExecutionEnv:       "container",
		Workspace:          "/workspace/project",
		ContainerID:        "container-1",
		At:                 now,
	})
	if err != nil {
		t.Fatalf("创建验证记录失败: %v", err)
	}
	status, _, body := get(t, fixture.baseURL, feedbackServerPath(
		fixture.first.task.ID.String(),
		fixture.first.step.ID.String(),
		run.ID.String(),
	))
	if status != http.StatusNotFound {
		t.Fatalf("没有反馈时应返回 404，实际 %d，响应: %s", status, body)
	}
	var response httpapi.ErrorResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("应返回 JSON 错误: %s", body)
	}
	if response.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
}

// TestValidationFeedbackWithoutServiceOverRealServer 校验未注入服务时真实服务返回 503。
func TestValidationFeedbackWithoutServiceOverRealServer(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)
	target := feedbackServerPath(
		domain.NewID().String(),
		domain.NewID().String(),
		domain.NewID().String(),
	)
	status, _, body := get(t, baseURL, target)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("依赖缺失应返回 503，实际 %d，响应: %s", status, body)
	}
}
