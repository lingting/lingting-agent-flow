package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
)

// SSHOptions 是构造 SSH 服务的参数。
type SSHOptions struct {
	// KnownHostsPath 是额外的只读 known_hosts 文件路径，为空时使用用户默认 known_hosts。
	KnownHostsPath string
	// StorePath 是 AgentFlow 自己的主机密钥记录文件路径，为空时放在数据库目录下。
	StorePath string
	// DatabasePath 用于在 StorePath 为空时推导记录文件位置。
	DatabasePath string
	// ConnectTimeout 是 TCP 连接与认证的超时时间，零值使用默认值。
	ConnectTimeout time.Duration
}

// SSHService 提供已认证的 SSH 连接、远程命令执行与文件传输能力。
//
// 凭据只在调用期间通过内存中的 CredentialProvider 传递，本服务不保存、不记录
// 任何密码、私钥原文或私钥口令。
type SSHService struct {
	store    storage.Store
	dialer   *sshclient.Dialer
	hostKeys *sshclient.KnownHostsStore
	logger   *logging.Logger
}

// NewSSHService 构造 SSH 服务。
func NewSSHService(
	store storage.Store,
	logger *logging.Logger,
	options SSHOptions,
) (*SSHService, error) {
	if store == nil {
		return nil, fmt.Errorf("SSH 服务初始化失败: 存储不能为空")
	}
	hostKeys, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: knownHostsSources(options.KnownHostsPath),
		StorePath:    resolveStorePath(options),
	})
	if err != nil {
		return nil, fmt.Errorf("SSH 服务初始化失败: %w", err)
	}
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{
		HostKeys:       hostKeys,
		ConnectTimeout: options.ConnectTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("SSH 服务初始化失败: %w", err)
	}
	return &SSHService{store: store, dialer: dialer, hostKeys: hostKeys, logger: logger}, nil
}

// KnownHostsStorePath 返回 AgentFlow 主机密钥记录的写入路径。
func (s *SSHService) KnownHostsStorePath() string {
	if s == nil || s.hostKeys == nil {
		return ""
	}
	return s.hostKeys.StorePath()
}

// knownHostsSources 返回参与校验的只读 known_hosts 文件列表。
//
// 优先使用显式配置；未配置时复用用户 OpenSSH 的 known_hosts，
// 使已经信任过的主机无需在 AgentFlow 中重复确认。
func knownHostsSources(configured string) []string {
	trimmed := strings.TrimSpace(configured)
	if trimmed != "" {
		return []string{trimmed}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".ssh", "known_hosts"),
		filepath.Join(home, ".ssh", "known_hosts2"),
	}
}

// resolveStorePath 决定 AgentFlow 主机密钥记录文件的写入位置。
func resolveStorePath(options SSHOptions) string {
	if trimmed := strings.TrimSpace(options.StorePath); trimmed != "" {
		return trimmed
	}
	directory := strings.TrimSpace(options.DatabasePath)
	if directory == "" {
		return filepath.Join(".agentflow", "known_hosts")
	}
	return filepath.Join(filepath.Dir(directory), "known_hosts")
}

// TestConnectionOptions 约束一次连接测试。
type TestConnectionOptions struct {
	// Credentials 提供本次认证所需的凭据，密码与私钥口令认证必填。
	Credentials sshclient.CredentialProvider
	// CheckWorkspace 表示连接成功后同时解析并检查远程工作空间。
	CheckWorkspace bool
	// CreateWorkspace 表示检查工作空间时按需创建目录。
	CreateWorkspace bool
}

// ConnectionResult 是一次 SSH 连接测试的完整结果。
type ConnectionResult struct {
	// Host 是被测试的执行节点配置。
	Host domain.ExecutionHost
	// Authenticated 表示是否完成主机密钥验证与身份认证。
	Authenticated bool
	// RemoteUser 是远程返回的登录用户名，仅认证成功后填充。
	RemoteUser string
	// Home 是远程登录用户的 Home 目录。
	Home string
	// Workspace 是远程工作空间解析结果。
	Workspace *sshclient.Workspace
	// HostKey 是服务端提供的主机密钥信息。
	HostKey sshclient.HostKeyInfo
	// HostKeyVerdict 是主机密钥校验结论。
	HostKeyVerdict sshclient.HostKeyVerdict
	// Code 是结果类别，成功时为 CodeOK。
	Code sshclient.ErrorCode
	// Detail 是可读说明，不包含凭据内容。
	Detail string
	// Duration 是本次测试耗时。
	Duration time.Duration
}

// Succeeded 判断连接测试是否完成认证。
func (r ConnectionResult) Succeeded() bool {
	return r.Authenticated && r.Code == sshclient.CodeOK
}

// TestConnection 测试完整的 SSH 身份认证连接。
//
// 该方法区分 TCP 不可达、SSH 握手失败、主机密钥不可信与认证失败四类问题，
// 并把结果类别写入节点的最近连接记录。连接失败时仍返回结果，便于调用方
// 展示指纹或失败原因。
func (s *SSHService) TestConnection(
	ctx context.Context,
	id string,
	options TestConnectionOptions,
) (ConnectionResult, error) {
	host, err := s.loadHost(ctx, id)
	if err != nil {
		return ConnectionResult{}, err
	}
	result := ConnectionResult{Host: host}
	if !host.Enabled {
		result.Code = sshclient.CodeInvalidConfig
		result.Detail = "节点已禁用，请先启用该节点"
		return result, fmt.Errorf("测试 SSH 连接失败: %s", result.Detail)
	}
	endpoint, authConfig, err := endpointFor(host)
	if err != nil {
		result.Code = sshclient.CodeInvalidConfig
		result.Detail = err.Error()
		return result, fmt.Errorf("测试 SSH 连接失败: %w", err)
	}
	dialResult, dialErr := s.dialer.Dial(ctx, endpoint, authConfig, options.Credentials)
	result.HostKey = dialResult.HostKey
	result.HostKeyVerdict = dialResult.HostKeyVerdict
	result.Duration = dialResult.Duration
	if dialErr != nil {
		result.Code = sshclient.ErrorCodeOf(dialErr)
		result.Detail = dialErr.Error()
		s.recordResult(ctx, host, domain.ConnectStatusFailed, result.Code.String())
		return result, fmt.Errorf("测试 SSH 连接失败: %w", dialErr)
	}
	client := dialResult.Client
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			s.debug(ctx, "关闭 SSH 连接失败", slog.String("error", closeErr.Error()))
		}
	}()
	result.Authenticated = true
	result.Code = sshclient.CodeOK
	result.Detail = "SSH 认证成功"
	if err := s.inspectSession(ctx, client, &result, options); err != nil {
		s.recordResult(ctx, host, domain.ConnectStatusSuccess, "")
		return result, err
	}
	s.recordResult(ctx, host, domain.ConnectStatusSuccess, "")
	return result, nil
}

// inspectSession 在已认证连接上收集远程用户、Home 与工作空间信息。
func (s *SSHService) inspectSession(
	ctx context.Context,
	client sshclient.Client,
	result *ConnectionResult,
	options TestConnectionOptions,
) error {
	if user, err := s.remoteUser(ctx, client); err == nil {
		result.RemoteUser = user
	}
	home, err := sshclient.ResolveHome(ctx, client, 0)
	if err != nil {
		return fmt.Errorf("SSH 认证成功，但解析远程 Home 目录失败: %w", err)
	}
	result.Home = home
	if !options.CheckWorkspace {
		return nil
	}
	workspace, err := sshclient.EnsureWorkspace(
		ctx,
		client,
		result.Host.WorkspaceRoot,
		options.CreateWorkspace,
	)
	result.Workspace = &workspace
	if err != nil {
		return fmt.Errorf("SSH 认证成功，但远程工作空间不可用: %w", err)
	}
	return nil
}

// remoteUser 通过固定命令取得远程登录用户名。
func (s *SSHService) remoteUser(ctx context.Context, client sshclient.Client) (string, error) {
	command, err := sshclient.ShellCommand("id", "-un")
	if err != nil {
		return "", err
	}
	runResult, err := client.Run(ctx, command, sshclient.RunOptions{})
	if err != nil {
		return "", err
	}
	if !runResult.Succeeded() {
		return "", fmt.Errorf("远程命令退出码 %d", runResult.ExitCode)
	}
	return strings.TrimSpace(runResult.Stdout), nil
}

// recordResult 把最近一次连接测试结果写入数据库。
//
// 写入失败只记录日志，不影响已经完成的连接测试结论。
func (s *SSHService) recordResult(
	ctx context.Context,
	host domain.ExecutionHost,
	status domain.ConnectStatus,
	errorCode string,
) {
	if err := host.RecordConnectResult(status, errorCode, time.Now()); err != nil {
		s.debug(ctx, "构造连接结果失败", slog.String("error", err.Error()))
		return
	}
	if err := s.store.UpdateExecutionHost(ctx, host); err != nil {
		s.debug(ctx, "记录连接结果失败", slog.String("error", err.Error()))
		return
	}
	s.debug(
		ctx,
		"执行节点连接结果已记录",
		slog.String("id", host.ID.String()),
		slog.String("status", status.String()),
		slog.String("error_code", host.LastConnectError),
	)
}

// HostKeyProbe 是一次主机密钥探测的结果。
type HostKeyProbe struct {
	// Host 是被探测的执行节点配置。
	Host domain.ExecutionHost
	// Info 是服务端提供的主机密钥信息。
	Info sshclient.HostKeyInfo
	// Verdict 是当前校验结论。
	Verdict sshclient.HostKeyVerdict
	// Trusted 是已信任记录中的主机密钥信息。
	Trusted []sshclient.HostKeyInfo
}

// ProbeHostKey 探测远程主机的主机密钥与当前信任状态。
//
// 该操作只完成 SSH 握手并取得主机密钥，不执行身份认证，
// 因此不会要求任何凭据。
func (s *SSHService) ProbeHostKey(ctx context.Context, id string) (HostKeyProbe, error) {
	host, err := s.loadHost(ctx, id)
	if err != nil {
		return HostKeyProbe{}, err
	}
	endpoint, _, err := endpointFor(host)
	if err != nil {
		return HostKeyProbe{}, fmt.Errorf("探测主机密钥失败: %w", err)
	}
	info, err := s.dialer.ProbeHostKey(ctx, endpoint)
	if err != nil {
		return HostKeyProbe{Host: host, Info: info}, fmt.Errorf("探测主机密钥失败: %w", err)
	}
	check, checkErr := s.hostKeys.Check(endpoint.Host, endpoint.Port, info.PublicKey())
	probe := HostKeyProbe{
		Host:    host,
		Info:    info,
		Verdict: check.Verdict,
		Trusted: check.Trusted,
	}
	if checkErr != nil {
		return probe, fmt.Errorf("探测主机密钥失败: %w", checkErr)
	}
	return probe, nil
}

// TrustHostKey 在用户确认后记录信任的主机密钥。
//
// 该方法会重新探测服务端主机密钥，只有指纹与用户确认的指纹一致时才写入记录，
// 避免信任一个与用户核验结果不同的密钥。
func (s *SSHService) TrustHostKey(
	ctx context.Context,
	id string,
	fingerprint string,
) (domain.ExecutionHost, error) {
	expected := strings.TrimSpace(fingerprint)
	if expected == "" {
		return domain.ExecutionHost{}, fmt.Errorf("记录主机密钥失败: 必须给出已核验的指纹")
	}
	probe, err := s.ProbeHostKey(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, err
	}
	if probe.Info.Fingerprint != expected {
		return domain.ExecutionHost{}, fmt.Errorf(
			"记录主机密钥失败: 服务端当前指纹 %s 与已核验指纹 %s 不一致，请重新核验",
			probe.Info.Fingerprint,
			expected,
		)
	}
	if err := s.hostKeys.Trust(probe.Info); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("记录主机密钥失败: %w", err)
	}
	host := probe.Host
	if err := host.TrustHostKey(probe.Info.Algorithm, probe.Info.Fingerprint, time.Now()); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("记录主机密钥失败: %w", err)
	}
	if err := s.store.UpdateExecutionHost(ctx, host); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("记录主机密钥失败: %w", err)
	}
	s.debug(
		ctx,
		"主机密钥已记录信任",
		slog.String("id", host.ID.String()),
		slog.String("algorithm", probe.Info.Algorithm),
		slog.String("fingerprint", probe.Info.Fingerprint),
	)
	reloaded, err := s.store.GetExecutionHost(ctx, host.ID)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("记录主机密钥失败: %w", err)
	}
	return *reloaded, nil
}

// SSHConnection 是一条已认证的连接及其归属的执行节点。
type SSHConnection struct {
	// Host 是连接对应的执行节点配置。
	Host domain.ExecutionHost
	// Client 是已认证连接，调用方必须在使用结束后调用 Close。
	Client sshclient.Client
	// Home 是远程登录用户的 Home 目录。
	Home string
	// HostKey 是服务端提供的主机密钥信息。
	HostKey sshclient.HostKeyInfo
}

// Close 关闭连接，重复调用返回 nil。
func (c *SSHConnection) Close() error {
	if c == nil || c.Client == nil {
		return nil
	}
	return c.Client.Close()
}

// Connect 建立经过主机密钥验证与身份认证的连接。
func (s *SSHService) Connect(
	ctx context.Context,
	id string,
	provider sshclient.CredentialProvider,
) (*SSHConnection, error) {
	host, err := s.loadHost(ctx, id)
	if err != nil {
		return nil, err
	}
	if !host.Enabled {
		return nil, fmt.Errorf("连接执行节点失败: 节点 %s 已禁用", host.ID)
	}
	endpoint, authConfig, err := endpointFor(host)
	if err != nil {
		return nil, fmt.Errorf("连接执行节点失败: %w", err)
	}
	dialResult, dialErr := s.dialer.Dial(ctx, endpoint, authConfig, provider)
	if dialErr != nil {
		s.recordResult(ctx, host, domain.ConnectStatusFailed, sshclient.ErrorCodeOf(dialErr).String())
		return nil, fmt.Errorf("连接执行节点失败: %w", dialErr)
	}
	connection := &SSHConnection{
		Host:    host,
		Client:  dialResult.Client,
		HostKey: dialResult.HostKey,
	}
	home, err := sshclient.ResolveHome(ctx, connection.Client, 0)
	if err != nil {
		_ = connection.Close()
		s.recordResult(ctx, host, domain.ConnectStatusFailed, sshclient.CodeOK.String())
		return nil, err
	}
	connection.Home = home
	s.recordResult(ctx, host, domain.ConnectStatusSuccess, "")
	return connection, nil
}

// WorkspaceResult 是远程工作空间检查结果。
type WorkspaceResult struct {
	// Host 是被检查的执行节点配置。
	Host domain.ExecutionHost
	// Workspace 是解析与检查结果。
	Workspace sshclient.Workspace
}

// EnsureWorkspace 连接执行节点并检查远程工作空间可访问性。
//
// create 为 true 时按需创建工作空间目录；目录只在解析出的工作空间路径内创建，
// 不会切换到其他系统目录。
func (s *SSHService) EnsureWorkspace(
	ctx context.Context,
	id string,
	provider sshclient.CredentialProvider,
	create bool,
) (WorkspaceResult, error) {
	connection, err := s.Connect(ctx, id, provider)
	if err != nil {
		return WorkspaceResult{}, err
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			s.debug(ctx, "关闭 SSH 连接失败", slog.String("error", closeErr.Error()))
		}
	}()
	workspace, err := sshclient.EnsureWorkspace(
		ctx,
		connection.Client,
		connection.Host.WorkspaceRoot,
		create,
	)
	result := WorkspaceResult{Host: connection.Host, Workspace: workspace}
	if err != nil {
		return result, fmt.Errorf("检查远程工作空间失败: %w", err)
	}
	return result, nil
}

// TransferOptions 约束一次文件传输。
type TransferOptions struct {
	// Credentials 提供本次认证所需的凭据。
	Credentials sshclient.CredentialProvider
	// Overwrite 允许覆盖已存在的目标文件。
	Overwrite bool
	// CreateParent 表示自动创建目标文件的父目录。
	CreateParent bool
}

// UploadFile 通过 SFTP 上传本机文件到远程路径。
//
// 该能力用于后续远程环境准备与配置文件同步，不代表项目代码同步已经开始。
func (s *SSHService) UploadFile(
	ctx context.Context,
	id, localPath, remotePath string,
	options TransferOptions,
) error {
	return s.withTransfer(ctx, id, options.Credentials, func(transfer sshclient.FileTransfer) error {
		return transfer.Upload(ctx, localPath, remotePath, sshclient.UploadOptions{
			Overwrite:    options.Overwrite,
			CreateParent: options.CreateParent,
		})
	})
}

// DownloadFile 通过 SFTP 下载远程文件到本机路径。
func (s *SSHService) DownloadFile(
	ctx context.Context,
	id, remotePath, localPath string,
	options TransferOptions,
) error {
	return s.withTransfer(ctx, id, options.Credentials, func(transfer sshclient.FileTransfer) error {
		return transfer.Download(ctx, remotePath, localPath, sshclient.DownloadOptions{
			Overwrite:    options.Overwrite,
			CreateParent: options.CreateParent,
		})
	})
}

// withTransfer 建立连接并执行一次 SFTP 操作。
func (s *SSHService) withTransfer(
	ctx context.Context,
	id string,
	provider sshclient.CredentialProvider,
	run func(transfer sshclient.FileTransfer) error,
) error {
	connection, err := s.Connect(ctx, id, provider)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			s.debug(ctx, "关闭 SSH 连接失败", slog.String("error", closeErr.Error()))
		}
	}()
	transfer, err := connection.Client.SFTP()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := transfer.Close(); closeErr != nil {
			s.debug(ctx, "关闭 SFTP 通道失败", slog.String("error", closeErr.Error()))
		}
	}()
	return run(transfer)
}

// Host 返回指定执行节点的配置。
//
// 该方法供命令层在连接前读取认证方式，以便按正确的方式收集凭据。
func (s *SSHService) Host(ctx context.Context, id string) (domain.ExecutionHost, error) {
	return s.loadHost(ctx, id)
}

// loadHost 读取执行节点配置。
func (s *SSHService) loadHost(ctx context.Context, id string) (domain.ExecutionHost, error) {
	host, err := s.store.GetExecutionHost(ctx, domain.ID(strings.TrimSpace(id)))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.ExecutionHost{}, fmt.Errorf("查询执行节点失败: %w", err)
		}
		return domain.ExecutionHost{}, fmt.Errorf("查询执行节点失败: %w", err)
	}
	return *host, nil
}

// endpointFor 把执行节点配置转换为连接目标与认证配置。
func endpointFor(host domain.ExecutionHost) (sshclient.Endpoint, sshclient.AuthConfig, error) {
	endpoint := sshclient.Endpoint{Host: host.Host, Port: host.Port, Username: host.Username}
	method, err := toSSHAuthMethod(host.AuthMethod)
	if err != nil {
		return endpoint, sshclient.AuthConfig{}, err
	}
	authConfig := sshclient.AuthConfig{
		Method:         method,
		PrivateKeyPath: host.PrivateKeyPath,
	}
	if err := authConfig.Validate(); err != nil {
		return endpoint, authConfig, err
	}
	return endpoint, authConfig, nil
}

// toSSHAuthMethod 把领域层的认证方式转换为 SSH 客户端的认证方式。
func toSSHAuthMethod(method domain.AuthMethod) (sshclient.AuthMethod, error) {
	switch method {
	case domain.AuthMethodPassword:
		return sshclient.AuthMethodPassword, nil
	case domain.AuthMethodPrivateKey:
		return sshclient.AuthMethodPrivateKey, nil
	case domain.AuthMethodAgent:
		return sshclient.AuthMethodAgent, nil
	default:
		return "", fmt.Errorf("不支持的认证方式 %q", method)
	}
}

// debug 输出调试日志；未配置日志器时忽略。
func (s *SSHService) debug(ctx context.Context, message string, attrs ...slog.Attr) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Slog().LogAttrs(ctx, slog.LevelDebug, message, attrs...)
}
