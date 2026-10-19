package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshprobe"
	"github.com/lingting/agentflow/internal/storage"
)

// ExecutionHostService 提供执行节点的管理能力。
//
// 业务规则集中在本服务，CLI 与后续 HTTP 接口共用同一套实现；探测能力只验证
// 网络与 SSH 协议标识，不代表已经完成身份认证。
type ExecutionHostService struct {
	store  storage.Store
	prober *sshprobe.Prober
	logger *logging.Logger
	// validator 是后台校验服务的提交入口，为空时校验由调用方显式触发。
	validator HostValidationSubmitter
	// hostValidator 是同步校验执行器，供 CLI 使用一次性凭据时调用。
	hostValidator *HostValidator
}

// NewExecutionHostService 构造执行节点服务。
func NewExecutionHostService(
	store storage.Store,
	logger *logging.Logger,
) (*ExecutionHostService, error) {
	if store == nil {
		return nil, fmt.Errorf("执行节点服务初始化失败: 存储不能为空")
	}
	prober, err := sshprobe.NewProber(sshprobe.Options{})
	if err != nil {
		return nil, fmt.Errorf("执行节点服务初始化失败: %w", err)
	}
	return &ExecutionHostService{store: store, prober: prober, logger: logger}, nil
}

// WithValidation 接入环境预检能力。
//
// 后台服务负责异步校验，校验器负责在提供一次性凭据时同步执行；两者都可以为空，
// 此时新增与修改节点只会把状态置为 checking，不会自动推进。
func (s *ExecutionHostService) WithValidation(
	submitter HostValidationSubmitter,
	validator *HostValidator,
) *ExecutionHostService {
	if s == nil {
		return s
	}
	s.validator = submitter
	s.hostValidator = validator
	return s
}

// CreateExecutionHostRequest 是新增执行节点的输入。
// CreateExecutionHostRequest 是新增执行节点的输入。
type CreateExecutionHostRequest struct {
	// ID 是节点标识，为空时自动生成。
	ID string
	// Name 是节点名称，为空时使用主机地址。
	Name string
	// Host 是 IP 地址或主机名。
	Host string
	// Port 是 SSH 端口，0 表示使用默认端口 22。
	Port int
	// Username 是 SSH 登录用户名。
	Username string
	// WorkspaceRoot 是远程工作空间路径，为空时使用默认值。
	WorkspaceRoot string
	// AuthMethod 是认证方式，为空时使用 SSH Agent。
	AuthMethod domain.AuthMethod
	// PrivateKeyPath 是本机私钥文件路径，仅私钥认证使用。
	PrivateKeyPath string
	// Default 表示是否同时设为默认节点。
	Default bool
}

// Create 新增执行节点。
//
// 该操作只保存连接目标与工作空间配置，不执行 SSH 登录，也不创建远程目录。
// 需要设为默认节点时，写入与默认切换在同一个事务内完成。
func (s *ExecutionHostService) Create(
	ctx context.Context,
	request CreateExecutionHostRequest,
) (domain.ExecutionHost, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = strings.TrimSpace(request.Host)
	}
	method := request.AuthMethod
	if method == "" {
		// 本版本只支持私钥认证：新建节点必须显式给出私钥路径。
		method = domain.AuthMethodPrivateKey
	}
	host, err := domain.NewExecutionHost(
		domain.ID(strings.TrimSpace(request.ID)),
		name,
		request.Host,
		request.Port,
		request.Username,
		request.WorkspaceRoot,
		time.Now(),
		domain.WithAuth(method, request.PrivateKeyPath),
	)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("新增执行节点失败: %w", err)
	}
	if err := s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if err := tx.CreateExecutionHost(ctx, host); err != nil {
			return err
		}
		if !request.Default {
			return nil
		}
		return tx.SetDefaultExecutionHost(ctx, host.ID)
	}); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("新增执行节点失败: %w", err)
	}
	s.debug(ctx, "执行节点已创建", slog.String("id", host.ID.String()))
	created, err := s.store.GetExecutionHost(ctx, host.ID)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("新增执行节点失败: %w", err)
	}
	// 事务提交后再提交后台校验：HTTP 请求不会等待远程连接与镜像查询。
	return s.scheduleValidation(ctx, *created)
}

// UpdateExecutionHostRequest 是更新执行节点配置的输入。
//
// 字段为 nil 表示不修改该项，避免把"未指定"与"设置为零值"混为一谈。
type UpdateExecutionHostRequest struct {
	// ID 是要更新的节点标识。
	ID string
	// Name 是新的节点名称。
	Name *string
	// Host 是新的主机地址。
	Host *string
	// Port 是新的 SSH 端口。
	Port *int
	// Username 是新的 SSH 登录用户名。
	Username *string
	// WorkspaceRoot 是新的远程工作空间路径。
	WorkspaceRoot *string
	// AuthMethod 是新的认证方式。
	AuthMethod *domain.AuthMethod
	// PrivateKeyPath 是新的本机私钥文件路径。
	PrivateKeyPath *string
	// Enabled 控制节点是否允许使用。
	Enabled *bool
	// Default 控制节点是否成为默认节点。
	Default *bool
}

// Update 更新执行节点配置。
func (s *ExecutionHostService) Update(
	ctx context.Context,
	request UpdateExecutionHostRequest,
) (domain.ExecutionHost, error) {
	id := domain.ID(strings.TrimSpace(request.ID))
	if err := id.Validate(); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("更新执行节点失败: %w", err)
	}
	current, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("更新执行节点失败: %w", err)
	}
	updated := *current
	applyHostChanges(&updated, request)
	updated.UpdatedAt = time.Now()
	// 影响连接目标、认证或工作空间的变更必须让旧校验结果失效。
	invalidated := validationAffectingFields(*current, updated)
	if invalidated {
		updated.InvalidateValidation(updated.UpdatedAt)
	}
	wantDefault := updated.IsDefault
	// is_default 统一由 SetDefaultExecutionHost 维护，避免与唯一索引冲突。
	updated.IsDefault = false
	if err := updated.Validate(); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("更新执行节点失败: %w", err)
	}
	if err := s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if err := tx.UpdateExecutionHost(ctx, updated); err != nil {
			return err
		}
		if !wantDefault {
			return nil
		}
		return tx.SetDefaultExecutionHost(ctx, id)
	}); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("更新执行节点失败: %w", err)
	}
	s.debug(ctx, "执行节点已更新", slog.String("id", id.String()))
	reloaded, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("更新执行节点失败: %w", err)
	}
	if !invalidated {
		return *reloaded, nil
	}
	return s.scheduleValidation(ctx, *reloaded)
}

// applyHostChanges 把请求中显式给出的字段写入目标节点。
func applyHostChanges(host *domain.ExecutionHost, request UpdateExecutionHostRequest) {
	if request.Name != nil {
		host.Name = strings.TrimSpace(*request.Name)
	}
	if request.Host != nil {
		host.Host = domain.NormalizeHostAddress(*request.Host)
	}
	if request.Port != nil {
		host.Port = *request.Port
	}
	if request.Username != nil {
		host.Username = strings.TrimSpace(*request.Username)
	}
	if request.WorkspaceRoot != nil {
		host.WorkspaceRoot = strings.TrimSpace(*request.WorkspaceRoot)
	}
	if request.AuthMethod != nil {
		host.AuthMethod = *request.AuthMethod
	}
	if request.PrivateKeyPath != nil {
		host.PrivateKeyPath = strings.TrimSpace(*request.PrivateKeyPath)
	}
	if request.Enabled != nil {
		host.Enabled = *request.Enabled
	}
	if request.Default != nil {
		host.IsDefault = *request.Default
	}
}

// List 返回全部执行节点。
func (s *ExecutionHostService) List(ctx context.Context) ([]domain.ExecutionHost, error) {
	hosts, err := s.store.ListExecutionHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询执行节点失败: %w", err)
	}
	return hosts, nil
}

// Get 返回指定执行节点。
func (s *ExecutionHostService) Get(ctx context.Context, id string) (domain.ExecutionHost, error) {
	host, err := s.store.GetExecutionHost(ctx, domain.ID(strings.TrimSpace(id)))
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("查询执行节点失败: %w", err)
	}
	return *host, nil
}

// SetDefault 把指定节点设为默认节点。
//
// 节点不存在或已被禁用时返回明确错误，默认状态在单个事务内切换。
func (s *ExecutionHostService) SetDefault(
	ctx context.Context,
	id string,
) (domain.ExecutionHost, error) {
	host, err := s.Get(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, err
	}
	if !host.Enabled {
		return domain.ExecutionHost{}, fmt.Errorf(
			"设置默认执行节点失败: 节点 %s 已禁用，请先启用该节点",
			host.ID,
		)
	}
	if !host.IsDefault {
		if err := s.store.SetDefaultExecutionHost(ctx, host.ID); err != nil {
			return domain.ExecutionHost{}, fmt.Errorf("设置默认执行节点失败: %w", err)
		}
	}
	s.debug(ctx, "默认执行节点已切换", slog.String("id", host.ID.String()))
	reloaded, err := s.store.GetExecutionHost(ctx, host.ID)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("设置默认执行节点失败: %w", err)
	}
	return *reloaded, nil
}

// Remove 逻辑删除执行节点配置。
//
// 删除只影响本机数据库记录，不会删除远程工作空间或远程 Docker 资源。
// 数据库不声明外键，因此级联行为由这里显式承担：仍有未结束任务使用该节点时
// 拒绝删除，避免正在执行的任务失去节点配置；其余历史记录（容器、环境版本、
// 已完成任务）保留节点标识，不会被连带删除。
func (s *ExecutionHostService) Remove(
	ctx context.Context,
	id string,
) (domain.ExecutionHost, error) {
	host, err := s.Get(ctx, id)
	if err != nil {
		return domain.ExecutionHost{}, err
	}
	active, err := s.hasActiveTasks(ctx, host.ID)
	if err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("删除执行节点失败: %w", err)
	}
	if active {
		return domain.ExecutionHost{}, fmt.Errorf(
			"删除执行节点失败: 节点 %s 仍有未结束的任务，请等待任务结束后重试",
			host.ID,
		)
	}
	if err := s.store.DeleteExecutionHost(ctx, host.ID); err != nil {
		return domain.ExecutionHost{}, fmt.Errorf("删除执行节点失败: %w", err)
	}
	s.debug(ctx, "执行节点已删除", slog.String("id", host.ID.String()))
	return host, nil
}

// hasActiveTasks 判断节点上是否存在未结束的任务。
//
// 终态任务（completed / failed / cancelled）不再需要节点，允许节点被逻辑删除。
func (s *ExecutionHostService) hasActiveTasks(ctx context.Context, hostID domain.ID) (bool, error) {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		HostID: hostID,
		ExcludeStatuses: []domain.TaskStatus{
			domain.TaskStatusCompleted,
			domain.TaskStatusFailed,
			domain.TaskStatusCancelled,
		},
		Limit: 1,
	})
	if err != nil {
		return false, err
	}
	return len(tasks) > 0, nil
}

// ExecutionHostProbeResult 是一次节点探测的结果。
type ExecutionHostProbeResult struct {
	// Host 是被探测的节点配置。
	Host domain.ExecutionHost
	// Probe 是探测结果，只描述网络与 SSH 协议标识。
	Probe sshprobe.Result
}

// Probe 探测指定节点的 TCP 与 SSH 协议可达性。
//
// 返回的错误只在节点不存在时出现；探测失败通过 Probe.ErrorCode 表达，
// 便于调用方按类别区分不可达、超时与协议标识无效。
func (s *ExecutionHostService) Probe(
	ctx context.Context,
	id string,
) (ExecutionHostProbeResult, error) {
	host, err := s.Get(ctx, id)
	if err != nil {
		return ExecutionHostProbeResult{}, err
	}
	result, probeErr := s.prober.Probe(ctx, sshprobe.Target{Host: host.Host, Port: host.Port})
	s.debug(
		ctx,
		"执行节点探测完成",
		slog.String("id", host.ID.String()),
		slog.String("target", result.Target),
		slog.String("error_code", result.ErrorCode.String()),
		slog.Bool("ssh_detected", result.SSHDetected),
	)
	if probeErr != nil {
		var structured *sshprobe.ProbeError
		if !errors.As(probeErr, &structured) {
			return ExecutionHostProbeResult{Host: host, Probe: result}, fmt.Errorf(
				"探测执行节点失败: %w",
				probeErr,
			)
		}
	}
	return ExecutionHostProbeResult{Host: host, Probe: result}, nil
}

// debug 输出调试日志；未配置日志器时忽略。
func (s *ExecutionHostService) debug(ctx context.Context, message string, attrs ...slog.Attr) {
	if s.logger == nil {
		return
	}
	s.logger.Slog().LogAttrs(ctx, slog.LevelDebug, message, attrs...)
}
