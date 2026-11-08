package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// 本文件实现项目执行权的后台续租与收敛。
//
// 它只有一个长期运行的循环：按固定周期续租本进程持有的执行权，并收敛已经真实收敛的
// 项目。循环不领取任务、不启动工作流、不接触容器：领取仍然由调度器完成。
//
// 单独成类型的原因：ProjectExecutionService 的 Start 是业务方法（开始一个项目），而
// 生命周期接口要求的 Start 是"启动后台循环"，同名的两个语义不能挤在一个类型上。

// defaultProjectExecutionStopTimeout 是等待后台循环退出的默认时限。
const defaultProjectExecutionStopTimeout = 10 * time.Second

// ProjectExecutionKeeperOptions 是构造执行权续租服务的输入。
type ProjectExecutionKeeperOptions struct {
	// Service 是项目执行协调服务，必填。
	Service *ProjectExecutionService
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// StopTimeout 是停止时等待循环退出的时限，零值使用默认值。
	StopTimeout time.Duration
}

// ProjectExecutionKeeper 负责续租与收敛项目执行权。
type ProjectExecutionKeeper struct {
	service *ProjectExecutionService
	logger  *slog.Logger
	timeout time.Duration

	mu       sync.Mutex
	started  bool
	stopped  bool
	stopOnce context.CancelFunc
	runs     sync.WaitGroup
}

// NewProjectExecutionKeeper 构造执行权续租服务。
func NewProjectExecutionKeeper(
	options ProjectExecutionKeeperOptions,
) (*ProjectExecutionKeeper, error) {
	if options.Service == nil {
		return nil, fmt.Errorf("%w: 项目执行服务不能为空", ErrProjectExecutionInvalidInput)
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := options.StopTimeout
	if timeout <= 0 {
		timeout = defaultProjectExecutionStopTimeout
	}
	return &ProjectExecutionKeeper{
		service: options.Service,
		logger:  logger,
		timeout: timeout,
	}, nil
}

// Name 返回后台服务名称。
func (k *ProjectExecutionKeeper) Name() string {
	return "project_execution_keeper"
}

// Start 启动续租与收敛循环。
func (k *ProjectExecutionKeeper) Start(ctx context.Context) error {
	k.mu.Lock()
	if k.started {
		k.mu.Unlock()
		return fmt.Errorf("项目执行续租服务已经启动")
	}
	k.started = true
	stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))
	k.stopOnce = stop
	k.mu.Unlock()

	k.runs.Add(1)
	go func() {
		defer k.runs.Done()
		k.loop(stopSignal)
	}()
	return nil
}

// Stop 停止续租循环，并释放没有在途任务的项目执行权。
//
// 停止不重放任何远程操作：需要收敛的任务由既有工作流路径处理，执行权在 TTL 内到期。
func (k *ProjectExecutionKeeper) Stop(ctx context.Context) error {
	k.mu.Lock()
	if k.stopped {
		k.mu.Unlock()
		return nil
	}
	k.stopped = true
	stop := k.stopOnce
	k.mu.Unlock()
	if stop != nil {
		stop()
	}
	done := make(chan struct{})
	go func() {
		k.runs.Wait()
		close(done)
	}()
	timeout := k.timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("项目执行续租服务停止被取消: %w", ctx.Err())
	case <-time.After(timeout):
		return fmt.Errorf("项目执行续租服务停止超时")
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := k.service.ReleaseOnStop(cleanupCtx); err != nil {
		k.logger.WarnContext(
			cleanupCtx,
			"停止时释放项目执行权失败，执行权将在有效期内到期",
			slog.String("error", errorSummary(err)),
		)
	}
	k.service.Close()
	return nil
}

// loop 是续租与收敛循环。
func (k *ProjectExecutionKeeper) loop(ctx context.Context) {
	interval := k.service.renew
	k.renewAndReconcile(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			k.renewAndReconcile(ctx)
		}
	}
}

// renewAndReconcile 执行一次续租与收敛。
//
// 两个动作的失败都不会终止循环：下一周期会重新读取数据库事实，任务的真实结论只由
// 既有工作流路径写入。
func (k *ProjectExecutionKeeper) renewAndReconcile(ctx context.Context) {
	if err := k.service.RenewLeases(ctx); err != nil {
		k.logger.WarnContext(
			ctx,
			"项目执行权续租存在失败项",
			slog.String("error", errorSummary(err)),
		)
	}
	if err := k.service.Reconcile(ctx); err != nil {
		k.logger.WarnContext(
			ctx,
			"项目执行权收敛存在失败项",
			slog.String("error", errorSummary(err)),
		)
	}
}
