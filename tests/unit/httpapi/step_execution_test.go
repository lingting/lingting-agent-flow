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

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// stepAPIReplies 控制替身执行器的返回结果。
type stepAPIReplies struct {
	status    agent.RunStatus
	failure   string
	finalText string
}

// stepAPIExecutor 是容器能力的受控替身，只用于 API 层测试。
type stepAPIExecutor struct {
	reply  stepAPIReplies
	calls  int
	ranCtx context.Context
	// sessions 非空时把会话记录写入数据库，模拟真实执行器的持久化行为。
	sessions storage.SessionStore
}

// InspectContainer 返回一个运行中的受管理容器。
func (e *stepAPIExecutor) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			DockerID: "docker-" + request.ContainerID,
		},
		Inspect: &docker.ContainerInspect{Running: true},
	}, nil
}

// RunAgent 记录调用并返回受控结果。
func (e *stepAPIExecutor) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	e.calls++
	e.ranCtx = ctx
	now := time.Now().UTC()
	status := e.reply.status
	if status == "" {
		status = agent.RunCompleted
	}
	sessionID := domain.NewID()
	completeness := agent.SessionComplete
	if status != agent.RunCompleted {
		completeness = agent.SessionPartial
	}
	result := agent.RunResult{
		SessionID:    sessionID.String(),
		InvocationID: "invocation",
		Status:       status,
		FinalText:    e.reply.finalText,
		Failure:      e.reply.failure,
		ExitCode:     0,
		StartedAt:    now,
		FinishedAt:   now,
		Session: agent.SessionFiles{
			JSONLPath:    "sessions/invocation/session.jsonl",
			Completeness: completeness,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	if e.sessions != nil {
		if err := e.sessions.CreateSession(ctx, stepAPISession(request, result, now)); err != nil {
			return app.ContainerAgentResult{Result: result}, err
		}
	}
	return app.ContainerAgentResult{Result: result}, nil
}

// stepAPISession 构造一条与真实执行一致的会话记录。
func stepAPISession(
	request app.ContainerAgentRequest,
	result agent.RunResult,
	now time.Time,
) domain.Session {
	status := domain.SessionStatusCompleted
	runStatus := domain.SessionRunCompleted
	completeness := domain.SessionCompletenessComplete
	if result.Status != agent.RunCompleted {
		status = domain.SessionStatusFailed
		runStatus = domain.SessionRunFailed
		completeness = domain.SessionCompletenessPartial
	}
	stepID := domain.ID(request.StepID)
	attemptID := domain.ID(request.AttemptID)
	roleID := domain.ID(request.RoleType + "-role")
	roleType := domain.RoleType(request.RoleType)
	return domain.Session{
		ID:        domain.ID(result.SessionID),
		TaskID:    domain.ID(request.TaskID),
		StepID:    &stepID,
		AttemptID: &attemptID,
		RoleID:    roleID,
		RoleSnapshot: domain.RoleSnapshot{
			RoleID:       roleID,
			Type:         roleType,
			Name:         string(roleType),
			Runner:       domain.RoleRunnerPi,
			Model:        request.Model,
			SystemPrompt: "系统提示词",
			ToolsPolicy:  domain.DefaultToolsPolicy(roleType),
		},
		Provider:     "provider",
		Model:        request.Model,
		Status:       status,
		RunStatus:    runStatus,
		Completeness: completeness,
		ExportStatus: domain.SessionExportNotAttempted,
		InvocationID: result.InvocationID,
		JSONLPath:    result.Session.JSONLPath,
		StartedAt:    now,
		CreatedAt:    now,
	}
}

// stepAPIFixture 是步骤执行接口的测试环境。
type stepAPIFixture struct {
	handler http.Handler
	db      *sqlite.DB
	ctx     context.Context
	port    *stepAPIExecutor
	task    domain.Task
	step    domain.Step
}

// newStepAPIFixture 准备任务、步骤与执行接口处理器。
func newStepAPIFixture(t *testing.T, stayQueued bool) *stepAPIFixture {
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
	// 容器归属必须来自真实的容器登记记录：任务记录里的 container_id 只是普通
	// 文本列，执行链路会核验归属。
	seedOwnedContainer(t, db, ctx, task, "container-1", "docker-container-1")
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
	if !stayQueued {
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
	}
	port := &stepAPIExecutor{sessions: db}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      db,
		Containers: port,
		Sessions:   sessions,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	if startErr := executor.Start(ctx); startErr != nil {
		t.Fatalf("启动步骤执行协调器失败: %v", startErr)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if stopErr := executor.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止步骤执行协调器失败: %v", stopErr)
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
	// 单步骤入口同样需要任务租约：它是与任务级工作流共用的唯一所有权令牌。
	leases, err := app.NewTaskLeaseService(app.TaskLeaseOptions{
		Store:  db,
		Logger: logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造任务租约服务失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{
		Health:           app.NewHealthService(db),
		SystemInfo:       func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:            tasks,
		ExecutionRecords: records,
		StepExecutor:     executor,
		TaskLeases:       leases,
	})
	return &stepAPIFixture{
		handler: handler,
		db:      db,
		ctx:     ctx,
		port:    port,
		task:    task,
		step:    step,
	}
}

// executePath 返回步骤执行接口路径。
func (f *stepAPIFixture) executePath(taskID, stepID string) string {
	return httpapi.PathTasks + "/" + taskID + "/steps/" + stepID + "/execute"
}

// post 发送一次执行请求。
func (f *stepAPIFixture) post(
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

// TestExecuteStepAPI 验证成功执行返回 200 与真实结果字段。
func TestExecuteStepAPI(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	recorder := fixture.post(t, fixture.executePath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), map[string]any{"prompt": "补充说明"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ExecuteStepResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if response.Outcome != string(domain.StepExecutionCompleted) {
		t.Fatalf("执行结论不符: %s", response.Outcome)
	}
	if !response.Success || !response.Finalized {
		t.Fatalf("成功响应必须同时标记成功与已持久化: %+v", response)
	}
	if response.StepStatus != string(domain.StepStatusCompleted) {
		t.Fatalf("步骤状态不符: %s", response.StepStatus)
	}
	// 单个步骤成功不等于任务成功：任务仍处于 developing。
	if response.TaskStatus != string(domain.TaskStatusDeveloping) {
		t.Fatalf("任务状态不得被标记为成功: %s", response.TaskStatus)
	}
	if response.AttemptID == "" || response.SessionID == "" {
		t.Fatal("响应必须给出尝试与会话标识")
	}
	if response.ArchiveStatus == "" || response.ExportStatus == "" {
		t.Fatal("归档与导出状态必须分别给出")
	}
	if fixture.port.calls != 1 {
		t.Fatalf("接口应触发一次真实调用，实际 %d", fixture.port.calls)
	}
}

// TestExecuteStepAPIFailureReturns500 验证执行失败不会被返回为成功。
func TestExecuteStepAPIFailureReturns500(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	fixture.port.reply = stepAPIReplies{status: agent.RunFailed, failure: "模型不可用"}
	recorder := fixture.post(t, fixture.executePath(
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
	// 执行已经发生：尝试记录必须保留，不能被删除以恢复"干净"状态。
	attempts, err := fixture.db.ListAttemptsByStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("失败执行必须保留一条尝试，实际 %d", len(attempts))
	}
}

// TestExecuteStepAPIQueuedConflict 验证状态不允许时返回 409。
func TestExecuteStepAPIQueuedConflict(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, true)
	recorder := fixture.post(t, fixture.executePath(
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
	if fixture.port.calls != 0 {
		t.Fatal("状态不允许时不应调用执行器")
	}
	attempts, err := fixture.db.ListAttemptsByStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("冲突请求不应创建尝试，实际 %d", len(attempts))
	}
}

// TestExecuteStepAPINotFound 验证不存在或不属于该任务的步骤返回 404。
func TestExecuteStepAPINotFound(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	other := newStepAPIFixture(t, false)
	cases := []struct {
		name   string
		taskID string
		stepID string
	}{
		{name: "任务不存在", taskID: domain.NewID().String(), stepID: fixture.step.ID.String()},
		{name: "步骤不存在", taskID: fixture.task.ID.String(), stepID: domain.NewID().String()},
		{name: "步骤不属于任务", taskID: fixture.task.ID.String(), stepID: other.step.ID.String()},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			recorder := fixture.post(t, fixture.executePath(testCase.taskID, testCase.stepID), nil)
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
			if response.Error.RequestID == "" {
				t.Fatal("错误响应必须包含请求标识")
			}
		})
	}
	if fixture.port.calls != 0 {
		t.Fatal("不存在的目标不应调用执行器")
	}
}

// TestExecuteStepAPIMethodNotAllowed 验证执行路径只接受 POST。
func TestExecuteStepAPIMethodNotAllowed(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	request := httptest.NewRequest(http.MethodGet, fixture.executePath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), nil)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	// 执行记录查询不提供该形状，因此 GET 由执行处理器给出 405。
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if allow := recorder.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow 头不符: %q", allow)
	}
}

// TestExecuteStepAPIInvalidTimeout 验证超时参数越界返回 400。
func TestExecuteStepAPIInvalidTimeout(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	recorder := fixture.post(t, fixture.executePath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), map[string]any{"timeout_seconds": -1})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestExecuteStepAPIServiceUnavailable 验证未注入协调器时返回 503。
func TestExecuteStepAPIServiceUnavailable(t *testing.T) {
	t.Parallel()
	handler := httpapi.NewHandler(httpapi.Deps{})
	request := httptest.NewRequest(
		http.MethodPost,
		httpapi.PathTasks+"/"+domain.NewID().String()+"/steps/"+domain.NewID().String()+"/execute",
		nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	// 未注入任务服务时接口在更早的分支返回 503：这保证未实现能力不会被伪装成可用。
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestExecuteStepAPINotHandledBySPA 验证执行路径不会被当作页面回退。
func TestExecuteStepAPINotHandledBySPA(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	request := httptest.NewRequest(
		http.MethodPost,
		httpapi.PathTasks+"/"+fixture.task.ID.String()+"/steps/"+fixture.step.ID.String()+"/exec",
		nil,
	)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("未知 API 必须返回 JSON: %q", contentType)
	}
}

// TestExecuteStepAPIStorageConflict 验证占用冲突映射为 409。
func TestExecuteStepAPIStorageConflict(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	// 手工占用步骤，模拟另一个执行者已经拿到占用。
	attempt, err := fixture.db.StartAttempt(fixture.ctx, storage.StartAttemptRequest{
		TaskID: fixture.task.ID,
		StepID: fixture.step.ID,
		At:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("创建占用尝试失败: %v", err)
	}
	step, err := fixture.db.GetStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusRunning, time.Now().UTC()); err != nil {
		t.Fatalf("迁移步骤失败: %v", err)
	}
	if err := step.SetCurrentAttempt(&attempt.ID); err != nil {
		t.Fatalf("记录占用失败: %v", err)
	}
	if err := fixture.db.UpdateStep(fixture.ctx, *step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	recorder := fixture.post(t, fixture.executePath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
	), nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("期望 409，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if fixture.port.calls != 0 {
		t.Fatal("步骤已被占用时不应调用执行器")
	}
}

// markAPIHostNormal 把执行节点置为已通过环境预检。
func markAPIHostNormal(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	host domain.ExecutionHost,
) domain.ExecutionHost {
	t.Helper()
	stored, err := db.GetExecutionHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Checks: []domain.ValidationCheck{
			{Name: domain.ValidationCheckSSHConnection, Status: domain.CheckStatusPassed},
		},
	}
	if err := stored.RecordValidationResult(
		stored.ValidationGeneration,
		details,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := db.UpdateExecutionHostValidation(ctx, *stored, stored.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	return *stored
}

// stepAPILogDiscard 丢弃日志输出，避免测试污染标准输出。
type stepAPILogDiscard struct{}

// Write 丢弃全部内容。
func (stepAPILogDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

// 编译期确认替身满足接口，避免签名漂移后测试静默失效。
var _ app.StepExecutionContainerPort = (*stepAPIExecutor)(nil)

// 确认注入的错误仍可被识别。
var _ = errors.Is

// executeStepsPath 返回顺序执行接口路径。
func (f *stepAPIFixture) executeStepsPath(taskID string) string {
	return httpapi.PathTasks + "/" + taskID + "/execute-steps"
}

// TestExecuteStepsAPI 验证顺序执行返回每一步的结果与任务状态。
func TestExecuteStepsAPI(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	recorder := fixture.post(t, fixture.executeStepsPath(fixture.task.ID.String()), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ExecuteStepsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(response.Steps) != 1 {
		t.Fatalf("应执行一个步骤，实际 %d", len(response.Steps))
	}
	if response.Stopped {
		t.Fatalf("全部成功时不应提前停止: %s", response.StopReason)
	}
	if !response.Success {
		t.Fatalf("全部步骤成功时应标记成功: %+v", response)
	}
	if response.TaskStatus != string(domain.TaskStatusDeveloping) {
		t.Fatalf("任务状态不得被标记为成功: %s", response.TaskStatus)
	}
	entry := response.Steps[0]
	if entry.AttemptID == "" || entry.SessionID == "" {
		t.Fatal("每一步都必须给出尝试与会话标识")
	}
	if entry.Outcome != string(domain.StepExecutionCompleted) {
		t.Fatalf("步骤执行结论不符: %s", entry.Outcome)
	}
	if fixture.port.calls != 1 {
		t.Fatalf("接口应触发一次真实调用，实际 %d", fixture.port.calls)
	}
}

// TestExecuteStepsAPIStopsOnFailure 验证失败后停止并报告实际失败步骤。
func TestExecuteStepsAPIStopsOnFailure(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	fixture.port.reply = stepAPIReplies{status: agent.RunFailed, failure: "模型不可用"}
	recorder := fixture.post(t, fixture.executeStepsPath(fixture.task.ID.String()), nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("顺序执行本身应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ExecuteStepsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !response.Stopped {
		t.Fatal("步骤失败后必须标记为已停止")
	}
	if response.Success {
		t.Fatal("步骤失败时不能标记为成功")
	}
	if response.StoppedStepID != fixture.step.ID.String() {
		t.Fatalf("停止步骤不符: %s", response.StoppedStepID)
	}
	if response.StopReason == "" {
		t.Fatal("停止必须给出原因")
	}
	if response.Steps[0].Error == "" {
		t.Fatal("失败步骤必须给出可诊断原因")
	}
}

// TestExecuteStepsAPIRejectsQueuedTask 验证任务状态不允许时返回 409。
func TestExecuteStepsAPIRejectsQueuedTask(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, true)
	recorder := fixture.post(t, fixture.executeStepsPath(fixture.task.ID.String()), nil)
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
	if fixture.port.calls != 0 {
		t.Fatal("状态不允许时不应调用执行器")
	}
}

// TestExecuteStepsAPIRejectsInvalidTimeout 验证超时参数越界返回 400。
func TestExecuteStepsAPIRejectsInvalidTimeout(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	recorder := fixture.post(t, fixture.executeStepsPath(fixture.task.ID.String()), map[string]any{
		"timeout_seconds": -1,
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if fixture.port.calls != 0 {
		t.Fatal("参数非法时不应调用执行器")
	}
}

// TestExecuteStepsAPIRejectsGet 验证顺序执行接口只接受 POST。
func TestExecuteStepsAPIRejectsGet(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	request := httptest.NewRequest(
		http.MethodGet,
		fixture.executeStepsPath(fixture.task.ID.String()),
		nil,
	)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("期望 405，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestExecuteStepsAPINotFound 验证不存在的任务返回 404。
func TestExecuteStepsAPINotFound(t *testing.T) {
	t.Parallel()
	fixture := newStepAPIFixture(t, false)
	recorder := fixture.post(t, fixture.executeStepsPath(domain.NewID().String()), nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if fixture.port.calls != 0 {
		t.Fatal("目标不存在时不应调用执行器")
	}
}
