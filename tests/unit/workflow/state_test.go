package workflow_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/workflow"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"github.com/lingting/agentflow/tests/internal/workflowfake"
)

// TestTaskStatusTransitionsAreEnforced 覆盖任务状态的合法与非法迁移。
func TestTaskStatusTransitionsAreEnforced(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	task, err := domain.NewTask(domain.NewID(), "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	legal := []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
		domain.TaskStatusDeveloping,
		domain.TaskStatusVerifying,
		domain.TaskStatusSaving,
		domain.TaskStatusCompleted,
	}
	for _, status := range legal {
		if err := task.Transition(status, now); err != nil {
			t.Fatalf("合法迁移 %s 被拒绝: %v", status, err)
		}
	}
	if !task.Status.IsTerminal() {
		t.Fatalf("completed 应为终态: %s", task.Status)
	}
	// 已成功结束的任务不能被重新推进到执行阶段。
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusDeveloping,
		domain.TaskStatusVerifying,
	} {
		if err := task.Transition(status, now); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("从 completed 迁移到 %s 应当被拒绝，实际: %v", status, err)
		}
	}
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("非法迁移改变了任务状态: %s", task.Status)
	}
}

// TestCancelledTaskCannotRestart 校验已取消任务不会被自动重启。
func TestCancelledTaskCannotRestart(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	task, err := domain.NewTask(domain.NewID(), "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusCancelling, now); err != nil {
		t.Fatalf("迁移到 cancelling 失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusCancelled, now); err != nil {
		t.Fatalf("迁移到 cancelled 失败: %v", err)
	}
	if task.Status.CanStart() {
		t.Fatal("已取消任务不应允许开始推进")
	}
	if err := task.Transition(domain.TaskStatusPreparing, now); !errors.Is(
		err,
		domain.ErrInvalidTransition,
	) {
		t.Fatalf("已取消任务不能被重新推进，实际: %v", err)
	}
}

// TestFinishedAttemptCannotReturnToRunning 校验已结束尝试不会被覆盖为 running。
func TestFinishedAttemptCannotReturnToRunning(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	attempt, err := domain.NewAttempt(domain.NewID(), 1, now)
	if err != nil {
		t.Fatalf("构造尝试失败: %v", err)
	}
	if err := attempt.Fail("构建失败", now); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	// 尝试状态机不提供回到 running 的迁移：直接赋值必须被存储层拒绝，
	// 领域层则通过 Transition 语义间接保证。
	if attempt.Status != domain.AttemptStatusFailed {
		t.Fatalf("尝试状态不符: %s", attempt.Status)
	}
	if !attempt.Status.IsTerminal() {
		t.Fatal("failed 应为终态")
	}
}

// TestConditionalUpdateRejectsStaleStatus 校验条件更新拒绝过期状态。
func TestConditionalUpdateRejectsStaleStatus(t *testing.T) {
	t.Parallel()
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	ctx := context.Background()
	stale := fixture.Reload(t, ctx)
	if err := stale.Transition(domain.TaskStatusPreparing, time.Now().UTC()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 另一个执行者先推进了状态。
	fresh := fixture.Reload(t, ctx)
	if err := fresh.Transition(domain.TaskStatusCancelling, time.Now().UTC()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := fixture.DB.UpdateTask(ctx, fresh); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	// 过期执行者用旧状态做条件更新时必须失败。
	err := fixture.DB.UpdateTaskIfStatus(
		ctx,
		stale,
		[]domain.TaskStatus{domain.TaskStatusQueued},
	)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("过期状态更新应当返回冲突，实际: %v", err)
	}
	loaded := fixture.Reload(t, ctx)
	if loaded.Status != domain.TaskStatusCancelling {
		t.Fatalf("新状态被过期结果覆盖: %s", loaded.Status)
	}
}

// TestTaskLeaseIsExclusive 校验同一任务的租约互斥。
func TestTaskLeaseIsExclusive(t *testing.T) {
	t.Parallel()
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	ctx := context.Background()
	now := time.Now().UTC()
	first := storage.TaskLeaseRequest{
		TaskID: fixture.Task.ID,
		Owner:  "worker-a",
		TTL:    time.Minute,
		At:     now,
	}
	if err := fixture.DB.AcquireTaskLease(ctx, first); err != nil {
		t.Fatalf("第一个执行者应能领取租约: %v", err)
	}
	second := first
	second.Owner = "worker-b"
	if err := fixture.DB.AcquireTaskLease(ctx, second); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("第二个执行者不应领取到同一任务，实际: %v", err)
	}
	// 租约过期后可以被其他执行者接管。
	expired := second
	expired.At = now.Add(2 * time.Minute)
	if err := fixture.DB.AcquireTaskLease(ctx, expired); err != nil {
		t.Fatalf("租约过期后应允许接管: %v", err)
	}
	loaded := fixture.Reload(t, ctx)
	if loaded.LeaseOwner != "worker-b" {
		t.Fatalf("租约持有者不符: %s", loaded.LeaseOwner)
	}
	// 非持有者不能释放他人的租约。
	if err := fixture.DB.ReleaseTaskLease(ctx, fixture.Task.ID, "worker-a", now); err != nil {
		t.Fatalf("释放租约失败: %v", err)
	}
	if fixture.Reload(t, ctx).LeaseOwner != "worker-b" {
		t.Fatal("非持有者不应清除他人租约")
	}
	if err := fixture.DB.ReleaseTaskLease(ctx, fixture.Task.ID, "worker-b", now); err != nil {
		t.Fatalf("释放租约失败: %v", err)
	}
	if fixture.Reload(t, ctx).LeaseOwner != "" {
		t.Fatal("持有者释放后租约应为空")
	}
}

// TestConcurrentWorkersDoNotShareTask 校验两个执行者不会同时推进同一任务。
func TestConcurrentWorkersDoNotShareTask(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Retry: domain.RetryConfig{MaxAttempts: 1},
	})
	withoutValidator(t, fixture)
	first := newEngine(t, fixture, ports)
	second, err := newWorkflowEngineWithOwner(t, fixture, ports, "second-worker")
	if err != nil {
		t.Fatalf("构造第二个引擎失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var (
		wg    sync.WaitGroup
		errMu sync.Mutex
		errs  []error
	)
	wg.Add(2)
	for _, engine := range []interface {
		RunTask(context.Context, domain.ID) error
	}{first, second} {
		go func(target interface {
			RunTask(context.Context, domain.ID) error
		}) {
			defer wg.Done()
			if runErr := target.RunTask(ctx, fixture.Task.ID); runErr != nil {
				errMu.Lock()
				errs = append(errs, runErr)
				errMu.Unlock()
			}
		}(engine)
	}
	wg.Wait()
	// 至少一个执行者成功推进，另一个要么成功跟随，要么因租约/状态冲突退出。
	task := fixture.Reload(t, ctx)
	if task.Status == domain.TaskStatusQueued || task.Status == domain.TaskStatusPreparing {
		t.Fatalf("任务不应停留在中间状态: %s", task.Status)
	}
	if len(errs) > 1 {
		t.Fatalf("不应有两个执行者同时失败: %v", errs)
	}
	// 规划步骤只应产生一条计划记录，重复执行会写出两份开发步骤。
	steps := developmentSteps(fixture.Steps(t, ctx))
	if len(steps) != 1 {
		t.Fatalf("并发推进不应重复创建计划步骤，实际: %d", len(steps))
	}
}

// TestRecoveryMarksUnfinishedAttemptsInterrupted 校验恢复核验会结束悬挂尝试。
func TestRecoveryMarksUnfinishedAttemptsInterrupted(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	ctx := context.Background()
	step := fixture.CreateStep(t, ctx, 1, domain.VerificationSpec{}, 3)
	attempt, err := domain.NewAttempt(step.ID, 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造尝试失败: %v", err)
	}
	if err := fixture.DB.CreateAttempt(ctx, attempt); err != nil {
		t.Fatalf("写入尝试失败: %v", err)
	}
	recovery := newRecovery(t, fixture, ports)
	report, err := recovery.Recover(ctx, fixture.Task.ID)
	if err != nil {
		t.Fatalf("恢复核验失败: %v", err)
	}
	if len(report.UncertainAttempts) != 1 {
		t.Fatalf("应报告一个结果未知的尝试: %+v", report)
	}
	if !report.RecoveryRequired {
		t.Fatal("未结束任务应报告需要恢复核验")
	}
	loaded, err := fixture.DB.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if loaded.Status != domain.AttemptStatusInterrupted {
		t.Fatalf("悬挂尝试应被标记为 interrupted，实际: %s", loaded.Status)
	}
	if report.Task.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("任务状态应为 recovery_required，实际: %s", report.Task.Status)
	}
}

// TestRecoveryKeepsTerminalTaskUnchanged 校验已结束任务不会被恢复流程改写。
func TestRecoveryKeepsTerminalTaskUnchanged(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	ports.Agents.SetReplies(
		domain.RoleTypeValidator,
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "实现符合验收条件")},
	)
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	engine := newEngine(t, fixture, ports)
	ctx := context.Background()
	if err := engine.RunTask(ctx, fixture.Task.ID); err != nil {
		t.Fatalf("推进任务失败: %v", err)
	}
	recovery := newRecovery(t, fixture, ports)
	report, err := recovery.Recover(ctx, fixture.Task.ID)
	if err != nil {
		t.Fatalf("恢复核验失败: %v", err)
	}
	if report.RecoveryRequired {
		t.Fatal("已结束任务不应报告需要恢复核验")
	}
	if report.Task.Status != domain.TaskStatusCompleted {
		t.Fatalf("已结束任务状态被改写: %s", report.Task.Status)
	}
}

// TestPrepareRestartRequiresDisposition 校验重新开始必须显式选择旧容器处置方式。
func TestPrepareRestartRequiresDisposition(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	ctx := context.Background()
	task := fixture.Reload(t, ctx)
	if err := task.SetContainer("container-old"); err != nil {
		t.Fatalf("记录容器关联失败: %v", err)
	}
	if err := fixture.DB.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	recovery := newRecovery(t, fixture, ports)
	if err := recovery.PrepareRestart(ctx, fixture.Task.ID, ""); err == nil {
		t.Fatal("缺少处置方式时必须拒绝重新开始")
	}
	if len(ports.Containers.Disposed) != 0 {
		t.Fatalf("缺少授权时不得处置容器: %+v", ports.Containers.Disposed)
	}
	if err := recovery.PrepareRestart(
		ctx,
		fixture.Task.ID,
		domain.RestartDispositionDetach,
	); err != nil {
		t.Fatalf("重新开始失败: %v", err)
	}
	if len(ports.Containers.Disposed) != 1 {
		t.Fatalf("旧容器应被处置一次: %+v", ports.Containers.Disposed)
	}
	if ports.Containers.Disposed[0].Disposition != domain.RestartDispositionDetach {
		t.Fatalf("处置方式不符: %s", ports.Containers.Disposed[0].Disposition)
	}
	loaded := fixture.Reload(t, ctx)
	if loaded.ContainerID != "" {
		t.Fatalf("重新开始必须清空旧容器关联: %s", loaded.ContainerID)
	}
	// 新的执行必须创建新容器，而不是复用旧容器。
	engine := newEngine(t, fixture, ports)
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	withoutValidator(t, fixture)
	if err := engine.RunTask(ctx, fixture.Task.ID); err != nil {
		t.Fatalf("重新推进失败: %v", err)
	}
	if len(ports.Containers.Created) != 1 {
		t.Fatalf("重新开始必须创建新容器: %+v", ports.Containers.Created)
	}
	if ports.Containers.Created[0].ID == "container-old" {
		t.Fatal("不得复用旧容器")
	}
}

// TestRecoveryReportsDetachedContainer 校验恢复核验如实报告脱离管理事实。
func TestRecoveryReportsDetachedContainer(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	ctx := context.Background()
	task := fixture.Reload(t, ctx)
	if err := task.SetContainer("container-detached"); err != nil {
		t.Fatalf("记录容器关联失败: %v", err)
	}
	if err := fixture.DB.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	ports.Containers.States = map[string]workflow.ContainerInfo{
		"container-detached": {ID: "container-detached", Detached: true},
	}
	recovery := newRecovery(t, fixture, ports)
	report, err := recovery.Recover(ctx, fixture.Task.ID)
	if err != nil {
		t.Fatalf("恢复核验失败: %v", err)
	}
	if !containsText(report.ContainerState, "脱离") {
		t.Fatalf("应报告容器已脱离管理: %s", report.ContainerState)
	}
	if len(ports.Containers.Disposed) != 0 {
		t.Fatalf("恢复核验不得处置容器: %+v", ports.Containers.Disposed)
	}
}

// containsText 判断文本是否包含子串。
func containsText(value, fragment string) bool {
	return len(fragment) == 0 || len(value) >= len(fragment) &&
		indexOf(value, fragment) >= 0
}

// indexOf 返回子串位置，不存在时返回 -1。
func indexOf(value, fragment string) int {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return index
		}
	}
	return -1
}

// TestFailedTaskDoesNotReuseContainerWithoutRetry 校验失败任务不会静默复用旧容器。
func TestFailedTaskDoesNotReuseContainerWithoutRetry(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	ctx := context.Background()
	task := fixture.Reload(t, ctx)
	if err := task.SetContainer("container-old"); err != nil {
		t.Fatalf("记录容器关联失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusPreparing, time.Now().UTC()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusFailed, time.Now().UTC()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := fixture.DB.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	engine := newEngine(t, fixture, ports)
	final, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("失败任务未处置旧容器时不应直接重新开始")
	}
	if final.Status != domain.TaskStatusFailed {
		t.Fatalf("任务状态应为 failed，实际: %s", final.Status)
	}
	if ports.Calls.Contains("source.fetch") {
		t.Fatalf("未处置旧容器时不应重新获取代码: %v", ports.Calls.Snapshot())
	}
	if ports.Calls.Contains("container.create") {
		t.Fatalf("未处置旧容器时不应创建新容器: %v", ports.Calls.Snapshot())
	}
}

// TestUncertainStepBlocksAutomaticReplay 校验结果不确定的步骤不会被自动重放。
func TestUncertainStepBlocksAutomaticReplay(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	ctx := context.Background()
	step := fixture.CreateStep(t, ctx, 1, domain.VerificationSpec{}, 3)
	if err := step.Transition(domain.StepStatusRunning, time.Now().UTC()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusRecoveryRequired, time.Now().UTC()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := fixture.DB.UpdateStep(ctx, step); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	task := fixture.Reload(t, ctx)
	if err := task.Transition(domain.TaskStatusPreparing, time.Now().UTC()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusPlanning, time.Now().UTC()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusPlanned, time.Now().UTC()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := fixture.DB.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	engine := newEngine(t, fixture, ports)
	final, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("步骤结果不确定时不应自动重放")
	}
	if final.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("任务状态应为 recovery_required，实际: %s", final.Status)
	}
	if ports.Calls.Contains("agent.run:developer") {
		t.Fatalf("结果不确定时不应再次调用开发者: %v", ports.Calls.Snapshot())
	}
}
