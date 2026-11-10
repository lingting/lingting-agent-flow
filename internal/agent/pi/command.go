package pi

import (
	"strings"
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
	// argTools 用显式工具列表替换 Pi 的默认工具选择。
	argTools = "--tools"
	// argNoTools 让本次调用不加载任何内置、扩展、自定义或 MCP 工具。
	argNoTools = "--no-tools"
	// argNoMCP 关闭本次调用的内置 MCP 支持，MCP 工具不能绕过工具策略。
	argNoMCP = "--no-mcp"
	// argNoExtensions 关闭本次调用发现的、配置的与内置的扩展，避免扩展注册或覆盖工具。
	argNoExtensions = "--no-extensions"
	// argExport 把已有 Session 文件导出为 HTML 后退出。
	argExport = "--export"
	// argVersion 打印版本号。
	argVersion = "--version"
)

// exportArguments 返回导出 HTML 的参数。
//
// 导出在 Pi 主进程退出后单独执行，不会启动新的交互式 Pi 会话。
func exportArguments(input, output string) []string {
	return []string{argExport, input, output}
}

// toolPolicyArguments 返回限制本次调用可用工具的参数。
//
// 工具列表由调用方按角色工具策略计算后给出，这里只做参数转换：为空时使用
// --no-tools，绝不省略参数让 Pi 回退到默认工具列表。随后固定关闭 MCP 与扩展，
// 使它们无法绕过工具策略注册或被覆盖内置工具。
func toolPolicyArguments(tools []string) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if name := strings.TrimSpace(tool); name != "" {
			names = append(names, name)
		}
	}
	args := make([]string, 0, 4)
	if len(names) == 0 {
		args = append(args, argNoTools)
	} else {
		args = append(args, argTools, strings.Join(names, ","))
	}
	return append(args, argNoMCP, argNoExtensions)
}
