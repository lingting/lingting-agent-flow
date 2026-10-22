package cli

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/server"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/spf13/cobra"
)

// newServerCommand 构建 server 命令。
//
// 它是 Web 服务与后台服务的统一启动入口：同一个进程内初始化配置、数据库与后台服务，
// 再启动 HTTP 服务，并按逆序完成优雅关闭。
func newServerCommand(a *app.App) *cobra.Command {
	var (
		configPath   string
		address      string
		requireWebUI bool
	)
	command := &cobra.Command{
		Use:   "server",
		Short: "启动 AgentFlow 服务（HTTP API、内嵌 Web 页面与后台服务）",
		Long: "启动 AgentFlow 完整应用服务。同一个进程内依次初始化配置、数据库与后台服务，\n" +
			"随后提供 /api 接口与内嵌的前端页面，默认只监听 " + config.DefaultServerHost + ":" +
			fmt.Sprint(config.DefaultServerPort) + "。\n" +
			"可用 --address 或配置文件的 server 段覆盖监听地址，用 server.shutdown_timeout_seconds\n" +
			"设置优雅关闭超时。数据库初始化失败时服务不会启动。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(cmd, a, configPath, address, requireWebUI)
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "配置文件路径（默认 "+config.DefaultPath+"）")
	command.Flags().StringVar(&address, "address", "", "监听地址，覆盖配置文件的 server 段（例如 127.0.0.1:26380）")
	command.Flags().BoolVar(
		&requireWebUI,
		"require-web-ui",
		true,
		"缺少内嵌前端构建产物时直接启动失败；设为 false 时只提供 API 与诊断页",
	)
	return command
}

// runServer 构造统一服务实例并运行到退出。
//
// 执行节点环境预检依赖 SSH 服务：这里在打开数据库后构造 SSH 服务并注入 Server，
// 由 Server 统一把它接入后台校验服务，命令层不自行启动任何长期 goroutine。
func runServer(cmd *cobra.Command, a *app.App, configPath, address string, requireWebUI bool) error {
	ctx := cmd.Context()
	instance, err := server.New(server.Options{
		Address:   address,
		OpenStore: a.OpenStore,
		SSHFactory: func(store storage.Store, cfg *config.Config) (*app.SSHService, error) {
			return app.NewSSHService(store, a.Logger(), sshOptionsFrom(cfg))
		},
		StepExecutorFactory: func(
			store storage.Store,
			cfg *config.Config,
		) (*app.StepExecutor, error) {
			sessions, sessionsErr := app.NewSessionService(
				store,
				app.SessionRoot(cfg),
				a.Logger().Slog(),
			)
			if sessionsErr != nil {
				return nil, sessionsErr
			}
			sshService, sshErr := app.NewSSHService(store, a.Logger(), sshOptionsFrom(cfg))
			if sshErr != nil {
				return nil, sshErr
			}
			containers, containersErr := app.NewContainerService(app.DockerServiceOptions{
				SSH:    sshService,
				Store:  store,
				Logger: a.Logger(),
			})
			if containersErr != nil {
				return nil, containersErr
			}
			return app.NewStepExecutor(app.StepExecutorOptions{
				Store:      store,
				Containers: containers,
				Sessions:   sessions,
				// Git 工作区状态采集复用同一容器服务的只读核验能力：
				// 记录到的分支与 HEAD 必须与代码保存看到的是同一个工作区。
				Git:    containers,
				Logger: a.Logger().Slog(),
			})
		},
		ValidationFactory: func(
			store storage.Store,
			cfg *config.Config,
		) (*app.ValidationService, error) {
			sshService, sshErr := app.NewSSHService(store, a.Logger(), sshOptionsFrom(cfg))
			if sshErr != nil {
				return nil, sshErr
			}
			containers, containersErr := app.NewContainerService(app.DockerServiceOptions{
				SSH:    sshService,
				Store:  store,
				Logger: a.Logger(),
			})
			if containersErr != nil {
				return nil, containersErr
			}
			return app.NewValidationService(app.ValidationOptions{
				Store:      store,
				Containers: containers,
				OutputRoot: app.ValidationOutputRoot(cfg),
				Logger:     a.Logger().Slog(),
			})
		},
		CodeInitializeFactory: func(
			store storage.Store,
			cfg *config.Config,
		) (*app.CodeInitializeService, error) {
			sshService, sshErr := app.NewSSHService(store, a.Logger(), sshOptionsFrom(cfg))
			if sshErr != nil {
				return nil, sshErr
			}
			containers, containersErr := app.NewContainerService(app.DockerServiceOptions{
				SSH:    sshService,
				Store:  store,
				Logger: a.Logger(),
			})
			if containersErr != nil {
				return nil, containersErr
			}
			return app.NewCodeInitializeService(app.CodeInitializeOptions{
				Store:      store,
				Containers: containers,
				Logger:     a.Logger().Slog(),
			})
		},
		TaskWorkflowFactory: func(
			store storage.Store,
			cfg *config.Config,
			steps *app.StepWorkflowService,
		) (*app.TaskWorkflowService, error) {
			sshService, sshErr := app.NewSSHService(store, a.Logger(), sshOptionsFrom(cfg))
			if sshErr != nil {
				return nil, sshErr
			}
			containers, containersErr := app.NewContainerService(app.DockerServiceOptions{
				SSH:    sshService,
				Store:  store,
				Logger: a.Logger(),
			})
			if containersErr != nil {
				return nil, containersErr
			}
			sessions, sessionsErr := app.NewSessionService(
				store,
				app.SessionRoot(cfg),
				a.Logger().Slog(),
			)
			if sessionsErr != nil {
				return nil, sessionsErr
			}
			return app.NewTaskWorkflowService(app.TaskWorkflowOptions{
				Store: store,
				Steps: steps,
				// 容器只被核验与使用：编排器不创建、启动、删除或接管容器。
				Containers: containers,
				// 代码保存复用已有实现：编排器不构造 Git 命令。
				CodeSaver: containers,
				// 计划生成复用同一会话服务：每次 Planner 调用写入独立会话记录。
				Sessions: sessions,
				Logger:   a.Logger().Slog(),
			})
		},
		SystemInfo:   a.SystemInfo,
		RequireWebUI: requireWebUI,
		Logger:       a.Logger().Slog(),
		Report: func(message string) {
			// 启动信息同时写入标准输出（便于用户与自动化脚本读取实际端口）与结构化日志。
			_, _ = fmt.Fprintln(a.Stdout(), message)
			a.Logger().Slog().InfoContext(ctx, message)
		},
	})
	if err != nil {
		return err
	}
	return instance.Run(ctx, func(loadCtx context.Context) (*config.Config, error) {
		return a.ValidateConfig(loadCtx, configPath)
	})
}
