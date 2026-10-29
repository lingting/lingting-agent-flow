package workflow_remote

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// e2eFixture 是一条真实远程端到端链路的完整现场。
//
// 与本地受控替身用例的区别：这里的环境准备、容器创建、容器内 Git Clone、容器内
// Pi 调用、代码保存与任务状态写入全部走生产代码，只把"配置来源"换成测试配置。
type e2eFixture struct {
	*e2eEnv
	t      *testing.T
	db     *sqlite.DB
	host   domain.ExecutionHost
	task   domain.Task
	record domain.Container

	environments  *app.EnvironmentService
	containers    *app.ContainerService
	execution     *app.ExecutionEnvironmentService
	initWorkspace *app.TaskWorkspaceService
	sessions      *app.SessionService
	executor      *app.StepExecutor
	validation    *app.ValidationService
	stepFlow      *app.StepWorkflowService
	workflow      *app.TaskWorkflowService
	launch        *app.TaskLaunchService
}

// newE2EFixture 装配真实远程端到端现场。
func newE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	env := newE2EEnv(t)
	db, err := sqlite.Open(env.ctx, sqlite.Options{Path: env.workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	fixture := &e2eFixture{e2eEnv: env, t: t, db: db}
	fixture.createHost(t)
	// 清理责任必须在统一启动之前登记：容器一旦创建就受测试追踪，业务链路中途
	// 失败也不会失去回收能力；登记早于后台服务启动，因此它的清理晚于服务停止执行。
	fixture.trackTaskContainers(t)
	fixture.assemble(t)
	fixture.seedTask(t)
	return fixture
}

// trackTaskContainers 在统一启动之前登记本任务容器的清理责任。
//
// 生产容器服务会先写入容器登记记录、再创建 Docker 容器，因此清理时可以从数据库
// 解析出实际容器身份：即使容器创建在返回之前失败，也能找到并回收远程容器。
func (f *e2eFixture) trackTaskContainers(t *testing.T) {
	t.Helper()
	containerRun.Track(t, f.runner, testkit.TrackedContainer{
		Resolve: f.resolveTaskContainers,
	})
}

// trackCreatedContainer 在容器创建成功后立即登记精确身份与所有权证据。
func (f *e2eFixture) trackCreatedContainer(t *testing.T, record domain.Container) {
	t.Helper()
	containerRun.Track(t, f.runner, testkit.TrackedContainer{
		Name:     record.Name,
		DockerID: record.DockerID,
		Record:   &record,
	})
}

// resolveTaskContainers 从数据库解析本任务创建的全部容器记录。
func (f *e2eFixture) resolveTaskContainers(
	ctx context.Context,
) ([]testkit.TrackedContainer, error) {
	taskID := f.task.ID
	records, err := f.db.ListContainers(ctx, domain.ContainerFilter{TaskID: &taskID})
	if err != nil {
		return nil, fmt.Errorf("读取任务容器记录失败: %w", err)
	}
	targets := make([]testkit.TrackedContainer, 0, len(records))
	for index := range records {
		record := records[index]
		targets = append(targets, testkit.TrackedContainer{
			Name:     record.Name,
			DockerID: record.DockerID,
			Record:   &record,
		})
	}
	return targets, nil
}

// createHost 写入一个已通过环境预检的执行节点。
func (f *e2eFixture) createHost(t *testing.T) {
	t.Helper()
	sshConfig, _ := testkit.LoadSSHConfig(t)
	home, err := sshclient.ResolveHome(f.ctx, f.client, e2eOpTimeout)
	if err != nil {
		t.Fatalf("解析远程 Home 失败: %v", err)
	}
	// 每个用例使用带随机后缀的独立远程管理工作区，并统一落在远程测试资源目录
	// .lingting/agentflow-tests/workflow 下：远端目录是持久状态，与全新测试数据库
	// 共用固定目录会让"版本目录已存在"变成假失败，也会让不同用例互相污染。
	workspaceRoot, err := testkit.RemoteTestRoot(
		home,
		testkit.RemoteTestPackageWorkflow,
		f.suffix,
	)
	if err != nil {
		t.Fatalf("拼接远程管理工作区失败: %v", err)
	}
	host, err := domain.NewExecutionHost(
		"",
		"真实远程端到端节点",
		sshConfig.Host,
		sshConfig.Port,
		sshConfig.User,
		workspaceRoot,
		time.Now().UTC(),
		domain.WithPrivateKey(sshConfig.PrivateKeyPath),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := f.db.CreateExecutionHost(f.ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	if err := f.markHostNormal(t, host.ID); err != nil {
		t.Fatalf("写入节点校验结果失败: %v", err)
	}
	stored, err := f.db.GetExecutionHost(f.ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	f.host = *stored
	t.Logf("REMOTE_HOST=%s HOME=%s", f.host.ID, home)
}

// markHostNormal 把执行节点置为已通过环境预检。
func (f *e2eFixture) markHostNormal(t *testing.T, hostID domain.ID) error {
	t.Helper()
	host, err := f.db.GetExecutionHost(f.ctx, hostID)
	if err != nil {
		return err
	}
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Checks: []domain.ValidationCheck{
			{Name: domain.ValidationCheckSSHConnection, Status: domain.CheckStatusPassed},
		},
	}
	if err := host.RecordValidationResult(
		host.ValidationGeneration, details, time.Now().UTC(),
	); err != nil {
		return err
	}
	_, err = f.db.UpdateExecutionHostValidation(f.ctx, *host, host.ValidationGeneration)
	return err
}

// assemble 用真实服务装配整条链路。
func (f *e2eFixture) assemble(t *testing.T) {
	t.Helper()
	sshService, err := app.NewSSHService(f.db, f.logger, app.SSHOptions{})
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	containers, err := app.NewContainerService(app.DockerServiceOptions{
		SSH: sshService, Store: f.db, Logger: f.logger,
	})
	if err != nil {
		t.Fatalf("构造容器服务失败: %v", err)
	}
	environments, err := app.NewEnvironmentService(app.DockerServiceOptions{
		SSH: sshService, Store: f.db, Logger: f.logger,
	})
	if err != nil {
		t.Fatalf("构造环境服务失败: %v", err)
	}
	// 执行环境的容器端口包一层"创建后注入容器内 Pi 配置"的钩子：真实链路要求
	// 容器内 Pi 能连上模型接入点，而镜像内没有任何凭据。
	execution, err := app.NewExecutionEnvironmentService(app.ExecutionEnvironmentOptions{
		Environments: environments,
		Containers:   containers,
		ContainerPort: &piConfigPort{
			containers: containers,
			fixture:    f,
			t:          f.t,
		},
		Logger: f.logger,
	})
	if err != nil {
		t.Fatalf("构造执行环境服务失败: %v", err)
	}
	sessions, err := app.NewSessionService(f.db, f.workspace.SessionRoot(), f.logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	initWorkspace, err := app.NewTaskWorkspaceService(app.TaskWorkspaceOptions{
		Store: f.db, Containers: containers, Logger: f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造工作区服务失败: %v", err)
	}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store: f.db, Containers: containers, Sessions: sessions,
		Git: containers, Logger: f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行器失败: %v", err)
	}
	reviewer, err := app.NewValidatorReviewer(app.ValidatorReviewerOptions{
		Store: f.db, Containers: containers, Sessions: sessions, Logger: f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造语义审查器失败: %v", err)
	}
	validation, err := app.NewValidationService(app.ValidationOptions{
		Store: f.db, Containers: containers,
		OutputRoot: f.workspace.Dir("validations"),
		Reviewer:   reviewer, Logger: f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
	stepFlow, err := app.NewStepWorkflowService(app.StepWorkflowOptions{
		Steps: executor, Validation: validation, Store: f.db, Cycles: f.db, Logger: f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造单步骤闭环失败: %v", err)
	}
	workflow, err := app.NewTaskWorkflowService(app.TaskWorkflowOptions{
		Store: f.db, Steps: stepFlow, Containers: containers,
		CodeSaver: containers, Sessions: sessions, Logger: f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造任务级编排器失败: %v", err)
	}
	launch, err := app.NewTaskLaunchService(app.TaskLaunchOptions{
		Store: f.db, Environments: execution, Containers: containers,
		Planner: workflow, Workspace: initWorkspace, Workflow: workflow,
		Logger: f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造统一启动服务失败: %v", err)
	}
	f.containers = containers
	f.execution = execution
	f.initWorkspace = initWorkspace
	f.sessions = sessions
	f.executor = executor
	f.validation = validation
	f.stepFlow = stepFlow
	f.workflow = workflow
	f.launch = launch
	f.environments = environments
	startE2EServices(t, launch, executor, validation, stepFlow, workflow)
}

// startE2EServices 启动后台服务并在用例结束时逆序停止。
func startE2EServices(t *testing.T, services ...interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}) {
	t.Helper()
	startCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

// seedTask 写入角色、需求与一条 queued 任务。
//
// 任务从 queued 开始：容器、工作区、计划与工作流全部由统一启动入口推进。
func (f *e2eFixture) seedTask(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	planner := e2eRole(t, f.db, f.ctx, domain.RoleTypePlanner, "架构规划师", now)
	developer := e2eRole(t, f.db, f.ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	validator := e2eRole(t, f.db, f.ctx, domain.RoleTypeValidator, "验证工程师", now)
	requirement, err := domain.NewRequirement(
		"真实远程端到端需求", "验证真实环境下的完整交付链路", domain.PriorityNormal, now,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := f.db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(
		requirement.ID, f.repo.ResolvedRepository(), "main", "", now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &f.host.ID
	task.Config = domain.TaskConfig{
		Goal:            "在仓库根目录新增一个 NOTES.md 文件，内容为一行说明",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
		ValidatorRoleID: validator.ID,
		Retry:           domain.RetryConfig{MaxAttempts: 1},
		Verification: domain.VerificationSpec{
			AcceptanceCriteria: []string{"NOTES.md 存在于仓库根目录"},
			Commands: []domain.VerificationCommand{
				{
					Name:     "notes-exists",
					Command:  "sh",
					Args:     []string{"-c", "test -f NOTES.md"},
					Required: true,
				},
			},
		},
	}
	if err := f.db.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	f.task = task
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
		name, roleType, "group/free", "角色提示词",
		domain.DefaultToolsPolicy(roleType), now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// prepareEnvironmentVersion 在远端创建并构建基础环境版本。
//
// 这是真实远程链路的必要前置：生产环境准备阶段要求已存在可用的环境版本。
// 版本号由服务端分配，镜像标签因此唯一，不会影响其他任务。
func (f *e2eFixture) prepareEnvironmentVersion(t *testing.T) {
	t.Helper()
	// 环境准备有固定顺序，不能一步到位：
	//   1. 初始化目录并写入默认基础版本；
	//   2. 构建基础镜像（语言镜像通过 AGENTFLOW_BASE_IMAGE 依赖它）；
	//   3. 再初始化语言版本（此时才能找到"已构建成功"的基础版本）；
	//   4. 构建语言镜像。
	if _, err := f.environments.Init(f.ctx, app.InitOptions{
		MachineID: f.host.ID.String(),
	}); err != nil {
		t.Fatalf("初始化远程环境目录失败: %v", err)
	}
	base, err := f.environments.Build(f.ctx, app.BuildRequest{
		MachineID: f.host.ID.String(),
		Ref:       domain.EnvironmentRef{Type: domain.EnvironmentTypeBase},
	})
	if err != nil {
		t.Fatalf("构建基础环境镜像失败: %v", err)
	}
	t.Logf("BASE_IMAGE=%s IMAGE_ID=%s VERSION=%s",
		base.Version.ImageRef, base.Version.ImageID, base.Version.Label())
	if _, err := f.environments.Init(f.ctx, app.InitOptions{
		MachineID: f.host.ID.String(), WithLanguages: []string{"node"},
	}); err != nil {
		t.Fatalf("初始化语言环境失败: %v", err)
	}
	built, err := f.environments.Build(f.ctx, app.BuildRequest{
		MachineID: f.host.ID.String(),
		Ref:       domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
	})
	if err != nil {
		t.Fatalf("构建语言环境镜像失败: %v", err)
	}
	t.Logf("ENV_IMAGE=%s IMAGE_ID=%s VERSION=%s",
		built.Version.ImageRef, built.Version.ImageID, built.Version.Label())
}

// steps 读取任务已持久化的计划步骤。
func (f *e2eFixture) steps(t *testing.T) []domain.Step {
	t.Helper()
	steps, err := f.db.ListStepsByTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return steps
}

// TestRemoteE2EFullChain 在真实远程环境执行完整链路，覆盖成功与失败两类终局。
//
// 断言只依赖持久化事实与领域规则，不假设模型输出特定文本：真实模型可能生成不同
// 的计划或代码，但"计划必须落库""状态迁移必须合法""失败必须如实收敛"这些事实
// 与模型输出无关。
func TestRemoteE2EFullChain(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t)
	fixture.prepareEnvironmentVersion(t)

	launched, err := fixture.launch.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID:      fixture.task.ID.String(),
		PlanTimeout: e2eOpTimeout,
		Timeout:     e2eOpTimeout,
	})
	if err != nil {
		// 真实链路失败必须带可定位的阶段信息，而不是笼统报错。
		fixture.dumpFailure(t)
		t.Fatalf("真实远程统一启动失败（阶段 %s，状态 %s）: %v",
			launched.FailureStage, launched.TaskStatus, err)
	}
	if !launched.PlanPersisted {
		t.Fatalf("真实远程启动必须持久化计划: %+v", launched)
	}
	if !launched.WorkflowStarted {
		t.Fatalf("计划就绪后必须启动工作流: %+v", launched)
	}
	t.Logf("LAUNCH stage=%s status=%s container=%s commit=%s steps=%d",
		launched.Stage, launched.TaskStatus, launched.ContainerID,
		launched.WorkspaceCommit, len(launched.PlanSteps))

	// 计划必须真实落库，且首个步骤是规划阶段。
	steps := fixture.steps(t)
	if len(steps) < 2 {
		t.Fatalf("真实计划应至少包含规划阶段与一个开发步骤，实际 %d 个", len(steps))
	}
	if steps[0].Type != domain.StepTypePlan {
		t.Fatalf("首个步骤必须是规划阶段，实际 %s", steps[0].Type)
	}
	for index, step := range steps {
		if step.Sequence != index+1 {
			t.Fatalf("步骤序号不连续: %d", step.Sequence)
		}
		if !step.VerificationSpec.HasCommands() {
			t.Fatalf("步骤 %d 缺少任务级必需命令", step.Sequence)
		}
	}
	// 容器必须真实创建并关联到任务。
	stored, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if strings.TrimSpace(stored.ContainerID) == "" {
		t.Fatal("真实远程启动必须把容器关联写回任务")
	}
	container, err := fixture.db.GetContainer(fixture.ctx, domain.ID(stored.ContainerID))
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if container.DockerID == "" {
		t.Fatal("容器记录必须带有真实 Docker 容器 ID")
	}
	t.Logf("REMOTE_CONTAINER=%s DOCKER_ID=%s", container.ID, container.DockerID)

	// 工作流由编排器在后台推进：等待它停止自动推进后核对终态。
	result := fixture.settle(t)
	t.Logf("WORKFLOW status=%s stage=%s class=%s summary=%s",
		result.Run.Status, result.Run.Stage, result.Run.ErrorClass, result.Run.ErrorSummary)

	// 无论成功或失败，执行记录必须真实落库，且状态必须是领域规则允许的取值。
	storedRun, err := fixture.db.GetWorkflowRun(fixture.ctx, result.Run.RunID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if storedRun.Status != result.Run.Status {
		t.Fatalf("数据库中的执行记录状态与返回值不一致: %s != %s",
			storedRun.Status, result.Run.Status)
	}
	if storedRun.Status == domain.WorkflowRunRunning ||
		storedRun.Status == domain.WorkflowRunPending {
		t.Fatalf("执行记录不得停留在未收敛状态: %s", storedRun.Status)
	}
	// 步骤必须都已收敛：不存在仍在执行占用的步骤。
	for _, step := range fixture.steps(t) {
		switch step.Status {
		case domain.StepStatusRunning, domain.StepStatusDeveloping, domain.StepStatusVerifying:
			t.Fatalf("步骤 %d 仍处于执行占用 %s，工作流不得报告已停止",
				step.Sequence, step.Status)
		}
	}
	// 只有全部必需条件满足时才允许完成；否则必须给出可解释的停止原因。
	finalTask, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	t.Logf("FINAL_TASK status=%s result=%s failure=%s",
		finalTask.Status, finalTask.FinalResult, finalTask.FailureSummary)
	if storedRun.Status == domain.WorkflowRunCompleted {
		if finalTask.Status != domain.TaskStatusCompleted {
			t.Fatalf("工作流完成时任务必须为 completed，实际 %s", finalTask.Status)
		}
		if finalTask.FinalResult != domain.TaskResultSucceeded {
			t.Fatalf("完成的任务结论必须为 succeeded，实际 %s", finalTask.FinalResult)
		}
		// 真实交付必须留下代码保存记录与已归档会话。
		records, err := fixture.db.ListCodeSaveRecordsByTask(
			fixture.ctx, fixture.task.ID.String(),
		)
		if err != nil {
			t.Fatalf("读取代码保存记录失败: %v", err)
		}
		if len(records) == 0 {
			t.Fatal("工作流完成时必须留下代码保存记录")
		}
		record := records[len(records)-1]
		if record.PushStatus != domain.PushStatusSucceeded {
			t.Fatalf("代码保存必须推送成功，实际 %s（%s）",
				record.PushStatus, record.ErrorSummary)
		}
		if record.CommitSHA == "" {
			t.Fatal("代码保存记录必须带有真实 commit SHA")
		}
		t.Logf("CODE_SAVE commit=%s push=%s branch=%s",
			record.CommitSHA, record.PushStatus, record.TaskBranch)
		sessions, err := fixture.db.ListSessionsByTask(fixture.ctx, fixture.task.ID)
		if err != nil {
			t.Fatalf("读取会话失败: %v", err)
		}
		if len(sessions) == 0 {
			t.Fatal("工作流完成时必须留下会话记录")
		}
		for _, session := range sessions {
			if session.ArchiveStatus != domain.SessionArchiveSucceeded {
				t.Fatalf("会话 %s 必须归档成功，实际 %s", session.ID, session.ArchiveStatus)
			}
		}
		t.Logf("SESSIONS=%d 全部已归档", len(sessions))
		return
	}
	// 未完成时必须停在领域规则允许的停止状态，并给出可解释的原因。
	switch storedRun.Status {
	case domain.WorkflowRunBlocked, domain.WorkflowRunRecoveryRequired:
	default:
		t.Fatalf("未完成的工作流必须停在 blocked 或 recovery_required，实际 %s",
			storedRun.Status)
	}
	if finalTask.Status == domain.TaskStatusCompleted {
		t.Fatal("工作流未完成时任务不得标记为完成")
	}
	if strings.TrimSpace(storedRun.ErrorClass) == "" {
		t.Fatal("未完成的工作流必须给出可诊断的错误分类")
	}
	// 停在交付阶段是可解释的真实终局：前面的开发与验证确实全部通过，只有代码保存
	// 未成功。把这类停止与"开发/验证阶段失败"混为一谈会掩盖真实链路已经走通的事实。
	records, err := fixture.db.ListCodeSaveRecordsByTask(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("停在交付阶段时必须留下代码保存记录，实际分类 %s", storedRun.ErrorClass)
	}
	record := records[len(records)-1]
	if record.CommitSHA == "" {
		t.Fatalf("代码保存记录必须带有真实 commit SHA（分类 %s）", storedRun.ErrorClass)
	}
	// 提交必须真实成功：这是"Agent 真的改动了容器内代码"的证据。
	if record.CommitStatus != domain.CommitStatusSucceeded &&
		record.CommitStatus != domain.CommitStatusNoChanges {
		t.Fatalf("代码保存的提交必须真实成功，实际 %s（%s）",
			record.CommitStatus, record.ErrorSummary)
	}
	// 推送未成功时必须给出可解释的原因，且不得伪造成成功。
	if record.PushStatus != domain.PushStatusSucceeded &&
		strings.TrimSpace(record.ErrorSummary) == "" {
		t.Fatalf("推送未成功时必须给出可诊断原因，实际状态 %s", record.PushStatus)
	}
	t.Logf("STOPPED_AT_DELIVERY class=%s commit=%s push=%s reason=%s",
		storedRun.ErrorClass, record.CommitSHA, record.PushStatus, record.ErrorSummary)
}

// dumpFailure 输出失败现场的可诊断信息。
func (f *e2eFixture) dumpFailure(t *testing.T) {
	t.Helper()
	t.Logf("TEST_ARTIFACT_DIR=%s", f.workspace.Root())
	task, err := f.db.GetTask(f.ctx, f.task.ID)
	if err == nil {
		t.Logf("TASK status=%s failure=%s", task.Status, task.FailureSummary)
	}
	inits, err := f.db.ListTaskWorkspaceInitsByTask(f.ctx, f.task.ID.String())
	if err == nil {
		for _, init := range inits {
			t.Logf("WORKSPACE_INIT status=%s stage=%s error=%s/%s",
				init.Status, init.Stage, init.ErrorCode, init.ErrorSummary)
		}
	}
}

// 编译期确认本用例使用的服务确实实现生产契约。
var (
	_ execenv.Provider              = (*app.ExecutionEnvironmentService)(nil)
	_ app.WorkflowCodeSaverPort     = (*app.ContainerService)(nil)
	_ app.TaskLaunchEnvironmentPort = (*app.ExecutionEnvironmentService)(nil)
	_ app.TaskLaunchContainerPort   = (*app.ContainerService)(nil)
	_ app.TaskLaunchWorkspacePort   = (*app.TaskWorkspaceService)(nil)
	_ app.TaskLaunchPlanPort        = (*app.TaskWorkflowService)(nil)
	_ app.TaskLaunchWorkflowPort    = (*app.TaskWorkflowService)(nil)
)

// loadContainer 读取容器登记记录。
func (f *e2eFixture) loadContainer(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	id := domain.ID(strings.TrimSpace(containerID))
	if err := id.Validate(); err != nil {
		return domain.Container{}, fmt.Errorf("读取容器记录失败: %w", err)
	}
	container, err := f.db.GetContainer(ctx, id)
	if err != nil {
		return domain.Container{}, fmt.Errorf("读取容器记录失败: %w", err)
	}
	return *container, nil
}

// loadHost 读取执行节点配置。
func (f *e2eFixture) loadHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	id := domain.ID(strings.TrimSpace(hostID))
	if err := id.Validate(); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("读取执行节点失败: %w", err)
	}
	host, err := f.db.GetExecutionHost(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("读取执行节点失败: %w", err)
	}
	return *host, nil
}

// applyContainerPiConfig 把 docker.pi 配置写入容器内 Pi 的配置目录。
//
// 真实端到端链路必须让容器内 Pi 能连上模型接入点：镜像内没有凭据，配置只能来自
// tests/config/docker.yml 的 docker.pi。文件只写入容器内部文件系统
// （$HOME/.pi/agent），不写入项目代码目录、不写入远程项目配置目录，也不挂载宿主机
// 目录；密钥不会出现在日志或数据库里。
//
// 该步骤属于测试侧的凭据注入，与产品无关：产品本身不保存模型凭据。
func (f *e2eFixture) applyContainerPiConfig(t *testing.T, dockerID string) {
	t.Helper()
	config, _ := testkit.LoadDockerConfig(t)
	if !config.Pi.Enabled() {
		t.Logf("CONTAINER_PI_CONFIG=none（沿用容器内已有配置）")
		return
	}
	home, err := f.containerExec(t, dockerID, []string{"sh", "-c", `printf %s "$HOME"`})
	if err != nil {
		t.Fatalf("解析容器内 Home 失败: %v", err)
	}
	home = strings.TrimSpace(home)
	if !strings.HasPrefix(home, "/") {
		t.Fatalf("容器内 Home 不是绝对路径: %q", home)
	}
	dir := home + "/" + testkit.ContainerPiConfigDir
	if _, err := f.containerExec(t, dockerID, []string{"mkdir", "-p", dir}); err != nil {
		t.Fatalf("创建容器内 Pi 配置目录失败: %v", err)
	}
	write := func(name string, content []byte) {
		t.Helper()
		script := fmt.Sprintf("cat > %s && chmod 600 %s", name, name)
		if _, writeErr := f.containerExecWithInput(
			t, dockerID, []string{"sh", "-c", script}, string(content),
		); writeErr != nil {
			t.Fatalf("写入容器内 Pi 配置 %s 失败: %v", name, writeErr)
		}
	}
	write(dir+"/models.json", testkit.ContainerPiModelsFile(config.Pi))
	write(dir+"/settings.json", testkit.ContainerPiSettingsFile(config.Pi))
	t.Logf("CONTAINER_PI_CONFIG=%s provider=%s model=%s thinking=%s",
		dir, config.Pi.ResolvedProvider(), config.Pi.Model, config.Pi.ThinkingLevel)
}

// verifySSHClientConfig 校验基础镜像已内置 GitHub 的 443 接入点配置。
//
// 该配置由产品基础镜像（internal/docker.DefaultBaseDockerfile）提供：ssh.github.com:443
// 是 GitHub 官方支持的等价接入点，在 22 可达与不可达的环境下都成立，因此属于镜像
// 默认值。这里只做只读校验，不再由测试注入——如果它缺失，说明基础镜像回归了，应当
// 让用例失败而不是在测试侧补齐。
func (f *e2eFixture) verifySSHClientConfig(t *testing.T, dockerID string) {
	t.Helper()
	output, err := f.containerExec(t, dockerID, []string{"cat", "/root/.ssh/config"})
	if err != nil {
		t.Fatalf("读取容器内 SSH 客户端配置失败: %v", err)
	}
	for _, expected := range []string{
		"Host github.com",
		"Hostname ssh.github.com",
		"Port 443",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("基础镜像缺少 GitHub 443 接入点配置项 %q，实际内容:\n%s", expected, output)
		}
	}
	t.Logf("CONTAINER_SSH_CONFIG=/root/.ssh/config（由基础镜像内置）")
}

// applyGitCredentials 把 git.yml 中的凭据注入容器内临时目录。
//
// 与 tests/integration/remote/agent 的做法一致：私钥经标准输入写入容器内临时文件，
// 并通过 core.sshCommand 限定到该私钥。凭据只存在于本次容器内部文件系统，不写入
// 项目代码目录、不写入远程项目配置目录、不挂载宿主机目录，容器删除后随之消失；
// 私钥内容不会出现在日志或数据库里。
//
// 该步骤属于测试侧凭据注入：产品本身不保存 Git 凭据（internal/source 与
// internal/codesaver 的 GitCredentialProvider 均无实现）。
func (f *e2eFixture) applyGitCredentials(t *testing.T, dockerID string) {
	t.Helper()
	if missing := f.repo.CredentialMissing(); len(missing) > 0 {
		t.Fatalf("Git 凭据不完整，缺少 %s", strings.Join(missing, ", "))
	}
	auth, err := testkit.ApplyGitCredentials(
		f.ctx,
		f.exec,
		dockerID,
		f.repo,
		"/tmp/agentflow-credentials-"+f.suffix,
	)
	if err != nil {
		t.Fatalf("注入容器内 Git 凭据失败: %v", err)
	}
	if !auth.Enabled() {
		t.Logf("GIT_CREDENTIAL=none（沿用容器内已有配置）")
		return
	}
	t.Logf("GIT_CREDENTIAL=%s DIR=%s", auth.Kind, auth.Dir)
}

// applyGitIdentity 在容器内配置提交身份。
//
// 代码保存使用容器内 Git 提交，而提交身份属于执行环境的一部分：产品不伪造身份，
// 实际运行时由项目环境镜像提供。测试镜像未预置身份，因此这里按仓库测试规范显式
// 写入容器内的 Git 配置（只存在于本次容器，容器删除后消失）。
func (f *e2eFixture) applyGitIdentity(t *testing.T, dockerID string) {
	t.Helper()
	for _, item := range [][]string{
		{"git", "config", "--global", "user.name", "AgentFlow Tester"},
		{"git", "config", "--global", "user.email", "agentflow@example.com"},
	} {
		if _, err := f.containerExec(t, dockerID, item); err != nil {
			t.Fatalf("写入容器内 Git 身份失败 %v: %v", item, err)
		}
	}
	t.Logf("CONTAINER_GIT_IDENTITY=AgentFlow Tester <agentflow@example.com>")
}

// containerExec 在容器内执行一条命令并返回标准输出。
func (f *e2eFixture) containerExec(
	t *testing.T,
	dockerID string,
	command []string,
) (string, error) {
	t.Helper()
	return f.containerExecWithInput(t, dockerID, command, "")
}

// containerExecWithInput 在容器内执行一条命令并可写入标准输入。
func (f *e2eFixture) containerExecWithInput(
	t *testing.T,
	dockerID string,
	command []string,
	stdin string,
) (string, error) {
	t.Helper()
	var (
		run docker.CommandResult
		err error
	)
	if stdin == "" {
		run, err = f.runner.ExecCommand(f.ctx, dockerID, command, e2eOpTimeout)
	} else {
		run, err = f.runner.ExecCommandWithInput(
			f.ctx, dockerID, command, strings.NewReader(stdin), e2eOpTimeout,
		)
	}
	if err != nil {
		return "", err
	}
	if !run.Succeeded() {
		return "", fmt.Errorf("容器内命令退出码 %d: %s", run.ExitCode, run.Stderr)
	}
	return run.Stdout, nil
}

// piConfigPort 在执行环境创建容器之后，把模型接入配置注入容器内 Pi。
//
// 它只包一层"容器创建完成后的钩子"，不改变任何生产语义：容器创建、状态推进与
// 登记写回仍由 app.ContainerService 完成。注入内容来自 tests/config/docker.yml 的
// docker.pi，属于测试侧凭据，产品本身不保存模型凭据。
type piConfigPort struct {
	containers *app.ContainerService
	fixture    *e2eFixture
	t          *testing.T
}

// CreateContainer 创建容器后立即写入容器内 Pi 配置。
func (p *piConfigPort) CreateContainer(
	ctx context.Context,
	request app.CreateContainerRequest,
) (app.ContainerDetail, error) {
	detail, err := p.containers.CreateContainer(ctx, request)
	if err != nil {
		return detail, err
	}
	if detail.Container.DockerID == "" {
		return detail, fmt.Errorf("容器 %s 创建后缺少 Docker 容器 ID", detail.Container.ID)
	}
	// 容器创建成功后立即按精确身份登记，后续注入步骤失败也能精确回收。
	p.fixture.trackCreatedContainer(p.t, detail.Container)
	p.fixture.applyContainerPiConfig(p.t, detail.Container.DockerID)
	p.fixture.verifySSHClientConfig(p.t, detail.Container.DockerID)
	p.fixture.applyGitCredentials(p.t, detail.Container.DockerID)
	p.fixture.applyGitIdentity(p.t, detail.Container.DockerID)
	return detail, nil
}

// InspectContainer 转发到生产容器服务。
func (p *piConfigPort) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	return p.containers.InspectContainer(ctx, request)
}

// Exec 转发到生产容器服务。
func (p *piConfigPort) Exec(
	ctx context.Context,
	request app.ExecRequest,
) (app.ExecResult, error) {
	return p.containers.Exec(ctx, request)
}

// DeleteContainer 转发到生产容器服务。
func (p *piConfigPort) DeleteContainer(
	ctx context.Context,
	request app.DeleteContainerRequest,
) (app.ContainerDetail, error) {
	return p.containers.DeleteContainer(ctx, request)
}

// DetachContainer 转发到生产容器服务。
func (p *piConfigPort) DetachContainer(
	ctx context.Context,
	request app.DetachContainerRequest,
) (app.ContainerDetail, error) {
	return p.containers.DetachContainer(ctx, request)
}

// LoadContainer 读取容器登记记录。
func (p *piConfigPort) LoadContainer(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	return p.fixture.loadContainer(ctx, containerID)
}

// LoadHost 读取执行节点配置。
func (p *piConfigPort) LoadHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	return p.fixture.loadHost(ctx, hostID)
}

// settle 等待工作流停止自动推进并返回最后的执行记录。
func (f *e2eFixture) settle(t *testing.T) app.WorkflowStatusResult {
	t.Helper()
	const timeout = 30 * time.Minute
	deadline := time.Now().Add(timeout)
	var last app.WorkflowStatusResult
	for time.Now().Before(deadline) {
		result, err := f.workflow.GetWorkflowStatus(f.ctx, f.task.ID.String())
		if err != nil {
			t.Fatalf("查询工作流状态失败: %v", err)
		}
		last = result
		if result.HasRun && e2eSettled(result.Run.Status) {
			return result
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("等待工作流收敛超时，最后一次状态: %s/%s",
		last.Run.Status, last.Run.ErrorClass)
	return last
}

// e2eSettled 判断工作流是否已经停止自动推进。
func e2eSettled(status domain.WorkflowRunStatus) bool {
	if status.IsTerminal() {
		return true
	}
	return status == domain.WorkflowRunBlocked || status == domain.WorkflowRunRecoveryRequired
}
