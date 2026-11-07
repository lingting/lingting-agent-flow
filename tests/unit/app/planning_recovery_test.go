package app_test

// 本文件验证规划调用被中断后的容器重启与历史 Session 恢复。
//
// 用例经过真实的 PlanningAgentService、PlanningRuntimeService、ContainerService 与容器
// 状态机，只有 SSH 连接与远程宿主机由受控替身提供。因此"同一个容器被重新启动""历史
// Session 先恢复再调用模型"这类顺序断言来自真实调用链，而不是替身的默认行为。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// planningRecoveryFixture 是规划调用中断与恢复的测试现场。
type planningRecoveryFixture struct {
	*remoteContainerFixture
	// roles 是固定角色配置读取入口。
	roles *app.RoleService
	// coordination 是本次装配使用的会话协调器。
	coordination *app.PlanningSessionCoordinator
	// runtime 是规划运行时服务。
	runtime *app.PlanningRuntimeService
	// planningAgent 是被测的规划 Agent 服务。
	planningAgent *app.PlanningAgentService
}

// newPlanningRecoveryFixture 构造规划调用中断与恢复的测试现场。
func newPlanningRecoveryFixture(t *testing.T) *planningRecoveryFixture {
	return newPlanningRecoveryFixtureWithStore(t, nil)
}

// newPlanningRecoveryFixtureWithStore 构造可注入存储故障的规划恢复测试现场。
//
// wrap 非空时服务层拿到的存储入口会被包装：用例可以让容器记录写入失败，再恢复写入条件，
// 从而构造"远端已停止但本地状态没写进去"的真实恢复现场。
func newPlanningRecoveryFixtureWithStore(
	t *testing.T,
	wrap func(storage.Store) storage.Store,
) *planningRecoveryFixture {
	t.Helper()
	base := newRemoteContainerFixtureWithStore(t, wrap)
	if _, err := base.db.SeedRoles(base.ctx, app.RoleSeeds()); err != nil {
		t.Fatalf("初始化固定角色失败: %v", err)
	}
	roles, err := app.NewRoleService(base.store)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	fixture := &planningRecoveryFixture{
		remoteContainerFixture: base,
		roles:                  roles,
	}
	fixture.buildPlanningServices(t)
	t.Cleanup(func() {
		if fixture.planningAgent == nil {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := fixture.planningAgent.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止规划 Agent 服务失败: %v", stopErr)
		}
	})
	return fixture
}

// buildPlanningServices 按当前存储、容器服务与会话协调器构造规划服务实例。
func (f *planningRecoveryFixture) buildPlanningServices(t *testing.T) {
	t.Helper()
	f.coordination = app.NewPlanningSessionCoordinator()
	runtime, err := app.NewPlanningRuntimeService(app.PlanningRuntimeServiceOptions{
		Store:        f.store,
		Containers:   f.containers,
		Coordination: f.coordination,
	})
	if err != nil {
		t.Fatalf("构造规划运行时服务失败: %v", err)
	}
	planningAgent, err := app.NewPlanningAgentService(app.PlanningAgentServiceOptions{
		Store:        f.store,
		Roles:        f.roles,
		Containers:   f.containers,
		Runtime:      runtime,
		Coordination: f.coordination,
		SessionRoot:  f.workspace.SessionRoot(),
	})
	if err != nil {
		t.Fatalf("构造规划 Agent 服务失败: %v", err)
	}
	if err := planningAgent.Start(f.ctx); err != nil {
		t.Fatalf("启动规划 Agent 服务失败: %v", err)
	}
	f.runtime = runtime
	f.planningAgent = planningAgent
}

// restartPlanningServices 丢弃进程内的规划服务实例并按同一数据库重建，用于模拟 Server 重启。
func (f *planningRecoveryFixture) restartPlanningServices(t *testing.T) {
	t.Helper()
	if f.planningAgent != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := f.planningAgent.Stop(stopCtx); err != nil {
			t.Fatalf("停止规划 Agent 服务失败: %v", err)
		}
		f.planningAgent = nil
	}
	f.buildPlanningServices(t)
}

// seedPlanningTarget 写入规划会话、运行中的规划容器与已固定的快照卷。
func (f *planningRecoveryFixture) seedPlanningTarget(t *testing.T) (domain.PlanningSession, domain.Container) {
	t.Helper()
	now := time.Now().UTC()
	session, err := domain.NewPlanningSession(f.project.ID, now)
	if err != nil {
		t.Fatalf("构造规划会话失败: %v", err)
	}
	if err := session.BeginAnalysis(now); err != nil {
		t.Fatalf("规划会话进入 analyzing 失败: %v", err)
	}
	container := f.seedContainer(session.ID, domain.ContainerStateRunning)
	hostID := f.node.ID
	if err := session.BindRuntime(&hostID, container.ID.String(), containerTestCommit, now); err != nil {
		t.Fatalf("绑定规划运行环境失败: %v", err)
	}
	if err := f.db.CreatePlanningSession(f.ctx, session); err != nil {
		t.Fatalf("写入规划会话失败: %v", err)
	}
	f.host.trackVolume(f.volumeName(session.ID), session.ID, f.node.ID)
	return session, container
}

// volumeName 返回会话与测试节点推导出的快照卷名称。
func (f *planningRecoveryFixture) volumeName(sessionID domain.ID) string {
	return "agentflow-plan-" + sessionID.String() + "-" + f.node.ID.String()
}

// roleRequest 构造一次需求分析师角色调用请求。
func (f *planningRecoveryFixture) roleRequest(session domain.PlanningSession) app.RunPlanningRoleRequest {
	return app.RunPlanningRoleRequest{
		PlanningSessionID: session.ID.String(),
		RoleType:          string(domain.RoleTypeRequirementAnalyst),
		Prompt:            "分析这次需求",
	}
}

// containerCount 返回数据库中的容器登记记录数量。
func (f *remoteContainerFixture) containerCount() int {
	f.t.Helper()
	containers, err := f.db.ListContainers(f.ctx, domain.ContainerFilter{})
	if err != nil {
		f.t.Fatalf("查询容器记录失败: %v", err)
	}
	return len(containers)
}

// readLocalFile 读取本机文件内容。
func readLocalFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取本机文件失败: %s: %v", path, err)
	}
	return string(content)
}

// requireContainerBindings 断言规划会话的运行时绑定保持不变。
func (f *planningRecoveryFixture) requireContainerBindings(
	t *testing.T,
	session domain.PlanningSession,
	container domain.Container,
) {
	t.Helper()
	reloaded := f.loadSession(session.ID)
	if reloaded.HostID == nil || *reloaded.HostID != f.node.ID {
		t.Fatalf("规划会话的执行节点绑定被改写: %v", reloaded.HostID)
	}
	if reloaded.ContainerID != container.ID.String() {
		t.Fatalf("规划会话的容器绑定被改写: %s", reloaded.ContainerID)
	}
	if reloaded.SourceCommit != containerTestCommit {
		t.Fatalf("规划会话的固定 commit 被改写: %s", reloaded.SourceCommit)
	}
}

// TestPlanningRoleRestartsSameContainerAfterInterruptedCall 验证中断后按原容器重启并恢复历史 Session。
func TestPlanningRoleRestartsSameContainerAfterInterruptedCall(t *testing.T) {
	t.Parallel()
	f := newPlanningRecoveryFixture(t)
	session, container := f.seedPlanningTarget(t)

	// 第一轮正常完成：建立本机最后一份完整稳定 Session 归档。
	first, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	requireNoError(t, err, "首轮规划调用应当成功")
	if first.Result.Status != agent.RunCompleted {
		t.Fatalf("首轮执行状态应为 completed，实际 %s", first.Result.Status)
	}
	if first.Container.ID != container.ID {
		t.Fatalf("首轮应使用登记容器 %s，实际 %s", container.ID, first.Container.ID)
	}
	if !first.Result.StableSession.Archived {
		t.Fatalf("首轮应建立稳定 Session 归档: %+v", first.Result.StableSession)
	}
	archivePath := first.ArchivePath
	if content := readLocalFile(t, archivePath); content != containerTestSessionJSONL {
		t.Fatalf("本机归档内容不符: %q", content)
	}

	// 第二轮：Pi 已经启动但结果无法确认 → 停止独占规划容器。
	f.host.piCancelled = true
	interrupted, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	if err == nil {
		t.Fatal("结果不确定的规划调用必须返回错误")
	}
	if !interrupted.Result.RemoteTerminationConfirmed {
		t.Fatalf("必须确认远端终止: %s", interrupted.Result.RemoteTerminationError)
	}
	if interrupted.Result.Status != agent.RunCancelled {
		t.Fatalf("执行状态应保持真实的 cancelled，实际 %s", interrupted.Result.Status)
	}
	if f.host.isRunning(container.DockerID) {
		t.Fatal("规划容器应当已经停止")
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateStopped {
		t.Fatalf("容器记录状态应为 stopped，实际 %s", stored.State)
	}
	// 停止容器只中断本轮执行：绑定、快照与最后一份完整归档都必须保留。
	f.requireContainerBindings(t, session, container)
	if content := readLocalFile(t, archivePath); content != containerTestSessionJSONL {
		t.Fatalf("中断的调用不得覆盖最后一份完整归档: %q", content)
	}
	if count := f.containerCount(); count != 1 {
		t.Fatalf("不得创建第二个规划容器，实际容器记录 %d 条", count)
	}

	// 第三轮：同一会话再次调用 → 重启同一个容器并先恢复历史 Session。
	f.host.piCancelled = false
	restored, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	requireNoError(t, err, "恢复后的规划调用应当成功")
	if restored.Container.ID != container.ID {
		t.Fatalf("必须复用原容器 %s，实际 %s", container.ID, restored.Container.ID)
	}
	if !restored.Result.StableSession.Restored {
		t.Fatalf("重启后必须从本机归档恢复历史 Session: %+v", restored.Result.StableSession)
	}
	if count := f.containerCount(); count != 1 {
		t.Fatalf("重启后不得创建第二个规划容器，实际容器记录 %d 条", count)
	}
	if !f.host.isRunning(container.DockerID) {
		t.Fatal("重启后容器应重新处于运行状态")
	}
	if reloaded := f.loadContainer(container.ID); reloaded.State != domain.ContainerStateRunning {
		t.Fatalf("重启后容器记录状态应为 running，实际 %s", reloaded.State)
	}
	f.requireContainerBindings(t, session, container)
	if content := readLocalFile(t, archivePath); content != containerTestSessionJSONL {
		t.Fatalf("重启前后的稳定归档必须一致: %q", content)
	}
	// 恢复顺序：首轮没有历史 Session，之后每轮都必须先恢复到容器内再调用模型。
	present := f.host.printSessionStates()
	if len(present) != 3 {
		t.Fatalf("应当只发生三次 Pi 主调用，实际 %d 次", len(present))
	}
	if present[0] {
		t.Fatal("首轮调用前不应存在历史 Session")
	}
	if !present[1] || !present[2] {
		t.Fatalf("后续每轮必须先恢复历史 Session 再调用模型: %v", present)
	}
}

// TestPlanningRoleDoesNotRetryAfterUncertainTermination 验证终止结果不确定时不重试且不影响其他会话。
func TestPlanningRoleDoesNotRetryAfterUncertainTermination(t *testing.T) {
	t.Parallel()
	f := newPlanningRecoveryFixture(t)
	sessionA, containerA := f.seedPlanningTarget(t)
	sessionB, containerB := f.seedPlanningTarget(t)
	f.host.piCancelled = true
	f.host.stopFailure = true
	f.host.inspectAfterStopFailure = true
	result, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(sessionA))
	if err == nil {
		t.Fatal("终止结果不确定时规划调用必须返回错误")
	}
	if result.Result.RemoteTerminationConfirmed {
		t.Fatal("无法确认停止时不得报告远端终止已确认")
	}
	if strings.TrimSpace(result.Result.RemoteTerminationError) == "" {
		t.Fatal("无法确认停止时必须给出可诊断的终止错误")
	}
	// 不自动重试：本轮只启动了一次 Pi。
	if prints := f.host.printSessionStates(); len(prints) != 1 {
		t.Fatalf("终止结果不确定时不得重试 Pi，实际主调用 %d 次", len(prints))
	}
	// 定位信息保留：不虚构 stopped，也不清空运行时绑定。
	storedA := f.loadContainer(containerA.ID)
	if storedA.State == domain.ContainerStateStopped {
		t.Fatal("无法确认停止时不得把容器标记为已停止")
	}
	f.requireContainerBindings(t, sessionA, containerA)
	// 另一个规划会话不受影响。
	if !f.host.isRunning(containerB.DockerID) {
		t.Fatal("另一个规划会话的容器不得被停止")
	}
	if storedB := f.loadContainer(containerB.ID); storedB.State != domain.ContainerStateRunning {
		t.Fatalf("另一个规划会话的容器状态被改写: %s", storedB.State)
	}
	if calls := f.host.countCommands(" docker stop "); calls != 1 {
		t.Fatalf("只应尝试停止本会话的容器一次，实际 %d 次", calls)
	}
	if sessionB.ContainerID != containerB.ID.String() {
		t.Fatalf("另一个规划会话的容器绑定被改写: %s", sessionB.ContainerID)
	}
}

// TestPlanningRuntimePrepareRestartsStoppedContainer 验证 Prepare 按原容器 ID 重新启动已停止的规划容器。
func TestPlanningRuntimePrepareRestartsStoppedContainer(t *testing.T) {
	t.Parallel()
	f := newPlanningRecoveryFixture(t)
	session, container := f.seedPlanningTarget(t)
	// 模拟上一轮规划调用被中断：容器记录与远程状态都已停止。
	requireNoError(t, f.markContainerStopped(container), "把容器记录置为已停止应当成功")

	detail, err := f.runtime.Prepare(f.ctx, app.PreparePlanningRuntimeRequest{
		PlanningSessionID: session.ID.String(),
		HostID:            f.node.ID.String(),
	})
	requireNoError(t, err, "准备运行环境应当重新启动已停止的规划容器")
	if !detail.ContainerReused {
		t.Fatalf("已登记的容器必须被复用: %+v", detail)
	}
	if detail.Container.ID != container.ID {
		t.Fatalf("必须复用原容器 %s，实际 %s", container.ID, detail.Container.ID)
	}
	if count := f.containerCount(); count != 1 {
		t.Fatalf("不得创建第二个规划容器，实际容器记录 %d 条", count)
	}
	if !f.host.isRunning(container.DockerID) {
		t.Fatal("Prepare 之后容器应重新处于运行状态")
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("容器记录状态应为 running，实际 %s", stored.State)
	}
	// 重启不改变运行时绑定，也不重新克隆快照。
	f.requireContainerBindings(t, session, container)
}

// TestPlanningRuntimePrepareRefusesUnstableContainer 验证暂停、重启中的规划容器不会被自动启动。
//
// paused 与 restarting 容器同样报告 State.Running=true，因此"没有在运行"必须由统一状态判定给出；
// 拒绝时不得创建第二个容器、不得改写运行时绑定，也不得提交 docker start。
func TestPlanningRuntimePrepareRefusesUnstableContainer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		state   domain.DockerContainerState
		running bool
	}{
		{name: "远端暂停", state: domain.DockerStatePaused, running: true},
		{name: "远端重启中", state: domain.DockerStateRestarting, running: true},
		{name: "状态字段矛盾", state: domain.DockerStateCreated, running: true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newPlanningRecoveryFixture(t)
			session, container := f.seedPlanningTarget(t)
			f.host.setDockerState(container.DockerID, testCase.state, testCase.running)

			_, err := f.runtime.Prepare(f.ctx, app.PreparePlanningRuntimeRequest{
				PlanningSessionID: session.ID.String(),
				HostID:            f.node.ID.String(),
			})
			if err == nil {
				t.Fatalf("远端容器处于 %s 且 Running=%t 时不得自动重启", testCase.state, testCase.running)
			}
			if !strings.Contains(err.Error(), "不在可自动恢复的退出状态") {
				t.Fatalf("错误应说明远端状态不允许自动重启，实际 %v", err)
			}
			if calls := f.host.countCommands(" docker start "); calls != 0 {
				t.Fatalf("拒绝重启时不得提交启动命令，实际 %d 次", calls)
			}
			if count := f.containerCount(); count != 1 {
				t.Fatalf("拒绝重启时不得创建第二个规划容器，实际容器记录 %d 条", count)
			}
			if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
				t.Fatalf("拒绝重启时不得改写管理状态，实际 %s", stored.State)
			}
			f.requireContainerBindings(t, session, container)
		})
	}
}

// requireRecoveredContainer 断言一次恢复沿用原容器、原绑定与最后一份完整归档。
func (f *planningRecoveryFixture) requireRecoveredContainer(
	t *testing.T,
	session domain.PlanningSession,
	container domain.Container,
	result app.PlanningAgentResult,
	archivePath string,
) {
	t.Helper()
	if result.Container.ID != container.ID {
		t.Fatalf("必须复用原容器 %s，实际 %s", container.ID, result.Container.ID)
	}
	if result.Container.DockerID != container.DockerID {
		t.Fatalf("必须使用原 Docker 容器 ID %s，实际 %s", container.DockerID, result.Container.DockerID)
	}
	if !result.Result.StableSession.Restored {
		t.Fatalf("恢复调用必须从本机归档恢复历史 Session: %+v", result.Result.StableSession)
	}
	if count := f.containerCount(); count != 1 {
		t.Fatalf("不得创建第二个规划容器，实际容器记录 %d 条", count)
	}
	if volumes := f.host.volumeCount(); volumes != 1 {
		t.Fatalf("快照卷只应保留一份，实际 %d 份", volumes)
	}
	if content := readLocalFile(t, archivePath); content != containerTestSessionJSONL {
		t.Fatalf("最后一份完整归档不得被中断调用覆盖: %q", content)
	}
	if !f.host.isRunning(container.DockerID) {
		t.Fatal("恢复后原容器应重新处于运行状态")
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("恢复后容器记录状态应为 running，实际 %s", stored.State)
	}
	f.requireContainerBindings(t, session, container)
}

// TestPlanningRoleConvergesStaleRunningStateAfterInterruptedCall 验证本地 running、远端 exited 时恢复原容器。
//
// 现场来自真实的"停止已生效但本地记录没写进去"：远端容器已退出，数据库记录仍是 running，
// 本机保留最后一份完整稳定 Session 归档。
func TestPlanningRoleConvergesStaleRunningStateAfterInterruptedCall(t *testing.T) {
	t.Parallel()
	f := newPlanningRecoveryFixture(t)
	session, container := f.seedPlanningTarget(t)

	first, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	requireNoError(t, err, "首轮规划调用应当成功")
	archivePath := first.ArchivePath
	if content := readLocalFile(t, archivePath); content != containerTestSessionJSONL {
		t.Fatalf("首轮归档内容不符: %q", content)
	}

	// 中断现场：远端容器已经退出，本地记录仍是 running。
	f.host.setRunning(container.DockerID, false)
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("中断现场要求本地记录仍为 running，实际 %s", stored.State)
	}

	restored, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	requireNoError(t, err, "本地 running、远端 exited 时应当收敛状态并恢复原容器")
	f.requireRecoveredContainer(t, session, container, restored, archivePath)
	if calls := f.host.countCommands(" docker start "); calls != 1 {
		t.Fatalf("应当只提交一次启动命令，实际 %d 次", calls)
	}
	if calls := f.host.countCommands(" docker stop "); calls != 0 {
		t.Fatalf("恢复过程不得提交停止命令，实际 %d 次", calls)
	}
	// 恢复顺序：第二轮 Pi 主调用之前容器内必须已经恢复历史 Session。
	present := f.host.printSessionStates()
	if len(present) != 2 {
		t.Fatalf("应当只发生两次 Pi 主调用，实际 %d 次", len(present))
	}
	if present[0] {
		t.Fatal("首轮调用前不应存在历史 Session")
	}
	if !present[1] {
		t.Fatal("恢复调用必须先恢复历史 Session 再调用模型")
	}
}

// TestPlanningRoleRecoversAfterStopStatePersistFailed 验证停止已确认但本地写入失败后的下一轮恢复。
func TestPlanningRoleRecoversAfterStopStatePersistFailed(t *testing.T) {
	t.Parallel()
	f := newPlanningRecoveryFixtureWithStore(t, func(inner storage.Store) storage.Store {
		return newFaultStore(inner)
	})
	if f.faults == nil {
		t.Fatal("存储故障注入未生效")
	}
	session, container := f.seedPlanningTarget(t)

	first, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	requireNoError(t, err, "首轮规划调用应当成功")
	archivePath := first.ArchivePath

	// 第二轮：Pi 结果不确定 → 远端容器已停止并核验，但本地容器记录写入失败。
	f.faults.failContainerUpdates(true)
	f.host.piCancelled = true
	interrupted, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	if err == nil {
		t.Fatal("结果不确定的规划调用必须返回错误")
	}
	if !interrupted.Result.RemoteTerminationConfirmed {
		t.Fatalf("必须确认远端终止: %s", interrupted.Result.RemoteTerminationError)
	}
	if strings.TrimSpace(interrupted.Result.RemoteTerminationError) == "" {
		t.Fatal("本地状态未能持久化时必须如实报告")
	}
	if f.host.isRunning(container.DockerID) {
		t.Fatal("远端容器应当已经停止")
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("写入失败时容器记录应保持 running，实际 %s", stored.State)
	}
	f.requireContainerBindings(t, session, container)

	// 恢复写入条件后再次调用：重新核验远端、收敛状态、恢复原容器与历史 Session。
	f.faults.failContainerUpdates(false)
	f.host.piCancelled = false
	restored, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	requireNoError(t, err, "恢复写入条件后规划调用应当成功")
	f.requireRecoveredContainer(t, session, container, restored, archivePath)
	if calls := f.host.countCommands(" docker stop "); calls != 1 {
		t.Fatalf("不得重复停止已经退出的容器，实际停止命令 %d 次", calls)
	}
	if calls := f.host.countCommands(" docker start "); calls != 1 {
		t.Fatalf("应当只提交一次启动命令，实际 %d 次", calls)
	}
}

// TestPlanningRoleConvergesAfterServerRestart 验证 Server 中断后的状态收敛。
//
// 进程内的规划服务实例全部重建、协调器重建、调用方上下文是新的：恢复仍然只能靠同一套远端
// 核验与状态收敛路径完成。
func TestPlanningRoleConvergesAfterServerRestart(t *testing.T) {
	t.Parallel()
	f := newPlanningRecoveryFixture(t)
	session, container := f.seedPlanningTarget(t)

	first, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	requireNoError(t, err, "首轮规划调用应当成功")
	archivePath := first.ArchivePath

	// 远端容器已经退出，数据库记录仍是 running，进程内服务实例全部消失。
	f.host.setRunning(container.DockerID, false)
	f.restartPlanningServices(t)

	restored, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	requireNoError(t, err, "Server 重启后应当通过同一套远端核验与收敛路径恢复")
	f.requireRecoveredContainer(t, session, container, restored, archivePath)
	if calls := f.host.countCommands(" docker start "); calls != 1 {
		t.Fatalf("应当只提交一次启动命令，实际 %d 次", calls)
	}
}

// TestPlanningRoleRefusesRecoveryOnOwnershipMismatch 验证所有权或规划标签不成立时不会恢复容器。
func TestPlanningRoleRefusesRecoveryOnOwnershipMismatch(t *testing.T) {
	t.Parallel()
	t.Run("标签不一致", func(t *testing.T) {
		t.Parallel()
		f := newPlanningRecoveryFixture(t)
		session, container := f.seedPlanningTarget(t)
		f.host.setRunning(container.DockerID, false)
		f.host.setContainerLabels(container.DockerID, map[string]string{
			docker.OwnershipLabelKey:       "true",
			docker.HostLabelKey:            f.node.ID.String(),
			docker.ContainerLabelKey:       container.ID.String(),
			docker.PlanningSessionLabelKey: domain.NewID().String(),
		})

		_, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
		if err == nil {
			t.Fatal("规划标签不一致时不得恢复容器")
		}
		if calls := f.host.countCommands(" docker start "); calls != 0 {
			t.Fatalf("拒绝恢复时不得提交启动命令，实际 %d 次", calls)
		}
		if f.host.isRunning(container.DockerID) {
			t.Fatal("拒绝恢复时容器不应被启动")
		}
	})
	t.Run("Docker 容器 ID 不一致", func(t *testing.T) {
		t.Parallel()
		f := newPlanningRecoveryFixture(t)
		session, container := f.seedPlanningTarget(t)
		f.host.setRunning(container.DockerID, false)
		f.host.inspectIDOverride = "docker-unexpected-" + container.ID.String()

		_, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
		if err == nil {
			t.Fatal("Docker 容器 ID 不一致时不得恢复容器")
		}
		if calls := f.host.countCommands(" docker start "); calls != 0 {
			t.Fatalf("拒绝恢复时不得提交启动命令，实际 %d 次", calls)
		}
	})
	t.Run("已脱离管理", func(t *testing.T) {
		t.Parallel()
		f := newPlanningRecoveryFixture(t)
		session, container := f.seedPlanningTarget(t)
		f.host.setRunning(container.DockerID, false)
		detached := container
		if err := detached.Detach(time.Now().UTC()); err != nil {
			t.Fatalf("把容器置为脱离管理失败: %v", err)
		}
		if err := f.db.UpdateContainer(f.ctx, detached); err != nil {
			t.Fatalf("更新容器记录失败: %v", err)
		}

		_, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
		if err == nil {
			t.Fatal("已脱离管理的容器不得被自动恢复")
		}
		if calls := f.host.countCommands(" docker start "); calls != 0 {
			t.Fatalf("拒绝恢复时不得提交启动命令，实际 %d 次", calls)
		}
		if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateDetached {
			t.Fatalf("拒绝恢复时不得改写容器状态，实际 %s", stored.State)
		}
	})
}

// TestPlanningRoleRefusesRecoveryOnUnconfirmedRemoteState 验证远端状态不确定时不会自动启动。
func TestPlanningRoleRefusesRecoveryOnUnconfirmedRemoteState(t *testing.T) {
	t.Parallel()
	unstable := []struct {
		name    string
		state   domain.DockerContainerState
		running bool
	}{
		{name: "远端暂停", state: domain.DockerStatePaused, running: true},
		{name: "远端暂停且 Running 为假", state: domain.DockerStatePaused, running: false},
		{name: "远端重启中", state: domain.DockerStateRestarting, running: true},
		{name: "远端 dead", state: domain.DockerStateDead, running: false},
		{name: "远端 removing", state: domain.DockerStateRemoving, running: false},
		{name: "状态字段矛盾", state: domain.DockerStateExited, running: true},
	}
	for _, testCase := range unstable {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newPlanningRecoveryFixture(t)
			session, container := f.seedPlanningTarget(t)
			f.host.setDockerState(container.DockerID, testCase.state, testCase.running)

			_, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
			if err == nil {
				t.Fatalf("远端容器处于 %s 且 Running=%t 时不得自动恢复", testCase.state, testCase.running)
			}
			if !strings.Contains(err.Error(), "不在可自动恢复的退出状态") {
				t.Fatalf("错误应说明远端状态不允许自动恢复，实际 %v", err)
			}
			if calls := f.host.countCommands(" docker start "); calls != 0 {
				t.Fatalf("拒绝恢复时不得提交启动命令，实际 %d 次", calls)
			}
			if calls := f.host.countCommands(" docker exec "); calls != 0 {
				t.Fatalf("拒绝恢复时不得发起 Pi 主调用，实际 %d 次", calls)
			}
			if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
				t.Fatalf("拒绝恢复时不得改写容器状态，实际 %s", stored.State)
			}
			if count := f.containerCount(); count != 1 {
				t.Fatalf("拒绝恢复时不得创建第二个规划容器，实际容器记录 %d 条", count)
			}
			f.requireContainerBindings(t, session, container)
		})
	}
	t.Run("远端状态无法确认", func(t *testing.T) {
		t.Parallel()
		f := newPlanningRecoveryFixture(t)
		session, container := f.seedPlanningTarget(t)
		f.host.setRunning(container.DockerID, false)
		f.host.setInspectUncertain(true)

		_, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
		if err == nil {
			t.Fatal("无法确认远端状态时不得恢复容器")
		}
		if calls := f.host.countCommands(" docker start "); calls != 0 {
			t.Fatalf("拒绝恢复时不得提交启动命令，实际 %d 次", calls)
		}
		if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
			t.Fatalf("拒绝恢复时不得改写容器状态，实际 %s", stored.State)
		}
	})
}

// TestPlanningRoleRecoveryHoldsSessionCoordinator 验证恢复与 Pi 调用都在会话协调器内完成。
//
// Pi 主命令执行期间另一处同会话操作必须排队：这说明状态收敛、容器启动、历史 Session 恢复与
// Pi 调用仍然覆盖在同一个 `PlanningSessionCoordinator` 临界区内。
func TestPlanningRoleRecoveryHoldsSessionCoordinator(t *testing.T) {
	t.Parallel()
	f := newPlanningRecoveryFixture(t)
	session, container := f.seedPlanningTarget(t)
	// 构造本地 running、远端 exited 的恢复现场。
	f.host.setRunning(container.DockerID, false)

	acquired := make(chan struct{})
	f.host.setPiPrintHook(func() {
		go func() {
			unlock := f.coordination.Lock(session.ID)
			unlock()
			close(acquired)
		}()
		select {
		case <-acquired:
			t.Error("恢复与 Pi 调用期间会话锁不得被其他同会话操作取得")
		case <-time.After(100 * time.Millisecond):
		}
	})

	restored, err := f.planningAgent.RunRole(f.ctx, f.roleRequest(session))
	requireNoError(t, err, "恢复现场下的规划调用应当成功")
	if restored.Container.ID != container.ID {
		t.Fatalf("必须复用原容器 %s，实际 %s", container.ID, restored.Container.ID)
	}
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("调用结束后同会话操作应当可以取得会话锁")
	}
}
