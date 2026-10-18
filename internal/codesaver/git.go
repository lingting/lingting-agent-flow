package codesaver

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// Git 可执行文件名与固定参数。
const (
	// gitExecutable 是容器内 Git 命令名。
	gitExecutable = "git"
	// defaultRemoteName 是默认使用的 Git 远程名称。
	defaultRemoteName = "origin"
)

// gitRunner 在容器内执行 Git 命令。
//
// 所有命令都以参数切片形式给出，由执行器负责转义；本类型不拼接 shell 字符串，
// 因此提交信息、分支名与路径中的特殊字符不会被 shell 解释。
type gitRunner struct {
	executor     ContainerExecutor
	dockerID     string
	workDir      string
	probeTimeout time.Duration
}

// run 在容器内执行一条 Git 命令并返回原始结果。
func (g *gitRunner) run(
	ctx context.Context,
	timeout time.Duration,
	args ...string,
) (docker.CommandResult, error) {
	if timeout <= 0 {
		timeout = g.probeTimeout
	}
	command := append([]string{gitExecutable, "-C", g.workDir}, args...)
	return g.executor.ExecInContainer(ctx, g.dockerID, command, timeout)
}

// output 在容器内执行一条 Git 查询命令并返回标准输出。
func (g *gitRunner) output(
	ctx context.Context,
	stage string,
	code ErrorCode,
	args ...string,
) (string, error) {
	result, err := g.run(ctx, 0, args...)
	if err != nil {
		return "", classifyExecError(stage, err, result)
	}
	if !result.Succeeded() {
		return "", newError(
			code,
			stage,
			"git "+firstArg(args)+" 失败: "+summarizeOutput(result),
			nil,
		)
	}
	return result.Stdout, nil
}

// firstArg 返回参数列表中第一个可读的子命令名，用于诊断信息。
func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// classifyExecError 把执行层错误转换为代码保存错误。
//
// 取消与超时优先判断：远程执行被中断时输出往往不完整，不能据此推断 Git 真的
// 执行失败，因此统一标记为结果不确定，要求调用方先核验实际状态。
func classifyExecError(stage string, err error, result docker.CommandResult) error {
	if err == nil {
		return nil
	}
	code := ErrorCodeOf(err)
	if code != CodeOK {
		// 已经带类别的错误不再重复包装。
		return err
	}
	if result.TimedOut() || errors.Is(err, context.DeadlineExceeded) {
		return newError(CodeTimeout, stage, "容器内 Git 命令超时", err)
	}
	if result.Cancelled() || errors.Is(err, context.Canceled) {
		return newError(CodeInterrupted, stage, "容器内 Git 命令被调用方取消", err)
	}
	if result.Uncertain() {
		return newError(
			CodeStateUncertain,
			stage,
			"容器内 Git 命令未能确认结束，请先核验实际状态",
			err,
		)
	}
	return newError(CodeWorktreeInvalid, stage, "容器内 Git 命令执行失败", err)
}

// summarizeOutput 生成命令输出的有界摘要。
//
// 只保留末尾若干行并限制长度：Git 输出可能包含大量文件名，直接打印既冗长也
// 可能泄露不必要的细节。
func summarizeOutput(result docker.CommandResult) string {
	combined := strings.TrimSpace(result.Stderr + "\n" + result.Stdout)
	if combined == "" {
		return "退出码 " + itoa(result.ExitCode)
	}
	lines := strings.Split(combined, "\n")
	const keepLines = 5
	if len(lines) > keepLines {
		lines = lines[len(lines)-keepLines:]
	}
	summary := strings.Join(lines, " | ")
	const limit = 500
	if len(summary) > limit {
		return summary[:limit] + "..."
	}
	return summary
}

// itoa 把整数转换为十进制字符串，避免为一次诊断引入额外依赖。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}

// trimRefs 去掉 refs/heads/ 与 refs/remotes/<remote>/ 前缀。
func trimRefs(value, remote string) string {
	trimmed := strings.TrimSpace(value)
	for _, prefix := range []string{
		"refs/heads/",
		"refs/remotes/" + remote + "/",
		remote + "/",
	} {
		trimmed = strings.TrimPrefix(trimmed, prefix)
	}
	return trimmed
}

// validCommit 判断字符串是否是合法的 commit SHA。
func validCommit(value string) bool {
	return domain.ValidateGitCommitSHA("commit", value) == nil
}
