// Package app_test 提供 Developer + 确定性验证 + Validator 语义审查决策链
// 的应用服务集成测试。
//
// 用例使用真实 SQLite 文件、真实 app.StepExecutor / app.ValidationService /
// app.StepWorkflowService / app.ValidatorReviewer，只把容器内 Agent 调用与检查命令
// 执行替换为受控替身，因此覆盖的是真实业务链路（占位、Session、持久化、收口），
// 不访问真实 Pi、模型、SSH 或 Docker。
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

// reviewReply 是一条受控的 Validator 调用结果。
type reviewReply struct {
	status    agent.RunStatus
	finalText string
	failure   string
	exitCode  int
	err       error
}

// reviewPort 是容器能力的受控替身，覆盖开发与验证两个阶段。
//
// 它按调用顺序返回受控的 Agent 结果，并按命令名返回受控的检查结果，因此用例可以
// 精确控制"开发成功 / 确定性检查通过 / 审查输出"三件事。
type reviewPort struct {
	mu sync.Mutex
	// agentReplies 按调用顺序返回；索引 0 是开发调用，其后是审查调用。
	agentReplies []reviewReply
	// agentRoles 记录每次 Agent 调用的角色类型。
	agentRoles []string
	// agentRequests 记录收到的 Agent 调用请求。
	agentRequests []app.ContainerAgentRequest
	// checkReplies 按命令名返回受控的检查结果。
	checkReplies map[string]workflowCheckReply
	// sessions 非空时把会话记录写入数据库。
	sessions storage.SessionStore
	// onAgent 在返回 Agent 结果前执行，用于注入副作用。
	onAgent func(request app.ContainerAgentRequest)
}

// InspectContainer 返回受控的容器状态。
func (f *reviewPort) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   "host-1",
			Name:     "agentflow-review",
			DockerID: "docker-" + request.ContainerID,
		},
		// 容器运行状态由审查链路的核验直接决定：这里始终报告运行中。
		Inspect: &docker.ContainerInspect{Running: true},
	}, nil
}

// RunAgent 返回受控的 Agent 调用结果。
func (f *reviewPort) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	f.mu.Lock()
	index := len(f.agentRoles)
	f.agentRoles = append(f.agentRoles, request.RoleType)
	f.agentRequests = append(f.agentRequests, request)
	reply := reviewReply{status: agent.RunCompleted, finalText: "已完成"}
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
			Completeness: agent.SessionComplete,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	if reply.status != agent.RunCompleted {
		result.Session.Completeness = agent.SessionPartial
	}
	if sessions != nil {
		if err := sessions.CreateSession(ctx, reviewSessionOf(request, result, now)); err != nil {
			return app.ContainerAgentResult{Result: result}, err
		}
	}
	return app.ContainerAgentResult{Result: result}, reply.err
}

// Exec 返回受控的检查命令结果。
func (f *reviewPort) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	f.mu.Lock()
	name := ""
	if len(request.Command) > 0 {
		name = request.Command[0]
	}
	reply := f.checkReplies[name]
	f.mu.Unlock()
	return workflowExecResult(reply)
}

// agentCalls 返回 Agent 调用次数。
func (f *reviewPort) agentCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.agentRoles)
}

// roles 返回全部 Agent 调用的角色类型。
func (f *reviewPort) roles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.agentRoles...)
}

// lastRequest 返回最后一次 Agent 调用请求。
func (f *reviewPort) lastRequest(t *testing.T) app.ContainerAgentRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.agentRequests) == 0 {
		t.Fatalf("没有收到 Agent 调用请求")
	}
	return f.agentRequests[len(f.agentRequests)-1]
}

// reviewFixture 是一套可直接发起完整决策链的测试数据。
type reviewFixture struct {
	db         *sqlite.DB
	port       *reviewPort
	sessions   *app.SessionService
	executor   *app.StepExecutor
	validation *app.ValidationService
	service    *app.StepWorkflowService
	reviewer   *app.ValidatorReviewer
	task       domain.Task
	step       domain.Step
	ctx        context.Context
	workspace  *testkit.Workspace
}

// reviewOptions 允许用例调整决策链的构造方式。
type reviewOptions struct {
	// validatorRoleID 覆盖任务配置的验证角色；为空表示使用 fixture 创建的角色。
	validatorRoleID *domain.ID
	// omitValidatorRole 表示任务完全不配置验证角色。
	omitValidatorRole bool
	// noReviewer 表示服务不装配语义审查能力。
	noReviewer bool
	// agentReplies 按顺序返回 Agent 调用结果（0 为开发调用）。
	agentReplies []reviewReply
	// checkReplies 按命令名返回检查结果。
	checkReplies map[string]workflowCheckReply
	// failReviewFinish 使验证结论写入失败，用于验证"持久化失败不报告成功"。
	failFinish bool
}

// newReviewFixture 在工作空间中准备一套完整的决策链测试数据。
func newReviewFixture(t *testing.T, options reviewOptions) *reviewFixture {
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
	developer := createManagementRole(t, db, ctx, domain.RoleTypeDeveloper, "开发角色", now)
	planner := createManagementRole(t, db, ctx, domain.RoleTypePlanner, "规划角色", now)
	validator := createManagementRole(t, db, ctx, domain.RoleTypeValidator, "验证角色", now)
	host := markHostNormal(t, db, ctx, createManagementHost(t, db, ctx, now))
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
	config := domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		DeveloperRoleID: developer.ID,
		PlannerRoleID:   planner.ID,
		ValidatorRoleID: validator.ID,
	}
	if options.omitValidatorRole {
		config.ValidatorRoleID = ""
	}
	if options.validatorRoleID != nil {
		config.ValidatorRoleID = *options.validatorRoleID
	}
	task.Config = config
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
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
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"不要修改构建脚本",
		developer,
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
	port := &reviewPort{
		sessions:     db,
		agentReplies: options.agentReplies,
		checkReplies: options.checkReplies,
	}
	if port.checkReplies == nil {
		port.checkReplies = map[string]workflowCheckReply{}
	}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      db,
		Containers: port,
		Sessions:   sessions,
		Git: &fakeGitPort{
			before: domain.GitWorktreeState{StatusRead: true, Branch: "agent/" + task.ID.String()},
			after:  domain.GitWorktreeState{StatusRead: true, Branch: "agent/" + task.ID.String()},
		},
		Logger: logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	reviewer, err := app.NewValidatorReviewer(app.ValidatorReviewerOptions{
		Store:      db,
		Containers: port,
		Sessions:   sessions,
		Logger:     logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造语义审查器失败: %v", err)
	}
	validationOptions := app.ValidationOptions{
		Store:      db,
		Containers: port,
		OutputRoot: workspace.Dir("validations"),
		Logger:     logger.Slog(),
	}
	if !options.noReviewer {
		validationOptions.Reviewer = reviewer
	}
	if options.failFinish {
		validationOptions.Store = &failingValidationStore{
			ValidationStore: db,
			finishErr:       errors.New("数据库写入失败"),
		}
	}
	validation, err := app.NewValidationService(validationOptions)
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
	return &reviewFixture{
		db:         db,
		port:       port,
		sessions:   sessions,
		executor:   executor,
		validation: validation,
		service:    service,
		reviewer:   reviewer,
		task:       task,
		step:       step,
		ctx:        ctx,
		workspace:  workspace,
	}
}

// execute 发起一次完整的开发—验证闭环。
func (f *reviewFixture) execute(
	t *testing.T,
) (app.ExecuteAndValidateResult, error) {
	t.Helper()
	return f.service.ExecuteAndValidate(f.ctx, app.ExecuteAndValidateRequest{
		TaskID: f.task.ID.String(),
		StepID: f.step.ID.String(),
	})
}

// reloadStep 读取数据库中的步骤。
func (f *reviewFixture) reloadStep(t *testing.T) domain.Step {
	t.Helper()
	step, err := f.db.GetStep(context.Background(), f.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return *step
}

// reloadTask 读取数据库中的任务。
func (f *reviewFixture) reloadTask(t *testing.T) domain.Task {
	t.Helper()
	task, err := f.db.GetTask(context.Background(), f.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *task
}

// validationHistory 返回步骤的验证历史。
func (f *reviewFixture) validationHistory(t *testing.T) app.ValidationHistory {
	t.Helper()
	records, err := app.NewExecutionRecordService(f.db)
	if err != nil {
		t.Fatalf("构造执行记录服务失败: %v", err)
	}
	history, err := records.ListValidations(f.ctx, f.task.ID.String(), f.step.ID.String())
	if err != nil {
		t.Fatalf("读取验证历史失败: %v", err)
	}
	return history
}

// sessionsOf 返回任务的全部会话。
func (f *reviewFixture) sessionsOf(t *testing.T) []domain.Session {
	t.Helper()
	sessions, err := f.db.ListSessionsByTask(context.Background(), f.task.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	return sessions
}

// reviewSessionOf 构造与假执行结果一致的会话记录。
func reviewSessionOf(
	request app.ContainerAgentRequest,
	result agent.RunResult,
	at time.Time,
) domain.Session {
	roleType := domain.RoleType(request.RoleType)
	var roleID domain.ID
	var stepID, attemptID *domain.ID
	if roleType == domain.RoleTypeValidator {
		if value := domain.ID(request.StepID); !value.IsEmpty() {
			stepID = &value
		}
		if value := domain.ID(request.AttemptID); !value.IsEmpty() {
			attemptID = &value
		}
		roleID = "validator"
	} else {
		if value := domain.ID(request.StepID); !value.IsEmpty() {
			stepID = &value
		}
		if value := domain.ID(request.AttemptID); !value.IsEmpty() {
			attemptID = &value
		}
		roleID = "developer"
	}
	role := domain.Role{
		ID:           roleID,
		Name:         string(roleType),
		Type:         roleType,
		Runner:       domain.RoleRunnerPi,
		Model:        request.Model,
		SystemPrompt: "提示词",
		ToolsPolicy:  domain.DefaultToolsPolicy(roleType),
		Enabled:      true,
	}
	session, err := domain.NewSession(
		domain.SessionScope{
			TaskID:    domain.ID(request.TaskID),
			StepID:    stepID,
			AttemptID: attemptID,
		},
		role,
		app.ProviderOf(request.Model),
		request.Model,
		at,
	)
	if err != nil {
		return domain.Session{}
	}
	if id := domain.ID(result.SessionID); !id.IsEmpty() {
		session.ID = id
	}
	session.InvocationID = result.InvocationID
	session.ContainerID = request.ContainerID
	if err := session.MarkRunning(""); err != nil {
		return domain.Session{}
	}
	if err := session.Complete(at); err != nil {
		return domain.Session{}
	}
	session.RunStatus = domain.SessionRunCompleted
	session.Completeness = domain.SessionCompletenessComplete
	session.ExportStatus = domain.SessionExportNotAttempted
	session.ArchiveStatus = domain.SessionArchiveSucceeded
	session.ArchivePath = "sessions/" + session.ID.String() + "/session.jsonl"
	session.JSONLPath = session.ArchivePath
	return session
}

// TestReviewPassedWithDeterministicPassed 验证确定性通过 + 审查通过时最终通过。
func TestReviewPassedWithDeterministicPassed(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{status: agent.RunCompleted, finalText: `{"passed":true,"summary":"需求已满足"}`},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if !result.ValidationPassed || !result.Verified {
		t.Fatalf("确定性通过且审查通过时应验证通过: %+v", result)
	}
	if result.ReviewStatus != domain.ReviewStatusPassed {
		t.Fatalf("审查状态不符: %s", result.ReviewStatus)
	}
	if result.Assessment == nil || !result.Assessment.Passed {
		t.Fatalf("必须持久化可解释的评估: %+v", result.Assessment)
	}
	// 开发与审查必须各用一次独立调用，且角色类型不同。
	roles := fixture.port.roles()
	if len(roles) != 2 {
		t.Fatalf("应发生 2 次 Agent 调用，实际 %v", roles)
	}
	if roles[0] != string(domain.RoleTypeDeveloper) || roles[1] != string(domain.RoleTypeValidator) {
		t.Fatalf("角色调用顺序不符: %v", roles)
	}
	// 验证记录必须保留审查会话、评估与结论。
	history := fixture.validationHistory(t)
	if len(history.Entries) != 1 {
		t.Fatalf("应只有一条验证记录，实际 %d", len(history.Entries))
	}
	run := history.Entries[0].Run
	if run.ReviewStatus != domain.ReviewStatusPassed {
		t.Fatalf("验证记录审查状态不符: %s", run.ReviewStatus)
	}
	if run.Assessment == nil || !run.Assessment.Passed {
		t.Fatalf("验证记录必须保存评估: %+v", run.Assessment)
	}
	if run.ValidatorSessionID == nil {
		t.Fatal("验证记录必须关联 Validator 会话")
	}
	validatorSession, err := fixture.sessions.Load(fixture.ctx, *run.ValidatorSessionID)
	if err != nil {
		t.Fatalf("读取审查会话失败: %v", err)
	}
	if validatorSession.RoleSnapshot.Type != domain.RoleTypeValidator {
		t.Fatalf("审查会话角色不符: %s", validatorSession.RoleSnapshot.Type)
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusCompleted {
		t.Fatalf("步骤状态不符: %s", step.Status)
	}
}

// TestReviewCannotOverrideDeterministicFailure 验证确定性失败 + 审查声称通过仍失败。
func TestReviewCannotOverrideDeterministicFailure(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{status: agent.RunCompleted, finalText: `{"passed":true,"summary":"我认为通过"}`},
		},
		checkReplies: map[string]workflowCheckReply{"go": failedWorkflowCheck("编译失败")},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationVerdict != domain.VerificationVerdictFailed {
		t.Fatalf("必需检查失败时结论必须是 failed，实际 %s", result.ValidationVerdict)
	}
	if result.Verified {
		t.Fatal("必需检查失败时不得标记为已验证完成")
	}
	// 必需检查没有通过时不应发起审查调用：那是一次注定被忽略的模型调用。
	if fixture.port.agentCalls() != 1 {
		t.Fatalf("必需检查失败时不应调用 Validator，实际 %d 次", fixture.port.agentCalls())
	}
	if result.ReviewStatus != domain.ReviewStatusNotRequested {
		t.Fatalf("未发起审查时状态应为空，实际 %s", result.ReviewStatus)
	}
	// 确定性失败证据必须保留。
	failed := false
	for _, check := range result.Checks {
		if !check.Status.Passed() && check.Required {
			failed = true
		}
	}
	if !failed {
		t.Fatal("必须保留确定性失败证据")
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusFailed {
		t.Fatalf("步骤状态不符: 期望 failed，实际 %s", step.Status)
	}
}

// TestReviewFailureTightensPassed 验证确定性通过 + 审查未通过时最终失败。
func TestReviewFailureTightensPassed(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{
				status:    agent.RunCompleted,
				finalText: `{"passed":false,"summary":"需求未满足","findings":["缺少错误处理"]}`,
			},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationPassed {
		t.Fatal("审查未通过时不得判定验证通过")
	}
	if result.ValidationVerdict != domain.VerificationVerdictFailed {
		t.Fatalf("结论不符: %s", result.ValidationVerdict)
	}
	if !strings.Contains(result.ValidationFailureSummary, "缺少错误处理") {
		t.Fatalf("失败原因应可追溯到审查发现: %s", result.ValidationFailureSummary)
	}
	if result.Assessment == nil || result.Assessment.Passed {
		t.Fatalf("必须保存未通过的评估: %+v", result.Assessment)
	}
	step := fixture.reloadStep(t)
	if step.Status == domain.StepStatusCompleted {
		t.Fatal("审查未通过时步骤不得进入 completed")
	}
}

// TestReviewNeedsHumanReviewBlocksCompletion 验证要求人工复核时最终不能通过。
func TestReviewNeedsHumanReviewBlocksCompletion(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{
				status:    agent.RunCompleted,
				finalText: `{"passed":true,"needs_human_review":true,"summary":"无法确认边界处理"}`,
			},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationPassed || result.Verified {
		t.Fatal("要求人工复核时不得判定通过")
	}
	if result.ValidationVerdict != domain.VerificationVerdictInconclusive {
		t.Fatalf("结论应为不确定，实际 %s", result.ValidationVerdict)
	}
	if result.ReviewStatus != domain.ReviewStatusNeedsHumanReview {
		t.Fatalf("审查状态不符: %s", result.ReviewStatus)
	}
	if result.Verification != domain.StepVerificationInconclusive {
		t.Fatalf("验证状态应为不确定，实际 %s", result.Verification)
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤状态不符: 期望 recovery_required，实际 %s", step.Status)
	}
	task := fixture.reloadTask(t)
	if task.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("任务状态不符: 期望 recovery_required，实际 %s", task.Status)
	}
}

// TestReviewUnparseableOutputBlocksCompletion 验证审查输出非法时不能判通过。
func TestReviewUnparseableOutputBlocksCompletion(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{status: agent.RunCompleted, finalText: "看起来没问题，我认为可以交付。"},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationPassed || result.Verified {
		t.Fatal("审查输出不可解析时不得判定通过")
	}
	if result.ReviewStatus != domain.ReviewStatusUnparseable {
		t.Fatalf("审查状态不符: %s", result.ReviewStatus)
	}
	if result.ValidationVerdict != domain.VerificationVerdictInconclusive {
		t.Fatalf("结论应为不确定，实际 %s", result.ValidationVerdict)
	}
	if result.ReviewSummary == "" {
		t.Fatal("必须保留可诊断的审查说明")
	}
}

// TestReviewCallFailureBlocksCompletion 验证审查调用失败时不能判通过。
func TestReviewCallFailureBlocksCompletion(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{status: agent.RunFailed, failure: "Pi 进程非零退出", exitCode: 1},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationPassed || result.Verified {
		t.Fatal("审查调用失败时不得判定通过")
	}
	if result.ReviewStatus != domain.ReviewStatusCallFailed {
		t.Fatalf("审查状态不符: %s", result.ReviewStatus)
	}
	if result.ValidationVerdict != domain.VerificationVerdictInconclusive {
		t.Fatalf("结论应为不确定，实际 %s", result.ValidationVerdict)
	}
	if result.ValidationPersisted != true {
		t.Fatal("审查失败本身不妨碍验证结论持久化")
	}
	history := fixture.validationHistory(t)
	if len(history.Entries) != 1 {
		t.Fatalf("必须保留验证记录，实际 %d 条", len(history.Entries))
	}
	if history.Entries[0].Run.ReviewStatus != domain.ReviewStatusCallFailed {
		t.Fatalf("验证记录应保存审查失败状态: %s", history.Entries[0].Run.ReviewStatus)
	}
}

// TestReviewTimeoutBlocksCompletion 验证审查超时时不能判通过。
func TestReviewTimeoutBlocksCompletion(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{status: agent.RunTimedOut, failure: "调用超时"},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationPassed {
		t.Fatal("审查超时时不得判定通过")
	}
	if result.ReviewStatus != domain.ReviewStatusTimedOut {
		t.Fatalf("审查状态不符: %s", result.ReviewStatus)
	}
	if result.ValidationVerdict != domain.VerificationVerdictInconclusive {
		t.Fatalf("结论应为不确定，实际 %s", result.ValidationVerdict)
	}
}

// TestReviewPersistenceFailureIsNotSuccess 验证评估写入失败时不报告可靠成功。
func TestReviewPersistenceFailureIsNotSuccess(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		failFinish: true,
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{status: agent.RunCompleted, finalText: `{"passed":true,"summary":"需求已满足"}`},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, execErr := fixture.execute(t)
	if !errors.Is(execErr, app.ErrValidationNotPersisted) {
		t.Fatalf("结论写入失败时应返回持久化错误，实际 %v", execErr)
	}
	if result.ValidationPersisted {
		t.Fatal("写入失败时 ValidationPersisted 不能为 true")
	}
	if result.Verified {
		t.Fatal("写入失败时不得标记为已验证完成")
	}
	step := fixture.reloadStep(t)
	if step.Status == domain.StepStatusCompleted {
		t.Fatal("验证结论未能可靠写入时步骤不得进入 completed")
	}
}

// TestReviewNotConfiguredBlocksCompletion 验证缺少验证角色时按明确语义收敛。
func TestReviewNotConfiguredBlocksCompletion(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		validatorRoleID: func() *domain.ID {
			missing := domain.ID("validator-missing")
			return &missing
		}(),
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationPassed || result.Verified {
		t.Fatal("验证角色不可用时不得判定通过")
	}
	if result.ReviewStatus != domain.ReviewStatusNotConfigured {
		t.Fatalf("审查状态不符: %s", result.ReviewStatus)
	}
	if result.ValidationVerdict != domain.VerificationVerdictInconclusive {
		t.Fatalf("结论应为不确定，实际 %s", result.ValidationVerdict)
	}
	// 配置问题不应启动一次注定失败的模型调用。
	if fixture.port.agentCalls() != 1 {
		t.Fatalf("验证角色不可用时不应调用 Validator，实际 %d 次", fixture.port.agentCalls())
	}
}

// TestReviewOptionalWhenTaskHasNoValidatorRole 验证未配置验证角色时按既有语义判定。
func TestReviewOptionalWhenTaskHasNoValidatorRole(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		omitValidatorRole: true,
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if !result.ValidationPassed || !result.Verified {
		t.Fatalf("未要求语义审查时只依据确定性检查判定: %+v", result)
	}
	if result.ReviewRan {
		t.Fatal("未配置验证角色时不得发起审查")
	}
	if result.ReviewStatus != domain.ReviewStatusNotRequested {
		t.Fatalf("审查状态应为空，实际 %s", result.ReviewStatus)
	}
}

// TestReviewHistoryIsAppendOnly 验证重复执行不会覆盖历史验证记录。
func TestReviewHistoryIsAppendOnly(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: `{"passed":false,"summary":"未满足需求"}`},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	first, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("第一次执行失败: %v", err)
	}
	if first.ValidationPassed {
		t.Fatal("第一轮审查未通过时不得判定通过")
	}
	// 第二轮：审查通过并验证通过。failed 步骤本身允许再次执行，不需要人工重置，
	// 因此这里只准备第二轮要返回的受控结果。
	step := fixture.reloadStep(t)
	if !step.Status.CanStartExecution() {
		t.Fatalf("第一轮失败后步骤应可再次执行，实际 %s", step.Status)
	}
	fixture.port.mu.Lock()
	fixture.port.agentReplies = append(fixture.port.agentReplies,
		reviewReply{status: agent.RunCompleted, finalText: "第二次实现"},
		reviewReply{status: agent.RunCompleted, finalText: `{"passed":true,"summary":"已满足"}`},
	)
	fixture.port.mu.Unlock()
	second, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("第二次执行失败: %v", err)
	}
	if !second.ValidationPassed || !second.Verified {
		t.Fatalf("第二轮应验证通过: %+v", second)
	}
	history := fixture.validationHistory(t)
	if len(history.Entries) != 2 {
		t.Fatalf("历史验证记录必须保留，实际 %d 条", len(history.Entries))
	}
	if history.Entries[0].Run.ID == history.Entries[1].Run.ID {
		t.Fatal("每轮验证必须是独立记录")
	}
	if history.Entries[0].Run.ReviewStatus != domain.ReviewStatusFailed {
		t.Fatalf("第一轮审查结论被覆盖: %s", history.Entries[0].Run.ReviewStatus)
	}
	if history.Entries[1].Run.ReviewStatus != domain.ReviewStatusPassed {
		t.Fatalf("第二轮审查结论不符: %s", history.Entries[1].Run.ReviewStatus)
	}
	if history.State != domain.StepVerificationPassed {
		t.Fatalf("派生状态应取最新一轮: %s", history.State)
	}
}

// TestReviewSessionIsNotReused 验证审查使用独立会话，不复用开发会话。
func TestReviewSessionIsNotReused(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{status: agent.RunCompleted, finalText: `{"passed":true,"summary":"已满足"}`},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	developerSession := result.SessionID
	history := fixture.validationHistory(t)
	if len(history.Entries) != 1 {
		t.Fatalf("应只有一条验证记录，实际 %d", len(history.Entries))
	}
	validatorSessionID := history.Entries[0].Run.ValidatorSessionID
	if validatorSessionID == nil {
		t.Fatal("验证记录必须关联 Validator 会话")
	}
	if *validatorSessionID == developerSession {
		t.Fatal("Validator 不得复用 Developer 会话")
	}
	sessions := fixture.sessionsOf(t)
	validatorFound := false
	for _, session := range sessions {
		if session.ID != *validatorSessionID {
			continue
		}
		validatorFound = true
		if session.AttemptID == nil || *session.AttemptID != result.AttemptID {
			t.Fatalf("审查会话必须关联本次开发尝试: %+v", session.AttemptID)
		}
		if session.RoleSnapshot.Type != domain.RoleTypeValidator {
			t.Fatalf("审查会话角色不符: %s", session.RoleSnapshot.Type)
		}
	}
	if !validatorFound {
		t.Fatal("数据库中必须存在审查会话记录")
	}
}

// TestReviewPromptCarriesDeterministicEvidence 验证审查提示词包含确定性检查事实。
func TestReviewPromptCarriesDeterministicEvidence(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
			{status: agent.RunCompleted, finalText: `{"passed":true,"summary":"已满足"}`},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	if _, err := fixture.execute(t); err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	request := fixture.port.lastRequest(t)
	if request.RoleType != string(domain.RoleTypeValidator) {
		t.Fatalf("最后一次调用必须来自验证角色，实际 %s", request.RoleType)
	}
	if !strings.Contains(request.Prompt, "确定性检查结果") {
		t.Fatalf("审查提示词必须包含确定性检查事实: %s", request.Prompt)
	}
	if !strings.Contains(request.Prompt, "构建") {
		t.Fatalf("审查提示词必须包含检查名称: %s", request.Prompt)
	}
	if !strings.Contains(request.Prompt, `"passed"`) {
		t.Fatalf("审查提示词必须声明结构化输出格式: %s", request.Prompt)
	}
	if request.Model == "" {
		t.Fatal("审查调用必须显式指定模型")
	}
	if request.WorkDir != fixture.task.Config.EffectiveWorkDir() {
		t.Fatalf("审查必须使用任务工作区: %s", request.WorkDir)
	}
	if request.AttemptID != "" && request.StepID == "" {
		t.Fatal("审查调用必须带步骤归属")
	}
}

// passedWorkflowCheck 返回一条通过的检查结果。
func passedWorkflowCheck() workflowCheckReply {
	return workflowCheckReply{exitCode: 0, stdout: "ok", started: true, certain: true}
}

// failedWorkflowCheck 返回一条退出码失败的检查结果。
func failedWorkflowCheck(stderr string) workflowCheckReply {
	return workflowCheckReply{exitCode: 1, stderr: stderr, started: true, certain: true}
}

// workflowExecResult 把受控检查结果转换为执行层的执行结果。
//
// 转换规则与既有闭环用例保持一致：启动失败的检查不属于"命令以非零码结束"，
// 因此状态与 Certain 必须能区分这两类事实。
func workflowExecResult(reply workflowCheckReply) (app.ExecResult, error) {
	execErr := reply.err
	status := sshclient.CommandStatusSucceeded
	switch {
	case execErr != nil && !reply.started:
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
	}}, nil
}

// TestReviewRequiredButUnavailableBlocksCompletion 验证任务声明了验证角色但服务未装配
// 语义审查能力时不得判通过。
func TestReviewRequiredButUnavailableBlocksCompletion(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, reviewOptions{
		noReviewer: true,
		agentReplies: []reviewReply{
			{status: agent.RunCompleted, finalText: "实现完成"},
		},
		checkReplies: map[string]workflowCheckReply{"go": passedWorkflowCheck()},
	})
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("执行闭环失败: %v", err)
	}
	if result.ValidationPassed || result.Verified {
		t.Fatal("要求的语义审查无法执行时不得判定通过")
	}
	if result.ReviewStatus != domain.ReviewStatusNotConfigured {
		t.Fatalf("审查状态不符: %s", result.ReviewStatus)
	}
	if result.ValidationVerdict != domain.VerificationVerdictInconclusive {
		t.Fatalf("结论应为不确定，实际 %s", result.ValidationVerdict)
	}
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤状态不符: 期望 recovery_required，实际 %s", step.Status)
	}
}
