// Package app_test 提供任务级工作流编排器的单元测试。
//
// 用例只依赖数据库与受控替身：步骤闭环、容器核验与代码保存都由替身提供，因此默认
// 执行路径不访问真实 Pi、模型、SSH 或 Docker，全部落在测试工作空间内。
package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// taskWorkflowReplyKind 描述一次受控闭环结果的类型。
type taskWorkflowReplyKind int

const (
	// taskReplyVerified 表示开发与验证都通过。
	taskReplyVerified taskWorkflowReplyKind = iota
	// taskReplyVerificationFailed 表示验证未通过。
	taskReplyVerificationFailed
	// taskReplyDeveloperFailed 表示开发阶段失败。
	taskReplyDeveloperFailed
	// taskReplyInfrastructure 表示验证因基础设施异常没有拿到证据。
	taskReplyInfrastructure
	// taskReplyError 表示闭环协调器直接返回错误。
	taskReplyError
	// taskReplyBlocked 表示闭环阻塞直到上下文取消。
	taskReplyBlocked
	// taskReplyRepairStop 表示修复循环以结构化类别停止。
	taskReplyRepairStop
)

// taskWorkflowReply 是一次受控的步骤闭环结果。
type taskWorkflowReply struct {
	kind taskWorkflowReplyKind
	err  error
	// completes 表示闭环是否把步骤在数据库中收口为 completed。
	completes bool
	// repairClass / repairResult 只在 taskReplyRepairStop 时使用：
	// 前者是修复循环给出的停止类别，后者是期望的步骤终态。
	repairClass  domain.RepairFailureClass
	repairResult domain.StepStatus
}

// taskWorkflowRunner 是单步骤闭环服务的替身。
//
// 它记录收到的请求并顺序返回受控结果，使编排器可以在不启动 Pi 的情况下被完整验证。
type taskWorkflowRunner struct {
	mu       sync.Mutex
	db       *sqlite.DB
	replies  []taskWorkflowReply
	requests []app.ExecuteAndValidateRequest
	calls    int
}

// ExecuteAndValidate 返回受控的闭环结果。
func (r *taskWorkflowRunner) ExecuteAndValidate(
	ctx context.Context,
	request app.ExecuteAndValidateRequest,
) (app.ExecuteAndValidateResult, error) {
	r.mu.Lock()
	index := r.calls
	r.calls++
	r.requests = append(r.requests, request)
	db := r.db
	reply := taskWorkflowReply{kind: taskReplyVerified, completes: true}
	if index < len(r.replies) {
		reply = r.replies[index]
	}
	r.mu.Unlock()

	stepID := domain.ID(request.StepID)
	result := app.ExecuteAndValidateResult{
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
	}
	switch reply.kind {
	case taskReplyVerificationFailed:
		result.ValidationVerdict = domain.VerificationVerdictFailed
		result.ValidationPassed = false
		result.ValidationFailureSummary = "必需检查 build 未通过"
		result.Verification = domain.StepVerificationFailed
		result.StepStatus = domain.StepStatusFailed
		result.Verified = false
	case taskReplyDeveloperFailed:
		result.Outcome = domain.StepExecutionFailed
		result.DeveloperFailure = "Pi 进程非零退出"
		result.ValidationRan = false
		result.ValidationStatus = ""
		result.ValidationVerdict = ""
		result.ValidationPassed = false
		result.ValidationPersisted = false
		result.Verification = domain.StepVerificationUnverified
		result.StepStatus = domain.StepStatusFailed
		result.Verified = false
	case taskReplyInfrastructure:
		result.InfrastructureError = true
		result.ValidationStatus = domain.ValidationRunStatusFailed
		result.ValidationVerdict = domain.VerificationVerdictInconclusive
		result.ValidationPassed = false
		result.ValidationFailureSummary = "容器内命令无法启动"
		result.Verification = domain.StepVerificationInfrastructure
		result.StepStatus = domain.StepStatusRecoveryRequired
		result.Verified = false
	case taskReplyError:
		return app.ExecuteAndValidateResult{}, reply.err
	case taskReplyRepairStop:
		// 修复循环停止：步骤终态与停止类别都来自领域判定，替身只做投影。
		result.Verification = domain.StepVerificationFailed
		result.StepStatus = reply.repairResult
		result.Verified = false
		result.Attempts = 2
		result.Round = 2
		result.ValidationFailureSummary = "必需检查 build 未通过"
		result.Repair = domain.RepairDecision{
			Kind:    domain.RepairDecisionStop,
			Class:   reply.repairClass,
			Summary: "修复循环因 " + string(reply.repairClass) + " 停止",
		}
		if reply.repairClass != domain.RepairClassDeveloperFailed &&
			reply.repairClass != domain.RepairClassDeveloperTimedOut &&
			reply.repairClass != domain.RepairClassDeveloperInterrupted {
			result.FeedbackPersisted = true
		}
		if reply.repairClass == domain.RepairClassInfrastructure ||
			reply.repairClass == domain.RepairClassNotPersisted ||
			reply.repairClass == domain.RepairClassVerificationNotRun {
			result.InfrastructureError = true
		}
	case taskReplyBlocked:
		<-ctx.Done()
		return app.ExecuteAndValidateResult{}, ctx.Err()
	}
	if reply.completes && db != nil {
		if err := completeStepInDatabase(ctx, db, stepID); err != nil {
			return app.ExecuteAndValidateResult{}, fmt.Errorf("替身收口步骤失败: %w", err)
		}
	}
	return result, nil
}

// completeStepInDatabase 把步骤推进为已完成。
//
// 替身闭环不写数据库，因此需要显式模拟"闭环已把步骤收口为 completed"，使编排器
// 后续的 NextStepToExecute 能按真实状态推进。
func completeStepInDatabase(ctx context.Context, db *sqlite.DB, stepID domain.ID) error {
	step, err := db.GetStep(ctx, stepID)
	if err != nil {
		return err
	}
	// 真实闭环会把步骤依次推进为 running → verifying → completed；替身按同一路径
	// 收口，避免用非法迁移伪造"已验证完成"。
	for _, next := range []domain.StepStatus{
		domain.StepStatusRunning,
		domain.StepStatusVerifying,
		domain.StepStatusCompleted,
	} {
		if step.Status == next {
			continue
		}
		if err := step.Transition(next, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := step.SetCurrentAttempt(nil); err != nil {
		return err
	}
	return db.UpdateStep(ctx, *step)
}

// callCount 返回闭环调用次数。
func (r *taskWorkflowRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// receivedRequests 返回收到的闭环请求。
func (r *taskWorkflowRunner) receivedRequests() []app.ExecuteAndValidateRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]app.ExecuteAndValidateRequest(nil), r.requests...)
}

// taskWorkflowContainer 是容器核验的替身。
type taskWorkflowContainer struct {
	mu         sync.Mutex
	inspectErr error
	detached   bool
	removed    bool
	running    bool
	inspected  int
	// agentReplies 是计划生成路径上依次返回的 Agent 调用结果。
	agentReplies []fakeAgentReply
	// agentRequests 记录收到的 Agent 调用请求。
	agentRequests []app.ContainerAgentRequest
	// agentCalls 统计 Agent 调用次数。
	agentCalls int
}

// InspectContainer 返回受控的容器状态。
func (p *taskWorkflowContainer) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inspected++
	if p.inspectErr != nil {
		return app.ContainerDetail{}, p.inspectErr
	}
	state := domain.ContainerStateRunning
	if p.removed {
		state = domain.ContainerStateRemoved
	}
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   "host-1",
			Name:     "agentflow-workflow",
			DockerID: "docker-" + request.ContainerID,
			State:    state,
			Detached: p.detached,
		},
		Inspect: &docker.ContainerInspect{Running: p.running},
	}, nil
}

// RunAgent 返回受控的 Agent 调用结果。
//
// 计划生成用例通过它注入 Planner 的最终输出，因此不需要真实 Pi、模型或 Docker。
func (p *taskWorkflowContainer) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	p.mu.Lock()
	p.agentRequests = append(p.agentRequests, request)
	p.agentCalls++
	index := p.agentCalls - 1
	reply := fakeAgentReply{status: agent.RunCompleted, finalText: "已完成"}
	if index < len(p.agentReplies) {
		reply = p.agentReplies[index]
	}
	p.mu.Unlock()
	now := time.Now().UTC()
	sessionID := domain.ID(strings.TrimSpace(request.SessionID))
	if sessionID.IsEmpty() {
		sessionID = domain.NewID()
	}
	result := agent.RunResult{
		SessionID:    sessionID.String(),
		InvocationID: request.InvocationID,
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
	// 会话记录由调用方的会话服务写入（SessionService.Start），替身不重复写入：
	// 重复写入会与真实记录冲突，也会掩盖"谁负责持久化会话"的职责边界。
	return app.ContainerAgentResult{Result: result}, reply.err
}

// taskWorkflowCodeSaver 是代码保存的替身。
type taskWorkflowCodeSaver struct {
	mu     sync.Mutex
	record domain.CodeSaveRecord
	err    error
	calls  int
}

// SaveCode 返回受控的代码保存结果。
func (s *taskWorkflowCodeSaver) SaveCode(
	ctx context.Context,
	request app.CodeSaveRequest,
) (app.CodeSaveResult, error) {
	s.mu.Lock()
	s.calls++
	record := s.record
	err := s.err
	s.mu.Unlock()
	result := app.CodeSaveResult{Result: codesaver.Result{Record: record}}
	if err != nil {
		return result, err
	}
	return result, nil
}

// callCount 返回代码保存调用次数。
func (s *taskWorkflowCodeSaver) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// taskWorkflowStore 包装真实存储，只把指定的写入方法替换为可控失败。
//
// 它是"执行完成但进度写入失败"这条路径的注入点：业务代码不变，失败来自存储层，
// 因此验证的是真实的错误处理与报告逻辑。
type taskWorkflowStore struct {
	app.TaskWorkflowStore
	updateRunErr error
	createRunErr error
	// sessions 非空时用它替代真实会话查询结果，用于覆盖数据库无法表达的归档状态。
	sessions []domain.Session
}

// UpdateWorkflowRun 在配置了错误时返回该错误。
func (s *taskWorkflowStore) UpdateWorkflowRun(
	ctx context.Context,
	run domain.WorkflowRun,
) error {
	if s.updateRunErr != nil {
		return s.updateRunErr
	}
	return s.TaskWorkflowStore.UpdateWorkflowRun(ctx, run)
}

// CreateWorkflowRun 在配置了错误时返回该错误。
func (s *taskWorkflowStore) CreateWorkflowRun(
	ctx context.Context,
	run domain.WorkflowRun,
) error {
	if s.createRunErr != nil {
		return s.createRunErr
	}
	return s.TaskWorkflowStore.CreateWorkflowRun(ctx, run)
}

// ListSessionsByTask 在配置了替身时返回替身结果。
//
// 数据库的 archive_path 约束不允许"归档成功但位置为空"，而读取层必须能承受这类
// 历史数据，因此收尾判定的边界场景通过这里注入。
func (s *taskWorkflowStore) ListSessionsByTask(
	ctx context.Context,
	taskID domain.ID,
) ([]domain.Session, error) {
	if s.sessions != nil {
		return append([]domain.Session(nil), s.sessions...), nil
	}
	return s.TaskWorkflowStore.ListSessionsByTask(ctx, taskID)
}

// taskWorkflowFixture 是一套可直接启动任务级工作流的测试数据。
type taskWorkflowFixture struct {
	db         *sqlite.DB
	store      *taskWorkflowStore
	runner     *taskWorkflowRunner
	containers *taskWorkflowContainer
	codeSaver  *taskWorkflowCodeSaver
	service    *app.TaskWorkflowService
	task       domain.Task
	steps      []domain.Step
	ctx        context.Context
	workspace  *testkit.Workspace
	logger     *logging.Logger
	// now 是构造测试数据时使用的时间。
	now time.Time
}

// taskWorkflowOptions 允许用例调整测试数据的构造方式。
type taskWorkflowOptions struct {
	// stepCount 是计划步骤数量，默认 2。
	stepCount int
	// noSteps 为 true 时不写入任何计划步骤，用于验证"计划缺失"。
	noSteps bool
	// replies 是闭环调用的受控结果。
	replies []taskWorkflowReply
	// codeSaver 为 true 时提供代码保存能力。
	codeSaver bool
	// codeSaveRecord 是代码保存返回的记录。
	codeSaveRecord domain.CodeSaveRecord
	// codeSaveErr 是代码保存返回的错误。
	codeSaveErr error
	// containerDetached 表示容器已脱离管理。
	containerDetached bool
	// containerRemoved 表示容器已删除。
	containerRemoved bool
	// containerStopped 表示容器不可执行命令。
	containerStopped bool
	// inspectErr 使容器核验失败。
	inspectErr error
	// updateRunErr 使执行记录写入失败。
	updateRunErr error
	// createRunErr 使执行记录创建失败。
	createRunErr error
	// existingRun 预先写入一条执行记录，用于验证并发与恢复。
	existingRun func(t *testing.T, task domain.Task, steps []domain.Step) domain.WorkflowRun
	// sessions 为 true 时预先写入一条完整且已归档的会话。
	sessions bool
	// session 非空时按给定取值调整预置会话的完整性与归档字段。
	session func(session domain.Session) domain.Session
	// hostStatus 覆盖执行节点校验状态，空值表示 normal。
	hostStatus domain.ValidationStatus
	// lease 覆盖执行权有效期，零值使用服务默认值。
	//
	// 短租约配合阻塞的闭环替身，可以在毫秒级完成"单次调用跨越租约"的确定性验证，
	// 不需要长时间 Sleep。
	lease time.Duration
}

// newTaskWorkflowFixture 在工作空间中准备一套可启动工作流的测试数据。
func newTaskWorkflowFixture(t *testing.T, options taskWorkflowOptions) *taskWorkflowFixture {
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
	role := createManagementRole(t, db, ctx, domain.RoleTypeDeveloper, "开发角色", now)
	host := createManagementHost(t, db, ctx, now)
	switch options.hostStatus {
	case domain.ValidationStatusAbnormal:
		host = markHostAbnormalForWorkflow(t, db, ctx, host)
	default:
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
	stepCount := options.stepCount
	if stepCount <= 0 && !options.noSteps {
		stepCount = 2
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
			role,
			workflowSpec(),
			domain.RetryConfig{MaxAttempts: 1},
			now,
		)
		if stepErr != nil {
			t.Fatalf("构造步骤失败: %v", stepErr)
		}
		steps = append(steps, step)
	}
	if len(steps) > 0 {
		if err := db.CreateSteps(ctx, steps); err != nil {
			t.Fatalf("写入步骤失败: %v", err)
		}
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
	if options.sessions {
		writeTaskWorkflowSession(t, db, ctx, task, now, options.session)
	}
	if options.existingRun != nil {
		run := options.existingRun(t, task, steps)
		if err := db.CreateWorkflowRun(ctx, run); err != nil {
			t.Fatalf("写入预置执行记录失败: %v", err)
		}
	}
	store := &taskWorkflowStore{
		TaskWorkflowStore: db,
		updateRunErr:      options.updateRunErr,
		createRunErr:      options.createRunErr,
	}
	runner := &taskWorkflowRunner{db: db, replies: options.replies}
	containers := &taskWorkflowContainer{
		inspectErr: options.inspectErr,
		detached:   options.containerDetached,
		removed:    options.containerRemoved,
		running:    !options.containerStopped,
	}
	codeSaver := &taskWorkflowCodeSaver{record: options.codeSaveRecord, err: options.codeSaveErr}
	serviceOptions := app.TaskWorkflowOptions{
		Store:      store,
		Steps:      runner,
		Containers: containers,
		Logger:     logger.Slog(),
	}
	if options.codeSaver {
		serviceOptions.CodeSaver = codeSaver
	}
	if options.lease > 0 {
		serviceOptions.Lease = options.lease
	}
	service, err := app.NewTaskWorkflowService(serviceOptions)
	if err != nil {
		t.Fatalf("构造工作流编排器失败: %v", err)
	}
	startTaskWorkflowService(t, service)
	return &taskWorkflowFixture{
		db:         db,
		store:      store,
		runner:     runner,
		containers: containers,
		codeSaver:  codeSaver,
		service:    service,
		task:       task,
		steps:      steps,
		ctx:        ctx,
		workspace:  workspace,
		logger:     logger,
		now:        now,
	}
}

// markHostAbnormalForWorkflow 把执行节点置为预检失败。
func markHostAbnormalForWorkflow(
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
	if err := stored.FailValidation(
		stored.ValidationGeneration,
		"预检失败：docker 不可用",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("标记校验失败失败: %v", err)
	}
	if _, err := db.UpdateExecutionHostValidation(ctx, *stored, stored.ValidationGeneration); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	return *stored
}

// writeTaskWorkflowSession 写入一条完整且已归档的会话记录。
//
// adjust 非空时在写入前调整完整性与归档字段，使用例可以表达 HTML 导出失败与归档
// 失败等真实状态；无论怎样调整，写入前都要求字段自洽。
//
// 数据库的 archive_path 有"非空"约束，因此"归档成功但位置为空"这类历史数据由
// baseSession 构造后直接交给收尾判定，不经过写入路径。
func writeTaskWorkflowSession(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	now time.Time,
	adjust func(session domain.Session) domain.Session,
) domain.Session {
	t.Helper()
	session := baseSession(t, db, ctx, task, now, adjust)
	if err := session.Complete(now); err != nil {
		t.Fatalf("完成会话失败: %v", err)
	}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatalf("写入会话失败: %v", err)
	}
	return session
}

// baseSession 构造一条完整且已归档的会话记录，不写入数据库。
func baseSession(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	task domain.Task,
	now time.Time,
	adjust func(session domain.Session) domain.Session,
) domain.Session {
	t.Helper()
	role, err := db.GetRole(ctx, task.Config.DeveloperRoleID)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	session, err := domain.NewSession(
		domain.SessionScope{TaskID: task.ID},
		*role,
		"pi",
		"group/free",
		now,
	)
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	if err := session.MarkRunning("pi-session-1"); err != nil {
		t.Fatalf("标记会话运行失败: %v", err)
	}
	session.Completeness = domain.SessionCompletenessComplete
	session.ExportStatus = domain.SessionExportNotAttempted
	session.JSONLPath = "sessions/session.jsonl"
	session.ArchiveStatus = domain.SessionArchiveSucceeded
	session.ArchivePath = "sessions/archive.jsonl"
	session.RunStatus = domain.SessionRunCompleted
	if adjust != nil {
		session = adjust(session)
	}
	return session
}

// startTaskWorkflowService 启动编排器并在用例结束时停止。
func startTaskWorkflowService(t *testing.T, service *app.TaskWorkflowService) {
	t.Helper()
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
}

// succeededCodeSaveRecord 构造一条"已安全保存"的代码保存记录。
func succeededCodeSaveRecord() domain.CodeSaveRecord {
	return domain.CodeSaveRecord{
		ID:           domain.NewID(),
		TaskID:       "task",
		ContainerID:  "container-1",
		RemoteName:   "origin",
		TaskBranch:   "agent/task",
		CommitStatus: domain.CommitStatusSucceeded,
		CommitSHA:    "0123456789012345678901234567890123456789",
		PushStatus:   domain.PushStatusSucceeded,
		MergeStatus:  domain.MergeStatusNotRequested,
	}
}

// waitTaskWorkflowStatus 轮询工作流状态直到满足条件或超时。
func waitTaskWorkflowStatus(
	t *testing.T,
	service *app.TaskWorkflowService,
	taskID domain.ID,
	accept func(app.WorkflowStatusResult) bool,
) app.WorkflowStatusResult {
	t.Helper()
	const timeout = 20 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	deadline := time.Now().Add(timeout)
	var last app.WorkflowStatusResult
	for time.Now().Before(deadline) {
		result, err := service.GetWorkflowStatus(ctx, taskID.String())
		if err != nil {
			t.Fatalf("查询工作流状态失败: %v", err)
		}
		last = result
		if result.HasRun && accept(result) {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待工作流状态超时，最后一次状态: %+v", last.Run)
	return last
}

// workflowSettled 判断工作流是否已经停止自动推进。
//
// pending 与 running 都还在推进或尚未开始，不能作为"已停止"接受，否则用例会在
// 编排真正开始之前就通过。
func workflowSettled(status domain.WorkflowRunStatus) bool {
	if status.IsTerminal() {
		return true
	}
	return status == domain.WorkflowRunBlocked || status == domain.WorkflowRunRecoveryRequired
}

// waitTaskRunStatus 等待工作流执行进入指定状态集合。
func waitTaskRunStatus(
	t *testing.T,
	service *app.TaskWorkflowService,
	taskID domain.ID,
	accept func(domain.WorkflowRunStatus) bool,
) app.WorkflowStatusResult {
	t.Helper()
	return waitTaskWorkflowStatus(t, service, taskID, func(result app.WorkflowStatusResult) bool {
		if !accept(result.Run.Status) {
			return false
		}
		// 收尾的写入顺序是"先执行记录、后任务"：执行记录写成 completed 之后，任务的
		// 成功终态才写入。任务仍在推进说明第二步还没落盘，需要继续等待。
		if result.Run.Status == domain.WorkflowRunCompleted {
			return !result.TaskStatus.IsActive()
		}
		return true
	})
}

// TestWorkflowExecutesStepsInOrder 验证多个步骤按稳定顺序执行。
func TestWorkflowExecutesStepsInOrder(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:      2,
		codeSaver:      true,
		sessions:       true,
		codeSaveRecord: succeededCodeSaveRecord(),
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		func(status domain.WorkflowRunStatus) bool { return status.IsTerminal() })
	if result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("工作流应完成，实际 %s（原因 %s）", result.Run.Status, result.Run.ErrorSummary)
	}
	requests := fixture.runner.receivedRequests()
	if len(requests) != 2 {
		t.Fatalf("应当执行 2 个步骤，实际 %d", len(requests))
	}
	if requests[0].StepID != fixture.steps[0].ID.String() {
		t.Fatalf("第一步顺序错误: %s", requests[0].StepID)
	}
	if requests[1].StepID != fixture.steps[1].ID.String() {
		t.Fatalf("第二步顺序错误: %s", requests[1].StepID)
	}
	if result.Run.CompletedSteps != 2 || result.Run.TotalSteps != 2 {
		t.Fatalf("进度记录不正确: %+v", result.Run)
	}
	if fixture.codeSaver.callCount() != 1 {
		t.Fatalf("收尾应当执行一次代码保存，实际 %d", fixture.codeSaver.callCount())
	}
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("全部条件满足后任务应完成，实际 %s", task.Status)
	}
}

// TestWorkflowStopsWhenVerificationFails 验证验证失败后不再启动下一步骤。
func TestWorkflowStopsWhenVerificationFails(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:      2,
		codeSaver:      true,
		sessions:       true,
		codeSaveRecord: succeededCodeSaveRecord(),
		replies: []taskWorkflowReply{
			{kind: taskReplyVerificationFailed},
			{kind: taskReplyVerified, completes: true},
		},
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		workflowSettled)
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("验证失败后工作流应阻塞，实际 %s", result.Run.Status)
	}
	if result.Run.ErrorClass != "verification_failed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	if calls := fixture.runner.callCount(); calls != 1 {
		t.Fatalf("验证失败后不应继续执行下一步，实际执行 %d 次", calls)
	}
	if fixture.codeSaver.callCount() != 0 {
		t.Fatalf("验证失败时不应执行代码保存，实际 %d", fixture.codeSaver.callCount())
	}
	if result.Run.CompletedSteps != 0 {
		t.Fatalf("失败的步骤不应计入已完成进度: %d", result.Run.CompletedSteps)
	}
}

// TestWorkflowStopsWhenDeveloperFails 验证开发失败后停止工作流。
func TestWorkflowStopsWhenDeveloperFails(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 2,
		replies:   []taskWorkflowReply{{kind: taskReplyDeveloperFailed}},
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		workflowSettled)
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("开发失败后工作流应阻塞，实际 %s", result.Run.Status)
	}
	if result.Run.ErrorClass != "developer_failed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	if fixture.runner.callCount() != 1 {
		t.Fatalf("开发失败后不应继续执行，实际 %d 次", fixture.runner.callCount())
	}
}

// TestWorkflowRequiresRecoveryOnInfrastructureError 验证基础设施异常需要人工核验。
func TestWorkflowRequiresRecoveryOnInfrastructureError(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 1,
		replies:   []taskWorkflowReply{{kind: taskReplyInfrastructure}},
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		workflowSettled)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("基础设施异常时应需要恢复核验，实际 %s", result.Run.Status)
	}
	if result.Run.ErrorClass != "verification_unconfirmed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
}

// TestWorkflowDoesNotReportSuccessWithoutCodeSaver 验证缺少代码保存时不伪造成功。
func TestWorkflowDoesNotReportSuccessWithoutCodeSaver(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 1,
		sessions:  true,
		// 不提供 CodeSaver：步骤全部通过验证也不能判定任务完整成功。
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		workflowSettled)
	if result.Run.Status == domain.WorkflowRunCompleted {
		t.Fatal("缺少代码保存能力时不得把工作流标记为完成")
	}
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("缺少代码保存时应阻塞，实际 %s", result.Run.Status)
	}
	if result.Run.ErrorClass != "code_saver_unavailable" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("代码保存未完成时任务不得标记为完成")
	}
}

// TestWorkflowRejectsMissingPlan 验证计划缺失时拒绝启动。
func TestWorkflowRejectsMissingPlan(t *testing.T) {
	t.Parallel()
	empty := newTaskWorkflowFixture(t, taskWorkflowOptions{noSteps: true})
	_, err := empty.service.StartWorkflow(empty.ctx, app.StartWorkflowRequest{
		TaskID: empty.task.ID.String(),
	})
	if err == nil {
		t.Fatal("计划非法时应当拒绝启动")
	}
	if !errors.Is(err, app.ErrWorkflowConflict) {
		t.Fatalf("缺少计划时应返回状态冲突: %v", err)
	}
	status, statusErr := empty.service.GetWorkflowStatus(empty.ctx, empty.task.ID.String())
	if statusErr != nil {
		t.Fatalf("查询状态失败: %v", statusErr)
	}
	if status.HasRun {
		t.Fatal("拒绝启动时不得创建执行记录")
	}
}

// TestWorkflowRejectsUnreadyContainer 验证容器不可用时拒绝启动。
func TestWorkflowRejectsUnreadyContainer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		options taskWorkflowOptions
	}{
		{name: "容器已脱离管理", options: taskWorkflowOptions{stepCount: 1, containerDetached: true}},
		{name: "容器已删除", options: taskWorkflowOptions{stepCount: 1, containerRemoved: true}},
		{name: "容器不可执行命令", options: taskWorkflowOptions{stepCount: 1, containerStopped: true}},
		{name: "容器核验失败", options: taskWorkflowOptions{stepCount: 1, inspectErr: errors.New("ssh 不可达")}},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newTaskWorkflowFixture(t, testCase.options)
			_, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
				TaskID: fixture.task.ID.String(),
			})
			if err == nil {
				t.Fatal("容器不可用时应当拒绝启动")
			}
			if !errors.Is(err, app.ErrWorkflowPrerequisite) {
				t.Fatalf("错误类别应为前置条件未满足: %v", err)
			}
			if fixture.runner.callCount() != 0 {
				t.Fatalf("拒绝启动时不应执行任何步骤，实际 %d", fixture.runner.callCount())
			}
		})
	}
}

// TestWorkflowRejectsUnvalidatedHost 验证节点预检未通过时拒绝启动。
func TestWorkflowRejectsUnvalidatedHost(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:  1,
		hostStatus: domain.ValidationStatusAbnormal,
	})
	_, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err == nil {
		t.Fatal("执行节点未通过预检时应当拒绝启动")
	}
	if !errors.Is(err, app.ErrWorkflowPrerequisite) {
		t.Fatalf("错误类别应为前置条件未满足: %v", err)
	}
}

// TestWorkflowRejectsConcurrentStart 验证并发启动不会创建两次工作流。
func TestWorkflowRejectsConcurrentStart(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 1,
		replies:   []taskWorkflowReply{{kind: taskReplyBlocked}},
	})
	var (
		group      sync.WaitGroup
		mu         sync.Mutex
		successes  int
		conflicts  int
		otherError error
	)
	for index := 0; index < 4; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
				TaskID: fixture.task.ID.String(),
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, app.ErrWorkflowConflict):
				conflicts++
			default:
				otherError = err
			}
		}()
	}
	group.Wait()
	if otherError != nil {
		t.Fatalf("并发启动出现非预期错误: %v", otherError)
	}
	if successes != 1 {
		t.Fatalf("并发启动应只有一次成功，实际 %d 次（冲突 %d 次）", successes, conflicts)
	}
	if conflicts != 3 {
		t.Fatalf("其余请求应返回状态冲突，实际 %d 次", conflicts)
	}
	runs, err := fixture.db.ListWorkflowRunsByTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("并发启动不应创建多条执行记录，实际 %d 条", len(runs))
	}
}

// TestWorkflowProgressIsPersisted 验证进度持久化且多次查询一致。
func TestWorkflowProgressIsPersisted(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:      2,
		codeSaver:      true,
		sessions:       true,
		codeSaveRecord: succeededCodeSaveRecord(),
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		func(status domain.WorkflowRunStatus) bool { return status.IsTerminal() })
	first, err := fixture.service.GetWorkflowStatus(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("查询状态失败: %v", err)
	}
	second, err := fixture.service.GetWorkflowStatus(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("查询状态失败: %v", err)
	}
	if first.Run.RunID != second.Run.RunID || first.Run.Status != second.Run.Status {
		t.Fatalf("多次查询应返回一致状态: %+v vs %+v", first.Run, second.Run)
	}
	if first.Run.LastCompletedStepID == nil {
		t.Fatal("完成后应记录最后一个已确认完成的步骤")
	}
	if first.Run.LastCompletedSequence != 2 {
		t.Fatalf("最后完成序号应为 2，实际 %d", first.Run.LastCompletedSequence)
	}
	stored, err := fixture.db.GetWorkflowRun(fixture.ctx, result.Run.RunID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if stored.Status != domain.WorkflowRunCompleted {
		t.Fatalf("数据库中的状态应为 completed，实际 %s", stored.Status)
	}
	if stored.FinishedAt == nil {
		t.Fatal("完成的执行记录必须记录结束时间")
	}
}

// TestWorkflowProgressWriteFailureStopsAdvance 验证进度写入失败后不推进。
func TestWorkflowProgressWriteFailureStopsAdvance(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:    2,
		updateRunErr: errors.New("数据库写入失败"),
	})
	// 启动需要先写入 running 状态：写入失败时启动必须直接失败，而不是"看似已启动"。
	_, startErr := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	})
	if startErr == nil {
		t.Fatal("执行记录写入失败时启动应当返回错误")
	}
	if fixture.runner.callCount() != 0 {
		t.Fatalf("写入失败时不应执行步骤，实际 %d 次", fixture.runner.callCount())
	}
	run, err := fixture.db.GetActiveWorkflowRun(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if run.Status != domain.WorkflowRunPending {
		t.Fatalf("写入失败时记录应保持 pending，实际 %s", run.Status)
	}
	if run.StartedAt != nil {
		t.Fatal("写入失败时不应记录开始时间")
	}
}

// TestWorkflowCancelStopsAdvance 验证取消后不再启动新步骤。
func TestWorkflowCancelStopsAdvance(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 2,
		replies:   []taskWorkflowReply{{kind: taskReplyBlocked}},
	})
	view, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	// 等待编排器真正进入步骤执行，确保取消信号作用在在途调用上。
	waitTaskWorkflowStatus(t, fixture.service, fixture.task.ID, func(result app.WorkflowStatusResult) bool {
		return result.Run.CurrentStepID != nil
	})
	canceled, err := fixture.service.CancelWorkflow(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("请求取消失败: %v", err)
	}
	if !canceled.CancelRequested {
		t.Fatal("取消请求必须被持久化")
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		func(status domain.WorkflowRunStatus) bool { return status.IsTerminal() })
	if result.Run.Status != domain.WorkflowRunCancelled {
		t.Fatalf("取消后工作流应进入已取消，实际 %s", result.Run.Status)
	}
	if fixture.runner.callCount() != 1 {
		t.Fatalf("取消后不应启动新步骤，实际执行 %d 次", fixture.runner.callCount())
	}
	if result.Run.RunID != view.RunID {
		t.Fatal("取消应作用于同一条执行记录")
	}
}

// TestWorkflowCancelIsIdempotent 验证重复取消请求不会改写时间戳。
func TestWorkflowCancelIsIdempotent(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 1,
		replies:   []taskWorkflowReply{{kind: taskReplyBlocked}},
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	waitTaskWorkflowStatus(t, fixture.service, fixture.task.ID, func(result app.WorkflowStatusResult) bool {
		return result.Run.CurrentStepID != nil
	})
	if _, err := fixture.service.CancelWorkflow(fixture.ctx, fixture.task.ID.String()); err != nil {
		t.Fatalf("请求取消失败: %v", err)
	}
	run, err := fixture.db.GetActiveWorkflowRun(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	firstAt := *run.CancelRequestedAt
	if _, err := fixture.service.CancelWorkflow(fixture.ctx, fixture.task.ID.String()); err != nil {
		t.Fatalf("重复请求取消应当幂等: %v", err)
	}
	repeated, err := fixture.db.GetWorkflowRun(fixture.ctx, run.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if !repeated.CancelRequestedAt.Equal(firstAt) {
		t.Fatal("重复取消不应改写取消请求时间")
	}
}

// TestWorkflowResumeSkipsCompletedSteps 验证恢复不会重新执行已完成步骤。
func TestWorkflowResumeSkipsCompletedSteps(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:      2,
		codeSaver:      true,
		sessions:       true,
		codeSaveRecord: succeededCodeSaveRecord(),
		replies: []taskWorkflowReply{
			{kind: taskReplyVerified, completes: true},
			{kind: taskReplyVerificationFailed},
			{kind: taskReplyVerified, completes: true},
		},
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	blocked := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		workflowSettled)
	if blocked.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("第二步验证失败后应阻塞，实际 %s", blocked.Run.Status)
	}
	// 恢复：第一步已完成，不应重新执行；第二步重新执行并通过。
	resumed, err := fixture.service.ResumeWorkflow(fixture.ctx, app.ResumeWorkflowRequest{
		TaskID: fixture.task.ID.String(),
		RunID:  blocked.Run.RunID.String(),
	})
	if err != nil {
		t.Fatalf("恢复工作流失败: %v", err)
	}
	if resumed.RunID != blocked.Run.RunID {
		t.Fatal("恢复应作用于同一条执行记录")
	}
	final := waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		func(status domain.WorkflowRunStatus) bool { return status.IsTerminal() })
	if final.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("恢复后应完成，实际 %s（原因 %s）", final.Run.Status, final.Run.ErrorSummary)
	}
	requests := fixture.runner.receivedRequests()
	if len(requests) != 3 {
		t.Fatalf("恢复只应重新执行未完成步骤，实际调用 %d 次", len(requests))
	}
	if requests[0].StepID != fixture.steps[0].ID.String() {
		t.Fatalf("首次执行顺序错误: %s", requests[0].StepID)
	}
	if requests[1].StepID != fixture.steps[1].ID.String() {
		t.Fatalf("首次执行顺序错误: %s", requests[1].StepID)
	}
	if requests[2].StepID != fixture.steps[1].ID.String() {
		t.Fatalf("恢复应从第二步继续，实际 %s", requests[2].StepID)
	}
}

// TestWorkflowResumeRejectsRecoveryRequiredStep 验证结果未知时拒绝盲目恢复。
func TestWorkflowResumeRejectsRecoveryRequiredStep(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{stepCount: 2})
	step, err := fixture.db.GetStep(fixture.ctx, fixture.steps[0].ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusRecoveryRequired, time.Now().UTC()); err != nil {
		t.Fatalf("标记步骤待核验失败: %v", err)
	}
	if err := fixture.db.UpdateStep(fixture.ctx, *step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	_, startErr := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	})
	if startErr == nil {
		t.Fatal("存在待核验步骤时不应直接启动工作流")
	}
	if fixture.runner.callCount() != 0 {
		t.Fatalf("拒绝启动时不应执行步骤，实际 %d", fixture.runner.callCount())
	}
}

// TestWorkflowResumeRejectsPlanChange 验证计划变化后不能沿用旧游标恢复。
func TestWorkflowResumeRejectsPlanChange(t *testing.T) {
	t.Parallel()
	var runID domain.ID
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 2,
		existingRun: func(t *testing.T, task domain.Task, steps []domain.Step) domain.WorkflowRun {
			run, err := domain.NewWorkflowRun(task.ID, steps, time.Now().UTC())
			if err != nil {
				t.Fatalf("构造执行记录失败: %v", err)
			}
			runID = run.ID
			return run
		},
	})
	// 改写计划内容：步骤标题变化会让摘要不再匹配。
	step, err := fixture.db.GetStep(fixture.ctx, fixture.steps[1].ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	step.Title = "计划已重新生成"
	if err := fixture.db.UpdateStep(fixture.ctx, *step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	_, resumeErr := fixture.service.ResumeWorkflow(fixture.ctx, app.ResumeWorkflowRequest{
		TaskID: fixture.task.ID.String(),
		RunID:  runID.String(),
	})
	if resumeErr == nil {
		t.Fatal("计划变化后不应允许恢复")
	}
	if !errors.Is(resumeErr, app.ErrWorkflowConflict) {
		t.Fatalf("错误类别应为状态冲突: %v", resumeErr)
	}
	if fixture.runner.callCount() != 0 {
		t.Fatalf("拒绝恢复时不应执行步骤，实际 %d", fixture.runner.callCount())
	}
}

// TestWorkflowRecoveryOnServerRestart 验证服务重启后能识别未结束的执行。
func TestWorkflowRecoveryOnServerRestart(t *testing.T) {
	t.Parallel()
	var existing domain.WorkflowRun
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 2,
		existingRun: func(t *testing.T, task domain.Task, steps []domain.Step) domain.WorkflowRun {
			run, err := domain.NewWorkflowRun(task.ID, steps, time.Now().UTC())
			if err != nil {
				t.Fatalf("构造执行记录失败: %v", err)
			}
			// 模拟上一次进程退出时留下的 running 残留：执行权已过期。
			if err := run.Start("dead-worker", time.Millisecond, time.Now().UTC().Add(-time.Hour)); err != nil {
				t.Fatalf("模拟残留执行失败: %v", err)
			}
			existing = run
			return run
		},
	})
	// 新进程启动时把过期残留收敛为 blocked，而不是盲目重放。
	recovered, err := fixture.db.GetWorkflowRun(fixture.ctx, existing.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if recovered.Status != domain.WorkflowRunBlocked {
		t.Fatalf("残留执行应收敛为 blocked，实际 %s", recovered.Status)
	}
	if recovered.ErrorClass != "worker_interrupted" {
		t.Fatalf("错误分类不正确: %s", recovered.ErrorClass)
	}
	if fixture.runner.callCount() != 0 {
		t.Fatalf("启动恢复不应重放 Pi 调用，实际 %d", fixture.runner.callCount())
	}
}

// TestWorkflowRecoveryKeepsLiveLease 验证有效租约不会被其他进程抢占。
func TestWorkflowRecoveryKeepsLiveLease(t *testing.T) {
	t.Parallel()
	var existing domain.WorkflowRun
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 2,
		existingRun: func(t *testing.T, task domain.Task, steps []domain.Step) domain.WorkflowRun {
			run, err := domain.NewWorkflowRun(task.ID, steps, time.Now().UTC())
			if err != nil {
				t.Fatalf("构造执行记录失败: %v", err)
			}
			if err := run.Start("live-worker", time.Hour, time.Now().UTC()); err != nil {
				t.Fatalf("模拟在途执行失败: %v", err)
			}
			existing = run
			return run
		},
	})
	recovered, err := fixture.db.GetWorkflowRun(fixture.ctx, existing.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if recovered.Status != domain.WorkflowRunRunning {
		t.Fatalf("有效租约不应被抢占，实际 %s", recovered.Status)
	}
}

// TestWorkflowRecoveryCancelsRequested 验证服务启动时确认已记录的取消请求。
func TestWorkflowRecoveryCancelsRequested(t *testing.T) {
	t.Parallel()
	var existing domain.WorkflowRun
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount: 2,
		existingRun: func(t *testing.T, task domain.Task, steps []domain.Step) domain.WorkflowRun {
			run, err := domain.NewWorkflowRun(task.ID, steps, time.Now().UTC())
			if err != nil {
				t.Fatalf("构造执行记录失败: %v", err)
			}
			if err := run.Start("dead-worker", time.Millisecond, time.Now().UTC().Add(-time.Hour)); err != nil {
				t.Fatalf("模拟残留执行失败: %v", err)
			}
			if err := run.RequestCancel(time.Now().UTC()); err != nil {
				t.Fatalf("记录取消请求失败: %v", err)
			}
			if err := run.Transition(domain.WorkflowRunCancelRequested, time.Now().UTC()); err != nil {
				t.Fatalf("推进取消状态失败: %v", err)
			}
			existing = run
			return run
		},
	})
	recovered, err := fixture.db.GetWorkflowRun(fixture.ctx, existing.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if recovered.Status != domain.WorkflowRunCancelled {
		t.Fatalf("已记录取消请求的残留应收敛为已取消，实际 %s", recovered.Status)
	}
}

// TestWorkflowRecordsEvents 验证关键阶段都会写入任务事件。
func TestWorkflowRecordsEvents(t *testing.T) {
	t.Parallel()
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:      1,
		codeSaver:      true,
		sessions:       true,
		codeSaveRecord: succeededCodeSaveRecord(),
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	waitTaskRunStatus(t, fixture.service, fixture.task.ID,
		func(status domain.WorkflowRunStatus) bool { return status.IsTerminal() })
	// 终态先落盘、事件后追加：等待收尾事件出现，否则断言会与事件写入竞态。
	seen := waitTaskWorkflowEvents(t, fixture, []domain.EventType{
		domain.EventTypeWorkflowStarted,
		domain.EventTypeStepStarted,
		domain.EventTypeWorkflowStepPassed,
		domain.EventTypeWorkflowCodeSaved,
		domain.EventTypeWorkflowCompleted,
	})
	for _, expected := range []domain.EventType{
		domain.EventTypeWorkflowStarted,
		domain.EventTypeStepStarted,
		domain.EventTypeWorkflowStepPassed,
		domain.EventTypeWorkflowCodeSaved,
		domain.EventTypeWorkflowCompleted,
	} {
		if !seen[expected] {
			t.Fatalf("缺少事件 %s，实际事件: %v", expected, seen)
		}
	}
}

// TestWorkflowCloseoutRequiresSessionArchive 验证收尾对会话归档的判定。
//
// 只有原始会话完整且归档成功、归档位置非空时才算归档完成：HTML 导出失败不影响
// 判定，完整性不足、归档失败、未归档与归档位置缺失都必须收敛为需要人工核验。
func TestWorkflowCloseoutRequiresSessionArchive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		adjust      func(session domain.Session) domain.Session
		expected    domain.WorkflowRunStatus
		errorClass  string
		description string
	}{
		{
			name: "HTML 导出失败但原始归档成功",
			adjust: func(session domain.Session) domain.Session {
				session.Completeness = domain.SessionCompletenessExportFailed
				session.ExportStatus = domain.SessionExportFailed
				session.HTMLPath = ""
				return session
			},
			expected:    domain.WorkflowRunCompleted,
			description: "原始 JSONL 已保存并归档，HTML 导出失败不应阻止收尾",
		},
		{
			name: "归档失败",
			adjust: func(session domain.Session) domain.Session {
				session.ArchiveStatus = domain.SessionArchiveFailed
				session.ArchivePath = ""
				return session
			},
			expected:    domain.WorkflowRunRecoveryRequired,
			errorClass:  "session_not_persisted",
			description: "归档失败时原始会话可能只在容器内，必须人工核验",
		},
		{
			name: "未尝试归档",
			adjust: func(session domain.Session) domain.Session {
				session.ArchiveStatus = domain.SessionArchiveNotAttempted
				session.ArchivePath = ""
				return session
			},
			expected:    domain.WorkflowRunRecoveryRequired,
			errorClass:  "session_not_persisted",
			description: "未归档不能当作归档完成",
		},
		{
			name: "归档位置缺失",
			adjust: func(session domain.Session) domain.Session {
				session.ArchivePath = ""
				return session
			},
			expected:    domain.WorkflowRunRecoveryRequired,
			errorClass:  "session_not_persisted",
			description: "声称归档成功却没有归档位置时无法定位原始会话",
		},
		{
			name: "完整性为部分",
			adjust: func(session domain.Session) domain.Session {
				session.Completeness = domain.SessionCompletenessPartial
				return session
			},
			expected:    domain.WorkflowRunRecoveryRequired,
			errorClass:  "session_not_persisted",
			description: "只保存了部分会话内容时不算归档完成",
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
			expected:    domain.WorkflowRunRecoveryRequired,
			errorClass:  "session_not_persisted",
			description: "没有可用原始会话文件时不得判定归档完成",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
				stepCount:      1,
				codeSaver:      true,
				codeSaveRecord: succeededCodeSaveRecord(),
			})
			// 会话只交给收尾判定，不经过数据库写入：archive_path 的"非空"约束
			// 无法表达"归档成功但位置为空"这类需要被识别出的历史数据。
			session := baseSession(t, fixture.db, fixture.ctx, fixture.task, fixture.now, testCase.adjust)
			fixture.store.sessions = []domain.Session{session}
			if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
				TaskID: fixture.task.ID.String(),
			}); err != nil {
				t.Fatalf("启动工作流失败: %v", err)
			}
			result := waitTaskRunStatus(t, fixture.service, fixture.task.ID, workflowSettled)
			if result.Run.Status != testCase.expected {
				t.Fatalf(
					"%s: 期望状态 %s，实际 %s（原因 %s）",
					testCase.description,
					testCase.expected,
					result.Run.Status,
					result.Run.ErrorSummary,
				)
			}
			if result.Run.ErrorClass != testCase.errorClass {
				t.Fatalf(
					"%s: 期望错误分类 %q，实际 %q",
					testCase.description,
					testCase.errorClass,
					result.Run.ErrorClass,
				)
			}
			task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
			if err != nil {
				t.Fatalf("读取任务失败: %v", err)
			}
			if testCase.expected == domain.WorkflowRunCompleted {
				if task.Status != domain.TaskStatusCompleted {
					t.Fatalf("%s: 归档完成时任务应完成，实际 %s", testCase.description, task.Status)
				}
				return
			}
			if task.Status == domain.TaskStatusCompleted {
				t.Fatalf("%s: 会话归档未完成时任务不得标记为完成", testCase.description)
			}
		})
	}
}

// TestWorkflowCloseoutRequiresRecoveryOnUnknownCommit 验证提交结果未知时需要人工核验。
//
// 推送尚未成功且提交结果无法确认时，不能判定任务完整成功，也不能退化为普通阻塞：
// 必须先核验仓库真实状态。
func TestWorkflowCloseoutRequiresRecoveryOnUnknownCommit(t *testing.T) {
	t.Parallel()
	record := succeededCodeSaveRecord()
	record.CommitStatus = domain.CommitStatusUnknown
	record.CommitSHA = ""
	// 真实场景中提交结果未知时不会继续推送，推送停留在 pending。
	record.PushStatus = domain.PushStatusPending
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:      1,
		codeSaver:      true,
		sessions:       true,
		codeSaveRecord: record,
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID, workflowSettled)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("提交结果未知时应需要人工核验，实际 %s", result.Run.Status)
	}
	if result.Run.ErrorClass != "code_save_indeterminate" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("提交结果未知时任务不得标记为完成")
	}
}

// TestWorkflowCloseoutRequiresRecoveryOnUnconfirmedRecord 验证记录写入未确认时的收尾。
//
// 内存中的记录看起来"推送成功"，但数据库没能写进去：此时不能依据内存结论判定任务
// 完成，必须先把"记录是否可信"这件事交给人核验。它与 code_save_indeterminate 是
// 不同的事实，因此使用独立的错误分类。
func TestWorkflowCloseoutRequiresRecoveryOnUnconfirmedRecord(t *testing.T) {
	t.Parallel()
	record := succeededCodeSaveRecord()
	record.PersistStatus = domain.PersistStatusUnconfirmed
	record.PersistError = "database is locked"
	fixture := newTaskWorkflowFixture(t, taskWorkflowOptions{
		stepCount:      1,
		codeSaver:      true,
		sessions:       true,
		codeSaveRecord: record,
	})
	if _, err := fixture.service.StartWorkflow(fixture.ctx, app.StartWorkflowRequest{
		TaskID: fixture.task.ID.String(),
	}); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	result := waitTaskRunStatus(t, fixture.service, fixture.task.ID, workflowSettled)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("记录写入未确认时应需要人工核验，实际 %s", result.Run.Status)
	}
	if result.Run.ErrorClass != "code_save_unconfirmed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("记录写入未确认时任务不得标记为完成")
	}
}

// waitTaskWorkflowEvents 轮询事件表直到出现全部期望的事件类型或超时。
//
// 事件在状态落盘之后追加，因此"执行记录已进入终态"并不等于"全部事件已写入"。
// 用轮询代替固定 Sleep 使断言既确定又不引入等待时长假设。
func waitTaskWorkflowEvents(
	t *testing.T,
	fixture *taskWorkflowFixture,
	expected []domain.EventType,
) map[domain.EventType]bool {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		events, err := fixture.db.ListEventsByTask(fixture.ctx, fixture.task.ID, storage.EventFilter{})
		if err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		seen := make(map[domain.EventType]bool, len(events))
		for _, event := range events {
			seen[event.Type] = true
		}
		missing := false
		for _, eventType := range expected {
			if !seen[eventType] {
				missing = true
				break
			}
		}
		if !missing {
			return seen
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待事件超时，期望 %v，实际 %v", expected, seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
