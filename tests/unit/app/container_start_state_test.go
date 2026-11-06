package app_test

// 本文件验证容器服务在启动前的状态对账：远端 inspect 是容器是否仍在运行的最终依据，本地
// 过期的管理状态必须先经领域状态机收敛，再执行 Docker start。
//
// 用例全部经过真实的 ContainerService、真实的 docker.Runner 命令构造与真实的容器状态机；
// 只有 SSH 连接与远程宿主机由本包的受控替身提供，因此"本地 running、远端 exited"这类现场
// 不是靠替身默认行为构造的，而是显式写入的。

import (
	"strings"
	"sync"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// TestContainerServiceStartConvergesStaleRunningState 验证本地 running、远端 exited 时先收敛再启动。
func TestContainerServiceStartConvergesStaleRunningState(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setRunning(container.DockerID, false)

	detail, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "远端已退出的容器应当收敛管理状态后重新启动")
	if detail.Container.State != domain.ContainerStateRunning {
		t.Fatalf("容器管理状态应为 running，实际 %s", detail.Container.State)
	}
	if detail.Container.DockerState != domain.DockerStateRunning {
		t.Fatalf("Docker 状态应同步为 running，实际 %s", detail.Container.DockerState)
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		t.Fatalf("启动后应当取得运行中的检查结果，实际 %+v", detail.Inspect)
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateRunning {
		t.Fatalf("持久化的容器状态应为 running，实际 %s", stored.State)
	}
	if calls := f.host.countCommands(" docker start "); calls != 1 {
		t.Fatalf("应当只提交一次启动命令，实际 %d 次", calls)
	}
	if calls := f.host.countCommands(" docker stop "); calls != 0 {
		t.Fatalf("启动对账不得提交停止命令，实际 %d 次", calls)
	}
	if registry := f.registryContent(); !strings.Contains(registry, `"state": "running"`) {
		t.Fatalf("远程登记清单未同步运行状态: %s", registry)
	}
}

// TestContainerServiceStartConvergesUnknownState 验证本地 unknown、远端 exited 时按已核实的退出状态收敛。
func TestContainerServiceStartConvergesUnknownState(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateUnknown)
	f.host.setRunning(container.DockerID, false)

	detail, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "远端已明确退出的容器应当经状态机收敛为 stopped 后启动")
	if detail.Container.State != domain.ContainerStateRunning {
		t.Fatalf("容器管理状态应为 running，实际 %s", detail.Container.State)
	}
	if calls := f.host.countCommands(" docker start "); calls != 1 {
		t.Fatalf("应当只提交一次启动命令，实际 %d 次", calls)
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("持久化的容器状态应为 running，实际 %s", stored.State)
	}
}

// TestContainerServiceStartConvergesCreatedDockerState 验证远端 created 时同样先收敛再启动。
func TestContainerServiceStartConvergesCreatedDockerState(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setDockerState(container.DockerID, domain.DockerStateCreated)

	detail, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "远端处于 created 的容器应当收敛管理状态后启动")
	if detail.Container.State != domain.ContainerStateRunning {
		t.Fatalf("容器管理状态应为 running，实际 %s", detail.Container.State)
	}
	if calls := f.host.countCommands(" docker start "); calls != 1 {
		t.Fatalf("应当只提交一次启动命令，实际 %d 次", calls)
	}
}

// TestContainerServiceStartDoesNotStartAlreadyRunningContainer 验证重复启动是幂等的。
func TestContainerServiceStartDoesNotStartAlreadyRunningContainer(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)

	detail, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "已在运行的受管理容器重复启动应当成功")
	if detail.Container.State != domain.ContainerStateRunning {
		t.Fatalf("容器管理状态应为 running，实际 %s", detail.Container.State)
	}
	if calls := f.host.countCommands(" docker start "); calls != 0 {
		t.Fatalf("已在运行的容器不得重复提交启动命令，实际 %d 次", calls)
	}
}

// TestContainerServiceStartRefusesUnconfirmedDockerStates 验证不可安全启动的远端状态不会被自动启动。
func TestContainerServiceStartRefusesUnconfirmedDockerStates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		state domain.DockerContainerState
	}{
		{name: "paused", state: domain.DockerStatePaused},
		{name: "restarting", state: domain.DockerStateRestarting},
		{name: "dead", state: domain.DockerStateDead},
		{name: "removing", state: domain.DockerStateRemoving},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newRemoteContainerFixture(t)
			container := f.seedContainer("", domain.ContainerStateRunning)
			f.host.setDockerState(container.DockerID, testCase.state)

			_, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
				ContainerID: container.ID.String(),
			})
			if err == nil {
				t.Fatalf("远端状态为 %s 时不得自动启动容器", testCase.state)
			}
			if !strings.Contains(err.Error(), "不在可安全启动的退出状态") {
				t.Fatalf("错误应说明远端状态不允许启动，实际 %v", err)
			}
			if calls := f.host.countCommands(" docker start "); calls != 0 {
				t.Fatalf("拒绝启动时不得提交启动命令，实际 %d 次", calls)
			}
			stored := f.loadContainer(container.ID)
			if stored.State != domain.ContainerStateRunning {
				t.Fatalf("拒绝启动时不得改写管理状态，实际 %s", stored.State)
			}
			if stored.DockerState != domain.DockerStateRunning {
				t.Fatalf("拒绝启动时不得改写观测状态，实际 %s", stored.DockerState)
			}
		})
	}
}

// TestContainerServiceStartKeepsStateWhenInspectUncertain 验证无法确认远端状态时不启动也不改写记录。
func TestContainerServiceStartKeepsStateWhenInspectUncertain(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setRunning(container.DockerID, false)
	f.host.setInspectUncertain(true)

	_, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	if err == nil {
		t.Fatal("无法确认远端状态时启动必须失败")
	}
	if calls := f.host.countCommands(" docker start "); calls != 0 {
		t.Fatalf("无法确认远端状态时不得提交启动命令，实际 %d 次", calls)
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateRunning {
		t.Fatalf("无法确认远端状态时不得改写管理状态，实际 %s", stored.State)
	}
}

// TestContainerServiceStartRefusesWhenStatePersistFails 验证本地状态持久化失败时不执行 Docker start。
func TestContainerServiceStartRefusesWhenStatePersistFails(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixtureWithStore(t, func(inner storage.Store) storage.Store {
		return newFaultStore(inner)
	})
	if f.faults == nil {
		t.Fatal("存储故障注入未生效")
	}
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setRunning(container.DockerID, false)
	f.faults.failContainerUpdates(true)

	_, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	if err == nil {
		t.Fatal("本地状态持久化失败时启动必须失败")
	}
	if !strings.Contains(err.Error(), "启动前保存已停止状态失败") {
		t.Fatalf("错误应说明本地状态未能落库，实际 %v", err)
	}
	if calls := f.host.countCommands(" docker start "); calls != 0 {
		t.Fatalf("本地状态未落库时不得提交启动命令，实际 %d 次", calls)
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("写入失败时数据库中的状态应保持不变，实际 %s", stored.State)
	}

	// 恢复数据库写入条件后，同一容器可以继续收敛并启动。
	f.faults.failContainerUpdates(false)
	detail, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "恢复写入条件后应当能够继续收敛并启动")
	if detail.Container.State != domain.ContainerStateRunning {
		t.Fatalf("恢复后容器状态应为 running，实际 %s", detail.Container.State)
	}
	if calls := f.host.countCommands(" docker start "); calls != 1 {
		t.Fatalf("恢复后应当只提交一次启动命令，实际 %d 次", calls)
	}
}

// TestContainerServiceStartKeepsPersistedStateWhenRegistrySyncFails 验证清单同步失败时保留已落库状态。
func TestContainerServiceStartKeepsPersistedStateWhenRegistrySyncFails(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setRunning(container.DockerID, false)
	f.host.setRegistryWriteFailure(true)

	_, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	if err == nil {
		t.Fatal("远程登记清单同步失败时必须返回错误")
	}
	if !strings.Contains(err.Error(), "启动前同步远程登记清单失败") {
		t.Fatalf("错误应说明登记清单同步失败，实际 %v", err)
	}
	if calls := f.host.countCommands(" docker start "); calls != 0 {
		t.Fatalf("登记清单同步失败时不得提交启动命令，实际 %d 次", calls)
	}
	stored := f.loadContainer(container.ID)
	if stored.State != domain.ContainerStateStopped {
		t.Fatalf("已落库的 stopped 不得因清单失败而回退，实际 %s", stored.State)
	}
	if stored.DockerState != domain.DockerStateExited {
		t.Fatalf("已落库的观测状态应为 exited，实际 %s", stored.DockerState)
	}

	// 恢复清单写入条件后，下一轮按真实状态继续收敛。
	f.host.setRegistryWriteFailure(false)
	detail, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	requireNoError(t, err, "恢复清单写入条件后应当能够继续收敛并启动")
	if detail.Container.State != domain.ContainerStateRunning {
		t.Fatalf("恢复后容器状态应为 running，实际 %s", detail.Container.State)
	}
	if calls := f.host.countCommands(" docker start "); calls != 1 {
		t.Fatalf("恢复后应当只提交一次启动命令，实际 %d 次", calls)
	}
}

// TestContainerServiceStartRefusesDockerIDMismatch 验证远端容器 ID 不一致时不会恢复与启动。
func TestContainerServiceStartRefusesDockerIDMismatch(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setRunning(container.DockerID, false)
	f.host.inspectIDOverride = "docker-unexpected-" + container.ID.String()

	_, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	if err == nil {
		t.Fatal("远端容器 ID 与本地记录不一致时必须拒绝启动")
	}
	if !strings.Contains(err.Error(), "远程容器 ID 与本地记录不一致") {
		t.Fatalf("错误应说明 Docker 容器 ID 不一致，实际 %v", err)
	}
	if calls := f.host.countCommands(" docker start "); calls != 0 {
		t.Fatalf("拒绝启动时不得提交启动命令，实际 %d 次", calls)
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("拒绝启动时不得改写管理状态，实际 %s", stored.State)
	}
}

// TestContainerServiceStartConcurrentCallsStartOnce 验证同一容器的并发启动只提交一次启动命令。
func TestContainerServiceStartConcurrentCallsStartOnce(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setRunning(container.DockerID, false)

	var (
		wait    sync.WaitGroup
		mu      sync.Mutex
		details []domain.ContainerState
		errs    []error
	)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			detail, err := f.containers.StartContainer(f.ctx, app.StartContainerRequest{
				ContainerID: container.ID.String(),
			})
			mu.Lock()
			defer mu.Unlock()
			details = append(details, detail.Container.State)
			errs = append(errs, err)
		}()
	}
	wait.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("并发启动同一容器不应当失败: %v", err)
		}
	}
	for _, state := range details {
		if state != domain.ContainerStateRunning {
			t.Fatalf("每次并发启动都应返回 running，实际 %s", state)
		}
	}
	if calls := f.host.countCommands(" docker start "); calls != 1 {
		t.Fatalf("并发启动同一容器只应提交一次启动命令，实际 %d 次", calls)
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("并发启动后容器状态应为 running，实际 %s", stored.State)
	}
	if !f.host.isRunning(container.DockerID) {
		t.Fatal("并发启动后远程容器应处于运行状态")
	}
}
