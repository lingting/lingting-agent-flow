package cli

import (
	"fmt"

	"github.com/lingting/agentflow/internal/app"
	"github.com/spf13/cobra"
)

// newAgentCommand 构建 agent 命令组。
func newAgentCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "agent",
		Short: "检查与调试 Agent 执行链路",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newAgentCheckCommand(a))
	command.AddCommand(newAgentTestCommand(a))
	return command
}

// newAgentCheckCommand 构建 agent check 子命令。
func newAgentCheckCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "检查 Pi 安装与 RPC 协议是否可用",
		Long: "依次检查 pi 是否安装、当前版本是否声明 rpc 输出模式、\n" +
			"get_state 请求是否返回成功响应，以及进程能否正常退出。\n" +
			"该命令不发送提示词，因此不会调用模型。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := a.AgentCheck(cmd.Context())
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				a.Stdout(),
				"可执行程序: %s\n版本: %s\nRPC 模式: %s\nget_state 握手: %s\nPi 会话标识: %s\n退出码: %d\n",
				report.Executable,
				report.Version,
				checkMark(report.RPCModeSupported),
				checkMark(report.StateHandshake),
				emptyAsUnknown(report.ExternalSessionID),
				report.ExitCode,
			)
			return err
		},
	}
}

// newAgentTestCommand 构建 agent test 子命令。
func newAgentTestCommand(a *app.App) *cobra.Command {
	var request app.AgentTestRequest
	command := &cobra.Command{
		Use:   "test",
		Short: "通过统一 Runner 执行一次真实的 Agent 调用",
		Long: "以指定模型与工作目录启动独立的 Pi 进程，发送提示词并等待执行结束。\n" +
			"该命令直接调用 Runner，不写入数据库；事件逐条输出到标准错误流。\n" +
			"它用于验证执行链路，不代表工作流或验证阶段已实现。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, runErr := a.AgentTest(cmd.Context(), request)
			_, err := fmt.Fprintf(
				a.Stdout(),
				"会话标识: %s\nPi 会话标识: %s\n状态: %s\n退出码: %d\n开始时间: %s\n结束时间: %s\n事件: %s\n最终文本:\n%s\n",
				result.SessionID,
				emptyAsUnknown(result.Result.ExternalSessionID),
				result.Result.Status,
				result.Result.ExitCode,
				result.Result.StartedAt.Format(timeFormat),
				result.Result.FinishedAt.Format(timeFormat),
				result.EventSummary(),
				emptyAsUnknown(result.Result.FinalText),
			)
			if err != nil {
				return err
			}
			return runErr
		},
	}
	command.Flags().StringVar(&request.Model, "model", "", "使用的模型标识，例如 provider/model-id（必填）")
	command.Flags().StringVar(&request.Dir, "dir", "", "Agent 执行时的工作目录（必填）")
	command.Flags().StringVar(&request.Prompt, "prompt", "", "发送给 Agent 的提示词（必填）")
	for _, name := range []string{"model", "dir", "prompt"} {
		if err := command.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
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
