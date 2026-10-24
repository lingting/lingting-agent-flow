package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// workflowAPIPort 是开发阶段与验证阶段共用的容器替身。
type workflowAPIPort struct {
	mu sync.Mutex
	// agentStatus 是开发阶段的 Agent 结论。
	agentStatus agent.RunStatus
	// agentFailure 是开发阶段的失败原因。
	agentFailure string
	// checkExitCode 是检查命令的退出码。
	checkExitCode int
	// checkStarted 表示检查命令是否真的启动。
	checkStarted bool
	// agentCalls 统计 Agent 调用次数。
	agentCalls int
	// execCalls 统计检查命令执行次数。
	execCalls int
	// sessions 非空时把会话记录写入数据库，模拟真实执行器的持久化行为。
	sessions storage.SessionStore
}

// InspectContainer 返回一个运行中的受管理容器。
func (p *workflowAPIPort) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   "host-1",
			Name:     "agentflow-workflow-api",
			DockerID: "docker-" + request.ContainerID,
		},
		Inspect: &docker.ContainerInspect{Running: true},
	}, nil
}

// RunAgent 记录调用并返回受控结果。
func (p *workflowAPIPort) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	p.mu.Lock()
	p.agentCalls++
	status := p.agentStatus
	if status == "" {
		status = agent.RunCompleted
	}
	failure := p.agentFailure
	p.mu.Unlock()
	now := time.Now().UTC()
	sessionID := domain.NewID()
	completeness := agent.SessionComplete
	if status != agent.RunCompleted {
		completeness = agent.SessionPartial
	}
	result := agent.RunResult{
		SessionID:    sessionID.String(),
		InvocationID: "invocation",
		Status:       status,
		FinalText:    "实现完成",
		Failure:      failure,
		StartedAt:    now,
		FinishedAt:   now,
		Session: agent.SessionFiles{
			JSONLPath:    "sessions/invocation/session.jsonl",
			Completeness: completeness,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	p.mu.Lock()
	sessions := p.sessions
	p.mu.Unlock()
	if sessions != nil {
		if err := sessions.CreateSession(ctx, stepAPISession(request, result, now)); err != nil {
			return app.ContainerAgentResult{Result: result}, err
		}
	}
	return app.ContainerAgentResult{Result: result}, nil
}

// Exec 返回受控的检查结果。
func (p *workflowAPIPort) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	p.mu.Lock()
	p.execCalls++
	exitCode := p.checkExitCode
	started := p.checkStarted
	p.mu.Unlock()
	status := sshclient.CommandStatusSucceeded
	if exitCode != 0 {
		status = sshclient.CommandStatusExitError
	}
	if !started {
		status = sshclient.CommandStatusConnectionError
	}
	return app.ExecResult{Result: docker.CommandResult{
		ExitCode: exitCode,
		Stdout:   "ok",
		Status:   status,
		Certain:  started,
		Started:  started,
		Duration: 5 * time.Millisecond,
	}}, nil
}

// workflowAPIFixture 是闭环接口的测试环境。
type workflowAPIFixture struct {
	handler http.Handler
	db      *sqlite.DB
	ctx     context.Context
	port    *workflowAPIPort
	task    domain.Task
	step    domain.Step
}

// newWorkflowAPIFixture 准备任务、步骤与闭环接口处理器。
func newWorkflowAPIFixture(t *testing.T) *workflowAPIFixture {
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
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	sessions, err := app.NewSessionService(db, workspace.SessionRoot(), logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	now := time.Now().UTC()
	developer := apiRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
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
		Goal:                "完成本次需求",
		Environment:         domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		DeveloperRoleID:     developer.ID,
		PlannerRoleID:       developer.ID,
		AgentTimeoutSeconds: 30,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"",
		developer,
		domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
		domain.RetryConfig{MaxAttempts: 1},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
	} {
		if err := task.Transition(status, now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", status, err)
		}
	}
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	port := &workflowAPIPort{checkStarted: true, sessions: db}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      db,
		Containers: port,
		Sessions:   sessions,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	validation, err := app.NewValidationService(app.ValidationOptions{
		Store:      db,
		Containers: port,
		OutputRoot: workspace.Dir("validations"),
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
	workflow, err := app.NewStepWorkflowService(app.StepWorkflowOptions{
		Steps:      executor,
		Validation: validation,
		Store:      db,
		Cycles:     db,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造闭环协调器失败: %v", err)
	}
	for _, service := range []interface {
		Start(ctx context.Context) error
		Stop(ctx context.Context) error
	}{executor, validation, workflow} {
		if err := service.Start(ctx); err != nil {
			t.Fatalf("启动后台服务失败: %v", err)
		}
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		for _, service := range []interface {
			Stop(ctx context.Context) error
		}{workflow, validation, executor} {
			if err := service.Stop(stopCtx); err != nil {
				t.Errorf("停止后台服务失败: %v", err)
			}
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
	handler := httpapi.NewHandler(httpapi.Deps{
		Health:           app.NewHealthService(db),
		SystemInfo:       func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:            tasks,
		ExecutionRecords: records,
		StepExecutor:     executor,
		Validation:       validation,
		StepWorkflow:     workflow,
	})
	return &workflowAPIFixture{
		handler: handler,
		db:      db,
		ctx:     ctx,
		port:    port,
		task:    task,
		step:    step,
	}
}

// workflowPath 返回闭环接口路径。
func workflowPath(taskID, stepID string) string {
	return httpapi.PathTasks + "/" + taskID + "/steps/" + stepID + "/execute-and-validate"
}

// reopenCyclePath 返回"显式开启执行周期"动作的路径。
func reopenCyclePath(taskID, stepID string) string {
	return httpapi.PathTasks + "/" + taskID + "/steps/" + stepID + "/open-repair-cycle"
}

// post 发送一次闭环请求。
func (f *workflowAPIFixture) post(t *testing.T, target string, body any) *httptest.ResponseRecorder {
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

// TestExecuteAndValidateAPI 验证闭环通过时返回 200 与完整字段。
func TestExecuteAndValidateAPI(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowAPIFixture(t)
	recorder := fixture.post(t, workflowPath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), map[string]any{"prompt": "补充说明"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ExecuteAndValidateStepResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !response.ValidationRan {
		t.Fatalf("开发成功后应启动验证")
	}
	if !response.Verified || !response.ValidationPassed {
		t.Fatalf("验证通过时应标记为已验证完成: %+v", response)
	}
	if response.StepStatus != string(domain.StepStatusCompleted) {
		t.Fatalf("步骤状态不符: %s", response.StepStatus)
	}
	if response.ValidationRunID == "" {
		t.Fatalf("响应必须给出验证记录标识")
	}
	if response.AttemptID == "" || response.SessionID == "" {
		t.Fatalf("响应必须给出尝试与会话标识")
	}
	if !response.Finalized {
		t.Fatalf("收口状态应完整写入: %v", response.FinalizationErrors)
	}
	// 单个步骤通过验证不等于任务完成。
	if response.TaskStatus == string(domain.TaskStatusCompleted) {
		t.Fatalf("任务不得被标记为完成: %s", response.TaskStatus)
	}
	if fixture.port.agentCalls != 1 || fixture.port.execCalls != 1 {
		t.Fatalf(
			"应各触发一次开发与验证调用，实际 agent=%d exec=%d",
			fixture.port.agentCalls,
			fixture.port.execCalls,
		)
	}
}

// TestExecuteAndValidateAPIVerificationFailedReturns500 验证必需检查失败时返回 500。
func TestExecuteAndValidateAPIVerificationFailedReturns500(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowAPIFixture(t)
	fixture.port.checkExitCode = 1
	recorder := fixture.post(t, workflowPath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), nil)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("期望 500，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if response.Error.Code != httpapi.CodeExecutionFailed {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
	if response.Error.Message == "" {
		t.Fatal("错误响应必须给出说明")
	}
	// 验证记录与检查结果必须保留，不能因为失败被删除。
	attempts, err := fixture.db.ListAttemptsByStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("失败闭环必须保留一条尝试，实际 %d", len(attempts))
	}
	runs, err := fixture.db.ListValidationRunsByAttempt(fixture.ctx, attempts[0].ID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("失败闭环必须保留一条验证记录，实际 %d", len(runs))
	}
}

// TestExecuteAndValidateAPIDeveloperFailureReturns500 验证开发失败时返回 500 且不启动验证。
func TestExecuteAndValidateAPIDeveloperFailureReturns500(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowAPIFixture(t)
	fixture.port.agentStatus = agent.RunFailed
	fixture.port.agentFailure = "模型不可用"
	recorder := fixture.post(t, workflowPath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), nil)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("期望 500，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if fixture.port.execCalls != 0 {
		t.Fatalf("开发失败后不应执行任何检查命令: %d", fixture.port.execCalls)
	}
}

// TestExecuteAndValidateAPIMethodNotAllowed 验证非 POST 请求返回 405。
func TestExecuteAndValidateAPIMethodNotAllowed(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowAPIFixture(t)
	target := workflowPath(fixture.task.ID.String(), fixture.step.ID.String())
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("Allow 头不符: %s", recorder.Header().Get("Allow"))
	}
}

// TestExecuteAndValidateAPINotFound 验证未知步骤返回 404。
func TestExecuteAndValidateAPINotFound(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowAPIFixture(t)
	recorder := fixture.post(t, workflowPath(
		fixture.task.ID.String(),
		"not-exists",
	), nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if response.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
	if fixture.port.agentCalls != 0 {
		t.Fatal("目标不存在时不应调用执行器")
	}
}

// TestExecuteAndValidateAPIConflict 验证状态不允许时返回 409。
func TestExecuteAndValidateAPIConflict(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowAPIFixture(t)
	// 先把步骤推进到不可再次执行的状态。
	step, err := fixture.db.GetStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusRunning, time.Now().UTC()); err != nil {
		t.Fatalf("迁移步骤失败: %v", err)
	}
	if err := fixture.db.UpdateStep(fixture.ctx, *step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	recorder := fixture.post(t, workflowPath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("期望 409，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if response.Error.Code != httpapi.CodeConflict {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
	if fixture.port.agentCalls != 0 {
		t.Fatal("状态冲突时不应调用执行器")
	}
}

// TestExecuteAndValidateAPIInvalidConfigReturns400 验证配置无效时返回 400。
func TestExecuteAndValidateAPIInvalidConfigReturns400(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowAPIFixture(t)
	step, err := fixture.db.GetStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	step.VerificationSpec = domain.VerificationSpec{}
	if err := fixture.db.UpdateStep(fixture.ctx, *step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	recorder := fixture.post(t, workflowPath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if response.Error.Code != httpapi.CodeInvalidRequest {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
	if fixture.port.agentCalls != 0 {
		t.Fatal("验证配置无效时不应调用执行器")
	}
}

// TestListValidationsAPI 验证步骤验证历史可以查询，且历史记录不被覆盖。
func TestListValidationsAPI(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowAPIFixture(t)
	// 第一次执行：必需检查失败。
	fixture.port.checkExitCode = 1
	if recorder := fixture.post(t, workflowPath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), nil); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("第一次闭环应返回 500，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	// 第二次执行前必须先显式开启新的执行周期：上一周期已结束，继续执行是用户的
	// 显式决定，而不是自动重试。
	if recorder := fixture.post(t, reopenCyclePath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), nil); recorder.Code != http.StatusOK {
		t.Fatalf("开启执行周期应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	// 第二次执行：必需检查通过。
	fixture.port.checkExitCode = 0
	if recorder := fixture.post(t, workflowPath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), nil); recorder.Code != http.StatusOK {
		t.Fatalf("第二次闭环应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	target := httpapi.PathTasks + "/" + fixture.task.ID.String() +
		"/steps/" + fixture.step.ID.String() + "/validations"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ValidationHistoryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if response.Total != 2 {
		t.Fatalf("验证历史条数不符: %d", response.Total)
	}
	if response.State != string(domain.StepVerificationPassed) {
		t.Fatalf("派生验证状态不符: %s", response.State)
	}
	if response.Latest == nil {
		t.Fatalf("应给出最新验证记录")
	}
	if response.Latest.Verdict != string(domain.VerificationVerdictPassed) {
		t.Fatalf("最新结论不符: %s", response.Latest.Verdict)
	}
	// 历史记录不得被覆盖：第一条仍然是失败结论。
	if response.Records[0].Verdict != string(domain.VerificationVerdictFailed) {
		t.Fatalf("历史验证记录被覆盖: %s", response.Records[0].Verdict)
	}
	if response.Records[0].ID == response.Records[1].ID {
		t.Fatalf("每次验证必须创建独立记录")
	}
	if len(response.Records[0].Checks) == 0 {
		t.Fatalf("验证记录应包含检查结果")
	}
}

// TestListValidationsAPIEmpty 验证没有验证记录时返回未验证状态。
func TestListValidationsAPIEmpty(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowAPIFixture(t)
	target := httpapi.PathTasks + "/" + fixture.task.ID.String() +
		"/steps/" + fixture.step.ID.String() + "/validations"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ValidationHistoryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if response.State != string(domain.StepVerificationUnverified) {
		t.Fatalf("没有验证记录时应为 unverified: %s", response.State)
	}
	if response.Latest != nil {
		t.Fatalf("没有验证记录时不应给出最新记录")
	}
	if response.Total != 0 {
		t.Fatalf("验证历史条数不符: %d", response.Total)
	}
}
