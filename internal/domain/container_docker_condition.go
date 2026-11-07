package domain

// 本文件承载 docker inspect 状态字段的统一判定。
//
// Docker Engine 的 State.Status 与 State.Running 不是互斥的布尔字段：paused 与 restarting
// 容器同样报告 State.Running=true。因此"容器能否执行命令、能否自动启动、能否记录为已停止"
// 都不允许由 Running 单独决定，必须同时考虑两个字段；任何字段矛盾都按"不确定"失败关闭，
// 不得降级解释为正常运行或正常退出。

// DockerContainerCondition 是 docker inspect 的 State.Status 与 State.Running 组合出的生命周期判定。
type DockerContainerCondition string

// Docker 容器生命周期判定取值。
const (
	// DockerConditionRunning 表示已确认运行中。
	DockerConditionRunning DockerContainerCondition = "running"
	// DockerConditionExited 表示已确认退出。
	DockerConditionExited DockerContainerCondition = "exited"
	// DockerConditionCreated 表示已创建但从未启动。
	DockerConditionCreated DockerContainerCondition = "created"
	// DockerConditionPaused 表示容器已暂停：进程仍存在，但不可执行，也不得当作已退出。
	DockerConditionPaused DockerContainerCondition = "paused"
	// DockerConditionRestarting 表示容器正在重启：状态不稳定，不可执行也不得自动启动。
	DockerConditionRestarting DockerContainerCondition = "restarting"
	// DockerConditionDead 表示容器处于不可恢复状态，需要人工核验。
	DockerConditionDead DockerContainerCondition = "dead"
	// DockerConditionRemoving 表示容器删除过程尚未完成。
	DockerConditionRemoving DockerContainerCondition = "removing"
	// DockerConditionIndeterminate 表示状态未知，或 State.Status 与 State.Running 互相矛盾。
	DockerConditionIndeterminate DockerContainerCondition = "indeterminate"
)

// String 返回容器生命周期判定的字符串形式。
func (c DockerContainerCondition) String() string {
	return string(c)
}

// ClassifyDockerContainerCondition 把 docker inspect 的状态字段组合判定为生命周期语义。
//
// 规则固定为：
//   - running 且 Running=true → 运行中；running 且 Running=false → 矛盾，按不确定处理；
//   - exited 且 Running=false → 已退出；exited 且 Running=true → 矛盾，按不确定处理；
//   - created 且 Running=false → 已创建未运行；created 且 Running=true → 矛盾，按不确定处理；
//   - paused 与 restarting 不论 Running 取值都按暂停 / 重启中处理：Docker 在这两种状态下
//     同样可能报告 Running=true，不能因此把它们当作正常运行，也不能当作已退出；
//   - dead 与 removing 保留各自的确定语义，不降级为已退出；
//   - 其他取值（含无法识别的状态）一律按不确定处理。
func ClassifyDockerContainerCondition(
	status DockerContainerState,
	running bool,
) DockerContainerCondition {
	switch status {
	case DockerStateRunning:
		if !running {
			return DockerConditionIndeterminate
		}
		return DockerConditionRunning
	case DockerStateExited:
		if running {
			return DockerConditionIndeterminate
		}
		return DockerConditionExited
	case DockerStateCreated:
		if running {
			return DockerConditionIndeterminate
		}
		return DockerConditionCreated
	case DockerStatePaused:
		return DockerConditionPaused
	case DockerStateRestarting:
		return DockerConditionRestarting
	case DockerStateDead:
		return DockerConditionDead
	case DockerStateRemoving:
		return DockerConditionRemoving
	default:
		return DockerConditionIndeterminate
	}
}

// IsActivelyRunning 判断容器是否已确认正常运行。
//
// 只有该判定为真时才能执行容器内命令、发起 Pi 调用或启动规划 Agent：paused 与 restarting
// 同样报告 Running=true，但它们不属于可执行状态。
func (c DockerContainerCondition) IsActivelyRunning() bool {
	return c == DockerConditionRunning
}

// IsConfirmedExited 判断容器是否已确认退出（Docker 同时报告 exited 与 Running=false）。
func (c DockerContainerCondition) IsConfirmedExited() bool {
	return c == DockerConditionExited
}

// ConfirmsNotRunning 判断容器是否确认未在运行：已退出的 exited 与从未启动的 created。
//
// 暂停、重启中、dead、removing 与不确定状态都不属于"未在运行"，因此既不能据此记录停止完成，
// 也不能把它们当作可以安全启动的现场。
func (c DockerContainerCondition) ConfirmsNotRunning() bool {
	return c == DockerConditionExited || c == DockerConditionCreated
}

// IsStartable 判断容器是否处于可以安全自动启动的状态。
//
// 只有确认未运行的 exited 与 created 满足条件；paused、restarting、dead、removing 与
// 不确定状态都不得进入自动启动路径。
func (c DockerContainerCondition) IsStartable() bool {
	return c.ConfirmsNotRunning()
}
