package cli

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/spf13/cobra"
)

// envFlags 是环境命令共用的参数。
type envFlags struct {
	// Machine 是目标执行节点。
	Machine string
	// Type 是环境类型。
	Type string
	// Name 是语言环境名称。
	Name string
	// Version 是版本号，0 表示最新版本。
	Version int
}

// addEnvFlags 注册环境命令的公共参数。
func addEnvFlags(command *cobra.Command, flags *envFlags, withVersion bool) {
	command.Flags().StringVar(&flags.Machine, "machine", "", "目标执行节点标识（必填）")
	command.Flags().StringVar(
		&flags.Type,
		"type",
		string(domain.EnvironmentTypeBase),
		"环境类型：base 或 language",
	)
	command.Flags().StringVar(&flags.Name, "name", "", "语言环境名称，仅 --type language 时使用")
	if withVersion {
		command.Flags().IntVar(&flags.Version, "version", 0, "版本号，省略时使用最新版本")
	}
	if err := command.MarkFlagRequired("machine"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
}

// resolve 把命令行参数解析为环境引用。
func (f envFlags) resolve() (domain.EnvironmentRef, error) {
	environmentType, err := domain.ParseEnvironmentType(f.Type)
	if err != nil {
		return domain.EnvironmentRef{}, err
	}
	return domain.NewEnvironmentRef(environmentType, f.Name)
}

// newDockerEnvListCommand 构建 docker env list 子命令。
func newDockerEnvListCommand(a *app.App) *cobra.Command {
	var machine string
	command := &cobra.Command{
		Use:   "list",
		Short: "列出远程环境定义与版本",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withEnvironmentService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.EnvironmentService,
			) error {
				summaries, err := service.List(ctx, app.ListRequest{MachineID: machine})
				if err != nil {
					return err
				}
				if len(summaries) == 0 {
					_, err := fmt.Fprintln(a.Stdout(), "尚未创建任何环境版本；请先执行 agentflow docker init。")
					return err
				}
				if _, err := fmt.Fprintln(
					a.Stdout(),
					"环境\t最新版本\t当前可用版本\t版本数",
				); err != nil {
					return err
				}
				for _, summary := range summaries {
					if _, err := fmt.Fprintf(
						a.Stdout(),
						"%s\t%s\t%s\t%d\n",
						summary.Ref.Label(),
						summary.LatestVersion,
						emptyAsUnknown(summary.CurrentVersion),
						len(summary.Versions),
					); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	command.Flags().StringVar(&machine, "machine", "", "目标执行节点标识（必填）")
	if err := command.MarkFlagRequired("machine"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}

// newDockerEnvShowCommand 构建 docker env show 子命令。
func newDockerEnvShowCommand(a *app.App) *cobra.Command {
	var flags envFlags
	var flagsCredentials credentialFlags
	var withContent bool
	command := &cobra.Command{
		Use:   "show",
		Short: "查看环境定义与 Dockerfile",
		Long: "默认展示版本元数据；--dockerfile 会同时输出该版本的 Dockerfile 内容。\n" +
			"输出内容只来自远程工作空间，不会修改任何版本。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := flags.resolve()
			if err != nil {
				return err
			}
			return withEnvironmentService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.EnvironmentService,
			) error {
				summary, err := service.Get(ctx, app.GetRequest{
					MachineID: flags.Machine,
					Ref:       ref,
				})
				if err != nil {
					return err
				}
				if err := printEnvironmentSummary(a, summary); err != nil {
					return err
				}
				if !withContent {
					return nil
				}
				provider, providerErr := credentialProviderForArgs(
					ctx,
					service.SSHService(),
					flags.Machine,
					flagsCredentials,
				)
				if providerErr != nil {
					return providerErr
				}
				content, readErr := service.ReadDockerfile(ctx, app.ReadDockerfileRequest{
					MachineID:     flags.Machine,
					Ref:           ref,
					VersionNumber: flags.Version,
					Credentials:   provider,
				})
				if readErr != nil {
					return readErr
				}
				if _, err := fmt.Fprintf(
					a.Stdout(),
					"\nDockerfile (%s):\n%s",
					content.RemotePath,
					ensureTrailingNewline(string(content.Content)),
				); err != nil {
					return err
				}
				return nil
			})
		},
	}
	addEnvFlags(command, &flags, true)
	addCredentialFlags(&flagsCredentials, command)
	command.Flags().BoolVar(&withContent, "dockerfile", false, "同时输出该版本的 Dockerfile 内容")
	return command
}

// newDockerEnvHistoryCommand 构建 docker env history 子命令。
func newDockerEnvHistoryCommand(a *app.App) *cobra.Command {
	var flags envFlags
	command := &cobra.Command{
		Use:   "history",
		Short: "查看环境的历史版本",
		Long: "历史版本只追加不覆盖：已保存的版本内容、Dockerfile 哈希与构建配置哈希\n" +
			"不会被后续版本修改。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := flags.resolve()
			if err != nil {
				return err
			}
			return withEnvironmentService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.EnvironmentService,
			) error {
				versions, err := service.History(ctx, app.GetRequest{
					MachineID: flags.Machine,
					Ref:       ref,
				})
				if err != nil {
					return err
				}
				return printVersionTable(a, versions)
			})
		},
	}
	addEnvFlags(command, &flags, false)
	return command
}

// newDockerEnvBuildCommand 构建 docker env build 子命令。
func newDockerEnvBuildCommand(a *app.App) *cobra.Command {
	var flags envFlags
	var flagsCredentials credentialFlags
	var noCache bool
	var downloadLog string
	var overwrite bool
	command := &cobra.Command{
		Use:   "build",
		Short: "在远程机器上构建环境镜像",
		Long: "构建上下文只包含环境配置，不包含项目源代码。构建日志写入远程工作空间的\n" +
			"builds 目录，本机只展示末尾摘要；--download-log 可把完整日志下载到本机。\n" +
			"构建成功后会核验实际镜像 ID 并写入镜像元数据；构建失败不会把版本标记为可用。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := flags.resolve()
			if err != nil {
				return err
			}
			return withEnvironmentService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.EnvironmentService,
			) error {
				provider, providerErr := credentialProviderForArgs(
					ctx,
					service.SSHService(),
					flags.Machine,
					flagsCredentials,
				)
				if providerErr != nil {
					return providerErr
				}
				result, buildErr := service.Build(ctx, app.BuildRequest{
					MachineID:     flags.Machine,
					Ref:           ref,
					VersionNumber: flags.Version,
					NoCache:       noCache,
					Credentials:   provider,
				})
				if printErr := printBuildResult(a, result); printErr != nil {
					return printErr
				}
				if downloadLog != "" && result.Version.Number > 0 {
					if _, err := service.DownloadBuildLog(ctx, app.DownloadBuildLogRequest{
						MachineID:     flags.Machine,
						Ref:           ref,
						VersionNumber: result.Version.Number,
						LocalPath:     downloadLog,
						Overwrite:     overwrite,
						Credentials:   provider,
					}); err != nil {
						return err
					}
					if _, err := fmt.Fprintf(a.Stdout(), "构建日志已下载: %s\n", downloadLog); err != nil {
						return err
					}
				}
				return buildErr
			})
		},
	}
	addEnvFlags(command, &flags, true)
	addCredentialFlags(&flagsCredentials, command)
	command.Flags().BoolVar(&noCache, "no-cache", false, "禁用 Docker 构建缓存")
	command.Flags().StringVar(&downloadLog, "download-log", "", "构建后把完整日志下载到该本机路径")
	command.Flags().BoolVar(&overwrite, "overwrite", false, "允许覆盖已存在的本机日志文件")
	return command
}

// newDockerEnvLogCommand 构建 docker env log 子命令。
func newDockerEnvLogCommand(a *app.App) *cobra.Command {
	var flags envFlags
	var flagsCredentials credentialFlags
	var output string
	var overwrite bool
	command := &cobra.Command{
		Use:   "log",
		Short: "下载环境构建日志",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := flags.resolve()
			if err != nil {
				return err
			}
			return withEnvironmentService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.EnvironmentService,
			) error {
				provider, providerErr := credentialProviderForArgs(
					ctx,
					service.SSHService(),
					flags.Machine,
					flagsCredentials,
				)
				if providerErr != nil {
					return providerErr
				}
				remotePath, err := service.DownloadBuildLog(ctx, app.DownloadBuildLogRequest{
					MachineID:     flags.Machine,
					Ref:           ref,
					VersionNumber: flags.Version,
					LocalPath:     output,
					Overwrite:     overwrite,
					Credentials:   provider,
				})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(
					a.Stdout(),
					"构建日志已下载: %s -> %s\n",
					remotePath,
					output,
				)
				return err
			})
		},
	}
	addEnvFlags(command, &flags, true)
	addCredentialFlags(&flagsCredentials, command)
	command.Flags().StringVar(&output, "output", "", "本机日志文件路径（必填）")
	command.Flags().BoolVar(&overwrite, "overwrite", false, "允许覆盖已存在的本机文件")
	if err := command.MarkFlagRequired("output"); err != nil {
		panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
	}
	return command
}
