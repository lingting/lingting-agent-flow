// Package httpapi_test 提供统一任务启动接口的协议测试。
//
// 用例只验证路径分发、请求体校验与状态码映射：启动服务的三段能力都由受控替身
// 提供，因此默认执行路径不访问真实 Pi、模型、SSH 或 Docker。
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
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// startAPITaskStore 是启动接口路径上的存储替身。
type startAPITaskStore struct {
	// task 是当前任务记录。
	task domain.Task
	// steps 是当前计划步骤。
	steps []domain.Step
	// notFound 使任务读取返回不存在。
	notFound bool
	// activeRun 表示存在未结束的执行记录。
	activeRun bool
	// workspacePending 表示当前容器上还没有成功的工作区初始化记录。
	workspacePending bool
}

// GetTask 返回受控的任务记录。
func (s *startAPITaskStore) GetTask(
	ctx context.Context,
	id domain.ID,
) (*domain.Task, error) {
	if s.notFound || s.task.ID != id {
		return nil, storage.ErrNotFound
	}
	task := s.task
	return &task, nil
}

// ListStepsByTask 返回受控的计划步骤。
func (s *startAPITaskStore) ListStepsByTask(
	ctx context.Context,
	taskID domain.ID,
) ([]domain.Step, error) {
	return append([]domain.Step(nil), s.steps...), nil
}

// UpdateTaskIfStatus 忽略写入：启动接口测试不关心任务状态落库细节。
func (s *startAPITaskStore) UpdateTaskIfStatus(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
) error {
	s.task = task
	return nil
}

// UpdateStepIfStatus 忽略写入。
func (s *startAPITaskStore) UpdateStepIfStatus(
	ctx context.Context,
	step domain.Step,
	expected []domain.StepStatus,
) error {
	return nil
}

// GetActiveWorkflowRun 返回受控的未结束执行记录。
func (s *startAPITaskStore) GetActiveWorkflowRun(
	ctx context.Context,
	taskID domain.ID,
) (*domain.WorkflowRun, error) {
	if !s.activeRun {
		return nil, storage.ErrNotFound
	}
	return &domain.WorkflowRun{ID: domain.NewID(), TaskID: taskID}, nil
}

// AppendEvent 忽略事件写入。
func (s *startAPITaskStore) AppendEvent(
	ctx context.Context,
	event domain.Event,
) (domain.Event, error) {
	return event, nil
}

// ListTasksWithOccupiedSteps 返回空集合：启动接口测试不覆盖恢复路径。
func (s *startAPITaskStore) ListTasksWithOccupiedSteps(
	ctx context.Context,
) ([]domain.ID, error) {
	return nil, nil
}

// ListTaskWorkspaceInitsByTask 返回受控的工作区初始化记录。
//
// 默认返回一条"当前容器上工作区已就绪"的记录：既有用例关注阶段判定与错误映射，
// 不需要每次都为工作区准备单独现场。需要覆盖工作区阶段的用例把 workspacePending
// 置为 true，使判定停在 workspace 阶段。
func (s *startAPITaskStore) ListTaskWorkspaceInitsByTask(
	ctx context.Context,
	taskID string,
) ([]domain.TaskWorkspaceInitRecord, error) {
	if s.workspacePending {
		return nil, nil
	}
	return []domain.TaskWorkspaceInitRecord{
		{
			TaskID:      taskID,
			ContainerID: s.task.ContainerID,
			Status:      domain.TaskWorkspaceInitStatusSucceeded,
			CommitSHA:   "1111111111111111111111111111111111111111",
		},
	}, nil
}

// ListContainers 返回空集合，表示该任务没有需要先处置的旧容器。
func (s *startAPITaskStore) ListContainers(
	ctx context.Context,
	filter domain.ContainerFilter,
) ([]domain.Container, error) {
	return nil, nil
}

// startAPIWorkspace 是任务级工作区初始化的替身。
//
// 默认报告"已就绪"，使既有用例继续覆盖计划与工作流阶段。
type startAPIWorkspace struct {
	// calls 记录初始化调用次数。
	calls int
	// result 是返回的初始化结果，零值时按 succeeded 返回。
	result app.TaskWorkspaceResult
	// err 是返回的错误。
	err error
}

// InitializeTaskWorkspace 返回受控的初始化结果。
func (w *startAPIWorkspace) InitializeTaskWorkspace(
	ctx context.Context,
	request app.TaskWorkspaceRequest,
) (app.TaskWorkspaceResult, error) {
	w.calls++
	if w.err != nil {
		return w.result, w.err
	}
	result := w.result
	if result.Status == "" {
		result.Status = domain.TaskWorkspaceInitStatusSucceeded
		result.Succeeded = true
	}
	return result, nil
}

// RecoverInterrupted 忽略收敛：启动接口测试不覆盖恢复路径。
func (w *startAPIWorkspace) RecoverInterrupted(ctx context.Context) error {
	return nil
}

// startAPIContainers 是容器核验的替身。
type startAPIContainers struct {
	// usable 表示核验结果中的容器是否运行中。
	usable bool
	// err 使核验失败。
	err error
}

// InspectContainer 返回受控的容器状态。
func (c *startAPIContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	if c.err != nil {
		return app.ContainerDetail{}, c.err
	}
	return app.ContainerDetail{
		Container: domain.Container{
			ID:    domain.ID(request.ContainerID),
			State: domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: c.usable},
	}, nil
}

// startAPIWorkflow 是工作流启动的替身。
type startAPIWorkflow struct {
	// err 是返回的错误。
	err error
}

// StartWorkflow 返回受控的启动结果。
func (w *startAPIWorkflow) StartWorkflow(
	ctx context.Context,
	request app.StartWorkflowRequest,
) (app.WorkflowRunView, error) {
	if w.err != nil {
		return app.WorkflowRunView{}, w.err
	}
	return app.WorkflowRunView{
		RunID:  domain.NewID(),
		TaskID: domain.ID(request.TaskID),
		Status: domain.WorkflowRunRunning,
		Stage:  domain.WorkflowStageStepExecution,
	}, nil
}

// startAPIFixture 是一套可直接发起启动请求的受控现场。
type startAPIFixture struct {
	handler   http.Handler
	store     *startAPITaskStore
	task      domain.Task
	workspace *startAPIWorkspace
}

// startAPITestTask 构造一条可用于启动判定的任务记录。
func startAPITestTask(t *testing.T) domain.Task {
	t.Helper()
	task, err := domain.NewTask(
		domain.NewID(),
		"git@example.com:demo/repo.git",
		"main",
		"",
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	environment, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	hostID := domain.NewID()
	task.HostID = &hostID
	task.ContainerID = "container-existing"
	task.Config = domain.TaskConfig{
		Goal:            "实现功能",
		Environment:     environment,
		PlannerRoleID:   domain.NewID(),
		DeveloperRoleID: domain.NewID(),
	}
	return task
}

// newStartAPIFixture 构造启动接口处理器。
func newStartAPIFixture(t *testing.T, store *startAPITaskStore) *startAPIFixture {
	t.Helper()
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	workspace := &startAPIWorkspace{}
	service, err := app.NewTaskLaunchService(app.TaskLaunchOptions{
		Store:      store,
		Containers: &startAPIContainers{usable: true},
		Workspace:  workspace,
		Workflow:   &startAPIWorkflow{},
	})
	if err != nil {
		t.Fatalf("构造启动服务失败: %v", err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动服务失败: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := service.Stop(context.Background()); stopErr != nil {
			t.Errorf("停止启动服务失败: %v", stopErr)
		}
	})
	return &startAPIFixture{
		handler: httpapi.NewHandler(httpapi.Deps{
			TaskLaunch: service,
			Logger:     logger.Slog(),
		}),
		store:     store,
		task:      store.task,
		workspace: workspace,
	}
}

// startPath 返回统一启动接口路径。
func (f *startAPIFixture) startPath() string {
	return httpapi.PathTasks + "/" + f.task.ID.String() + "/start"
}

// do 发起一次接口请求。
func (f *startAPIFixture) do(
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

// TestStartAPIReachesWorkflowStage 校验计划已就绪时返回 202 并启动编排。
func TestStartAPIReachesWorkflowStage(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	store := &startAPITaskStore{
		task:  task,
		steps: []domain.Step{startAPIStep(t, task.ID)},
	}
	fixture := newStartAPIFixture(t, store)
	recorder := fixture.do(t, http.MethodPost, fixture.startPath(), httpapi.StartTaskRequest{
		Prompt:         "补充说明",
		TimeoutSeconds: 60,
	})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("启动应返回 202，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.StartTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if response.Stage != domain.TaskLaunchStageWorkflow.String() {
		t.Fatalf("阶段应为 workflow，实际 %s", response.Stage)
	}
	if !response.WorkflowStarted {
		t.Fatal("工作流应已启动")
	}
	if response.Run == nil {
		t.Fatal("启动成功后必须返回执行记录")
	}
}

// TestStartAPIReportsWorkspaceStage 校验响应如实报告工作区阶段与结论。
func TestStartAPIReportsWorkspaceStage(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	store := &startAPITaskStore{task: task, workspacePending: true}
	fixture := newStartAPIFixture(t, store)
	fixture.workspace.result = app.TaskWorkspaceResult{
		Status:    domain.TaskWorkspaceInitStatusSucceeded,
		Succeeded: true,
		Commit:    "1111111111111111111111111111111111111111",
	}
	fixture.workspace.err = app.ErrTaskWorkspaceFailed
	recorder := fixture.do(t, http.MethodPost, fixture.startPath(), httpapi.StartTaskRequest{})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("工作区初始化失败应返回 500，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if fixture.workspace.calls != 1 {
		t.Fatalf("工作区初始化应调用 1 次，实际 %d", fixture.workspace.calls)
	}
}

// TestStartAPIMapsWorkspaceUncertainToConflict 校验结果不确定返回 409。
func TestStartAPIMapsWorkspaceUncertainToConflict(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	store := &startAPITaskStore{task: task, workspacePending: true}
	fixture := newStartAPIFixture(t, store)
	fixture.workspace.result = app.TaskWorkspaceResult{
		Status: domain.TaskWorkspaceInitStatusUncertain,
	}
	fixture.workspace.err = app.ErrTaskWorkspaceUncertain
	recorder := fixture.do(t, http.MethodPost, fixture.startPath(), httpapi.StartTaskRequest{})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("结果不确定应返回 409，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestStartAPIMapsWorkspacePrerequisiteToPrecondition 校验前置条件缺失返回 412。
func TestStartAPIMapsWorkspacePrerequisiteToPrecondition(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	store := &startAPITaskStore{task: task, workspacePending: true}
	fixture := newStartAPIFixture(t, store)
	fixture.workspace.err = app.ErrTaskWorkspacePrerequisite
	recorder := fixture.do(t, http.MethodPost, fixture.startPath(), httpapi.StartTaskRequest{})
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("前置条件缺失应返回 412，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestStartAPIMapsWorkspaceNotPersisted 校验未落库返回 execution_not_persisted。
func TestStartAPIMapsWorkspaceNotPersisted(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	store := &startAPITaskStore{task: task, workspacePending: true}
	fixture := newStartAPIFixture(t, store)
	fixture.workspace.err = app.ErrTaskWorkspaceNotPersisted
	recorder := fixture.do(t, http.MethodPost, fixture.startPath(), httpapi.StartTaskRequest{})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("未落库应返回 500，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if response.Error.Code != httpapi.CodeExecutionNotPersisted {
		t.Fatalf("错误码应为 %s，实际 %s", httpapi.CodeExecutionNotPersisted, response.Error.Code)
	}
}

// TestStartAPIMethodNotAllowed 校验非 POST 请求返回 405。
func TestStartAPIMethodNotAllowed(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	fixture := newStartAPIFixture(t, &startAPITaskStore{task: task})
	recorder := fixture.do(t, http.MethodGet, fixture.startPath(), nil)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("非 POST 请求应返回 405，实际 %d", recorder.Code)
	}
	if allow := recorder.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow 头不符: %q", allow)
	}
}

// TestStartAPIRejectsUnknownTask 校验任务不存在映射为 404。
func TestStartAPIRejectsUnknownTask(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	fixture := newStartAPIFixture(t, &startAPITaskStore{task: task, notFound: true})
	recorder := fixture.do(t, http.MethodPost, fixture.startPath(), nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("任务不存在应返回 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestStartAPIRejectsActiveRun 校验已有未结束执行映射为 409。
func TestStartAPIRejectsActiveRun(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	fixture := newStartAPIFixture(t, &startAPITaskStore{task: task, activeRun: true})
	recorder := fixture.do(t, http.MethodPost, fixture.startPath(), nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("已有未结束执行应返回 409，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestStartAPIRejectsNegativeTimeout 校验非法超时映射为参数错误。
func TestStartAPIRejectsNegativeTimeout(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	fixture := newStartAPIFixture(t, &startAPITaskStore{task: task})
	recorder := fixture.do(t, http.MethodPost, fixture.startPath(), httpapi.StartTaskRequest{
		PlanTimeoutSeconds: -1,
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法超时应返回 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestStartAPIWithoutServiceReturns503 校验服务未初始化时返回 503。
func TestStartAPIWithoutServiceReturns503(t *testing.T) {
	t.Parallel()
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog()})
	request := httptest.NewRequest(
		http.MethodPost,
		httpapi.PathTasks+"/"+domain.NewID().String()+"/start",
		bytes.NewReader(nil),
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("服务未初始化应返回 503，实际 %d", recorder.Code)
	}
}

// TestStartAPIMapsPrerequisiteError 校验前置条件缺失映射为 412。
func TestStartAPIMapsPrerequisiteError(t *testing.T) {
	t.Parallel()
	task := startAPITestTask(t)
	task.ContainerID = "container-existing"
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	service, err := app.NewTaskLaunchService(app.TaskLaunchOptions{
		Store: &startAPITaskStore{task: task},
		// 容器核验失败：无法确认容器是否可用，属于前置条件缺失。
		Containers: &startAPIContainers{err: errors.New("远程节点不可达")},
		Workflow:   &startAPIWorkflow{},
	})
	if err != nil {
		t.Fatalf("构造启动服务失败: %v", err)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动服务失败: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := service.Stop(context.Background()); stopErr != nil {
			t.Errorf("停止启动服务失败: %v", stopErr)
		}
	})
	handler := httpapi.NewHandler(httpapi.Deps{TaskLaunch: service, Logger: logger.Slog()})
	request := httptest.NewRequest(
		http.MethodPost,
		httpapi.PathTasks+"/"+task.ID.String()+"/start",
		bytes.NewReader(nil),
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("前置条件缺失应返回 412，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// startAPIStep 构造一条属于指定任务的开发步骤。
func startAPIStep(t *testing.T, taskID domain.ID) domain.Step {
	t.Helper()
	step, err := domain.NewAgentStep(
		taskID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"只修改当前工作区",
		domain.Role{
			ID:           domain.NewID(),
			Name:         "开发工程师",
			Type:         domain.RoleTypeDeveloper,
			Runner:       domain.RoleRunnerPi,
			Model:        "group/free",
			SystemPrompt: "开发提示词",
		},
		domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}},
			},
		},
		domain.RetryConfig{},
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	return step
}
