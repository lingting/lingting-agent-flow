package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/spf13/cobra"
)

// newAgentRunCommand 构建 agent run 子命令。
func newAgentRunCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var request app.ContainerAgentRequest
	var timeout string
	// permissions、allowTools 与 denyTools 是本次调用的角色工具策略输入，
	// 由 domain 统一校验角色能力上限，子命令不自行判定权限。
	var permissions string
	var allowTools string
	var denyTools string
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
			policy, policyErr := parseToolsPolicy(
				strings.TrimSpace(request.RoleType),
				permissions,
				allowTools,
				denyTools,
			)
			if policyErr != nil {
				return policyErr
			}
			request.ToolsPolicy = policy
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
	command.Flags().StringVar(
		&request.RoleType,
		"role-type",
		"",
		"角色类型（必填）：决定本次调用的系统能力上限",
	)
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
	command.Flags().StringVar(
		&permissions,
		"permissions",
		"",
		"授权能力，逗号分隔：read、write、execute；省略表示不加载任何工具",
	)
	command.Flags().StringVar(
		&allowTools,
		"allow-tools",
		"",
		"允许的内置工具，逗号分隔；省略表示不按名称限制已授权的工具",
	)
	command.Flags().StringVar(
		&denyTools,
		"deny-tools",
		"",
		"拒绝的内置工具，逗号分隔；拒绝优先于允许",
	)
	command.Flags().StringVar(&timeout, "timeout", "", "本次调用的超时时间，例如 10m")
	for _, required := range []string{"role-type", "model", "prompt"} {
		if err := command.MarkFlagRequired(required); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// parseToolsPolicy 把命令行参数转换为角色工具策略。
//
// 只做参数解析与校验：能力到内置工具的映射、角色能力上限与实际生效工具都由 domain
// 统一计算，子命令不构造 Pi 参数，也不自行判定权限。
func parseToolsPolicy(
	roleType, permissions, allowTools, denyTools string,
) (domain.ToolsPolicy, error) {
	values := make([]domain.ToolPermission, 0, 3)
	for _, name := range splitCSV(permissions) {
		permission := domain.ToolPermission(name)
		if err := permission.Validate(); err != nil {
			return domain.ToolsPolicy{}, fmt.Errorf("--permissions 取值非法: %w", err)
		}
		values = append(values, permission)
	}
	policy := domain.ToolsPolicy{
		Permissions: values,
		AllowTools:  splitCSV(allowTools),
		DenyTools:   splitCSV(denyTools),
	}
	if err := domain.ValidateToolsPolicyForRole(domain.RoleType(roleType), policy); err != nil {
		return domain.ToolsPolicy{}, fmt.Errorf("工具策略取值非法: %w", err)
	}
	return policy, nil
}

// splitCSV 把逗号分隔的参数拆成去空白的非空取值。
func splitCSV(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	items := make([]string, 0, len(trimmed))
	for _, item := range strings.Split(trimmed, ",") {
		if name := strings.TrimSpace(item); name != "" {
			items = append(items, name)
		}
	}
	return items
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
