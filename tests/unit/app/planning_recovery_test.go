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
	"github.com/lingting/agentflow/internal/domain"
)

// planningRecoveryFixture 是规划调用中断与恢复的测试现场。
type planningRecoveryFixture struct {
	*remoteContainerFixture
	// roles 是固定角色配置读取入口。
	roles *app.RoleService
	// runtime 是规划运行时服务。
	runtime *app.PlanningRuntimeService
	// planningAgent 是被测的规划 Agent 服务。
	planningAgent *app.PlanningAgentService
}

// newPlanningRecoveryFixture 构造规划调用中断与恢复的测试现场。
func newPlanningRecoveryFixture(t *testing.T) *planningRecoveryFixture {
	t.Helper()
	base := newRemoteContainerFixture(t)
	if _, err := base.db.SeedRoles(base.ctx, app.RoleSeeds()); err != nil {
		t.Fatalf("初始化固定角色失败: %v", err)
	}
	roles, err := app.NewRoleService(base.db)
	if err != nil {
		t.Fatalf("构造角色服务失败: %v", err)
	}
	coordination := app.NewPlanningSessionCoordinator()
	runtime, err := app.NewPlanningRuntimeService(app.PlanningRuntimeServiceOptions{
		Store:        base.db,
		Containers:   base.containers,
		Coordination: coordination,
	})
	if err != nil {
		t.Fatalf("构造规划运行时服务失败: %v", err)
	}
	planningAgent, err := app.NewPlanningAgentService(app.PlanningAgentServiceOptions{
		Store:        base.db,
		Roles:        roles,
		Containers:   base.containers,
		Runtime:      runtime,
		Coordination: coordination,
		SessionRoot:  base.workspace.SessionRoot(),
	})
	if err != nil {
		t.Fatalf("构造规划 Agent 服务失败: %v", err)
	}
	if err := planningAgent.Start(base.ctx); err != nil {
		t.Fatalf("启动规划 Agent 服务失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := planningAgent.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止规划 Agent 服务失败: %v", stopErr)
		}
	})
	return &planningRecoveryFixture{
		remoteContainerFixture: base,
		roles:                  roles,
		runtime:                runtime,
		planningAgent:          planningAgent,
	}
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
