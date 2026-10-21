// Package cli 实现 AgentFlow 的 Cobra 命令。
//
// 命令层只负责参数解析、结果输出与退出码，业务规则位于 app 与各领域包。
package cli

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/app"
	"github.com/spf13/cobra"
)

// Execute 构建根命令并在 ctx 下执行，返回进程退出码。
//
// 错误只在此处输出一次，下层不再重复打印。
func Execute(ctx context.Context, a *app.App) int {
	root := NewRootCommand(a)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(a.Stderr(), "错误: %v\n", err)
		return 1
	}
	return 0
}

// NewRootCommand 构建根命令及其子命令。
func NewRootCommand(a *app.App) *cobra.Command {
	root := &cobra.Command{
		Use:   "agentflow",
		Short: "本地优先的 AI 编码工作流编排器",
		Long: "AgentFlow 把编码需求拆分为可执行步骤，并调度相互隔离的 Pi Agent 会话\n" +
			"完成规划、实现与验证。当前版本仅提供项目骨架与配置校验。",
		Version:       a.Build().Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate(a.Build().String())
	root.AddCommand(newVersionCommand(a))
	root.AddCommand(newConfigCommand(a))
	root.AddCommand(newAgentCommand(a))
	root.AddCommand(newHostCommand(a))
	root.AddCommand(newDockerCommand(a))
	root.AddCommand(newExecEnvCommand(a))
	root.AddCommand(newSourceCommand(a))
	root.AddCommand(newCodeCommand(a))
	root.AddCommand(newTaskCommand(a))
	root.AddCommand(newServerCommand(a))
	return root
}
