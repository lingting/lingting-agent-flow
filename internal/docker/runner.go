package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
)

// 远程命令的默认超时。
const (
	// DefaultQueryTimeout 是轻量查询命令（version、inspect、ls）的超时时间。
	DefaultQueryTimeout = 60 * time.Second
	// DefaultMutateTimeout 是创建、启动、停止、删除容器的超时时间。
	DefaultMutateTimeout = 180 * time.Second
	// DefaultBuildTimeout 是镜像构建的超时时间。
	DefaultBuildTimeout = 45 * time.Minute
)

// Runner 在一条已认证的 SSH 连接上执行远程 Docker 命令。
//
// Runner 不持有连接的所有权：连接的建立与关闭由调用方负责。它只保证命令构造
// 使用固定子命令与转义后的参数，并把执行结果统一转换为带分类的错误。
type Runner struct {
	client sshclient.Client
	// queryTimeout 是查询命令的超时时间。
	queryTimeout time.Duration
	// mutateTimeout 是变更命令的超时时间。
	mutateTimeout time.Duration
}

// RunnerOptions 约束 Runner 的构造。
type RunnerOptions struct {
	// QueryTimeout 覆盖查询命令超时，零值使用 DefaultQueryTimeout。
	QueryTimeout time.Duration
	// MutateTimeout 覆盖变更命令超时，零值使用 DefaultMutateTimeout。
	MutateTimeout time.Duration
}

// NewRunner 在已认证连接上构造 Docker 命令执行器。
func NewRunner(client sshclient.Client, options RunnerOptions) (*Runner, error) {
	if client == nil {
		return nil, fmt.Errorf("docker: 连接不能为空")
	}
	queryTimeout := options.QueryTimeout
	if queryTimeout <= 0 {
		queryTimeout = DefaultQueryTimeout
	}
	mutateTimeout := options.MutateTimeout
	if mutateTimeout <= 0 {
		mutateTimeout = DefaultMutateTimeout
	}
	return &Runner{client: client, queryTimeout: queryTimeout, mutateTimeout: mutateTimeout}, nil
}

// CommandResult 是一条远程 Docker 命令的执行结果。
type CommandResult struct {
	// Command 是实际执行的命令字符串。
	Command string
	// ExitCode 是远程退出码，未能取得时为 -1。
	ExitCode int
	// Stdout 是标准输出。
	Stdout string
	// Stderr 是标准错误。
	Stderr string
	// Truncated 表示输出因超过上限被截断。
	Truncated bool
	// Duration 是执行耗时。
	Duration time.Duration
	// Status 是 sshclient 的结果状态。
	Status sshclient.CommandStatus
	// Stdin 是标准输入的写入结果，未提供输入时状态为 none。
	Stdin sshclient.StdinResult
	// Certain 表示可以确认远程命令已经结束。
	//
	// 超时、取消或连接中断时为 false：此时远程命令可能已经执行完成，
	// 调用方必须先核验远程状态，不得直接重试可能产生副作用的命令。
	Certain bool
	// Started 表示远程命令确实被提交执行过。
	//
	// 会话创建失败或上下文已取消时保持 false：此时退出码与输出都没有意义，
	// 调用方必须区分"命令执行失败"与"命令根本没跑起来"。
	Started bool
}

// Succeeded 判断命令是否以退出码 0 正常结束。
func (r CommandResult) Succeeded() bool {
	return r.Certain && r.Status == sshclient.CommandStatusSucceeded && r.ExitCode == 0
}

// Uncertain 判断远程执行结果是否不确定。
func (r CommandResult) Uncertain() bool {
	return !r.Certain
}

// TimedOut 判断命令是否因超时被中断。
func (r CommandResult) TimedOut() bool {
	return r.Status == sshclient.CommandStatusTimeout
}

// Cancelled 判断命令是否因调用方取消被中断。
func (r CommandResult) Cancelled() bool {
	return r.Status == sshclient.CommandStatusCancelled
}

// Run 执行一条固定的远程命令字符串。
//
// 调用方必须使用 sshclient.ShellCommand 或本包提供的构造函数生成命令，
// 不允许把用户输入直接拼接进命令字符串。
func (r *Runner) Run(
	ctx context.Context,
	op string,
	command string,
	timeout time.Duration,
) (CommandResult, error) {
	return r.runWithInput(ctx, op, command, nil, timeout)
}

// RunWithInput 执行一条固定的远程命令字符串并向其标准输入写入内容。
//
// input 为 nil 时等价于 Run。写入结果通过 CommandResult.Stdin 报告，
// 写入失败不改变命令本身的退出码判定。
func (r *Runner) RunWithInput(
	ctx context.Context,
	op string,
	command string,
	input io.Reader,
	timeout time.Duration,
) (CommandResult, error) {
	return r.runWithInput(ctx, op, command, input, timeout)
}

// runWithInput 是 Run 与 RunWithInput 的共同实现。
func (r *Runner) runWithInput(
	ctx context.Context,
	op string,
	command string,
	input io.Reader,
	timeout time.Duration,
) (CommandResult, error) {
	if strings.TrimSpace(command) == "" {
		return CommandResult{}, newError(CodeInvalidArgument, op, "远程命令不能为空", nil)
	}
	if timeout <= 0 {
		timeout = r.queryTimeout
	}
	var (
		result sshclient.CommandResult
		err    error
	)
	if input == nil {
		result, err = r.client.Run(ctx, command, sshclient.RunOptions{Timeout: timeout})
	} else {
		result, err = r.client.RunWithInput(
			ctx,
			command,
			input,
			sshclient.RunOptions{Timeout: timeout},
		)
	}
	converted := CommandResult{
		Command:   command,
		ExitCode:  result.ExitCode,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		Truncated: result.StdoutTruncated || result.StderrTruncated,
		Duration:  result.Duration,
		Status:    result.Status,
		Stdin:     result.Stdin,
		Certain:   result.TerminationConfirmed,
		Started:   result.Started,
	}
	if err != nil {
		// 传输层错误：超时、取消、连接中断。此时远程是否已经执行无法确认。
		code := CodeSSHUnavailable
		if result.TimedOut() {
			code = CodeStateUncertain
		}
		if result.Cancelled() {
			code = CodeStateUncertain
		}
		return converted, newError(code, op, "远程命令未能确认结束", err)
	}
	if converted.Succeeded() {
		return converted, nil
	}
	return converted, converted.commandError(op)
}

// commandError 把非零退出码转换为带分类的错误。
func (r CommandResult) commandError(op string) error {
	combined := strings.TrimSpace(r.Stderr + "\n" + r.Stdout)
	code := ClassifyDockerMessage(combined)
	detail := fmt.Sprintf("退出码 %d", r.ExitCode)
	if summary := summarizeOutput(combined); summary != "" {
		detail += ": " + summary
	}
	return newError(code, op, detail, nil)
}

// runWithOutputLimit 以显式输出上限执行一条远程命令。
//
// 它只被需要限制输出的调用路径使用（例如确定性验证检查）：其余路径保持
// sshclient 的默认上限，避免无意中改变既有行为。
func (r *Runner) runWithOutputLimit(
	ctx context.Context,
	command string,
	timeout time.Duration,
	maxOutputBytes int,
) (sshclient.CommandResult, error) {
	if strings.TrimSpace(command) == "" {
		return sshclient.CommandResult{}, newError(
			CodeInvalidArgument,
			"docker",
			"远程命令不能为空",
			nil,
		)
	}
	if timeout <= 0 {
		timeout = r.queryTimeout
	}
	return r.client.Run(ctx, command, sshclient.RunOptions{
		Timeout:        timeout,
		MaxOutputBytes: maxOutputBytes,
	})
}

// RunQuery 执行查询类命令，使用查询超时。
func (r *Runner) RunQuery(ctx context.Context, op, command string) (CommandResult, error) {
	return r.Run(ctx, op, command, r.queryTimeout)
}

// RunMutate 执行变更类命令，使用变更超时。
func (r *Runner) RunMutate(ctx context.Context, op, command string) (CommandResult, error) {
	return r.Run(ctx, op, command, r.mutateTimeout)
}

// dockerCommand 构造业务调用的 docker 子命令远程命令字符串。
//
// 所有业务调用统一显式设置 DOCKER_API_VERSION=<API 基准>，使实际请求固定在 AgentFlow
// 的 API 基准上，不依赖远端环境中的隐式默认值。固定版本与 Engine 不兼容时 Docker 会
// 直接返回错误，不会静默降级到旧 API 或升级到新 API。
//
// 子命令与参数都经过校验与转义：子命令只能是固定字面量，参数逐个用 POSIX shell 规则转义。
func dockerCommand(subcommand string, args ...string) (string, error) {
	return dockerCommandWithEnv(
		[]string{dockerAPIVersionEnv + "=" + EngineAPIBaseVersion},
		subcommand,
		args...,
	)
}

// dockerProbeCommand 构造版本探测使用的 docker 子命令远程命令字符串。
//
// 探测阶段必须隔离远端可能遗留的 DOCKER_API_VERSION：该变量会关闭 CLI 的自动版本协商，
// 使探测结果无法反映真实的客户端与服务端能力。因此这里显式取消该变量，探测与业务调用的
// 环境变量策略完全分离。
func dockerProbeCommand(subcommand string, args ...string) (string, error) {
	return dockerCommandWithEnv([]string{"-u", dockerAPIVersionEnv}, subcommand, args...)
}

// dockerCommandWithEnv 构造带环境变量控制的 docker 子命令远程命令字符串。
//
// env 中的每一项都会被单独转义；它只用于明确设置或取消 DOCKER_API_VERSION，
// 不接受来自业务数据的任意环境变量。
func dockerCommandWithEnv(env []string, subcommand string, args ...string) (string, error) {
	if strings.TrimSpace(subcommand) == "" {
		return "", newError(CodeInvalidArgument, "docker", "docker 子命令不能为空", nil)
	}
	for _, char := range subcommand {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= '0' && char <= '9':
		case char == '-' || char == '_':
		default:
			return "", newError(
				CodeInvalidArgument,
				"docker",
				fmt.Sprintf("docker 子命令 %q 含有非法字符", subcommand),
				nil,
			)
		}
	}
	parts := make([]string, 0, len(env)+len(args)+2)
	parts = append(parts, env...)
	parts = append(parts, "docker", subcommand)
	parts = append(parts, args...)
	command, err := sshclient.ShellCommand("env", parts...)
	if err != nil {
		return "", newError(CodeInvalidArgument, "docker", "构造远程命令失败", err)
	}
	return command, nil
}
