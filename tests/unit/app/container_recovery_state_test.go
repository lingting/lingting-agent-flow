package app_test

// 本文件验证容器残留观测与收敛的状态判定：paused / restarting / dead / removing 与字段矛盾的
// 现场既不是"正在运行"也不是"已确认退出"，因此既不得被收敛成 stopped，也不得触发任何自动启动
// 或删除。用例经过真实的 ContainerRecoveryService、真实的 ContainerService 与真实的状态机，
// 只有 SSH 连接与远程宿主机是受控替身。

import (
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestClassifyContainerObservationStatusMatrix 校验观测结论只对确认运行中与确认未运行给出肯定结论。
func TestClassifyContainerObservationStatusMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		state    domain.DockerContainerState
		running  bool
		expected domain.ContainerObservationClass
	}{
		{
			name:     "running 且 Running 为真",
			state:    domain.DockerStateRunning,
			running:  true,
			expected: domain.ContainerObservedRunning,
		},
		{
			name:     "exited 且 Running 为假",
			state:    domain.DockerStateExited,
			expected: domain.ContainerObservedStopped,
		},
		{
			name:     "created 且 Running 为假",
			state:    domain.DockerStateCreated,
			expected: domain.ContainerObservedStopped,
		},
		{
			name:     "paused 且 Running 为真",
			state:    domain.DockerStatePaused,
			running:  true,
			expected: domain.ContainerObservedIndeterminate,
		},
		{
			name:     "paused 且 Running 为假",
			state:    domain.DockerStatePaused,
			expected: domain.ContainerObservedIndeterminate,
		},
		{
			name:     "restarting 且 Running 为真",
			state:    domain.DockerStateRestarting,
			running:  true,
			expected: domain.ContainerObservedIndeterminate,
		},
		{
			name:     "dead",
			state:    domain.DockerStateDead,
			expected: domain.ContainerObservedIndeterminate,
		},
		{
			name:     "removing",
			state:    domain.DockerStateRemoving,
			expected: domain.ContainerObservedIndeterminate,
		},
		{
			name:     "未知状态",
			state:    domain.DockerStateUnknown,
			expected: domain.ContainerObservedIndeterminate,
		},
		{
			name:     "running 但 Running 为假",
			state:    domain.DockerStateRunning,
			expected: domain.ContainerObservedIndeterminate,
		},
		{
			name:     "exited 但 Running 为真",
			state:    domain.DockerStateExited,
			running:  true,
			expected: domain.ContainerObservedIndeterminate,
		},
		{
			name:     "created 但 Running 为真",
			state:    domain.DockerStateCreated,
			running:  true,
			expected: domain.ContainerObservedIndeterminate,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			container := domain.Container{ID: domain.ID("container-1"), DockerID: "docker-1"}
			detail := app.ContainerDetail{Inspect: &docker.ContainerInspect{
				ID:      "docker-1",
				State:   testCase.state,
				Running: testCase.running,
			}}
			observation, observedDockerID := app.ClassifyContainerObservation(container, detail, nil)
			if observation != testCase.expected {
				t.Fatalf("观测结论不符：期望 %s，实际 %s", testCase.expected, observation)
			}
			if observedDockerID != "" {
				t.Fatalf("记录已有 Docker ID 时不应回写：%s", observedDockerID)
			}
		})
	}
}

// TestClassifyContainerObservationReportsIndeterminate 校验不确定现场不会被折叠成运行中或已停止。
func TestClassifyContainerObservationReportsIndeterminate(t *testing.T) {
	t.Parallel()
	container := domain.Container{ID: domain.ID("container-1")}
	detail := app.ContainerDetail{Inspect: &docker.ContainerInspect{
		ID:      "docker-1",
		State:   domain.DockerStatePaused,
		Running: true,
	}}
	observation, _ := app.ClassifyContainerObservation(container, detail, nil)
	if observation != domain.ContainerObservedIndeterminate {
		t.Fatalf("暂停容器的观测结论应为不确定，实际 %s", observation)
	}
	decision := domain.DecideContainerRecovery(domain.ContainerRecoveryInput{
		Container:        container,
		Observation:      observation,
		ObservedDockerID: "docker-1",
	})
	if decision.Needed {
		t.Fatalf("不确定现场不得产生收敛目标：%+v", decision)
	}
	if !decision.NeedsManual {
		t.Fatal("不确定现场必须要求人工核验")
	}
}

// TestContainerRecoveryServiceKeepsUnstableContainerUnchanged 校验残留收敛不会改写不确定现场。
func TestContainerRecoveryServiceKeepsUnstableContainerUnchanged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		state    domain.DockerContainerState
		running  bool
		expected domain.ContainerState
	}{
		{name: "远端暂停", state: domain.DockerStatePaused, running: true, expected: domain.ContainerStateUnknown},
		{name: "远端重启中", state: domain.DockerStateRestarting, running: true, expected: domain.ContainerStateUnknown},
		{name: "远端 dead", state: domain.DockerStateDead, expected: domain.ContainerStateUnknown},
		{name: "远端 removing", state: domain.DockerStateRemoving, expected: domain.ContainerStateUnknown},
		{name: "状态字段矛盾", state: domain.DockerStateExited, running: true, expected: domain.ContainerStateUnknown},
		{name: "已确认退出", state: domain.DockerStateExited, expected: domain.ContainerStateStopped},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newRemoteContainerFixture(t)
			container := f.seedContainer("", domain.ContainerStateUnknown)
			f.host.setDockerState(container.DockerID, testCase.state, testCase.running)
			logger, err := testkit.Logger(logging.LevelError)
			requireNoError(t, err, "构造日志器失败")
			service, err := app.NewContainerRecoveryService(app.ContainerRecoveryOptions{
				Store:      f.store,
				Containers: f.containers,
				Logger:     logger.Slog(),
			})
			requireNoError(t, err, "构造容器残留收敛服务失败")

			requireNoError(t, service.Start(f.ctx), "收敛不确定现场不应失败")
			stored := f.loadContainer(container.ID)
			if stored.State != testCase.expected {
				t.Fatalf("容器管理状态应为 %s，实际 %s", testCase.expected, stored.State)
			}
			if calls := f.host.countCommands(" docker start "); calls != 0 {
				t.Fatalf("残留收敛不得启动容器，实际 %d 次", calls)
			}
			if calls := f.host.countCommands(" docker stop "); calls != 0 {
				t.Fatalf("残留收敛不得停止容器，实际 %d 次", calls)
			}
			if calls := f.host.countCommands(" docker rm "); calls != 0 {
				t.Fatalf("残留收敛不得删除容器，实际 %d 次", calls)
			}
		})
	}
}
