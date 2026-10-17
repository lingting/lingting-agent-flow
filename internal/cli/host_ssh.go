package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/spf13/cobra"
)

// newHostTestCommand 构建 host test 子命令。
//
// 该命令执行完整的 SSH 身份认证连接测试，而不是只探测 TCP 可达性。
func newHostTestCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var checkWorkspace bool
	var createWorkspace bool
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "test <id>",
		Short: "测试执行节点的完整 SSH 身份认证连接",
		Long: "使用节点配置的认证方式建立真实 SSH 连接，成功后显示远程用户、远程 Home\n" +
			"目录与工作空间路径。该命令区分 TCP 不可达、SSH 握手失败、主机密钥不可信\n" +
			"与认证失败。密码与私钥口令只在内存中传递，可用 --password-stdin 或\n" +
			"--passphrase-stdin 从标准输入读取；非交互环境不会挂起等待输入。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSSHService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.SSHService,
			) error {
				host, err := service.Host(ctx, args[0])
				if err != nil {
					return err
				}
				provider, err := credentialProviderFor(host.AuthMethod, flags)
				if err != nil {
					return err
				}
				testCtx, cancel := withOptionalTimeout(ctx, timeout)
				defer cancel()
				result, testErr := service.TestConnection(testCtx, args[0], app.TestConnectionOptions{
					Credentials:     provider,
					CheckWorkspace:  checkWorkspace || createWorkspace,
					CreateWorkspace: createWorkspace,
				})
				if printErr := printConnectionResult(a, result); printErr != nil {
					return printErr
				}
				return testErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().BoolVar(
		&checkWorkspace,
		"check-workspace",
		false,
		"认证成功后同时解析并检查远程工作空间",
	)
	command.Flags().BoolVar(
		&createWorkspace,
		"create-workspace",
		false,
		"检查工作空间时按需创建目录（隐含 --check-workspace）",
	)
	command.Flags().DurationVar(&timeout, "timeout", 0, "本次连接测试的超时时间，省略时使用默认值")
	return command
}

// newHostTrustCommand 构建 host trust 子命令。
//
// 未知主机密钥必须先由用户核验指纹，再显式确认信任；
// 该命令不会在未核验指纹的情况下写入信任记录。
func newHostTrustCommand(a *app.App) *cobra.Command {
	var fingerprint string
	var assumeYes bool
	command := &cobra.Command{
		Use:   "trust <id>",
		Short: "核验并信任执行节点的主机密钥",
		Long: "先探测服务端主机密钥并展示算法与 SHA-256 指纹，再按用户确认写入信任记录。\n" +
			"必须用 --fingerprint 提供用户已经核验过的指纹，否则拒绝记录；\n" +
			"非交互执行还必须显式给出 --yes。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSSHService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.SSHService,
			) error {
				probe, err := service.ProbeHostKey(ctx, args[0])
				if err != nil {
					return err
				}
				if err := printHostKeyProbe(a, probe); err != nil {
					return err
				}
				if err := requireTrustConfirmation(probe, fingerprint, assumeYes); err != nil {
					return err
				}
				host, err := service.TrustHostKey(ctx, args[0], fingerprint)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(
					a.Stdout(),
					"已记录主机密钥信任: %s\n算法: %s\n指纹: %s\n记录文件: %s\n",
					host.ID,
					host.HostKeyAlgorithm,
					host.HostKeyFingerprint,
					service.KnownHostsStorePath(),
				)
				return err
			})
		},
	}
	command.Flags().StringVar(
		&fingerprint,
		"fingerprint",
		"",
		"用户已核验的主机密钥 SHA-256 指纹，例如 SHA256:xxxx",
	)
	command.Flags().BoolVar(&assumeYes, "yes", false, "确认写入信任记录（非交互执行必须显式给出）")
	return command
}

// requireTrustConfirmation 校验用户是否已经完成核验与确认。
//
// 返回 nil 表示可以写入信任记录；已受信任时通过错误短路命令，避免重复写入。
func requireTrustConfirmation(
	probe app.HostKeyProbe,
	fingerprint string,
	assumeYes bool,
) error {
	switch probe.Verdict {
	case sshclient.VerdictTrusted:
		return fmt.Errorf("该主机密钥已在信任记录中，无需重复写入")
	case sshclient.VerdictMismatch:
		return fmt.Errorf(
			"主机密钥与已信任记录不一致，已拒绝信任；如确认服务端密钥已合法更换，"+
				"请先清理记录文件中的旧条目: %s",
			probe.Host.ID,
		)
	case sshclient.VerdictRevoked:
		return fmt.Errorf("主机密钥已被标记为吊销，已拒绝信任: %s", probe.Info.Fingerprint)
	}
	expected := strings.TrimSpace(fingerprint)
	if expected == "" {
		return fmt.Errorf(
			"未提供已核验的指纹，已拒绝信任；请先核验服务端指纹 %s，再用 --fingerprint 指定它",
			probe.Info.Fingerprint,
		)
	}
	if expected != probe.Info.Fingerprint {
		return fmt.Errorf(
			"指定的指纹 %s 与服务端实际指纹 %s 不一致，已拒绝信任",
			expected,
			probe.Info.Fingerprint,
		)
	}
	if !assumeYes {
		return fmt.Errorf(
			"非交互执行不会自动写入信任记录；确认指纹 %s 无误后请追加 --yes",
			expected,
		)
	}
	return nil
}

// newHostWorkspaceCommand 构建 host workspace 子命令。
func newHostWorkspaceCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var create bool
	command := &cobra.Command{
		Use:   "workspace <id>",
		Short: "解析并检查远程 AgentFlow 工作空间",
		Long: "通过已认证连接解析远程登录用户的 Home 目录，把节点配置的工作空间解析为\n" +
			"远程绝对路径，并检查目录是否存在与可写。未指定 --create 时不会创建目录；\n" +
			"创建只发生在解析出的工作空间路径内。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSSHService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.SSHService,
			) error {
				host, err := service.Host(ctx, args[0])
				if err != nil {
					return err
				}
				provider, err := credentialProviderFor(host.AuthMethod, flags)
				if err != nil {
					return err
				}
				result, ensureErr := service.EnsureWorkspace(ctx, args[0], provider, create)
				if printErr := printWorkspaceResult(a, result); printErr != nil {
					return printErr
				}
				return ensureErr
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().BoolVar(&create, "create", false, "按需递归创建工作空间目录")
	return command
}

// newHostTransferCommand 构建 host transfer 子命令。
//
// 该命令用于开发者验证 SFTP 上传与下载能力，不是正式的业务命令。
func newHostTransferCommand(a *app.App) *cobra.Command {
	command := &cobra.Command{
		Use:   "transfer",
		Short: "开发者诊断：通过 SFTP 上传或下载单个文件",
		Long: "用于验证 SSH/SFTP 基础能力的诊断命令，不代表项目代码同步已经开始。\n" +
			"远程路径必须是 Linux 绝对路径，且不允许包含 .. 段。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newHostUploadCommand(a))
	command.AddCommand(newHostDownloadCommand(a))
	return command
}

// newHostUploadCommand 构建 host transfer upload 子命令。
func newHostUploadCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var overwrite bool
	var createParent bool
	command := &cobra.Command{
		Use:   "upload <id> <local> <remote>",
		Short: "上传本机文件到远程绝对路径",
		Long: "先写入同目录临时文件，校验写入结果后再原子重命名到目标路径，\n" +
			"失败或中断时不会留下内容不完整的最终文件。",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSSHService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.SSHService,
			) error {
				provider, err := credentialProviderForArgs(ctx, service, args[0], flags)
				if err != nil {
					return err
				}
				err = service.UploadFile(ctx, args[0], args[1], args[2], app.TransferOptions{
					Credentials:  provider,
					Overwrite:    overwrite,
					CreateParent: createParent,
				})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(a.Stdout(), "上传成功: %s -> %s\n", args[1], args[2])
				return err
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().BoolVar(&overwrite, "overwrite", false, "允许覆盖已存在的远程文件")
	command.Flags().BoolVar(&createParent, "create-parent", false, "按需递归创建远程父目录")
	return command
}

// newHostDownloadCommand 构建 host transfer download 子命令。
func newHostDownloadCommand(a *app.App) *cobra.Command {
	var flags credentialFlags
	var overwrite bool
	var createParent bool
	command := &cobra.Command{
		Use:   "download <id> <remote> <local>",
		Short: "下载远程文件到本机路径",
		Long: "先写入本机同目录临时文件，成功后再原子重命名到目标路径；\n" +
			"默认不覆盖已存在的本机文件，下载文件默认权限为 0600。",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSSHService(cmd.Context(), a, hostConfigPath(cmd), func(
				ctx context.Context,
				service *app.SSHService,
			) error {
				provider, err := credentialProviderForArgs(ctx, service, args[0], flags)
				if err != nil {
					return err
				}
				err = service.DownloadFile(ctx, args[0], args[1], args[2], app.TransferOptions{
					Credentials:  provider,
					Overwrite:    overwrite,
					CreateParent: createParent,
				})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(a.Stdout(), "下载成功: %s -> %s\n", args[1], args[2])
				return err
			})
		},
	}
	addCredentialFlags(&flags, command)
	command.Flags().BoolVar(&overwrite, "overwrite", false, "允许覆盖已存在的本机文件")
	command.Flags().BoolVar(&createParent, "create-parent", false, "按需递归创建本机父目录")
	return command
}

// credentialProviderForArgs 读取节点配置并构造凭据提供器。
func credentialProviderForArgs(
	ctx context.Context,
	service *app.SSHService,
	id string,
	flags credentialFlags,
) (sshclient.CredentialProvider, error) {
	host, err := service.Host(ctx, id)
	if err != nil {
		return nil, err
	}
	return credentialProviderFor(host.AuthMethod, flags)
}

// credentialProviderFor 按认证方式构造凭据提供器。
func credentialProviderFor(
	method domain.AuthMethod,
	flags credentialFlags,
) (sshclient.CredentialProvider, error) {
	return newCredentialProvider(credentialOptions{
		Method:       sshclient.AuthMethod(method),
		Flags:        flags,
		Stdin:        osStdin(),
		Prompt:       osStderr(),
		IsTerminal:   defaultIsTerminal,
		ReadPassword: defaultReadPassword,
	})
}

// withOptionalTimeout 在 timeout 大于零时附加超时。
func withOptionalTimeout(ctx context.Context, timeout time.Duration) (context.Context, func()) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// printConnectionResult 输出连接测试结果。
func printConnectionResult(a *app.App, result app.ConnectionResult) error {
	lines := []string{
		fmt.Sprintf("节点: %s (%s)", result.Host.ID, result.Host.Name),
		fmt.Sprintf("地址: %s", result.Host.Address()),
		fmt.Sprintf("用户名: %s", result.Host.Username),
		fmt.Sprintf("认证方式: %s", result.Host.AuthMethod),
		fmt.Sprintf("结果类别: %s", result.Code),
	}
	if result.HostKey.Fingerprint != "" {
		lines = append(
			lines,
			fmt.Sprintf("主机密钥算法: %s", result.HostKey.Algorithm),
			fmt.Sprintf("主机密钥指纹: %s", result.HostKey.Fingerprint),
			fmt.Sprintf("主机密钥信任: %s", result.HostKeyVerdict),
		)
	}
	if result.Authenticated {
		lines = append(
			lines,
			"SSH 认证成功",
			fmt.Sprintf("远程用户: %s", emptyAsUnknown(result.RemoteUser)),
			fmt.Sprintf("远程 Home: %s", emptyAsUnknown(result.Home)),
		)
	}
	if result.Workspace != nil {
		lines = append(lines, workspaceLines(*result.Workspace)...)
	}
	lines = append(
		lines,
		fmt.Sprintf("耗时: %s", result.Duration.Round(time.Millisecond)),
		fmt.Sprintf("说明: %s", result.Detail),
	)
	if _, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n")); err != nil {
		return err
	}
	return printHostKeyHint(a, result)
}

// printHostKeyHint 在主机密钥未知时提示用户如何完成信任。
func printHostKeyHint(a *app.App, result app.ConnectionResult) error {
	if result.Authenticated || result.Code != sshclient.CodeHostKeyUnknown {
		return nil
	}
	_, err := fmt.Fprintf(
		a.Stdout(),
		"下一步: 核验上述指纹后执行 agentflow host trust %s --fingerprint %s --yes\n",
		result.Host.ID,
		result.HostKey.Fingerprint,
	)
	return err
}

// printHostKeyProbe 输出主机密钥探测结果。
func printHostKeyProbe(a *app.App, probe app.HostKeyProbe) error {
	lines := []string{
		fmt.Sprintf("节点: %s (%s)", probe.Host.ID, probe.Host.Name),
		fmt.Sprintf("地址: %s", probe.Host.Address()),
		fmt.Sprintf("主机密钥算法: %s", probe.Info.Algorithm),
		fmt.Sprintf("主机密钥指纹: %s", probe.Info.Fingerprint),
		fmt.Sprintf("信任状态: %s", probe.Verdict),
	}
	for _, trusted := range probe.Trusted {
		lines = append(
			lines,
			fmt.Sprintf("已信任密钥: %s %s", trusted.Algorithm, trusted.Fingerprint),
		)
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// printWorkspaceResult 输出远程工作空间检查结果。
func printWorkspaceResult(a *app.App, result app.WorkspaceResult) error {
	lines := []string{fmt.Sprintf("节点: %s (%s)", result.Host.ID, result.Host.Name)}
	lines = append(lines, workspaceLines(result.Workspace)...)
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// workspaceLines 生成工作空间结果的可读行。
func workspaceLines(workspace sshclient.Workspace) []string {
	return []string{
		fmt.Sprintf("配置的工作空间: %s", workspace.Configured),
		fmt.Sprintf("远程 Home: %s", emptyAsUnknown(workspace.Home)),
		fmt.Sprintf("解析后的工作空间: %s", emptyAsUnknown(workspace.Root)),
		fmt.Sprintf("目录存在: %s", flagMark(workspace.Exists)),
		fmt.Sprintf("本次创建: %s", flagMark(workspace.Created)),
		fmt.Sprintf("可写: %s", flagMark(workspace.Writable)),
	}
}

// withSSHService 加载配置、打开数据库并执行一次 SSH 服务调用。
func withSSHService(
	ctx context.Context,
	a *app.App,
	configPath string,
	run func(ctx context.Context, service *app.SSHService) error,
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
	service, err := app.NewSSHService(store, a.Logger(), sshOptionsFrom(cfg))
	if err != nil {
		return err
	}
	return run(ctx, service)
}

// sshOptionsFrom 从应用配置推导 SSH 服务参数。
func sshOptionsFrom(cfg *config.Config) app.SSHOptions {
	if cfg == nil {
		return app.SSHOptions{}
	}
	return app.SSHOptions{DatabasePath: cfg.Storage.DatabasePath}
}
