package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/spf13/cobra"
)

// newAgentRunCommand 构建 agent run 子命令。
func newAgentRunCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var request app.ContainerAgentRequest
	var timeout string
	command := &cobra.Command{
		Use:   "run <container>",
		Short: "在任务容器内部执行一次 Pi 调用并把 Session 保存到本机",
		Long: "Pi 进程在指定任务容器内部启动，不会回退到本机执行。\n" +
			"每次调用都使用独立的 invocation ID 与独立的 Session 文件；原始 JSONL 会复制到\n" +
			"本机 Session 目录，HTML 导出失败不影响原始文件保存。\n" +
			"该命令只执行一次调用，不决定任务成功与否，也不会删除或接管容器。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsedTimeout, err := parseOptionalDuration(timeout)
			if err != nil {
				return err
			}
			cfg, cfgErr := a.ValidateConfig(cmd.Context(), hostConfigPath(cmd))
			if cfgErr != nil {
				return cfgErr
			}
			sessionRoot := strings.TrimSpace(request.SessionRoot)
			if sessionRoot == "" {
				sessionRoot = app.SessionRoot(cfg)
			}
			if sessionRoot == "" {
				return fmt.Errorf(
					"容器内 Agent 调用失败: 无法确定本机 Session 根目录，" +
						"请在配置中设置 storage.database_path 或显式传入 --session-root",
				)
			}
			request.SessionRoot = sessionRoot
			request.Timeout = parsedTimeout
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				request.ContainerID = args[0]
				result, runErr := service.RunAgent(ctx, request)
				if printErr := printContainerAgentResult(a, result); printErr != nil {
					return printErr
				}
				return runErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&request.TaskID, "task", "", "任务标识，用于确定 Session 目录归属")
	command.Flags().StringVar(&request.StepID, "step", "", "步骤标识，可为空")
	command.Flags().StringVar(&request.AttemptID, "attempt", "", "尝试标识，可为空")
	command.Flags().StringVar(&request.RoleType, "role-type", "", "角色类型，仅用于日志与诊断")
	command.Flags().StringVar(&request.Model, "model", "", "Pi 使用的模型标识（必填）")
	command.Flags().StringVar(&request.SystemPrompt, "system-prompt", "", "追加到默认系统提示词之后的内容")
	command.Flags().StringVar(&request.Prompt, "prompt", "", "发送给 Agent 的提示词（必填）")
	command.Flags().StringVar(
		&request.SessionRoot,
		"session-root",
		"",
		"本机 Session 根目录，省略时使用配置数据目录下的 sessions",
	)
	command.Flags().StringVar(
		&request.InvocationID,
		"invocation",
		"",
		"调用标识，省略时自动生成；指定时必须保证唯一",
	)
	command.Flags().StringVar(&timeout, "timeout", "", "本次调用的超时时间，例如 10m")
	for _, required := range []string{"model", "prompt"} {
		if err := command.MarkFlagRequired(required); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// printContainerAgentResult 输出容器内 Agent 调用结果。
//
// 只输出摘要与路径，不打印提示词或 Session 内容。
func printContainerAgentResult(a *app.App, result app.ContainerAgentResult) error {
	run := result.Result
	if run.SessionID == "" {
		return nil
	}
	lines := []string{
		fmt.Sprintf("容器: %s", emptyAsUnknown(run.ContainerID)),
		fmt.Sprintf("Docker 容器 ID: %s", emptyAsUnknown(run.DockerID)),
		fmt.Sprintf("调用标识: %s", emptyAsUnknown(run.InvocationID)),
		fmt.Sprintf("会话标识: %s", run.SessionID),
		fmt.Sprintf("模型: %s", emptyAsUnknown(run.Model)),
		fmt.Sprintf("状态: %s", run.Status),
		fmt.Sprintf("退出码: %d", run.ExitCode),
		fmt.Sprintf("Session 完整性: %s", emptyAsUnknown(string(run.Session.Completeness))),
		fmt.Sprintf("Session 文件: %s", emptyAsUnknown(run.Session.JSONLPath)),
		fmt.Sprintf("HTML 导出: %s", exportSummary(run.Session)),
		fmt.Sprintf("标准输出被截断: %s", flagMark(run.StdoutTruncated)),
		fmt.Sprintf("标准错误被截断: %s", flagMark(run.StderrTruncated)),
		fmt.Sprintf("开始时间: %s", run.StartedAt.Format(timeFormat)),
		fmt.Sprintf("结束时间: %s", run.FinishedAt.Format(timeFormat)),
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
