package cli

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/spf13/cobra"
)

// newConfigCommand 构建 config 命令组。
func newConfigCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "查看与校验配置文件",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newConfigValidateCommand(a))
	return command
}

// newConfigValidateCommand 构建 config validate 子命令。
func newConfigValidateCommand(a *app.App) *cobra.Command {
	var path string
	command := &cobra.Command{
		Use:   "validate",
		Short: "校验配置文件的结构与取值",
		Long: "读取 YAML 配置并校验其结构、必需字段与取值约束。\n" +
			"未指定 --config 时读取 " + config.DefaultPath + "；配置文件不存在视为错误。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.ValidateConfig(cmd.Context(), path)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				a.Stdout(),
				"配置校验通过: %s\n应用: %s\n日志级别: %s\n角色: %s\n",
				cfg.SourcePath,
				cfg.App.Name,
				a.Logger().Level(),
				strings.Join(roleSummary(cfg.Roles), "、"),
			)
			return err
		},
	}
	command.Flags().StringVar(
		&path,
		"config",
		"",
		"配置文件路径（默认 "+config.DefaultPath+"）",
	)
	return command
}

// roleSummary 生成角色摘要，用于校验成功后的输出。
func roleSummary(roles []config.RoleConfig) []string {
	summary := make([]string, 0, len(roles))
	for _, role := range roles {
		summary = append(summary, fmt.Sprintf("%s(%s)", role.ID, role.Type))
	}
	return summary
}
