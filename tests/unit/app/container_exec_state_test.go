package app_test

// 本文件验证容器执行入口的状态判定：只有已确认的 running 状态可以执行容器内命令或发起 Pi 主调用。
//
// paused 与 restarting 容器同样报告 State.Running=true，因此这些用例全部以真实 Docker 字段组合
// （State.Status + State.Running）构造，而不是把 Running 当作"是否运行"的唯一依据。用例经过真实的
// ContainerService、真实的 docker.Runner 与真实的容器状态机，只有 SSH 连接与远程宿主机是受控替身。

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// TestContainerServiceExecRequiresActivelyRunning 验证不可执行状态不会提交 docker exec。
func TestContainerServiceExecRequiresActivelyRunning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		state   domain.DockerContainerState
		running bool
	}{
		{name: "paused 且 Running 为真", state: domain.DockerStatePaused, running: true},
		{name: "paused 且 Running 为假", state: domain.DockerStatePaused, running: false},
		{name: "restarting 且 Running 为真", state: domain.DockerStateRestarting, running: true},
		{name: "running 但 Running 为假（矛盾）", state: domain.DockerStateRunning, running: false},
		{name: "无法识别的状态", state: domain.DockerStateUnknown, running: true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newRemoteContainerFixture(t)
			container := f.seedContainer("", domain.ContainerStateRunning)
			f.host.setDockerState(container.DockerID, testCase.state, testCase.running)

			_, err := f.containers.Exec(f.ctx, app.ExecRequest{
				ContainerID: container.ID.String(),
				Command:     []string{"git", "--version"},
				WorkDir:     domain.DefaultContainerWorkDir,
			})
			if err == nil {
				t.Fatalf("Docker 状态 %s 且 Running=%t 时不得执行容器内命令", testCase.state, testCase.running)
			}
			if calls := f.host.countCommands(" docker exec "); calls != 0 {
				t.Fatalf("被拒绝的执行不得提交 docker exec，实际 %d 次", calls)
			}
			if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
				t.Fatalf("被拒绝的执行不得改写管理状态，实际 %s", stored.State)
			}
		})
	}
}

// TestContainerServiceExecRunsOnConfirmedRunningContainer 对照验证确认运行中的容器可以执行命令。
func TestContainerServiceExecRunsOnConfirmedRunningContainer(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)

	result, err := f.containers.Exec(f.ctx, app.ExecRequest{
		ContainerID: container.ID.String(),
		Command:     []string{"git", "--version"},
		WorkDir:     domain.DefaultContainerWorkDir,
	})
	requireNoError(t, err, "确认运行中的容器应当可以执行命令")
	if !result.Result.Succeeded() {
		t.Fatalf("容器内命令应当成功结束: %+v", result.Result)
	}
	if calls := f.host.countCommands(" docker exec "); calls != 1 {
		t.Fatalf("应当只提交一次 docker exec，实际 %d 次", calls)
	}
}

// TestContainerServiceRunAgentRequiresActivelyRunning 验证不可执行状态不会发起 Pi 主调用。
func TestContainerServiceRunAgentRequiresActivelyRunning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		state   domain.DockerContainerState
		running bool
	}{
		{name: "paused 且 Running 为真", state: domain.DockerStatePaused, running: true},
		{name: "restarting 且 Running 为真", state: domain.DockerStateRestarting, running: true},
		{name: "running 但 Running 为假（矛盾）", state: domain.DockerStateRunning, running: false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newRemoteContainerFixture(t)
			container := f.seedContainer("", domain.ContainerStateRunning)
			f.host.setDockerState(container.DockerID, testCase.state, testCase.running)

			_, err := f.containers.RunAgent(f.ctx, f.agentRequest(container, "", false))
			if err == nil {
				t.Fatalf("Docker 状态 %s 且 Running=%t 时不得发起 Pi 主调用", testCase.state, testCase.running)
			}
			if !strings.Contains(err.Error(), "未处于可执行状态") {
				t.Fatalf("错误应说明容器不处于可执行状态，实际 %v", err)
			}
			if calls := f.host.countCommands(" docker exec "); calls != 0 {
				t.Fatalf("被拒绝的调用不得提交任何容器内执行，实际 %d 次", calls)
			}
			if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
				t.Fatalf("被拒绝的调用不得改写管理状态，实际 %s", stored.State)
			}
		})
	}
}

// TestContainerServiceExecSkipsRemoteEffectsWhenInspectUncertain 验证核验失败时不产生远程副作用。
func TestContainerServiceExecSkipsRemoteEffectsWhenInspectUncertain(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)
	f.host.setInspectUncertain(true)

	_, err := f.containers.Exec(f.ctx, app.ExecRequest{
		ContainerID: container.ID.String(),
		Command:     []string{"git", "--version"},
		WorkDir:     domain.DefaultContainerWorkDir,
	})
	if err == nil {
		t.Fatal("无法确认容器状态时必须拒绝执行")
	}
	if calls := f.host.countCommands(" docker exec "); calls != 0 {
		t.Fatalf("核验失败时不得提交 docker exec，实际 %d 次", calls)
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateRunning {
		t.Fatalf("核验失败时不得改写管理状态，实际 %s", stored.State)
	}
}

// TestContainerServiceExecAndStopSerializeOnContainerLock 验证同一容器的并发 Exec 与 Stop 仍串行且有界返回。
//
// 两个入口共用同一把 per-container 键控锁：无论谁先进入临界区，另一方都必须等到锁释放后重新核验远端
// 事实，因此不会出现"停止后继续在容器内执行"的越界，也不会因为锁未释放而挂住。
func TestContainerServiceExecAndStopSerializeOnContainerLock(t *testing.T) {
	t.Parallel()
	f := newRemoteContainerFixture(t)
	container := f.seedContainer("", domain.ContainerStateRunning)

	var (
		wait    sync.WaitGroup
		mu      sync.Mutex
		execErr error
		stopErr error
	)
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, err := f.containers.Exec(f.ctx, app.ExecRequest{
			ContainerID: container.ID.String(),
			Command:     []string{"git", "--version"},
			WorkDir:     domain.DefaultContainerWorkDir,
		})
		mu.Lock()
		defer mu.Unlock()
		execErr = err
	}()
	go func() {
		defer wait.Done()
		_, err := f.containers.StopContainer(f.ctx, app.StopContainerRequest{
			ContainerID: container.ID.String(),
		})
		mu.Lock()
		defer mu.Unlock()
		stopErr = err
	}()

	done := make(chan struct{})
	go func() {
		wait.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("并发 Exec 与 Stop 未在限期内完成：容器锁可能未被释放")
	}

	mu.Lock()
	defer mu.Unlock()
	if stopErr != nil {
		t.Fatalf("并发停止同一容器不应当失败: %v", stopErr)
	}
	if execErr != nil && !strings.Contains(execErr.Error(), "未运行") {
		t.Fatalf("Exec 只允许因容器已停止而被拒绝，实际错误: %v", execErr)
	}
	if calls := f.host.countCommands(" docker stop "); calls != 1 {
		t.Fatalf("并发停止只应提交一次停止命令，实际 %d 次", calls)
	}
	if f.host.isRunning(container.DockerID) {
		t.Fatal("并发操作结束后远程容器应处于停止状态")
	}
	if stored := f.loadContainer(container.ID); stored.State != domain.ContainerStateStopped {
		t.Fatalf("并发操作结束后容器状态应为 stopped，实际 %s", stored.State)
	}
}
