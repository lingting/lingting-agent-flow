package cli

import (
	"fmt"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/httpserver"
	"github.com/spf13/cobra"
)

// newWebCommand 构建 web 命令组。
func newWebCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "web",
		Short: "本地 Web 界面与 HTTP API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newWebServeCommand(a))
	return command
}

// newWebServeCommand 构建 web serve 子命令。
func newWebServeCommand(a *app.App) *cobra.Command {
	var (
		configPath string
		address    string
	)
	command := &cobra.Command{
		Use:   "serve",
		Short: "启动本地 HTTP 服务，同时提供 API 与内嵌的 Web 页面",
		Long: "启动本地 HTTP 服务。服务同时提供 /api/v1 接口与内嵌的前端页面，\n" +
			"默认只监听 " + config.DefaultServerHost + ":" +
			fmt.Sprint(config.DefaultServerPort) + "，可用 --address 或配置文件的 server 段覆盖。\n" +
			"数据库打开失败时服务仍然启动，健康检查会如实报告 degraded。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWebServe(cmd, a, configPath, address)
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "配置文件路径（默认 "+config.DefaultPath+"）")
	command.Flags().StringVar(&address, "address", "", "监听地址，覆盖配置文件的 server 段（例如 127.0.0.1:26380）")
	return command
}

// runWebServe 加载配置、打开存储并启动 HTTP 服务。
func runWebServe(cmd *cobra.Command, a *app.App, configPath, address string) error {
	ctx := cmd.Context()
	cfg, err := a.ValidateConfig(ctx, configPath)
	if err != nil {
		return err
	}
	store, storeErr := a.OpenStore(ctx, cfg)
	if storeErr != nil {
		// 存储不可用时不阻止服务启动：页面需要能打开，并如实显示后端状态。
		a.Logger().Slog().WarnContext(ctx, "数据库打开失败，健康检查将报告 degraded", "error", storeErr.Error())
	}
	if store != nil {
		defer func() {
			if closeErr := store.Close(); closeErr != nil {
				a.Logger().Slog().WarnContext(ctx, "关闭数据库失败", "error", closeErr.Error())
			}
		}()
	}
	server, err := httpserver.New(httpserver.Options{
		Address:    resolveAddress(cfg, address),
		Health:     app.NewHealthService(store),
		SystemInfo: a.SystemInfo,
		Logger:     a.Logger().Slog(),
		Report: func(message string) {
			// 启动信息同时写入标准输出（便于用户与自动化脚本读取实际端口）与结构化日志。
			_, _ = fmt.Fprintln(a.Stdout(), message)
			a.Logger().Slog().InfoContext(ctx, message)
		},
	})
	if err != nil {
		return err
	}
	return server.Run(ctx)
}

// resolveAddress 计算最终监听地址：命令行参数优先于配置文件。
func resolveAddress(cfg *config.Config, override string) string {
	if override != "" {
		return override
	}
	return cfg.Server.Address()
}
