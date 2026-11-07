package app_test

// 本文件验证容器服务的受控停止语义。
//
// 所有用例都经过真实的 ContainerService、真实的 docker.Runner 命令构造与真实的容器
// 状态机；只有 SSH 连接与远程宿主机由本包的受控替身提供。因此"停止成功"必须由停止后
// 的实际检查结果证明，而不是由替身默认给出。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// TestContainerServiceStopConfirmsVerifiedState 验证停止成功后本地记录与远程清单都按核验结果更新。
func TestContainerServiceStopConfirmsVerifiedState(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	detail, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "停止受管理容器应当成功")
	if detail.Container.State != domain.ContainerStateStopped {
		t.Fatalf("容器管理状态应为 stopped，实际 %s", detail.Container.State)
	}
	if detail.Container.DockerState != domain.DockerStateExited {
		t.Fatalf("Docker 状态应同步为 exited，实际 %s", detail.Container.DockerState)
	}
	if detail.Inspect == nil || detail.Inspect.Running {
		t.Fatalf("停止后应当取得未运行的检查结果，实际 %+v", detail.Inspect)
	}
	if f.host.isRunning(container.DockerID) {
		t.Fatal("远程容器应当已经停止")
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateStopped {
		t.Fatalf("持久化的容器状态应为 stopped，实际 %s", stored.State)
	}
	if stored.DockerState != domain.DockerStateExited {
		t.Fatalf("持久化的 Docker 状态应为 exited，实际 %s", stored.DockerState)
	}
	if calls := f.host.countCommands(" docker stop "); calls != 1 {
		t.Fatalf("应当只提交一次停止命令，实际 %d", calls)
	}
	if registry := f.registryContent(); !strings.Contains(registry, `"state": "stopped"`) {
		t.Fatalf("远程登记清单未同步停止状态: %s", registry)
	}
}

// TestContainerServiceStopAlreadyStoppedConverges 验证已停止的容器按核验结果幂等收敛。
func TestContainerServiceStopAlreadyStoppedConverges(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setRunning(container.DockerID, false)
	requireNoError(t, f.markContainerStopped(container), "把容器记录置为已停止应当成功")
	detail, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "停止已停止的容器应当按核验结果成功")
	if detail.Container.State != domain.ContainerStateStopped {
		t.Fatalf("容器管理状态应为 stopped，实际 %s", detail.Container.State)
	}
	if calls := f.host.countCommands(" docker stop "); calls != 0 {
		t.Fatalf("已停止的容器不应再提交停止命令，实际 %d 次", calls)
	}
}

// TestContainerServiceStopConvergesWhenStopCommandUncertain 验证停止命令结果不确定但实际已停止时按事实收敛。
func TestContainerServiceStopConvergesWhenStopCommandUncertain(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.stopFailure = true
	f.host.stopApplies = true
	detail, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "停止命令报错但容器确实停止时应当收敛为成功")
	if detail.Container.State != domain.ContainerStateStopped {
		t.Fatalf("容器管理状态应为 stopped，实际 %s", detail.Container.State)
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateStopped {
		t.Fatalf("持久化的容器状态应为 stopped，实际 %s", stored.State)
	}
}

// TestContainerServiceStopReportsContainerStillRunning 验证停止后仍在运行的容器不会被标记为已停止。
func TestContainerServiceStopReportsContainerStillRunning(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.stopIgnored = true
	detail, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
		ContainerID: container.ID.String(),
	})
	if err == nil {
		t.Fatal("容器仍在运行时停止必须失败")
	}
	if !strings.Contains(err.Error(), "仍在运行") {
		t.Fatalf("错误应说明容器仍在运行，实际 %v", err)
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		t.Fatalf("应当返回仍在运行的检查结果，实际 %+v", detail.Inspect)
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateRunning {
		t.Fatalf("容器仍在运行时不得改写管理状态，实际 %s", stored.State)
	}
}

// TestContainerServiceStopMarksUnknownWhenStateCannotBeConfirmed 验证无法确认停止时不虚构 stopped。
func TestContainerServiceStopMarksUnknownWhenStateCannotBeConfirmed(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	session := domain.NewID()
	container := f.seedContainer(session, domain.ContainerStateRunning)
	planning := f.seedPlanningSession(container, containerTestCommit)
	f.host.stopFailure = true
	f.host.inspectAfterStopFailure = true
	_, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
		ContainerID: container.ID.String(),
	})
	if err == nil {
		t.Fatal("停止与复核都无法确认时必须返回错误")
	}
	if !strings.Contains(err.Error(), "无法确认") {
		t.Fatalf("错误应说明停止结果无法确认，实际 %v", err)
	}
	stored := f.loadContainer(container.ID)
	if stored.State == domain.ContainerStateStopped {
		t.Fatal("无法确认时不得把容器标记为已停止")
	}
	if stored.State != domain.ContainerStateUnknown {
		t.Fatalf("无法确认时容器状态应为 unknown，实际 %s", stored.State)
	}
	// 停止失败不得清空规划会话的运行时绑定。
	reloaded := f.loadSession(planning.ID)
	if reloaded.ContainerID != container.ID.String() {
		t.Fatalf("规划会话的容器绑定被改写: %s", reloaded.ContainerID)
	}
	if reloaded.HostID == nil || *reloaded.HostID != f.node.ID {
		t.Fatalf("规划会话的执行节点绑定被改写: %v", reloaded.HostID)
	}
	if reloaded.SourceCommit != containerTestCommit {
		t.Fatalf("规划会话的固定 commit 被改写: %s", reloaded.SourceCommit)
	}
}

// TestContainerServiceStopRefusesDetachedContainer 验证已脱离管理的容器不会被自动停止。
func TestContainerServiceStopRefusesDetachedContainer(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	detached := container
	if err := detached.Detach(time.Now().UTC()); err != nil {
		t.Fatalf("把容器置为脱离管理失败: %v", err)
	}
	if err := f.db.UpdateContainer(f.ctx, detached); err != nil {
		t.Fatalf("更新容器记录失败: %v", err)
	}
	_, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
		ContainerID: container.ID.String(),
	})
	if err == nil {
		t.Fatal("已脱离管理的容器不得被自动停止")
	}
	if !f.host.isRunning(container.DockerID) {
		t.Fatal("拒绝停止时容器不应被停止")
	}
	if calls := f.host.countCommands(" docker stop "); calls != 0 {
		t.Fatalf("拒绝停止时不得提交停止命令，实际 %d", calls)
	}
}

// TestContainerServiceStopRefusesMissingRecord 验证本地记录不存在时拒绝停止。
func TestContainerServiceStopRefusesMissingRecord(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	_, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
		ContainerID: domain.NewID().String(),
	})
	if err == nil {
		t.Fatal("本地记录不存在时必须拒绝停止")
	}
	if calls := f.host.countCommands(" docker stop "); calls != 0 {
		t.Fatalf("拒绝停止时不得提交停止命令，实际 %d", calls)
	}
}

// TestContainerServiceRunAgentStopsPlanningContainerOnUncertain 验证规划调用结果不确定时终止独占容器。
func TestContainerServiceRunAgentStopsPlanningContainerOnUncertain(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	sessionID := domain.NewID()
	container := f.seedContainer(sessionID, domain.ContainerStateRunning)
	f.host.piCancelled = true
	outcome, err := f.containers.RunAgent(f.ctx, f.agentRequest(container, sessionID.String(), true))
	if err == nil {
		t.Fatal("容器内 Pi 结果不确定时调用必须失败")
	}
	if !outcome.Result.RemoteTerminationConfirmed {
		t.Fatalf(
			"规划调用结果不确定时必须确认远端终止: %s",
			outcome.Result.RemoteTerminationError,
		)
	}
	if outcome.Result.RemoteTerminationError != "" {
		t.Fatalf("终止成功时不应带终止错误: %s", outcome.Result.RemoteTerminationError)
	}
	if outcome.Result.Status != agent.RunCancelled {
		t.Fatalf("执行状态应保持真实的 cancelled，实际 %s", outcome.Result.Status)
	}
	if f.host.isRunning(container.DockerID) {
		t.Fatal("规划容器应当已经停止")
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateStopped {
		t.Fatalf("容器记录状态应为 stopped，实际 %s", stored.State)
	}
	if outcome.Container.State != domain.ContainerStateStopped {
		t.Fatalf("返回的容器记录应为停止后的真实状态，实际 %s", outcome.Container.State)
	}
	if calls := f.host.countCommands(" docker stop "); calls != 1 {
		t.Fatalf("应当只提交一次停止命令，实际 %d", calls)
	}
	if calls := f.host.countCommands(" docker cp "); calls != 0 {
		t.Fatalf("容器已停止时不得再从容器复制 Session，实际 %d 次", calls)
	}
}

// TestContainerServiceRunAgentRefusesStopOnPlanningLabelMismatch 验证标签不一致时拒绝停止。
func TestContainerServiceRunAgentRefusesStopOnPlanningLabelMismatch(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	sessionID := domain.NewID()
	container := f.seedContainer(domain.NewID(), domain.ContainerStateRunning)
	f.host.piCancelled = true
	outcome, err := f.containers.RunAgent(f.ctx, f.agentRequest(container, sessionID.String(), true))
	if err == nil {
		t.Fatal("规划容器标签不一致时调用必须失败")
	}
	if outcome.Result.RemoteTerminationConfirmed {
		t.Fatal("标签不一致时不得报告远端终止已确认")
	}
	if !strings.Contains(outcome.Result.RemoteTerminationError, "规划会话标签") {
		t.Fatalf("终止错误应说明标签不一致，实际 %q", outcome.Result.RemoteTerminationError)
	}
	if !errors.Is(err, agent.ErrRemoteTermination) {
		t.Fatalf("调用错误应属于远端终止类别，实际 %v", err)
	}
	if !f.host.isRunning(container.DockerID) {
		t.Fatal("拒绝停止时容器不应被停止")
	}
	if calls := f.host.countCommands(" docker stop "); calls != 0 {
		t.Fatalf("拒绝停止时不得提交停止命令，实际 %d", calls)
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("拒绝停止时不得改写容器状态，实际 %s", stored.State)
	}
}

// TestContainerServiceRunAgentRefusesStopOnDockerIDMismatch 验证远程容器 ID 与本地记录不一致时拒绝停止。
func TestContainerServiceRunAgentRefusesStopOnDockerIDMismatch(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	sessionID := domain.NewID()
	container := f.seedContainer(sessionID, domain.ContainerStateRunning)
	f.host.piCancelled = true
	f.host.inspectIDOverride = "docker-unexpected-" + container.ID.String()
	outcome, err := f.containers.RunAgent(f.ctx, f.agentRequest(container, sessionID.String(), true))
	if err == nil {
		t.Fatal("Docker 容器 ID 不一致时调用必须失败")
	}
	if outcome.Result.RemoteTerminationConfirmed {
		t.Fatal("Docker 容器 ID 不一致时不得报告远端终止已确认")
	}
	if !strings.Contains(outcome.Result.RemoteTerminationError, "远程容器 ID 与本地记录不一致") {
		t.Fatalf("终止错误应说明 ID 不一致，实际 %q", outcome.Result.RemoteTerminationError)
	}
	if calls := f.host.countCommands(" docker stop "); calls != 0 {
		t.Fatalf("拒绝停止时不得提交停止命令，实际 %d", calls)
	}
}

// TestContainerServiceRunAgentDoesNotStopTaskContainer 验证普通任务调用不会停止容器。
func TestContainerServiceRunAgentDoesNotStopTaskContainer(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.piCancelled = true
	outcome, err := f.containers.RunAgent(f.ctx, f.agentRequest(container, "", false))
	if err == nil {
		t.Fatal("容器内 Pi 结果不确定时调用必须失败")
	}
	if outcome.Result.RemoteTerminationConfirmed || outcome.Result.RemoteTerminationError != "" {
		t.Fatalf("普通任务调用不得触发容器终止: %+v", outcome.Result)
	}
	if !f.host.isRunning(container.DockerID) {
		t.Fatal("普通任务容器不得被停止")
	}
	if calls := f.host.countCommands(" docker stop "); calls != 0 {
		t.Fatalf("普通任务调用不得提交停止命令，实际 %d", calls)
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("普通任务容器状态不得被改写，实际 %s", stored.State)
	}
}

// TestContainerServiceRunAgentReloadsStoppedStateWithCancelledContext 验证调用上下文被取消后仍能读回停止状态。
//
// 触发终止的调用通常已经取消：读回记录必须使用独立的有界上下文，否则用户取消会让返回的容器
// 记录错误地停留在停止前的 running 状态。
func TestContainerServiceRunAgentReloadsStoppedStateWithCancelledContext(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	sessionID := domain.NewID()
	container := f.seedContainer(sessionID, domain.ContainerStateRunning)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.host.piCancelled = true
	// Pi 主命令已经提交执行之后取消调用方上下文：这是真实的"用户取消"现场。
	f.host.setPiPrintHook(cancel)

	outcome, err := f.containers.RunAgent(ctx, f.agentRequest(container, sessionID.String(), true))
	if err == nil {
		t.Fatal("容器内 Pi 结果不确定时调用必须失败")
	}
	if ctx.Err() == nil {
		t.Fatal("本次调用的上下文应当保持取消状态")
	}
	if !outcome.Result.RemoteTerminationConfirmed {
		t.Fatalf("规划调用结果不确定时必须确认远端终止: %s", outcome.Result.RemoteTerminationError)
	}
	if outcome.ContainerStateReloadError != "" {
		t.Fatalf("读回停止后的记录不应失败: %s", outcome.ContainerStateReloadError)
	}
	if outcome.Container.State != domain.ContainerStateStopped {
		t.Fatalf("返回的容器记录应为停止后的真实状态，实际 %s", outcome.Container.State)
	}
	if outcome.Container.DockerState != domain.DockerStateExited {
		t.Fatalf("返回的 Docker 状态应为 exited，实际 %s", outcome.Container.DockerState)
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateStopped {
		t.Fatalf("持久化的容器状态应为 stopped，实际 %s", stored.State)
	}
}

// TestContainerServiceRunAgentReturnsPersistedUnknownState 验证无法确认停止时返回落库的 unknown 状态。
func TestContainerServiceRunAgentReturnsPersistedUnknownState(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	sessionID := domain.NewID()
	container := f.seedContainer(sessionID, domain.ContainerStateRunning)
	f.host.piCancelled = true
	f.host.stopFailure = true
	f.host.inspectAfterStopFailure = true

	outcome, err := f.containers.RunAgent(f.ctx, f.agentRequest(container, sessionID.String(), true))
	if err == nil {
		t.Fatal("终止结果无法确认时调用必须失败")
	}
	if outcome.Result.RemoteTerminationConfirmed {
		t.Fatal("无法确认停止时不得报告远端终止已确认")
	}
	if strings.TrimSpace(outcome.Result.RemoteTerminationError) == "" {
		t.Fatal("无法确认停止时必须给出可诊断的终止错误")
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateUnknown {
		t.Fatalf("无法确认时持久化的容器状态应为 unknown，实际 %s", stored.State)
	}
	if outcome.Container.State != domain.ContainerStateUnknown {
		t.Fatalf("返回的容器记录应反映已落库的 unknown 状态，实际 %s", outcome.Container.State)
	}
	if outcome.ContainerStateReloadError != "" {
		t.Fatalf("读回记录不应失败: %s", outcome.ContainerStateReloadError)
	}
}

// TestContainerServiceStopDoesNotTreatPausedAsStopped 验证暂停容器不会被当成"已经停止"。
//
// paused 容器同样报告 State.Running=true：如果只看 Running 就会跳过停止命令并直接记录 stopped，
// 因此这里要求真正提交一次 docker stop，并在停止后按实际检查结果收敛。
func TestContainerServiceStopDoesNotTreatPausedAsStopped(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setDockerState(container.DockerID, domain.DockerStatePaused, true)

	detail, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "暂停容器应当按真实停止结果收敛")
	if calls := f.host.countCommands(" docker stop "); calls != 1 {
		t.Fatalf("暂停容器不是已停止，必须提交停止命令，实际 %d 次", calls)
	}
	if detail.Container.State != domain.ContainerStateStopped {
		t.Fatalf("停止后容器管理状态应为 stopped，实际 %s", detail.Container.State)
	}
	if detail.Inspect == nil || detail.Inspect.State != domain.DockerStateExited {
		t.Fatalf("停止后观测状态应为 exited，实际 %+v", detail.Inspect)
	}
	if f.host.isRunning(container.DockerID) {
		t.Fatal("远程容器应当已经停止")
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateStopped || stored.DockerState != domain.DockerStateExited {
		t.Fatalf("持久化记录应为 stopped / exited，实际 %s / %s", stored.State, stored.DockerState)
	}
}

// TestContainerServiceStopRefusesToRecordStoppedForUnconfirmedStates 验证停止后仍未确认退出时不记录 stopped。
func TestContainerServiceStopRefusesToRecordStoppedForUnconfirmedStates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		state   domain.DockerContainerState
		running bool
	}{
		{name: "停止后仍然暂停", state: domain.DockerStatePaused, running: true},
		{name: "停止后仍在重启", state: domain.DockerStateRestarting, running: true},
		{name: "停止后状态矛盾", state: domain.DockerStateExited, running: true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newRemoteContainerFixture(t)
			container := f.seedContainer("", domain.ContainerStateRunning)
			f.host.setDockerState(container.DockerID, testCase.state, testCase.running)
			// 停止命令报告成功但容器现场没有变化：只有停止后的实际检查能发现真相。
			f.host.stopIgnored = true

			_, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
				ContainerID: container.ID.String(),
			})
			if err == nil {
				t.Fatalf("Docker 状态 %s 且 Running=%t 时停止必须失败", testCase.state, testCase.running)
			}
			if !strings.Contains(err.Error(), "停止后未确认退出") {
				t.Fatalf("错误应说明停止后未确认退出，实际 %v", err)
			}
			if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
				t.Fatalf("未确认退出时不得改写管理状态，实际 %s", stored.State)
			}
			if calls := f.host.countCommands(" docker start "); calls != 0 {
				t.Fatalf("未确认退出时不得提交启动命令，实际 %d 次", calls)
			}
		})
	}
}

// TestContainerServiceStopConcurrentCallsStopOnce 验证同一容器的并发停止只提交一次停止命令。
func TestContainerServiceStopConcurrentCallsStopOnce(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)

	var (
		wait   sync.WaitGroup
		mu     sync.Mutex
		states []domain.ContainerState
		errs   []error
	)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			detail, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
				ContainerID: container.ID.String(),
			})
			mu.Lock()
			defer mu.Unlock()
			states = append(states, detail.Container.State)
			errs = append(errs, err)
		}()
	}
	wait.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("并发停止同一容器不应当失败: %v", err)
		}
	}
	for _, state := range states {
		if state != domain.ContainerStateStopped {
			t.Fatalf("每次并发停止都应返回 stopped，实际 %s", state)
		}
	}
	if calls := f.host.countCommands(" docker stop "); calls != 1 {
		t.Fatalf("并发停止同一容器只应提交一次停止命令，实际 %d 次", calls)
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateStopped {
		t.Fatalf("并发停止后容器状态应为 stopped，实际 %s", stored.State)
	}
	if f.host.isRunning(container.DockerID) {
		t.Fatal("并发停止后远程容器应处于停止状态")
	}
}
