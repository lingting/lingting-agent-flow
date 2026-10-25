package domain

import (
	"fmt"
	"strings"
)

// ContainerOwnershipRejection 是容器归属校验的拒绝原因。
//
// 归属校验回答的是"这条容器登记记录能不能被该任务当作自己的执行容器使用"，
// 与"容器当前是否运行"是两个不同的问题：后者由 Docker 侧观测结果决定。
// 两者必须分开，否则"属于其他任务的运行中容器"会被当成可用容器。
type ContainerOwnershipRejection string

// 容器归属校验结论取值。
const (
	// ContainerOwnershipAllowed 表示归属成立。
	ContainerOwnershipAllowed ContainerOwnershipRejection = ""
	// ContainerOwnershipMissingRef 表示任务记录没有容器引用。
	ContainerOwnershipMissingRef ContainerOwnershipRejection = "container_ref_missing"
	// ContainerOwnershipRecordMismatch 表示容器记录不是任务当前引用的那一条。
	ContainerOwnershipRecordMismatch ContainerOwnershipRejection = "container_record_mismatch"
	// ContainerOwnershipUnassigned 表示容器记录没有登记任务归属。
	ContainerOwnershipUnassigned ContainerOwnershipRejection = "container_unassigned"
	// ContainerOwnershipTaskMismatch 表示容器登记的是另一个任务的归属。
	ContainerOwnershipTaskMismatch ContainerOwnershipRejection = "container_task_mismatch"
	// ContainerOwnershipHostMismatch 表示容器所属执行节点与任务不一致。
	ContainerOwnershipHostMismatch ContainerOwnershipRejection = "container_host_mismatch"
	// ContainerOwnershipDetached 表示容器已脱离 AgentFlow 自动管理。
	ContainerOwnershipDetached ContainerOwnershipRejection = "container_detached"
	// ContainerOwnershipRemoved 表示容器已被删除。
	ContainerOwnershipRemoved ContainerOwnershipRejection = "container_removed"
	// ContainerOwnershipDockerIDMissing 表示容器记录缺少 Docker 容器 ID。
	ContainerOwnershipDockerIDMissing ContainerOwnershipRejection = "container_docker_id_missing"
)

// String 返回拒绝原因的字符串形式。
func (r ContainerOwnershipRejection) String() string {
	return string(r)
}

// ContainerOwnershipDecision 是一次容器归属校验的结论。
//
// 结论只描述"归属是否成立"，不描述"应该怎么处置"：调用方据此拒绝执行或拒绝
// 回收，不得自行猜测另一种关联。
type ContainerOwnershipDecision struct {
	// Allowed 表示该容器可以被该任务当作自己的执行容器使用。
	Allowed bool
	// Reason 是拒绝原因，Allowed 为 true 时为空。
	Reason ContainerOwnershipRejection
	// Detail 是可读说明，Allowed 为 true 时为空。
	Detail string
}

// DecideContainerOwnership 判断容器登记记录是否确实属于该任务。
//
// 规则顺序固定，只在 domain 中定义一次：
//  1. 任务没有容器引用 → 拒绝（调用方本不该在这里询问）；
//  2. 记录标识与任务引用不一致 → 拒绝：数据库里存在两条互相矛盾的关联，
//     无法判断哪一条代表真实现场；
//  3. 记录未登记任务归属 → 拒绝：没有归属证明就不能被当作任务容器；
//  4. 记录登记的是另一个任务 → 拒绝：这是跨任务串扰的唯一可核验防线，
//     "容器名称唯一"与"容器里有一个工作区"都不能替代它；
//  5. 记录所属执行节点与任务不一致 → 拒绝：命令会在错误的机器上执行；
//  6. 已脱离管理 / 已删除 → 拒绝：这两类容器不再接受自动操作；
//  7. 缺少 Docker 容器 ID → 拒绝：没有可核验的执行目标。
//
// 该函数是纯函数：不读数据库、不建立远程会话，也不修改任何状态。
func DecideContainerOwnership(task Task, container Container) ContainerOwnershipDecision {
	referenced := strings.TrimSpace(task.ContainerID)
	if referenced == "" {
		return ContainerOwnershipDecision{
			Reason: ContainerOwnershipMissingRef,
			Detail: fmt.Sprintf("任务 %s 没有任务容器引用", task.ID),
		}
	}
	if container.ID.String() != referenced {
		return ContainerOwnershipDecision{
			Reason: ContainerOwnershipRecordMismatch,
			Detail: fmt.Sprintf(
				"容器记录 %s 不是任务 %s 引用的容器 %s",
				container.ID,
				task.ID,
				referenced,
			),
		}
	}
	if container.TaskID == nil {
		return ContainerOwnershipDecision{
			Reason: ContainerOwnershipUnassigned,
			Detail: fmt.Sprintf("容器 %s 没有登记任务归属", container.ID),
		}
	}
	if *container.TaskID != task.ID {
		return ContainerOwnershipDecision{
			Reason: ContainerOwnershipTaskMismatch,
			Detail: fmt.Sprintf(
				"容器 %s 登记的是任务 %s，不是任务 %s",
				container.ID,
				*container.TaskID,
				task.ID,
			),
		}
	}
	if task.HostID != nil && *task.HostID != container.HostID {
		return ContainerOwnershipDecision{
			Reason: ContainerOwnershipHostMismatch,
			Detail: fmt.Sprintf(
				"容器 %s 属于执行节点 %s，与任务 %s 的节点 %s 不一致",
				container.ID,
				container.HostID,
				task.ID,
				*task.HostID,
			),
		}
	}
	if container.Detached || container.State == ContainerStateDetached {
		return ContainerOwnershipDecision{
			Reason: ContainerOwnershipDetached,
			Detail: fmt.Sprintf("容器 %s 已脱离 AgentFlow 自动管理", container.ID),
		}
	}
	if container.State == ContainerStateRemoved {
		return ContainerOwnershipDecision{
			Reason: ContainerOwnershipRemoved,
			Detail: fmt.Sprintf("容器 %s 已被删除", container.ID),
		}
	}
	if strings.TrimSpace(container.DockerID) == "" {
		return ContainerOwnershipDecision{
			Reason: ContainerOwnershipDockerIDMissing,
			Detail: fmt.Sprintf("容器 %s 缺少 Docker 容器标识", container.ID),
		}
	}
	return ContainerOwnershipDecision{Allowed: true}
}

// CheckContainerOwnership 判断容器归属是否成立，不成立时返回可诊断错误。
//
// 错误包装 ErrInvalidState，调用方据此把结论映射为各自的前置条件或冲突类别，
// 不得改写成"容器不可用"这类掩盖真实原因的说法。
func CheckContainerOwnership(task Task, container Container) error {
	decision := DecideContainerOwnership(task, container)
	if decision.Allowed {
		return nil
	}
	return fmt.Errorf(
		"%w: %s: %s",
		ErrInvalidState,
		decision.Reason,
		decision.Detail,
	)
}
