// Package workflow_test 验证单任务端到端链路在真实持久化现场上的完整成立。
//
// 本文件提供 P1-M5 端到端验收的装配：真实 SQLite 与正式迁移、真实领域状态机、
// 真实 app.TaskLaunchService / app.TaskWorkflowService / app.StepExecutor /
// app.ValidationService / app.ValidatorReviewer / app.StepWorkflowService /
// app.TaskWorkspaceService / app.SessionService，以及真实的代码保存组件
// （internal/codesaver）与真实 Git 仓库。
//
// 只把远程 SSH、Docker 容器与容器内 Pi 进程替换为受控替身：容器内命令与 Git
// 副作用由 e2e_ports_test.go 映射到测试工作空间中的真实仓库，因此断言的对象是
// 数据库里真实留下的 Task / WorkflowRun / Attempt / Session / ValidationRun /
// CodeSave 记录与事件，而不是方法的返回值。
package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// e2ePlanJSON 是 Planner 替身返回的合法计划输出。
//
// 计划以两个开发步骤表达，使端到端用例可以验证"逐步推进"与"只在前一步骤验证
// 通过后才执行下一步骤"。
const e2ePlanJSON = `{"summary":"分两步完成需求","steps":[` +
	`{"id":"step-1","title":"实现接口","objective":"按需求实现接口",` +
	`"instructions":"只修改当前工作区","acceptance_criteria":["接口可以编译"]},` +
	`{"id":"step-2","title":"补齐测试","objective":"为接口补齐测试",` +
	`"instructions":"不要修改构建脚本","acceptance_criteria":["测试可以运行"]}]}`

// e2eValidatorPassJSON 是 Validator 声称通过的评估输出。
const e2eValidatorPassJSON = `{"passed":true,"summary":"实现符合验收条件",` +
	`"evidence":["internal/feature.go"],"suggest_fix":false,"needs_human_review":false}`

// e2eFixture 是一条任务端到端验收的完整现场。
type e2eFixture struct {
	ctx       context.Context
	db        *sqlite.DB
	logger    *logging.Logger
	workspace *testkit.Workspace
	repo      *e2eRepo
	ports     *e2ePorts
	sessions  *app.SessionService
	launch    *app.TaskLaunchService
	workflow  *app.TaskWorkflowService
	codeSaver *e2eCodeSaver
	task      domain.Task
	now       time.Time
	// options 是本次现场的构造参数，重启后按同一份参数重新装配。
	options e2eOptions
	// dbPath 是数据库文件路径，重启时用同一路径重新打开。
	dbPath string
}

// e2eOptions 允许用例调整端到端现场的构造方式。
type e2eOptions struct {
	// retry 是步骤生效的重试策略，零值使用上限 1（不自动修复）。
	retry domain.RetryConfig
	// maxAttempts 覆盖步骤的尝试次数上限；为零时使用 retry.MaxAttempts。
	maxAttempts int
	// agentReplies 是 Agent 调用结果序列，按调用顺序返回。
	agentReplies []e2eAgentReply
	// checkSequence 是确定性检查结果序列，按调用顺序返回。
	checkSequence []e2eCheckReply
	// mergeAfterPush 覆盖任务配置的合并开关。
	mergeAfterPush bool
	// codeSaver 为 false 时不装配代码保存能力。
	codeSaver bool
	// codeSaverWrapper 允许用例替换代码保存端口的返回结果。
	codeSaverWrapper func(*e2eCodeSaver)
	// storeWrapper 允许用例替换编排器的存储入口，用于构造写入失败现场。
	storeWrapper func(app.TaskWorkflowStore) app.TaskWorkflowStore
	// containerRunning 覆盖容器运行状态，默认 true。
	containerRunning bool
	// gitInspectErr 让 Git 状态采集失败。
	gitInspectErr error
}

// newE2EFixture 在工作空间中准备一条可执行完整端到端链路的任务现场。
func newE2EFixture(t *testing.T, options e2eOptions) *e2eFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
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
	fixture := &e2eFixture{
		ctx:       ctx,
		db:        db,
		logger:    logger,
		workspace: workspace,
		now:       time.Now().UTC(),
		options:   options,
		dbPath:    workspace.DatabasePath(),
	}
	fixture.seed(t, options)
	fixture.repo = newE2ERepo(t, fixture.task.ID)
	fixture.ports = &e2ePorts{
		repo:          fixture.repo,
		running:       !options.containerRunning || options.containerRunning,
		agentReplies:  options.agentReplies,
		checkSequence: options.checkSequence,
	}
	if options.gitInspectErr != nil {
		fixture.ports.setGitInspectErr(options.gitInspectErr)
	}
	fixture.assemble(t, options)
	return fixture
}

// seed 写入角色、执行节点、需求与任务。
//
// 任务初始状态保持 queued：端到端用例必须从"任务创建"开始，由统一启动入口自己
// 推进到容器准备、工作区初始化、计划生成与工作流启动，而不是由用例预先写入。
func (f *e2eFixture) seed(t *testing.T, options e2eOptions) {
	t.Helper()
	planner := e2eRole(t, f.db, f.ctx, domain.RoleTypePlanner, "架构规划师", f.now)
	developer := e2eRole(t, f.db, f.ctx, domain.RoleTypeDeveloper, "开发工程师", f.now)
	validator := e2eRole(t, f.db, f.ctx, domain.RoleTypeValidator, "验证工程师", f.now)
	host := e2eHost(t, f.db, f.ctx, f.now)
	host = e2eMarkHostNormal(t, f.db, f.ctx, host)
	requirement, err := domain.NewRequirement("端到端需求", "需求正文", domain.PriorityNormal, f.now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := f.db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, e2eRepositoryURL, e2eBaseBranch, "", f.now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	retry := options.retry
	if retry.IsZero() {
		retry = domain.RetryConfig{MaxAttempts: 1}
	}
	if options.maxAttempts > 0 {
		retry.MaxAttempts = options.maxAttempts
	}
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
		ValidatorRoleID: validator.ID,
		Retry:           retry,
		MergeAfterPush:  options.mergeAfterPush,
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
	f.task = task
}

// assemble 装配真实服务链路，只把外部执行与外部 Git 远程替换为受控替身。
func (f *e2eFixture) assemble(t *testing.T, options e2eOptions) {
	t.Helper()
	sessions, err := app.NewSessionService(f.db, f.workspace.SessionRoot(), f.logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	f.sessions = sessions
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      f.db,
		Containers: f.ports,
		Sessions:   sessions,
		Git:        f.ports,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	reviewer, err := app.NewValidatorReviewer(app.ValidatorReviewerOptions{
		Store:      f.db,
		Containers: f.ports,
		Sessions:   sessions,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造语义审查器失败: %v", err)
	}
	validation, err := app.NewValidationService(app.ValidationOptions{
		Store:      f.db,
		Containers: f.ports,
		OutputRoot: f.workspace.Dir("validations"),
		Reviewer:   reviewer,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
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
	workspace, err := app.NewTaskWorkspaceService(app.TaskWorkspaceOptions{
		Store:      f.db,
		Containers: f.ports,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造任务级工作区初始化服务失败: %v", err)
	}
	var store app.TaskWorkflowStore = f.db
	if options.storeWrapper != nil {
		store = options.storeWrapper(store)
	}
	f.codeSaver = &e2eCodeSaver{ports: f.ports, sessions: sessions, store: f.db}
	if options.codeSaverWrapper != nil {
		options.codeSaverWrapper(f.codeSaver)
	}
	workflowOptions := app.TaskWorkflowOptions{
		Store:      store,
		Steps:      stepFlow,
		Containers: f.ports,
		Sessions:   sessions,
		Logger:     f.logger.Slog(),
	}
	if options.codeSaver {
		workflowOptions.CodeSaver = f.codeSaver
	}
	workflow, err := app.NewTaskWorkflowService(workflowOptions)
	if err != nil {
		t.Fatalf("构造任务级编排器失败: %v", err)
	}
	f.workflow = workflow
	launch, err := app.NewTaskLaunchService(app.TaskLaunchOptions{
		Store:        f.db,
		Environments: f.executionEnvironment(t),
		Containers:   f.ports,
		Planner:      workflow,
		Workspace:    workspace,
		Workflow:     workflow,
		Logger:       f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造统一启动服务失败: %v", err)
	}
	f.launch = launch
	startE2EServices(t, launch, executor, validation, stepFlow, workflow)
}

// e2eCodeSaver 是代码保存端口的实现。
//
// 它不伪造保存结果：真实的 codesaver.Saver 在 e2ePorts 提供的"容器内命令"上执行
// 真实 Git 操作（提交、推送、远程引用核验），记录由真实存储写入 code_saves。
// 因此端到端用例断言的是真实产生的提交与真实推送后的远程引用。
type e2eCodeSaver struct {
	ports    *e2ePorts
	sessions *app.SessionService
	store    storage.Store
	// saveErr 使本次保存直接返回错误（用于构造"保存明确失败"的现场）。
	saveErr error
	// failPersistWhen 让满足条件的保存记录写入失败。
	//
	// 它用于构造"Git 已经成功、但记录没落盘"的现场：只有后续阶段的写入会失败，
	// 记录创建与远程操作本身仍然真实发生。
	failPersistWhen func(domain.CodeSaveRecord) bool
	// pushUncertain 让容器内推送命令返回"结果无法确认"。
	pushUncertain bool
}

// SaveCode 执行一次真实 Git 代码保存。
func (s *e2eCodeSaver) SaveCode(
	ctx context.Context,
	request app.CodeSaveRequest,
) (app.CodeSaveResult, error) {
	if s.saveErr != nil {
		return app.CodeSaveResult{}, s.saveErr
	}
	s.ports.setPushUncertain(s.pushUncertain)
	saver, err := codesaver.New(codesaver.Options{
		Executor: e2eContainerExecutor{ports: s.ports},
		Guard:    e2eGuard{ports: s.ports},
		Recorder: e2eRecorder{
			store:    s.store,
			failWhen: s.failPersistWhen,
		},
	})
	if err != nil {
		return app.CodeSaveResult{}, err
	}
	container, err := e2eGuard{ports: s.ports}.EnsureExecutable(ctx, request.ContainerID)
	if err != nil {
		return app.CodeSaveResult{}, err
	}
	result, saveErr := saver.Run(ctx, codesaver.Request{
		MachineID:      container.HostID.String(),
		ContainerID:    container.ID.String(),
		DockerID:       container.DockerID,
		ProjectID:      request.ProjectID,
		TaskID:         request.TaskID,
		AttemptID:      request.AttemptID,
		WorkDir:        request.WorkDir,
		BaseBranch:     request.BaseBranch,
		MergeAfterPush: request.MergeAfterPush,
		RemoteName:     e2eRemoteName,
	})
	return app.CodeSaveResult{Result: result, Container: container}, saveErr
}

// e2eGuard 核验容器受管理且正在运行。
type e2eGuard struct {
	ports *e2ePorts
}

// EnsureExecutable 返回受控的容器记录。
func (g e2eGuard) EnsureExecutable(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	detail, err := g.ports.InspectContainer(ctx, app.InspectContainerRequest{ContainerID: containerID})
	if err != nil {
		return domain.Container{}, err
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		return domain.Container{}, errors.New("容器当前未运行，无法执行命令")
	}
	return detail.Container, nil
}

// e2eRecorder 把代码保存记录写入真实存储。
type e2eRecorder struct {
	store storage.Store
	// failWhen 非空时让满足条件的写入失败，用于构造"记录未确认"的现场。
	failWhen func(domain.CodeSaveRecord) bool
}

// Create 写入新记录。
func (r e2eRecorder) Create(ctx context.Context, record domain.CodeSaveRecord) error {
	if r.failing(record) {
		return errors.New("数据库写入失败")
	}
	return r.store.CreateCodeSaveRecord(ctx, record)
}

// Update 更新记录。
func (r e2eRecorder) Update(ctx context.Context, record domain.CodeSaveRecord) error {
	if r.failing(record) {
		return errors.New("数据库写入失败")
	}
	return r.store.UpdateCodeSaveRecord(ctx, record)
}

// failing 判断本次写入是否应当失败。
func (r e2eRecorder) failing(record domain.CodeSaveRecord) bool {
	if r.failWhen == nil {
		return false
	}
	return r.failWhen(record)
}

// Get 读取记录。
func (r e2eRecorder) Get(ctx context.Context, id domain.ID) (*domain.CodeSaveRecord, error) {
	return r.store.GetCodeSaveRecord(ctx, id)
}

// ListByTask 按任务列出记录。
func (r e2eRecorder) ListByTask(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error) {
	return r.store.ListCodeSaveRecordsByTask(ctx, taskID)
}

// startE2EServices 启动后台服务并在用例结束时逆序停止。
func startE2EServices(t *testing.T, services ...interface {
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
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer stopCancel()
		for index := len(services) - 1; index >= 0; index-- {
			if err := services[index].Stop(stopCtx); err != nil {
				t.Errorf("停止后台服务失败: %v", err)
			}
		}
	})
}

// e2eRole 写入一个启用中的角色。
func e2eRole(
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

// e2eHost 写入一个执行节点（校验状态为 checking）。
func e2eHost(
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

// e2eMarkHostNormal 把执行节点置为已通过环境预检。
func e2eMarkHostNormal(
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

// restart 关闭数据库并用同一路径重新打开，然后重新装配全部服务。
//
// 它模拟"进程退出后重启"：只有数据库里的记录会保留，进程内的对象（编排器、执行器、
// 替身端口）全部重建。因此重启后得到的结论只可能来自持久化事实。
func (f *e2eFixture) restart(t *testing.T) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	db, err := sqlite.Open(f.ctx, sqlite.Options{Path: f.dbPath})
	if err != nil {
		t.Fatalf("重新打开测试数据库失败: %s: %v", f.dbPath, err)
	}
	f.db = db
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	// 替身端口按原样重建：Agent 回复与检查序列保持同一份配置，但调用计数从零开始。
	f.ports = &e2ePorts{
		repo:          f.repo,
		running:       true,
		agentReplies:  f.options.agentReplies,
		checkSequence: f.options.checkSequence,
	}
	f.assemble(t, f.options)
}

// e2eSettled 判断工作流是否已经停止自动推进。
func e2eSettled(status domain.WorkflowRunStatus) bool {
	if status.IsTerminal() {
		return true
	}
	return status == domain.WorkflowRunBlocked || status == domain.WorkflowRunRecoveryRequired
}

// formatE2ERun 返回执行记录的紧凑描述，用于失败信息。
func formatE2ERun(run app.WorkflowRunView) string {
	return fmt.Sprintf(
		"status=%s stage=%s class=%s summary=%s",
		run.Status,
		run.Stage,
		run.ErrorClass,
		run.ErrorSummary,
	)
}

// executionEnvironment 装配真实的执行环境服务，只替换镜像与 Docker 容器两个外部端口。
//
// 环境准备的四阶段流程、容器登记写回与可用性核验全部走生产代码：端到端验收必须
// 证明"任务容器确实被准备并关联到任务"，而不是由用例直接写入容器标识。
func (f *e2eFixture) executionEnvironment(t *testing.T) *app.ExecutionEnvironmentService {
	t.Helper()
	service, err := app.NewExecutionEnvironmentService(app.ExecutionEnvironmentOptions{
		// 镜像与容器能力都由受控替身提供，因此这里只填入占位实现以满足构造契约。
		Environments:  &app.EnvironmentService{},
		Containers:    &app.ContainerService{},
		Images:        &e2eImages{},
		ContainerPort: &e2eEnvContainers{ports: f.ports, db: f.db},
		Tasks:         f.db,
		Logger:        f.logger,
	})
	if err != nil {
		t.Fatalf("构造执行环境服务失败: %v", err)
	}
	return service
}

// launch 从统一启动入口推进任务：容器准备 → 工作区初始化 → 计划生成 → 工作流启动。
func (f *e2eFixture) launchTask(t *testing.T) app.TaskLaunchResult {
	t.Helper()
	result, err := f.launch.Launch(f.ctx, app.TaskLaunchRequest{TaskID: f.task.ID.String()})
	if err != nil {
		t.Fatalf("统一启动失败: %v（阶段 %s，状态 %s）", err, result.FailureStage, result.TaskStatus)
	}
	return result
}

// settle 等待工作流停止自动推进并返回最后的执行记录。
func (f *e2eFixture) settle(t *testing.T) app.WorkflowStatusResult {
	t.Helper()
	const timeout = 120 * time.Second
	deadline := time.Now().Add(timeout)
	var last app.WorkflowStatusResult
	for time.Now().Before(deadline) {
		result, err := f.workflow.GetWorkflowStatus(f.ctx, f.task.ID.String())
		if err != nil {
			t.Fatalf("查询工作流状态失败: %v", err)
		}
		last = result
		if result.HasRun && e2eSettled(result.Run.Status) {
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
	t.Fatalf("等待工作流收敛超时，最后一次状态: %s", formatE2ERun(last.Run))
	return last
}

// reloadTask 读取数据库中的任务。
func (f *e2eFixture) reloadTask(t *testing.T) domain.Task {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *task
}

// reloadRun 读取数据库中的工作流执行记录。
func (f *e2eFixture) reloadRun(t *testing.T, id domain.ID) domain.WorkflowRun {
	t.Helper()
	run, err := f.db.GetWorkflowRun(f.ctx, id)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	return *run
}

// reloadSteps 读取数据库中的计划步骤，按序号升序。
func (f *e2eFixture) reloadSteps(t *testing.T) []domain.Step {
	t.Helper()
	steps, err := f.db.ListStepsByTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return steps
}

// attempts 读取某步骤的全部尝试。
func (f *e2eFixture) attempts(t *testing.T, stepID domain.ID) []domain.Attempt {
	t.Helper()
	attempts, err := f.db.ListAttemptsByStep(f.ctx, stepID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	return attempts
}

// sessionsOf 读取任务的全部会话。
func (f *e2eFixture) sessionsOf(t *testing.T) []domain.Session {
	t.Helper()
	sessions, err := f.db.ListSessionsByTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	return sessions
}

// validationRuns 读取某次尝试的全部验证记录。
func (f *e2eFixture) validationRuns(t *testing.T, attemptID domain.ID) []domain.ValidationRun {
	t.Helper()
	runs, err := f.db.ListValidationRunsByAttempt(f.ctx, attemptID)
	if err != nil {
		t.Fatalf("读取验证记录失败: %v", err)
	}
	return runs
}

// codeSaves 读取任务的全部代码保存记录。
func (f *e2eFixture) codeSaves(t *testing.T) []domain.CodeSaveRecord {
	t.Helper()
	records, err := f.db.ListCodeSaveRecordsByTask(f.ctx, f.task.ID.String())
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	return records
}

// events 读取任务的全部事件。
func (f *e2eFixture) events(t *testing.T) []domain.Event {
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
func (f *e2eFixture) waitEvents(t *testing.T, expected ...domain.EventType) {
	t.Helper()
	const timeout = 30 * time.Second
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

// workspaceInits 读取任务的工作区初始化记录。
func (f *e2eFixture) workspaceInits(t *testing.T) []domain.TaskWorkspaceInitRecord {
	t.Helper()
	records, err := f.db.ListTaskWorkspaceInitsByTask(f.ctx, f.task.ID.String())
	if err != nil {
		t.Fatalf("读取工作区初始化记录失败: %v", err)
	}
	return records
}
