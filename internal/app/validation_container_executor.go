package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
)

// checkEnvironment 是确定性检查使用的执行环境。
//
// 它把"在哪里执行"与"执行什么"分开：验证引擎只持有已准备好的执行环境，
// 不再自己拼接 docker exec 参数，也不感知 SSH 或容器登记记录。
type checkEnvironment struct {
	port        execenv.ExecPort
	environment execenv.Environment
}

// newCheckEnvironment 构造检查执行环境。
func newCheckEnvironment(
	port execenv.ExecPort,
	environment execenv.Environment,
) (*checkEnvironment, error) {
	if port == nil {
		return nil, fmt.Errorf("检查执行环境初始化失败: 执行端口不能为空")
	}
	if err := environment.Validate(); err != nil {
		return nil, fmt.Errorf("检查执行环境初始化失败: %w", err)
	}
	return &checkEnvironment{port: port, environment: environment}, nil
}

// Exec 在任务容器内执行一条检查命令。
//
// 命令以参数切片传入，由执行环境层逐个转义，不经过 shell。工作目录必须是容器内
// 的安全绝对路径；超时同时由请求字段与 ctx 表达，取消会中断远程命令会话。
func (e *checkEnvironment) Exec(
	ctx context.Context,
	request CheckExecutionRequest,
) (CheckExecutionResult, error) {
	run, err := e.port.Exec(ctx, e.environment, execenv.ExecSpec{
		Command:        request.Command,
		Args:           request.Args,
		WorkDir:        request.WorkDir,
		Timeout:        checkExecTimeout(request.Timeout),
		MaxOutputBytes: request.MaxOutputBytes,
	})
	return CheckExecutionResult{
		ExitCode:  run.ExitCode,
		Stdout:    run.Stdout,
		Stderr:    run.Stderr,
		Truncated: run.Truncated,
		Duration:  run.Duration,
		TimedOut:  run.TimedOut(),
		Cancelled: run.Cancelled(),
		Certain:   run.Certain,
		Started:   run.Started,
		Error:     err,
	}, err
}

// Name 返回执行环境类型标识。
func (e *checkEnvironment) Name() string {
	return e.environment.Kind().String()
}

// Describe 返回可写入验证记录的执行环境描述。
func (e *checkEnvironment) Describe() string {
	return e.environment.Label()
}

// WorkDir 返回检查命令的默认工作目录。
func (e *checkEnvironment) WorkDir() string {
	return e.environment.WorkDir()
}

// containerExecPort 把容器命令执行能力适配为 execenv.ExecPort。
//
// 适配器只做协议转换：容器归属、管理状态与命令转义由容器服务负责；它拒绝与
// 执行环境不匹配的容器，避免验证命令被发到另一个容器。
type containerExecPort struct {
	port ValidationContainerPort
}

// newContainerExecPort 构造容器命令执行端口。
func newContainerExecPort(port ValidationContainerPort) (execenv.ExecPort, error) {
	if port == nil {
		return nil, fmt.Errorf("检查执行环境初始化失败: 容器能力不能为空")
	}
	return &containerExecPort{port: port}, nil
}

// Exec 在环境对应的容器内执行一条命令。
func (p *containerExecPort) Exec(
	ctx context.Context,
	environment execenv.Environment,
	spec execenv.ExecSpec,
) (execenv.ExecResult, error) {
	if err := environment.Validate(); err != nil {
		return execenv.ExecResult{}, err
	}
	resolved, err := spec.Resolve(environment)
	if err != nil {
		return execenv.ExecResult{}, err
	}
	ref := environment.Container()
	run, runErr := p.port.Exec(ctx, ExecRequest{
		ContainerID:    ref.ContainerID,
		Command:        resolved.Argv(),
		WorkDir:        resolved.WorkDir,
		Env:            resolved.Env,
		Timeout:        resolved.Timeout,
		MaxOutputBytes: resolved.MaxOutputBytes,
		Stdin:          resolved.Stdin,
	})
	return convertContainerExecResult(run, resolved), runErr
}

// convertContainerExecResult 把容器命令结果转换为执行环境结果。
func convertContainerExecResult(
	run ExecResult,
	spec execenv.ExecSpec,
) execenv.ExecResult {
	raw := run.Result
	result := execenv.ExecResult{
		Command:    strings.Join(spec.Argv(), " "),
		ExitCode:   raw.ExitCode,
		Stdout:     raw.Stdout,
		Stderr:     raw.Stderr,
		Truncated:  raw.Truncated,
		Duration:   raw.Duration,
		Started:    raw.Started,
		Certain:    raw.Certain,
		StdinBytes: raw.Stdin.Bytes,
		StdinError: raw.Stdin.Error,
	}
	switch {
	case raw.TimedOut():
		result.Status = execenv.ExecStatusTimeout
	case raw.Cancelled():
		result.Status = execenv.ExecStatusCancelled
	case !raw.Certain:
		result.Status = execenv.ExecStatusUnknown
	case raw.ExitCode == 0 && raw.Started:
		result.Status = execenv.ExecStatusSucceeded
	case raw.Started:
		result.Status = execenv.ExecStatusExitError
	default:
		result.Status = execenv.ExecStatusConnectionError
	}
	return result
}

// checkExecTimeout 返回检查执行的超时时间，零值使用默认值。
func checkExecTimeout(requested time.Duration) time.Duration {
	if requested > 0 {
		return requested
	}
	return defaultValidationCheckTimeout
}

// resolveValidationEnvironment 依据任务容器与工作目录解析本次验证的执行环境。
//
// 容器标识只从数据库记录中取得：调用方不能指定容器、工作目录或任意命令行。
func resolveValidationEnvironment(
	container domain.Container,
	workDir string,
) (execenv.Environment, error) {
	ref, err := execenv.ContainerRefFrom(container)
	if err != nil {
		return execenv.Environment{}, err
	}
	workspace, err := execenv.NewWorkspace(workDir, "", nil)
	if err != nil {
		return execenv.Environment{}, err
	}
	return execenv.NewEnvironment(
		execenv.KindRemoteContainer,
		workspace,
		ref,
		time.Now().UTC(),
	)
}

// 编译期确认适配器满足执行环境契约。
var _ execenv.ExecPort = (*containerExecPort)(nil)

// NewCheckEnvironmentForTest 构造检查执行环境，仅供测试与诊断使用。
//
// 它不改变任何业务规则：容器核验与命令转义仍由容器服务负责，工作目录仍必须是
// 容器内 /workspace 之下的安全绝对路径。
func NewCheckEnvironmentForTest(
	port ValidationContainerPort,
	ref execenv.ContainerRef,
	workDir string,
) (CheckExecutor, error) {
	execPort, err := newContainerExecPort(port)
	if err != nil {
		return nil, err
	}
	workspace, err := execenv.NewWorkspace(workDir, "", nil)
	if err != nil {
		return nil, err
	}
	environment, err := execenv.NewEnvironment(
		execenv.KindRemoteContainer,
		workspace,
		ref,
		time.Now().UTC(),
	)
	if err != nil {
		return nil, err
	}
	return newCheckEnvironment(execPort, environment)
}
