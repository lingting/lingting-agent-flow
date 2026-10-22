package cli

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/spf13/cobra"
)

// newAgentCommand 构建 agent 命令组。
func newAgentCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "agent",
		Short: "检查与调试 Agent 执行链路",
		Long: "检查 Pi 是否支持非交互式 CLI 执行，并用真实模型跑通一次独立 Session 的调用。\n" +
			"当前执行方式为 pi --print --session <file>，不使用 RPC 协议，也不使用 --no-session。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newAgentCheckCommand(a))
	command.AddCommand(newAgentRunCommand(a))
	return command
}

// newAgentCheckCommand 构建 agent check 子命令。
func newAgentCheckCommand(a *app.App) *cobra.Command {
	var model string
	command := &cobra.Command{
		Use:   "check",
		Short: "检查 Pi 安装与非交互式执行所需的参数",
		Long: "依次检查 pi 是否安装、版本是否可识别、帮助信息是否声明 --print 与 --session，\n" +
			"以及 --export 是否可用。指定 --model 时还会确认 Pi 是否识别该模型，\n" +
			"并查询该模型的凭据状态。该命令不发送提示词，因此不会调用模型。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := a.AgentCheck(cmd.Context(), model)
			if err != nil {
				return err
			}
			lines := []string{
				fmt.Sprintf("可执行程序: %s", report.Executable),
				fmt.Sprintf("版本: %s", report.Version),
				fmt.Sprintf("非交互模式 --print: %s", checkMark(report.PrintSupported)),
				fmt.Sprintf("会话参数 --session: %s", checkMark(report.SessionSupported)),
				fmt.Sprintf("导出参数 --export: %s", checkMark(report.ExportSupported)),
			}
			if report.Model != "" {
				lines = append(
					lines,
					fmt.Sprintf("模型: %s", report.Model),
					fmt.Sprintf("模型可识别: %s", checkMark(report.ModelRecognized)),
					fmt.Sprintf("凭据状态: %s", emptyAsUnknown(report.AuthStatus)),
				)
			}
			_, err = fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
			return err
		},
	}
	command.Flags().StringVar(&model, "model", "", "同时检查的模型标识，例如 provider/model-id")
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

// checkMark 把布尔检查结果转换为可读标记。
func checkMark(ok bool) string {
	if ok {
		return "通过"
	}
	return "失败"
}

// emptyAsUnknown 把空字符串转换为可读占位符。
func emptyAsUnknown(value string) string {
	if value == "" {
		return "（无）"
	}
	return value
}
