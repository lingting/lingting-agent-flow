// Package app_test 提供单步骤执行协调器的单元测试。
//
// 用例只依赖数据库与受控替身：不访问真实 Pi、模型、SSH 或 Docker，
// 默认执行路径完全在测试工作空间内完成。
package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// stepExecutorStore 包装真实存储，只把指定的写入方法替换为可控失败。
//
// 它是"执行完成但状态写入失败"这条路径的唯一注入点：业务代码不变，
// 失败来自存储层，因此验证的是真实的错误处理与报告逻辑。
type stepExecutorStore struct {
	app.StepExecutionStore
	updateAttemptErr error
	// beginExecution 覆盖占用逻辑，用于模拟占用失败。
	beginExecution func(
		ctx context.Context,
		request storage.StepExecutionStartRequest,
	) (storage.StepExecutionStart, error)
}

// UpdateAttemptIfStatus 在配置了错误时返回该错误。
func (s *stepExecutorStore) UpdateAttemptIfStatus(
	ctx context.Context,
	attempt domain.Attempt,
	expected []domain.AttemptStatus,
) error {
	if s.updateAttemptErr != nil {
		return s.updateAttemptErr
	}
	return s.StepExecutionStore.UpdateAttemptIfStatus(ctx, attempt, expected)
}

// BeginStepExecution 在配置了替身时使用替身，用于模拟占用失败。
func (s *stepExecutorStore) BeginStepExecution(
	ctx context.Context,
	request storage.StepExecutionStartRequest,
) (storage.StepExecutionStart, error) {
	if s.beginExecution != nil {
		return s.beginExecution(ctx, request)
	}
	return s.StepExecutionStore.BeginStepExecution(ctx, request)
}

// fakeContainerPort 是容器能力的受控替身。
type fakeContainerPort struct {
	mu sync.Mutex
	// inspectErr 使容器核验失败。
	inspectErr error
	// running 表示核验结果中的容器是否在运行。
	running bool
	// detached 表示容器已脱离管理。
	detached bool
	// replies 是依次返回的 Agent 调用结果。
	replies []fakeAgentReply
	// requests 记录收到的 Agent 调用请求。
	requests []app.ContainerAgentRequest
	// calls 统计 Agent 调用次数。
	calls int
	// started 在 RunAgent 开始时关闭，用于同步并发用例。
	started chan struct{}
	// block 使 RunAgent 阻塞直到 ctx 结束。
	block bool
	// sessions 非空时把会话记录写入数据库，模拟真实执行器的持久化行为。
	sessions storage.SessionStore
}

// fakeAgentReply 是一次受控的 Agent 调用结果。
type fakeAgentReply struct {
	status       agent.RunStatus
	finalText    string
	failure      string
	exitCode     int
	completeness agent.SessionCompleteness
	noSession    bool
	err          error
}

// InspectContainer 返回受控的容器状态。
func (f *fakeContainerPort) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	if f.inspectErr != nil {
		return app.ContainerDetail{}, f.inspectErr
	}
	container := domain.Container{
		ID:       domain.ID(request.ContainerID),
		DockerID: "docker-" + request.ContainerID,
		Detached: f.detached,
	}
	return app.ContainerDetail{
		Container: container,
		Inspect:   &docker.ContainerInspect{Running: f.running},
	}, nil
}

// RunAgent 返回受控的 Agent 调用结果。
func (f *fakeContainerPort) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.calls++
	index := f.calls - 1
	var reply fakeAgentReply
	if index < len(f.replies) {
		reply = f.replies[index]
	} else {
		reply = fakeAgentReply{status: agent.RunCompleted, finalText: "已完成"}
	}
	started, block := f.started, f.block
	f.mu.Unlock()
	if started != nil {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	if block {
		<-ctx.Done()
		return app.ContainerAgentResult{}, ctx.Err()
	}
	now := time.Now().UTC()
	if reply.noSession {
		return app.ContainerAgentResult{
			Result: agent.RunResult{
				Status:     reply.status,
				Failure:    reply.failure,
				ExitCode:   reply.exitCode,
				StartedAt:  now,
				FinishedAt: now,
			},
		}, reply.err
	}
	sessionID := domain.NewID()
	result := agent.RunResult{
		SessionID:    sessionID.String(),
		InvocationID: "invocation",
		Status:       reply.status,
		FinalText:    reply.finalText,
		Failure:      reply.failure,
		ExitCode:     reply.exitCode,
		StartedAt:    now,
		FinishedAt:   now,
		Session: agent.SessionFiles{
			JSONLPath:    filepath.ToSlash("sessions/" + sessionID.String() + "/session.jsonl"),
			Completeness: reply.completeness,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	if result.Session.Completeness == "" {
		if reply.status == agent.RunCompleted {
			result.Session.Completeness = agent.SessionComplete
		} else {
			result.Session.Completeness = agent.SessionPartial
		}
	}
	if f.sessions != nil {
		if err := f.sessions.CreateSession(ctx, fakeStepSession(request, result, now)); err != nil {
			return app.ContainerAgentResult{Result: result}, err
		}
	}
	return app.ContainerAgentResult{Result: result}, reply.err
}

// fakeStepSession 构造一条与真实执行一致的会话记录。
func fakeStepSession(
	request app.ContainerAgentRequest,
	result agent.RunResult,
	now time.Time,
) domain.Session {
	runStatus := domain.SessionRunCompleted
	status := domain.SessionStatusCompleted
	completeness := domain.SessionCompletenessComplete
	switch result.Status {
	case agent.RunFailed:
		runStatus = domain.SessionRunFailed
		status = domain.SessionStatusFailed
		completeness = domain.SessionCompletenessPartial
	case agent.RunCancelled:
		runStatus = domain.SessionRunCancelled
		status = domain.SessionStatusCancelled
		completeness = domain.SessionCompletenessPartial
	case agent.RunTimedOut:
		runStatus = domain.SessionRunTimedOut
		status = domain.SessionStatusFailed
		completeness = domain.SessionCompletenessPartial
	}
	if result.Session.Completeness == agent.SessionMissing {
		completeness = domain.SessionCompletenessMissing
	}
	roleType := domain.RoleType(request.RoleType)
	roleID := domain.ID(request.RoleType + "-role")
	stepID := domain.ID(request.StepID)
	attemptID := domain.ID(request.AttemptID)
	session := domain.Session{
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
	if completeness == domain.SessionCompletenessMissing {
		session.JSONLPath = ""
	}
	return session
}

// fakeGitPort 是 Git 工作区状态采集的受控替身。
//
// 它不执行任何真实 Git 命令：用例通过 before / after 指定采集结果，通过 err 指定
// 采集失败，从而验证"采集失败不会被当作工作区干净"这条业务规则。
type fakeGitPort struct {
	mu     sync.Mutex
	before domain.GitWorktreeState
	after  domain.GitWorktreeState
	// err 使所有采集失败。
	err error
	// calls 记录收到的采集请求，按调用顺序排列。
	calls []app.GitWorktreeRequest
}

// InspectWorktree 返回受控的采集结果。
func (f *fakeGitPort) InspectWorktree(
	ctx context.Context,
	request app.GitWorktreeRequest,
) (domain.GitWorktreeState, error) {
	f.mu.Lock()
	f.calls = append(f.calls, request)
	index := len(f.calls) - 1
	before, after, err := f.before, f.after, f.err
	f.mu.Unlock()
	if err != nil {
		return domain.GitWorktreeState{StatusRead: false, StatusError: err.Error()}, err
	}
	if index == 0 {
		return before, nil
	}
	return after, nil
}

// count 返回采集次数。
func (f *fakeGitPort) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// stepExecutorFixture 是一套单步骤执行测试数据。
type stepExecutorFixture struct {
	db         *sqlite.DB
	store      *stepExecutorStore
	executor   *app.StepExecutor
	containers *fakeContainerPort
	git        *fakeGitPort
	role       domain.Role
	host       domain.ExecutionHost
	task       domain.Task
	step       domain.Step
}

// stepExecutorOptions 允许用例调整构造参数。
type stepExecutorOptions struct {
	// taskStatus 是任务准备写入的状态序列；为空时使用默认的"已就绪"路径。
	taskStatus []domain.TaskStatus
	// stayQueued 表示任务停留在 queued，用于验证状态准入拒绝。
	stayQueued bool
	// skipHostValidation 表示不把执行节点置为已通过预检。
	skipHostValidation bool
	// stepStatus 是步骤准备写入的状态序列。
	stepStatus []domain.StepStatus
	// stepType 覆盖步骤类型。
	stepType domain.StepType
	// noContainer 表示任务不绑定容器。
	noContainer bool
	// updateAttemptErr 使尝试终态写入失败。
	updateAttemptErr error
	// git 覆盖 Git 采集替身，为空时使用默认的"干净工作区"替身。
	git *fakeGitPort
	// skipGit 表示不注入 Git 采集能力。
	skipGit bool
}

// newStepExecutorFixture 在测试工作空间中准备一套可执行的步骤，并启动协调器。
func newStepExecutorFixture(t *testing.T, options stepExecutorOptions) *stepExecutorFixture {
	t.Helper()
	return newStepExecutorFixtureWithStart(t, options, true)
}

// newStepExecutorFixtureWithoutStart 构造但不启动协调器，用于生命周期用例。
func newStepExecutorFixtureWithoutStart(t *testing.T) *stepExecutorFixture {
	t.Helper()
	return newStepExecutorFixtureWithStart(t, stepExecutorOptions{}, false)
}

// newStepExecutorFixtureWithStart 按需启动协调器。
func newStepExecutorFixtureWithStart(
	t *testing.T,
	options stepExecutorOptions,
	start bool,
) *stepExecutorFixture {
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
	logger, err := logging.New(nopWriter{}, logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	sessions, err := app.NewSessionService(db, workspace.SessionRoot(), logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	now := time.Now().UTC()
	roleType := domain.RoleTypeDeveloper
	stepType := options.stepType
	if stepType == "" {
		stepType = domain.StepTypeDevelop
		roleType = domain.RoleTypeDeveloper
	}
	role := createManagementRole(t, db, ctx, roleType, "执行角色", now)
	host := createManagementHost(t, db, ctx, now)
	if !options.skipHostValidation {
		// 执行入口要求节点已通过环境预检：这里直接把状态置为 normal，
		// 真实预检流程由执行节点相关用例覆盖。
		host = markHostNormal(t, db, ctx, host)
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
	task.HostID = &host.ID
	task.Config = domain.TaskConfig{
		Goal:                "完成本次需求",
		Environment:         domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		DeveloperRoleID:     role.ID,
		PlannerRoleID:       role.ID,
		AgentTimeoutSeconds: 30,
	}
	if !options.noContainer {
		task.ContainerID = "container-1"
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	step, err := newFixtureStep(task.ID, stepType, role, now)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	for _, status := range options.stepStatus {
		if err := step.Transition(status, now); err != nil {
			t.Fatalf("步骤迁移到 %s 失败: %v", status, err)
		}
		if err := db.UpdateStep(ctx, step); err != nil {
			t.Fatalf("更新步骤失败: %v", err)
		}
	}
	taskPath := options.taskStatus
	if len(taskPath) == 0 && !options.stayQueued {
		taskPath = []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanning,
			domain.TaskStatusPlanned,
		}
	}
	for _, status := range taskPath {
		if err := task.Transition(status, now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", status, err)
		}
		if err := db.UpdateTask(ctx, task); err != nil {
			t.Fatalf("更新任务失败: %v", err)
		}
	}
	containers := &fakeContainerPort{running: true, sessions: db}
	store := &stepExecutorStore{
		StepExecutionStore: db,
		updateAttemptErr:   options.updateAttemptErr,
	}
	gitPort := options.git
	if gitPort == nil {
		gitPort = &fakeGitPort{
			before: domain.GitWorktreeState{
				StatusRead: true,
				Branch:     "agent/" + task.ID.String(),
				Head:       "1111111111111111111111111111111111111111",
			},
			after: domain.GitWorktreeState{
				StatusRead: true,
				Branch:     "agent/" + task.ID.String(),
				Head:       "2222222222222222222222222222222222222222",
				Dirty:      true,
				Modified:   1,
			},
		}
	}
	var gitCapability app.StepExecutionGitPort
	if !options.skipGit {
		gitCapability = gitPort
	}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      store,
		Containers: containers,
		Sessions:   sessions,
		Git:        gitCapability,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	if start {
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
	}
	return &stepExecutorFixture{
		db:         db,
		store:      store,
		executor:   executor,
		containers: containers,
		git:        gitPort,
		role:       role,
		host:       host,
		task:       task,
		step:       step,
	}
}

// markHostNormal 把执行节点置为已通过环境预检。
func markHostNormal(
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

// newFixtureStep 按步骤类型构造测试步骤。
//
// Agent 阶段使用带角色的构造函数，固定阶段使用不带角色的构造函数：
// 两者对角色字段的约束不同，混用会让测试数据本身不合法。
func newFixtureStep(
	taskID domain.ID,
	stepType domain.StepType,
	role domain.Role,
	at time.Time,
) (domain.Step, error) {
	spec := domain.VerificationSpec{AcceptanceCriteria: []string{"接口可以编译"}}
	if stepType.AgentRole() == "" {
		return domain.NewWorkflowStep(taskID, 1, stepType, "实现接口", "按需求实现接口", spec, 1, at)
	}
	return domain.NewAgentStep(
		taskID,
		1,
		stepType,
		"实现接口",
		"按需求实现接口",
		"不要修改构建脚本",
		role,
		spec,
		domain.RetryConfig{MaxAttempts: 1},
		at,
	)
}

// execute 发起一次执行并返回结果。
func (f *stepExecutorFixture) execute(
	t *testing.T,
	ctx context.Context,
) (app.StepExecutionResult, error) {
	t.Helper()
	return f.executor.Execute(ctx, app.StepExecuteRequest{
		TaskID: f.task.ID.String(),
		StepID: f.step.ID.String(),
	})
}

// reloadTask 读取数据库中的任务。
func (f *stepExecutorFixture) reloadTask(t *testing.T, ctx context.Context) domain.Task {
	t.Helper()
	task, err := f.db.GetTask(ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *task
}

// reloadStep 读取数据库中的步骤。
func (f *stepExecutorFixture) reloadStep(t *testing.T, ctx context.Context) domain.Step {
	t.Helper()
	step, err := f.db.GetStep(ctx, f.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return *step
}

// attempts 返回步骤的全部尝试。
func (f *stepExecutorFixture) attempts(t *testing.T, ctx context.Context) []domain.Attempt {
	t.Helper()
	attempts, err := f.db.ListAttemptsByStep(ctx, f.step.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	return attempts
}

// sessions 返回任务的全部会话。
func (f *stepExecutorFixture) sessionsOf(t *testing.T, ctx context.Context) []domain.Session {
	t.Helper()
	sessions, err := f.db.ListSessionsByTask(ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	return sessions
}

// 编译期确认替身满足协调器依赖。
var _ app.StepExecutionStore = (*stepExecutorStore)(nil)
var _ app.StepExecutionContainerPort = (*fakeContainerPort)(nil)

// 编译期确认存储接口仍由真实存储满足。
var _ app.StepExecutionStore = storage.Store(nil)

// errInjected 是注入的存储失败。
var errInjected = errors.New("注入的存储失败")
