package domain

import (
	"fmt"
	"strings"
	"time"
)

// ContainerObservationClass 是一次只读远程观测得到的结论分类。
//
// 它与 ContainerState 是不同的事实：ContainerState 表达"AgentFlow 打算怎么管理它"，
// 本类型表达"这次观测到底看到了什么"。两者必须分开，否则"确认不存在"与"无法访问"
// 会被压平成同一个布尔值，恢复就无法区分该收敛还是该保留不确定性。
type ContainerObservationClass string

// 容器观测结论取值。
const (
	// ContainerObservedRunning 表示确认存在、所有权匹配且正在运行。
	ContainerObservedRunning ContainerObservationClass = "running"
	// ContainerObservedStopped 表示确认存在、所有权匹配但未运行。
	ContainerObservedStopped ContainerObservationClass = "stopped"
	// ContainerObservedAbsent 表示确认远程已不存在该容器。
	ContainerObservedAbsent ContainerObservationClass = "absent"
	// ContainerObservedOwnershipMismatch 表示同名容器存在，但标签与登记记录不一致。
	//
	// 它不是"不存在"，也不是"可用"：可能是同名冲突，也可能是他人创建的容器。
	// 任何破坏性处置都必须由人工确认。
	ContainerObservedOwnershipMismatch ContainerObservationClass = "ownership_mismatch"
	// ContainerObservedUnreachable 表示执行节点或 Docker 不可访问，观测没有完成。
	ContainerObservedUnreachable ContainerObservationClass = "unreachable"
	// ContainerObservedIncomplete 表示登记记录缺少可定位容器的信息，无法观测。
	ContainerObservedIncomplete ContainerObservationClass = "incomplete"
)

// String 返回观测结论的字符串形式。
func (c ContainerObservationClass) String() string {
	return string(c)
}

// Validate 校验观测结论是否属于已知取值。
func (c ContainerObservationClass) Validate() error {
	switch c {
	case ContainerObservedRunning,
		ContainerObservedStopped,
		ContainerObservedAbsent,
		ContainerObservedOwnershipMismatch,
		ContainerObservedUnreachable,
		ContainerObservedIncomplete:
		return nil
	default:
		return invalidField("Container", "observation", "未知的容器观测结论 "+string(c))
	}
}

// ContainerRecoveryInput 是一次容器残留收敛所需的持久化事实与观测结论。
type ContainerRecoveryInput struct {
	// Container 是读取到的登记记录。
	Container Container
	// Observation 是本次只读观测的结论。
	Observation ContainerObservationClass
	// ObservedDockerID 是按名称核验成功时得到的真实 Docker 容器 ID。
	//
	// 它只在登记记录缺少 docker_id 且按名称核验通过时填写：此时"缺 ID"不是
	// 孤儿，而是"创建成功但 ID 没落库"，补写 ID 才是正确的收敛。
	ObservedDockerID string
}

// ContainerRecoveryDecision 是一次容器残留收敛的结论。
type ContainerRecoveryDecision struct {
	// Needed 表示是否需要改写登记记录。
	Needed bool
	// Next 是目标管理状态，Needed 为 true 时有效。
	Next ContainerState
	// DockerID 是要补写的 Docker 容器 ID，空值表示不修改。
	DockerID string
	// ErrorClass 是收敛分类，供事件与日志使用。
	ErrorClass string
	// Summary 是面向用户的原因说明。
	Summary string
	// NeedsManual 表示必须由人工介入，恢复不得自行处置。
	NeedsManual bool
}

// DecideContainerRecovery 依据观测结论计算容器残留的收敛目标。
//
// 规则（顺序固定，只在 domain 中定义一次）：
//  1. 已脱离管理（detached）→ 不处理：用户已经显式放弃管理权，恢复不得重新接管，
//     也不得删除；
//  2. 已删除（removed）→ 不处理：终态；
//  3. 观测到运行中 → running（补写观测到的 Docker ID）；
//  4. 观测到已停止 → stopped（补写观测到的 Docker ID）；
//  5. 确认不存在 → removed（结束原因 manual）；
//  6. 所有权不匹配 → 保持现状 + 需要人工：不得删除、不得标记为已删除；
//  7. 无法访问 → 保持现状 + 需要人工：保留不确定性，不得改写为任何确定状态；
//  8. 记录不完整 → 保持现状 + 需要人工：缺 Docker ID 且按名称也无法核验。
//
// 该函数是纯函数：不读数据库、不建立远程会话，因此可以被完整地单元测试。
func DecideContainerRecovery(input ContainerRecoveryInput) ContainerRecoveryDecision {
	container := input.Container
	if err := input.Observation.Validate(); err != nil {
		return ContainerRecoveryDecision{
			ErrorClass:  "container_observation_invalid",
			Summary:     err.Error(),
			NeedsManual: true,
		}
	}
	if container.Detached || container.State == ContainerStateDetached {
		return ContainerRecoveryDecision{
			ErrorClass: "container_detached",
			Summary:    "容器已脱离 AgentFlow 自动管理，恢复不会重新接管或删除它",
		}
	}
	if container.State == ContainerStateRemoved {
		return ContainerRecoveryDecision{
			ErrorClass: "container_already_removed",
			Summary:    "容器登记记录已标记为删除，无需再次收敛",
		}
	}
	switch input.Observation {
	case ContainerObservedRunning:
		return ContainerRecoveryDecision{
			Needed:     true,
			Next:       ContainerStateRunning,
			DockerID:   strings.TrimSpace(input.ObservedDockerID),
			ErrorClass: "container_confirmed_running",
			Summary:    "已确认容器存在且所有权匹配，收敛为运行状态",
		}
	case ContainerObservedStopped:
		return ContainerRecoveryDecision{
			Needed:     true,
			Next:       ContainerStateStopped,
			DockerID:   strings.TrimSpace(input.ObservedDockerID),
			ErrorClass: "container_confirmed_stopped",
			Summary:    "已确认容器存在但未运行，收敛为停止状态并保留现场",
		}
	case ContainerObservedAbsent:
		return ContainerRecoveryDecision{
			Needed:     true,
			Next:       ContainerStateRemoved,
			ErrorClass: "container_confirmed_absent",
			Summary:    "已确认容器在远程不存在，收敛为已删除",
		}
	case ContainerObservedOwnershipMismatch:
		return ContainerRecoveryDecision{
			ErrorClass:  "container_ownership_mismatch",
			Summary:     "同名容器存在但所有权标签与登记记录不一致，必须人工核验，禁止自动删除",
			NeedsManual: true,
		}
	case ContainerObservedIncomplete:
		return ContainerRecoveryDecision{
			ErrorClass:  "container_record_incomplete",
			Summary:     "登记记录缺少可定位容器的信息，且按名称未能核验，必须人工确认容器实际归属",
			NeedsManual: true,
		}
	default:
		return ContainerRecoveryDecision{
			ErrorClass:  "container_unreachable",
			Summary:     "执行节点或 Docker 当前不可访问，无法确认容器状态，保留原记录等待重新核验",
			NeedsManual: true,
		}
	}
}

// ApplyContainerRecovery 把一次收敛结论写回容器记录副本。
//
// 只有 Needed 为真时才改写管理状态；已脱离管理与已删除的记录不会被重新打开，
// 因为领域状态机拒绝从终态迁出。
func (c Container) ApplyContainerRecovery(
	decision ContainerRecoveryDecision,
	at time.Time,
) (Container, error) {
	updated := c
	if !decision.Needed {
		return updated, nil
	}
	now := normalizeTime(at)
	if id := strings.TrimSpace(decision.DockerID); id != "" {
		updated.DockerID = id
	}
	switch decision.Next {
	case ContainerStateRunning:
		if err := updated.MarkRunning(now); err != nil {
			return c, err
		}
	case ContainerStateStopped:
		if err := updated.MarkStopped(now); err != nil {
			return c, err
		}
	case ContainerStateRemoved:
		if err := updated.MarkRemoved(ContainerEndReasonManual, now); err != nil {
			return c, err
		}
	default:
		return c, invalidField(
			"Container",
			"state",
			"容器收敛目标只能是 running / stopped / removed，收到 "+string(decision.Next),
		)
	}
	// 收敛成功说明现场已确认：清除上一次"状态不确定"留下的诊断摘要，
	// 但保留 Docker 侧观测状态，两者是不同的事实。
	updated.ErrorSummary = ""
	updated.UpdatedAt = now
	return updated, nil
}

// ContainerRecoverySummary 返回收敛结论的可读摘要，供事件与日志使用。
func ContainerRecoverySummary(decision ContainerRecoveryDecision) string {
	summary := strings.TrimSpace(decision.Summary)
	if summary == "" {
		summary = "容器残留收敛未产生结论"
	}
	if decision.NeedsManual {
		return summary + "（需要人工介入）"
	}
	return summary
}

// ContainerOrphanSummary 返回孤儿容器报告使用的固定摘要。
//
// 孤儿容器只被报告，不被自动删除或重新关联：它的工作区状态未知，自动接管会让
// "创建成功、关联失败"的现场重新进入执行链路。
func ContainerOrphanSummary(taskID ID) string {
	return fmt.Sprintf(
		"容器登记了任务 %s 的关联，但该任务当前使用的不是它；"+
			"恢复只报告该现场，不自动删除也不自动重新关联",
		taskID,
	)
}
