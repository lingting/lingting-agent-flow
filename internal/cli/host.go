package cli

import (
	"context"
	"fmt"
	"time"

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
		Long: "维护执行节点的名称、主机地址、SSH 端口、登录用户名、认证方式与远程\n" +
			"工作空间，并测试完整的 SSH 身份认证连接。\n" +
			"认证只使用节点配置中的无口令私钥，命令层不收集任何凭据；\n" +
			"未知主机密钥必须核验指纹后显式信任，密钥变化时会拒绝连接。\n" +
			"host validate 执行环境预检（SSH 连接、Docker CLI 帮助信息、基础镜像元数据）。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newHostAddCommand(a))
	command.AddCommand(newHostListCommand(a))
	command.AddCommand(newHostShowCommand(a))
	command.AddCommand(newHostUpdateCommand(a))
	command.AddCommand(newHostSetDefaultCommand(a))
	command.AddCommand(newHostProbeCommand(a))
	command.AddCommand(newHostTestCommand(a))
	command.AddCommand(newHostTrustCommand(a))
	command.AddCommand(newHostValidateCommand(a))
	command.AddCommand(newHostWorkspaceCommand(a))
	command.AddCommand(newHostTransferCommand(a))
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
			domain.DefaultWorkspaceRoot + "。\n" +
			"认证方式只支持 private_key，必须同时给出 --key，且私钥必须是无口令私钥。\n" +
			"新建节点的校验状态为 checking，由后台服务异步完成环境预检。",
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
	var authMethod string
	var privateKeyPath string
	command.Flags().StringVar(
		&authMethod,
		"auth",
		string(domain.AuthMethodPrivateKey),
		"认证方式：只支持 private_key",
	)
	command.Flags().StringVar(
		&privateKeyPath,
		"key",
		"",
		"本机私钥文件路径（必填，无口令私钥）",
	)
	command.PreRunE = func(cmd *cobra.Command, args []string) error {
		method, err := domain.ParseAuthMethod(authMethod)
		if err != nil {
			return fmt.Errorf("认证方式非法: %w", err)
		}
		request.AuthMethod = method
		request.PrivateKeyPath = privateKeyPath
		return nil
	}
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
				"标识\t名称\t地址\t用户名\t认证方式\t工作空间\t默认\t启用",
			); err != nil {
				return err
			}
			for _, host := range hosts {
				if _, err := fmt.Fprintf(
					a.Stdout(),
					"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					host.ID,
					host.Name,
					host.Address(),
					host.Username,
					host.AuthMethod,
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
					"认证方式: %s\n私钥文件: %s\n默认节点: %s\n启用: %s\n"+
					"主机密钥算法: %s\n主机密钥指纹: %s\n主机密钥信任时间: %s\n"+
					"最近连接结果: %s\n最近连接错误: %s\n最近连接时间: %s\n"+
					"创建时间: %s\n更新时间: %s\n",
				host.ID,
				host.Name,
				host.Host,
				host.Port,
				host.Username,
				host.WorkspaceRoot,
				host.AuthMethod,
				emptyAsUnknown(host.PrivateKeyPath),
				flagMark(host.IsDefault),
				flagMark(host.Enabled),
				emptyAsUnknown(host.HostKeyAlgorithm),
				emptyAsUnknown(host.HostKeyFingerprint),
				formatOptionalTime(host.HostKeyTrustedAt),
				connectStatusLabel(host.LastConnectStatus),
				emptyAsUnknown(host.LastConnectError),
				formatOptionalTime(host.LastConnectedAt),
				host.CreatedAt.Format(timeFormat),
				host.UpdatedAt.Format(timeFormat),
			)
			return err
		},
	}
}

// newHostUpdateCommand 构建 host update 子命令。
//
// 只修改显式给出的字段，未给出的配置保持原值。
func newHostUpdateCommand(a *app.App) *cobra.Command {
	var (
		request    app.UpdateExecutionHostRequest
		name       string
		hostAddr   string
		port       int
		username   string
		workspace  string
		authMethod string
		privateKey string
		enabled    bool
	)
	command := &cobra.Command{
		Use:   "update <id>",
		Short: "修改执行节点的连接参数或认证方式",
		Long: "只修改显式给出的参数，未给出的配置保持原值。\n" +
			"该命令不执行 SSH 登录，也不接受任何凭据输入。\n" +
			"影响连接目标、认证方式、私钥路径、工作空间或启用状态的修改会使旧校验结果失效，\n" +
			"状态回到 checking 并重新提交后台环境预检；仅修改名称不会失效。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request.ID = args[0]
			if err := applyUpdateFlags(cmd, &request, updateFlagValues{
				name:       name,
				hostAddr:   hostAddr,
				port:       port,
				username:   username,
				workspace:  workspace,
				authMethod: authMethod,
				privateKey: privateKey,
				enabled:    enabled,
			}); err != nil {
				return err
			}
			host, err := withHostService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.ExecutionHostService,
			) (domain.ExecutionHost, error) {
				return service.Update(ctx, request)
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				a.Stdout(),
				"执行节点已更新: %s\n名称: %s\n地址: %s\n认证方式: %s\n工作空间: %s\n",
				host.ID,
				host.Name,
				host.Address(),
				host.AuthMethod,
				host.WorkspaceRoot,
			)
			return err
		},
	}
	command.Flags().StringVar(&name, "name", "", "节点名称")
	command.Flags().StringVar(&hostAddr, "host", "", "IP 地址或主机名")
	command.Flags().IntVar(&port, "port", 0, "SSH 端口")
	command.Flags().StringVar(&username, "user", "", "SSH 登录用户名")
	command.Flags().StringVar(&workspace, "workspace", "", "远程工作空间路径")
	command.Flags().StringVar(&authMethod, "auth", "", "认证方式：agent、private_key 或 password")
	command.Flags().StringVar(&privateKey, "key", "", "本机私钥文件路径")
	command.Flags().BoolVar(&enabled, "enabled", true, "是否启用该节点")
	return command
}

// updateFlagValues 汇总 host update 的命令行取值。
type updateFlagValues struct {
	name       string
	hostAddr   string
	port       int
	username   string
	workspace  string
	authMethod string
	privateKey string
	enabled    bool
}

// applyUpdateFlags 把显式给出的参数写入更新请求。
func applyUpdateFlags(
	cmd *cobra.Command,
	request *app.UpdateExecutionHostRequest,
	values updateFlagValues,
) error {
	flags := cmd.Flags()
	if flags.Changed("name") {
		request.Name = &values.name
	}
	if flags.Changed("host") {
		request.Host = &values.hostAddr
	}
	if flags.Changed("port") {
		request.Port = &values.port
	}
	if flags.Changed("user") {
		request.Username = &values.username
	}
	if flags.Changed("workspace") {
		request.WorkspaceRoot = &values.workspace
	}
	if flags.Changed("auth") {
		method, err := domain.ParseAuthMethod(values.authMethod)
		if err != nil {
			return fmt.Errorf("认证方式非法: %w", err)
		}
		request.AuthMethod = &method
	}
	if flags.Changed("key") {
		request.PrivateKeyPath = &values.privateKey
	}
	if flags.Changed("enabled") {
		request.Enabled = &values.enabled
	}
	return nil
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

// formatOptionalTime 把可空时间转换为可读文本。
func formatOptionalTime(at *time.Time) string {
	if at == nil {
		return "（无）"
	}
	return at.Format(timeFormat)
}

// connectStatusLabel 把最近连接结果类别转换为可读文本。
func connectStatusLabel(status domain.ConnectStatus) string {
	if status == domain.ConnectStatusUnknown {
		return "尚未测试"
	}
	return string(status)
}

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
