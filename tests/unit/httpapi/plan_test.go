// Package httpapi_test 提供计划生成接口的协议测试。
//
// 用例只验证协议转换与路径分发：计划生成由受控替身返回，不访问真实 Pi、模型、
// SSH 或 Docker。
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// planAPIContainer 是计划生成路径上的容器替身。
type planAPIContainer struct {
	// finalText 是 Planner 返回的最终文本。
	finalText string
	// status 覆盖运行结果。
	status agent.RunStatus
	// sessions 非空时把会话记录写入数据库。
}

// validPlanAPIText 是一份合法的计划输出。
const validPlanAPIText = `{"summary":"分两步实现需求","steps":[` +
	`{"id":"step-1","title":"实现接口","objective":"按需求实现接口",` +
	`"instructions":"只修改当前工作区","acceptance_criteria":["接口可以编译"]},` +
	`{"id":"step-2","title":"补齐测试","objective":"为接口补齐测试",` +
	`"instructions":"不要修改构建脚本","acceptance_criteria":["测试可以运行"]}]}`

// InspectContainer 返回运行中的受管理容器。
func (c *planAPIContainer) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   "host-1",
			Name:     "agentflow-plan-api",
			DockerID: "docker-" + request.ContainerID,
			State:    domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: true},
	}, nil
}

// RunAgent 返回受控的 Agent 调用结果。
func (c *planAPIContainer) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	now := time.Now().UTC()
	sessionID := domain.ID(strings.TrimSpace(request.SessionID))
	if sessionID.IsEmpty() {
		sessionID = domain.NewID()
	}
	status := c.status
	if status == "" {
		status = agent.RunCompleted
	}
	result := agent.RunResult{
		SessionID:    sessionID.String(),
		InvocationID: request.InvocationID,
		Status:       status,
		FinalText:    c.finalText,
		ExitCode:     0,
		StartedAt:    now,
		FinishedAt:   now,
		Session: agent.SessionFiles{
			JSONLPath:    "sessions/" + sessionID.String() + "/session.jsonl",
			Completeness: agent.SessionComplete,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	return app.ContainerAgentResult{Result: result}, nil
}

// planAPIFixture 是一套可发起计划生成请求的接口测试数据。
type planAPIFixture struct {
	db      *sqlite.DB
	service *app.TaskWorkflowService
	handler http.Handler
	task    domain.Task
	ctx     context.Context
}

// newPlanAPIFixture 在工作空间中准备任务与接口处理器。
func newPlanAPIFixture(t *testing.T, finalText string) *planAPIFixture {
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
	now := time.Now().UTC()
	planner, err := domain.NewRole(
		"规划角色",
		domain.RoleTypePlanner,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(domain.RoleTypePlanner),
		now,
	)
	if err != nil {
		t.Fatalf("构造规划角色失败: %v", err)
	}
	if createErr := db.CreateRole(ctx, planner); createErr != nil {
		t.Fatalf("写入规划角色失败: %v", createErr)
	}
	developer, err := domain.NewRole(
		"开发角色",
		domain.RoleTypeDeveloper,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		now,
	)
	if err != nil {
		t.Fatalf("构造开发角色失败: %v", err)
	}
	if createErr := db.CreateRole(ctx, developer); createErr != nil {
		t.Fatalf("写入开发角色失败: %v", createErr)
	}
	host := apiHost(t, db, ctx, now)
	host = markAPIHostNormal(t, db, ctx, host)
	requirement, err := domain.NewRequirement("需求标题", "需求正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if createErr := db.CreateRequirement(ctx, requirement); createErr != nil {
		t.Fatalf("写入需求失败: %v", createErr)
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
		Verification: domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
	}
	if createErr := db.CreateTask(ctx, task); createErr != nil {
		t.Fatalf("写入任务失败: %v", createErr)
	}
	sessions, err := app.NewSessionService(db, workspace.SessionRoot(), logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	service, err := app.NewTaskWorkflowService(app.TaskWorkflowOptions{
		Store:      db,
		Steps:      &taskWorkflowAPIRunner{db: db},
		Containers: &planAPIContainer{finalText: finalText},
		Sessions:   sessions,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造工作流编排器失败: %v", err)
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startCancel()
	if startErr := service.Start(startCtx); startErr != nil {
		t.Fatalf("启动工作流编排器失败: %v", startErr)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if stopErr := service.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止工作流编排器失败: %v", stopErr)
		}
	})
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: db, Logger: logger.Slog()})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	return &planAPIFixture{
		db:      db,
		service: service,
		handler: httpapi.NewHandler(httpapi.Deps{
			Tasks:    tasks,
			Workflow: service,
			Logger:   logger.Slog(),
		}),
		task: task,
		ctx:  ctx,
	}
}

// do 发起一次接口请求。
func (f *planAPIFixture) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
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

// planPath 返回计划生成接口路径。
func (f *planAPIFixture) planPath() string {
	return httpapi.PathTasks + "/" + f.task.ID.String() + "/plan"
}

// TestPlanAPIPersistsPlan 校验合法输出返回 200 并给出真实步骤。
func TestPlanAPIPersistsPlan(t *testing.T) {
	t.Parallel()
	fixture := newPlanAPIFixture(t, validPlanAPIText)
	recorder := fixture.do(t, http.MethodPost, fixture.planPath(), httpapi.PlanRequest{
		Prompt:         "补充说明",
		TimeoutSeconds: 60,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("生成计划应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.PlanResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !response.Persisted {
		t.Fatalf("计划应写入数据库: %+v", response)
	}
	if response.TaskStatus != string(domain.TaskStatusPlanned) {
		t.Fatalf("任务状态应为 planned，实际 %s", response.TaskStatus)
	}
	// 计划固定以规划阶段步骤开头，其后是 Planner 生成的开发步骤。
	if len(response.Steps) != 3 {
		t.Fatalf("步骤数量不符: %d", len(response.Steps))
	}
	if response.Steps[0].Type != string(domain.StepTypePlan) {
		t.Fatalf("首个步骤必须是规划阶段: %s", response.Steps[0].Type)
	}
	if response.SessionID == "" {
		t.Fatalf("必须返回规划会话标识")
	}
	for index, step := range response.Steps {
		if len(step.RequiredChecks) == 0 {
			t.Fatalf("步骤 %d 缺少生效的必需检查", step.Sequence)
		}
		expectedType := domain.StepTypeDevelop
		if index == 0 {
			expectedType = domain.StepTypePlan
		}
		if step.Type != string(expectedType) {
			t.Fatalf("步骤 %d 类型不符: %s", step.Sequence, step.Type)
		}
	}
}

// TestPlanAPIRejectsInvalidOutput 校验非法输出映射为参数错误。
func TestPlanAPIRejectsInvalidOutput(t *testing.T) {
	t.Parallel()
	fixture := newPlanAPIFixture(t, "我建议先实现接口。")
	recorder := fixture.do(t, http.MethodPost, fixture.planPath(), httpapi.PlanRequest{})
	if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("非法输出应返回 4xx，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	steps, err := fixture.db.ListStepsByTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("非法输出不得留下步骤")
	}
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status == domain.TaskStatusPlanned {
		t.Fatalf("非法输出不得让任务进入 planned")
	}
}

// TestPlanAPIMethodNotAllowed 校验非 POST 请求返回 405。
func TestPlanAPIMethodNotAllowed(t *testing.T) {
	t.Parallel()
	fixture := newPlanAPIFixture(t, validPlanAPIText)
	recorder := fixture.do(t, http.MethodGet, fixture.planPath(), nil)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("非 POST 请求应返回 405，实际 %d", recorder.Code)
	}
	if allow := recorder.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow 头不符: %q", allow)
	}
}

// TestPlanAPINegativeTimeout 校验非法超时映射为参数错误。
func TestPlanAPINegativeTimeout(t *testing.T) {
	t.Parallel()
	fixture := newPlanAPIFixture(t, validPlanAPIText)
	recorder := fixture.do(t, http.MethodPost, fixture.planPath(), httpapi.PlanRequest{
		TimeoutSeconds: -1,
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法超时应返回 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestPlanPathNotSwallowedByTaskHandler 校验 plan 路径不被任务处理器当作未知子路径。
func TestPlanPathNotSwallowedByTaskHandler(t *testing.T) {
	t.Parallel()
	fixture := newPlanAPIFixture(t, validPlanAPIText)
	recorder := fixture.do(t, http.MethodPost, fixture.planPath(), httpapi.PlanRequest{})
	// 关键点：响应必须来自计划接口（成功或业务错误），而不是"接口不存在"的 404。
	var parsed httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err == nil {
		if parsed.Error.Code == httpapi.CodeNotFound {
			t.Fatalf("plan 路径被误判为不存在的接口: %s", recorder.Body.String())
		}
	}
}
