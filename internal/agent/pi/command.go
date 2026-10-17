package pi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// processCommand 构造 Pi 子进程的启动描述。
//
// 每次执行都显式指定 --session，不使用 --no-session：Session 是本次调用的
// 审计记录，路径由调用方按任务、步骤与调用标识决定。
func processCommand(options processOptions) process.Command {
	executable := strings.TrimSpace(options.Executable)
	if executable == "" {
		executable = executableName
	}
	args := make([]string, 0, 8+len(options.PrefixArgs))
	args = append(args, options.PrefixArgs...)
	args = append(args, argPrint, argSession, options.SessionPath, argModel, options.Model)
	if prompt := strings.TrimSpace(options.SystemPrompt); prompt != "" {
		args = append(args, argAppendSystemPrompt, prompt)
	}
	return process.Command{
		Path: executable,
		Args: args,
		Dir:  options.WorkingDir,
		Env:  options.Env,
	}
}

// processOptions 是 Pi 子进程的启动选项。
type processOptions struct {
	// Executable 是 Pi 可执行文件路径或 PATH 中的名称。
	Executable string
	// PrefixArgs 是附加在 Pi 参数之前的额外参数。
	PrefixArgs []string
	// Model 是本次执行使用的模型标识。
	Model string
	// SystemPrompt 是追加到默认系统提示词之后的内容，可为空。
	SystemPrompt string
	// SessionPath 是本次执行使用的 Session 文件路径。
	SessionPath string
	// WorkingDir 是子进程工作目录。
	WorkingDir string
	// Env 是子进程环境变量，nil 表示继承当前进程环境。
	Env []string
}

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

// prepareSessionDir 创建本次调用的 Session 目录并清理上次遗留的同名文件。
//
// Session 文件由 Pi 创建；这里只保证父目录存在，并避免旧的同名文件被误当成本次结果。
func prepareSessionDir(paths SessionPaths) error {
	if err := paths.EnsureDir(); err != nil {
		return err
	}
	for _, path := range []string{paths.JSONL, paths.HTML} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("pi: 清理旧 Session 文件失败: %s: %w", path, err)
		}
	}
	return nil
}

// writePrompt 把提示词写入子进程 stdin 并立即关闭，避免 Pi 继续等待输入。
func writePrompt(supervisor *process.Supervisor, prompt string) error {
	payload := []byte(prompt)
	if !strings.HasSuffix(prompt, "\n") {
		payload = append(payload, '\n')
	}
	if err := supervisor.Write(payload); err != nil {
		_ = supervisor.CloseStdin()
		return fmt.Errorf("pi: 写入提示词失败: %w", err)
	}
	if err := supervisor.CloseStdin(); err != nil {
		return fmt.Errorf("pi: 关闭提示词输入失败: %w", err)
	}
	return nil
}

// drainTimeout 返回等待输出读取协程退出的上限。
func drainTimeout(grace time.Duration) time.Duration {
	if grace <= 0 {
		grace = DefaultGraceTimeout
	}
	return grace + readerDrainTimeout
}

// runOnce 运行一次短生命周期的 Pi 子进程并收集 stdout。
//
// 用于版本查询、帮助查询与 HTML 导出这类不需要提示词输入的调用。
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

// ensureParent 创建目标文件的父目录。
func ensureParent(path string) error {
	parent := filepath.Dir(path)
	if parent == "" || parent == "." {
		return nil
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("pi: 创建目录失败: %s: %w", parent, err)
	}
	return nil
}
