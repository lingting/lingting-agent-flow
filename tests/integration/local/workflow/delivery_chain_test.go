// Package workflow_test 验证"修复后交付"链路在真实持久化现场上的完整成立。
//
// 本文件覆盖 P1-M4 的完成定义：一条任务从"计划步骤就绪"经 Developer 开发、
// 确定性验证失败、修复重验成功、全部步骤完成，进入代码保存、会话归档核验与
// 任务收尾，最终由 domain.DecideWorkflowCloseout 判定为 completed。
//
// 用例使用真实 SQLite、真实 app.TaskWorkflowService、真实 app.StepExecutor、
// 真实 app.ValidationService、真实 app.StepWorkflowService 与真实
// app.SessionService；只把容器内 Pi 调用、容器内命令执行、容器核验与外部 Git
// 操作替换为受控替身。因此断言的对象是数据库里真实留下的 Attempt / Session /
// Validation Run / CodeSave 记录与任务、工作流状态，而不是方法的返回值。
package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 交付链路用例使用的固定标识。
const (
	deliveryContainerID = "container-delivery"
	deliveryDockerID    = "docker-container-delivery"
	// deliveryCommitSHA 是受控代码保存返回的提交标识。
	deliveryCommitSHA = "3333333333333333333333333333333333333333"
)

// deliveryCheckReply 是一条受控的容器内检查结果。
type deliveryCheckReply struct {
	exitCode int
	stdout   string
	stderr   string
	started  bool
	certain  bool
	err      error
}

// deliveryContainers 同时满足容器核验、容器内 Agent 调用与容器内命令执行三个端口。
//
// 它不写数据库、不启动进程：会话记录由真实的 app.SessionService 写入，检查结果
// 按调用顺序返回，使"第二轮验证是否真的重新执行了检查"这类断言可以直接比较次数。
type deliveryContainers struct {
	mu sync.Mutex
	// running 表示容器核验结论中的运行状态。
	running bool
	// inspectCalls 统计容器核验次数。
	inspectCalls int
	// agentReplies 按调用顺序返回的 Agent 结果。
	agentReplies []deliveryAgentReply
	// agentRequests 记录收到的全部 Agent 调用请求。
	agentRequests []app.ContainerAgentRequest
	// checkSequence 按调用顺序返回检查结果；为空时全部通过。
	checkSequence []deliveryCheckReply
	// execRequests 记录收到的检查命令执行请求。
	execRequests []app.ExecRequest
}

// deliveryAgentReply 是一次受控 Agent 调用的结果。
type deliveryAgentReply struct {
	status       agent.RunStatus
	finalText    string
	failure      string
	completeness agent.SessionCompleteness
	err          error
}

// InspectContainer 返回受控的容器状态。
func (c *deliveryContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inspectCalls++
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   "host-delivery",
			Name:     "agentflow-delivery",
			DockerID: deliveryDockerID,
			State:    domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: c.running},
	}, nil
}

// RunAgent 返回受控的 Agent 调用结果。
//
// 它刻意复用调用方给出的 SessionID：真实执行端口必须让 Session 文件与数据库
// 记录指向同一次会话，替身同样不得自行生成标识。
func (c *deliveryContainers) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	c.mu.Lock()
	index := len(c.agentRequests)
	c.agentRequests = append(c.agentRequests, request)
	reply := deliveryAgentReply{status: agent.RunCompleted, finalText: "已完成"}
	if index < len(c.agentReplies) {
		reply = c.agentReplies[index]
	}
	c.mu.Unlock()
	now := time.Now().UTC()
	sessionID := domain.ID(request.SessionID)
	if sessionID.IsEmpty() {
		return app.ContainerAgentResult{}, errors.New("执行端口必须复用调用方给出的会话标识")
	}
	completeness := reply.completeness
	if completeness == "" {
		if reply.status == agent.RunCompleted {
			completeness = agent.SessionComplete
		} else {
			completeness = agent.SessionPartial
		}
	}
	result := agent.RunResult{
		SessionID:    sessionID.String(),
		InvocationID: request.InvocationID,
		Status:       reply.status,
		FinalText:    reply.finalText,
		Failure:      reply.failure,
		ExitCode:     0,
		StartedAt:    now,
		FinishedAt:   now,
		Session: agent.SessionFiles{
			JSONLPath:    "sessions/" + sessionID.String() + "/session.jsonl",
			Completeness: completeness,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	return app.ContainerAgentResult{Result: result}, reply.err
}

// Exec 返回受控的检查命令结果。
func (c *deliveryContainers) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	c.mu.Lock()
	c.execRequests = append(c.execRequests, request)
	index := len(c.execRequests) - 1
	reply := deliveryCheckReply{exitCode: 0, stdout: "ok", started: true, certain: true}
	if index < len(c.checkSequence) {
		reply = c.checkSequence[index]
	}
	c.mu.Unlock()
	status := sshclient.CommandStatusSucceeded
	switch {
	case reply.err != nil && !reply.started:
		status = sshclient.CommandStatusConnectionError
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
		Duration: time.Millisecond,
	}}, reply.err
}

// counts 返回三类调用的次数快照。
func (c *deliveryContainers) counts() (inspect, agentCalls, execCalls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inspectCalls, len(c.agentRequests), len(c.execRequests)
}

// agentPrompts 返回收到的全部提示词。
func (c *deliveryContainers) agentPrompts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	prompts := make([]string, 0, len(c.agentRequests))
	for _, request := range c.agentRequests {
		prompts = append(prompts, request.Prompt)
	}
	return prompts
}

// deliveryCodeSaver 是代码保存端口的受控替身。
//
// 它不执行真实 Git：只返回受控记录并把调用次数记录下来。真实 Git 行为已经在
// tests/integration/local/codesaver/ 中用真实仓库覆盖，这里关注的是编排器如何
// 消费保存结果并据此决定任务是否完成。
type deliveryCodeSaver struct {
	mu sync.Mutex
	// record 是返回的保存记录，零值时按"已安全保存"构造。
	record domain.CodeSaveRecord
	// err 是返回的错误。
	err error
	// calls 记录调用次数。
	calls int
}

// SaveCode 返回受控的代码保存结果。
func (s *deliveryCodeSaver) SaveCode(
	ctx context.Context,
	request app.CodeSaveRequest,
) (app.CodeSaveResult, error) {
	s.mu.Lock()
	s.calls++
	record := s.record
	err := s.err
	s.mu.Unlock()
	if record.ID.IsEmpty() && err == nil {
		record = deliveryCodeSaveRecord()
	}
	result := app.CodeSaveResult{Result: codesaver.Result{Record: record}}
	if err != nil {
		return result, err
	}
	return result, nil
}

// callCount 返回代码保存调用次数。
func (s *deliveryCodeSaver) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// deliveryCodeSaveRecord 构造一条"已安全保存"的代码保存记录。
func deliveryCodeSaveRecord() domain.CodeSaveRecord {
	return domain.CodeSaveRecord{
		ID:             domain.NewID(),
		ContainerID:    deliveryContainerID,
		RemoteName:     "origin",
		TaskBranch:     "agent/task",
		CommitStatus:   domain.CommitStatusSucceeded,
		CommitSHA:      deliveryCommitSHA,
		PushStatus:     domain.PushStatusSucceeded,
		MergeStatus:    domain.MergeStatusNotRequested,
		PersistStatus:  domain.PersistStatusPersisted,
		RecoveryStatus: domain.RecoveryStatusUnverified,
	}
}

// deliveryStore 包装真实存储，只把指定的读写方法替换为可控失败。
type deliveryStore struct {
	app.TaskWorkflowStore
	// sessionErr 使会话查询失败，用于区分"查询失败"与"会话确实缺失"。
	sessionErr error
	// sessions 非空时替代真实会话查询结果。
	sessions []domain.Session
	// updateRunErr 使执行记录写入失败。
	updateRunErr error
	// failUpdateWhenStatus 非空时只让写入该目标状态的尝试失败。
	//
	// 它用于构造"阶段写入与任务状态写入都成功，只有最终收尾记录写不进去"的现场：
	// 若让所有写入都失败，编排器在启动阶段就报错，根本走不到收尾。
	failUpdateWhenStatus domain.WorkflowRunStatus
	// failTaskUpdateStatus 非空时只让写入该目标状态的任务更新失败。
	//
	// 它用于构造"执行记录已确认完成、任务状态写入失败"的现场，验证收尾顺序把
	// 执行记录当作栅栏后不会再伪造任务成功。
	failTaskUpdateStatus domain.TaskStatus
	// writes 记录执行记录与任务状态的写入顺序。
	//
	// 断言写入顺序是"先执行记录、后任务"，这是收尾协议的核心不变量。
	writes []string
}

// UpdateTaskIfStatus 在配置了目标状态时返回错误，并记录写入顺序。
func (s *deliveryStore) UpdateTaskIfStatus(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
) error {
	s.writes = append(s.writes, "task:"+string(task.Status))
	if s.failTaskUpdateStatus != "" && task.Status == s.failTaskUpdateStatus {
		return errors.New("任务状态写入失败")
	}
	return s.TaskWorkflowStore.UpdateTaskIfStatus(ctx, task, expected)
}

// UpdateTaskLeaseGuard 与 UpdateTaskIfStatus 记录同一份写入顺序。
//
// 收尾的成功终态写入经过租约栅栏接口，顺序不变量因此必须在两个入口上都成立。
func (s *deliveryStore) UpdateTaskLeaseGuard(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
	handle domain.TaskLeaseHandle,
) error {
	s.writes = append(s.writes, "task:"+string(task.Status))
	if s.failTaskUpdateStatus != "" && task.Status == s.failTaskUpdateStatus {
		return errors.New("任务状态写入失败")
	}
	return s.TaskWorkflowStore.UpdateTaskLeaseGuard(ctx, task, expected, handle)
}

// writeOrder 返回写入顺序的快照。
func (s *deliveryStore) writeOrder() []string {
	return append([]string(nil), s.writes...)
}

// ListSessionsByTask 在配置了替身时返回替身结果。
func (s *deliveryStore) ListSessionsByTask(
	ctx context.Context,
	taskID domain.ID,
) ([]domain.Session, error) {
	if s.sessionErr != nil {
		return nil, s.sessionErr
	}
	if s.sessions != nil {
		return append([]domain.Session(nil), s.sessions...), nil
	}
	return s.TaskWorkflowStore.ListSessionsByTask(ctx, taskID)
}

// UpdateWorkflowRun 在配置了错误时返回该错误。
//
// 它是"执行记录写不进去"这条路径的注入点：失败来自存储层而不是业务代码，
// 因此验证的是真实的错误传播与"不得把收尾报告为完成"的语义。
func (s *deliveryStore) UpdateWorkflowRun(
	ctx context.Context,
	run domain.WorkflowRun,
) error {
	s.writes = append(s.writes, "run:"+string(run.Status))
	if s.updateRunErr != nil {
		return s.updateRunErr
	}
	if s.failUpdateWhenStatus != "" && run.Status == s.failUpdateWhenStatus {
		return errors.New("数据库写入失败")
	}
	return s.TaskWorkflowStore.UpdateWorkflowRun(ctx, run)
}

// deliveryFixture 是一套可直接发起任务级工作流的完整交付现场。
type deliveryFixture struct {
	ctx        context.Context
	db         *sqlite.DB
	store      *deliveryStore
	logger     *logging.Logger
	workspace  *testkit.Workspace
	containers *deliveryContainers
	codeSaver  *deliveryCodeSaver
	executor   *app.StepExecutor
	validation *app.ValidationService
	stepFlow   *app.StepWorkflowService
	workflow   *app.TaskWorkflowService
	sessions   *app.SessionService
	task       domain.Task
	steps      []domain.Step
	now        time.Time
}

// deliveryOptions 允许用例调整交付现场的构造方式。
type deliveryOptions struct {
	// stepCount 是开发步骤数量，默认 1。
	stepCount int
	// retry 是步骤生效的重试策略，零值使用上限 1（不自动修复）。
	retry domain.RetryConfig
	// agentReplies 是 Agent 调用结果序列（Planner 不参与，此处只有 Developer）。
	agentReplies []deliveryAgentReply
	// checkSequence 是检查结果序列。
	checkSequence []deliveryCheckReply
	// codeSaver 为 true 时装配代码保存能力。
	codeSaver bool
	// codeSaveRecord 覆盖代码保存返回的记录。
	codeSaveRecord domain.CodeSaveRecord
	// codeSaveErr 是代码保存返回的错误。
	codeSaveErr error
	// sessionErr 使会话查询失败。
	sessionErr error
	// sessions 覆盖会话查询结果。
	sessions []domain.Session
	// containerRunning 覆盖容器运行状态，默认 true。
	containerStopped bool
	// failTaskUpdateStatus 只让写入该目标状态的任务更新失败。
	failTaskUpdateStatus domain.TaskStatus
}

// newDeliveryFixture 在工作空间中准备一套可执行完整交付链路的测试数据。
func newDeliveryFixture(t *testing.T, options deliveryOptions) *deliveryFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
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
	fixture := &deliveryFixture{
		ctx:       ctx,
		db:        db,
		logger:    logger,
		workspace: workspace,
		now:       now,
	}
	fixture.seed(t, options)
	fixture.assemble(t, options)
	return fixture
}

// seed 写入角色、执行节点、需求、任务与计划步骤。
func (f *deliveryFixture) seed(t *testing.T, options deliveryOptions) {
	t.Helper()
	developer := deliveryRole(t, f.db, f.ctx, domain.RoleTypeDeveloper, "开发角色", f.now)
	host := deliveryHost(t, f.db, f.ctx, f.now)
	host = deliveryMarkHostNormal(t, f.db, f.ctx, host)
	requirement, err := domain.NewRequirement("交付需求", "需求正文", domain.PriorityNormal, f.now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := f.db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", f.now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	seedOwnedContainer(t, f.db, f.ctx, task, deliveryContainerID, deliveryDockerID)
	task.ContainerID = deliveryContainerID
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		DeveloperRoleID: developer.ID,
		PlannerRoleID:   developer.ID,
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
	stepCount := options.stepCount
	if stepCount <= 0 {
		stepCount = 1
	}
	retry := options.retry
	if retry.IsZero() {
		retry = domain.RetryConfig{MaxAttempts: 1}
	}
	steps := make([]domain.Step, 0, stepCount)
	for index := 0; index < stepCount; index++ {
		step, stepErr := domain.NewAgentStep(
			task.ID,
			index+1,
			domain.StepTypeDevelop,
			fmt.Sprintf("实现第 %d 步", index+1),
			"按需求实现",
			"",
			developer,
			task.Config.Verification,
			retry,
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
	// 任务必须先完成环境准备与计划阶段才允许执行步骤：这里按状态机逐级推进，
	// 不直接赋值状态字段。
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
	} {
		if err := task.Transition(status, f.now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", status, err)
		}
		if err := f.db.UpdateTask(f.ctx, task); err != nil {
			t.Fatalf("更新任务失败: %v", err)
		}
	}
	f.task = task
	f.steps = steps
}

// assemble 装配真实服务链路，只把外部执行与外部 Git 替换为受控替身。
func (f *deliveryFixture) assemble(t *testing.T, options deliveryOptions) {
	t.Helper()
	sessions, err := app.NewSessionService(f.db, f.workspace.SessionRoot(), f.logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	f.sessions = sessions
	containers := &deliveryContainers{
		running:       !options.containerStopped,
		agentReplies:  options.agentReplies,
		checkSequence: options.checkSequence,
	}
	f.containers = containers
	gitPort := &deliveryGitPort{
		state: domain.GitWorktreeState{
			StatusRead: true,
			Branch:     "agent/" + f.task.ID.String(),
			Head:       deliveryCommitSHA,
		},
	}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      f.db,
		Containers: containers,
		Sessions:   sessions,
		Git:        gitPort,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	f.executor = executor
	validation, err := app.NewValidationService(app.ValidationOptions{
		Store:      f.db,
		Containers: containers,
		OutputRoot: f.workspace.Dir("validations"),
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
	f.validation = validation
	stepFlow, err := app.NewStepWorkflowService(app.StepWorkflowOptions{
		Steps:      executor,
		Validation: validation,
		Store:      f.db,
		Cycles:     f.db,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造单步骤闭环协调器失败: %v", err)
	}
	f.stepFlow = stepFlow
	store := &deliveryStore{
		TaskWorkflowStore:    f.db,
		sessionErr:           options.sessionErr,
		sessions:             options.sessions,
		failTaskUpdateStatus: options.failTaskUpdateStatus,
	}
	f.store = store
	codeSaver := &deliveryCodeSaver{record: options.codeSaveRecord, err: options.codeSaveErr}
	f.codeSaver = codeSaver
	workflowOptions := app.TaskWorkflowOptions{
		Store:      store,
		Steps:      stepFlow,
		Containers: containers,
		Logger:     f.logger.Slog(),
	}
	if options.codeSaver {
		workflowOptions.CodeSaver = codeSaver
	}
	workflow, err := app.NewTaskWorkflowService(workflowOptions)
	if err != nil {
		t.Fatalf("构造任务级编排器失败: %v", err)
	}
	f.workflow = workflow
	startServices(t, executor, validation, stepFlow, workflow)
}

// startServices 启动后台服务并在用例结束时逆序停止。
func startServices(t *testing.T, services ...interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}) {
	t.Helper()
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, service := range services {
		if err := service.Start(startCtx); err != nil {
			t.Fatalf("启动后台服务失败: %v", err)
		}
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer stopCancel()
		for index := len(services) - 1; index >= 0; index-- {
			if err := services[index].Stop(stopCtx); err != nil {
				t.Errorf("停止后台服务失败: %v", err)
			}
		}
	})
}

// start 启动任务级工作流。
func (f *deliveryFixture) start(t *testing.T) app.WorkflowRunView {
	t.Helper()
	view, err := f.workflow.StartWorkflow(f.ctx, app.StartWorkflowRequest{
		TaskID: f.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	return view
}

// settle 等待工作流停止自动推进并返回最后的记录。
func (f *deliveryFixture) settle(t *testing.T) app.WorkflowStatusResult {
	t.Helper()
	const timeout = 60 * time.Second
	deadline := time.Now().Add(timeout)
	var last app.WorkflowStatusResult
	for time.Now().Before(deadline) {
		result, err := f.workflow.GetWorkflowStatus(f.ctx, f.task.ID.String())
		if err != nil {
			t.Fatalf("查询工作流状态失败: %v", err)
		}
		last = result
		if result.HasRun && deliverySettled(result.Run.Status) {
			// 收尾的写入顺序是"先执行记录、后任务"：执行记录写成 completed 之后，
			// 任务的成功终态才写入。只有这一步追加等待，因为它是协议规定的必然后续；
			// 停止类结论不会改写任务，因此不能在这里等任务状态变化。
			// 任务仍在推进（verifying / saving）说明收尾的第二半步还没落盘；
			// 一旦任务离开活动状态（已完成或已收敛为待核验），现场就稳定了。
			if result.Run.Status != domain.WorkflowRunCompleted ||
				!result.TaskStatus.IsActive() {
				return result
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待工作流收敛超时，最后一次状态: %+v", last.Run)
	return last
}

// reloadTask 读取数据库中的任务。
func (f *deliveryFixture) reloadTask(t *testing.T) domain.Task {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *task
}

// reloadRun 读取数据库中的工作流执行记录。
func (f *deliveryFixture) reloadRun(t *testing.T, id domain.ID) domain.WorkflowRun {
	t.Helper()
	run, err := f.db.GetWorkflowRun(f.ctx, id)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	return *run
}

// reloadSteps 读取数据库中的步骤。
func (f *deliveryFixture) reloadSteps(t *testing.T) []domain.Step {
	t.Helper()
	steps, err := f.db.ListStepsByTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return steps
}

// attempts 读取某步骤的全部尝试。
func (f *deliveryFixture) attempts(t *testing.T, stepID domain.ID) []domain.Attempt {
	t.Helper()
	attempts, err := f.db.ListAttemptsByStep(f.ctx, stepID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	return attempts
}

// sessions 读取任务的全部会话。
func (f *deliveryFixture) sessionsOf(t *testing.T) []domain.Session {
	t.Helper()
	sessions, err := f.db.ListSessionsByTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	return sessions
}

// validationRuns 读取某次尝试的全部验证记录。
func (f *deliveryFixture) validationRuns(t *testing.T, attemptID domain.ID) []domain.ValidationRun {
	t.Helper()
	runs, err := f.db.ListValidationRunsByAttempt(f.ctx, attemptID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	return runs
}

// events 读取任务的全部事件。
func (f *deliveryFixture) events(t *testing.T) []domain.Event {
	t.Helper()
	events, err := f.db.ListEventsByTask(f.ctx, f.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	return events
}

// waitEvents 在限定时间内等待指定类型的事件全部写入。
//
// 事件与状态写入是两次独立的持久化：状态先落盘，事件随后追加。因此"状态已可见"
// 并不保证"事件已写入"，断言事件必须按截止时间轮询，而不是一次性读取。
func (f *deliveryFixture) waitEvents(t *testing.T, expected ...domain.EventType) {
	t.Helper()
	const timeout = 20 * time.Second
	deadline := time.Now().Add(timeout)
	var missing []domain.EventType
	for time.Now().Before(deadline) {
		seen := make(map[domain.EventType]bool, len(expected))
		for _, event := range f.events(t) {
			seen[event.Type] = true
		}
		missing = missing[:0]
		for _, eventType := range expected {
			if !seen[eventType] {
				missing = append(missing, eventType)
			}
		}
		if len(missing) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待事件超时，缺少: %v", missing)
}

// TestDeliveryChainRepairThenCodeSaveArchiveAndCloseout 验证完整交付链路。
//
// 场景：第 1 步首轮验证失败 → 修复重验通过 → 第 2 步验证通过 → 代码保存成功 →
// 全部会话已归档 → 任务与工作流进入 completed。断言覆盖 Attempt / Session /
// Validation Run / 修复事件 / 代码保存记录之间的关联与历史保留。
func TestDeliveryChainRepairThenCodeSaveArchiveAndCloseout(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 2,
		retry:     domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies: []deliveryAgentReply{
			// 第 1 步：首轮实现 + 依据反馈修复。
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "根据反馈修复"},
			// 第 2 步。
			{status: agent.RunCompleted, finalText: "第二步实现"},
		},
		checkSequence: []deliveryCheckReply{
			{exitCode: 1, stderr: "编译错误: undefined: foo", started: true, certain: true},
			{exitCode: 0, stdout: "ok", started: true, certain: true},
			{exitCode: 0, stdout: "ok", started: true, certain: true},
		},
		codeSaver: true,
	})
	view := fixture.start(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf(
			"修复后交付链路应完成，实际 %s（分类 %s，原因 %s）",
			result.Run.Status,
			result.Run.ErrorClass,
			result.Run.ErrorSummary,
		)
	}
	if result.Run.RunID != view.RunID {
		t.Fatal("完成结论必须写在同一条执行记录上")
	}
	// 数据库是唯一真相来源：任务与执行记录的终态都必须真实落盘。
	stored := fixture.reloadRun(t, result.Run.RunID)
	if stored.Status != domain.WorkflowRunCompleted {
		t.Fatalf("数据库中的执行记录应为 completed，实际 %s", stored.Status)
	}
	task := fixture.reloadTask(t)
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("任务应为 completed，实际 %s", task.Status)
	}
	if task.FinalResult != domain.TaskResultSucceeded {
		t.Fatalf("任务最终结论应为 succeeded，实际 %s", task.FinalResult)
	}
	if task.CurrentStepID != nil || task.CurrentAttemptID != nil {
		t.Fatal("完成的任务必须释放执行占用")
	}
	// 计划步骤全部完成。
	steps := fixture.reloadSteps(t)
	if len(steps) != 2 {
		t.Fatalf("步骤数量不符: %d", len(steps))
	}
	for _, step := range steps {
		if step.Status != domain.StepStatusCompleted {
			t.Fatalf("步骤 %d 应为 completed，实际 %s", step.Sequence, step.Status)
		}
		if step.CurrentAttemptID != nil {
			t.Fatalf("步骤 %d 完成后必须释放执行占用", step.Sequence)
		}
	}
	// 第 1 步经历两次尝试：首轮失败与修复都必须保留为历史记录。
	firstAttempts := fixture.attempts(t, steps[0].ID)
	if len(firstAttempts) != 2 {
		t.Fatalf("第 1 步应产生 2 次尝试，实际 %d", len(firstAttempts))
	}
	if firstAttempts[0].ID == firstAttempts[1].ID {
		t.Fatal("历史尝试不得被覆盖")
	}
	if firstAttempts[0].AttemptNumber == firstAttempts[1].AttemptNumber {
		t.Fatalf(
			"尝试序号必须递增: %d 与 %d",
			firstAttempts[0].AttemptNumber,
			firstAttempts[1].AttemptNumber,
		)
	}
	// 每次尝试都必须关联一条真实落库的 Session。
	for _, attempt := range firstAttempts {
		if len(attempt.SessionIDs) != 1 {
			t.Fatalf("尝试 %s 应关联 1 条会话，实际 %d", attempt.ID, len(attempt.SessionIDs))
		}
	}
	secondAttempts := fixture.attempts(t, steps[1].ID)
	if len(secondAttempts) != 1 {
		t.Fatalf("第 2 步应产生 1 次尝试，实际 %d", len(secondAttempts))
	}
	// 每次尝试都产生独立的 Validation Run，并绑定该轮的 Attempt。
	for _, step := range steps {
		for _, attempt := range fixture.attempts(t, step.ID) {
			runs := fixture.validationRuns(t, attempt.ID)
			if len(runs) != 1 {
				t.Fatalf("尝试 %s 应有 1 条验证记录，实际 %d", attempt.ID, len(runs))
			}
			if runs[0].StepID != step.ID {
				t.Fatalf("验证记录的步骤归属不正确: %s", runs[0].StepID)
			}
		}
	}
	// 会话必须真实落库并已归档：这是收尾判定的必要条件。
	sessions := fixture.sessionsOf(t)
	if len(sessions) != 3 {
		t.Fatalf("应为 3 次 Developer 调用各留下 1 条会话，实际 %d", len(sessions))
	}
	sessionIDs := make(map[domain.ID]struct{}, len(sessions))
	for _, session := range sessions {
		if session.TaskID != fixture.task.ID {
			t.Fatalf("会话归属不正确: %s", session.TaskID)
		}
		if session.StepID == nil || session.AttemptID == nil {
			t.Fatalf("Developer 会话必须挂在步骤与尝试下: %+v", session)
		}
		if session.ArchiveStatus != domain.SessionArchiveSucceeded {
			t.Fatalf("会话 %s 归档状态不符: %s", session.ID, session.ArchiveStatus)
		}
		if session.ArchivePath == "" {
			t.Fatalf("会话 %s 归档成功时必须给出归档位置", session.ID)
		}
		if session.Completeness != domain.SessionCompletenessComplete {
			t.Fatalf("会话 %s 完整性不符: %s", session.ID, session.Completeness)
		}
		sessionIDs[session.ID] = struct{}{}
	}
	// Attempt 关联的会话标识必须都指向真实存在的会话记录。
	for _, step := range steps {
		for _, attempt := range fixture.attempts(t, step.ID) {
			for _, sessionID := range attempt.SessionIDs {
				if _, ok := sessionIDs[sessionID]; !ok {
					t.Fatalf("尝试 %s 关联了不存在的会话 %s", attempt.ID, sessionID)
				}
			}
		}
	}
	// 修复过程必须留下可追溯的轮次事件：首轮未通过与重验通过至少各一条。
	repairRounds := 0
	for _, event := range fixture.events(t) {
		if event.Type == domain.EventTypeRepairRound {
			repairRounds++
		}
	}
	if repairRounds < 2 {
		t.Fatalf("修复循环应至少记录 2 轮事件（首轮未通过 + 通过），实际 %d", repairRounds)
	}
	// 代码保存必须被真实调用且只调用一次：成功路径不重复保存。
	if fixture.codeSaver.callCount() != 1 {
		t.Fatalf("代码保存应调用 1 次，实际 %d", fixture.codeSaver.callCount())
	}
	records, err := fixture.db.ListCodeSaveRecordsByTask(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if len(records) != 0 {
		// 替身不写数据库：这里断言的是"业务代码没有绕过保存端口自行造记录"。
		t.Fatalf("受控替身不应写入代码保存记录，实际 %d 条", len(records))
	}
	// 修复轮次与交付阶段的事件都必须存在。
	//
	// 事件在状态写入之后追加：状态先落盘、事件随后写入，因此按截止时间轮询，
	// 而不是假定"状态一旦可见，事件就一定已经写入"。
	fixture.waitEvents(t,
		domain.EventTypeWorkflowStarted,
		domain.EventTypeStepStarted,
		domain.EventTypeWorkflowStepPassed,
		domain.EventTypeWorkflowCodeSaved,
		domain.EventTypeWorkflowCompleted,
	)
	// 修复轮次与交付阶段之间不得出现"未验证就推进"的痕迹。
	if len(fixture.containers.agentPrompts()) != 3 {
		t.Fatalf("应发生 3 次 Developer 调用，实际 %d", len(fixture.containers.agentPrompts()))
	}
	_, agentCalls, execCalls := fixture.containers.counts()
	if agentCalls != 3 {
		t.Fatalf("Agent 调用次数不符: %d", agentCalls)
	}
	if execCalls != 3 {
		t.Fatalf("确定性检查应执行 3 次（首轮失败 + 重验 + 第 2 步），实际 %d", execCalls)
	}
}

// TestDeliveryChainStopsAtRepairLimit 验证修复达到上限后不进入交付阶段。
func TestDeliveryChainStopsAtRepairLimit(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		retry:     domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "第二次实现"},
			{status: agent.RunCompleted, finalText: "第三次实现"},
		},
		checkSequence: []deliveryCheckReply{
			{exitCode: 1, stderr: "编译错误: undefined: foo", started: true, certain: true},
			{exitCode: 1, stderr: "编译错误: undefined: foo", started: true, certain: true},
			{exitCode: 1, stderr: "编译错误: undefined: foo", started: true, certain: true},
		},
		codeSaver: true,
	})
	fixture.start(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("达到尝试上限应阻塞，实际 %s（%s）", result.Run.Status, result.Run.ErrorClass)
	}
	if result.Run.ErrorClass != "attempts_exhausted" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	// 上限为 2 时不得产生第 3 次 Developer / 验证调用。
	_, agentCalls, execCalls := fixture.containers.counts()
	if agentCalls != 2 {
		t.Fatalf("达到上限后不得继续调用 Developer，实际 %d 次", agentCalls)
	}
	if execCalls != 2 {
		t.Fatalf("达到上限后不得继续执行检查，实际 %d 次", execCalls)
	}
	// 成功路径的能力一律不得被触发。
	if fixture.codeSaver.callCount() != 0 {
		t.Fatalf("修复失败时不得执行代码保存，实际 %d 次", fixture.codeSaver.callCount())
	}
	task := fixture.reloadTask(t)
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("修复失败时任务不得标记为完成")
	}
	// 历史现场必须保留：每次尝试、会话与验证记录都可查询。
	steps := fixture.reloadSteps(t)
	attempts := fixture.attempts(t, steps[0].ID)
	if len(attempts) != 2 {
		t.Fatalf("应保留 2 次尝试记录，实际 %d", len(attempts))
	}
	if sessions := fixture.sessionsOf(t); len(sessions) != 2 {
		t.Fatalf("应保留 2 条会话记录，实际 %d", len(sessions))
	}
	for _, attempt := range attempts {
		if runs := fixture.validationRuns(t, attempt.ID); len(runs) != 1 {
			t.Fatalf("尝试 %s 应保留 1 条验证记录，实际 %d", attempt.ID, len(runs))
		}
	}
}

// TestDeliveryChainStopsWhenCodeSaveFails 验证代码保存明确失败时不判成功。
func TestDeliveryChainStopsWhenCodeSaveFails(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver:   true,
		codeSaveErr: errors.New("推送失败: 远程不可达"),
	})
	fixture.start(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("代码保存失败应阻塞，实际 %s（%s）", result.Run.Status, result.Run.ErrorClass)
	}
	if result.Run.ErrorClass != "code_save_failed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	task := fixture.reloadTask(t)
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("代码保存失败时任务不得标记为完成")
	}
	// 步骤本身确实通过验证：失败来自交付阶段而不是验证阶段。
	for _, step := range fixture.reloadSteps(t) {
		if step.Status != domain.StepStatusCompleted {
			t.Fatalf("步骤 %d 应已完成验证，实际 %s", step.Sequence, step.Status)
		}
	}
	if fixture.codeSaver.callCount() != 1 {
		t.Fatalf("代码保存应被尝试 1 次，实际 %d", fixture.codeSaver.callCount())
	}
}

// TestDeliveryChainStopsWhenCodeSaveIndeterminate 验证保存结果不确定时不判成功。
func TestDeliveryChainStopsWhenCodeSaveIndeterminate(t *testing.T) {
	t.Parallel()
	record := deliveryCodeSaveRecord()
	// 推送结果未知：远程可能已收到提交，必须先核验再决定。
	record.PushStatus = domain.PushStatusUnknown
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver:      true,
		codeSaveRecord: record,
	})
	fixture.start(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("保存结果不确定应需要人工核验，实际 %s（%s）", result.Run.Status, result.Run.ErrorClass)
	}
	if result.Run.ErrorClass != "code_save_indeterminate" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("保存结果不确定时任务不得标记为完成")
	}
}

// TestDeliveryChainStopsWhenCodeSaveRecordUnconfirmed 验证记录未可靠落盘时不判成功。
func TestDeliveryChainStopsWhenCodeSaveRecordUnconfirmed(t *testing.T) {
	t.Parallel()
	record := deliveryCodeSaveRecord()
	record.PersistStatus = domain.PersistStatusUnconfirmed
	record.PersistError = "database is locked"
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver:      true,
		codeSaveRecord: record,
	})
	fixture.start(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("记录未确认应需要人工核验，实际 %s（%s）", result.Run.Status, result.Run.ErrorClass)
	}
	if result.Run.ErrorClass != "code_save_unconfirmed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("记录未确认时任务不得标记为完成")
	}
}

// TestDeliveryChainStopsWithoutCodeSaver 验证缺少代码保存能力时不判成功。
func TestDeliveryChainStopsWithoutCodeSaver(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
	})
	fixture.start(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("缺少代码保存能力应阻塞，实际 %s（%s）", result.Run.Status, result.Run.ErrorClass)
	}
	if result.Run.ErrorClass != "code_saver_unavailable" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("缺少代码保存能力时任务不得标记为完成")
	}
}

// TestDeliveryChainStopsWhenSessionArchiveIncomplete 验证会话归档不完整时不判成功。
func TestDeliveryChainStopsWhenSessionArchiveIncomplete(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		adjust      func(session domain.Session) domain.Session
		errorClass  string
		description string
	}{
		{
			name: "归档失败",
			adjust: func(session domain.Session) domain.Session {
				session.ArchiveStatus = domain.SessionArchiveFailed
				session.ArchivePath = ""
				return session
			},
			errorClass:  "session_not_persisted",
			description: "归档失败时原始会话可能只在容器内",
		},
		{
			name: "归档位置缺失",
			adjust: func(session domain.Session) domain.Session {
				session.ArchivePath = ""
				return session
			},
			errorClass:  "session_not_persisted",
			description: "声称归档成功却没有位置时无法定位原始会话",
		},
		{
			name: "完整性为部分",
			adjust: func(session domain.Session) domain.Session {
				session.Completeness = domain.SessionCompletenessPartial
				return session
			},
			errorClass:  "session_not_persisted",
			description: "只保存部分会话内容不算归档完成",
		},
		{
			name: "完整性为缺失",
			adjust: func(session domain.Session) domain.Session {
				session.Completeness = domain.SessionCompletenessMissing
				session.JSONLPath = ""
				session.ArchiveStatus = domain.SessionArchiveFailed
				session.ArchivePath = ""
				return session
			},
			errorClass:  "session_not_persisted",
			description: "没有可用原始会话文件时不得判定归档完成",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newDeliveryFixture(t, deliveryOptions{
				stepCount: 1,
				agentReplies: []deliveryAgentReply{
					{status: agent.RunCompleted, finalText: "实现完成"},
				},
				codeSaver: true,
			})
			// 会话查询结果由用例注入：数据库的 archive_path 非空约束无法表达
			// "归档成功但位置为空"这类必须被识别出的历史数据。
			session := incompleteSession(t, fixture, testCase.adjust)
			fixture.store.sessions = []domain.Session{session}
			fixture.start(t)
			result := fixture.settle(t)
			if result.Run.Status != domain.WorkflowRunRecoveryRequired {
				t.Fatalf(
					"%s: 期望 recovery_required，实际 %s（%s）",
					testCase.description,
					result.Run.Status,
					result.Run.ErrorClass,
				)
			}
			if result.Run.ErrorClass != testCase.errorClass {
				t.Fatalf("%s: 错误分类不正确: %s", testCase.description, result.Run.ErrorClass)
			}
			if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
				t.Fatalf("%s: 归档不完整时任务不得标记为完成", testCase.description)
			}
		})
	}
}

// TestDeliveryChainStopsWhenSessionQueryFails 验证会话查询失败不被当成"没有会话"。
func TestDeliveryChainStopsWhenSessionQueryFails(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver:  true,
		sessionErr: errors.New("数据库读取失败"),
	})
	fixture.start(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("会话查询失败应需要人工核验，实际 %s（%s）", result.Run.Status, result.Run.ErrorClass)
	}
	if result.Run.ErrorClass != "session_not_persisted" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("会话查询失败时任务不得标记为完成")
	}
}

// TestDeliveryChainKeepsTaskCompletedWhenCloseoutFails 验证收尾写入失败不被报告为成功。
func TestDeliveryChainKeepsTaskCompletedWhenCloseoutFails(t *testing.T) {
	t.Parallel()
	fixture := newDeliveryFixture(t, deliveryOptions{
		stepCount: 1,
		agentReplies: []deliveryAgentReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		codeSaver: true,
	})
	// 只让"收尾后写入 completed"这一步失败：阶段推进与任务状态写入都成功，
	// 从而使"任务已置为 completed 但执行记录最终写入失败"这一现场真实成立。
	fixture.store.failUpdateWhenStatus = domain.WorkflowRunCompleted
	fixture.start(t)
	result := fixture.settle(t)
	// 执行记录没能写成 completed：收尾必须如实报告需要人工核验，不得对外宣布完成。
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf(
			"收尾记录写入失败时应需要人工核验，实际 %s（分类 %s）",
			result.Run.Status,
			result.Run.ErrorClass,
		)
	}
	if result.Run.ErrorClass != "closeout_failed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	// 数据库中的执行记录同样不得是 completed：内存中的终态不能替代数据库事实。
	stored, err := fixture.db.GetWorkflowRun(fixture.ctx, result.Run.RunID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if stored.Status == domain.WorkflowRunCompleted {
		t.Fatal("执行记录最终写入失败时数据库中不得出现 completed")
	}
}

// incompleteSession 构造一条按用例调整后不再满足归档要求的会话记录。
func incompleteSession(
	t *testing.T,
	fixture *deliveryFixture,
	adjust func(session domain.Session) domain.Session,
) domain.Session {
	t.Helper()
	role, err := fixture.db.GetRole(fixture.ctx, fixture.task.Config.DeveloperRoleID)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	session, err := domain.NewSession(
		domain.SessionScope{TaskID: fixture.task.ID},
		*role,
		"pi",
		role.Model,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	if err := session.MarkRunning("pi-session-delivery"); err != nil {
		t.Fatalf("标记会话运行失败: %v", err)
	}
	session.Completeness = domain.SessionCompletenessComplete
	session.ExportStatus = domain.SessionExportNotAttempted
	session.JSONLPath = "sessions/session.jsonl"
	session.ArchiveStatus = domain.SessionArchiveSucceeded
	session.ArchivePath = "sessions/archive.jsonl"
	session.RunStatus = domain.SessionRunCompleted
	return adjust(session)
}

// deliveryRole 写入一个启用中的角色。
func deliveryRole(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	roleType domain.RoleType,
	name string,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		name,
		roleType,
		"group/free",
		"角色提示词",
		domain.DefaultToolsPolicy(roleType),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// deliveryHost 写入一个执行节点（校验状态为 checking）。
func deliveryHost(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"",
		"测试节点",
		"10.0.0.1",
		22,
		"deploy",
		"/srv/agentflow",
		now,
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host
}

// deliveryMarkHostNormal 把执行节点置为已通过环境预检。
func deliveryMarkHostNormal(
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
	if err := stored.RecordValidationResult(stored.ValidationGeneration, details, time.Now().UTC()); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := db.UpdateExecutionHostValidation(ctx, *stored, stored.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	return *stored
}

// deliveryGitPort 是 Git 工作区状态采集的受控替身。
//
// 本用例关注编排与交付链路，因此只返回"已读取、工作区干净"的固定快照；采集失败
// 的语义已在 tests/unit/app/ 的用例中覆盖。
type deliveryGitPort struct {
	state domain.GitWorktreeState
}

// InspectWorktree 返回受控的采集结果。
func (g *deliveryGitPort) InspectWorktree(
	ctx context.Context,
	request app.GitWorktreeRequest,
) (domain.GitWorktreeState, error) {
	state := g.state
	if !state.StatusRead && state.StatusError == "" {
		state = domain.GitWorktreeState{
			StatusRead: true,
			Branch:     "agent/" + request.TaskID,
			Head:       deliveryCommitSHA,
		}
	}
	return state, nil
}

// deliverySettled 判断工作流是否已经停止自动推进。
//
// pending 与 running 都还在推进或尚未开始，不能作为"已停止"接受，否则用例会在
// 编排真正开始之前就通过。
func deliverySettled(status domain.WorkflowRunStatus) bool {
	if status.IsTerminal() {
		return true
	}
	return status == domain.WorkflowRunBlocked || status == domain.WorkflowRunRecoveryRequired
}

// restartWorkflow 停止并重新构造编排器，模拟服务重启后的启动恢复。
//
// 它复用同一份数据库与同一组装配，因此恢复读到的就是真实持久化事实；已启动的
// 依赖服务保持运行，只有编排器本身被重建。
func (f *deliveryFixture) restartWorkflow(t *testing.T) *app.TaskWorkflowService {
	t.Helper()
	stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := f.workflow.Stop(stopCtx); err != nil {
		t.Fatalf("停止编排器失败: %v", err)
	}
	workflow, err := app.NewTaskWorkflowService(app.TaskWorkflowOptions{
		Store:      f.store,
		Steps:      f.stepFlow,
		Containers: f.containers,
		CodeSaver:  f.codeSaver,
		Sessions:   f.sessions,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("重新构造编排器失败: %v", err)
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startCancel()
	if err := workflow.Start(startCtx); err != nil {
		t.Fatalf("重新启动编排器失败: %v", err)
	}
	f.workflow = workflow
	return workflow
}
