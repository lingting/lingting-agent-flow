package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/spf13/cobra"
)

// newDockerCommand 构建 docker 命令组。
func newDockerCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "docker",
		Short: "管理远程 Docker 环境、镜像版本与容器",
		Long: "通过已验证的 SSH 连接在远程 Linux 机器上操作 Docker，不开放 Docker TCP 端口，\n" +
			"也不要求本机访问远程 Docker Socket。\n" +
			"环境配置与镜像元数据保存在远程 AgentFlow 工作空间；Dockerfile 每次保存都会\n" +
			"生成新的不可覆盖版本，历史版本不会被覆盖。\n" +
			"任务容器默认不使用特权模式、不挂载宿主机 Docker Socket 与用户 Home，\n" +
			"项目代码只存在于容器内部，不绑定挂载到宿主机目录。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newDockerPreflightCommand(a))
	command.AddCommand(newDockerInitCommand(a))
	command.AddCommand(newDockerEnvCommand(a))
	command.AddCommand(newDockerContainerCommand(a))
	command.PersistentFlags().String(
		"config",
		"",
		"配置文件路径（默认 "+config.DefaultPath+"）",
	)
	return command
}

// newDockerPreflightCommand 构建 docker preflight 子命令。
func newDockerPreflightCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	command := &cobra.Command{
		Use:   "preflight <machine>",
		Short: "检查远程 Docker CLI、Engine 与使用权限",
		Long: "执行只读预检：docker CLI 是否存在、Engine 是否可访问、当前 SSH 用户是否有权\n" +
			"使用 Docker、工作空间是否可写、剩余空间是否足够。\n" +
			"该命令不会创建、启动或删除任何 Docker 资源，也不会尝试 sudo 或修改用户组、\n" +
			"Socket 权限等系统配置。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEnvironmentService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.EnvironmentService,
			) error {
				result, preflightErr := service.Preflight(ctx, app.InitOptions{
					MachineID: args[0],
				})
				if printErr := printPreflightResult(a, result); printErr != nil {
					return printErr
				}
				return preflightErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	return command
}

// newDockerInitCommand 构建 docker init 子命令。
func newDockerInitCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var languages []string
	command := &cobra.Command{
		Use:   "init <machine>",
		Short: "初始化远程 AgentFlow 环境目录",
		Long: "先执行 Docker 环境预检，通过后创建远程工作空间目录结构，并写入默认基础环境\n" +
			"版本；--languages 指定的语言环境会同时写入模板版本。\n" +
			"预检未通过时不会创建任何目录，避免留下半成品配置。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEnvironmentService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.EnvironmentService,
			) error {
				result, initErr := service.Init(ctx, app.InitOptions{
					MachineID:     args[0],
					WithLanguages: languages,
				})
				if printErr := printInitResult(a, result); printErr != nil {
					return printErr
				}
				return initErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringSliceVar(
		&languages,
		"languages",
		nil,
		"同时初始化的语言环境模板，可选 node、python、go",
	)
	return command
}

// newDockerEnvCommand 构建 docker env 命令组。
func newDockerEnvCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "env",
		Short: "管理远程环境定义与不可覆盖的配置版本",
		Long: "基础环境固定为 base，语言环境按名称区分。每次保存都会生成新的版本号，\n" +
			"已经保存的历史版本不会被覆盖；构建失败不会把版本标记为可用。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newDockerEnvListCommand(a))
	command.AddCommand(newDockerEnvShowCommand(a))
	command.AddCommand(newDockerEnvHistoryCommand(a))
	command.AddCommand(newDockerEnvEditCommand(a))
	command.AddCommand(newDockerEnvBuildCommand(a))
	command.AddCommand(newDockerEnvLogCommand(a))
	return command
}

// withEnvironmentService 加载配置并执行一次环境服务调用。
func withEnvironmentService(
	ctx context.Context,
	a *app.App,
	configPath string,
	run func(ctx context.Context, service *app.EnvironmentService) error,
) error {
	return withDockerServices(ctx, a, configPath, func(ctx context.Context, services dockerServices) error {
		return run(ctx, services.environments)
	})
}

// withContainerService 加载配置并执行一次容器服务调用。
func withContainerService(
	ctx context.Context,
	a *app.App,
	configPath string,
	run func(ctx context.Context, service *app.ContainerService) error,
) error {
	return withDockerServices(ctx, a, configPath, func(ctx context.Context, services dockerServices) error {
		return run(ctx, services.containers)
	})
}

// dockerServices 是一次命令执行所需的远程 Docker 服务集合。
type dockerServices struct {
	environments *app.EnvironmentService
	containers   *app.ContainerService
	// store 是持久化入口，供任务命令直接读写任务记录。
	store storage.Store
	// ssh 是 SSH 服务，供任务命令构造凭据提供器。
	ssh *app.SSHService
	// config 是本次命令使用的配置。
	config *config.Config
}

// withDockerServices 加载配置、打开数据库并构造远程 Docker 服务。
func withDockerServices(
	ctx context.Context,
	a *app.App,
	configPath string,
	run func(ctx context.Context, services dockerServices) error,
) error {
	cfg, err := a.ValidateConfig(ctx, configPath)
	if err != nil {
		return err
	}
	store, err := a.OpenStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			a.Logger().Slog().WarnContext(ctx, "关闭数据库失败", "error", closeErr)
		}
	}()
	sshService, err := app.NewSSHService(store, a.Logger(), sshOptionsFrom(cfg))
	if err != nil {
		return err
	}
	options := app.DockerServiceOptions{
		SSH:    sshService,
		Store:  store,
		Logger: a.Logger(),
	}
	environments, err := app.NewEnvironmentService(options)
	if err != nil {
		return err
	}
	containers, err := app.NewContainerService(options)
	if err != nil {
		return err
	}
	return run(ctx, dockerServices{
		environments: environments,
		containers:   containers,
		store:        store,
		ssh:          sshService,
		config:       cfg,
	})
}

// printPreflightResult 输出远程 Docker 预检结果。
func printPreflightResult(a *app.App, result app.InitResult) error {
	preflight := result.Preflight
	lines := []string{
		fmt.Sprintf("节点: %s (%s)", result.Host.ID, result.Host.Name),
		fmt.Sprintf("远程工作空间: %s", emptyAsUnknown(result.WorkspaceRoot)),
		fmt.Sprintf("SSH 连接: %s", flagMark(preflight.SSHOK)),
		fmt.Sprintf("Docker CLI: %s", flagMark(preflight.CLIPresent)),
		fmt.Sprintf("Docker CLI 版本: %s（最低 %s）", emptyAsUnknown(preflight.CLIVersion), docker.MinimumCLIVersion),
		fmt.Sprintf("Docker Engine: %s", flagMark(preflight.EngineReachable)),
		fmt.Sprintf("Docker Engine 版本: %s（最低 %s）", emptyAsUnknown(preflight.EngineVersion), docker.MinimumEngineVersion),
		fmt.Sprintf(
			"Engine API: 最高 %s / 最低接受 %s（AgentFlow 基准 %s，兼容: %s）",
			emptyAsUnknown(preflight.APIVersion),
			emptyAsUnknown(preflight.MinAPIVersion),
			docker.EngineAPIBaseVersion,
			flagMark(preflight.APIVersionCheckOK),
		),
		fmt.Sprintf("使用权限: %s", flagMark(preflight.PermissionOK)),
		fmt.Sprintf("工作空间可写: %s", flagMark(preflight.WorkspaceWritable)),
		fmt.Sprintf("剩余空间: %s", formatBytes(preflight.FreeDiskBytes)),
		fmt.Sprintf("结果类别: %s", preflight.ErrorCode),
	}
	if preflight.Detail != "" {
		lines = append(lines, fmt.Sprintf("说明: %s", preflight.Detail))
	}
	if _, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n")); err != nil {
		return err
	}
	return printDockerPermissionHint(a, result)
}

// printDockerPermissionHint 在权限不足时提示用户人工处理。
func printDockerPermissionHint(a *app.App, result app.InitResult) error {
	if result.Preflight.ErrorCode != docker.CodePermissionDenied {
		return nil
	}
	_, err := fmt.Fprintln(
		a.Stdout(),
		"提示: 当前 SSH 用户没有使用 Docker 的权限。AgentFlow 不会自动切换到 sudo，"+
			"也不会修改用户组或 Socket 权限；请自行决定如何调整远程环境。",
	)
	return err
}

// printInitResult 输出远程环境初始化结果。
func printInitResult(a *app.App, result app.InitResult) error {
	if err := printPreflightResult(a, result); err != nil {
		return err
	}
	if len(result.Environments) == 0 {
		return nil
	}
	lines := []string{"\n已就绪的环境:"}
	for _, ref := range result.Environments {
		lines = append(lines, "  - "+ref.Label())
	}
	if len(result.Created) > 0 {
		lines = append(lines, "本次新建的路径:")
		for _, path := range result.Created {
			lines = append(lines, "  - "+path)
		}
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}
