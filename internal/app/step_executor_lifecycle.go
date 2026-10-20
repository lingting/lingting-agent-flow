package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrStepExecutorStopped 表示服务已停止，在途执行被取消。
//
// 它与调用方主动取消区分开：两者都会终止进程，但诊断信息必须说明真实的触发
// 来源，否则服务关闭会被误读为"用户取消了任务"。
var ErrStepExecutorStopped = errors.New("app: 步骤执行协调器已停止")

// 停止相关的时间参数。
const (
	// defaultStepExecutorStopTimeout 是等待在途执行收敛的默认时限。
	defaultStepExecutorStopTimeout = 30 * time.Second
)

// lifecycleState 记录后台服务的启动与停止状态。
type lifecycleState struct {
	mu      sync.Mutex
	started bool
	stopped bool
	// stopSignal 是停止信号，用于取消在途调用。
	stopSignal context.Context
	// stopOnce 保证停止信号只关闭一次。
	stopOnce context.CancelFunc
	// runs 统计在途执行，用于判断是否全部收敛。
	runs sync.WaitGroup
	// active 是在途执行数量。
	active int
}

// Name 返回后台服务名称。
func (e *StepExecutor) Name() string {
	return "step_executor"
}

// Start 标记服务可以接受执行请求。
//
// 该方法不启动长期 goroutine：执行是请求驱动的，长期运行状态只有"是否允许执行"
// 与"在途执行集合"。
func (e *StepExecutor) Start(ctx context.Context) error {
	state := &e.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.started {
		return fmt.Errorf("步骤执行协调器已经启动")
	}
	state.started = true
	// 分离自父上下文：Server 关闭信号通过 Stop 显式传播，而不是依赖 HTTP 请求上下文。
	stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))
	state.stopSignal = stopSignal
	state.stopOnce = stop
	return nil
}

// Stop 取消在途执行并等待其收敛。
//
// 停止后新的执行请求直接返回错误；已经在执行的调用会收到取消信号，执行协调器
// 负责终止子进程、保存已有 Session，并把记录推进到中断终态。
func (e *StepExecutor) Stop(ctx context.Context) error {
	state := &e.lifecycle
	state.mu.Lock()
	if state.stopped {
		state.mu.Unlock()
		return nil
	}
	state.stopped = true
	stop := state.stopOnce
	state.mu.Unlock()
	if stop != nil {
		stop()
	}
	done := make(chan struct{})
	go func() {
		state.runs.Wait()
		close(done)
	}()
	timeout := defaultStepExecutorStopTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("步骤执行协调器停止被取消: %w", ctx.Err())
	case <-time.After(timeout):
		return fmt.Errorf(
			"步骤执行协调器停止超时: 仍有 %d 个执行未收敛",
			e.ActiveExecutions(),
		)
	}
}

// beginRun 登记一次在途执行，服务已停止时拒绝。
func (e *StepExecutor) beginRun(ctx context.Context) (context.Context, func(), error) {
	state := &e.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stopped {
		return nil, nil, fmt.Errorf("%w: 不再接受新的执行请求", ErrStepExecutorStopped)
	}
	if !state.started {
		return nil, nil, fmt.Errorf("步骤执行协调器尚未启动")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrStepExecutorStopped, err)
	}
	state.active++
	state.runs.Add(1)
	stop := state.stopSignal
	runCtx, cancelRun := context.WithCancelCause(ctx)
	finish := func() {
		cancelRun(nil)
		state.mu.Lock()
		state.active--
		state.mu.Unlock()
		state.runs.Done()
	}
	if stop == nil {
		return runCtx, finish, nil
	}
	// 停止信号通过取消本次执行的上下文传播：Runner 看到的永远是同一个 ctx。
	go func() {
		select {
		case <-stop.Done():
			cancelRun(ErrStepExecutorStopped)
		case <-runCtx.Done():
		}
	}()
	return runCtx, finish, nil
}

// isServiceStopCause 判断取消是否由服务停止引起。
func isServiceStopCause(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return errors.Is(context.Cause(ctx), ErrStepExecutorStopped)
}

// debugLifecycle 记录生命周期事件，便于定位停止过程中的收敛情况。
func (e *StepExecutor) debugLifecycle(ctx context.Context, message string, attrs ...slog.Attr) {
	if e.logger == nil {
		return
	}
	e.logger.LogAttrs(ctx, slog.LevelDebug, message, attrs...)
}
