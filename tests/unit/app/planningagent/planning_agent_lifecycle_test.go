package planningagent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
)

// TestPlanningAgentServiceRejectsCallBeforeStart 校验未启动时规划调用被拒绝。
func TestPlanningAgentServiceRejectsCallBeforeStart(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: false})
	_, err := fixture.service.RunRole(context.Background(), fixture.request(0))
	if !errors.Is(err, app.ErrPlanningAgentNotStarted) {
		t.Fatalf("未启动时应拒绝规划调用，实际错误: %v", err)
	}
	if calls := fixture.port.callCount(); calls != 0 {
		t.Fatalf("被拒绝的调用不应进入执行，实际进入 %d 次", calls)
	}
}

// TestPlanningAgentServiceRunsRoleAfterStart 校验启动后调用能进入执行阶段。
func TestPlanningAgentServiceRunsRoleAfterStart(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := fixture.call(ctx, 0)
	fixture.port.waitEntered(t, 1)
	request := fixture.port.requestAt(t, 0)
	if request.ContainerID != fixture.targets[0].container.ID.String() {
		t.Fatalf("调用应落在本会话的规划容器上，实际 %s", request.ContainerID)
	}
	if request.StableSession == nil || request.StableSession.ContainerPath == "" {
		t.Fatal("规划调用必须携带服务端生成的稳定 Session 位置")
	}
	if err := fixture.port.contextAt(t, 0).Err(); err != nil {
		t.Fatalf("在途调用的上下文不应提前取消: %v", err)
	}
	cancel()
	err := waitCall(t, done)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("调用方取消应作为调用错误返回，实际: %v", err)
	}
	if active := fixture.service.ActiveCalls(); active != 0 {
		t.Fatalf("调用返回后不应残留在途计数，实际 %d", active)
	}
}

// TestPlanningAgentServiceRejectsCallAfterStop 校验停止完成后规划调用被拒绝。
func TestPlanningAgentServiceRejectsCallAfterStop(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	if err := fixture.stop(t); err != nil {
		t.Fatalf("停止规划 Agent 服务失败: %v", err)
	}
	_, err := fixture.service.RunRole(context.Background(), fixture.request(0))
	if !errors.Is(err, app.ErrPlanningAgentStopped) {
		t.Fatalf("停止后应拒绝规划调用，实际错误: %v", err)
	}
	if calls := fixture.port.callCount(); calls != 0 {
		t.Fatalf("停止后的调用不应进入执行，实际进入 %d 次", calls)
	}
}

// TestPlanningAgentServiceStopCancelsInflightCall 校验在途调用收到停止取消并完成收敛。
func TestPlanningAgentServiceStopCancelsInflightCall(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	done := fixture.call(context.Background(), 0)
	fixture.port.waitEntered(t, 1)
	inflight := fixture.port.contextAt(t, 0)
	if err := fixture.stop(t); err != nil {
		t.Fatalf("停止规划 Agent 服务失败: %v", err)
	}
	// 取消必须真正传到 Agent 执行入口的上下文，而不是只改生命周期状态字段。
	if err := inflight.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("执行入口应收到取消信号，实际: %v", err)
	}
	if err := waitCall(t, done); err == nil {
		t.Fatal("被取消的规划调用不应返回成功")
	}
	if active := fixture.service.ActiveCalls(); active != 0 {
		t.Fatalf("停止完成后不应残留在途调用，实际 %d", active)
	}
}

// TestPlanningAgentServiceStopWaitsForConvergence 校验停止等待调用完整收敛后才返回。
func TestPlanningAgentServiceStopWaitsForConvergence(t *testing.T) {
	t.Parallel()
	hold := 200 * time.Millisecond
	fixture := newPlanningFixture(t, fixtureOptions{start: true, hold: hold})
	done := fixture.call(context.Background(), 0)
	fixture.port.waitEntered(t, 1)
	started := time.Now()
	if err := fixture.stop(t); err != nil {
		t.Fatalf("停止规划 Agent 服务失败: %v", err)
	}
	elapsed := time.Since(started)
	if finished := fixture.port.finishCount(); finished != 1 {
		t.Fatalf("停止返回时调用尚未收敛，已返回 %d 次", finished)
	}
	if elapsed < hold {
		t.Fatalf("停止应在调用收敛之后返回，实际耗时 %s", elapsed)
	}
	if err := waitCall(t, done); err == nil {
		t.Fatal("被取消的规划调用不应返回成功")
	}
}

// TestPlanningAgentServiceStopWaitsForMultipleCalls 校验停止会取消并等待全部在途调用。
func TestPlanningAgentServiceStopWaitsForMultipleCalls(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{sessions: 3, start: true})
	done := make([]<-chan error, 3)
	for index := 0; index < 3; index++ {
		done[index] = fixture.call(context.Background(), index)
	}
	fixture.port.waitEntered(t, 3)
	if err := fixture.stop(t); err != nil {
		t.Fatalf("停止规划 Agent 服务失败: %v", err)
	}
	for index := 0; index < 3; index++ {
		if active := fixture.service.ActiveCalls(); active != 0 {
			t.Fatalf("停止完成后仍有 %d 次在途调用", active)
		}
		if err := fixture.port.contextAt(t, index).Err(); !errors.Is(err, context.Canceled) {
			t.Fatalf("第 %d 次调用应收到取消信号，实际: %v", index+1, err)
		}
		if err := waitCall(t, done[index]); err == nil {
			t.Fatalf("第 %d 次被取消的调用不应返回成功", index+1)
		}
	}
	if finished := fixture.port.finishCount(); finished != 3 {
		t.Fatalf("停止返回时应已收敛全部调用，实际 %d 次", finished)
	}
}

// TestPlanningAgentServiceAdmissionStopsAtBoundary 校验停止边界之后不再接受新调用。
func TestPlanningAgentServiceAdmissionStopsAtBoundary(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true})
	const workers = 6
	stopDone := make(chan struct{})
	stopErr := make(chan error, 1)
	go func() {
		defer close(stopDone)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stopErr <- fixture.service.Stop(ctx)
	}()
	var wg sync.WaitGroup
	// violations 收集"停止返回后仍被受理"的违规调用，容量取 worker 数量，写入不阻塞。
	violations := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopDone:
					// 停止已经返回：此后的每次调用都必须在准入阶段被拒绝。
					_, err := fixture.service.RunRole(context.Background(), fixture.request(0))
					if !errors.Is(err, app.ErrPlanningAgentStopped) {
						violations <- err
					}
					return
				default:
				}
				if _, err := fixture.service.RunRole(
					context.Background(),
					fixture.request(0),
				); errors.Is(err, app.ErrPlanningAgentStopped) {
					// 已被停止边界拒绝：稍作退让，避免空转抢占准备执行的在途调用。
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
	<-stopDone
	callsAtStop := fixture.port.callCount()
	wg.Wait()
	if err := <-stopErr; err != nil {
		t.Fatalf("停止规划 Agent 服务失败: %v", err)
	}
	select {
	case err := <-violations:
		t.Fatalf("停止边界之后仍接受了规划调用: %v", err)
	default:
	}
	if calls := fixture.port.callCount(); calls != callsAtStop {
		t.Fatalf("停止返回后又有 %d 次调用进入执行", calls-callsAtStop)
	}
	if active := fixture.service.ActiveCalls(); active != 0 {
		t.Fatalf("停止完成后不应残留在途调用，实际 %d", active)
	}
}

// TestPlanningAgentServiceConcurrentStopIsSafe 校验并发停止不会重复收敛或破坏状态。
func TestPlanningAgentServiceConcurrentStopIsSafe(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{sessions: 2, start: true})
	done := make([]<-chan error, 2)
	for index := 0; index < 2; index++ {
		done[index] = fixture.call(context.Background(), index)
	}
	fixture.port.waitEntered(t, 2)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for index := 0; index < 4; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			errs[slot] = fixture.service.Stop(ctx)
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("并发停止第 %d 次返回错误: %v", index+1, err)
		}
	}
	for index := 0; index < 2; index++ {
		if err := fixture.port.contextAt(t, index).Err(); !errors.Is(err, context.Canceled) {
			t.Fatalf("第 %d 次在途调用应收到取消信号，实际: %v", index+1, err)
		}
		if err := waitCall(t, done[index]); err == nil {
			t.Fatalf("第 %d 次被取消的调用不应返回成功", index+1)
		}
	}
	if active := fixture.service.ActiveCalls(); active != 0 {
		t.Fatalf("并发停止后不应残留在途调用，实际 %d", active)
	}
	if _, err := fixture.service.RunRole(context.Background(), fixture.request(0)); !errors.Is(
		err,
		app.ErrPlanningAgentStopped,
	) {
		t.Fatalf("并发停止后应拒绝新的规划调用，实际错误: %v", err)
	}
}

// TestPlanningAgentServiceStopReportsTimeout 校验停止超时返回真实错误而不是成功。
func TestPlanningAgentServiceStopReportsTimeout(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t, fixtureOptions{start: true, ignoreCancel: true})
	done := fixture.call(context.Background(), 0)
	fixture.port.waitEntered(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := fixture.service.Stop(ctx)
	if err == nil {
		t.Fatal("在途调用未收敛时停止不应报告成功")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("停止应返回带 deadline 的真实错误，实际: %v", err)
	}
	if active := fixture.service.ActiveCalls(); active != 1 {
		t.Fatalf("超时时仍应有 1 次未收敛调用，实际 %d", active)
	}
	fixture.port.releaseAll()
	if callErr := waitCall(t, done); callErr == nil {
		t.Fatal("被中断的规划调用不应返回成功")
	}
	if stopErr := fixture.stop(t); stopErr != nil {
		t.Fatalf("调用收敛后停止应成功: %v", stopErr)
	}
	if active := fixture.service.ActiveCalls(); active != 0 {
		t.Fatalf("调用收敛后不应残留在途调用，实际 %d", active)
	}
}
