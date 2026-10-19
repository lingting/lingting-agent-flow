package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
)

// DockerServiceOptions 是构造远程 Docker 服务族的参数。
type DockerServiceOptions struct {
	// SSH 是已配置的 SSH 服务，负责建立经过验证的连接。
	SSH *SSHService
	// Store 是持久化入口。
	Store storage.Store
	// Logger 是应用日志器。
	Logger *logging.Logger
	// QueryTimeout 覆盖查询命令超时，零值使用默认值。
	QueryTimeout time.Duration
	// MutateTimeout 覆盖变更命令超时，零值使用默认值。
	MutateTimeout time.Duration
}

// remoteDocker 是一次远程 Docker 会话：已认证连接、SFTP 通道、命令执行器与目录布局。
//
// 会话只在单次应用服务调用内存在，调用结束后由 withRemoteDocker 统一关闭连接与通道。
type remoteDocker struct {
	host      domain.ExecutionHost
	home      string
	layout    docker.Layout
	client    sshclient.Client
	transfer  sshclient.FileTransfer
	runner    *docker.Runner
	workspace sshclient.Workspace
}

// close 关闭 SFTP 通道与 SSH 连接，只释放运行时资源，不删除远程目录。
func (r *remoteDocker) close(ctx context.Context, logger *logging.Logger) {
	if r == nil {
		return
	}
	if r.transfer != nil {
		if err := r.transfer.Close(); err != nil && logger != nil {
			logger.Slog().WarnContext(ctx, "关闭 SFTP 通道失败", slog.String("error", err.Error()))
		}
		r.transfer = nil
	}
	if r.client != nil {
		if err := r.client.Close(); err != nil && logger != nil {
			logger.Slog().WarnContext(ctx, "关闭 SSH 连接失败", slog.String("error", err.Error()))
		}
		r.client = nil
	}
}

// remoteDockerService 是环境、镜像与容器服务共用的远程会话建立逻辑。
type remoteDockerService struct {
	ssh           *SSHService
	store         storage.Store
	logger        *logging.Logger
	queryTimeout  time.Duration
	mutateTimeout time.Duration
}

// newRemoteDockerService 构造远程 Docker 服务公共依赖。
func newRemoteDockerService(options DockerServiceOptions) (*remoteDockerService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("远程 Docker 服务初始化失败: 存储不能为空")
	}
	if options.SSH == nil {
		return nil, fmt.Errorf("远程 Docker 服务初始化失败: SSH 服务不能为空")
	}
	return &remoteDockerService{
		ssh:           options.SSH,
		store:         options.Store,
		logger:        options.Logger,
		queryTimeout:  options.QueryTimeout,
		mutateTimeout: options.MutateTimeout,
	}, nil
}

// sessionOptions 约束一次远程会话的建立方式。
type sessionOptions struct {
	// CreateWorkspace 表示按需创建工作空间目录。
	CreateWorkspace bool
	// AllowMissingWorkspace 表示工作空间不存在时仍然继续。
	//
	// 只读预检需要它：预检的职责之一就是报告工作空间是否存在与可写。
	AllowMissingWorkspace bool
	// AllowUnvalidated 表示跳过执行节点准入校验。
	//
	// 只允许只读预检与校验器自身使用：校验的职责之一就是确认节点是否可用，
	// 因此它必须能在节点尚未通过校验时执行。
	AllowUnvalidated bool
}

// withRemoteDocker 建立连接并在会话内执行一次操作。
//
// 其余目录由各服务自行按需创建，不会提前生成没有内容的空目录。
func (s *remoteDockerService) withRemoteDocker(
	ctx context.Context,
	hostID string,
	createWorkspace bool,
	run func(ctx context.Context, session *remoteDocker) error,
) error {
	return s.withRemoteSession(ctx, hostID, sessionOptions{
		CreateWorkspace: createWorkspace,
	}, run)
}

// withRemoteSession 按给定选项建立连接并执行一次操作。
//
// 这是全部真实远程操作的唯一入口，因此执行节点准入校验也在这里实施：只有当前
// 配置版本的环境预检通过（validation_status = normal）的节点才允许建立远程会话。
// 校验本身与只读预检（AllowUnvalidated）不受该限制约束。
func (s *remoteDockerService) withRemoteSession(
	ctx context.Context,
	hostID string,
	options sessionOptions,
	run func(ctx context.Context, session *remoteDocker) error,
) error {
	if run == nil {
		return fmt.Errorf("远程 Docker 操作失败: 回调不能为空")
	}
	if err := s.ensureHostUsable(ctx, hostID, options); err != nil {
		return err
	}
	connection, err := s.ssh.Connect(ctx, hostID)
	if err != nil {
		return err
	}
	session := &remoteDocker{
		host:   connection.Host,
		home:   connection.Home,
		client: connection.Client,
	}
	defer func() {
		session.close(ctx, s.logger)
	}()
	workspace, err := sshclient.EnsureWorkspace(
		ctx,
		connection.Client,
		connection.Host.WorkspaceRoot,
		options.CreateWorkspace,
	)
	if err != nil {
		return fmt.Errorf("解析远程工作空间失败: %w", err)
	}
	if !workspace.Exists && !options.AllowMissingWorkspace {
		return fmt.Errorf(
			"远程工作空间不存在: %s；请先执行 agentflow docker init --machine %s",
			workspace.Root,
			connection.Host.ID,
		)
	}
	layout, err := docker.NewLayout(workspace.Root)
	if err != nil {
		return err
	}
	transfer, err := connection.Client.SFTP()
	if err != nil {
		return fmt.Errorf("打开 SFTP 通道失败: %w", err)
	}
	session.workspace = workspace
	session.layout = layout
	session.transfer = transfer
	runner, err := docker.NewRunner(connection.Client, docker.RunnerOptions{
		QueryTimeout:  s.queryTimeout,
		MutateTimeout: s.mutateTimeout,
	})
	if err != nil {
		return err
	}
	session.runner = runner
	return run(ctx, session)
}

// ensureHostUsable 校验执行节点是否允许用于真实远程操作。
//
// 规则：节点必须存在且未删除，并且当前配置版本的 validation_status 必须是 normal。
// 处于 checking 或 abnormal 的节点一律拒绝，并给出可定位的原因与后续操作提示。
func (s *remoteDockerService) ensureHostUsable(
	ctx context.Context,
	hostID string,
	options sessionOptions,
) error {
	if options.AllowUnvalidated {
		return nil
	}
	return s.ensureHostValidated(ctx, hostID)
}

// ensureHostValidated 是准入校验的实际实现，供远程操作与任务执行入口共用。
func (s *remoteDockerService) ensureHostValidated(ctx context.Context, hostID string) error {
	host, err := s.loadHost(ctx, hostID)
	if err != nil {
		return err
	}
	return EnsureHostUsableForRemoteTask(host)
}

// EnsureHostUsableForRemoteTask 判断执行节点是否允许用于真实远程任务执行。
//
// 任何使用持久化执行节点执行真实远程操作的入口都必须调用本函数（或经由
// withRemoteSession 间接调用），不能只依赖 Web UI 禁用按钮。
func EnsureHostUsableForRemoteTask(host domain.ExecutionHost) error {
	if !host.Enabled {
		return fmt.Errorf(
			"%w: 执行节点 %s 已禁用，请先启用该节点",
			ErrHostValidationUnavailable,
			host.ID,
		)
	}
	switch host.ValidationStatus {
	case domain.ValidationStatusNormal:
		return nil
	case domain.ValidationStatusChecking:
		return fmt.Errorf(
			"%w: 执行节点 %s 的环境预检尚未完成（状态 checking），请等待校验结束后重试；"+
				"可用 agentflow host validate %s 重新触发校验",
			ErrHostValidationUnavailable,
			host.ID,
			host.ID,
		)
	default:
		message := strings.TrimSpace(host.ValidationMessage)
		if message == "" {
			message = "环境预检未通过"
		}
		return fmt.Errorf(
			"%w: 执行节点 %s 不可用（状态 %s）：%s；请修复后执行 agentflow host validate %s",
			ErrHostValidationUnavailable,
			host.ID,
			host.ValidationStatus,
			message,
			host.ID,
		)
	}
}

// SSHService 返回命令层用于构造凭据提供器的 SSH 服务。
func (s *remoteDockerService) SSHService() *SSHService {
	if s == nil {
		return nil
	}
	return s.ssh
}

// debug 输出调试日志；未配置日志器时忽略。
func (s *remoteDockerService) debug(ctx context.Context, message string, attrs ...slog.Attr) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Slog().LogAttrs(ctx, slog.LevelDebug, message, attrs...)
}

// loadHost 读取执行节点配置。
func (s *remoteDockerService) loadHost(ctx context.Context, hostID string) (domain.ExecutionHost, error) {
	id := domain.ID(strings.TrimSpace(hostID))
	if err := id.Validate(); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("读取执行节点失败: %w", err)
	}
	host, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("读取执行节点失败: %w", err)
	}
	return *host, nil
}
