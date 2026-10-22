package pi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/process"
)

// executableName 是 Pi CLI 的可执行文件名。
const executableName = "pi"

// Pi CLI 参数。取值以 pi --help 与官方 cli.md 为准。
const (
	// argPrint 选择非交互式单次执行模式。
	argPrint = "--print"
	// argSession 指定本次执行使用的 Session 文件路径。
	argSession = "--session"
	// argModel 指定本次执行使用的模型标识。
	argModel = "--model"
	// argAppendSystemPrompt 在默认系统提示词之后追加内容。
	argAppendSystemPrompt = "--append-system-prompt"
	// argExport 把已有 Session 文件导出为 HTML 后退出。
	argExport = "--export"
	// argVersion 打印版本号。
	argVersion = "--version"
	// argHelp 打印命令行帮助。
	argHelp = "--help"
	// argListModels 列出可用模型。
	argListModels = "--list-models"
	// argAuthCheck 检查指定模型的凭据可用性。
	argAuthCheck = "auth"
	// authSubcommandCheck 是凭据检查的子命令。
	authSubcommandCheck = "check"
	// argJSON 让凭据检查输出结构化结果。
	argJSON = "--json"
)

// exportArguments 返回导出 HTML 的参数。
//
// 导出在 Pi 主进程退出后单独执行，不会启动新的交互式 Pi 会话。
func exportArguments(input, output string) []string {
	return []string{argExport, input, output}
}

// resolveExecutable 检查 Pi 是否可用并返回其路径。
func resolveExecutable(executable string) (string, error) {
	name := strings.TrimSpace(executable)
	if name == "" {
		name = executableName
	}
	path, err := process.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("pi: 未找到可执行程序 %q: %w", name, err)
	}
	return path, nil
}

// runOnce 运行一次短生命周期的 Pi 子进程并收集 stdout。
//
// 用于版本查询、帮助查询与 HTML 导出这类不需要提示词输入的调用。容器内执行同样
// 使用它完成版本与帮助探测，因此它不依赖任何本机进程执行路径。
func runOnce(
	ctx context.Context,
	options Options,
	args []string,
	timeout time.Duration,
) (string, process.Exit, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return "", process.Exit{Code: -1}, err
	}
	executable := strings.TrimSpace(options.Executable)
	if executable == "" {
		executable = executableName
	}
	supervisor, err := process.Start(
		process.Command{Path: executable, Args: args, Env: options.Env},
		process.Options{GraceTimeout: options.GraceTimeout},
	)
	if err != nil {
		return "", process.Exit{Code: -1}, err
	}
	collected := make(chan collectedOutput, 1)
	go collectStdout(supervisor, collected)
	select {
	case output := <-collected:
		exit, shutdownErr := supervisor.Shutdown(options.GraceTimeout)
		if output.err != nil {
			return output.text, exit, output.err
		}
		return output.text, exit, shutdownErr
	case <-runCtx.Done():
		_, _ = supervisor.Shutdown(options.GraceTimeout)
		return "", process.Exit{Code: -1}, runCtx.Err()
	}
}

// collectedOutput 是收集到的子进程输出。
type collectedOutput struct {
	text string
	err  error
}

// collectStdout 读取子进程全部 stdout，直到输出流关闭。
func collectStdout(supervisor *process.Supervisor, result chan<- collectedOutput) {
	var builder strings.Builder
	for line := range supervisor.Lines() {
		if line.Err != nil {
			result <- collectedOutput{text: builder.String(), err: line.Err}
			return
		}
		builder.WriteString(line.Text)
		builder.WriteByte('\n')
	}
	result <- collectedOutput{text: builder.String()}
}
