package pi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/process"
)

// executableName 是 Pi CLI 的可执行文件名。
const executableName = "pi"

// 启动 Pi 时使用的固定参数。
const (
	// argMode 选择输出模式，rpc 表示长生命周期 JSONL 协议。
	argMode = "--mode"
	// modeRPC 是 RPC 模式取值。
	modeRPC = "rpc"
	// argNoSession 表示不保存会话，每次执行使用独立进程与独立上下文。
	argNoSession = "--no-session"
	// argModel 指定本次执行使用的模型。
	argModel = "--model"
)

// processOptions 是 Pi 子进程的启动选项。
type processOptions struct {
	// Executable 是 Pi 可执行文件路径或 PATH 中的名称，为空时使用 pi。
	Executable string
	// Model 是本次执行使用的模型标识。
	Model string
	// WorkingDir 是子进程工作目录。
	WorkingDir string
	// Env 是子进程环境变量，nil 表示继承当前进程环境。
	Env []string
}

// arguments 返回 Pi 的启动参数。
//
// 每次执行都使用全新进程与 --no-session，避免不同开发步骤之间复用上下文。
func arguments(model string) []string {
	args := []string{argMode, modeRPC, argNoSession}
	if strings.TrimSpace(model) != "" {
		args = append(args, argModel, model)
	}
	return args
}

// processCommand 构造 Pi 子进程的启动描述。
func processCommand(options processOptions) process.Command {
	executable := strings.TrimSpace(options.Executable)
	if executable == "" {
		executable = executableName
	}
	return process.Command{
		Path: executable,
		Args: arguments(options.Model),
		Dir:  options.WorkingDir,
		Env:  options.Env,
	}
}

// encodeCommand 把一条 RPC 命令编码为以 LF 结尾的 JSONL 记录。
func encodeCommand(cmd command) ([]byte, error) {
	encoded, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("pi: 编码 RPC 命令失败: %w", err)
	}
	return append(encoded, '\n'), nil
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
