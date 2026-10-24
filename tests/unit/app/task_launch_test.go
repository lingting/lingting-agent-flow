// Package app_test 验证统一任务启动服务的阶段编排。
//
// 用例只依赖受控替身：环境准备、计划生成与工作流启动都由替身提供，因此默认执行
// 路径不访问真实 Pi、模型、SSH 或 Docker。断言的重点是"下一步调用了谁""失败后
// 有没有继续往下走""重复请求会不会创建第二个容器"。
package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
	"github.com/lingting/agentflow/internal/storage"
)

// launchTaskStore 是任务、步骤与执行记录的受控替身。
type launchTaskStore struct {
	mu sync.Mutex
	// task 是当前任务记录。
	task domain.Task
	// steps 是当前计划步骤。
	steps []domain.Step
	// activeRun 表示存在未结束的工作流执行记录。
	activeRun bool
	// appendErr 使事件写入失败。
	appendErr error
	// events 记录写入的事件类型。
	events []domain.EventType
	// workspaceRecords 是受控的工作区初始化记录集合。
	workspaceRecords []domain.TaskWorkspaceInitRecord
	// containers 是受控的容器登记记录集合。
	//
	// 空集合表示该任务没有需要先处置的旧容器；非空时启动流程拒绝创建新容器。
	containers []domain.Container
}

// GetTask 返回当前任务记录。
func (s *launchTaskStore) GetTask(
	ctx context.Context,
	id domain.ID,
) (*domain.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task.ID != id {
		return nil, storage.ErrNotFound
	}
	task := s.task
	return &task, nil
}

// ListStepsByTask 返回当前计划步骤。
func (s *launchTaskStore) ListStepsByTask(
	ctx context.Context,
	taskID domain.ID,
) ([]domain.Step, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Step(nil), s.steps...), nil
}

// UpdateTaskIfStatus 记录任务写入并更新当前记录。
func (s *launchTaskStore) UpdateTaskIfStatus(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.task = task
	return nil
}

// AcquireTaskLease 记录一次领取并返回代次 1。
//
// 受控存储不模拟并发竞争：它只保证启动流程拿到的租约身份与任务记录一致。
func (s *launchTaskStore) AcquireTaskLease(
	ctx context.Context,
	request storage.TaskLeaseRequest,
) (domain.TaskLeaseHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.task.LeaseOwner = request.Owner
	s.task.LeaseEpoch = 1
	expires := request.At.Add(request.TTL)
	s.task.LeaseExpiresAt = &expires
	return domain.TaskLeaseHandle{TaskID: request.TaskID, Owner: request.Owner, Epoch: 1}, nil
}

// ReleaseTaskLease 清除受控记录上的租约。
func (s *launchTaskStore) ReleaseTaskLease(
	ctx context.Context,
	handle domain.TaskLeaseHandle,
	at time.Time,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task.LeaseOwner != handle.Owner || s.task.LeaseEpoch != handle.Epoch {
		return false, storage.ErrConflict
	}
	s.task.LeaseOwner = ""
	s.task.LeaseExpiresAt = nil
	return true, nil
}

// UpdateStepIfStatus 记录步骤写入并更新当前记录。
func (s *launchTaskStore) UpdateStepIfStatus(
	ctx context.Context,
	step domain.Step,
	expected []domain.StepStatus,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, current := range s.steps {
		if current.ID != step.ID {
			continue
		}
		s.steps[index] = step
		return nil
	}
	return storage.ErrNotFound
}

// GetActiveWorkflowRun 返回受控的未结束执行记录。
//
// activeRun 表示"仍有一个执行者持有有效执行权"：返回的记录必须是 running 且租约
// 有效，否则它就不再是抢占判据——步骤占用收敛只保护真正在推进的执行。
func (s *launchTaskStore) GetActiveWorkflowRun(
	ctx context.Context,
	taskID domain.ID,
) (*domain.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeRun {
		return nil, storage.ErrNotFound
	}
	expires := time.Now().UTC().Add(10 * time.Minute)
	return &domain.WorkflowRun{
		ID:             domain.NewID(),
		TaskID:         taskID,
		Status:         domain.WorkflowRunRunning,
		WorkerID:       "worker-test",
		LeaseExpiresAt: &expires,
	}, nil
}

// AppendEvent 记录事件类型。
func (s *launchTaskStore) AppendEvent(
	ctx context.Context,
	event domain.Event,
) (domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return domain.Event{}, s.appendErr
	}
	s.events = append(s.events, event.Type)
	return event, nil
}

// ListTasksWithOccupiedSteps 返回受控的占用任务集合。
func (s *launchTaskStore) ListTasksWithOccupiedSteps(
	ctx context.Context,
) ([]domain.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	occupied := make([]domain.ID, 0, 1)
	for _, step := range s.steps {
		switch step.Status {
		case domain.StepStatusRunning, domain.StepStatusDeveloping, domain.StepStatusVerifying:
			occupied = append(occupied, step.TaskID)
			return occupied, nil
		}
	}
	return occupied, nil
}

// ListTaskWorkspaceInitsByTask 返回受控的工作区初始化记录。
func (s *launchTaskStore) ListTaskWorkspaceInitsByTask(
	ctx context.Context,
	taskID string,
) ([]domain.TaskWorkspaceInitRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.TaskWorkspaceInitRecord(nil), s.workspaceRecords...), nil
}

// ListContainers 返回受控的容器登记记录。
//
// 默认返回空集合，表示该任务没有需要先处置的旧容器；用例可以设置
// containers 构造"仍有未处置容器"的现场。
func (s *launchTaskStore) ListContainers(
	ctx context.Context,
	filter domain.ContainerFilter,
) ([]domain.Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Container(nil), s.containers...), nil
}

// launchEnvironment 是环境准备的替身。
type launchEnvironment struct {
	mu sync.Mutex
	// calls 记录准备调用次数。
	calls int
	// prepared 是返回的准备结果。
	prepared execenv.Prepared
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
	return e.prepared, e.err
}

// launchContainers 是容器核验的替身。
type launchContainers struct {
	mu sync.Mutex
	// usable 表示核验结果中的容器是否运行中。
	usable bool
	// err 使核验失败。
	err error
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

// launchWorkspace 是任务级工作区初始化的替身。
type launchWorkspace struct {
	mu sync.Mutex
	// calls 记录初始化调用次数。
	calls int
	// recoverCalls 记录收敛调用次数。
	recoverCalls int
	// result 是返回的初始化结果。
	result app.TaskWorkspaceResult
	// err 是返回的错误。
	err error
}

// InitializeTaskWorkspace 返回受控的初始化结果。
func (w *launchWorkspace) InitializeTaskWorkspace(
	ctx context.Context,
	request app.TaskWorkspaceRequest,
) (app.TaskWorkspaceResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	return w.result, w.err
}

// RecoverInterrupted 记录收敛调用并返回 nil。
func (w *launchWorkspace) RecoverInterrupted(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recoverCalls++
	return nil
}

// launchPlanner 是计划生成的替身。
type launchPlanner struct {
	mu sync.Mutex
	// calls 记录调用次数。
	calls int
	// result 是返回的结果。
	result app.PlanGenerationResult
	// err 是返回的错误。
	err error
}

// GeneratePlan 返回受控的计划生成结果。
func (p *launchPlanner) GeneratePlan(
	ctx context.Context,
	request app.GeneratePlanRequest,
) (app.PlanGenerationResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.result, p.err
}

// launchWorkflow 是工作流启动的替身。
type launchWorkflow struct {
	mu sync.Mutex
	// calls 记录调用次数。
	calls int
	// result 是返回的执行记录视图。
	result app.WorkflowRunView
	// err 是返回的错误。
	err error
}

// StartWorkflow 返回受控的启动结果。
func (w *launchWorkflow) StartWorkflow(
	ctx context.Context,
	request app.StartWorkflowRequest,
) (app.WorkflowRunView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	return w.result, w.err
}

// launchFixture 是一套可直接发起启动的受控现场。
type launchFixture struct {
	store        *launchTaskStore
	environments *launchEnvironment
	containers   *launchContainers
	planner      *launchPlanner
	workspace    *launchWorkspace
	workflow     *launchWorkflow
	service      *app.TaskLaunchService
}

// newLaunchFixture 构造启动服务与受控替身。
func newLaunchFixture(t *testing.T) *launchFixture {
	t.Helper()
	environment, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
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
	hostID := domain.NewID()
	task.HostID = &hostID
	task.Config = domain.TaskConfig{
		Goal:            "实现功能",
		Environment:     environment,
		PlannerRoleID:   domain.NewID(),
		DeveloperRoleID: domain.NewID(),
	}
	fixture := &launchFixture{
		store:        &launchTaskStore{task: task},
		environments: &launchEnvironment{},
		containers:   &launchContainers{usable: true},
		planner:      &launchPlanner{},
		workspace:    &launchWorkspace{},
		workflow:     &launchWorkflow{},
	}
	service, err := app.NewTaskLaunchService(app.TaskLaunchOptions{
		Store:        fixture.store,
		Environments: fixture.environments,
		Containers:   fixture.containers,
		Planner:      fixture.planner,
		Workspace:    fixture.workspace,
		Workflow:     fixture.workflow,
	})
	if err != nil {
		t.Fatalf("构造启动服务失败: %v", err)
	}
	fixture.service = service
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("启动服务失败: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := service.Stop(context.Background()); stopErr != nil {
			t.Errorf("停止启动服务失败: %v", stopErr)
		}
	})
	return fixture
}

// setContainer 把容器关联写入任务记录。
func (f *launchFixture) setContainer(containerID string) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	f.store.task.ContainerID = containerID
}

// launchSucceededWorkspaceRecord 构造一条"该容器上工作区已就绪"的记录。
func launchSucceededWorkspaceRecord(
	t *testing.T,
	taskID domain.ID,
	containerID string,
) domain.TaskWorkspaceInitRecord {
	t.Helper()
	record, err := domain.NewTaskWorkspaceInitRecord(
		"",
		taskID.String(),
		containerID,
		"docker-"+containerID,
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造工作区初始化记录失败: %v", err)
	}
	record.Status = domain.TaskWorkspaceInitStatusSucceeded
	record.CommitSHA = "1111111111111111111111111111111111111111"
	return record
}
func TestLaunchRunsContainerThenPlanThenWorkflow(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	step := launchFixtureStep(t, fixture.store.task.ID, domain.StepStatusPending)
	fixture.planner.result = app.PlanGenerationResult{
		TaskID:     fixture.store.task.ID,
		TaskStatus: domain.TaskStatusPlanned,
		Persisted:  true,
		Steps:      []domain.Step{step},
	}
	fixture.workflow.result = app.WorkflowRunView{
		RunID:  domain.NewID(),
		TaskID: fixture.store.task.ID,
		Status: domain.WorkflowRunRunning,
	}
	fixture.environments.prepared = preparedContainer(t, fixture.store.task.ID)

	result, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if result.Stage != domain.TaskLaunchStageWorkflow {
		t.Fatalf("阶段应为 workflow，实际 %s", result.Stage)
	}
	if !result.WorkflowStarted {
		t.Fatal("工作流应已启动")
	}
	if !result.PlanPersisted {
		t.Fatal("计划应已落库")
	}
	if fixture.environments.calls != 1 {
		t.Fatalf("容器准备应调用 1 次，实际 %d", fixture.environments.calls)
	}
	if fixture.planner.calls != 1 {
		t.Fatalf("计划生成应调用 1 次，实际 %d", fixture.planner.calls)
	}
	if fixture.workflow.calls != 1 {
		t.Fatalf("工作流启动应调用 1 次，实际 %d", fixture.workflow.calls)
	}
}

func TestLaunchSkipsContainerWhenUsableContainerExists(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.setContainer("container-existing")
	step := launchFixtureStep(t, fixture.store.task.ID, domain.StepStatusPending)
	fixture.planner.result = app.PlanGenerationResult{
		TaskID:     fixture.store.task.ID,
		TaskStatus: domain.TaskStatusPlanned,
		Persisted:  true,
		Steps:      []domain.Step{step},
	}
	fixture.workflow.result = app.WorkflowRunView{
		RunID:  domain.NewID(),
		TaskID: fixture.store.task.ID,
		Status: domain.WorkflowRunRunning,
	}
	if _, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// 已有可用容器时不得创建第二个容器：那只会留下无法被任何链路使用的孤儿。
	if fixture.environments.calls != 0 {
		t.Fatalf("容器可用时不得再次准备，实际调用 %d 次", fixture.environments.calls)
	}
}

func TestLaunchSkipsPlanWhenStepsPersisted(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.setContainer("container-existing")
	// 计划已持久化且工作区已就绪：判定应直接进入工作流阶段。
	fixture.store.workspaceRecords = []domain.TaskWorkspaceInitRecord{
		launchSucceededWorkspaceRecord(t, fixture.store.task.ID, "container-existing"),
	}
	fixture.store.steps = []domain.Step{
		launchFixtureStep(t, fixture.store.task.ID, domain.StepStatusPending),
	}
	fixture.workflow.result = app.WorkflowRunView{
		RunID:  domain.NewID(),
		TaskID: fixture.store.task.ID,
		Status: domain.WorkflowRunRunning,
	}
	result, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if result.Stage != domain.TaskLaunchStageWorkflow {
		t.Fatalf("阶段应为 workflow，实际 %s", result.Stage)
	}
	if fixture.planner.calls != 0 {
		t.Fatalf("计划已存在时不得重新生成，实际调用 %d 次", fixture.planner.calls)
	}
}

// TestLaunchRunsContainerThenWorkspaceThenPlanThenWorkflow 校验四阶段顺序。
func TestLaunchRunsContainerThenWorkspaceThenPlanThenWorkflow(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	step := launchFixtureStep(t, fixture.store.task.ID, domain.StepStatusPending)
	fixture.planner.result = app.PlanGenerationResult{
		TaskID:     fixture.store.task.ID,
		TaskStatus: domain.TaskStatusPlanned,
		Persisted:  true,
		Steps:      []domain.Step{step},
	}
	fixture.workspace.result = app.TaskWorkspaceResult{
		Status:    domain.TaskWorkspaceInitStatusSucceeded,
		Succeeded: true,
		Commit:    "1111111111111111111111111111111111111111",
	}
	fixture.workflow.result = app.WorkflowRunView{
		RunID:  domain.NewID(),
		TaskID: fixture.store.task.ID,
		Status: domain.WorkflowRunRunning,
	}
	fixture.environments.prepared = preparedContainer(t, fixture.store.task.ID)

	result, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if result.Stage != domain.TaskLaunchStageWorkflow {
		t.Fatalf("阶段应为 workflow，实际 %s", result.Stage)
	}
	if fixture.environments.calls != 1 {
		t.Fatalf("容器准备应调用 1 次，实际 %d", fixture.environments.calls)
	}
	if fixture.workspace.calls != 1 {
		t.Fatalf("工作区初始化应调用 1 次，实际 %d", fixture.workspace.calls)
	}
	if fixture.planner.calls != 1 {
		t.Fatalf("计划生成应调用 1 次，实际 %d", fixture.planner.calls)
	}
	if fixture.workflow.calls != 1 {
		t.Fatalf("工作流启动应调用 1 次，实际 %d", fixture.workflow.calls)
	}
	if result.WorkspaceStatus != domain.TaskWorkspaceInitStatusSucceeded {
		t.Fatalf("结果应如实报告工作区结论，实际 %s", result.WorkspaceStatus)
	}
	if result.WorkspaceCommit == "" {
		t.Fatal("结果应如实报告工作区 commit")
	}
}

// TestLaunchSkipsWorkspaceWhenAlreadyReady 校验已就绪时不再初始化工作区。
func TestLaunchSkipsWorkspaceWhenAlreadyReady(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.setContainer("container-existing")
	fixture.store.workspaceRecords = []domain.TaskWorkspaceInitRecord{
		launchSucceededWorkspaceRecord(t, fixture.store.task.ID, "container-existing"),
	}
	step := launchFixtureStep(t, fixture.store.task.ID, domain.StepStatusPending)
	fixture.planner.result = app.PlanGenerationResult{
		TaskID:     fixture.store.task.ID,
		TaskStatus: domain.TaskStatusPlanned,
		Persisted:  true,
		Steps:      []domain.Step{step},
	}
	fixture.workflow.result = app.WorkflowRunView{
		RunID:  domain.NewID(),
		TaskID: fixture.store.task.ID,
		Status: domain.WorkflowRunRunning,
	}
	result, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if fixture.workspace.calls != 0 {
		t.Fatalf("工作区已就绪时不得再次初始化，实际调用 %d 次", fixture.workspace.calls)
	}
	if result.Stage != domain.TaskLaunchStageWorkflow {
		t.Fatalf("阶段应为 workflow，实际 %s", result.Stage)
	}
}

// TestLaunchStopsAfterWorkspaceFailure 校验工作区失败后不再生成计划。
func TestLaunchStopsAfterWorkspaceFailure(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.setContainer("container-existing")
	fixture.workspace.err = errors.New("目标目录非空")

	result, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if err == nil {
		t.Fatal("工作区初始化失败时必须返回错误")
	}
	if result.FailureStage != domain.TaskLaunchStageWorkspace.String() {
		t.Fatalf("失败阶段应为 workspace，实际 %s", result.FailureStage)
	}
	if fixture.planner.calls != 0 {
		t.Fatalf("工作区失败后不得生成计划，实际调用 %d 次", fixture.planner.calls)
	}
	if fixture.workflow.calls != 0 {
		t.Fatalf("工作区失败后不得启动工作流，实际调用 %d 次", fixture.workflow.calls)
	}
}

// TestLaunchWithoutWorkspacePortReturnsPrerequisite 校验缺少能力时返回前置条件错误。
func TestLaunchWithoutWorkspacePortReturnsPrerequisite(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.setContainer("container-existing")
	service, err := app.NewTaskLaunchService(app.TaskLaunchOptions{
		Store:        fixture.store,
		Environments: fixture.environments,
		Containers:   fixture.containers,
		Planner:      fixture.planner,
		Workflow:     fixture.workflow,
	})
	if err != nil {
		t.Fatalf("构造启动服务失败: %v", err)
	}
	if startErr := service.Start(context.Background()); startErr != nil {
		t.Fatalf("启动服务失败: %v", startErr)
	}
	t.Cleanup(func() {
		if stopErr := service.Stop(context.Background()); stopErr != nil {
			t.Errorf("停止启动服务失败: %v", stopErr)
		}
	})
	_, launchErr := service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if !errors.Is(launchErr, app.ErrTaskLaunchPrerequisite) {
		t.Fatalf("缺少工作区能力时必须返回前置条件错误，实际 %v", launchErr)
	}
	if fixture.planner.calls != 0 {
		t.Fatalf("缺少能力时不得生成计划，实际调用 %d 次", fixture.planner.calls)
	}
}

// TestLaunchRecoversWorkspaceOnStart 校验启动时收敛工作区初始化残留。
func TestLaunchRecoversWorkspaceOnStart(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	if fixture.workspace.recoverCalls != 1 {
		t.Fatalf("启动时应收敛一次工作区残留，实际 %d 次", fixture.workspace.recoverCalls)
	}
}
func TestLaunchStopsAfterContainerFailure(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.environments.err = errors.New("镜像构建失败")

	result, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if err == nil {
		t.Fatal("容器准备失败时必须返回错误")
	}
	if result.FailureStage != domain.TaskLaunchStageContainer.String() {
		t.Fatalf("失败阶段应为 container，实际 %s", result.FailureStage)
	}
	if fixture.planner.calls != 0 {
		t.Fatalf("容器失败后不得生成计划，实际调用 %d 次", fixture.planner.calls)
	}
	if fixture.workflow.calls != 0 {
		t.Fatalf("容器失败后不得启动工作流，实际调用 %d 次", fixture.workflow.calls)
	}
}

func TestLaunchStopsAfterPlanFailure(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.setContainer("container-existing")
	fixture.planner.result = app.PlanGenerationResult{
		TaskID:       fixture.store.task.ID,
		TaskStatus:   domain.TaskStatusFailed,
		FailureStage: "parse_output",
	}
	fixture.planner.err = errors.New("计划输出不是合法 JSON")

	result, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if err == nil {
		t.Fatal("计划失败时必须返回错误")
	}
	if !errors.Is(err, app.ErrTaskLaunchFailed) {
		t.Fatalf("错误类别应为启动失败，实际 %v", err)
	}
	if result.FailureStage != "parse_output" {
		t.Fatalf("失败阶段应保留下层给出的 parse_output，实际 %s", result.FailureStage)
	}
	if fixture.workflow.calls != 0 {
		t.Fatalf("计划失败后不得启动工作流，实际调用 %d 次", fixture.workflow.calls)
	}
}

func TestLaunchRejectsActiveWorkflowRun(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.store.activeRun = true
	_, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if !errors.Is(err, app.ErrTaskLaunchConflict) {
		t.Fatalf("已有未结束执行时必须返回状态冲突，实际 %v", err)
	}
	if fixture.environments.calls != 0 || fixture.planner.calls != 0 || fixture.workflow.calls != 0 {
		t.Fatal("冲突时不得产生任何远程调用")
	}
}

func TestLaunchRejectsUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	_, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: domain.NewID().String(),
	})
	if !errors.Is(err, app.ErrTaskLaunchNotFound) {
		t.Fatalf("任务不存在时必须返回不存在错误，实际 %v", err)
	}
}

func TestLaunchRejectsStoppedService(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	if err := fixture.service.Stop(context.Background()); err != nil {
		t.Fatalf("停止服务失败: %v", err)
	}
	_, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID: fixture.store.task.ID.String(),
	})
	if !errors.Is(err, app.ErrTaskLaunchStopped) {
		t.Fatalf("服务已停止时必须返回停止错误，实际 %v", err)
	}
	if fixture.environments.calls != 0 || fixture.planner.calls != 0 || fixture.workflow.calls != 0 {
		t.Fatal("服务已停止时不得产生任何远程调用")
	}
}

func TestLaunchRejectsNegativeTimeout(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	_, err := fixture.service.Launch(context.Background(), app.TaskLaunchRequest{
		TaskID:  fixture.store.task.ID.String(),
		Timeout: -time.Second,
	})
	if !errors.Is(err, app.ErrTaskLaunchInvalidInput) {
		t.Fatalf("负超时必须返回参数错误，实际 %v", err)
	}
}

func TestRecoverStepOccupancyConvergesIdleOccupancy(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	step := launchFixtureStep(t, fixture.store.task.ID, domain.StepStatusRunning)
	fixture.store.steps = []domain.Step{step}

	if err := fixture.service.RecoverStepOccupancy(context.Background()); err != nil {
		t.Fatalf("收敛步骤占用失败: %v", err)
	}
	fixture.store.mu.Lock()
	recovered := fixture.store.steps[0]
	fixture.store.mu.Unlock()
	if recovered.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤应收敛为 recovery_required，实际 %s", recovered.Status)
	}
	if recovered.CurrentAttemptID != nil {
		t.Fatal("收敛后必须释放尝试占用")
	}
	if recovered.FailureSummary == "" {
		t.Fatal("收敛后必须写入可读摘要")
	}
}

func TestRecoverStepOccupancySkipsActiveRun(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.store.activeRun = true
	fixture.store.steps = []domain.Step{
		launchFixtureStep(t, fixture.store.task.ID, domain.StepStatusDeveloping),
	}
	if err := fixture.service.RecoverStepOccupancy(context.Background()); err != nil {
		t.Fatalf("收敛步骤占用失败: %v", err)
	}
	fixture.store.mu.Lock()
	status := fixture.store.steps[0].Status
	fixture.store.mu.Unlock()
	if status != domain.StepStatusDeveloping {
		t.Fatalf("仍有未结束执行时不得抢占，实际 %s", status)
	}
}

// launchFixtureStep 构造一条属于指定任务的开发步骤。
func launchFixtureStep(
	t *testing.T,
	taskID domain.ID,
	status domain.StepStatus,
) domain.Step {
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
	step.Status = status
	return step
}

// preparedContainer 构造一个准备成功的执行环境结果。
//
// 只填容器标识与工作区：启动服务只关心"是否拿到可用环境"，不解析镜像细节。
func preparedContainer(t *testing.T, taskID domain.ID) execenv.Prepared {
	t.Helper()
	workspace, err := execenv.NewWorkspace(execenv.DefaultProjectDir, "", nil)
	if err != nil {
		t.Fatalf("构造工作区失败: %v", err)
	}
	environment, err := execenv.NewEnvironment(
		execenv.KindRemoteContainer,
		workspace,
		execenv.ContainerRef{
			ContainerID: domain.NewID().String(),
			DockerID:    "docker-" + taskID.String(),
			HostID:      domain.NewID().String(),
			Name:        "agentflow-test",
		},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造执行环境失败: %v", err)
	}
	return execenv.Prepared{Environment: environment}
}
