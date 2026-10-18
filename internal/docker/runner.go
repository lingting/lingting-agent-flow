package docker

import (
	"context"
	"fmt"
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
	// Certain 表示可以确认远程命令已经结束。
	//
	// 超时、取消或连接中断时为 false：此时远程命令可能已经执行完成，
	// 调用方必须先核验远程状态，不得直接重试可能产生副作用的命令。
	Certain bool
}

// Succeeded 判断命令是否以退出码 0 正常结束。
func (r CommandResult) Succeeded() bool {
	return r.Certain && r.Status == sshclient.CommandStatusSucceeded && r.ExitCode == 0
}

// Uncertain 判断远程执行结果是否不确定。
func (r CommandResult) Uncertain() bool {
	return !r.Certain
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
	if strings.TrimSpace(command) == "" {
		return CommandResult{}, newError(CodeInvalidArgument, op, "远程命令不能为空", nil)
	}
	if timeout <= 0 {
		timeout = r.queryTimeout
	}
	result, err := r.client.Run(ctx, command, sshclient.RunOptions{Timeout: timeout})
	converted := CommandResult{
		Command:   command,
		ExitCode:  result.ExitCode,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		Truncated: result.StdoutTruncated || result.StderrTruncated,
		Duration:  result.Duration,
		Status:    result.Status,
		Certain:   result.TerminationConfirmed,
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

// RunQuery 执行查询类命令，使用查询超时。
func (r *Runner) RunQuery(ctx context.Context, op, command string) (CommandResult, error) {
	return r.Run(ctx, op, command, r.queryTimeout)
}

// RunMutate 执行变更类命令，使用变更超时。
func (r *Runner) RunMutate(ctx context.Context, op, command string) (CommandResult, error) {
	return r.Run(ctx, op, command, r.mutateTimeout)
}

// dockerCommand 构造 docker 子命令的远程命令字符串。
//
// 子命令与参数都经过校验与转义：子命令只能是固定字面量，参数逐个用
// POSIX shell 规则转义。
func dockerCommand(subcommand string, args ...string) (string, error) {
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
	command, err := sshclient.ShellCommand("docker", append([]string{subcommand}, args...)...)
	if err != nil {
		return "", newError(CodeInvalidArgument, "docker", "构造远程命令失败", err)
	}
	return command, nil
}
