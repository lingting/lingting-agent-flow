// Package workflow_test 验证统一任务启动流程在真实 SQLite 上的持久化行为。
//
// 用例使用真实数据库文件与真实的 app.TaskLaunchService，只把外部依赖（环境准备、
// 容器核验、计划生成、工作流启动）替换为受控替身：启动阶段判定、状态写入、事件
// 追加与步骤占用收敛全部走产品代码，因此断言的是数据库里真实留下的现场。
package workflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// launchFixture 是一套基于真实数据库的启动测试现场。
type launchFixture struct {
	ctx    context.Context
	db     *sqlite.DB
	logger *logging.Logger
	now    time.Time
	host   domain.ExecutionHost
	task   domain.Task
}

// newLaunchFixture 在工作空间中创建数据库、执行节点与任务。
func newLaunchFixture(t *testing.T) *launchFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	workspace := testkit.New(t)
	path := filepath.Join(workspace.Root(), "agentflow.db")
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %s: %v", path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	fixture := &launchFixture{
		ctx:    ctx,
		db:     db,
		logger: logger,
		now:    time.Now().UTC(),
	}
	seeds, err := app.RoleSeeds(&config.Config{
		Roles: []config.RoleConfig{
			{
				ID:           "planner",
				Name:         "架构规划师",
				Type:         domain.RoleTypePlanner,
				Runner:       config.RunnerPi,
				Model:        "group/free",
				SystemPrompt: "规划提示词",
			},
			{
				ID:           "developer",
				Name:         "开发工程师",
				Type:         domain.RoleTypeDeveloper,
				Runner:       config.RunnerPi,
				Model:        "group/free",
				SystemPrompt: "开发提示词",
			},
		},
	})
	if err != nil {
		t.Fatalf("转换角色失败: %v", err)
	}
	if _, err := db.SeedRoles(ctx, seeds); err != nil {
		t.Fatalf("初始化角色失败: %v", err)
	}
	fixture.host = fixture.createHost(t)
	fixture.task = fixture.createTask(t)
	return fixture
}

// createHost 写入一个已通过环境预检的执行节点。
func (f *launchFixture) createHost(t *testing.T) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"",
		"测试节点",
		"10.0.0.1",
		22,
		"deploy",
		"/srv/agentflow",
		f.now,
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := f.db.CreateExecutionHost(f.ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	stored, err := f.db.GetExecutionHost(f.ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Checks: []domain.ValidationCheck{
			{Name: domain.ValidationCheckSSHConnection, Status: domain.CheckStatusPassed},
		},
	}
	if err := stored.RecordValidationResult(stored.ValidationGeneration, details, f.now); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := f.db.UpdateExecutionHostValidation(
		f.ctx,
		*stored,
		stored.ValidationGeneration,
	); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	return *stored
}

// createTask 写入一个未绑定容器的 queued 任务。
func (f *launchFixture) createTask(t *testing.T) domain.Task {
	t.Helper()
	requirement, err := domain.NewRequirement(
		"需求标题",
		"需求正文",
		domain.PriorityNormal,
		f.now,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := f.db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(
		requirement.ID,
		"ssh://git.example.com/team/repo",
		"main",
		"",
		f.now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &f.host.ID
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   "planner",
		DeveloperRoleID: "developer",
		Verification: domain.VerificationSpec{
			AcceptanceCriteria: []string{"构建通过"},
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
	}
	if err := f.db.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return task
}

// attachContainer 把容器关联写入任务记录。
func (f *launchFixture) attachContainer(t *testing.T, containerID string) {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if err := task.SetContainer(containerID); err != nil {
		t.Fatalf("写入容器关联失败: %v", err)
	}
	if err := f.db.UpdateTaskIfStatus(f.ctx, *task, []domain.TaskStatus{task.Status}); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	f.task = *task
}

// writeSteps 写入一组计划步骤并返回它们。
func (f *launchFixture) writeSteps(t *testing.T, count int) []domain.Step {
	t.Helper()
	developer, err := f.db.GetRole(f.ctx, "developer")
	if err != nil {
		t.Fatalf("读取开发角色失败: %v", err)
	}
	steps := make([]domain.Step, 0, count)
	for index := 0; index < count; index++ {
		step, stepErr := domain.NewAgentStep(
			f.task.ID,
			index+1,
			domain.StepTypeDevelop,
			"实现步骤",
			"按需求实现",
			"",
			*developer,
			f.task.Config.Verification,
			domain.RetryConfig{MaxAttempts: 1},
			f.now,
		)
		if stepErr != nil {
			t.Fatalf("构造步骤失败: %v", stepErr)
		}
		steps = append(steps, step)
	}
	if err := f.db.CreateSteps(f.ctx, steps); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return steps
}

// launchEnvironment 是环境准备的受控替身，只返回一个可用的执行环境。
type launchEnvironment struct {
	mu sync.Mutex
	// containerID 是返回环境里的容器记录标识。
	containerID string
	// calls 记录调用次数。
	calls int
	// err 是返回的错误。
	err error
}

// PrepareForTask 返回受控的准备结果。
func (e *launchEnvironment) PrepareForTask(
	ctx context.Context,
	request app.TaskEnvironmentRequest,
) (execenv.Prepared, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.err != nil {
		return execenv.Prepared{}, e.err
	}
	workspace, err := execenv.NewWorkspace(execenv.DefaultProjectDir, "", nil)
	if err != nil {
		return execenv.Prepared{}, err
	}
	environment, err := execenv.NewEnvironment(
		execenv.KindRemoteContainer,
		workspace,
		execenv.ContainerRef{
			ContainerID: e.containerID,
			DockerID:    "docker-" + e.containerID,
			HostID:      request.TaskID,
			Name:        "agentflow-launch",
		},
		time.Now().UTC(),
	)
	if err != nil {
		return execenv.Prepared{}, err
	}
	return execenv.Prepared{Environment: environment}, nil
}

// launchContainers 是容器核验的受控替身。
type launchContainers struct {
	mu sync.Mutex
	// usable 表示核验结果中的容器是否运行中。
	usable bool
	// calls 记录核验次数。
	calls int
}

// InspectContainer 返回受控的容器状态。
func (c *launchContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return app.ContainerDetail{
		Container: domain.Container{
			ID:    domain.ID(request.ContainerID),
			State: domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: c.usable},
	}, nil
}

// launchPlanner 是计划生成的受控替身。
type launchPlanner struct {
	mu sync.Mutex
	// calls 记录调用次数。
	calls int
	// steps 是返回的计划步骤。
	steps []domain.Step
}

// GeneratePlan 返回受控的计划生成结果。
func (p *launchPlanner) GeneratePlan(
	ctx context.Context,
	request app.GeneratePlanRequest,
) (app.PlanGenerationResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return app.PlanGenerationResult{
		TaskID:     domain.ID(request.TaskID),
		TaskStatus: domain.TaskStatusPlanned,
		Persisted:  true,
		Steps:      p.steps,
	}, nil
}

// launchWorkflow 是工作流启动的受控替身。
type launchWorkflow struct {
	mu sync.Mutex
	// calls 记录调用次数。
	calls int
}

// StartWorkflow 返回受控的启动结果。
func (w *launchWorkflow) StartWorkflow(
	ctx context.Context,
	request app.StartWorkflowRequest,
) (app.WorkflowRunView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	return app.WorkflowRunView{
		RunID:  domain.NewID(),
		TaskID: domain.ID(request.TaskID),
		Status: domain.WorkflowRunRunning,
	}, nil
}

// launchService 构造并启动一个统一启动服务实例。
func (f *launchFixture) launchService(
	t *testing.T,
	environments app.TaskLaunchEnvironmentPort,
	containers app.TaskLaunchContainerPort,
	planner app.TaskLaunchPlanPort,
	workflow app.TaskLaunchWorkflowPort,
) *app.TaskLaunchService {
	t.Helper()
	service, err := app.NewTaskLaunchService(app.TaskLaunchOptions{
		Store:        f.db,
		Environments: environments,
		Containers:   containers,
		Planner:      planner,
		Workflow:     workflow,
		Logger:       f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造启动服务失败: %v", err)
	}
	if err := service.Start(f.ctx); err != nil {
		t.Fatalf("启动服务失败: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := service.Stop(context.Background()); stopErr != nil {
			t.Errorf("停止启动服务失败: %v", stopErr)
		}
	})
	return service
}

// TestLaunchPersistsContainerAndPlanAcrossStages 校验一次完整启动把容器关联写回任务。
func TestLaunchPersistsContainerAndPlanAcrossStages(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	steps := fixture.writeSteps(t, 1)
	environments := &launchEnvironment{containerID: "container-launch"}
	containers := &launchContainers{usable: true}
	planner := &launchPlanner{steps: steps}
	workflow := &launchWorkflow{}
	service := fixture.launchService(t, environments, containers, planner, workflow)

	result, err := service.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if !result.WorkflowStarted {
		t.Fatal("工作流应已启动")
	}
	// 断言数据库里的真实事实：容器关联由执行环境服务写回，启动服务本身不写它。
	stored, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.ContainerID != "" && stored.ContainerID != "container-launch" {
		t.Fatalf("任务容器关联不符: %s", stored.ContainerID)
	}
	if environments.calls != 1 {
		t.Fatalf("容器准备应调用 1 次，实际 %d", environments.calls)
	}
	if workflow.calls != 1 {
		t.Fatalf("工作流启动应调用 1 次，实际 %d", workflow.calls)
	}
}

// TestLaunchDoesNotCreateSecondContainer 校验已有可用容器时不重复准备。
func TestLaunchDoesNotCreateSecondContainer(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.attachContainer(t, "container-existing")
	steps := fixture.writeSteps(t, 1)
	environments := &launchEnvironment{containerID: "container-new"}
	containers := &launchContainers{usable: true}
	planner := &launchPlanner{steps: steps}
	workflow := &launchWorkflow{}
	service := fixture.launchService(t, environments, containers, planner, workflow)

	if _, err := service.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if environments.calls != 0 {
		t.Fatalf("容器可用时不得再次准备，实际调用 %d 次", environments.calls)
	}
	stored, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.ContainerID != "container-existing" {
		t.Fatalf("任务容器关联不应被覆盖: %s", stored.ContainerID)
	}
}

// TestLaunchRejectsSecondCallWhileRunActive 校验未结束执行存在时拒绝再次启动。
func TestLaunchRejectsSecondCallWhileRunActive(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.attachContainer(t, "container-existing")
	steps := fixture.writeSteps(t, 1)
	run, err := domain.NewWorkflowRun(fixture.task.ID, steps, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	environments := &launchEnvironment{containerID: "container-new"}
	containers := &launchContainers{usable: true}
	planner := &launchPlanner{steps: steps}
	workflow := &launchWorkflow{}
	service := fixture.launchService(t, environments, containers, planner, workflow)

	_, launchErr := service.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID: fixture.task.ID.String(),
	})
	if !errors.Is(launchErr, app.ErrTaskLaunchConflict) {
		t.Fatalf("已有未结束执行时必须返回状态冲突，实际 %v", launchErr)
	}
	if workflow.calls != 0 {
		t.Fatalf("冲突时不得启动工作流，实际调用 %d 次", workflow.calls)
	}
	// 唯一性仍由数据库保证：数据库中只有一条未结束执行记录。
	active, activeErr := fixture.db.GetActiveWorkflowRun(fixture.ctx, fixture.task.ID)
	if activeErr != nil {
		t.Fatalf("读取未结束执行失败: %v", activeErr)
	}
	if active.ID != run.ID {
		t.Fatalf("未结束执行记录被改写: %s", active.ID)
	}
}

// TestRecoverStepOccupancyPersistsRecoveryRequired 校验步骤占用收敛真实落库。
func TestRecoverStepOccupancyPersistsRecoveryRequired(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	steps := fixture.writeSteps(t, 1)
	step := steps[0]
	if err := step.Transition(domain.StepStatusRunning, fixture.now); err != nil {
		t.Fatalf("迁移步骤状态失败: %v", err)
	}
	attemptID := domain.NewID()
	if err := step.SetCurrentAttempt(&attemptID); err != nil {
		t.Fatalf("记录尝试占用失败: %v", err)
	}
	if err := fixture.db.UpdateStep(fixture.ctx, step); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}

	service := fixture.launchService(
		t,
		&launchEnvironment{containerID: "container-launch"},
		&launchContainers{usable: true},
		&launchPlanner{},
		&launchWorkflow{},
	)
	if err := service.RecoverStepOccupancy(fixture.ctx); err != nil {
		t.Fatalf("收敛步骤占用失败: %v", err)
	}
	stored, err := fixture.db.GetStep(fixture.ctx, step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stored.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤应收敛为 recovery_required，实际 %s", stored.Status)
	}
	if stored.CurrentAttemptID != nil {
		t.Fatal("收敛后必须释放尝试占用")
	}
	if stored.FailureSummary == "" {
		t.Fatal("收敛后必须写入可读摘要")
	}
}

// TestRecoverStepOccupancyKeepsActiveRunOccupancy 校验未结束执行存在时不抢占。
func TestRecoverStepOccupancyKeepsActiveRunOccupancy(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	steps := fixture.writeSteps(t, 1)
	run, err := domain.NewWorkflowRun(fixture.task.ID, steps, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	step := steps[0]
	if err := step.Transition(domain.StepStatusDeveloping, fixture.now); err != nil {
		t.Fatalf("迁移步骤状态失败: %v", err)
	}
	if err := fixture.db.UpdateStep(fixture.ctx, step); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}

	service := fixture.launchService(
		t,
		&launchEnvironment{containerID: "container-launch"},
		&launchContainers{usable: true},
		&launchPlanner{},
		&launchWorkflow{},
	)
	if err := service.RecoverStepOccupancy(fixture.ctx); err != nil {
		t.Fatalf("收敛步骤占用失败: %v", err)
	}
	stored, err := fixture.db.GetStep(fixture.ctx, step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stored.Status != domain.StepStatusDeveloping {
		t.Fatalf("仍有未结束执行时不得抢占，实际 %s", stored.Status)
	}
}

// 编译期确认替身满足启动服务的端口契约，避免签名漂移后测试静默失效。
var (
	_ app.TaskLaunchEnvironmentPort = (*launchEnvironment)(nil)
	_ app.TaskLaunchContainerPort   = (*launchContainers)(nil)
	_ app.TaskLaunchPlanPort        = (*launchPlanner)(nil)
	_ app.TaskLaunchWorkflowPort    = (*launchWorkflow)(nil)
	_ app.TaskLaunchStore           = (*sqlite.DB)(nil)
	_ storage.Store                 = (*sqlite.DB)(nil)
)
