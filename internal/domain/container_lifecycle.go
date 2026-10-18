package domain

import (
	"fmt"
	"strings"
	"time"
)

// ContainerOutcome 是一次任务执行结果的分类，用于决定容器生命周期动作。
type ContainerOutcome string

// 任务执行结果分类。
//
// 注意：这里的"成功"指业务流程（例如代码保存）已经完成，而不是容器内某条命令
// 退出码为 0。工作流引擎负责判断业务成功，再调用本包的生命周期策略。
const (
	// ContainerOutcomeSucceeded 表示任务成功且必要的业务操作已完成。
	ContainerOutcomeSucceeded ContainerOutcome = "succeeded"
	// ContainerOutcomeFailed 表示任务失败。
	ContainerOutcomeFailed ContainerOutcome = "failed"
	// ContainerOutcomeInterrupted 表示任务被中断。
	ContainerOutcomeInterrupted ContainerOutcome = "interrupted"
)

// String 返回结果分类的字符串形式。
func (o ContainerOutcome) String() string {
	return string(o)
}

// Validate 校验结果分类是否受支持。
func (o ContainerOutcome) Validate() error {
	switch o {
	case ContainerOutcomeSucceeded, ContainerOutcomeFailed, ContainerOutcomeInterrupted:
		return nil
	default:
		return invalidField("ContainerLifecycle", "outcome", "未知的任务结果分类 "+string(o))
	}
}

// ContainerRetention 是生命周期策略给出的处置方式。
type ContainerRetention string

// 生命周期处置方式取值。
const (
	// ContainerRetentionKeep 表示保留容器及现场。
	ContainerRetentionKeep ContainerRetention = "keep"
	// ContainerRetentionDelete 表示删除容器及其任务级临时资源。
	ContainerRetentionDelete ContainerRetention = "delete"
	// ContainerRetentionDetach 表示保留容器但脱离 AgentFlow 自动管理。
	ContainerRetentionDetach ContainerRetention = "detach"
)

// String 返回处置方式的字符串形式。
func (r ContainerRetention) String() string {
	return string(r)
}

// ContainerLifecycleOptions 是一次生命周期决策的可选输入。
type ContainerLifecycleOptions struct {
	// KeepInterrupted 表示中断时仍然保留容器。
	//
	// 零值为保留：中断默认保留现场，只有调用方显式选择不保留时才删除。
	KeepInterrupted bool
}

// ContainerLifecycleDecision 是生命周期策略给出的结论。
//
// 结论只描述"应当做什么"，实际远程操作由容器服务执行；两者分离使策略可以在
// 不接触 Docker 的前提下被单独验证。
type ContainerLifecycleDecision struct {
	// Retention 是处置方式。
	Retention ContainerRetention
	// EndReason 是写入记录的生命周期结束原因。
	EndReason ContainerEndReason
	// RemoveTaskResources 表示同时清理任务级临时资源。
	//
	// 该标记永远不包含共享镜像、共享缓存卷与远程项目配置目录。
	RemoveTaskResources bool
	// Detail 是给用户看的可读说明。
	Detail string
}

// DecideContainerLifecycle 依据任务结果给出容器生命周期处置结论。
//
// 规则：
//   - 成功：删除容器与任务级临时资源；
//   - 失败：保留容器及现场；
//   - 中断：默认保留，调用方显式要求不保留时删除。
//
// 该函数不触碰共享镜像、共享缓存卷与远程项目配置目录，任何情况下都不会
// 建议清理它们。
func DecideContainerLifecycle(
	outcome ContainerOutcome,
	options ContainerLifecycleOptions,
) (ContainerLifecycleDecision, error) {
	if err := outcome.Validate(); err != nil {
		return ContainerLifecycleDecision{}, err
	}
	switch outcome {
	case ContainerOutcomeSucceeded:
		return ContainerLifecycleDecision{
			Retention:           ContainerRetentionDelete,
			EndReason:           ContainerEndReasonTaskSucceeded,
			RemoveTaskResources: true,
			Detail:              "任务成功且业务操作已完成，删除容器与任务级临时资源",
		}, nil
	case ContainerOutcomeFailed:
		return ContainerLifecycleDecision{
			Retention: ContainerRetentionKeep,
			EndReason: ContainerEndReasonTaskFailed,
			Detail:    "任务失败，保留容器及现场以便诊断",
		}, nil
	default:
		if options.KeepInterrupted {
			return ContainerLifecycleDecision{
				Retention: ContainerRetentionKeep,
				EndReason: ContainerEndReasonInterrupted,
				Detail:    "任务中断，按调用方要求保留容器现场",
			}, nil
		}
		return ContainerLifecycleDecision{
			Retention:           ContainerRetentionDelete,
			EndReason:           ContainerEndReasonInterrupted,
			RemoveTaskResources: true,
			Detail:              "任务中断，按调用方要求删除容器与任务级临时资源",
		}, nil
	}
}

// RestartDisposition 是重新开始失败任务时对旧容器的处置方式。
type RestartDisposition string

// 旧容器处置方式取值。
const (
	// RestartDispositionDelete 表示删除旧容器及其任务级临时资源。
	RestartDispositionDelete RestartDisposition = "delete"
	// RestartDispositionDetach 表示保留旧容器现场并脱离 AgentFlow 管理。
	RestartDispositionDetach RestartDisposition = "detach"
)

// String 返回处置方式的字符串形式。
func (d RestartDisposition) String() string {
	return string(d)
}

// Validate 校验处置方式是否受支持。
func (d RestartDisposition) Validate() error {
	switch d {
	case RestartDispositionDelete, RestartDispositionDetach:
		return nil
	default:
		return invalidField(
			"ContainerLifecycle",
			"restart_disposition",
			"重新开始时旧容器只能选择 delete 或 detach",
		)
	}
}

// ParseRestartDisposition 解析命令行给出的旧容器处置方式。
func ParseRestartDisposition(raw string) (RestartDisposition, error) {
	value := RestartDisposition(strings.ToLower(strings.TrimSpace(raw)))
	if err := value.Validate(); err != nil {
		return "", err
	}
	return value, nil
}

// RestartPlan 是重新开始失败任务的执行计划。
//
// 重新开始必须创建新容器：本计划明确要求调用方先处置旧容器，再创建新容器，
// 不允许复用旧容器，也不允许把旧容器中的未推送代码带入新容器。
type RestartPlan struct {
	// Previous 是旧的容器记录。
	Previous Container
	// Disposition 是旧容器的处置方式。
	Disposition RestartDisposition
	// NewContainerName 是为新容器生成的安全名称。
	NewContainerName string
}

// PlanContainerRestart 生成重新开始失败任务的计划。
//
// 旧容器必须已脱离自动管理或已被删除时才能重新开始；正在被自动管理的容器
// 必须先显式处置，避免两个容器同时被视为同一任务的活动容器。
func PlanContainerRestart(
	previous Container,
	disposition RestartDisposition,
	newName string,
	at time.Time,
) (RestartPlan, error) {
	if err := disposition.Validate(); err != nil {
		return RestartPlan{}, err
	}
	if err := ValidateContainerName(newName); err != nil {
		return RestartPlan{}, err
	}
	if newName == previous.Name {
		return RestartPlan{}, invalidField(
			"ContainerLifecycle",
			"new_container_name",
			"新容器名称不能与旧容器相同，重新开始必须创建新容器",
		)
	}
	if previous.State == ContainerStateRemoved && disposition == RestartDispositionDelete {
		return RestartPlan{}, fmt.Errorf(
			"%w: 旧容器已删除，无需再次删除",
			ErrInvalidState,
		)
	}
	if previous.Detached && disposition == RestartDispositionDetach {
		return RestartPlan{}, fmt.Errorf("%w: 旧容器已脱离管理", ErrInvalidState)
	}
	return RestartPlan{
		Previous:         previous,
		Disposition:      disposition,
		NewContainerName: newName,
	}, nil
}
