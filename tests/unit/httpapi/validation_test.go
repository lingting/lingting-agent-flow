package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// validateReply 是一条受控的检查执行结果。
type validateReply struct {
	exitCode int
	stdout   string
	stderr   string
	timedOut bool
	started  bool
	certain  bool
}

// validateExecutor 是容器能力的受控替身，只用于验证接口测试。
type validateExecutor struct {
	running bool
	replies map[string]validateReply
	calls   int
	// err 使执行层返回错误。
	err error
}

// InspectContainer 返回受控的容器状态。
func (e *validateExecutor) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			DockerID: "docker-" + request.ContainerID,
		},
		Inspect: &docker.ContainerInspect{Running: e.running},
	}, nil
}

// Exec 返回受控的命令执行结果。
func (e *validateExecutor) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	e.calls++
	name := ""
	if len(request.Command) > 0 {
		name = request.Command[0]
	}
	reply := e.replies[name]
	status := sshclient.CommandStatusSucceeded
	switch {
	case reply.timedOut:
		status = sshclient.CommandStatusTimeout
	case reply.exitCode != 0:
		status = sshclient.CommandStatusExitError
	}
	certain := reply.certain
	started := reply.started
	if status == sshclient.CommandStatusSucceeded || status == sshclient.CommandStatusExitError {
		certain = true
		started = true
	}
	return app.ExecResult{Result: docker.CommandResult{
		ExitCode: reply.exitCode,
		Stdout:   reply.stdout,
		Stderr:   reply.stderr,
		Status:   status,
		Certain:  certain,
		Started:  started,
		Duration: 5 * time.Millisecond,
	}}, e.err
}

// validateAPIFixture 是验证接口的测试环境。
type validateAPIFixture struct {
	handler http.Handler
	db      *sqlite.DB
	ctx     context.Context
	exec    *validateExecutor
	task    domain.Task
	step    domain.Step
	attempt domain.Attempt
}

// newValidateAPIFixture 准备任务、步骤、尝试与验证接口处理器。
func newValidateAPIFixture(t *testing.T) *validateAPIFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	logger, err := logging.New(stepAPILogDiscard{}, logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	now := time.Now().UTC()
	developer := apiRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	planner := apiRole(t, db, ctx, domain.RoleTypePlanner, "规划师", now)
	host := apiHost(t, db, ctx, now)
	host = markAPIHostNormal(t, db, ctx, host)
	requirement, err := domain.NewRequirement("需求标题", "需求正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	task.ContainerID = "container-1"
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusDeveloping,
	} {
		if err := task.Transition(status, now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", status, err)
		}
	}
	if err := db.UpdateTaskIfStatus(
		ctx,
		task,
		[]domain.TaskStatus{domain.TaskStatusQueued},
	); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"",
		developer,
		domain.VerificationSpec{},
		domain.RetryConfig{MaxAttempts: 1},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	attempt, err := domain.NewAttempt(task.ID, step.ID, 1, now)
	if err != nil {
		t.Fatalf("构造执行尝试失败: %v", err)
	}
	if err := db.CreateAttempt(ctx, attempt); err != nil {
		t.Fatalf("写入执行尝试失败: %v", err)
	}
	if err := attempt.Complete(now); err != nil {
		t.Fatalf("标记尝试完成失败: %v", err)
	}
	if err := db.UpdateAttemptIfStatus(
		ctx,
		attempt,
		[]domain.AttemptStatus{domain.AttemptStatusRunning},
	); err != nil {
		t.Fatalf("写入尝试终态失败: %v", err)
	}
	exec := &validateExecutor{running: true, replies: map[string]validateReply{}}
	service, err := app.NewValidationService(app.ValidationOptions{
		Store:      db,
		Containers: exec,
		OutputRoot: workspace.Dir("validations"),
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证服务失败: %v", err)
	}
	if startErr := service.Start(ctx); startErr != nil {
		t.Fatalf("启动验证服务失败: %v", startErr)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if stopErr := service.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止验证服务失败: %v", stopErr)
		}
	})
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	records, err := app.NewExecutionRecordService(db)
	if err != nil {
		t.Fatalf("构造执行记录服务失败: %v", err)
	}
	return &validateAPIFixture{
		handler: httpapi.NewHandler(httpapi.Deps{
			Health:           app.NewHealthService(db),
			SystemInfo:       func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
			Tasks:            tasks,
			ExecutionRecords: records,
			Validation:       service,
		}),
		db:      db,
		ctx:     ctx,
		exec:    exec,
		task:    task,
		step:    step,
		attempt: attempt,
	}
}

// validatePath 返回验证接口路径。
func (f *validateAPIFixture) validatePath() string {
	return httpapi.PathTasks + "/" + f.task.ID.String() +
		"/steps/" + f.step.ID.String() +
		"/attempts/" + f.attempt.ID.String() + "/validate"
}

// post 发送一次验证请求。
func (f *validateAPIFixture) post(
	t *testing.T,
	target string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		payload = encoded
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

// checks 构造一份请求用的检查配置。
func validateChecks(required bool, commands ...string) []map[string]any {
	checks := make([]map[string]any, 0, len(commands))
	for index, command := range commands {
		checks = append(checks, map[string]any{
			"id":       command,
			"name":     command,
			"enabled":  true,
			"required": required,
			"sequence": index + 1,
			"command":  command,
		})
	}
	return checks
}

// TestValidateAttemptAPI 验证通过接口发起验证并返回状态与结论。
func TestValidateAttemptAPI(t *testing.T) {
	t.Parallel()
	fixture := newValidateAPIFixture(t)
	fixture.exec.replies["build"] = validateReply{exitCode: 0, stdout: "构建成功"}
	recorder := fixture.post(t, fixture.validatePath(), map[string]any{
		"checks": validateChecks(true, "build"),
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ValidateAttemptResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if response.ValidationRunID == "" {
		t.Fatal("响应必须给出验证记录标识")
	}
	if response.Status != string(domain.ValidationRunStatusCompleted) {
		t.Fatalf("验证状态不符: %s", response.Status)
	}
	if response.Verdict != string(domain.VerificationVerdictPassed) || !response.Passed {
		t.Fatalf("验证结论不符: %s", response.Verdict)
	}
	if !response.Persisted {
		t.Fatalf("结果应已持久化: %v", response.PersistenceErrors)
	}
	if response.ExecutionEnv != "container" {
		t.Fatalf("执行环境未返回: %q", response.ExecutionEnv)
	}
	if len(response.Checks) != 1 {
		t.Fatalf("检查结果数量不符: %d", len(response.Checks))
	}
	if response.Checks[0].StdoutSummary != "构建成功" {
		t.Fatalf("输出摘要未返回: %+v", response.Checks[0])
	}
	// 完整输出不得塞进单个响应，只给出落盘位置。
	if response.Checks[0].StdoutPath == "" {
		t.Fatal("输出落盘位置未返回")
	}
	if fixture.exec.calls != 1 {
		t.Fatalf("接口应触发一次真实命令执行，实际 %d", fixture.exec.calls)
	}
}

// TestValidateAttemptAPIRequiredCheckFails 验证必需检查失败时结论为 failed。
func TestValidateAttemptAPIRequiredCheckFails(t *testing.T) {
	t.Parallel()
	fixture := newValidateAPIFixture(t)
	fixture.exec.replies["build"] = validateReply{exitCode: 1, stderr: "编译错误"}
	recorder := fixture.post(t, fixture.validatePath(), map[string]any{
		"checks": validateChecks(true, "build"),
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("验证流程完成时应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ValidateAttemptResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if response.Passed {
		t.Fatal("必需检查失败时结论不能是通过")
	}
	if response.Verdict != string(domain.VerificationVerdictFailed) {
		t.Fatalf("验证结论不符: %s", response.Verdict)
	}
	if response.FailureSummary == "" {
		t.Fatal("失败时必须给出原因")
	}
}

// TestValidateAttemptAPIContainerNotRunning 验证容器未运行时返回 412。
func TestValidateAttemptAPIContainerNotRunning(t *testing.T) {
	t.Parallel()
	fixture := newValidateAPIFixture(t)
	fixture.exec.running = false
	recorder := fixture.post(t, fixture.validatePath(), map[string]any{
		"checks": validateChecks(true, "build"),
	})
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("期望 412，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if response.Error.Code != httpapi.CodePreconditionFailed {
		t.Fatalf("错误码不符: %s", response.Error.Code)
	}
	if fixture.exec.calls != 0 {
		t.Fatal("前置条件不满足时不得执行命令")
	}
}

// TestValidateAttemptAPINotFound 验证跨任务的尝试返回 404。
func TestValidateAttemptAPINotFound(t *testing.T) {
	t.Parallel()
	fixture := newValidateAPIFixture(t)
	target := httpapi.PathTasks + "/" + fixture.task.ID.String() +
		"/steps/" + fixture.step.ID.String() +
		"/attempts/" + domain.NewID().String() + "/validate"
	recorder := fixture.post(t, target, map[string]any{
		"checks": validateChecks(true, "build"),
	})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestValidateAttemptAPIInvalidCheck 验证非法检查配置返回 400。
func TestValidateAttemptAPIInvalidCheck(t *testing.T) {
	t.Parallel()
	fixture := newValidateAPIFixture(t)
	recorder := fixture.post(t, fixture.validatePath(), map[string]any{
		"checks": []map[string]any{{
			"id":       "build",
			"name":     "build",
			"enabled":  true,
			"required": true,
			"sequence": 1,
			"command":  "go test ./...",
		}},
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if fixture.exec.calls != 0 {
		t.Fatal("参数非法时不得执行命令")
	}
}

// TestValidateAttemptAPIRejectsUnsafeWorkDir 验证容器外工作目录返回 400。
func TestValidateAttemptAPIRejectsUnsafeWorkDir(t *testing.T) {
	t.Parallel()
	fixture := newValidateAPIFixture(t)
	checks := validateChecks(true, "build")
	checks[0]["work_dir"] = "/etc"
	recorder := fixture.post(t, fixture.validatePath(), map[string]any{"checks": checks})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestValidateAttemptAPIMethodNotAllowed 验证非 POST 方法返回 405。
func TestValidateAttemptAPIMethodNotAllowed(t *testing.T) {
	t.Parallel()
	fixture := newValidateAPIFixture(t)
	request := httptest.NewRequest(http.MethodGet, fixture.validatePath(), nil)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestValidateAttemptAPIUnavailableService 验证服务缺失时返回 503。
func TestValidateAttemptAPIUnavailableService(t *testing.T) {
	t.Parallel()
	handler := httpapi.NewHandler(httpapi.Deps{})
	request := httptest.NewRequest(
		http.MethodPost,
		httpapi.PathTasks+"/t1/steps/s1/attempts/a1/validate",
		bytes.NewReader(nil),
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestValidateAttemptAPIUnknownPathIsNotFound 验证其他 attempts 形状仍按只读语义处理。
func TestValidateAttemptAPIUnknownPathIsNotFound(t *testing.T) {
	t.Parallel()
	fixture := newValidateAPIFixture(t)
	target := httpapi.PathTasks + "/" + fixture.task.ID.String() +
		"/steps/" + fixture.step.ID.String() +
		"/attempts/" + fixture.attempt.ID.String() + "/unknown"
	recorder := fixture.post(t, target, nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// 确认注入的错误仍可被识别，避免替身签名漂移后测试静默失效。
var _ = errors.Is
