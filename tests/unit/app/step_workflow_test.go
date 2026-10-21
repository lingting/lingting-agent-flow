// Package app_test 提供单步骤开发—验证闭环协调器的单元测试。
//
// 用例只依赖数据库与受控替身：开发阶段由 fakeContainerPort 返回受控的 Agent 结果，
// 验证阶段由 fakeCheckContainer 返回受控的检查结果，不访问真实 Pi、模型、SSH 或
// Docker，因此默认测试路径完全在测试工作空间内完成。
package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// workflowCheckReply 是一条受控的检查执行结果。
type workflowCheckReply struct {
	exitCode int
	stdout   string
	stderr   string
	started  bool
	certain  bool
	err      error
}

// workflowContainerPort 是开发阶段与验证阶段共用的容器替身。
//
// 同一个实例同时实现 StepExecutionContainerPort 与 ValidationContainerPort，因此用例
// 可以断言"开发与验证看到的是同一个容器与同一份工作区"。
type workflowContainerPort struct {
	mu sync.Mutex
	// inspectErr 使容器核验失败。
	inspectErr error
	// running 表示容器是否在运行。
	running bool
	// detached 表示容器已脱离管理。
	detached bool
	// agentReplies 按顺序返回的 Agent 调用结果。
	agentReplies []fakeAgentReply
	// agentRequests 记录收到的 Agent 调用请求。
	agentRequests []app.ContainerAgentRequest
	// agentCalls 统计 Agent 调用次数。
	agentCalls int
	// checkReplies 按命令名返回受控的检查结果。
	checkReplies map[string]workflowCheckReply
	// execRequests 记录收到的检查命令执行请求。
	execRequests []app.ExecRequest
	// sessions 非空时把会话记录写入数据库。
	sessions storage.SessionStore
	// onAgent 在返回 Agent 结果前执行，用于注入副作用。
	onAgent func(request app.ContainerAgentRequest)
}

// InspectContainer 返回受控的容器状态。
func (f *workflowContainerPort) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectErr != nil {
		return app.ContainerDetail{}, f.inspectErr
	}
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   "host-1",
			Name:     "agentflow-workflow",
			DockerID: "docker-" + request.ContainerID,
			Detached: f.detached,
		},
		Inspect: &docker.ContainerInspect{Running: f.running},
	}, nil
}

// RunAgent 返回受控的 Agent 调用结果。
func (f *workflowContainerPort) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	f.mu.Lock()
	f.agentRequests = append(f.agentRequests, request)
	f.agentCalls++
	index := f.agentCalls - 1
	reply := fakeAgentReply{status: agent.RunCompleted, finalText: "已完成"}
	if index < len(f.agentReplies) {
		reply = f.agentReplies[index]
	}
	onAgent := f.onAgent
	sessions := f.sessions
	f.mu.Unlock()
	if onAgent != nil {
		onAgent(request)
	}
	now := time.Now().UTC()
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
			JSONLPath:    "sessions/" + sessionID.String() + "/session.jsonl",
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
	if sessions != nil {
		if err := sessions.CreateSession(ctx, fakeStepSession(request, result, now)); err != nil {
			return app.ContainerAgentResult{Result: result}, err
		}
	}
	return app.ContainerAgentResult{Result: result}, reply.err
}

// Exec 返回受控的检查命令结果。
func (f *workflowContainerPort) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	f.mu.Lock()
	f.execRequests = append(f.execRequests, request)
	name := ""
	if len(request.Command) > 0 {
		name = request.Command[0]
	}
	reply := f.checkReplies[name]
	f.mu.Unlock()
	execErr := reply.err
	status := sshclient.CommandStatusSucceeded
	switch {
	case execErr != nil && !reply.started:
		// 执行层无法启动命令：退出码与输出都没有意义。
		status = sshclient.CommandStatusConnectionError
	case reply.exitCode != 0:
		status = sshclient.CommandStatusExitError
	}
	certain := reply.certain
	started := reply.started
	if status == sshclient.CommandStatusSucceeded ||
		status == sshclient.CommandStatusExitError {
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
		Duration: 10 * time.Millisecond,
	}}, execErr
}

// agentCallsCount 返回 Agent 调用次数。
func (f *workflowContainerPort) agentCallsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.agentCalls
}

// lastAgentRequest 返回最后一次 Agent 调用请求。
func (f *workflowContainerPort) lastAgentRequest(t *testing.T) app.ContainerAgentRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.agentRequests) == 0 {
		t.Fatalf("没有收到 Agent 调用请求")
	}
	return f.agentRequests[len(f.agentRequests)-1]
}

// execCount 返回检查命令执行次数。
func (f *workflowContainerPort) execCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.execRequests)
}

// workflowFixture 是一套可直接发起闭环的测试数据。
type workflowFixture struct {
	db         *sqlite.DB
	containers *workflowContainerPort
	store      *stepExecutorStore
	executor   *app.StepExecutor
	validation *app.ValidationService
	service    *app.StepWorkflowService
	task       domain.Task
	step       domain.Step
	ctx        context.Context
	workspace  *testkit.Workspace
}

// workflowOptions 允许用例调整闭环测试数据的构造方式。
type workflowOptions struct {
	// spec 是步骤的验证配置，为空时使用一条必需检查。
	spec *domain.VerificationSpec
	// agentReplies 是开发阶段的 Agent 调用结果。
	agentReplies []fakeAgentReply
	// checkReplies 是验证阶段的检查结果。
	checkReplies map[string]workflowCheckReply
	// inspectErr 使容器核验失败。
	inspectErr error
	// running 表示容器是否在运行，默认 true。
	stopped bool
	// updateAttemptErr 使开发阶段的尝试终态写入失败。
	updateAttemptErr error
}

// newWorkflowFixture 在工作空间中准备一套可执行闭环的测试数据。
func newWorkflowFixture(t *testing.T, options workflowOptions) *workflowFixture {
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
	role := createManagementRole(t, db, ctx, domain.RoleTypeDeveloper, "开发角色", now)
	host := createManagementHost(t, db, ctx, now)
	host = markHostNormal(t, db, ctx, host)
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
		DeveloperRoleID: role.ID,
		PlannerRoleID:   role.ID,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	spec := workflowSpec()
	if options.spec != nil {
		spec = *options.spec
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"不要修改构建脚本",
		role,
		spec,
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
		if err := db.UpdateTask(ctx, task); err != nil {
			t.Fatalf("更新任务失败: %v", err)
		}
	}
	containers := &workflowContainerPort{
		running:      !options.stopped,
		inspectErr:   options.inspectErr,
		agentReplies: options.agentReplies,
		checkReplies: options.checkReplies,
		sessions:     db,
	}
	if containers.checkReplies == nil {
		containers.checkReplies = map[string]workflowCheckReply{}
	}
	store := &stepExecutorStore{
		StepExecutionStore: db,
		updateAttemptErr:   options.updateAttemptErr,
	}
	gitPort := &fakeGitPort{
		before: domain.GitWorktreeState{StatusRead: true, Branch: "agent/" + task.ID.String()},
		after:  domain.GitWorktreeState{StatusRead: true, Branch: "agent/" + task.ID.String()},
	}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      store,
		Containers: containers,
		Sessions:   sessions,
		Git:        gitPort,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	validation, err := app.NewValidationService(app.ValidationOptions{
		Store:      db,
		Containers: containers,
		OutputRoot: workspace.Dir("validations"),
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
	service, err := app.NewStepWorkflowService(app.StepWorkflowOptions{
		Steps:      executor,
		Validation: validation,
		Store:      db,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造闭环协调器失败: %v", err)
	}
	startService(t, executor, validation, service)
	return &workflowFixture{
		db:         db,
		containers: containers,
		store:      store,
		executor:   executor,
		validation: validation,
		service:    service,
		task:       task,
		step:       step,
		ctx:        ctx,
		workspace:  workspace,
	}
}

// startService 启动后台服务并在用例结束时停止。
func startService(t *testing.T, services ...interface {
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
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		for index := len(services) - 1; index >= 0; index-- {
			if err := services[index].Stop(stopCtx); err != nil {
				t.Errorf("停止后台服务失败: %v", err)
			}
		}
	})
}

// request 构造一次闭环请求。
func (f *workflowFixture) request() app.ExecuteAndValidateRequest {
	return app.ExecuteAndValidateRequest{
		TaskID: f.task.ID.String(),
		StepID: f.step.ID.String(),
	}
}

// execute 发起一次闭环并返回结果。
func (f *workflowFixture) execute(
	t *testing.T,
	ctx context.Context,
) (app.ExecuteAndValidateResult, error) {
	t.Helper()
	return f.service.ExecuteAndValidate(ctx, f.request())
}

// reloadStep 读取数据库中的步骤。
func (f *workflowFixture) reloadStep(t *testing.T) domain.Step {
	t.Helper()
	step, err := f.db.GetStep(context.Background(), f.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return *step
}

// reloadTask 读取数据库中的任务。
func (f *workflowFixture) reloadTask(t *testing.T) domain.Task {
	t.Helper()
	task, err := f.db.GetTask(context.Background(), f.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *task
}

// attempts 返回步骤的全部尝试。
func (f *workflowFixture) attempts(t *testing.T) []domain.Attempt {
	t.Helper()
	attempts, err := f.db.ListAttemptsByStep(context.Background(), f.step.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	return attempts
}

// validationRuns 返回某次尝试的全部验证记录。
func (f *workflowFixture) validationRuns(t *testing.T, attemptID domain.ID) []domain.ValidationRun {
	t.Helper()
	runs, err := f.db.ListValidationRunsByAttempt(context.Background(), attemptID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	return runs
}

// workflowSpec 构造一条必需检查的验证配置。
func workflowSpec() domain.VerificationSpec {
	return domain.VerificationSpec{
		AcceptanceCriteria: []string{"接口可以编译"},
		Commands: []domain.VerificationCommand{
			{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
		},
	}
}

// passedCheck 返回一条通过的检查结果。
func passedCheck() workflowCheckReply {
	return workflowCheckReply{exitCode: 0, stdout: "ok", started: true, certain: true}
}

// failedCheck 返回一条退出码失败的检查结果。
func failedCheck(stderr string) workflowCheckReply {
	return workflowCheckReply{exitCode: 1, stderr: stderr, started: true, certain: true}
}

// startupErrorCheck 返回一条无法启动的检查结果。
func startupErrorCheck(err error) workflowCheckReply {
	return workflowCheckReply{exitCode: -1, started: false, certain: false, err: err}
}

// TestWorkflowValidatesAfterDeveloperSuccess 验证开发成功后自动启动验证并在通过后收口。
func TestWorkflowValidatesAfterDeveloperSuccess(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{{status: agent.RunCompleted, finalText: "实现完成"}},
		checkReplies: map[string]workflowCheckReply{"go": passedCheck()},
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if !result.ValidationRan {
		t.Fatalf("开发成功后应启动验证，实际未启动: %s", result.ValidationSkipReason)
	}
	if result.Outcome != domain.StepExecutionCompleted {
		t.Fatalf("开发阶段结论不符: %s", result.Outcome)
	}
	if result.ValidationVerdict != domain.VerificationVerdictPassed {
		t.Fatalf("验证结论不符: %s", result.ValidationVerdict)
	}
	if !result.Verified {
		t.Fatalf("验证通过后步骤应标记为已验证完成")
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusCompleted {
		t.Fatalf("步骤状态不符: 期望 completed，实际 %s", step.Status)
	}
	if !result.Finalized {
		t.Fatalf("收口状态应全部写入成功: %v", result.FinalizationErrors)
	}
	if fixture.containers.execCount() != 1 {
		t.Fatalf("检查命令执行次数不符: %d", fixture.containers.execCount())
	}
	if fixture.containers.agentCallsCount() != 1 {
		t.Fatalf("Agent 调用次数不符: %d", fixture.containers.agentCallsCount())
	}
	runs := fixture.validationRuns(t, result.AttemptID)
	if len(runs) != 1 {
		t.Fatalf("验证记录数量不符: %d", len(runs))
	}
	if runs[0].AttemptID != result.AttemptID {
		t.Fatalf("验证记录未关联开发尝试")
	}
	// 验证记录必须指向同一个容器工作区。
	if runs[0].ContainerID != fixture.task.ContainerID {
		t.Fatalf("验证记录容器不符: %s", runs[0].ContainerID)
	}
	if runs[0].Workspace != fixture.task.Config.EffectiveWorkDir() {
		t.Fatalf("验证记录工作区不符: %s", runs[0].Workspace)
	}
	// 任务不因为单个步骤通过验证而完成。
	task := fixture.reloadTask(t)
	if task.Status == domain.TaskStatusCompleted {
		t.Fatalf("单个步骤通过验证不应把任务标记为完成")
	}
}

// TestWorkflowSkipsValidationAfterDeveloperFailure 验证开发失败后不启动验证。
func TestWorkflowSkipsValidationAfterDeveloperFailure(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{{
			status:   agent.RunFailed,
			failure:  "编译失败",
			exitCode: 1,
		}},
		checkReplies: map[string]workflowCheckReply{"go": passedCheck()},
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationRan {
		t.Fatalf("开发失败后不得启动验证")
	}
	if result.Verified {
		t.Fatalf("开发失败不得标记为已验证完成")
	}
	if fixture.containers.execCount() != 0 {
		t.Fatalf("开发失败后不应执行任何检查命令")
	}
	if fixture.containers.agentCallsCount() != 1 {
		t.Fatalf("Agent 调用次数不符: %d", fixture.containers.agentCallsCount())
	}
	step := fixture.reloadStep(t)
	if step.Status == domain.StepStatusCompleted {
		t.Fatalf("开发失败时步骤不得进入 completed")
	}
	if len(fixture.validationRuns(t, result.AttemptID)) != 0 {
		t.Fatalf("开发失败时不得创建验证记录")
	}
}

// TestWorkflowSkipsValidationAfterDeveloperCancel 验证开发被取消后不启动验证。
func TestWorkflowSkipsValidationAfterDeveloperCancel(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{{status: agent.RunCancelled, failure: "调用方取消"}},
		checkReplies: map[string]workflowCheckReply{"go": passedCheck()},
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationRan {
		t.Fatalf("开发被取消后不得启动验证")
	}
	if result.Verified {
		t.Fatalf("开发被取消不得标记为已验证完成")
	}
	if result.Outcome != domain.StepExecutionCancelled {
		t.Fatalf("开发阶段结论不符: %s", result.Outcome)
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusCancelled {
		t.Fatalf("步骤状态不符: 期望 cancelled，实际 %s", step.Status)
	}
}

// TestWorkflowRequiredCheckFailureBlocksCompletion 验证必需检查失败时步骤不得完成。
func TestWorkflowRequiredCheckFailureBlocksCompletion(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{{status: agent.RunCompleted, finalText: "实现完成"}},
		checkReplies: map[string]workflowCheckReply{
			"go": failedCheck("编译错误: undefined: foo"),
		},
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if !result.ValidationRan {
		t.Fatalf("开发成功后应启动验证")
	}
	if result.Verified {
		t.Fatalf("必需检查失败时不得标记为已验证完成")
	}
	if result.ValidationVerdict != domain.VerificationVerdictFailed {
		t.Fatalf("验证结论不符: %s", result.ValidationVerdict)
	}
	step := fixture.reloadStep(t)
	if step.Status == domain.StepStatusCompleted {
		t.Fatalf("必需检查失败时步骤不得进入 completed")
	}
	if step.Status != domain.StepStatusFailed {
		t.Fatalf("步骤状态不符: 期望 failed，实际 %s", step.Status)
	}
	if !strings.Contains(step.FailureSummary, "build") {
		t.Fatalf("步骤失败摘要未包含失败检查名: %s", step.FailureSummary)
	}
}

// TestWorkflowInfrastructureErrorBlocksCompletion 验证基础设施异常不判通过也不伪装成代码失败。
func TestWorkflowInfrastructureErrorBlocksCompletion(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{{status: agent.RunCompleted, finalText: "实现完成"}},
		checkReplies: map[string]workflowCheckReply{
			"go": startupErrorCheck(errors.New("容器不可用")),
		},
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.Verified {
		t.Fatalf("基础设施异常不得标记为已验证完成")
	}
	if !result.InfrastructureError {
		t.Fatalf("应识别为验证基础设施异常")
	}
	if result.Verification != domain.StepVerificationInfrastructure {
		t.Fatalf("验证状态不符: %s", result.Verification)
	}
	step := fixture.reloadStep(t)
	if step.Status == domain.StepStatusCompleted {
		t.Fatalf("基础设施异常时步骤不得进入 completed")
	}
	if step.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤状态不符: 期望 recovery_required，实际 %s", step.Status)
	}
	task := fixture.reloadTask(t)
	if task.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("任务状态不符: 期望 recovery_required，实际 %s", task.Status)
	}
}

// TestWorkflowInvalidVerificationConfigRejected 验证配置无效时不得默认通过。
func TestWorkflowInvalidVerificationConfigRejected(t *testing.T) {
	t.Parallel()
	emptySpec := domain.VerificationSpec{AcceptanceCriteria: []string{"接口可以编译"}}
	fixture := newWorkflowFixture(t, workflowOptions{
		spec:         &emptySpec,
		agentReplies: []fakeAgentReply{{status: agent.RunCompleted, finalText: "实现完成"}},
	})
	_, err := fixture.execute(t, fixture.ctx)
	if !errors.Is(err, app.ErrStepWorkflowInvalidConfig) {
		t.Fatalf("应返回验证配置错误，实际: %v", err)
	}
	if fixture.containers.agentCallsCount() != 0 {
		t.Fatalf("验证配置无效时不应启动开发执行")
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusPending {
		t.Fatalf("验证配置无效时步骤不应改变状态: %s", step.Status)
	}
}

// TestWorkflowContainerUnavailableRejected 验证容器不可用时不回退到宿主机执行。
func TestWorkflowContainerUnavailableRejected(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{{status: agent.RunCompleted, finalText: "实现完成"}},
		checkReplies: map[string]workflowCheckReply{"go": passedCheck()},
		stopped:      true,
	})
	_, err := fixture.execute(t, fixture.ctx)
	if !errors.Is(err, app.ErrStepExecutionPrerequisite) {
		t.Fatalf("应返回前置条件错误，实际: %v", err)
	}
	if fixture.containers.agentCallsCount() != 0 {
		t.Fatalf("容器不可用时不应调用 Agent")
	}
	if fixture.containers.execCount() != 0 {
		t.Fatalf("容器不可用时不应执行检查命令")
	}
}

// TestWorkflowPersistsFailureFeedbackForRetry 验证失败反馈可持久化并被后续执行引用。
func TestWorkflowPersistsFailureFeedbackForRetry(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "第二次实现"},
		},
		checkReplies: map[string]workflowCheckReply{
			"go": failedCheck("编译错误: undefined: bar"),
		},
	})
	first, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("第一次闭环失败: %v", err)
	}
	if !first.FeedbackPersisted {
		t.Fatalf("验证失败反馈应被持久化")
	}
	feedback, err := fixture.db.GetValidationFailureFeedback(fixture.ctx, first.ValidationRunID)
	if err != nil {
		t.Fatalf("读取验证失败反馈失败: %v", err)
	}
	if feedback.DeveloperAttemptID != first.AttemptID {
		t.Fatalf("反馈未关联开发尝试")
	}
	if len(feedback.Checks) != 1 {
		t.Fatalf("反馈检查数量不符: %d", len(feedback.Checks))
	}
	if feedback.Checks[0].ExitCode != 1 {
		t.Fatalf("反馈退出码不符: %d", feedback.Checks[0].ExitCode)
	}
	if !strings.Contains(feedback.Checks[0].StderrSummary, "编译错误") {
		t.Fatalf("反馈输出摘要不符: %s", feedback.Checks[0].StderrSummary)
	}

	// 显式重新执行：必须创建新的 Attempt 与新的 Session，并在提示词中引用上一轮问题。
	second, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("第二次闭环失败: %v", err)
	}
	if second.AttemptID == first.AttemptID {
		t.Fatalf("重新执行必须创建新的尝试")
	}
	if second.SessionID == first.SessionID {
		t.Fatalf("重新执行必须创建新的会话")
	}
	attempts := fixture.attempts(t)
	if len(attempts) != 2 {
		t.Fatalf("尝试数量不符: %d", len(attempts))
	}
	if attempts[0].ID == attempts[1].ID {
		t.Fatalf("历史尝试不得被覆盖")
	}
	prompt := fixture.containers.lastAgentRequest(t).Prompt
	if !strings.Contains(prompt, "上一次验证未通过的问题") {
		t.Fatalf("重新执行的提示词未引用上一轮失败反馈")
	}
	if !strings.Contains(prompt, "编译错误") {
		t.Fatalf("重新执行的提示词未包含上一轮失败原因")
	}
}

// TestWorkflowConcurrentRequestsRejected 验证同一步骤不会同时运行两条闭环。
func TestWorkflowConcurrentRequestsRejected(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies: []fakeAgentReply{{status: agent.RunCompleted, finalText: "实现完成"}},
		checkReplies: map[string]workflowCheckReply{"go": passedCheck()},
	})
	fixture.containers.onAgent = func(request app.ContainerAgentRequest) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	firstDone := make(chan struct{})
	var (
		firstResult app.ExecuteAndValidateResult
		firstErr    error
	)
	go func() {
		defer close(firstDone)
		firstResult, firstErr = fixture.service.ExecuteAndValidate(
			context.Background(),
			fixture.request(),
		)
	}()
	<-entered
	_, secondErr := fixture.execute(t, fixture.ctx)
	if !errors.Is(secondErr, app.ErrStepExecutionConflict) {
		t.Fatalf("并发请求应返回状态冲突，实际: %v", secondErr)
	}
	close(release)
	<-firstDone
	if firstErr != nil {
		t.Fatalf("第一次闭环失败: %v", firstErr)
	}
	if !firstResult.Verified {
		t.Fatalf("第一次闭环应通过验证")
	}
	if fixture.containers.agentCallsCount() != 1 {
		t.Fatalf("并发请求不应重复启动 Pi: %d", fixture.containers.agentCallsCount())
	}
}

// TestWorkflowDeveloperFinalizationFailureSkipsValidation 验证开发记录写入失败时不启动验证。
func TestWorkflowDeveloperFinalizationFailureSkipsValidation(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowFixture(t, workflowOptions{
		agentReplies:     []fakeAgentReply{{status: agent.RunCompleted, finalText: "实现完成"}},
		checkReplies:     map[string]workflowCheckReply{"go": passedCheck()},
		updateAttemptErr: errInjected,
	})
	result, err := fixture.execute(t, fixture.ctx)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationRan {
		t.Fatalf("开发记录写入失败时不应启动验证")
	}
	if result.Verified {
		t.Fatalf("开发记录写入失败时不得标记为已验证完成")
	}
	if result.Finalized {
		t.Fatalf("收口状态不应报告为完整写入")
	}
	if len(result.DeveloperFinalizationErrors) == 0 {
		t.Fatalf("开发阶段写入失败原因不应为空")
	}
}

// TestWorkflowStopCancelsInFlightRun 验证服务停止会取消在途闭环。
func TestWorkflowStopCancelsInFlightRun(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	role := createManagementRole(t, db, ctx, domain.RoleTypeDeveloper, "开发角色", now)
	host := createManagementHost(t, db, ctx, now)
	host = markHostNormal(t, db, ctx, host)
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
		DeveloperRoleID: role.ID,
		PlannerRoleID:   role.ID,
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
		role,
		workflowSpec(),
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
		if err := db.UpdateTask(ctx, task); err != nil {
			t.Fatalf("更新任务失败: %v", err)
		}
	}
	containers := &workflowContainerPort{running: true, sessions: db}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      db,
		Containers: containers,
		Sessions:   sessions,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	validation, err := app.NewValidationService(app.ValidationOptions{
		Store:      db,
		Containers: containers,
		OutputRoot: workspace.Dir("validations"),
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
	service, err := app.NewStepWorkflowService(app.StepWorkflowOptions{
		Steps:      executor,
		Validation: validation,
		Store:      db,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造闭环协调器失败: %v", err)
	}
	startService(t, executor, validation, service)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = service.ExecuteAndValidate(context.Background(), app.ExecuteAndValidateRequest{
			TaskID: task.ID.String(),
			StepID: step.ID.String(),
		})
	}()
	// 等待开发阶段真正开始后再停止服务，避免竞态。
	deadline := time.Now().Add(10 * time.Second)
	for containers.agentCallsCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if containers.agentCallsCount() == 0 {
		t.Fatalf("开发阶段未在超时前开始")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	if err := service.Stop(stopCtx); err != nil {
		t.Fatalf("停止闭环协调器失败: %v", err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("在途闭环未在停止后收敛")
	}
	_, err = service.ExecuteAndValidate(ctx, app.ExecuteAndValidateRequest{
		TaskID: task.ID.String(),
		StepID: step.ID.String(),
	})
	if err == nil {
		t.Fatalf("停止后不应接受新的闭环请求")
	}
}

// 编译期确认闭环协调器满足后台服务契约。
var _ interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
} = (*app.StepWorkflowService)(nil)
