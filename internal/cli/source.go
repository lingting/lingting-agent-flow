package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/spf13/cobra"
)

// newSourceCommand 构建 source 命令组。
func newSourceCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "source",
		Short: "在任务容器内部获取项目代码并初始化任务分支",
		Long: "代码通过容器内 Git Clone 直接获取到任务容器的工作目录，\n" +
			"不经过本机数据目录，也不使用宿主机 bind mount。\n" +
			"任务分支统一为 agent/<task-id>；本命令不实现 Commit、Push 或 Merge。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newSourceFetchCommand(a))
	return command
}

// newSourceFetchCommand 构建 source fetch 子命令。
func newSourceFetchCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var taskID string
	var repositoryURL string
	var baseBranch string
	var workDir string
	var timeout string
	command := &cobra.Command{
		Use:   "fetch <container>",
		Short: "在任务容器内克隆仓库并创建 agent/<task-id> 分支",
		Long: "执行顺序：核验容器 → 校验目标目录 → 校验 Git → Clone → 识别基础分支 →\n" +
			"初始化任务分支 → 校验工作区。\n" +
			"目标目录非空时直接失败，不会为了重试自动删除目录内容。\n" +
			"仓库认证与 AgentFlow 连接远程机器的 SSH 认证是两种凭据，本命令不会把本机\n" +
			"私钥或 SSH Agent 暴露给任务容器；需要认证的私有仓库会返回明确错误。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsedTimeout, err := parseOptionalDuration(timeout)
			if err != nil {
				return err
			}
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				provider, providerErr := credentialProviderForArgs(
					ctx,
					service.SSHService(),
					args[0],
					flags,
				)
				if providerErr != nil {
					return providerErr
				}
				result, fetchErr := service.FetchSource(ctx, app.FetchSourceRequest{
					ContainerID:   args[0],
					TaskID:        taskID,
					RepositoryURL: repositoryURL,
					BaseBranch:    baseBranch,
					WorkDir:       workDir,
					Timeout:       parsedTimeout,
					Credentials:   provider,
				})
				if printErr := printSourceResult(a, result); printErr != nil {
					return printErr
				}
				return fetchErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&taskID, "task", "", "任务标识，用于生成 agent/<task-id> 分支（必填）")
	command.Flags().StringVar(&repositoryURL, "repo", "", "仓库地址，ssh:// 或 https:// 形式（必填）")
	command.Flags().StringVar(&baseBranch, "base", "", "基础分支，省略时按 origin/HEAD、main、master 顺序识别")
	command.Flags().StringVar(
		&workDir,
		"workdir",
		"",
		"容器内目标目录，省略时使用 "+domain.DefaultContainerWorkDir,
	)
	command.Flags().StringVar(&timeout, "timeout", "", "本次代码获取的超时时间，例如 10m")
	for _, required := range []string{"task", "repo"} {
		if err := command.MarkFlagRequired(required); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// printSourceResult 输出代码获取结果。
func printSourceResult(a *app.App, result source.Result) error {
	if result.Repository == "" {
		return nil
	}
	lines := []string{
		fmt.Sprintf("仓库: %s", result.Repository),
		fmt.Sprintf("容器: %s", result.ContainerID),
		fmt.Sprintf("Docker 容器 ID: %s", emptyAsUnknown(result.DockerID)),
		fmt.Sprintf("容器内目录: %s", result.WorkDir),
		fmt.Sprintf("基础分支: %s（%s）", result.BaseBranch, result.BaseSelection),
		fmt.Sprintf("基础分支 commit: %s", emptyAsUnknown(result.BaseCommit)),
		fmt.Sprintf("任务分支: %s（%s）", result.TaskBranch, result.TaskBranchSource),
		fmt.Sprintf("当前 commit: %s", emptyAsUnknown(result.Commit)),
		fmt.Sprintf("耗时: %s", result.Duration.Round(1e6)),
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// parseOptionalDuration 解析可选的时长参数。
func parseOptionalDuration(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("解析时长 %q 失败: %w", raw, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("时长必须大于 0: %q", raw)
	}
	return parsed, nil
}
