package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrValidationEngineStopped 表示服务已停止，在途验证被取消。
//
// 它与调用方主动取消区分开：两者都会终止命令，但诊断信息必须说明真实的触发来源，
// 否则服务关闭会被误读为"用户取消了验证"。
var ErrValidationEngineStopped = errors.New("app: 验证服务已停止")

// 停止相关的时间参数。
const (
	// defaultValidationStopTimeout 是等待在途验证收敛的默认时限。
	defaultValidationStopTimeout = 30 * time.Second
)

// validationLifecycle 记录验证服务的启动与停止状态。
type validationLifecycle struct {
	mu      sync.Mutex
	started bool
	stopped bool
	// stopSignal 是停止信号，用于取消在途验证。
	stopSignal context.Context
	// stopOnce 是停止信号的取消函数。
	stopOnce context.CancelFunc
	// runs 统计在途验证，用于判断是否全部收敛。
	runs sync.WaitGroup
	// active 是在途验证数量。
	active int
}

// Name 返回后台服务名称。
func (s *ValidationService) Name() string {
	return "validation_service"
}

// Start 标记服务可以接受验证请求。
//
// 该方法不启动长期 goroutine：验证是请求驱动的，长期运行状态只有"是否允许验证"
// 与"在途验证集合"。
func (s *ValidationService) Start(ctx context.Context) error {
	state := &s.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.started {
		return fmt.Errorf("验证服务已经启动")
	}
	state.started = true
	// 分离自父上下文：Server 关闭信号通过 Stop 显式传播，而不是依赖 HTTP 请求上下文。
	stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))
	state.stopSignal = stopSignal
	state.stopOnce = stop
	return nil
}

// Stop 取消在途验证并等待其收敛。
//
// 停止后新的验证请求直接返回错误；已经在执行的验证会收到取消信号，验证服务负责
// 中断远程命令并把记录推进到失败终态，不会留下 running 残留。
func (s *ValidationService) Stop(ctx context.Context) error {
	state := &s.lifecycle
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
	timeout := defaultValidationStopTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("验证服务停止被取消: %w", ctx.Err())
	case <-time.After(timeout):
		return fmt.Errorf(
			"验证服务停止超时: 仍有 %d 个验证未收敛",
			s.ActiveValidations(),
		)
	}
}

// beginRun 登记一次在途验证，服务已停止时拒绝。
func (s *ValidationService) beginRun(ctx context.Context) (context.Context, func(), error) {
	state := &s.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stopped {
		return nil, nil, fmt.Errorf("%w: 不再接受新的验证请求", ErrValidationEngineStopped)
	}
	if !state.started {
		return nil, nil, fmt.Errorf("验证服务尚未启动")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrValidationEngineStopped, err)
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
	// 停止信号通过取消本次验证的上下文传播：执行器看到的永远是同一个 ctx。
	go func() {
		select {
		case <-stop.Done():
			cancelRun(ErrValidationEngineStopped)
		case <-runCtx.Done():
		}
	}()
	return runCtx, finish, nil
}
