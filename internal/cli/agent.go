package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
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
	command.AddCommand(newAgentTestCommand(a))
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

// newAgentTestCommand 构建 agent test 子命令。
func newAgentTestCommand(a *app.App) *cobra.Command {
	var request app.AgentTestRequest
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "test",
		Short: "使用指定角色执行一次真实的 Agent 调用",
		Long: "以角色配置的模型与提示词启动一次独立的 Pi 非交互式调用。\n" +
			"每次调用使用独立 Session 文件，保存到配置数据目录的 sessions 下，\n" +
			"并在成功后尝试导出 HTML。事件写入日志，不污染标准输出。\n" +
			"该命令用于验证执行链路，不代表工作流或验证阶段已实现。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			request.Timeout = timeout
			result, runErr := a.AgentTest(cmd.Context(), request)
			printErr := printAgentTestResult(a, result)
			if printErr != nil {
				return printErr
			}
			return runErr
		},
	}
	command.Flags().StringVar(
		&request.ConfigPath,
		"config",
		"",
		"配置文件路径（默认 "+config.DefaultPath+"）",
	)
	command.Flags().StringVar(&request.RoleID, "role", "", "使用的角色标识，省略时使用配置中第一个角色")
	command.Flags().StringVar(&request.Model, "model", "", "覆盖角色配置中的模型")
	command.Flags().StringVar(&request.Dir, "dir", "", "Agent 执行时的工作目录，省略时继承当前目录")
	command.Flags().StringVar(&request.Prompt, "prompt", "", "发送给 Agent 的提示词（必填）")
	command.Flags().DurationVar(&timeout, "timeout", 0, "本次调用的超时时间，省略时使用默认值")
	if err := command.MarkFlagRequired("prompt"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}

// printAgentTestResult 输出测试命令的结果摘要。
//
// 失败时只输出错误摘要，不把全部原始日志写入终端。
func printAgentTestResult(a *app.App, result app.AgentTestResult) error {
	run := result.Result
	lines := []string{
		fmt.Sprintf("会话标识: %s", result.SessionID),
		fmt.Sprintf("角色: %s (%s)", result.Role.Name, result.Role.Type),
		fmt.Sprintf("模型: %s", result.Role.Model),
		fmt.Sprintf("状态: %s", run.Status),
		fmt.Sprintf("退出码: %d", run.ExitCode),
		fmt.Sprintf("Session 完整性: %s", emptyAsUnknown(string(run.Session.Completeness))),
		fmt.Sprintf("Session 文件: %s", emptyAsUnknown(run.Session.JSONLPath)),
		fmt.Sprintf("HTML 导出: %s", exportSummary(run.Session)),
		fmt.Sprintf("开始时间: %s", run.StartedAt.Format(timeFormat)),
		fmt.Sprintf("结束时间: %s", run.FinishedAt.Format(timeFormat)),
		fmt.Sprintf("事件: %s", result.EventSummary()),
	}
	if run.Failure != "" {
		lines = append(lines, fmt.Sprintf("错误摘要: %s", run.Failure))
	}
	if run.Session.Error != "" && run.Session.Error != run.Failure {
		lines = append(lines, fmt.Sprintf("Session 说明: %s", run.Session.Error))
	}
	if run.FinalText != "" {
		lines = append(lines, "最终文本:", run.FinalText)
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
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
