package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/spf13/cobra"
)

// newDockerContainerCommand 构建 docker container 命令组。
func newDockerContainerCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "container",
		Short: "管理由 AgentFlow 创建的远程容器",
		Long: "所有容器都由 AgentFlow 显式创建并登记；停止与删除前会核验本地记录与 Docker 标签。\n" +
			"已脱离管理（detached）的容器默认拒绝自动操作，只能人工处理。\n" +
			"删除容器不会删除共享镜像、共享缓存卷或远程项目配置目录。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newContainerCreateCommand(a))
	command.AddCommand(newContainerListCommand(a))
	command.AddCommand(newContainerShowCommand(a))
	command.AddCommand(newContainerLogsCommand(a))
	command.AddCommand(newContainerExecCommand(a))
	command.AddCommand(newContainerStartCommand(a))
	command.AddCommand(newContainerStopCommand(a))
	command.AddCommand(newContainerDeleteCommand(a))
	command.AddCommand(newContainerDetachCommand(a))
	command.AddCommand(newContainerLifecycleCommand(a))
	command.AddCommand(newContainerRestartCommand(a))
	return command
}

// newContainerCreateCommand 构建 docker container create 子命令。
func newContainerCreateCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var machine string
	var envType string
	var envName string
	var version int
	var projectID string
	var caches []string
	var workDir string
	var skipRunContract bool
	command := &cobra.Command{
		Use:   "create",
		Short: "创建并启动受管理的任务容器",
		Long: "容器由 AgentFlow 创建并登记，创建后会核验实际容器 ID、所有权标签与运行状态。\n" +
			"默认不启用特权模式、不挂载宿主机 Docker Socket、用户 Home 与远程项目配置目录；\n" +
			"项目代码只存在于容器内部 /workspace/project。\n" +
			"--cache 只挂载显式声明的命名卷，删除容器不会删除这些共享缓存卷。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			environmentType, err := domain.ParseEnvironmentType(envType)
			if err != nil {
				return err
			}
			ref, err := domain.NewEnvironmentRef(environmentType, envName)
			if err != nil {
				return err
			}
			if ref.IsBase() {
				return fmt.Errorf(
					"创建容器失败: 任务容器必须使用语言环境（--type language），" +
						"基础环境只作为语言环境的构建基础",
				)
			}
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				detail, createErr := service.CreateContainer(ctx, app.CreateContainerRequest{
					MachineID:            machine,
					Ref:                  ref,
					VersionNumber:        version,
					ProjectID:            projectID,
					Caches:               caches,
					WorkDir:              workDir,
					SkipRunContractCheck: skipRunContract,
				})
				if printErr := printContainerDetail(a, detail); printErr != nil {
					return printErr
				}
				return createErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().StringVar(&machine, "machine", "", "目标执行节点标识（必填）")
	command.Flags().StringVar(
		&envType,
		"type",
		string(domain.EnvironmentTypeLanguage),
		"环境类型，任务容器必须使用 language",
	)
	command.Flags().StringVar(&envName, "name", "", "语言环境名称（必填）")
	command.Flags().IntVar(&version, "version", 0, "环境版本号，省略时使用当前可用版本")
	command.Flags().StringVar(&projectID, "project", "", "项目标识，用于项目级缓存的共享范围")
	command.Flags().StringSliceVar(&caches, "cache", nil, "允许挂载的缓存类型，可重复指定")
	command.Flags().StringVar(&workDir, "workdir", "", "容器内项目工作目录，省略时使用 "+domain.DefaultContainerWorkDir)
	command.Flags().BoolVar(
		&skipRunContract,
		"skip-run-contract-check",
		false,
		"跳过镜像常驻运行契约预检（仅在已确认镜像可用时使用）",
	)
	for _, name := range []string{"machine", "name"} {
		if err := command.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// newContainerListCommand 构建 docker container list 子命令。
func newContainerListCommand(a *app.App) *cobra.Command {
	var machine string
	var includeDetached bool
	command := &cobra.Command{
		Use:   "list",
		Short: "列出受 AgentFlow 管理的容器",
		Long: "默认只列出仍在自动管理范围内的容器；--include-detached 同时显示已脱离管理的容器。\n" +
			"该命令只读取本机登记记录，不连接远程机器。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				containers, err := service.ListContainers(ctx, app.ListContainersRequest{
					MachineID:       machine,
					IncludeDetached: includeDetached,
				})
				if err != nil {
					return err
				}
				if len(containers) == 0 {
					_, err := fmt.Fprintln(a.Stdout(), "没有符合条件的容器记录。")
					return err
				}
				if _, err := fmt.Fprintln(
					a.Stdout(),
					"记录 ID\t名称\t环境\t版本\t状态\tDocker 状态\t脱离管理\t创建时间",
				); err != nil {
					return err
				}
				for _, container := range containers {
					if _, err := fmt.Fprintf(
						a.Stdout(),
						"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						container.ID,
						container.Name,
						container.Environment.Label(),
						container.EnvironmentVersion,
						container.State,
						container.DockerState,
						flagMark(container.Detached),
						container.CreatedAt.Format(timeFormat),
					); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	command.Flags().StringVar(&machine, "machine", "", "只列出指定执行节点的容器")
	command.Flags().BoolVar(&includeDetached, "include-detached", false, "同时列出已脱离管理的容器")
	return command
}

// newContainerShowCommand 构建 docker container show 子命令。
func newContainerShowCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var sync bool
	command := &cobra.Command{
		Use:   "show <container-id>",
		Short: "查看容器详情与远程实际状态",
		Long: "连接远程机器检查容器实际状态，并核验所有权标签。\n" +
			"对已脱离管理的容器只做只读观测，不会恢复管理权。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				if _, err := service.GetContainerRecord(ctx, args[0]); err != nil {
					return err
				}
				detail, inspectErr := service.InspectContainer(ctx, app.InspectContainerRequest{
					ContainerID: args[0],
					Sync:        sync,
				})
				if printErr := printContainerDetail(a, detail); printErr != nil {
					return printErr
				}
				return inspectErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().BoolVar(&sync, "sync", false, "同时更新本机记录的 Docker 状态观测结果")
	return command
}

// newContainerLogsCommand 构建 docker container logs 子命令。
func newContainerLogsCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var tail int
	command := &cobra.Command{
		Use:   "logs <container-id>",
		Short: "查看容器日志",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				if _, err := service.GetContainerRecord(ctx, args[0]); err != nil {
					return err
				}
				logs, logErr := service.ContainerLogs(ctx, app.ContainerLogsRequest{
					ContainerID: args[0],
					TailLines:   tail,
				})
				if logErr != nil {
					return logErr
				}
				_, err := fmt.Fprint(a.Stdout(), ensureTrailingNewline(logs))
				return err
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().IntVar(&tail, "tail", 200, "返回的末尾行数")
	return command
}

// newContainerExecCommand 构建 docker container exec 子命令。
func newContainerExecCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "exec <container-id> -- <命令> [参数...]",
		Short: "在运行中的容器内执行非交互命令",
		Long: "命令通过 docker exec 在容器内以非交互方式执行，工作目录为容器内项目目录。\n" +
			"容器内命令的退出码会透传到本命令的退出码。",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withContainerService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ContainerService,
			) error {
				if _, err := service.GetContainerRecord(ctx, args[0]); err != nil {
					return err
				}
				result, execErr := service.Exec(ctx, app.ExecRequest{
					ContainerID: args[0],
					Command:     args[1:],
					Timeout:     timeout,
				})
				if printErr := printExecResult(a, result); printErr != nil {
					return printErr
				}
				if execErr != nil {
					return execErr
				}
				if result.Result.ExitCode != 0 {
					return fmt.Errorf("容器内命令退出码 %d", result.Result.ExitCode)
				}
				return nil
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().DurationVar(&timeout, "timeout", 0, "本次执行超时时间，省略时使用默认值")
	return command
}

// newContainerStartCommand 构建 docker container start 子命令。
func newContainerStartCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	command := &cobra.Command{
		Use:   "start <container-id>",
		Short: "启动受管理的容器",
		Long:  "启动后核验 Docker 状态；已脱离管理的容器会被拒绝自动启动。",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerAction(cmd.Context(), a, hostConfigPath(cmd), args[0], flags, func(
				ctx context.Context,
				service *app.ContainerService,
			) (app.ContainerDetail, error) {
				return service.StartContainer(ctx, app.StartContainerRequest{
					ContainerID: args[0],
				})
			})
		},
	}
	addCredentialFlags(&flags, command)
	return command
}

// newContainerStopCommand 构建 docker container stop 子命令。
func newContainerStopCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var timeoutSeconds int
	command := &cobra.Command{
		Use:   "stop <container-id>",
		Short: "停止受管理的容器并保留现场",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerAction(cmd.Context(), a, hostConfigPath(cmd), args[0], flags, func(
				ctx context.Context,
				service *app.ContainerService,
			) (app.ContainerDetail, error) {
				return service.StopContainer(ctx, app.StopContainerRequest{
					ContainerID:    args[0],
					TimeoutSeconds: timeoutSeconds,
				})
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().IntVar(&timeoutSeconds, "timeout", 10, "等待容器退出的秒数")
	return command
}

// newContainerDeleteCommand 构建 docker container delete 子命令。
func newContainerDeleteCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var assumeYes bool
	var reason string
	command := &cobra.Command{
		Use:   "delete <container-id>",
		Short: "删除受管理的容器",
		Long: "删除容器及其任务级临时资源，不会删除共享镜像、共享缓存卷或远程项目配置目录。\n" +
			"已脱离管理的容器默认拒绝删除，必须由用户独立人工处理。\n" +
			"非交互执行必须显式给出 --yes。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endReason := domain.ContainerEndReason(strings.TrimSpace(reason))
			if endReason == "" {
				endReason = domain.ContainerEndReasonManual
			}
			if err := endReason.Validate(); err != nil {
				return err
			}
			if !assumeYes {
				return fmt.Errorf(
					"删除容器是不可逆操作；确认后请追加 --yes 重新执行（容器 %s）",
					args[0],
				)
			}
			return runContainerAction(cmd.Context(), a, hostConfigPath(cmd), args[0], flags, func(
				ctx context.Context,
				service *app.ContainerService,
			) (app.ContainerDetail, error) {
				return service.DeleteContainer(ctx, app.DeleteContainerRequest{
					ContainerID: args[0],
					Reason:      endReason,
				})
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().BoolVar(&assumeYes, "yes", false, "确认执行删除（非交互执行必须显式给出）")
	command.Flags().StringVar(
		&reason,
		"reason",
		string(domain.ContainerEndReasonManual),
		"生命周期结束原因：manual、task_succeeded、task_failed 或 interrupted",
	)
	return command
}

// newContainerDetachCommand 构建 docker container detach 子命令。
func newContainerDetachCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var assumeYes bool
	command := &cobra.Command{
		Use:   "detach <container-id>",
		Short: "把容器脱离 AgentFlow 自动管理",
		Long: "容器及其现场会被保留，但从此不再接受自动启停与删除；Docker 标签无法原地修改，\n" +
			"因此不能依靠补标签恢复管理权。该操作不可撤销。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !assumeYes {
				return fmt.Errorf(
					"脱离管理不可撤销；确认后请追加 --yes 重新执行（容器 %s）",
					args[0],
				)
			}
			return runContainerAction(cmd.Context(), a, hostConfigPath(cmd), args[0], flags, func(
				ctx context.Context,
				service *app.ContainerService,
			) (app.ContainerDetail, error) {
				return service.DetachContainer(ctx, app.DetachContainerRequest{
					ContainerID: args[0],
				})
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().BoolVar(&assumeYes, "yes", false, "确认脱离管理（非交互执行必须显式给出）")
	return command
}
