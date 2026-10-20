package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// ctxTimeout 返回带超时的测试上下文。
func ctxTimeout(t *testing.T, duration time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), duration)
}

// TestExecuteStepSuccess 验证成功执行只创建一条尝试与一条会话。
func TestExecuteStepSuccess(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	result, err := fixture.execute(t, ctx)
	if err != nil {
		t.Fatalf("执行步骤失败: %v", err)
	}
	if result.Outcome != domain.StepExecutionCompleted {
		t.Fatalf("期望执行结论 %s，实际 %s（%s）", domain.StepExecutionCompleted, result.Outcome, result.Failure)
	}
	if !result.Finalized {
		t.Fatalf("最终状态应全部写入成功: %v", result.FinalizationErrors)
	}
	if result.AttemptStatus != domain.AttemptStatusCompleted {
		t.Fatalf("期望尝试状态 completed，实际 %s", result.AttemptStatus)
	}
	if result.StepStatus != domain.StepStatusCompleted {
		t.Fatalf("期望步骤状态 completed，实际 %s", result.StepStatus)
	}
	// 单个步骤成功不代表任务验收成功：任务必须停留在执行中，等待验证与代码保存。
	if result.TaskStatus != domain.TaskStatusDeveloping {
		t.Fatalf("期望任务状态 developing，实际 %s", result.TaskStatus)
	}
	task := fixture.reloadTask(t, ctx)
	if task.FinalResult != domain.TaskResultNone {
		t.Fatalf("任务不应被标记为最终结论: %s", task.FinalResult)
	}
	step := fixture.reloadStep(t, ctx)
	if step.CurrentAttemptID != nil {
		t.Fatal("完成的步骤不应保留执行占用")
	}
	attempts := fixture.attempts(t, ctx)
	if len(attempts) != 1 {
		t.Fatalf("一次执行应只创建一条尝试，实际 %d", len(attempts))
	}
	if attempts[0].Status != domain.AttemptStatusCompleted {
		t.Fatalf("尝试状态不符: %s", attempts[0].Status)
	}
	if len(attempts[0].SessionIDs) != 1 {
		t.Fatalf("尝试应关联一条会话，实际 %d", len(attempts[0].SessionIDs))
	}
	if result.SessionID.IsEmpty() {
		t.Fatal("结果应返回会话标识")
	}
	sessions := fixture.sessionsOf(t, ctx)
	if len(sessions) != 1 {
		t.Fatalf("一次执行应只创建一条会话，实际 %d", len(sessions))
	}
	if sessions[0].TaskID != fixture.task.ID ||
		sessions[0].StepID == nil || *sessions[0].StepID != fixture.step.ID ||
		sessions[0].AttemptID == nil || *sessions[0].AttemptID != attempts[0].ID {
		t.Fatalf("会话归属不正确: %+v", sessions[0])
	}
}

// TestExecuteStepFailureKeepsHistory 验证失败结果被保留且不删除历史。
func TestExecuteStepFailureKeepsHistory(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	fixture.containers.replies = []fakeAgentReply{{
		status:    agent.RunFailed,
		failure:   "模型返回非零退出码",
		exitCode:  3,
		noSession: true,
	}}
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	result, err := fixture.execute(t, ctx)
	if err != nil {
		t.Fatalf("执行步骤应返回结果而不是错误: %v", err)
	}
	if result.Outcome != domain.StepExecutionFailed {
		t.Fatalf("期望执行结论 failed，实际 %s", result.Outcome)
	}
	if result.Failure == "" {
		t.Fatal("失败结果必须给出可诊断原因")
	}
	if result.StepStatus != domain.StepStatusFailed {
		t.Fatalf("期望步骤状态 failed，实际 %s", result.StepStatus)
	}
	// 任务不能被单个步骤的失败直接判为终态失败：现场仍保留，需要恢复核验。
	if result.TaskStatus != domain.TaskStatusRecoveryRequired {
		t.Fatalf("期望任务状态 recovery_required，实际 %s", result.TaskStatus)
	}
	attempts := fixture.attempts(t, ctx)
	if len(attempts) != 1 {
		t.Fatalf("失败执行同样必须保留尝试记录，实际 %d", len(attempts))
	}
	if attempts[0].Status != domain.AttemptStatusFailed {
		t.Fatalf("尝试状态不符: %s", attempts[0].Status)
	}
	if attempts[0].FailureSummary == "" {
		t.Fatal("失败的尝试必须保存失败摘要")
	}
	if attempts[0].FinishedAt == nil {
		t.Fatal("失败的尝试必须记录结束时间")
	}
}

// TestExecuteStepTimeout 验证超时具有明确的终态而不是成功。
func TestExecuteStepTimeout(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	fixture.containers.replies = []fakeAgentReply{{
		status:    agent.RunTimedOut,
		failure:   "调用超时被终止",
		exitCode:  -1,
		noSession: true,
	}}
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	result, err := fixture.execute(t, ctx)
	if err != nil {
		t.Fatalf("执行步骤失败: %v", err)
	}
	if result.Outcome != domain.StepExecutionTimedOut {
		t.Fatalf("期望执行结论 timed_out，实际 %s", result.Outcome)
	}
	if result.AttemptStatus != domain.AttemptStatusTimedOut {
		t.Fatalf("期望尝试状态 timed_out，实际 %s", result.AttemptStatus)
	}
	if result.StepStatus != domain.StepStatusFailed {
		t.Fatalf("超时的步骤必须收敛为 failed，实际 %s", result.StepStatus)
	}
	attempts := fixture.attempts(t, ctx)
	if len(attempts) != 1 || !attempts[0].TimedOut {
		t.Fatalf("尝试应记录超时标记: %+v", attempts)
	}
}

// TestExecuteStepCancelled 验证取消传播后的终态与任务收敛。
func TestExecuteStepCancelled(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	fixture.containers.block = true
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	done := make(chan struct{})
	var (
		result app.StepExecutionResult
		runErr error
	)
	go func() {
		defer close(done)
		result, runErr = fixture.execute(t, ctx)
	}()
	waitForCall(t, fixture.containers)
	cancel()
	<-done
	if runErr != nil {
		t.Fatalf("取消应返回结果而不是错误: %v", runErr)
	}
	if result.Outcome != domain.StepExecutionCancelled {
		t.Fatalf("期望执行结论 cancelled，实际 %s", result.Outcome)
	}
	if result.AttemptStatus != domain.AttemptStatusCancelled {
		t.Fatalf("期望尝试状态 cancelled，实际 %s", result.AttemptStatus)
	}
	if result.StepStatus != domain.StepStatusCancelled {
		t.Fatalf("期望步骤状态 cancelled，实际 %s", result.StepStatus)
	}
	if result.TaskStatus != domain.TaskStatusCancelled {
		t.Fatalf("期望任务状态 cancelled，实际 %s", result.TaskStatus)
	}
	readCtx, readCancel := ctxTimeout(t, 30*time.Second)
	defer readCancel()
	task := fixture.reloadTask(t, readCtx)
	if task.FinalResult != domain.TaskResultCancelled {
		t.Fatalf("任务结论不符: %s", task.FinalResult)
	}
	if !task.CancelRequested || task.CancelRequestedAt == nil {
		t.Fatal("取消必须持久化取消请求与时间")
	}
}

// waitForCall 等待受控执行器收到一次 Agent 调用。
func waitForCall(t *testing.T, port *fakeContainerPort) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		port.mu.Lock()
		calls := port.calls
		port.mu.Unlock()
		if calls > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待执行器被调用超时")
}

// TestExecuteStepRejectsQueuedTask 验证 queued 任务不能直接执行步骤。
func TestExecuteStepRejectsQueuedTask(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{stayQueued: true})
	ctx, cancel := ctxTimeout(t, 30*time.Second)
	defer cancel()
	_, err := fixture.execute(t, ctx)
	if err == nil {
		t.Fatal("queued 任务不应允许执行步骤")
	}
	if !errors.Is(err, app.ErrStepExecutionConflict) {
		t.Fatalf("期望状态冲突错误，实际 %v", err)
	}
	if len(fixture.attempts(t, ctx)) != 0 {
		t.Fatal("被拒绝的请求不应创建任何尝试")
	}
	if fixture.reloadStep(t, ctx).Status != domain.StepStatusPending {
		t.Fatal("被拒绝的请求不应改变步骤状态")
	}
	if fixture.containers.calls != 0 {
		t.Fatal("被拒绝的请求不应调用 Agent")
	}
}

// TestExecuteStepPlannedTask 验证 planned 任务可以执行并推进到 developing。
func TestExecuteStepPlannedTask(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	result, err := fixture.execute(t, ctx)
	if err != nil {
		t.Fatalf("执行步骤失败: %v", err)
	}
	if result.Outcome != domain.StepExecutionCompleted {
		t.Fatalf("期望成功执行，实际 %s", result.Outcome)
	}
	if fixture.reloadTask(t, ctx).Status != domain.TaskStatusDeveloping {
		t.Fatalf("任务应推进到 developing，实际 %s", fixture.reloadTask(t, ctx).Status)
	}
}

// TestExecuteStepRequiresContainer 验证缺少容器时给出明确的前置条件错误。
func TestExecuteStepRequiresContainer(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{
		noContainer: true,
	})
	ctx, cancel := ctxTimeout(t, 30*time.Second)
	defer cancel()
	_, err := fixture.execute(t, ctx)
	if err == nil {
		t.Fatal("缺少任务容器时不应执行")
	}
	if !errors.Is(err, app.ErrStepExecutionPrerequisite) {
		t.Fatalf("期望前置条件错误，实际 %v", err)
	}
	if len(fixture.attempts(t, ctx)) != 0 {
		t.Fatal("前置条件缺失时不应创建尝试")
	}
}

// TestExecuteStepRequiresValidatedHost 验证未通过预检的节点不能执行步骤。
func TestExecuteStepRequiresValidatedHost(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{skipHostValidation: true})
	ctx, cancel := ctxTimeout(t, 30*time.Second)
	defer cancel()
	_, err := fixture.execute(t, ctx)
	if err == nil {
		t.Fatal("未通过环境预检的节点不应执行步骤")
	}
	if !errors.Is(err, app.ErrStepExecutionPrerequisite) {
		t.Fatalf("期望前置条件错误，实际 %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("节点不可用时不应调用 Agent")
	}
}

// TestExecutorRejectsRunBeforeStart 验证未启动的协调器拒绝执行。
func TestExecutorRejectsRunBeforeStart(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixtureWithoutStart(t)
	ctx, cancel := ctxTimeout(t, 30*time.Second)
	defer cancel()
	_, err := fixture.execute(t, ctx)
	if err == nil {
		t.Fatal("未启动的协调器不应接受执行请求")
	}
	if fixture.containers.calls != 0 {
		t.Fatal("被拒绝的请求不应调用 Agent")
	}
	if len(fixture.attempts(t, ctx)) != 0 {
		t.Fatal("被拒绝的请求不应创建尝试")
	}
}

// TestExecutorRejectsRunAfterStop 验证停止后拒绝新的执行。
func TestExecutorRejectsRunAfterStop(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixtureWithoutStart(t)
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	if err := fixture.executor.Start(ctx); err != nil {
		t.Fatalf("启动协调器失败: %v", err)
	}
	stopCtx, stopCancel := ctxTimeout(t, 30*time.Second)
	defer stopCancel()
	if err := fixture.executor.Stop(stopCtx); err != nil {
		t.Fatalf("停止协调器失败: %v", err)
	}
	_, err := fixture.execute(t, ctx)
	if err == nil {
		t.Fatal("停止后不应接受执行请求")
	}
	if !errors.Is(err, app.ErrStepExecutorStopped) {
		t.Fatalf("期望服务已停止错误，实际 %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("停止后的请求不应调用 Agent")
	}
}

// TestExecutorStopCancelsInFlightRun 验证服务停止会取消在途执行并收敛为中断。
func TestExecutorStopCancelsInFlightRun(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	fixture.containers.block = true
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	done := make(chan struct{})
	var (
		result app.StepExecutionResult
		runErr error
	)
	go func() {
		defer close(done)
		result, runErr = fixture.execute(t, ctx)
	}()
	waitForCall(t, fixture.containers)
	stopCtx, stopCancel := ctxTimeout(t, 30*time.Second)
	defer stopCancel()
	if err := fixture.executor.Stop(stopCtx); err != nil {
		t.Fatalf("停止协调器失败: %v", err)
	}
	<-done
	if runErr != nil {
		t.Fatalf("停止应返回结果而不是错误: %v", runErr)
	}
	// 服务停止不是用户取消：必须记录为中断，而不是"任务被取消"。
	if result.Outcome != domain.StepExecutionInterrupted {
		t.Fatalf("期望执行结论 interrupted，实际 %s", result.Outcome)
	}
	if result.AttemptStatus != domain.AttemptStatusInterrupted {
		t.Fatalf("期望尝试状态 interrupted，实际 %s", result.AttemptStatus)
	}
	if result.TaskStatus != domain.TaskStatusRecoveryRequired {
		t.Fatalf("期望任务状态 recovery_required，实际 %s", result.TaskStatus)
	}
	if !result.Finalized {
		t.Fatalf("中断路径同样必须写入终态: %v", result.FinalizationErrors)
	}
	readCtx, readCancel := ctxTimeout(t, 30*time.Second)
	defer readCancel()
	if fixture.executor.ActiveExecutions() != 0 {
		t.Fatalf("停止后不应有在途执行，实际 %d", fixture.executor.ActiveExecutions())
	}
	if len(fixture.attempts(t, readCtx)) != 1 {
		t.Fatal("中断的执行记录必须保留")
	}
}

// TestExecutorName 验证后台服务名称固定，便于在生命周期日志中定位。
func TestExecutorName(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	if fixture.executor.Name() != "step_executor" {
		t.Fatalf("后台服务名称不符: %s", fixture.executor.Name())
	}
}

// TestExecuteStepRejectsNonAgentStep 验证非 Agent 步骤不能通过执行接口启动。
func TestExecuteStepRejectsNonAgentStep(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{
		stepType: domain.StepTypeVerify,
	})
	ctx, cancel := ctxTimeout(t, 30*time.Second)
	defer cancel()
	_, err := fixture.execute(t, ctx)
	if err == nil {
		t.Fatal("非 Agent 步骤不应通过执行接口启动")
	}
	if !errors.Is(err, app.ErrStepExecutionConflict) {
		t.Fatalf("期望状态冲突错误，实际 %v", err)
	}
}

// TestExecuteStepAttemptCreationFailure 验证尝试创建失败时不产生虚假运行状态。
func TestExecuteStepAttemptCreationFailure(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	fixture.store.beginExecution = func(
		ctx context.Context,
		request storage.StepExecutionStartRequest,
	) (storage.StepExecutionStart, error) {
		return storage.StepExecutionStart{}, errors.Join(storage.ErrInvalidData, errInjected)
	}
	ctx, cancel := ctxTimeout(t, 30*time.Second)
	defer cancel()
	_, err := fixture.execute(t, ctx)
	if err == nil {
		t.Fatal("尝试创建失败时不应返回成功")
	}
	if errors.Is(err, app.ErrStepExecutionNotFound) {
		t.Fatalf("不应误报为记录不存在: %v", err)
	}
	if len(fixture.attempts(t, ctx)) != 0 {
		t.Fatal("失败路径不应留下尝试记录")
	}
	step := fixture.reloadStep(t, ctx)
	if step.Status != domain.StepStatusPending {
		t.Fatalf("失败的占用不应改变步骤状态: %s", step.Status)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("占用失败后不应调用 Agent")
	}
}

// TestExecuteStepFinalizationFailureIsReported 验证状态写入失败不被当作成功。
func TestExecuteStepFinalizationFailureIsReported(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{
		updateAttemptErr: errInjected,
	})
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	result, err := fixture.execute(t, ctx)
	if err != nil {
		t.Fatalf("执行本身应返回结果: %v", err)
	}
	if result.Finalized {
		t.Fatal("尝试终态写入失败时必须报告未完成持久化")
	}
	if len(result.FinalizationErrors) == 0 {
		t.Fatal("必须给出写入失败的原因")
	}
	if result.Outcome != domain.StepExecutionCompleted {
		t.Fatalf("执行事实不应被改写: %s", result.Outcome)
	}
	// 尝试写入失败后仍应尝试写入步骤与任务，避免普通错误阻止状态清理。
	step := fixture.reloadStep(t, ctx)
	if step.Status != domain.StepStatusCompleted {
		t.Fatalf("步骤终态应被写入，实际 %s", step.Status)
	}
}

// TestExecuteStepAppendsEvents 验证执行开始与结束都写入任务时间线。
func TestExecuteStepAppendsEvents(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	if _, err := fixture.execute(t, ctx); err != nil {
		t.Fatalf("执行步骤失败: %v", err)
	}
	events, err := fixture.db.ListEventsByTask(ctx, fixture.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	types := make(map[domain.EventType]int)
	for _, event := range events {
		types[event.Type]++
	}
	if types[domain.EventTypeAttemptStarted] == 0 {
		t.Fatal("缺少尝试开始事件")
	}
	if types[domain.EventTypeStepCompleted] == 0 {
		t.Fatal("缺少步骤完成事件")
	}
}

// TestExecuteStepConcurrentRequests 验证并发请求不会同时执行同一步骤。
func TestExecuteStepConcurrentRequests(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	fixture.containers.block = true
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()

	const workers = 4
	var (
		wait  sync.WaitGroup
		mu    sync.Mutex
		errs  []error
		start = make(chan struct{})
	)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := fixture.execute(t, ctx)
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}()
	}
	close(start)
	waitForCall(t, fixture.containers)
	// 至少要有一次请求被拒绝：其余请求无法在步骤被占用时开始。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		failures := countConflicts(errs)
		mu.Unlock()
		if failures > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	wait.Wait()
	if countConflicts(errs) == 0 {
		t.Fatalf("并发请求必须至少有一个被拒绝为状态冲突: %v", errs)
	}
	readCtx, readCancel := ctxTimeout(t, 30*time.Second)
	defer readCancel()
	attempts := fixture.attempts(t, readCtx)
	if len(attempts) != 1 {
		t.Fatalf("同一时刻只允许一条执行尝试，实际 %d", len(attempts))
	}
	if fixture.containers.calls != 1 {
		t.Fatalf("同一步骤只应调用 Agent 一次，实际 %d", fixture.containers.calls)
	}
}

// countConflicts 统计状态冲突错误数量。
func countConflicts(errs []error) int {
	count := 0
	for _, err := range errs {
		if errors.Is(err, app.ErrStepExecutionConflict) {
			count++
		}
	}
	return count
}

// TestExecuteStepRejectsOccupiedStep 验证被占用的步骤再次请求返回冲突。
func TestExecuteStepRejectsOccupiedStep(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{
		stepStatus: []domain.StepStatus{domain.StepStatusRunning},
	})
	ctx, cancel := ctxTimeout(t, 30*time.Second)
	defer cancel()
	_, err := fixture.execute(t, ctx)
	if err == nil {
		t.Fatal("正在运行的步骤不应再次执行")
	}
	if !errors.Is(err, app.ErrStepExecutionConflict) {
		t.Fatalf("期望状态冲突错误，实际 %v", err)
	}
	if len(fixture.attempts(t, ctx)) != 0 {
		t.Fatal("冲突请求不应创建尝试")
	}
}

// TestExecuteStepLateResultDoesNotOverrideTerminal 验证迟到结果不覆盖已确认终态。
func TestExecuteStepLateResultDoesNotOverrideTerminal(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{})
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	started, err := fixture.store.BeginStepExecution(ctx, storage.StepExecutionStartRequest{
		TaskID:             fixture.task.ID,
		StepID:             fixture.step.ID,
		ExpectedTaskStatus: domain.TaskStatusPlanned,
		ExpectedStepStatus: domain.StepStatusPending,
		ContainerID:        "container-1",
		DockerID:           "docker-container-1",
		At:                 time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("占用步骤失败: %v", err)
	}
	// 另一条路径先把尝试收敛为失败终态。
	finished := started.Attempt
	if err := finished.Fail("已被其他写入确认失败", time.Now().UTC()); err != nil {
		t.Fatalf("标记尝试失败失败: %v", err)
	}
	if err := fixture.db.UpdateAttemptIfStatus(
		ctx,
		finished,
		[]domain.AttemptStatus{domain.AttemptStatusRunning},
	); err != nil {
		t.Fatalf("写入尝试终态失败: %v", err)
	}
	current, err := fixture.db.GetAttempt(ctx, started.Attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if current.Status != domain.AttemptStatusFailed {
		t.Fatalf("尝试终态不符: %s", current.Status)
	}
	// 模拟迟到结果：再次以 running 为前置条件写入必须失败，而不是覆盖终态。
	late := started.Attempt
	if err := late.Complete(time.Now().UTC()); err != nil {
		t.Fatalf("标记尝试完成失败: %v", err)
	}
	err = fixture.db.UpdateAttemptIfStatus(
		ctx,
		late,
		[]domain.AttemptStatus{domain.AttemptStatusRunning},
	)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("迟到结果必须被条件更新拒绝，实际 %v", err)
	}
}
