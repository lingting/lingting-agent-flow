package planning_remote

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// fixture 是一条真实规划运行时验收的完整现场。
//
// 它用生产装配方式构造 ContainerService、PlanningRuntimeService、
// PlanningAgentService、PlanningSessionCoordinator、当前 storage.Store 与 RoleService，
// 只把"配置来源"换成测试配置；没有第二套容器管理、Pi Runner 或 Session 管理实现。
type fixture struct {
	*env

	db           *sqlite.DB
	dbPath       string
	host         domain.ExecutionHost
	project      domain.Project
	version      domain.EnvironmentVersion
	containers   *app.ContainerService
	roles        *app.RoleService
	sessions     *app.PlanningSessionService
	runtime      *app.PlanningRuntimeService
	agent        *app.PlanningAgentService
	coordination *app.PlanningSessionCoordinator
	// sshOptions 是本次用例使用的 SSH 服务选项，服务重建时复用同一份装配。
	sshOptions app.SSHOptions
}

// fixtureOptions 是构造 fixture 时可选覆盖的装配参数。
type fixtureOptions struct {
	// sshOptions 覆盖 SSH 服务选项；零值表示生产默认装配。
	sshOptions app.SSHOptions
}

// newFixture 装配真实规划运行时现场。
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, fixtureOptions{})
}

// newFixtureWith 用给定装配参数构造真实规划运行时现场。
func newFixtureWith(t *testing.T, options fixtureOptions) *fixture {
	t.Helper()
	realEnv := newEnv(t)
	instance := &fixture{
		env:        realEnv,
		dbPath:     realEnv.workspace.DatabasePath(),
		sshOptions: options.sshOptions,
	}
	instance.openDB(t)
	instance.createHost(t)
	// 清理责任必须在规划容器创建之前登记：容器一旦创建就受测试追踪，准备过程
	// 中途失败也不会失去回收能力。
	planningHostContainerRun.Track(t, realEnv.runner, testkit.TrackedContainer{
		Resolve: instance.resolvePlanningContainers,
	})
	instance.createEnvironmentVersion(t)
	instance.initRemoteEnvironment(t)
	instance.createProject(t)
	instance.assemble(t)
	instance.configurePlanningRoleModels(t)
	return instance
}

// openDB 打开文件型 SQLite 数据库并补齐固定角色。
func (f *fixture) openDB(t *testing.T) {
	t.Helper()
	db, err := sqlite.Open(f.ctx, sqlite.Options{Path: f.dbPath})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	f.db = db
	if _, err := db.SeedRoles(f.ctx, app.RoleSeeds()); err != nil {
		t.Fatalf("初始化固定角色失败: %v", err)
	}
}

// closeDB 关闭数据库连接。
func (f *fixture) closeDB(t *testing.T) {
	t.Helper()
	if f.db == nil {
		return
	}
	if err := f.db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	f.db = nil
}

// createHost 写入一个已通过环境预检的执行节点。
//
// 远程管理工作区落在 ~/.lingting/agentflow-tests/planning/<随机后缀> 下，与生产工作空间
// 和其他测试包完全隔离。
func (f *fixture) createHost(t *testing.T) {
	t.Helper()
	home, err := sshclient.ResolveHome(f.ctx, f.client, planningOpTimeout)
	if err != nil {
		t.Fatalf("解析远程 Home 失败: %v", err)
	}
	workspaceRoot, err := testkit.RemoteTestRoot(
		home,
		testkit.RemoteTestPackagePlanning,
		f.suffix,
		"host",
	)
	if err != nil {
		t.Fatalf("拼接远程管理工作区失败: %v", err)
	}
	host, err := domain.NewExecutionHost(
		"",
		"真实规划验收节点",
		f.sshConfig.Host,
		f.sshConfig.Port,
		f.sshConfig.User,
		workspaceRoot,
		time.Now().UTC(),
		domain.WithPrivateKey(f.sshConfig.PrivateKeyPath),
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
	t.Logf("REMOTE_HOST=%s HOME=%s WORKSPACE=%s", f.host.ID, home, workspaceRoot)
}

// markHostNormal 把执行节点置为已通过环境预检。
//
// 生产路径中该状态由 HostValidationService 在真实预检后写入；本测试只关注规划运行时，
// 因此直接写入同一份校验结论，避免重复验证与规划无关的节点能力。
func (f *fixture) markHostNormal(t *testing.T, hostID domain.ID) error {
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

// createEnvironmentVersion 记录本次测试使用的环境版本。
//
// 记录两个真实版本，与生产构建链一致：
//   - 基础环境版本指向通用测试镜像（本目录测试镜像的构建基础）；
//   - 语言环境版本指向规划运行时测试镜像，并记录父基础环境版本标签。
//
// 镜像 ID 全部从远端读取真实值，不使用伪造值；记录内容不含任何凭据。
func (f *fixture) createEnvironmentVersion(t *testing.T) {
	t.Helper()
	base := f.recordEnvironmentVersion(t, domain.EnvironmentRef{
		Type: domain.EnvironmentTypeBase,
		Name: domain.BaseEnvironmentName,
	}, baseTestImage, "")
	version := f.recordEnvironmentVersion(t, domain.EnvironmentRef{
		Type: domain.EnvironmentTypeLanguage,
		Name: "node",
	}, f.image, base.Label())
	f.version = version
	t.Logf(
		"ENV_VERSION=%s IMAGE_REF=%s IMAGE_ID=%s PARENT=%s",
		version.Label(),
		version.ImageRef,
		version.ImageID,
		version.ParentVersion,
	)
}

// recordEnvironmentVersion 写入一个已构建成功、指向真实远端镜像的版本记录。
func (f *fixture) recordEnvironmentVersion(
	t *testing.T,
	ref domain.EnvironmentRef,
	imageRef, parentVersion string,
) domain.EnvironmentVersion {
	t.Helper()
	imageID, err := f.runner.InspectImageID(f.ctx, imageRef)
	if err != nil {
		t.Fatalf("读取镜像 %s 的真实 ID 失败: %v", imageRef, err)
	}
	now := time.Now().UTC()
	version := domain.EnvironmentVersion{
		HostID:           f.host.ID,
		Ref:              ref,
		Number:           1,
		ParentVersion:    parentVersion,
		DockerfileSHA256: sha256Hex(imageRef + "/Dockerfile"),
		ContextSHA256:    sha256Hex(imageRef + "/context"),
		ImageRef:         imageRef,
		ImageID:          imageID,
		Status:           domain.BuildStatusSucceeded,
		CreatedAt:        now,
	}
	builtAt := now
	version.BuiltAt = &builtAt
	version.BuildConfigSHA256 = docker.BuildConfigHash(version, "")
	if err := f.db.CreateEnvironmentVersion(f.ctx, version); err != nil {
		t.Fatalf("写入环境版本 %s 失败: %v", version.Label(), err)
	}
	t.Logf("ENV_RECORD=%s IMAGE_REF=%s IMAGE_ID=%s", version.Label(), version.ImageRef, version.ImageID)
	return version
}

// initRemoteEnvironment 用生产环境服务初始化远程管理工作空间。
//
// 规划容器把快照卷与容器登记清单都写在远程管理工作空间下，因此必须先按
// `agentflow docker init` 的生产路径创建目录结构。环境版本记录已经写入，Init 因此
// 不会重新创建版本记录，也不会覆盖镜像信息。
func (f *fixture) initRemoteEnvironment(t *testing.T) {
	t.Helper()
	sshService, err := app.NewSSHService(f.db, f.logger, f.sshOptions)
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	environments, err := app.NewEnvironmentService(app.DockerServiceOptions{
		SSH:    sshService,
		Store:  f.db,
		Logger: f.logger,
	})
	if err != nil {
		t.Fatalf("构造环境服务失败: %v", err)
	}
	result, err := environments.Init(f.ctx, app.InitOptions{
		MachineID:     f.host.ID.String(),
		WithLanguages: []string{"node"},
	})
	if err != nil {
		t.Fatalf("初始化远程环境目录失败: %v", err)
	}
	if !result.Preflight.Ready() {
		t.Fatalf("远程 Docker 预检未通过(%s): %s",
			result.Preflight.ErrorCode, result.Preflight.Detail)
	}
	t.Logf(
		"REMOTE_DOCKER CLI=%s ENGINE=%s API=%s MIN_API=%s ROOT=%s",
		result.Preflight.CLIVersion,
		result.Preflight.EngineVersion,
		result.Preflight.APIVersion,
		result.Preflight.MinAPIVersion,
		result.WorkspaceRoot,
	)
}

// createProject 写入规划会话所属项目。
func (f *fixture) createProject(t *testing.T) {
	t.Helper()
	repository := anonymousHTTPSRepository(t, f.gitConfig.ResolvedRepository())
	project, err := domain.NewProject(domain.ProjectInput{
		ID:                 "planning-" + f.suffix,
		Name:               "规划运行时验收项目",
		RepositoryURL:      repository,
		BaseBranch:         planningBaseBranch,
		Environment:        domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
		EnvironmentVersion: "v1",
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造项目失败: %v", err)
	}
	if err := f.db.CreateProject(f.ctx, project); err != nil {
		t.Fatalf("写入项目失败: %v", err)
	}
	f.project = project
	t.Logf("PROJECT=%s REPOSITORY=%s BRANCH=%s", project.ID, repository, project.NormalizedBaseBranch())
}

// assemble 用生产装配方式构造规划服务。
func (f *fixture) assemble(t *testing.T) {
	t.Helper()
	sshService, err := app.NewSSHService(f.db, f.logger, f.sshOptions)
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	containers, err := app.NewContainerService(app.DockerServiceOptions{
		SSH:    sshService,
		Store:  f.db,
		Logger: f.logger,
	})
	if err != nil {
		t.Fatalf("构造容器服务失败: %v", err)
	}
	roles, err := app.NewRoleService(f.db)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	sessions, err := app.NewPlanningSessionService(app.PlanningSessionServiceOptions{Store: f.db})
	if err != nil {
		t.Fatalf("构造规划会话服务失败: %v", err)
	}
	// 协调器由规划运行时与规划 Agent 共用：RunRole、Prepare 与 Release 串行同一会话。
	coordination := app.NewPlanningSessionCoordinator()
	runtime, err := app.NewPlanningRuntimeService(app.PlanningRuntimeServiceOptions{
		Store:        f.db,
		Containers:   containers,
		Coordination: coordination,
	})
	if err != nil {
		t.Fatalf("构造规划运行时服务失败: %v", err)
	}
	agent, err := app.NewPlanningAgentService(app.PlanningAgentServiceOptions{
		Store:        f.db,
		Roles:        roles,
		Containers:   containers,
		Runtime:      runtime,
		Coordination: coordination,
		SessionRoot:  f.sessionRoot,
	})
	if err != nil {
		t.Fatalf("构造规划 Agent 服务失败: %v", err)
	}
	f.containers = containers
	f.roles = roles
	f.sessions = sessions
	f.runtime = runtime
	f.agent = agent
	f.coordination = coordination
	f.startAgent(t)
}

// configurePlanningRoleModels 把两个规划角色的模型改成真实测试模型。
//
// 内置角色的默认模型是一个占位标识（product 不代用户选择模型），真实调用必须显式指定
// docker.pi.model；这里通过生产角色服务写入，不绕过角色配置校验。
func (f *fixture) configurePlanningRoleModels(t *testing.T) {
	t.Helper()
	model := f.model
	for _, roleType := range []domain.RoleType{
		domain.RoleTypeRequirementAnalyst,
		domain.RoleTypePlanner,
	} {
		updated, err := f.roles.Update(f.ctx, planningRoleIDFor(t, roleType), app.RoleUpdate{
			Model: &model,
		})
		if err != nil {
			t.Fatalf("配置规划角色 %s 的模型失败: %v", roleType, err)
		}
		if updated.Model != model {
			t.Fatalf("规划角色 %s 的模型未保存: 期望 %s，实际 %s", roleType, model, updated.Model)
		}
		t.Logf("ROLE_MODEL role=%s model=%s", roleType, updated.Model)
	}
}

// startAgent 启动规划 Agent 服务的生命周期。
func (f *fixture) startAgent(t *testing.T) {
	t.Helper()
	startCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := f.agent.Start(startCtx); err != nil {
		t.Fatalf("启动规划 Agent 服务失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stopCancel()
		if err := f.agent.Stop(stopCtx); err != nil {
			t.Errorf("停止规划 Agent 服务失败: %v", err)
		}
	})
}

// rebuild 关闭本次应用服务与数据库连接，再用同一个数据库文件与 Session 根目录重建。
//
// 它模拟"服务实例重建"：数据库文件、Session 根目录与远端资源都不改变，只有进程内的
// 服务对象与 SQLite 连接被关闭并重新打开。它不等于 Server 进程崩溃重启，报告时必须
// 如实区分。
func (f *fixture) rebuild(t *testing.T) {
	t.Helper()
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := f.agent.Stop(stopCtx); err != nil {
		t.Fatalf("停止规划 Agent 服务失败: %v", err)
	}
	f.closeDB(t)
	f.openDB(t)
	f.assemble(t)
}

// newSession 创建一个 analyzing 状态的规划会话。
func (f *fixture) newSession(t *testing.T, prompt string) domain.PlanningSession {
	t.Helper()
	session, err := f.sessions.Create(f.ctx, app.CreatePlanningSessionRequest{
		ProjectID: f.project.ID,
	})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	started, err := f.sessions.SubmitMessage(f.ctx, session.ID.String(), app.SubmitPlanningMessageRequest{
		Content: prompt,
	})
	if err != nil {
		t.Fatalf("提交规划消息失败: %v", err)
	}
	t.Logf("PLANNING_SESSION=%s", started.Session.ID)
	return started.Session
}

// prepare 用生产服务准备规划运行环境，并登记容器与快照卷的清理责任。
func (f *fixture) prepare(t *testing.T, session domain.PlanningSession) app.PlanningRuntimeDetail {
	t.Helper()
	detail, err := f.runtime.Prepare(f.ctx, app.PreparePlanningRuntimeRequest{
		PlanningSessionID: session.ID.String(),
		HostID:            f.host.ID.String(),
	})
	if err != nil {
		t.Fatalf("准备规划运行环境失败: %v", err)
	}
	f.trackRuntime(t, detail)
	return detail
}

// trackRuntime 登记一次已准备的规划运行时资源的清理责任。
func (f *fixture) trackRuntime(t *testing.T, detail app.PlanningRuntimeDetail) {
	t.Helper()
	record := detail.Container
	planningHostContainerRun.Track(t, f.runner, testkit.TrackedContainer{
		Name:     record.Name,
		DockerID: record.DockerID,
		Record:   &record,
	})
	planningVolumeRun.Track(t, f.runner, detail.CodeVolumeName)
	t.Logf(
		"PLANNING_RUNTIME session=%s container=%s docker_id=%s volume=%s commit=%s",
		detail.Session.ID,
		record.ID,
		record.DockerID,
		detail.CodeVolumeName,
		detail.SourceCommit,
	)
}

// resolvePlanningContainers 从数据库解析本测试节点创建的全部容器记录。
func (f *fixture) resolvePlanningContainers(
	ctx context.Context,
) ([]testkit.TrackedContainer, error) {
	records, err := f.db.ListContainers(ctx, domain.ContainerFilter{HostID: f.host.ID})
	if err != nil {
		return nil, fmt.Errorf("读取规划容器记录失败: %w", err)
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

// volumeName 返回本会话在测试节点上的快照卷名称。
//
// 名称规则与生产一致（agentflow-plan-<规划会话>-<执行节点>），这里复用它以便在
// 规划运行时之外核验卷的真实状态。
func (f *fixture) volumeName(session domain.PlanningSession) string {
	return "agentflow-plan-" + session.ID.String() + "-" + f.host.ID.String()
}

// reloadSession 重新读取规划会话的持久化事实。
func (f *fixture) reloadSession(t *testing.T, sessionID domain.ID) domain.PlanningSession {
	t.Helper()
	session, err := f.db.GetPlanningSession(f.ctx, sessionID)
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	return *session
}

// reloadContainer 重新读取容器登记记录。
func (f *fixture) reloadContainer(t *testing.T, containerID domain.ID) domain.Container {
	t.Helper()
	container, err := f.db.GetContainer(f.ctx, containerID)
	if err != nil {
		t.Fatalf("读取容器登记记录失败: %v", err)
	}
	return *container
}

// injectPiConfig 把测试侧的模型接入配置写入规划容器的临时可写配置目录。
//
// 规划容器 rootfs 只读，$HOME 不可写，因此配置只能写入 /tmp 下的配置目录：目录位置
// 由测试镜像的 PI_CODING_AGENT_DIR 指定，内容来自 tests/config/docker.yml 的 docker.pi。
// 它属于测试侧凭据注入，不改变生产角色策略，也不写入日志、数据库或项目代码目录。
func (f *fixture) injectPiConfig(t *testing.T, containerID domain.ID) {
	t.Helper()
	piConfig := f.dockerCfg.Pi
	if !piConfig.Enabled() {
		t.Fatalf("容器内 Pi 配置不完整，无法执行真实 Pi 调用")
	}
	if _, err := f.containers.Exec(f.ctx, app.ExecRequest{
		ContainerID: containerID.String(),
		Command:     []string{"mkdir", "-p", planningPiConfigDir},
		WorkDir:     planningWorkDir,
		Timeout:     planningOpTimeout,
	}); err != nil {
		t.Fatalf("创建规划容器内 Pi 配置目录失败: %v", err)
	}
	f.writeContainerFile(t, containerID, planningPiConfigDir+"/models.json",
		testkit.ContainerPiModelsFile(piConfig))
	f.writeContainerFile(t, containerID, planningPiConfigDir+"/settings.json",
		testkit.ContainerPiSettingsFile(piConfig))
	t.Logf(
		"CONTAINER_PI_CONFIG=%s provider=%s model=%s thinking=%s",
		planningPiConfigDir,
		piConfig.ResolvedProvider(),
		piConfig.Model,
		piConfig.ThinkingLevel,
	)
}

// writeContainerFile 通过生产容器服务在容器内写入一个文件。
func (f *fixture) writeContainerFile(
	t *testing.T,
	containerID domain.ID,
	target string,
	content []byte,
) {
	t.Helper()
	script := fmt.Sprintf("cat > %s && chmod 600 %s", target, target)
	if _, err := f.containers.Exec(f.ctx, app.ExecRequest{
		ContainerID: containerID.String(),
		Command:     []string{"sh", "-c", script},
		WorkDir:     planningWorkDir,
		Stdin:       content,
		Timeout:     planningOpTimeout,
	}); err != nil {
		t.Fatalf("写入容器内文件 %s 失败: %v", target, err)
	}
}

// runRole 执行一次真实规划角色调用。
func (f *fixture) runRole(
	t *testing.T,
	session domain.PlanningSession,
	roleType domain.RoleType,
	prompt string,
) (app.PlanningAgentResult, error) {
	t.Helper()
	return f.agent.RunRole(f.ctx, app.RunPlanningRoleRequest{
		PlanningSessionID: session.ID.String(),
		RoleType:          roleType.String(),
		Prompt:            prompt,
	})
}

// execInContainer 在规划容器内执行一条命令并返回原始结果。
func (f *fixture) execInContainer(
	t *testing.T,
	containerID domain.ID,
	command []string,
) app.ExecResult {
	t.Helper()
	result, err := f.containers.Exec(f.ctx, app.ExecRequest{
		ContainerID: containerID.String(),
		Command:     command,
		WorkDir:     planningWorkDir,
		Timeout:     planningOpTimeout,
	})
	if err != nil {
		t.Fatalf("在规划容器内执行 %v 失败: %v（退出码 %d）: %s",
			command, err, result.Result.ExitCode, strings.TrimSpace(result.Result.Stderr))
	}
	return result
}

// inspectRaw 读取容器原始的 docker inspect JSON。
//
// 生产 docker.ContainerInspect 只保留生命周期与标签字段；只读挂载、tmpfs 与
// HostConfig.ReadonlyRootfs 必须从原始输出核验，不能用创建参数代替现场事实。
func (f *fixture) inspectRaw(t *testing.T, dockerID string) rawContainerInspect {
	t.Helper()
	run, err := f.runner.Run(
		f.ctx,
		"docker inspect",
		"docker inspect "+shellQuote(dockerID),
		planningOpTimeout,
	)
	if err != nil {
		t.Fatalf("读取容器原始 inspect 失败: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("docker inspect 退出码 %d: %s", run.ExitCode, strings.TrimSpace(run.Stderr))
	}
	parsed, err := parseRawContainerInspect(run.Stdout)
	if err != nil {
		t.Fatalf("解析原始 inspect 失败: %v", err)
	}
	return parsed
}

// shellQuote 对命令行参数做单引号转义。
//
// 命令由测试构造并只包含服务端生成的标识，仍按固定规则转义，避免把外部内容拼进 shell。
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// remoteProcessAlive 通过独立控制连接检查容器内是否存在匹配的进程。
//
// 判定只依据进程命令行：`ps -eo args=` 自身的命令行不含待匹配的 Pi 参数，因此不会像
// `pgrep -f` 那样把探测命令自己当成目标进程。
func (f *fixture) remoteProcessAlive(t *testing.T, dockerID, pattern string) (bool, string) {
	t.Helper()
	command := "docker exec " + shellQuote(dockerID) +
		" sh -c " + shellQuote("ps -eo args=")
	run, err := f.runner.Run(f.ctx, "docker exec", command, planningOpTimeout)
	if err != nil {
		t.Fatalf("检查容器内进程失败: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("列取容器内进程失败（退出码 %d）: %s", run.ExitCode, strings.TrimSpace(run.Stderr))
	}
	matched := make([]string, 0, 2)
	for _, line := range strings.Split(run.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "ps -eo args=") {
			continue
		}
		if firstProcessField(trimmed) == pattern {
			matched = append(matched, trimmed)
		}
	}
	return len(matched) > 0, strings.Join(matched, " | ")
}

// firstProcessField 返回进程命令行中的第一个字段。
//
// 容器内的 `pi` 由 Node 包装脚本 exec 而成，命令行只有可执行文件名本身；按第一个字段
// 匹配可以避免把包含相同文本的其他进程误判为目标进程。
func firstProcessField(line string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// controlInspectCondition 通过控制连接读取容器生命周期判定。
func (f *fixture) controlInspectCondition(t *testing.T, dockerID string) docker.ContainerInspect {
	t.Helper()
	inspect, err := f.runner.InspectContainer(f.ctx, dockerID)
	if err != nil {
		t.Fatalf("控制连接读取容器状态失败: %v", err)
	}
	return inspect
}

// sessionRoleIDs 返回两个规划角色的持久化 Pi 会话标识。
func (f *fixture) sessionRoleIDs(t *testing.T, session domain.PlanningSession) (string, string) {
	t.Helper()
	current := f.reloadSession(t, session.ID)
	return current.AnalystSessionID, current.PlannerSessionID
}

// archivePath 返回角色归档文件路径。
func (f *fixture) archivePath(sessionID, roleSessionID string) string {
	return path.Join(
		f.sessionRoot,
		"planning",
		sessionID,
		roleSessionID,
		"session.jsonl",
	)
}

// planningSnapshotRelease 用生产服务释放规划运行环境。
func (f *fixture) release(t *testing.T, session domain.PlanningSession) app.ReleasePlanningRuntimeResult {
	t.Helper()
	result, err := f.runtime.Release(f.ctx, app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: session.ID.String(),
	})
	if err != nil {
		t.Fatalf("释放规划运行环境失败: %v", err)
	}
	return result
}

// requireNoContainer 核验远端容器已不存在。
func (f *fixture) requireNoContainer(t *testing.T, dockerID string) {
	t.Helper()
	_, err := f.runner.InspectContainer(f.ctx, dockerID)
	if err == nil {
		t.Fatalf("规划容器 %s 仍存在于远端", dockerID)
	}
	if docker.ErrorCodeOf(err) != docker.CodeContainerNotFound {
		t.Fatalf("无法确认规划容器 %s 是否已删除: %v", dockerID, err)
	}
}

// requireVolumeAbsent 核验快照卷已不存在。
func (f *fixture) requireVolumeAbsent(t *testing.T, name string) {
	t.Helper()
	if err := verifyVolumeAbsent(f.ctx, f.runner, name); err != nil {
		t.Fatalf("核验快照卷 %s 失败: %v", name, err)
	}
}

// requireNoPrepContainer 核验一次性准备容器已不存在。
func (f *fixture) requireNoPrepContainer(t *testing.T, session domain.PlanningSession) {
	t.Helper()
	name := "agentflow-planprep-" + session.ID.String() + "-" + f.host.ID.String()
	_, err := f.runner.InspectContainer(f.ctx, name)
	if err == nil {
		t.Fatalf("一次性准备容器 %s 仍存在于远端", name)
	}
	if docker.ErrorCodeOf(err) != docker.CodeContainerNotFound {
		t.Fatalf("无法确认一次性准备容器 %s 是否已删除: %v", name, err)
	}
}

// errSkipNotApplicable 表示当前环境无法覆盖某项验收。
var errSkipNotApplicable = errors.New("当前真实环境无法覆盖该验收项")

// planningRoleIDFor 返回规划角色类型对应的固定角色标识。
//
// 生产侧的映射只在 app.planningRoleID 中实现一次；测试直接使用领域固定标识，
// 避免在测试里复制业务映射规则。
func planningRoleIDFor(t *testing.T, roleType domain.RoleType) string {
	t.Helper()
	switch roleType {
	case domain.RoleTypeRequirementAnalyst:
		return domain.RoleIDRequirementAnalyst.String()
	case domain.RoleTypePlanner:
		return domain.RoleIDPlanner.String()
	default:
		t.Fatalf("不是规划角色类型: %s", roleType)
		return ""
	}
}
