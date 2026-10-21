package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// containerCheckExecutor 把受管理容器内的命令执行能力适配为 CheckExecutor。
//
// 它只做协议转换：容器可用性核验、命令转义与结果状态由容器服务负责，单项判定与
// 结论计算由验证服务负责。执行环境固定为任务容器，绝不回退到宿主机当前目录执行
// 命令：一旦容器不可用，执行器返回明确错误而不是"在本机跑一下"。
type containerCheckExecutor struct {
	port ValidationContainerPort
	// containerID 是本次验证使用的容器登记记录 ID。
	containerID string
	// defaultWorkDir 是任务容器内的项目工作目录，作为检查的默认工作目录。
	defaultWorkDir string
}

// newContainerCheckExecutor 构造容器内检查执行器。
func newContainerCheckExecutor(
	port ValidationContainerPort,
	containerID, workDir string,
) (*containerCheckExecutor, error) {
	if port == nil {
		return nil, fmt.Errorf("容器内检查执行器初始化失败: 容器能力不能为空")
	}
	trimmedID := strings.TrimSpace(containerID)
	if trimmedID == "" {
		return nil, fmt.Errorf("容器内检查执行器初始化失败: 容器标识不能为空")
	}
	resolved := strings.TrimSpace(workDir)
	if resolved == "" {
		resolved = domain.DefaultContainerWorkDir
	}
	if err := domain.ValidateContainerWorkDir(resolved); err != nil {
		return nil, fmt.Errorf("容器内检查执行器初始化失败: %w", err)
	}
	return &containerCheckExecutor{
		port:           port,
		containerID:    trimmedID,
		defaultWorkDir: resolved,
	}, nil
}

// Name 返回执行环境类型标识。
func (e *containerCheckExecutor) Name() string {
	return "container"
}

// Exec 在任务容器内执行一条检查命令。
//
// 命令以参数切片传入，由容器执行层逐个转义，不经过 shell。工作目录必须是容器内
// 的安全绝对路径；超时同时由请求字段与 ctx 表达，取消会中断远程命令会话。
func (e *containerCheckExecutor) Exec(
	ctx context.Context,
	request CheckExecutionRequest,
) (CheckExecutionResult, error) {
	command := append([]string{request.Command}, request.Args...)
	workDir := strings.TrimSpace(request.WorkDir)
	if workDir == "" {
		workDir = e.defaultWorkDir
	}
	run, err := e.port.Exec(ctx, ExecRequest{
		ContainerID:    e.containerID,
		Command:        command,
		WorkDir:        workDir,
		Timeout:        checkExecTimeout(request.Timeout),
		MaxOutputBytes: request.MaxOutputBytes,
	})
	return CheckExecutionResult{
		ExitCode:  run.Result.ExitCode,
		Stdout:    run.Result.Stdout,
		Stderr:    run.Result.Stderr,
		Truncated: run.Result.Truncated,
		Duration:  run.Result.Duration,
		TimedOut:  run.Result.TimedOut(),
		Cancelled: run.Result.Cancelled(),
		Certain:   run.Result.Certain,
		Started:   run.Result.Started,
		Error:     err,
	}, err
}

// 编译期确认适配器满足检查执行器契约。
var _ CheckExecutor = (*containerCheckExecutor)(nil)

// checkExecTimeout 返回检查执行的超时时间，零值使用默认值。
func checkExecTimeout(requested time.Duration) time.Duration {
	if requested > 0 {
		return requested
	}
	return defaultValidationCheckTimeout
}
