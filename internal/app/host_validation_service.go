package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
)

// 后台校验服务的稳定错误。
var (
	// ErrValidationQueueFull 表示待校验队列已满，配置未能进入校验流程。
	ErrValidationQueueFull = errors.New("app: 校验任务队列已满")
	// ErrValidationServiceStopped 表示校验服务已停止，不再接受新任务。
	ErrValidationServiceStopped = errors.New("app: 校验服务已停止")
)

// 后台校验服务的默认参数。
const (
	// DefaultValidationQueueSize 是待校验队列长度。
	DefaultValidationQueueSize = 128
	// DefaultValidationWorkers 是并发校验协程数量。
	DefaultValidationWorkers = 2
	// validationStopTimeout 是停止时等待在途校验结束的时间。
	validationStopTimeout = 20 * time.Second
)

// HostValidationService 是执行节点环境预检的后台服务。
//
// 它接入 agentflow server 的统一生命周期：由 Server 启动与停止，不在 HTTP Handler
// 内创建不受管理的长期 goroutine。服务保证：
//   - 同一配置的同一代次不会被重复执行；
//   - 停止时取消在途校验并等待其释放 SSH 连接；
//   - 启动时恢复停留在 checking 的记录；
//   - 校验失败或无法开始校验时把状态收敛为 abnormal，不会永久停留在 checking。
type HostValidationService struct {
	validator *HostValidator
	store     storage.Store
	logger    *logging.Logger

	queueSize int
	workers   int

	mu      sync.Mutex
	started bool
	stopped bool
	// inflight 记录正在执行或排队中的 (节点, 代次)。
	inflight map[validationKey]struct{}
	queue    chan validationKey
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	faults   chan error
}

// validationKey 唯一标识一次待执行的校验。
type validationKey struct {
	hostID     domain.ID
	generation int64
}

// HostValidationServiceOptions 是构造后台校验服务的参数。
type HostValidationServiceOptions struct {
	// Validator 是校验执行器，必填。
	Validator *HostValidator
	// Store 是持久化入口，必填。
	Store storage.Store
	// Logger 是应用日志器。
	Logger *logging.Logger
	// QueueSize 覆盖队列长度，零值使用 DefaultValidationQueueSize。
	QueueSize int
	// Workers 覆盖并发数，零值使用 DefaultValidationWorkers。
	Workers int
}

// NewHostValidationService 构造后台校验服务。
func NewHostValidationService(options HostValidationServiceOptions) (*HostValidationService, error) {
	if options.Validator == nil {
		return nil, fmt.Errorf("校验服务初始化失败: 校验器不能为空")
	}
	if options.Store == nil {
		return nil, fmt.Errorf("校验服务初始化失败: 存储不能为空")
	}
	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = DefaultValidationQueueSize
	}
	workers := options.Workers
	if workers <= 0 {
		workers = DefaultValidationWorkers
	}
	return &HostValidationService{
		validator: options.Validator,
		store:     options.Store,
		logger:    options.Logger,
		queueSize: queueSize,
		workers:   workers,
		inflight:  make(map[validationKey]struct{}),
		faults:    make(chan error, 1),
	}, nil
}

// Name 返回后台服务名称。
func (s *HostValidationService) Name() string {
	return "execution_host_validation"
}

// Start 启动后台校验服务并恢复停留在 checking 的记录。
//
// 恢复失败不会让服务启动失败：单条记录的问题不应阻止整个服务运行，但必须记录
// 明确错误，并在恢复时把无法开始的记录收敛为 abnormal。
func (s *HostValidationService) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("校验服务已经启动")
	}
	s.started = true
	serviceCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.queue = make(chan validationKey, s.queueSize)
	s.mu.Unlock()

	for index := 0; index < s.workers; index++ {
		s.wg.Add(1)
		go s.worker(serviceCtx)
	}
	if err := s.recoverPending(ctx); err != nil {
		s.loggerWarn(ctx, "恢复待校验的执行节点失败", slog.String("error", err.Error()))
	}
	return nil
}

// Stop 取消在途校验并等待协程退出。
func (s *HostValidationService) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(validationStopTimeout):
		return fmt.Errorf("校验服务停止超时: 仍有在途校验未结束")
	case <-ctx.Done():
		return fmt.Errorf("校验服务停止被取消: %w", ctx.Err())
	}
}

// Faults 返回运行期致命错误通道。
//
// 校验失败属于业务结果，会写入节点状态，不作为服务故障上报；该通道保留给
// 后续可能出现的不可恢复错误，当前不会写入任何值。
func (s *HostValidationService) Faults() <-chan error {
	return s.faults
}

// SubmitHost 读取节点当前代次并把校验提交到后台队列。
//
// 提交只做入队，不等待校验完成，因此 HTTP 请求不会因为远程连接与镜像查询而阻塞。
// 同一配置的同一代次不会被重复执行：已在排队或执行中的任务直接忽略。
//
// 后台校验只使用配置中的非机密信息，因此仅支持 SSH Agent 认证的节点；需要密码或
// 私钥口令的节点必须通过 CLI 的同步校验入口提供内存凭据。
func (s *HostValidationService) SubmitHost(ctx context.Context, hostID string) error {
	key, err := s.keyFor(ctx, hostID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.stopped || !s.started {
		s.mu.Unlock()
		return ErrValidationServiceStopped
	}
	if _, ok := s.inflight[key]; ok {
		s.mu.Unlock()
		return nil
	}
	s.inflight[key] = struct{}{}
	queue := s.queue
	s.mu.Unlock()

	select {
	case queue <- key:
		return nil
	default:
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		return ErrValidationQueueFull
	}
}

// InFlight 判断指定节点是否已有同代次的校验在排队或执行中。
func (s *HostValidationService) InFlight(hostID domain.ID, generation int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.inflight[validationKey{hostID: hostID, generation: generation}]
	return ok
}

// keyFor 读取节点当前代次并构造校验键。
func (s *HostValidationService) keyFor(ctx context.Context, hostID string) (validationKey, error) {
	host, err := s.validator.loadHost(ctx, hostID)
	if err != nil {
		return validationKey{}, err
	}
	return validationKey{hostID: host.ID, generation: host.ValidationGeneration}, nil
}

// worker 消费队列中的校验任务。
func (s *HostValidationService) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-s.queue:
			s.run(ctx, key)
		}
	}
}

// run 执行一次校验并清理在途标记。
func (s *HostValidationService) run(ctx context.Context, key validationKey) {
	defer func() {
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
	}()
	s.execute(ctx, key)
}

// execute 调用校验器并把无法开始校验的情况收敛为 abnormal。
func (s *HostValidationService) execute(
	ctx context.Context,
	key validationKey,
) {
	outcome, err := s.validator.Validate(ctx, ValidationRequest{
		HostID: key.hostID.String(),
	})
	if err != nil {
		s.loggerWarn(
			ctx,
			"执行节点校验失败",
			slog.String("id", key.hostID.String()),
			slog.Int64("generation", key.generation),
			slog.String("error", err.Error()),
		)
		// 校验本身失败（而非检查项失败）：必须让状态收敛，不能停留在 checking。
		if failErr := s.validator.FailWithoutChecks(
			context.WithoutCancel(ctx),
			key.hostID.String(),
			key.generation,
			"校验执行失败: "+err.Error(),
		); failErr != nil {
			s.loggerWarn(
				ctx,
				"记录校验失败状态时出错",
				slog.String("id", key.hostID.String()),
				slog.String("error", failErr.Error()),
			)
		}
		return
	}
	if outcome.Stale {
		s.loggerWarn(
			ctx,
			"校验结果已过期，未写入",
			slog.String("id", key.hostID.String()),
			slog.Int64("generation", key.generation),
		)
	}
}

// recoverPending 恢复停留在 checking 的记录。
//
// 启动时可能存在上次进程退出时尚未完成的校验：它们必须重新调度，不能永久卡在
// checking。这里只按当前代次重新入队，不修改已保存的旧结果。
func (s *HostValidationService) recoverPending(ctx context.Context) error {
	hosts, err := s.store.ListExecutionHostsByValidationStatus(
		ctx,
		domain.ValidationStatusChecking,
	)
	if err != nil {
		return fmt.Errorf("查询待校验执行节点失败: %w", err)
	}
	if len(hosts) == 0 {
		return nil
	}
	recovered := 0
	for _, host := range hosts {
		if submitErr := s.SubmitHost(ctx, host.ID.String()); submitErr != nil {
			// 无法入队时把状态收敛为 abnormal 并说明原因，避免永久停留。
			if failErr := s.validator.FailWithoutChecks(
				ctx,
				host.ID.String(),
				host.ValidationGeneration,
				"无法重新调度校验: "+submitErr.Error(),
			); failErr != nil {
				s.loggerWarn(
					ctx,
					"恢复校验失败且无法记录原因",
					slog.String("id", host.ID.String()),
					slog.String("error", failErr.Error()),
				)
			}
			continue
		}
		recovered++
	}
	s.loggerInfo(
		ctx,
		"已重新调度待校验的执行节点",
		slog.Int("count", recovered),
	)
	return nil
}

// loggerWarn 输出警告日志。
func (s *HostValidationService) loggerWarn(ctx context.Context, message string, attrs ...slog.Attr) {
	if s.logger == nil {
		return
	}
	s.logger.Slog().LogAttrs(ctx, slog.LevelWarn, message, attrs...)
}

// loggerInfo 输出信息日志。
func (s *HostValidationService) loggerInfo(ctx context.Context, message string, attrs ...slog.Attr) {
	if s.logger == nil {
		return
	}
	s.logger.Slog().LogAttrs(ctx, slog.LevelInfo, message, attrs...)
}
