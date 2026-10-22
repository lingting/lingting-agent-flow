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

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// taskWorkflowAPIRunner 是任务级工作流接口测试使用的步骤闭环替身。
type taskWorkflowAPIRunner struct {
	mu       sync.Mutex
	db       *sqlite.DB
	calls    int
	blocked  chan struct{}
	requests []app.ExecuteAndValidateRequest
}

// ExecuteAndValidate 返回受控的闭环结果并把步骤收口为已完成。
func (r *taskWorkflowAPIRunner) ExecuteAndValidate(
	ctx context.Context,
	request app.ExecuteAndValidateRequest,
) (app.ExecuteAndValidateResult, error) {
	r.mu.Lock()
	r.calls++
	r.requests = append(r.requests, request)
	blocked := r.blocked
	db := r.db
	r.mu.Unlock()
	if blocked != nil {
		select {
		case <-blocked:
		case <-ctx.Done():
			return app.ExecuteAndValidateResult{}, ctx.Err()
		}
	}
	stepID := domain.ID(request.StepID)
	completeWorkflowAPIStep(ctx, db, stepID)
	return app.ExecuteAndValidateResult{
		TaskID:              domain.ID(request.TaskID),
		StepID:              stepID,
		AttemptID:           domain.NewID(),
		Outcome:             domain.StepExecutionCompleted,
		ValidationRan:       true,
		ValidationStatus:    domain.ValidationRunStatusCompleted,
		ValidationVerdict:   domain.VerificationVerdictPassed,
		ValidationPassed:    true,
		ValidationPersisted: true,
		Verification:        domain.StepVerificationPassed,
		StepStatus:          domain.StepStatusCompleted,
		Verified:            true,
		Finalized:           true,
	}, nil
}

// completeWorkflowAPIStep 按真实闭环的迁移路径把步骤收口为已完成。
func completeWorkflowAPIStep(ctx context.Context, db *sqlite.DB, stepID domain.ID) {
	step, err := db.GetStep(ctx, stepID)
	if err != nil {
		return
	}
	for _, next := range []domain.StepStatus{
		domain.StepStatusRunning,
		domain.StepStatusVerifying,
		domain.StepStatusCompleted,
	} {
		if step.Status == next {
			continue
		}
		if err := step.Transition(next, time.Now().UTC()); err != nil {
			return
		}
	}
	_ = step.SetCurrentAttempt(nil)
	_ = db.UpdateStep(ctx, *step)
}

// taskWorkflowAPIContainer 是容器核验的受控替身。
type taskWorkflowAPIContainer struct{}

// InspectContainer 返回运行中的受管理容器。
func (taskWorkflowAPIContainer) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   "host-1",
			Name:     "agentflow-workflow-api",
			DockerID: "docker-" + request.ContainerID,
			State:    domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: true},
	}, nil
}

// taskWorkflowAPICodeSaver 是代码保存的受控替身。
type taskWorkflowAPICodeSaver struct {
	mu     sync.Mutex
	record domain.CodeSaveRecord
	calls  int
}

// SaveCode 返回受控的代码保存结果。
func (s *taskWorkflowAPICodeSaver) SaveCode(
	ctx context.Context,
	request app.CodeSaveRequest,
) (app.CodeSaveResult, error) {
	s.mu.Lock()
	s.calls++
	record := s.record
	s.mu.Unlock()
	return app.CodeSaveResult{Result: codesaver.Result{Record: record}}, nil
}

// taskWorkflowAPIFixture 是一套可直接调用工作流接口的测试数据。
type taskWorkflowAPIFixture struct {
	db       *sqlite.DB
	runner   *taskWorkflowAPIRunner
	service  *app.TaskWorkflowService
	handler  http.Handler
	task     domain.Task
	steps    []domain.Step
	ctx      context.Context
	blocked  chan struct{}
	codeSave *taskWorkflowAPICodeSaver
}

// newTaskWorkflowAPIFixture 在工作空间中准备任务与工作流接口。
func newTaskWorkflowAPIFixture(t *testing.T, stepCount int) *taskWorkflowAPIFixture {
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
	role, err := domain.NewRole(
		"开发角色",
		domain.RoleTypeDeveloper,
		"group/free",
		"提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
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
	task.ContainerID = "container-1"
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		DeveloperRoleID: role.ID,
		PlannerRoleID:   role.ID,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	steps := make([]domain.Step, 0, stepCount)
	for index := 0; index < stepCount; index++ {
		step, stepErr := domain.NewAgentStep(
			task.ID,
			index+1,
			domain.StepTypeDevelop,
			"实现接口",
			"按需求实现",
			"",
			role,
			domain.VerificationSpec{
				Commands: []domain.VerificationCommand{{Name: "build", Command: "go"}},
			},
			domain.RetryConfig{MaxAttempts: 1},
			now,
		)
		if stepErr != nil {
			t.Fatalf("构造步骤失败: %v", stepErr)
		}
		steps = append(steps, step)
	}
	if err := db.CreateSteps(ctx, steps); err != nil {
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
		if err := db.UpdateTask(ctx, task); err != nil {
			t.Fatalf("更新任务失败: %v", err)
		}
	}
	session, sessionErr := domain.NewSession(
		domain.SessionScope{TaskID: task.ID},
		role,
		"pi",
		"group/free",
		now,
	)
	if sessionErr != nil {
		t.Fatalf("构造会话失败: %v", sessionErr)
	}
	if err := session.MarkRunning("pi-session-1"); err != nil {
		t.Fatalf("标记会话运行失败: %v", err)
	}
	// 收尾要求原始会话已保存并归档：没有归档结论的会话不算归档完成。
	session.Completeness = domain.SessionCompletenessComplete
	session.ExportStatus = domain.SessionExportNotAttempted
	session.JSONLPath = "sessions/session.jsonl"
	session.ArchiveStatus = domain.SessionArchiveSucceeded
	session.ArchivePath = "sessions/archive.jsonl"
	session.RunStatus = domain.SessionRunCompleted
	if err := session.Complete(now); err != nil {
		t.Fatalf("完成会话失败: %v", err)
	}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatalf("写入会话失败: %v", err)
	}
	runner := &taskWorkflowAPIRunner{db: db}
	codeSaver := &taskWorkflowAPICodeSaver{record: domain.CodeSaveRecord{
		ID:           domain.NewID(),
		TaskID:       task.ID.String(),
		ContainerID:  "container-1",
		RemoteName:   "origin",
		TaskBranch:   "agent/" + task.ID.String(),
		CommitStatus: domain.CommitStatusSucceeded,
		CommitSHA:    "0123456789012345678901234567890123456789",
		PushStatus:   domain.PushStatusSucceeded,
		MergeStatus:  domain.MergeStatusNotRequested,
	}}
	service, err := app.NewTaskWorkflowService(app.TaskWorkflowOptions{
		Store:      db,
		Steps:      runner,
		Containers: taskWorkflowAPIContainer{},
		CodeSaver:  codeSaver,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造工作流编排器失败: %v", err)
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startCancel()
	if err := service.Start(startCtx); err != nil {
		t.Fatalf("启动工作流编排器失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if err := service.Stop(stopCtx); err != nil {
			t.Errorf("停止工作流编排器失败: %v", err)
		}
	})
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: db, Logger: logger.Slog()})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{
		Tasks:    tasks,
		Workflow: service,
		Logger:   logger.Slog(),
	})
	return &taskWorkflowAPIFixture{
		db:       db,
		runner:   runner,
		service:  service,
		handler:  handler,
		task:     task,
		steps:    steps,
		ctx:      ctx,
		codeSave: codeSaver,
	}
}

// do 发起一次接口请求。
func (f *taskWorkflowAPIFixture) do(
	t *testing.T,
	method, path string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

// workflowStatus 查询工作流状态响应。
func (f *taskWorkflowAPIFixture) workflowStatus(t *testing.T) httpapi.WorkflowStatusResponse {
	t.Helper()
	recorder := f.do(t, http.MethodGet,
		httpapi.PathTasks+"/"+f.task.ID.String()+"/workflow", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("查询工作流状态失败: %d %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.WorkflowStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析状态响应失败: %v", err)
	}
	return response
}

// waitSettled 等待工作流停止推进或结束。
func (f *taskWorkflowAPIFixture) waitSettled(t *testing.T) httpapi.WorkflowStatusResponse {
	t.Helper()
	const timeout = 20 * time.Second
	deadline := time.Now().Add(timeout)
	var last httpapi.WorkflowStatusResponse
	for time.Now().Before(deadline) {
		last = f.workflowStatus(t)
		if last.HasRun && last.Run != nil {
			switch last.Run.Status {
			case "completed", "cancelled", "failed", "blocked", "recovery_required":
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待工作流停止超时，最后一次状态: %+v", last.Run)
	return last
}

// TestWorkflowAPILifecycle 验证启动、查询与完成的完整流程。
func TestWorkflowAPILifecycle(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 2)
	recorder := fixture.do(t, http.MethodPost,
		httpapi.PathTasks+"/"+fixture.task.ID.String()+"/execute-workflow",
		httpapi.StartWorkflowRequest{Prompt: "补充说明"})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("启动工作流应返回 202，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var started httpapi.WorkflowRunResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
		t.Fatalf("解析启动响应失败: %v", err)
	}
	if started.RunID == "" || started.TaskID != fixture.task.ID.String() {
		t.Fatalf("启动响应缺少标识: %+v", started)
	}
	if !started.CodeSaverAvailable {
		t.Fatal("启动响应应如实说明代码保存能力可用")
	}
	final := fixture.waitSettled(t)
	if final.Run == nil || final.Run.Status != "completed" {
		t.Fatalf("工作流应完成，实际 %+v", final.Run)
	}
	if final.Run.CompletedSteps != 2 || final.Run.TotalSteps != 2 {
		t.Fatalf("进度不正确: %+v", final.Run)
	}
	if !final.Run.StatusTerminal {
		t.Fatal("完成状态应标记为终态")
	}
	if final.TaskStatus != string(domain.TaskStatusCompleted) {
		t.Fatalf("任务应完成，实际 %s", final.TaskStatus)
	}
}

// TestWorkflowAPIStatusWithoutRun 验证没有执行记录时返回真实空状态。
func TestWorkflowAPIStatusWithoutRun(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	status := fixture.workflowStatus(t)
	if status.HasRun {
		t.Fatal("尚未启动工作流时不应返回执行记录")
	}
	if status.Run != nil {
		t.Fatal("没有执行记录时 run 应为空")
	}
	if len(status.Steps) != 1 {
		t.Fatalf("步骤快照应包含计划步骤，实际 %d", len(status.Steps))
	}
	if status.Steps[0].Status != string(domain.StepStatusPending) {
		t.Fatalf("步骤应为待执行，实际 %s", status.Steps[0].Status)
	}
}

// TestWorkflowAPIRejectsUnknownPath 验证未知工作流路径返回 404 而不是页面。
func TestWorkflowAPIRejectsUnknownPath(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	for _, path := range []string{
		httpapi.PathTasks + "/" + fixture.task.ID.String() + "/workflow/extra",
		httpapi.PathTasks + "/" + fixture.task.ID.String() + "/execute-workflow-typo",
	} {
		recorder := fixture.do(t, http.MethodGet, path, nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("未知路径 %s 应返回 404，实际 %d", path, recorder.Code)
		}
		if contentType := recorder.Header().Get("Content-Type"); contentType == "" ||
			contentType[:len("application/json")] != "application/json" {
			t.Fatalf("未知路径必须返回 JSON 错误，实际 %s", contentType)
		}
	}
}

// TestWorkflowAPIRejectsWrongMethod 验证工作流接口只接受约定的方法。
func TestWorkflowAPIRejectsWrongMethod(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, httpapi.PathTasks + "/" + fixture.task.ID.String() + "/execute-workflow"},
		{http.MethodGet, httpapi.PathTasks + "/" + fixture.task.ID.String() + "/cancel-workflow"},
		{http.MethodGet, httpapi.PathTasks + "/" + fixture.task.ID.String() + "/resume-workflow"},
		{http.MethodPost, httpapi.PathTasks + "/" + fixture.task.ID.String() + "/workflow"},
	}
	for _, testCase := range cases {
		testCase := testCase
		recorder := fixture.do(t, testCase.method, testCase.path, nil)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s 应返回 405，实际 %d", testCase.method, testCase.path, recorder.Code)
		}
	}
}

// TestWorkflowAPIRejectsDuplicateStart 验证重复启动返回状态冲突。
func TestWorkflowAPIRejectsDuplicateStart(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	path := httpapi.PathTasks + "/" + fixture.task.ID.String() + "/execute-workflow"
	if recorder := fixture.do(t, http.MethodPost, path, nil); recorder.Code != http.StatusAccepted {
		t.Fatalf("首次启动应成功，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	fixture.waitSettled(t)
	// 已完成的任务不能再次启动：它不再处于可执行状态。
	recorder := fixture.do(t, http.MethodPost, path, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("重复启动应返回 409，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestWorkflowAPIRejectsInvalidTimeout 验证超时参数越界返回 400。
func TestWorkflowAPIRejectsInvalidTimeout(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	recorder := fixture.do(t, http.MethodPost,
		httpapi.PathTasks+"/"+fixture.task.ID.String()+"/execute-workflow",
		httpapi.StartWorkflowRequest{TimeoutSeconds: -1})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("超时为负数应返回 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestWorkflowAPIRejectsUnknownTask 验证任务不存在返回 404。
func TestWorkflowAPIRejectsUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	recorder := fixture.do(t, http.MethodGet,
		httpapi.PathTasks+"/unknown-task/workflow", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知任务应返回 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestWorkflowAPICancelRequiresRun 验证没有在途工作流时取消返回 404。
func TestWorkflowAPICancelRequiresRun(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	recorder := fixture.do(t, http.MethodPost,
		httpapi.PathTasks+"/"+fixture.task.ID.String()+"/cancel-workflow", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("没有在途工作流时应返回 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestWorkflowAPIResumeRequiresRunID 验证恢复缺少 run_id 时返回 400。
func TestWorkflowAPIResumeRequiresRunID(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowAPIFixture(t, 1)
	recorder := fixture.do(t, http.MethodPost,
		httpapi.PathTasks+"/"+fixture.task.ID.String()+"/resume-workflow",
		httpapi.ResumeWorkflowRequest{})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺少 run_id 应返回 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}
