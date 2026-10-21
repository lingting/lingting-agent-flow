package execenv

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
)

// Retention 是释放执行环境时对资源的处置方式。
//
// 三种取值的语义与既有容器生命周期策略一致，不得互相替代：
// 任务成功删除容器，任务失败保留现场，用户中断默认保留但允许显式不保留。
type Retention string

// 资源处置方式取值。
const (
	// RetentionKeep 表示保留容器与现场，容器仍在 AgentFlow 管理范围内。
	RetentionKeep Retention = "keep"
	// RetentionDelete 表示删除容器与任务级临时资源。
	RetentionDelete Retention = "delete"
	// RetentionDetach 表示保留容器但脱离 AgentFlow 自动管理。
	RetentionDetach Retention = "detach"
)

// String 返回处置方式的字符串形式。
func (r Retention) String() string {
	return string(r)
}

// Validate 校验处置方式是否受支持。
func (r Retention) Validate() error {
	switch r {
	case RetentionKeep, RetentionDelete, RetentionDetach:
		return nil
	default:
		return fmt.Errorf("%w: 未知的资源处置方式 %q", ErrInvalidValue, string(r))
	}
}

// ReleaseRequest 是一次资源释放的输入。
type ReleaseRequest struct {
	// Retention 是处置方式。
	Retention Retention
	// Outcome 是任务结果分类，用于记录生命周期结束原因。
	Outcome domain.ContainerOutcome
	// KeepInterrupted 表示中断时仍然保留容器。
	KeepInterrupted bool
	// Note 是补充说明，只写入日志与结果，不进入数据库敏感字段。
	Note string
}

// ReleaseResult 是一次资源释放的结果。
//
// 它显式区分三件事：实际执行的处置、容器是否仍存在、以及是否发生失败。
// 调用方不得只凭 Release 返回 nil 就断定容器已被删除。
type ReleaseResult struct {
	// Retention 是实际执行的处置方式。
	Retention Retention
	// ContainerRemoved 表示容器确实已被删除。
	ContainerRemoved bool
	// ContainerRetained 表示容器被保留（仍在管理范围内或已脱离管理）。
	ContainerRetained bool
	// TaskResourcesRemoved 表示任务级临时资源已清理。
	//
	// 该标记永远不包含共享镜像、共享缓存卷与远程项目配置目录。
	TaskResourcesRemoved bool
	// Detail 是给用户看的可读说明。
	Detail string
	// Err 是失败原因，成功时为 nil。
	Err error
}

// Succeeded 判断释放是否成功。
func (r ReleaseResult) Succeeded() bool {
	return r.Err == nil
}

// DecideRetention 依据任务结果与调用方选择给出处置方式。
//
// 规则与 domain.DecideContainerLifecycle 一致：本函数只把结论映射为执行环境层的
// 处置方式，不重新实现业务规则，也不触碰共享镜像、共享缓存卷与项目管理目录。
func DecideRetention(
	outcome domain.ContainerOutcome,
	keepInterrupted bool,
) (Retention, error) {
	decision, err := domain.DecideContainerLifecycle(
		outcome,
		domain.ContainerLifecycleOptions{KeepInterrupted: keepInterrupted},
	)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	switch decision.Retention {
	case domain.ContainerRetentionDelete:
		return RetentionDelete, nil
	case domain.ContainerRetentionDetach:
		return RetentionDetach, nil
	case domain.ContainerRetentionKeep:
		return RetentionKeep, nil
	default:
		return "", fmt.Errorf(
			"%w: 未知的容器处置方式 %q",
			ErrInvalidValue,
			decision.Retention,
		)
	}
}

// ExecPort 是执行环境提供的最小命令执行能力。
//
// 验证引擎与 Pi Runner 只需要这一部分能力：它们不准备环境、不释放资源，
// 因此依赖更窄的接口比依赖完整 Provider 更容易被测试替换。
type ExecPort interface {
	// Exec 在环境内执行一条命令。
	Exec(ctx context.Context, environment Environment, spec ExecSpec) (ExecResult, error)
}
