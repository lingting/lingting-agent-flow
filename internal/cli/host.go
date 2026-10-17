package cli

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/spf13/cobra"
)

// newHostCommand 构建 host 命令组。
func newHostCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "host",
		Short: "管理远程 SSH 执行节点",
		Long: "维护执行节点的名称、主机地址、SSH 端口、登录用户名与远程工作空间。\n" +
			"当前版本只保存配置与探测 SSH 服务可达性，不包含任何身份认证方式，\n" +
			"也不提供远程命令执行、文件传输与 Docker 管理能力。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newHostAddCommand(a))
	command.AddCommand(newHostListCommand(a))
	command.AddCommand(newHostShowCommand(a))
	command.AddCommand(newHostSetDefaultCommand(a))
	command.AddCommand(newHostProbeCommand(a))
	command.AddCommand(newHostRemoveCommand(a))
	command.PersistentFlags().String(
		"config",
		"",
		"配置文件路径（默认 "+config.DefaultPath+"）",
	)
	return command
}

// newHostAddCommand 构建 host add 子命令。
func newHostAddCommand(a *app.App) *cobra.Command {
	var request app.CreateExecutionHostRequest
	command := &cobra.Command{
		Use:   "add",
		Short: "新增执行节点配置",
		Long: "保存一个执行节点的连接目标与远程工作空间配置。\n" +
			"该命令不执行 SSH 登录，也不创建远程目录；工作空间路径中的 ~ 指远程\n" +
			"SSH 登录用户的 Home。未指定 --port 时使用 22，未指定 --workspace 时使用\n" +
			domain.DefaultWorkspaceRoot + "。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := withHostService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ExecutionHostService,
			) (domain.ExecutionHost, error) {
				return service.Create(ctx, request)
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				a.Stdout(),
				"执行节点已创建: %s\n名称: %s\n地址: %s\n用户名: %s\n工作空间: %s\n默认节点: %s\n",
				host.ID,
				host.Name,
				host.Address(),
				host.Username,
				host.WorkspaceRoot,
				flagMark(host.IsDefault),
			)
			return err
		},
	}
	command.Flags().StringVar(&request.ID, "id", "", "节点标识，省略时自动生成")
	command.Flags().StringVar(&request.Name, "name", "", "节点名称，省略时使用主机地址")
	command.Flags().StringVar(&request.Host, "host", "", "IP 地址或主机名（必填）")
	command.Flags().IntVar(&request.Port, "port", domain.DefaultSSHPort, "SSH 端口")
	command.Flags().StringVar(&request.Username, "user", "", "SSH 登录用户名（必填）")
	command.Flags().StringVar(
		&request.WorkspaceRoot,
		"workspace",
		domain.DefaultWorkspaceRoot,
		"远程工作空间路径，~ 指远程用户的 Home",
	)
	command.Flags().BoolVar(&request.Default, "default", false, "同时设为默认执行节点")
	for _, name := range []string{"host", "user"} {
		if err := command.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("cli: 标记必需参数失败: %v", err))
		}
	}
	return command
}

// newHostListCommand 构建 host list 子命令。
func newHostListCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出全部执行节点",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := withHostService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ExecutionHostService,
			) ([]domain.ExecutionHost, error) {
				return service.List(ctx)
			})
			if err != nil {
				return err
			}
			if len(hosts) == 0 {
				_, err := fmt.Fprintln(a.Stdout(), "尚未配置执行节点。")
				return err
			}
			if _, err := fmt.Fprintln(
				a.Stdout(),
				"标识\t名称\t地址\t用户名\t工作空间\t默认\t启用",
			); err != nil {
				return err
			}
			for _, host := range hosts {
				if _, err := fmt.Fprintf(
					a.Stdout(),
					"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					host.ID,
					host.Name,
					host.Address(),
					host.Username,
					host.WorkspaceRoot,
					flagMark(host.IsDefault),
					flagMark(host.Enabled),
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// newHostShowCommand 构建 host show 子命令。
func newHostShowCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "显示指定执行节点的详细配置",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := withHostService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ExecutionHostService,
			) (domain.ExecutionHost, error) {
				return service.Get(ctx, args[0])
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				a.Stdout(),
				"标识: %s\n名称: %s\n主机: %s\n端口: %d\n用户名: %s\n工作空间: %s\n"+
					"默认节点: %s\n启用: %s\n创建时间: %s\n更新时间: %s\n",
				host.ID,
				host.Name,
				host.Host,
				host.Port,
				host.Username,
				host.WorkspaceRoot,
				flagMark(host.IsDefault),
				flagMark(host.Enabled),
				host.CreatedAt.Format(timeFormat),
				host.UpdatedAt.Format(timeFormat),
			)
			return err
		},
	}
}

// newHostSetDefaultCommand 构建 host set-default 子命令。
func newHostSetDefaultCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "set-default <id>",
		Short: "把指定节点设为默认执行节点",
		Long:  "默认节点在单个事务内切换，数据库保证最多只有一个默认节点。",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := withHostService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ExecutionHostService,
			) (domain.ExecutionHost, error) {
				return service.SetDefault(ctx, args[0])
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.Stdout(), "默认执行节点已设置: %s (%s)\n", host.ID, host.Address())
			return err
		},
	}
}

// newHostProbeCommand 构建 host probe 子命令。
func newHostProbeCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "probe <id>",
		Short: "探测执行节点的 TCP 与 SSH 协议可达性",
		Long: "使用 Go 建立 TCP 连接并读取服务端 SSH 协议标识，不调用本机 ssh 命令。\n" +
			"探测成功只表示 SSH 服务可达且协议标识有效，尚未执行身份认证，\n" +
			"也不代表可以登录或远程执行命令。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := withHostService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ExecutionHostService,
			) (app.ExecutionHostProbeResult, error) {
				return service.Probe(ctx, args[0])
			})
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(
				a.Stdout(),
				"节点: %s (%s)\n目标地址: %s\nTCP 可达: %s\nSSH 协议标识: %s\n"+
					"身份认证: 未执行\n结果类别: %s\n耗时: %s\n说明: %s\n",
				result.Host.ID,
				result.Host.Name,
				result.Probe.Target,
				flagMark(result.Probe.Reachable),
				flagMark(result.Probe.SSHDetected),
				result.Probe.ErrorCode,
				result.Probe.Duration.Round(probeDurationUnit),
				result.Probe.Message,
			); err != nil {
				return err
			}
			if !result.Probe.Succeeded() {
				return fmt.Errorf("探测未识别到有效的 SSH 服务: %s", result.Probe.Message)
			}
			return nil
		},
	}
}

// hostConfigPath 读取 host 命令组上的 --config 参数。
func hostConfigPath(cmd *cobra.Command) string {
	path, err := cmd.Flags().GetString("config")
	if err != nil {
		return ""
	}
	return path
}

// flagMark 把布尔标记转换为可读文本，用于默认节点、启用状态与探测结果。
func flagMark(ok bool) string {
	if ok {
		return "是"
	}
	return "否"
}

// probeDurationUnit 是探测耗时的展示精度。
const probeDurationUnit = 1e3

// newHostRemoveCommand 构建 host remove 子命令。
func newHostRemoveCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "删除执行节点配置",
		Long:  "只删除本机数据库中的节点配置，不会删除远程工作空间或远程 Docker 资源。",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := withHostService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ExecutionHostService,
			) (domain.ExecutionHost, error) {
				return service.Remove(ctx, args[0])
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.Stdout(), "执行节点已删除: %s (%s)\n", host.ID, host.Address())
			return err
		},
	}
}

// withHostService 加载配置、打开数据库并执行一次执行节点服务调用。
//
// 数据库连接在调用结束后关闭，命令处理函数只负责参数解析与输出。
func withHostService[T any](
	ctx context.Context,
	a *app.App,
	configPath string,
	run func(ctx context.Context, service *app.ExecutionHostService) (T, error),
) (T, error) {
	var zero T
	cfg, err := a.ValidateConfig(ctx, configPath)
	if err != nil {
		return zero, err
	}
	store, err := a.OpenStore(ctx, cfg)
	if err != nil {
		return zero, err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			a.Logger().Slog().WarnContext(ctx, "关闭数据库失败", "error", closeErr)
		}
	}()
	service, err := app.NewExecutionHostService(store, a.Logger())
	if err != nil {
		return zero, err
	}
	return run(ctx, service)
}
