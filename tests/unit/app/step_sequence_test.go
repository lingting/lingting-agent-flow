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
)

// sequenceFixture 是一套顺序执行测试数据。
//
// 它在标准单步骤执行夹具之上补齐"同任务的多个开发步骤"，因此可以验证按计划顺序
// 推进、失败停止与前置条件拒绝，而不需要另建一套执行服务。
type sequenceFixture struct {
	*stepExecutorFixture
	steps []domain.Step
}

// newSequenceFixture 构造包含指定数量开发步骤的任务。
func newSequenceFixture(t *testing.T, count int) *sequenceFixture {
	t.Helper()
	base := newStepExecutorFixture(t, stepExecutorOptions{})
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	// 默认夹具已经写入序号 1 的步骤，其余步骤按序号追加。
	steps := []domain.Step{base.step}
	for index := 1; index < count; index++ {
		step, err := newFixtureStep(base.task.ID, domain.StepTypeDevelop, base.role, time.Now().UTC())
		if err != nil {
			t.Fatalf("构造步骤失败: %v", err)
		}
		step.Sequence = index + 1
		if err := base.db.CreateStep(ctx, step); err != nil {
			t.Fatalf("写入步骤失败: %v", err)
		}
		steps = append(steps, step)
	}
	return &sequenceFixture{stepExecutorFixture: base, steps: steps}
}

// executeSteps 发起一次顺序执行。
func (f *sequenceFixture) executeSteps(
	t *testing.T,
	ctx context.Context,
) (app.StepSequenceResult, error) {
	t.Helper()
	return f.executor.ExecuteSteps(ctx, app.ExecuteStepsRequest{
		TaskID: f.task.ID.String(),
	})
}

// reloadSteps 读取数据库中的全部步骤。
func (f *sequenceFixture) reloadSteps(t *testing.T, ctx context.Context) []domain.Step {
	t.Helper()
	steps, err := f.db.ListStepsByTask(ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return steps
}

// TestExecuteStepsRunsInPlanOrder 验证按计划顺序依次执行并各自使用独立会话。
func TestExecuteStepsRunsInPlanOrder(t *testing.T) {
	t.Parallel()
	fixture := newSequenceFixture(t, 2)
	ctx, cancel := ctxTimeout(t, 120*time.Second)
	defer cancel()
	result, err := fixture.executeSteps(t, ctx)
	if err != nil {
		t.Fatalf("顺序执行失败: %v", err)
	}
	if result.Stopped {
		t.Fatalf("全部成功时不应提前停止: %s", result.StopReason)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("应当执行两个步骤，实际 %d", len(result.Steps))
	}
	if result.Steps[0].Sequence != 1 || result.Steps[1].Sequence != 2 {
		t.Fatalf("执行顺序不符: %+v", result.Steps)
	}
	if result.Steps[0].StepID != fixture.steps[0].ID ||
		result.Steps[1].StepID != fixture.steps[1].ID {
		t.Fatal("执行的步骤与计划不符")
	}
	// 每一步都必须有自己的 Attempt 与 Session。
	first := result.Steps[0].Result
	second := result.Steps[1].Result
	if first.AttemptID == second.AttemptID {
		t.Fatal("不同步骤必须使用不同的尝试")
	}
	if first.SessionID == second.SessionID {
		t.Fatal("不同步骤必须使用不同的会话")
	}
	sessions := fixture.sessionsOf(t, ctx)
	if len(sessions) != 2 {
		t.Fatalf("两个步骤应各有一条会话，实际 %d", len(sessions))
	}
	steps := fixture.reloadSteps(t, ctx)
	for _, step := range steps {
		if step.Status != domain.StepStatusCompleted {
			t.Fatalf("步骤 %d 状态应为 completed，实际 %s", step.Sequence, step.Status)
		}
	}
	// 单个步骤成功不代表任务成功：任务必须停留在执行中。
	if result.TaskStatus != domain.TaskStatusDeveloping {
		t.Fatalf("任务状态应为 developing，实际 %s", result.TaskStatus)
	}
}

// TestExecuteStepsStopsOnFailure 验证前一步失败后停止，不执行后续步骤。
func TestExecuteStepsStopsOnFailure(t *testing.T) {
	t.Parallel()
	fixture := newSequenceFixture(t, 3)
	fixture.containers.replies = []fakeAgentReply{{
		status:    agent.RunFailed,
		failure:   "模型返回非零退出码",
		exitCode:  2,
		noSession: true,
	}}
	ctx, cancel := ctxTimeout(t, 120*time.Second)
	defer cancel()
	result, err := fixture.executeSteps(t, ctx)
	if err != nil {
		t.Fatalf("顺序执行本身应返回结果: %v", err)
	}
	if !result.Stopped {
		t.Fatal("步骤失败后必须停止")
	}
	if len(result.Steps) != 1 {
		t.Fatalf("失败后不应继续执行，实际执行 %d 个步骤", len(result.Steps))
	}
	if result.StoppedStepID == nil || *result.StoppedStepID != fixture.steps[0].ID {
		t.Fatalf("停止步骤不符: %+v", result.StoppedStepID)
	}
	if result.StopReason == "" {
		t.Fatal("停止必须给出原因")
	}
	if fixture.containers.calls != 1 {
		t.Fatalf("失败后不应再调用 Agent，实际调用 %d 次", fixture.containers.calls)
	}
	steps := fixture.reloadSteps(t, ctx)
	if steps[0].Status != domain.StepStatusFailed {
		t.Fatalf("第一个步骤应为 failed，实际 %s", steps[0].Status)
	}
	for _, step := range steps[1:] {
		if step.Status != domain.StepStatusPending {
			t.Fatalf("后续步骤不应被执行，实际 %s", step.Status)
		}
	}
}

// TestExecuteStepsStopsOnCancellation 验证中断后停止，不自动执行下一步。
func TestExecuteStepsStopsOnCancellation(t *testing.T) {
	t.Parallel()
	fixture := newSequenceFixture(t, 2)
	fixture.containers.block = true
	ctx, cancel := ctxTimeout(t, 120*time.Second)
	defer cancel()
	done := make(chan struct{})
	var (
		result app.StepSequenceResult
		runErr error
	)
	go func() {
		defer close(done)
		result, runErr = fixture.executeSteps(t, ctx)
	}()
	waitForCall(t, fixture.containers)
	cancel()
	<-done
	if runErr != nil {
		t.Fatalf("取消应返回结果而不是错误: %v", runErr)
	}
	if !result.Stopped {
		t.Fatal("中断后必须停止")
	}
	if len(result.Steps) != 1 {
		t.Fatalf("中断后不应继续执行，实际执行 %d 个步骤", len(result.Steps))
	}
	if fixture.containers.calls != 1 {
		t.Fatalf("中断后不应再调用 Agent，实际 %d", fixture.containers.calls)
	}
}

// TestExecuteStepsRejectsUnfinishedPrerequisite 验证跳过未完成前置步骤的请求被拒绝。
func TestExecuteStepsRejectsUnfinishedPrerequisite(t *testing.T) {
	t.Parallel()
	fixture := newSequenceFixture(t, 2)
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	// 直接执行第二个步骤：第一个步骤仍是 pending，必须被拒绝。
	_, err := fixture.executor.Execute(ctx, app.StepExecuteRequest{
		TaskID: fixture.task.ID.String(),
		StepID: fixture.steps[1].ID.String(),
	})
	if err == nil {
		t.Fatal("跳过未完成的前置步骤不应被允许")
	}
	if !errors.Is(err, app.ErrStepExecutionConflict) {
		t.Fatalf("期望状态冲突错误，实际 %v", err)
	}
	if fixture.containers.calls != 0 {
		t.Fatal("被拒绝的请求不应调用 Agent")
	}
	if len(fixture.attemptsOf(t, ctx, fixture.steps[1].ID)) != 0 {
		t.Fatal("被拒绝的请求不应创建尝试")
	}
}

// TestExecuteStepAllowsCompletedPrerequisite 验证前置步骤完成后可以执行后续步骤。
func TestExecuteStepAllowsCompletedPrerequisite(t *testing.T) {
	t.Parallel()
	fixture := newSequenceFixture(t, 2)
	ctx, cancel := ctxTimeout(t, 120*time.Second)
	defer cancel()
	if _, err := fixture.executor.Execute(ctx, app.StepExecuteRequest{
		TaskID: fixture.task.ID.String(),
		StepID: fixture.steps[0].ID.String(),
	}); err != nil {
		t.Fatalf("执行第一个步骤失败: %v", err)
	}
	if _, err := fixture.executor.Execute(ctx, app.StepExecuteRequest{
		TaskID: fixture.task.ID.String(),
		StepID: fixture.steps[1].ID.String(),
	}); err != nil {
		t.Fatalf("前置步骤完成后应允许执行后续步骤: %v", err)
	}
	if fixture.containers.calls != 2 {
		t.Fatalf("两个步骤应各调用一次 Agent，实际 %d", fixture.containers.calls)
	}
}

// TestExecuteStepsRejectsConcurrentSequence 验证同一任务的两个顺序执行不会交错推进。
func TestExecuteStepsRejectsConcurrentSequence(t *testing.T) {
	t.Parallel()
	fixture := newSequenceFixture(t, 2)
	fixture.containers.block = true
	ctx, cancel := ctxTimeout(t, 120*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = fixture.executeSteps(t, ctx)
	}()
	waitForCall(t, fixture.containers)
	_, err := fixture.executeSteps(t, ctx)
	if err == nil {
		t.Fatal("同一任务的并发顺序执行必须被拒绝")
	}
	if !errors.Is(err, app.ErrStepExecutionConflict) {
		t.Fatalf("期望状态冲突错误，实际 %v", err)
	}
	cancel()
	<-done
}

// TestExecuteStepsRecordsGitState 验证执行前后的 Git 状态被写入尝试。
func TestExecuteStepsRecordsGitState(t *testing.T) {
	t.Parallel()
	fixture := newSequenceFixture(t, 1)
	ctx, cancel := ctxTimeout(t, 120*time.Second)
	defer cancel()
	result, err := fixture.executeSteps(t, ctx)
	if err != nil {
		t.Fatalf("顺序执行失败: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("应执行一个步骤，实际 %d", len(result.Steps))
	}
	if fixture.git.count() != 2 {
		t.Fatalf("执行前后各应采集一次 Git 状态，实际 %d", fixture.git.count())
	}
	attempts := fixture.attempts(t, ctx)
	if len(attempts) != 1 {
		t.Fatalf("应只有一条尝试，实际 %d", len(attempts))
	}
	attempt := attempts[0]
	if attempt.GitBefore == nil || !attempt.GitBefore.StatusRead {
		t.Fatalf("执行前状态未写入: %+v", attempt.GitBefore)
	}
	if attempt.GitAfter == nil || !attempt.GitAfter.StatusRead {
		t.Fatalf("执行后状态未写入: %+v", attempt.GitAfter)
	}
	if attempt.GitBefore.Head == attempt.GitAfter.Head {
		t.Fatal("执行前后 HEAD 应能区分")
	}
	if !attempt.GitAfter.Dirty {
		t.Fatal("执行后应记录未提交变更")
	}
	if attempt.GitBranch == "" {
		t.Fatal("尝试必须记录当前分支")
	}
}

// TestExecuteStepGitStateFailureIsRecorded 验证采集失败不会被当作工作区干净。
func TestExecuteStepGitStateFailureIsRecorded(t *testing.T) {
	t.Parallel()
	gitPort := &fakeGitPort{err: errInjected}
	fixture := newStepExecutorFixture(t, stepExecutorOptions{git: gitPort})
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	result, err := fixture.execute(t, ctx)
	if err != nil {
		t.Fatalf("采集失败不应中断执行: %v", err)
	}
	if result.Outcome != domain.StepExecutionCompleted {
		t.Fatalf("采集失败不应改写执行结论: %s", result.Outcome)
	}
	attempts := fixture.attempts(t, ctx)
	if len(attempts) != 1 {
		t.Fatalf("应只有一条尝试，实际 %d", len(attempts))
	}
	attempt := attempts[0]
	if attempt.GitAfter == nil {
		t.Fatal("采集失败的状态必须被记录")
	}
	if attempt.GitAfter.StatusRead {
		t.Fatal("采集失败不能被记录为读取成功")
	}
	if attempt.GitAfter.StatusError == "" {
		t.Fatal("采集失败必须记录原因")
	}
}

// TestExecuteStepWithoutGitCapability 验证没有采集能力时不伪造 Git 状态。
func TestExecuteStepWithoutGitCapability(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{skipGit: true})
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	if _, err := fixture.execute(t, ctx); err != nil {
		t.Fatalf("缺少采集能力不应中断执行: %v", err)
	}
	attempts := fixture.attempts(t, ctx)
	if len(attempts) != 1 {
		t.Fatalf("应只有一条尝试，实际 %d", len(attempts))
	}
	if attempts[0].GitBefore != nil || attempts[0].GitAfter != nil {
		t.Fatalf("没有采集能力时不应有快照: %+v", attempts[0])
	}
}

// TestExecuteStepsRequiresContainer 验证缺少容器时顺序执行给出前置条件错误。
func TestExecuteStepsRequiresContainer(t *testing.T) {
	t.Parallel()
	fixture := newStepExecutorFixture(t, stepExecutorOptions{noContainer: true})
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	result, err := fixture.executor.ExecuteSteps(ctx, app.ExecuteStepsRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err == nil {
		t.Fatal("缺少容器时不应执行")
	}
	if !errors.Is(err, app.ErrStepExecutionPrerequisite) {
		t.Fatalf("期望前置条件错误，实际 %v", err)
	}
	// 结果里可以保留"本次尝试执行但未启动"的记录，但它不能携带任何真实执行标识。
	for _, step := range result.Steps {
		if !step.Skipped || step.Result.AttemptID != "" || step.Result.SessionID != "" {
			t.Fatalf("前置条件缺失时不应产生真实执行: %+v", step)
		}
	}
	if !result.Stopped {
		t.Fatal("前置条件缺失时必须标记为已停止")
	}
	if fixture.containers.calls != 0 {
		t.Fatal("前置条件缺失时不应调用 Agent")
	}
}

// TestExecuteStepsStopsOnUncertainStep 验证存在需要恢复核验的步骤时停止。
func TestExecuteStepsStopsOnUncertainStep(t *testing.T) {
	t.Parallel()
	fixture := newSequenceFixture(t, 2)
	ctx, cancel := ctxTimeout(t, 60*time.Second)
	defer cancel()
	// 把第二个步骤置为需要恢复核验：现场不确定时不允许自动继续推进。
	steps := fixture.reloadSteps(t, ctx)
	steps[1].Status = domain.StepStatusRecoveryRequired
	if err := fixture.db.UpdateStep(ctx, steps[1]); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	result, err := fixture.executor.ExecuteSteps(ctx, app.ExecuteStepsRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("顺序执行本身不应失败: %v", err)
	}
	if !result.Stopped {
		t.Fatal("存在需要恢复核验的步骤时必须停止")
	}
	if result.StopReason == "" {
		t.Fatal("停止必须给出原因")
	}
	if fixture.containers.calls != 0 {
		t.Fatal("现场不确定时不应调用 Agent")
	}
}

// attemptsOf 返回指定步骤的全部尝试。
func (f *stepExecutorFixture) attemptsOf(
	t *testing.T,
	ctx context.Context,
	stepID domain.ID,
) []domain.Attempt {
	t.Helper()
	attempts, err := f.db.ListAttemptsByStep(ctx, stepID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	return attempts
}

// 编译期确认 Git 采集替身满足协调器依赖。
var _ app.StepExecutionGitPort = (*fakeGitPort)(nil)

// 保证 sync 被使用：替身内部用互斥锁保护采集记录。
var _ sync.Locker = (*sync.Mutex)(nil)
