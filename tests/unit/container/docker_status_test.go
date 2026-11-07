package container_test

// 本文件验证 docker inspect 的状态字段按 Docker Engine 语义解析与判定。
//
// Docker 的 State.Status 与 State.Running 不是互斥的布尔字段：paused 与 restarting 容器同样报告
// Running=true。因此解析结果必须同时保留两个字段，判定必须统一走 ContainerInspect 的生命周期
// 判据，未知取值与字段矛盾一律失败关闭。

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// containerInspectOutput 构造一份 docker inspect 输出。
func containerInspectOutput(status string, running bool) string {
	return `[{"Id":"container-1","Name":"/agentflow-abcdef123456",` +
		`"Image":"sha256:` + strings.Repeat("b", 64) + `",` +
		`"Config":{"Labels":{"agentflow.managed":"true"}},` +
		`"State":{"Status":"` + status + `","Running":` + boolText(running) + `}}]`
}

// boolText 把布尔值渲染为 JSON 字面量。
func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// TestParseContainerInspectStatusMatrix 校验状态字段矩阵的解析结果与统一判定。
func TestParseContainerInspectStatusMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		status    string
		running   bool
		state     domain.DockerContainerState
		condition domain.DockerContainerCondition
		active    bool
		exited    bool
		startable bool
	}{
		{
			name:      "running 且 Running 为真",
			status:    "running",
			running:   true,
			state:     domain.DockerStateRunning,
			condition: domain.DockerConditionRunning,
			active:    true,
		},
		{
			name:      "running 但 Running 为假",
			status:    "running",
			running:   false,
			state:     domain.DockerStateRunning,
			condition: domain.DockerConditionIndeterminate,
		},
		{
			name:      "exited 且 Running 为假",
			status:    "exited",
			running:   false,
			state:     domain.DockerStateExited,
			condition: domain.DockerConditionExited,
			exited:    true,
			startable: true,
		},
		{
			name:      "exited 但 Running 为真",
			status:    "exited",
			running:   true,
			state:     domain.DockerStateExited,
			condition: domain.DockerConditionIndeterminate,
		},
		{
			name:      "created 且 Running 为假",
			status:    "created",
			running:   false,
			state:     domain.DockerStateCreated,
			condition: domain.DockerConditionCreated,
			startable: true,
		},
		{
			name:      "created 但 Running 为真",
			status:    "created",
			running:   true,
			state:     domain.DockerStateCreated,
			condition: domain.DockerConditionIndeterminate,
		},
		{
			name:      "paused 且 Running 为真",
			status:    "paused",
			running:   true,
			state:     domain.DockerStatePaused,
			condition: domain.DockerConditionPaused,
		},
		{
			name:      "paused 且 Running 为假",
			status:    "paused",
			running:   false,
			state:     domain.DockerStatePaused,
			condition: domain.DockerConditionPaused,
		},
		{
			name:      "restarting 且 Running 为真",
			status:    "restarting",
			running:   true,
			state:     domain.DockerStateRestarting,
			condition: domain.DockerConditionRestarting,
		},
		{
			name:      "restarting 且 Running 为假",
			status:    "restarting",
			running:   false,
			state:     domain.DockerStateRestarting,
			condition: domain.DockerConditionRestarting,
		},
		{
			name:      "dead",
			status:    "dead",
			running:   false,
			state:     domain.DockerStateDead,
			condition: domain.DockerConditionDead,
		},
		{
			name:      "removing",
			status:    "removing",
			running:   false,
			state:     domain.DockerStateRemoving,
			condition: domain.DockerConditionRemoving,
		},
		{
			name:      "无法识别的状态",
			status:    "unknown-future-status",
			running:   true,
			state:     domain.DockerStateUnknown,
			condition: domain.DockerConditionIndeterminate,
		},
		{
			name:      "状态文本大小写不敏感",
			status:    "Running",
			running:   true,
			state:     domain.DockerStateRunning,
			condition: domain.DockerConditionRunning,
			active:    true,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			inspect, err := docker.ParseContainerInspect(
				containerInspectOutput(testCase.status, testCase.running),
			)
			if err != nil {
				t.Fatalf("解析容器检查结果失败: %v", err)
			}
			if inspect.State != testCase.state {
				t.Fatalf("Docker 状态不符：期望 %s，实际 %s", testCase.state, inspect.State)
			}
			if inspect.Running != testCase.running {
				t.Fatalf("State.Running 必须按原样保留：期望 %t，实际 %t", testCase.running, inspect.Running)
			}
			if inspect.RuntimeCondition() != testCase.condition {
				t.Fatalf(
					"生命周期判定不符：期望 %s，实际 %s",
					testCase.condition,
					inspect.RuntimeCondition(),
				)
			}
			if inspect.IsActivelyRunning() != testCase.active {
				t.Fatalf("IsActivelyRunning 应为 %t，实际 %t", testCase.active, inspect.IsActivelyRunning())
			}
			if inspect.IsConfirmedExited() != testCase.exited {
				t.Fatalf("IsConfirmedExited 应为 %t，实际 %t", testCase.exited, inspect.IsConfirmedExited())
			}
			if inspect.IsStartable() != testCase.startable {
				t.Fatalf("IsStartable 应为 %t，实际 %t", testCase.startable, inspect.IsStartable())
			}
		})
	}
}

// TestClassifyDockerContainerConditionIsTotal 校验判定函数对每个已知状态都有确定结论。
func TestClassifyDockerContainerConditionIsTotal(t *testing.T) {
	t.Parallel()
	states := []domain.DockerContainerState{
		domain.DockerStateUnknown,
		domain.DockerStateCreated,
		domain.DockerStateRunning,
		domain.DockerStatePaused,
		domain.DockerStateRestarting,
		domain.DockerStateExited,
		domain.DockerStateDead,
		domain.DockerStateRemoving,
	}
	for _, state := range states {
		for _, running := range []bool{true, false} {
			condition := domain.ClassifyDockerContainerCondition(state, running)
			if condition == "" {
				t.Fatalf("状态 %s 且 Running=%t 时判定不能为空", state, running)
			}
			// 只有 running 且 Running 为真可以执行；只有 exited / created 且 Running 为假
			// 可以启动；两者不能同时成立。
			if condition.IsActivelyRunning() && condition.IsStartable() {
				t.Fatalf("状态 %s 且 Running=%t 不能同时可执行又可启动", state, running)
			}
			if condition.IsActivelyRunning() && running == false {
				t.Fatalf("Running 为假时不得判定为运行中: %s", state)
			}
			if condition.IsStartable() && running {
				t.Fatalf("Running 为真时不得判定为可启动: %s", state)
			}
		}
	}
}
