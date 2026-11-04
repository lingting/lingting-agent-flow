package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// 规划 Agent 服务的准入错误。
//
// 两者分开是因为诊断信息不同："还没启动"说明装配或启动顺序有问题，"已经停止"说明
// Server 正在关闭：调用方据此区分配置错误与正常的关闭拒绝。
var (
	// ErrPlanningAgentNotStarted 表示服务尚未启动，规划调用被拒绝。
	ErrPlanningAgentNotStarted = errors.New("app: 规划 Agent 服务尚未启动")
	// ErrPlanningAgentStopped 表示服务正在停止或已经停止，规划调用被拒绝。
	ErrPlanningAgentStopped = errors.New("app: 规划 Agent 服务已停止")
)

// planningAgentStopTimeout 是等待在途规划调用收敛的默认时限。
//
// 规划调用包含容器内 Pi 执行与随后的 Session 导出、归档，收敛通常远快于该上限；
// 上限只用于避免停止无限期挂起。Server 传入的 deadline 更早时以传入值优先。
const planningAgentStopTimeout = 30 * time.Second

// Name 返回后台服务名称。
func (s *PlanningAgentService) Name() string {
	return "planning_agent"
}

// Start 标记服务可以接受规划调用。
//
// 它不启动长期 goroutine：规划调用是请求驱动的，长期运行状态只有"是否允许调用"与
// "在途调用集合"。重复启动直接失败，避免覆盖已建立的生命周期取消信号。
func (s *PlanningAgentService) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("规划 Agent 服务启动被取消: %w", err)
	}
	state := &s.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.started {
		return fmt.Errorf("规划 Agent 服务已经启动")
	}
	state.started = true
	// 分离自父上下文：Server 关闭信号通过 Stop 显式传播，而不是依赖某次请求的上下文。
	stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))
	state.stopSignal = stopSignal
	state.stopOnce = stop
	return nil
}

// Stop 取消在途规划调用并等待其完整收敛。
//
// 停止边界是原子的：先在同一临界区内切到"已停止"，此后到来的调用一律被拒绝，不会
// 出现停止期间又启动一次 Pi 的情况。已准入的调用收到取消信号，Runner 仍会完成
// Session 提取与归档，因此这里的等待覆盖到 RunRole 完整返回，而不只是 Pi 主进程退出。
//
// 重复调用是安全的：取消函数可重复调用，等待也只会观察已经不会再增加的在途集合。
// 等待过程响应传入 ctx 的取消与 deadline；超时或未能收敛时返回真实错误，不报告成功。
//
// 它不释放规划容器、不删除快照卷、也不删除 Session 归档：Server 停止不等于规划会话结束。
func (s *PlanningAgentService) Stop(ctx context.Context) error {
	state := &s.lifecycle
	state.mu.Lock()
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
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < planningAgentStopTimeout {
		// 调用方给出的 deadline 更早：只依赖传入 ctx 的取消信号，不再额外启动定时器。
		// 两个几乎同时到期的计时器会让"取消"与"超时"的判定变成随机结果。
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("规划 Agent 服务停止被取消: %w", ctx.Err())
		}
	}
	timer := time.NewTimer(planningAgentStopTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("规划 Agent 服务停止被取消: %w", ctx.Err())
	case <-timer.C:
		return fmt.Errorf(
			"规划 Agent 服务停止超时: 仍有 %d 次规划调用未收敛",
			s.ActiveCalls(),
		)
	}
}

// ActiveCalls 返回本进程内已经准入但尚未返回的规划调用数量。
func (s *PlanningAgentService) ActiveCalls() int {
	state := &s.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.active
}

// beginCall 登记一次在途规划调用，服务未启动或已停止时拒绝。
//
// 准入判断与 runs.Add 在同一把锁内完成，Stop 也在同一把锁内切到"已停止"，因此
// Wait 开始之后不可能再出现新的 Add，不存在"停止期间又接受一次调用"的竞态窗口。
func (s *PlanningAgentService) beginCall(ctx context.Context) (context.Context, func(), error) {
	state := &s.lifecycle
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stopped {
		return nil, nil, fmt.Errorf("%w: 不再接受新的规划调用", ErrPlanningAgentStopped)
	}
	if !state.started {
		return nil, nil, fmt.Errorf("%w: 规划调用被拒绝", ErrPlanningAgentNotStarted)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrPlanningAgentStopped, err)
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
	// 停止信号通过取消本次调用的上下文传播：Runner 看到的始终是同一个 ctx。
	go func() {
		select {
		case <-stop.Done():
			cancelRun(ErrPlanningAgentStopped)
		case <-runCtx.Done():
		}
	}()
	return runCtx, finish, nil
}
