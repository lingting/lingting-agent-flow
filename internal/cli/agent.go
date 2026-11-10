package cli

import (
	"fmt"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/spf13/cobra"
)

// newAgentCommand 构建 agent 命令组。
func newAgentCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "agent",
		Short: "在任务容器内执行 Agent 调用",
		Long: "在任务容器内执行一次 Pi 调用，并把 Session 保存到本机。\n" +
			"当前执行方式为 pi --print --session <file>，不使用 RPC 协议，也不使用 --no-session。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newAgentRunCommand(a))
	return command
}

// exportSummary 生成 HTML 导出的可读摘要。
func exportSummary(files agent.SessionFiles) string {
	switch files.ExportStatus {
	case agent.ExportSucceeded:
		return fmt.Sprintf("%s (%s)", files.HTMLPath, files.ExportStatus)
	case agent.ExportFailed:
		return fmt.Sprintf("失败 (%s)", files.ExportStatus)
	default:
		return fmt.Sprintf("未尝试 (%s)", files.ExportStatus)
	}
}

// timeFormat 是命令输出的时间格式。
const timeFormat = "2006-01-02T15:04:05Z07:00"

// emptyAsUnknown 把空字符串转换为可读占位符。
func emptyAsUnknown(value string) string {
	if value == "" {
		return "（无）"
	}
	return value
}
