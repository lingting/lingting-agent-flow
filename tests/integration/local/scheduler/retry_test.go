// 任务显式重试的集成测试。
//
// 用例使用真实 SQLite、正式迁移、真实 app.TaskRetryService / app.TaskLeaseService，
// 只把统一启动端口替换为受控替身。断言对象是数据库里真实留下的任务状态、租约
// owner/epoch、事件与历史执行记录。
package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// retryRecorder 是显式重试用例的统一启动替身。
type retryRecorder struct {
	mu sync.Mutex
	// launched 是按调用顺序记录的任务标识。
	launched []domain.ID
	// grants 是每次启动收到的执行权凭据。
	grants []app.WorkflowExecutionGrant
	// failure 是注入的启动失败。
	failure error
	// workflowStarted 决定替身是否声明工作流已启动。
	workflowStarted bool
}

// Launch 记录一次启动并返回受控结果。
func (r *retryRecorder) Launch(
	ctx context.Context,
	request app.TaskLaunchRequest,
) (app.TaskLaunchResult, error) {
	taskID := domain.ID(request.TaskID)
	r.mu.Lock()
	r.launched = append(r.launched, taskID)
	r.grants = append(r.grants, request.Grant)
	failure := r.failure
	started := r.workflowStarted
	r.mu.Unlock()
	if failure != nil {
		return app.TaskLaunchResult{
			TaskID:       taskID,
			Stage:        domain.TaskLaunchStageContainer,
			FailureStage: domain.TaskLaunchStageContainer.String(),
			Failure:      failure.Error(),
		}, failure
	}
	return app.TaskLaunchResult{
		TaskID:          taskID,
		Stage:           domain.TaskLaunchStageWorkflow,
		TaskStatus:      domain.TaskStatusPlanned,
		WorkflowStarted: started,
	}, nil
}

// count 返回启动调用次数。
func (r *retryRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.launched)
}

// grant 返回第 index 次启动收到的凭据。
func (r *retryRecorder) grant(index int) app.WorkflowExecutionGrant {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index >= len(r.grants) {
		return app.WorkflowExecutionGrant{}
	}
	return r.grants[index]
}

// newRetryService 构造一个真实的重试服务。
func (f *schedulerFixture) newRetryService(
	t *testing.T,
	launch app.TaskRetryLaunchPort,
) *app.TaskRetryService {
	t.Helper()
	service, err := app.NewTaskRetryService(app.TaskRetryOptions{
		Store:  f.db,
		Leases: f.leases,
		Launch: launch,
		Logger: f.logger.Slog(),
		Now:    func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatalf("构造任务重试服务失败: %v", err)
	}
	return service
}

// TestRetryFailedTaskStartsNewCycle 验证失败任务经显式重试进入新的执行周期。
func TestRetryFailedTaskStartsNewCycle(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)
	before := fixture.task(t, task.ID)
	if before.Status != domain.TaskStatusFailed {
		t.Fatalf("前置状态应为 failed，实际 %s", before.Status)
	}

	recorder := &retryRecorder{workflowStarted: true}
	service := fixture.newRetryService(t, recorder)
	result, err := service.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{})
	if err != nil {
		t.Fatalf("显式重试失败: %v", err)
	}
	if result.PreviousStatus != domain.TaskStatusFailed {
		t.Fatalf("重试前状态应为 failed，实际 %s", result.PreviousStatus)
	}
	if recorder.count() != 1 {
		t.Fatalf("重试必须交接给统一启动流程，实际调用 %d 次", recorder.count())
	}
	// 执行权必须是新的代次，且随启动流程一起传递。
	grant := recorder.grant(0)
	if grant.Lease.TaskID != task.ID {
		t.Fatalf("启动流程收到的执行权不属于该任务: %+v", grant.Lease)
	}
	if grant.Lease.Epoch <= before.LeaseEpoch {
		t.Fatalf("重试必须建立在新代次之上: %d → %d", before.LeaseEpoch, grant.Lease.Epoch)
	}
	// 数据库事实：任务已离开 failed，上一周期的结论与占用被清空。
	stored := fixture.task(t, task.ID)
	if stored.Status == domain.TaskStatusFailed {
		t.Fatal("重试后任务不得仍停留在 failed")
	}
	if stored.FinalResult != domain.TaskResultNone {
		t.Fatalf("重试必须清空上一周期的结论，实际 %s", stored.FinalResult)
	}
	if stored.CurrentStepID != nil || stored.CurrentAttemptID != nil {
		t.Fatal("重试必须释放上一周期的执行占用")
	}
	// 历史执行记录全部保留：重试不覆盖旧 Attempt / WorkflowRun。
	events, err := fixture.db.ListEventsByTask(fixture.ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	retried := 0
	for _, event := range events {
		if event.Type == domain.EventTypeTaskRetried {
			retried++
		}
	}
	if retried != 1 {
		t.Fatalf("重试必须留下且只留下一条审计事件，实际 %d 条", retried)
	}
}

// TestRetryRejectsNonFailedTask 验证非失败状态不能重试。
func TestRetryRejectsNonFailedTask(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	recorder := &retryRecorder{workflowStarted: true}
	service := fixture.newRetryService(t, recorder)
	_, err := service.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{})
	if err == nil {
		t.Fatal("非失败状态不得重试")
	}
	if !errors.Is(err, app.ErrTaskRetryConflict) {
		t.Fatalf("应返回状态冲突，实际 %v", err)
	}
	if recorder.count() != 0 {
		t.Fatal("拒绝重试时不得调用启动流程")
	}
	if stored := fixture.task(t, task.ID); stored.Status != domain.TaskStatusPlanned {
		t.Fatalf("拒绝重试时任务状态不得变化，实际 %s", stored.Status)
	}
}

// TestRetryRejectsLiveLease 验证租约仍有效时不能重试。
//
// 有效租约说明仍有执行者持有执行权：此时接管会在未知现场上重放外部操作。
func TestRetryRejectsLiveLease(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)
	// 一个仍有效的租约（例如另一个执行者刚刚领取）。
	claim, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "live-worker",
		Statuses: []domain.TaskStatus{domain.TaskStatusFailed},
	})
	if err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	recorder := &retryRecorder{workflowStarted: true}
	service := fixture.newRetryService(t, recorder)
	_, err = service.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{})
	if err == nil {
		t.Fatal("租约仍有效时不得重试")
	}
	if !errors.Is(err, app.ErrTaskRetryConflict) {
		t.Fatalf("应返回状态冲突，实际 %v", err)
	}
	if recorder.count() != 0 {
		t.Fatal("拒绝重试时不得调用启动流程")
	}
	// 他人租约不得被清除。
	stored := fixture.task(t, task.ID)
	if stored.LeaseOwner != claim.Handle.Owner || stored.LeaseEpoch != claim.Handle.Epoch {
		t.Fatalf("拒绝重试不得清除他人租约: %+v", stored)
	}
}

// TestRetryRejectsPendingSideEffect 验证未决外部副作用阻止重试。
func TestRetryRejectsPendingSideEffect(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)
	branch, branchErr := domain.TaskBranchName(task.ID.String())
	if branchErr != nil {
		t.Fatalf("构造任务分支名失败: %v", branchErr)
	}
	record := domain.CodeSaveRecord{
		ID:           domain.NewID(),
		TaskID:       task.ID.String(),
		ContainerID:  "container-1",
		RemoteName:   "origin",
		CommitSHA:    "0123456789abcdef0123456789abcdef01234567",
		TaskBranch:   branch,
		CommitStatus: domain.CommitStatusSucceeded,
		PushStatus:   domain.PushStatusPending,
		MergeStatus:  domain.MergeStatusNotRequested,
	}
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	recorder := &retryRecorder{workflowStarted: true}
	service := fixture.newRetryService(t, recorder)
	_, err := service.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{})
	if err == nil {
		t.Fatal("仍有未核验的外部副作用时不得重试")
	}
	if !errors.Is(err, app.ErrTaskRetryConflict) {
		t.Fatalf("应返回状态冲突，实际 %v", err)
	}
	if recorder.count() != 0 {
		t.Fatal("拒绝重试时不得调用启动流程")
	}
	if stored := fixture.task(t, task.ID); stored.Status != domain.TaskStatusFailed {
		t.Fatalf("拒绝重试时任务必须保持 failed，实际 %s", stored.Status)
	}
}

// TestRetryRejectsRecoveryResidue 验证结果不确定的步骤阻止重试。
func TestRetryRejectsRecoveryResidue(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	step := fixture.writeStep(t, task.ID, 1)
	if err := step.Transition(domain.StepStatusRecoveryRequired, fixture.now); err != nil {
		t.Fatalf("步骤迁移失败: %v", err)
	}
	if err := fixture.db.UpdateStepIfStatus(
		fixture.ctx,
		step,
		[]domain.StepStatus{domain.StepStatusPending},
	); err != nil {
		t.Fatalf("写入步骤状态失败: %v", err)
	}
	fixture.moveToFailed(t, task.ID)
	recorder := &retryRecorder{workflowStarted: true}
	service := fixture.newRetryService(t, recorder)
	_, err := service.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{})
	if err == nil {
		t.Fatal("存在结果不确定的步骤时不得重试")
	}
	if !errors.Is(err, app.ErrTaskRetryConflict) {
		t.Fatalf("应返回状态冲突，实际 %v", err)
	}
	if recorder.count() != 0 {
		t.Fatal("拒绝重试时不得调用启动流程")
	}
}

// TestRetryReleasesLeaseOnLaunchFailure 验证启动失败后租约被释放。
//
// 除服务停止以外的失败都释放本次领取，使用户可以再次发起显式重试；任务停在
// preparing 说明它确实进入了新周期。
func TestRetryReleasesLeaseOnLaunchFailure(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)
	failure := errors.New("容器准备失败")
	recorder := &retryRecorder{failure: failure}
	service := fixture.newRetryService(t, recorder)
	_, err := service.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{})
	if err == nil {
		t.Fatal("启动失败必须如实返回错误")
	}
	if !errors.Is(err, app.ErrTaskRetryFailed) {
		t.Fatalf("应返回重试流程未成功，实际 %v", err)
	}
	stored := fixture.task(t, task.ID)
	if stored.LeaseActive(fixture.now) {
		t.Fatalf("非服务停止的失败必须释放租约: %+v", stored)
	}
	if stored.LeaseOwner != "" {
		t.Fatalf("释放后不得留下 owner: %+v", stored)
	}
}

// TestRetryKeepsLeaseOnServiceStop 验证服务停止时不释放无法确认的租约。
func TestRetryKeepsLeaseOnServiceStop(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)
	recorder := &retryRecorder{failure: app.ErrTaskLaunchStopped}
	service := fixture.newRetryService(t, recorder)
	_, err := service.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{})
	if err == nil {
		t.Fatal("服务停止必须如实返回错误")
	}
	stored := fixture.task(t, task.ID)
	if !stored.LeaseActive(fixture.now) {
		t.Fatalf("服务停止时不得释放无法确认的租约: %+v", stored)
	}
}

// TestRetryConcurrentOnlyOneWins 验证并发重试只有一个成功。
func TestRetryConcurrentOnlyOneWins(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)

	recorder := &retryRecorder{workflowStarted: true}
	service := fixture.newRetryService(t, recorder)
	type outcome struct {
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, retryErr := service.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{})
			results <- outcome{err: retryErr}
		}()
	}
	close(start)
	successes := 0
	failures := 0
	for index := 0; index < 2; index++ {
		item := <-results
		if item.err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("并发重试必须只有一个成功，实际成功 %d 次、失败 %d 次", successes, failures)
	}
	if recorder.count() != 1 {
		t.Fatalf("并发重试只允许调用一次启动流程，实际 %d 次", recorder.count())
	}
}

// TestRetryEligibilityIsReadOnly 验证资格查询不写任何状态。
func TestRetryEligibilityIsReadOnly(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)
	before := fixture.task(t, task.ID)
	recorder := &retryRecorder{workflowStarted: true}
	service := fixture.newRetryService(t, recorder)
	decision, err := service.Eligibility(fixture.ctx, task.ID.String())
	if err != nil {
		t.Fatalf("查询重试资格失败: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("干净的失败任务应当允许重试，实际拒绝: %v", decision.Rejections)
	}
	after := fixture.task(t, task.ID)
	if after.Status != before.Status ||
		after.LeaseOwner != before.LeaseOwner ||
		after.LeaseEpoch != before.LeaseEpoch {
		t.Fatalf("资格查询不得改写任务或租约: %+v → %+v", before, after)
	}
	if recorder.count() != 0 {
		t.Fatal("资格查询不得调用启动流程")
	}
}

// TestRetryKeepsHistoricalRecords 验证重试不覆盖历史执行记录。
func TestRetryKeepsHistoricalRecords(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	step := fixture.writeStep(t, task.ID, 1)
	run := fixture.writeActiveRun(
		t,
		fixture.task(t, task.ID),
		[]domain.Step{step},
		"old-worker",
		fixture.now.Add(-time.Minute),
	)
	// 执行记录已经结束（旧周期收尾），任务进入 failed。
	if err := run.Fail("developer_failed", "上一次执行失败", fixture.now); err != nil {
		t.Fatalf("收敛执行记录失败: %v", err)
	}
	if err := fixture.db.UpdateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	fixture.moveToFailed(t, task.ID)

	recorder := &retryRecorder{workflowStarted: true}
	service := fixture.newRetryService(t, recorder)
	if _, err := service.Retry(fixture.ctx, task.ID.String(), app.TaskRetryRequest{}); err != nil {
		t.Fatalf("显式重试失败: %v", err)
	}
	runs, err := fixture.db.ListWorkflowRunsByTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("重试不得删除或改写历史执行记录: %+v", runs)
	}
	if runs[0].Status != domain.WorkflowRunFailed {
		t.Fatalf("历史执行记录的结论必须保留，实际 %s", runs[0].Status)
	}
}

// failingRetryStore 让"重开任务"的写入失败。
//
// 它用于验证"恢复期间出现数据库错误时不伪造成功"：任务状态没有落库时，接口不得
// 报告重试成功，也不得继续执行启动流程。
type failingRetryStore struct {
	app.TaskRetryStore
}

// UpdateTaskLeaseGuard 返回受控失败。
func (s *failingRetryStore) UpdateTaskLeaseGuard(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
	handle domain.TaskLeaseHandle,
) error {
	return errors.New("数据库写入失败")
}

// TestRetryDoesNotFakeSuccessOnDatabaseError 验证写入失败时不伪造成功。
func TestRetryDoesNotFakeSuccessOnDatabaseError(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	fixture.writeStep(t, task.ID, 1)
	fixture.moveToFailed(t, task.ID)

	recorder := &retryRecorder{workflowStarted: true}
	service, err := app.NewTaskRetryService(app.TaskRetryOptions{
		Store:  &failingRetryStore{TaskRetryStore: fixture.db},
		Leases: fixture.leases,
		Launch: recorder,
		Logger: fixture.logger.Slog(),
		Now:    func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatalf("构造任务重试服务失败: %v", err)
	}
	if _, retryErr := service.Retry(
		fixture.ctx,
		task.ID.String(),
		app.TaskRetryRequest{},
	); retryErr == nil {
		t.Fatal("任务状态写入失败时重试必须返回错误")
	}
	if recorder.count() != 0 {
		t.Fatal("任务状态没有落库时不得继续执行启动流程")
	}
	// 数据库事实：任务仍停留在 failed，结论与失败摘要未被清空。
	stored := fixture.task(t, task.ID)
	if stored.Status != domain.TaskStatusFailed {
		t.Fatalf("写入失败时任务必须保持 failed，实际 %s", stored.Status)
	}
	if stored.FinalResult != domain.TaskResultFailed {
		t.Fatalf("写入失败时结论不得被清空，实际 %s", stored.FinalResult)
	}
}
