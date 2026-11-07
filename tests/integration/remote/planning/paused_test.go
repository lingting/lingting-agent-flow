package planning_remote

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestRemotePlanningPausedState 在真实 Docker 上验收 paused 状态的统一判定。
//
// 覆盖：真实 `docker pause` 后 inspect 字段（Status / Running / Paused）的原始取值、暂停
// 容器不可启动、不可执行、不可发起 Pi 调用、不可被复用时自动启动、观测不能被判定为运行中
// 或已退出，以及本地记录不会被错误写成 stopped。解除暂停后按正常路径释放资源。
func TestRemotePlanningPausedState(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	session := f.newSession(t, "暂停状态验收：只读取，不修改任何文件。")
	detail := f.prepare(t, session)
	defer f.release(t, session)

	f.pauseContainer(t, detail.Container.DockerID)
	raw := f.inspectRaw(t, detail.Container.DockerID)
	if raw.State.Status != "paused" {
		t.Fatalf("真实 Docker 状态不是 paused: %s", raw.State.Status)
	}
	if !raw.State.Paused {
		t.Fatalf("真实 Docker 未报告 State.Paused=true: %+v", raw.State)
	}
	t.Logf("PAUSED_INSPECT status=%s running=%t paused=%t",
		raw.State.Status, raw.State.Running, raw.State.Paused)
	if !raw.State.Running {
		// 不同引擎版本可能报告 Running=false；只有真实观测结果可以记录，不得伪造。
		t.Logf("PAUSED_RUNNING_FLAG=false（该引擎在暂停状态下不报告 Running=true）")
	}

	// 业务拒绝：启动、执行与规划调用都不得落在暂停容器上。
	assertStartRefused(t, f, detail.Container.ID)
	assertExecRefused(t, f, detail.Container.ID)
	assertRunRoleRefused(t, f, session, detail)
	assertPrepareRefused(t, f, session, detail)
	assertObservationIndeterminate(t, f, detail.Container.ID)
	assertLocalStateNotStopped(t, f, detail.Container.ID)

	// 暂停现场仍然必须能够通过真实 inspect 看到：禁止把状态改写成 running 或 stopped。
	rawAfter := f.inspectRaw(t, detail.Container.DockerID)
	if rawAfter.State.Status != "paused" {
		t.Fatalf("拒绝操作的路径改变了容器真实状态: %s", rawAfter.State.Status)
	}

	f.unpauseContainer(t, detail.Container.DockerID)
	f.requireContainerRunning(t, detail.Container.DockerID)
	t.Logf("PAUSED_STATE_OK container=%s", detail.Container.ID)
}

// TestRemotePlanningRestartingObservation 尝试真实观察 restarting 状态。
//
// 该用例只创建本次测试专用的短生命周期容器：重启窗口无法稳定捕获时如实报告"未观测"，
// 不得编造状态；dead 与 removing 同理需要真实现场，本用例只做记录。
func TestRemotePlanningRestartingObservation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	name := "agentflow-test-lifecycle-" + f.suffix
	planningHostContainerRun.Track(t, f.runner, testkit.TrackedContainer{Name: name})

	// 立即退出且总是重启的容器会短暂进入 restarting。
	command := "docker run -d --name " + shellQuote(name) +
		" --restart=always " + shellQuote(baseTestImage) +
		" sh -c " + shellQuote("exit 3")
	run, err := f.runner.Run(f.ctx, "docker run", command, planningOpTimeout)
	if err != nil || !run.Succeeded() {
		t.Fatalf("创建 restarting 观测容器失败: %v（退出码 %d）: %s",
			err, run.ExitCode, strings.TrimSpace(run.Stderr))
	}
	dockerID := strings.TrimSpace(run.Stdout)
	if dockerID == "" {
		t.Fatalf("创建 restarting 观测容器未返回容器 ID")
	}

	observed := false
	deadline := time.Now().Add(20 * time.Second)
	var lastStatus string
	var lastRunning bool
	for time.Now().Before(deadline) {
		inspect, inspectErr := f.runner.InspectContainer(f.ctx, dockerID)
		if inspectErr != nil {
			t.Fatalf("读取观测容器状态失败: %v", inspectErr)
		}
		lastStatus = inspect.State.String()
		lastRunning = inspect.Running
		if inspect.State == domain.DockerStateRestarting {
			observed = true
			t.Logf("RESTARTING_OBSERVED status=%s running=%t condition=%s",
				inspect.State, inspect.Running, inspect.RuntimeCondition())
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !observed {
		t.Logf(
			"RESTARTING_NOT_OBSERVED last_status=%s last_running=%t（重启窗口无法稳定捕获，如实报告未观测）",
			lastStatus,
			lastRunning,
		)
	}
	t.Logf("DEAD_NOT_OBSERVED（未制造 dead 现场，如实报告未观测）")
	t.Logf("REMOVING_NOT_OBSERVED（未制造 removing 现场，如实报告未观测）")

	// 清理本次观测容器：只删除本用例创建的短生命周期容器，不使用全局 prune。
	if _, removeErr := f.runner.RemoveContainer(f.ctx, dockerID); removeErr != nil &&
		docker.ErrorCodeOf(removeErr) != docker.CodeContainerNotFound {
		t.Fatalf("删除 restarting 观测容器失败: %v", removeErr)
	}
}

// pauseContainer 用真实 docker pause 暂停容器。
func (f *fixture) pauseContainer(t *testing.T, dockerID string) {
	t.Helper()
	run, err := f.runner.Run(f.ctx, "docker pause",
		"docker pause "+shellQuote(dockerID), planningOpTimeout)
	if err != nil || !run.Succeeded() {
		t.Fatalf("暂停容器失败: %v（退出码 %d）: %s",
			err, run.ExitCode, strings.TrimSpace(run.Stderr))
	}
}

// unpauseContainer 用真实 docker unpause 解除暂停。
func (f *fixture) unpauseContainer(t *testing.T, dockerID string) {
	t.Helper()
	run, err := f.runner.Run(f.ctx, "docker unpause",
		"docker unpause "+shellQuote(dockerID), planningOpTimeout)
	if err != nil || !run.Succeeded() {
		t.Fatalf("解除暂停失败: %v（退出码 %d）: %s",
			err, run.ExitCode, strings.TrimSpace(run.Stderr))
	}
}

// assertStartRefused 断言暂停容器不会被自动启动。
func assertStartRefused(t *testing.T, f *fixture, containerID domain.ID) {
	t.Helper()
	before := f.inspectRaw(t, f.reloadContainer(t, containerID).DockerID)
	if _, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: containerID.String(),
	}); err == nil {
		t.Fatalf("暂停容器被 StartContainer 接受")
	} else {
		t.Logf("START_REFUSED err=%v", truncate(err.Error(), 300))
	}
	after := f.inspectRaw(t, f.reloadContainer(t, containerID).DockerID)
	if after.State.Status != "paused" {
		t.Fatalf("拒绝启动后容器状态被改变: %s → %s", before.State.Status, after.State.Status)
	}
}

// assertExecRefused 断言暂停容器不执行容器内命令。
func assertExecRefused(t *testing.T, f *fixture, containerID domain.ID) {
	t.Helper()
	probe := "/tmp/paused-exec-probe-" + randomSuffix(t)
	if _, err := f.containers.Exec(f.ctx, app.ExecRequest{
		ContainerID: containerID.String(),
		Command:     []string{"sh", "-c", "touch " + probe},
		WorkDir:     planningWorkDir,
		Timeout:     planningOpTimeout,
	}); err == nil {
		t.Fatalf("暂停容器接受了容器内命令执行")
	} else {
		t.Logf("EXEC_REFUSED err=%v", truncate(err.Error(), 300))
	}
}

// assertRunRoleRefused 断言暂停容器不会发起 Pi 调用。
func assertRunRoleRefused(
	t *testing.T,
	f *fixture,
	session domain.PlanningSession,
	detail app.PlanningRuntimeDetail,
) {
	t.Helper()
	_, err := f.runRole(t, session, domain.RoleTypeRequirementAnalyst, "只回复 OK。")
	if err == nil {
		t.Fatalf("暂停容器接受了规划角色调用")
	}
	t.Logf("RUN_ROLE_REFUSED err=%v", truncate(err.Error(), 400))
	// 暂停容器无法执行任何命令，因此没有 Pi 进程是因为容器本身不可执行；
	// 这里用控制连接核验容器状态与进程现场，不把"无法 exec"当成"进程已退出"。
	f.requirePiProcessGone(t, detail.Container.DockerID)
}

// assertPrepareRefused 断言复用暂停容器时不会自动启动它。
func assertPrepareRefused(
	t *testing.T,
	f *fixture,
	session domain.PlanningSession,
	detail app.PlanningRuntimeDetail,
) {
	t.Helper()
	_, err := f.runtime.Prepare(f.ctx, app.PreparePlanningRuntimeRequest{
		PlanningSessionID: session.ID.String(),
		HostID:            f.host.ID.String(),
	})
	if err == nil {
		t.Fatalf("Prepare 接受了暂停容器")
	}
	if !errors.Is(err, app.ErrPlanningRuntimeConflict) {
		t.Fatalf("暂停容器的 Prepare 应返回冲突类别，实际: %v", err)
	}
	t.Logf("PREPARE_REFUSED err=%v", truncate(err.Error(), 400))
	f.assertBindingIntact(t, session, detail)
	raw := f.inspectRaw(t, detail.Container.DockerID)
	if raw.State.Status != "paused" {
		t.Fatalf("Prepare 拒绝后容器状态被改变: %s", raw.State.Status)
	}
}

// assertObservationIndeterminate 断言观测结论既不是运行中也不是已退出。
func assertObservationIndeterminate(t *testing.T, f *fixture, containerID domain.ID) {
	t.Helper()
	record := f.reloadContainer(t, containerID)
	detail, err := f.containers.InspectContainer(f.ctx, app.InspectContainerRequest{
		ContainerID: containerID.String(),
		Sync:        false,
	})
	if err != nil {
		t.Fatalf("核验暂停容器失败: %v", err)
	}
	observation, observedDockerID := app.ClassifyContainerObservation(record, detail, nil)
	if observation == domain.ContainerObservedRunning {
		t.Fatalf("暂停容器被判定为运行中")
	}
	if observation == domain.ContainerObservedStopped {
		t.Fatalf("暂停容器被判定为已退出")
	}
	if observation != domain.ContainerObservedIndeterminate {
		t.Fatalf("暂停容器的观测结论应为 indeterminate，实际 %s", observation)
	}
	if observedDockerID != "" {
		t.Fatalf("观测不应补写 Docker 容器 ID: %s", observedDockerID)
	}
	t.Logf("OBSERVATION_CLASS=%s", observation)
}

// assertLocalStateNotStopped 断言本地记录没有被错误写成 stopped。
func assertLocalStateNotStopped(t *testing.T, f *fixture, containerID domain.ID) {
	t.Helper()
	record := f.reloadContainer(t, containerID)
	if record.State == domain.ContainerStateStopped {
		t.Fatalf("暂停容器被错误记录为 stopped")
	}
	t.Logf("LOCAL_STATE_AFTER_REFUSAL state=%s docker_state=%s", record.State, record.DockerState)
}

// TestRemotePlanningPreconditions 记录本次真实验收观测到的环境事实。
//
// 它不创建规划资源，只输出 Docker 版本、测试镜像与容器内 Pi 版本、模型标识与项目固定
// commit，供验收报告引用；容器内 Pi 是否存在必须由容器内实际检查确认，不能由本机判断。
func TestRemotePlanningPreconditions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	preflight := dockerPreflight(t, f)
	t.Logf(
		"REMOTE_DOCKER CLI=%s ENGINE=%s API=%s MIN_API=%s",
		preflight.CLIVersion,
		preflight.EngineVersion,
		preflight.APIVersion,
		preflight.MinAPIVersion,
	)
	t.Logf("TEST_IMAGE=%s", f.image)
	t.Logf("PI_MODEL=%s", f.model)
	t.Logf("PI_THINKING=%s", f.dockerCfg.Pi.ThinkingLevel)
	t.Logf("PROJECT_REPOSITORY=%s BRANCH=%s", f.project.RepositoryURL, f.project.NormalizedBaseBranch())

	// 容器内 Pi 版本：创建观察用一次性容器（可写 rootfs）后查询，不涉及规划资源。
	name := "agentflow-test-pi-version-" + f.suffix
	planningHostContainerRun.Track(t, f.runner, testkit.TrackedContainer{Name: name})
	create := "docker run -d --name " + shellQuote(name) + " " + shellQuote(baseTestImage) +
		" sh -c " + shellQuote("sleep infinity")
	run, err := f.runner.Run(f.ctx, "docker run", create, planningOpTimeout)
	if err != nil || !run.Succeeded() {
		t.Fatalf("创建版本检查容器失败: %v（退出码 %d）: %s",
			err, run.ExitCode, strings.TrimSpace(run.Stderr))
	}
	dockerID := strings.TrimSpace(run.Stdout)
	version := f.execRaw(t, dockerID, []string{"pi", "--version"})
	t.Logf("CONTAINER_PI_VERSION=%s", version)
	gitVersion := f.execRaw(t, dockerID, []string{"git", "--version"})
	t.Logf("CONTAINER_GIT_VERSION=%s", gitVersion)
	if _, removeErr := f.runner.RemoveContainer(f.ctx, dockerID); removeErr != nil &&
		docker.ErrorCodeOf(removeErr) != docker.CodeContainerNotFound {
		t.Fatalf("删除版本检查容器失败: %v", removeErr)
	}
	t.Logf("HOST_PI_PRESENT=%t（本机 Pi 是否存在与容器内无关）", hostPiPresent())
}

// dockerPreflight 通过生产环境服务取得真实 Docker 版本信息。
func dockerPreflight(t *testing.T, f *fixture) docker.Preflight {
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
	result, err := environments.Init(f.ctx, app.InitOptions{MachineID: f.host.ID.String()})
	if err != nil {
		t.Fatalf("初始化远程环境失败: %v", err)
	}
	if !result.Preflight.Ready() {
		t.Fatalf("远程 Docker 预检未通过(%s): %s",
			result.Preflight.ErrorCode, result.Preflight.Detail)
	}
	return result.Preflight
}

// execRaw 用真实 docker exec 在容器内执行一条命令并返回标准输出。
//
// 这里使用控制连接上的原始 docker exec 而不是生产容器服务：版本检查容器不是受管理容器，
// 生产服务会拒绝在未登记容器上执行命令。
func (f *fixture) execRaw(t *testing.T, dockerID string, command []string) string {
	t.Helper()
	args := append([]string{"exec", dockerID}, command...)
	commandLine, err := sshclient.ShellCommand("docker", args...)
	if err != nil {
		t.Fatalf("构造容器内命令失败: %v", err)
	}
	run, runErr := f.runner.Run(f.ctx, "docker exec", commandLine, planningOpTimeout)
	if runErr != nil || !run.Succeeded() {
		t.Fatalf("容器内命令 %v 失败: %v（退出码 %d）: %s",
			command, runErr, run.ExitCode, strings.TrimSpace(run.Stderr))
	}
	return strings.TrimSpace(run.Stdout)
}

// hostPiPresent 报告本机是否存在 Pi CLI。
//
// 本机 Pi 是否存在与容器内是否可用无关：真实验收只以容器内检查结果为准。
func hostPiPresent() bool {
	path, err := exec.LookPath("pi")
	return err == nil && strings.TrimSpace(path) != ""
}
