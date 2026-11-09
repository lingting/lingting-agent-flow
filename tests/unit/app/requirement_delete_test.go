package app_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 需求聚合删除的应用层用例。
//
// 覆盖范围：
//   - 需求与全部关联任务在同一事务内软删除，历史记录与业务字段不被改写；
//   - 执行中的任务、`cancelling`、`paused`、`recovery_required` 与未知状态拒绝删除；
//   - 活动 WorkflowRun、未收敛步骤、结果不明的代码保存副作用同样拒绝；
//   - 共享 TaskExecutionCoordinator 阻止删除与启动并发，失败与成功后都释放执行权。

// deleteFixture 是需求删除用例的测试环境。
type deleteFixture struct {
	base         *confirmFixture
	coordination *app.TaskExecutionCoordinator
	service      *app.RequirementService
	// rawConn 是绕过仓储层的独立连接，只用于构造"数据库里存在未知状态"这类现场。
	rawConn *sql.DB
}

// newDeleteFixture 在确认用例的现场之上装配带任务执行协调器的需求服务。
func newDeleteFixture(t *testing.T) *deleteFixture {
	t.Helper()
	base := newConfirmFixture(t)
	coordination := app.NewTaskExecutionCoordinator()
	service, err := app.NewRequirementService(app.RequirementServiceOptions{
		Coordination: coordination,
		Store:        base.db,
	})
	if err != nil {
		t.Fatalf("构造需求服务失败: %v", err)
	}
	return &deleteFixture{base: base, coordination: coordination, service: service}
}

// confirm 创建 1 个需求与 count 个任务并返回确认结果。
func (f *deleteFixture) confirm(t *testing.T, count int) app.ConfirmRequirementResult {
	t.Helper()
	session, revision := f.base.previewSession(t, confirmAnalysisJSON("删除需求"), confirmPlanJSON(count))
	result, err := f.base.service.Confirm(f.base.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        revision.ID.String(),
	})
	if err != nil {
		t.Fatalf("确认方案失败: %v", err)
	}
	return result
}

// task 重新读取单个任务（包含已逻辑删除的记录）。
func (f *deleteFixture) task(t *testing.T, taskID domain.ID) domain.Task {
	t.Helper()
	persisted, err := f.base.db.GetTask(f.base.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务 %s 失败: %v", taskID, err)
	}
	return *persisted
}

// requirement 重新读取单个需求（包含已逻辑删除的记录）。
func (f *deleteFixture) requirement(t *testing.T, requirementID domain.ID) domain.Requirement {
	t.Helper()
	persisted, err := f.base.db.GetRequirement(f.base.ctx, requirementID)
	if err != nil {
		t.Fatalf("读取需求 %s 失败: %v", requirementID, err)
	}
	return *persisted
}

// moveTask 把任务推进到指定状态并落库。
func (f *deleteFixture) moveTask(t *testing.T, taskID domain.ID, status domain.TaskStatus) domain.Task {
	t.Helper()
	task := f.task(t, taskID)
	path := confirmedStatusPath(status)
	for _, next := range path {
		if err := task.Transition(next, time.Now().UTC()); err != nil {
			t.Fatalf("任务 %s 迁移到 %s 失败: %v", taskID, next, err)
		}
	}
	if err := f.base.db.UpdateTask(f.base.ctx, task); err != nil {
		t.Fatalf("更新任务 %s 失败: %v", taskID, err)
	}
	if status == domain.TaskStatusRecoveryRequired {
		return f.forceTaskStatus(t, taskID, status)
	}
	return task
}

// forceTaskStatus 直接写入状态列，用于构造"数据库里出现未知状态"与"现场不确定"的现场。
//
// 仓储层会拒绝未知状态，因此这里必须绕过它：测试要验证的正是读取路径遇到未知状态时的
// 失败关闭行为，而不是写入路径的校验。
func (f *deleteFixture) forceTaskStatus(
	t *testing.T,
	taskID domain.ID,
	status domain.TaskStatus,
) domain.Task {
	t.Helper()
	if f.rawConn == nil {
		f.rawConn = testkit.OpenSQLite(t, f.base.db.Path())
	}
	if _, err := f.rawConn.ExecContext(
		f.base.ctx,
		`UPDATE tasks SET status = ? WHERE id = ?`,
		string(status),
		taskID.String(),
	); err != nil {
		t.Fatalf("写入任务 %s 状态失败: %v", taskID, err)
	}
	return f.task(t, taskID)
}

// confirmedStatusPath 返回从 queued 到达目标状态的合法迁移序列。
func confirmedStatusPath(status domain.TaskStatus) []domain.TaskStatus {
	switch status {
	case domain.TaskStatusPreparing:
		return []domain.TaskStatus{domain.TaskStatusPreparing}
	case domain.TaskStatusPlanning:
		return []domain.TaskStatus{domain.TaskStatusPlanning}
	case domain.TaskStatusPlanned:
		return []domain.TaskStatus{domain.TaskStatusPlanning, domain.TaskStatusPlanned}
	case domain.TaskStatusDeveloping:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusDeveloping,
		}
	case domain.TaskStatusVerifying:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusDeveloping,
			domain.TaskStatusVerifying,
		}
	case domain.TaskStatusSaving:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusDeveloping,
			domain.TaskStatusVerifying,
			domain.TaskStatusSaving,
		}
	case domain.TaskStatusRetrying:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusDeveloping,
			domain.TaskStatusRetrying,
		}
	case domain.TaskStatusCancelling:
		return []domain.TaskStatus{domain.TaskStatusCancelling}
	case domain.TaskStatusCompleted:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusDeveloping,
			domain.TaskStatusVerifying,
			domain.TaskStatusCompleted,
		}
	case domain.TaskStatusFailed:
		return []domain.TaskStatus{domain.TaskStatusFailed}
	case domain.TaskStatusCancelled:
		return []domain.TaskStatus{domain.TaskStatusCancelling, domain.TaskStatusCancelled}
	case domain.TaskStatusPaused:
		return []domain.TaskStatus{domain.TaskStatusPlanning, domain.TaskStatusPaused}
	case domain.TaskStatusRecoveryRequired:
		return []domain.TaskStatus{domain.TaskStatusFailed}
	default:
		panic(fmt.Sprintf("未定义的状态迁移路径: %s", status))
	}
}

// TestDeleteRequiresCoordination 校验缺少任务执行协调器时删除失败关闭。
func TestDeleteRequiresCoordination(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 2)
	service, err := app.NewRequirementService(app.RequirementServiceOptions{Store: f.base.db})
	if err != nil {
		t.Fatalf("构造无协调器需求服务失败: %v", err)
	}
	_, err = service.Delete(f.base.ctx, result.Requirement.ID.String())
	if !errors.Is(err, app.ErrRequirementDeleteUnavailable) {
		t.Fatalf("缺少协调器应返回删除能力不可用，实际: %v", err)
	}
	if f.requirement(t, result.Requirement.ID).DeletedAt != nil {
		t.Fatal("拒绝删除时不得写入删除时间")
	}
}

// TestDeleteSoftDeletesRequirementAndTasks 校验需求与任务一起软删除且业务字段不变。
func TestDeleteSoftDeletesRequirementAndTasks(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 3)
	failed := f.moveTask(t, result.Tasks[0].ID, domain.TaskStatusFailed)
	if err := failed.Finish(domain.TaskResultFailed, "构建失败", time.Now().UTC()); err != nil {
		t.Fatalf("写入最终结论失败: %v", err)
	}
	if err := f.base.db.UpdateTask(f.base.ctx, failed); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	// 第三个任务走真实的取消流程：queued 任务直接收敛为 cancelled 并记录取消请求。
	cancelledTask := f.task(t, result.Tasks[2].ID)
	if _, err := cancelledTask.ApplyCancellationRequest(time.Now().UTC()); err != nil {
		t.Fatalf("取消等待中的任务失败: %v", err)
	}
	if err := f.base.db.UpdateTaskCancellation(
		f.base.ctx,
		cancelledTask,
		[]domain.TaskStatus{domain.TaskStatusQueued},
	); err != nil {
		t.Fatalf("写入取消请求失败: %v", err)
	}
	cancelled := cancelledTask

	deleted, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String())
	if err != nil {
		t.Fatalf("删除需求失败: %v", err)
	}
	if deleted.DeletedTaskCount != 3 {
		t.Fatalf("删除任务数量不符: %d", deleted.DeletedTaskCount)
	}
	if deleted.DeletedAt.IsZero() {
		t.Fatal("删除结果必须包含真实删除时间")
	}

	persistedRequirement := f.requirement(t, result.Requirement.ID)
	if persistedRequirement.DeletedAt == nil {
		t.Fatal("需求应写入删除时间")
	}
	if persistedRequirement.Status != result.Requirement.Status {
		t.Fatalf("需求业务状态不得被改写: %s", persistedRequirement.Status)
	}
	if string(persistedRequirement.FinalPlanJSON) != string(result.Requirement.FinalPlanJSON) {
		t.Fatal("需求确认快照不得被清空")
	}

	expected := map[domain.ID]domain.TaskStatus{
		result.Tasks[0].ID: domain.TaskStatusFailed,
		result.Tasks[1].ID: domain.TaskStatusQueued,
		result.Tasks[2].ID: domain.TaskStatusCancelled,
	}
	for taskID, status := range expected {
		persisted := f.task(t, taskID)
		if persisted.DeletedAt == nil {
			t.Fatalf("任务 %s 应写入删除时间", taskID)
		}
		if persisted.Status != status {
			t.Fatalf("任务 %s 状态被改写: 期望 %s，实际 %s", taskID, status, persisted.Status)
		}
	}
	persistedFailed := f.task(t, failed.ID)
	if persistedFailed.Status != domain.TaskStatusFailed ||
		persistedFailed.FinalResult != domain.TaskResultFailed ||
		persistedFailed.FailureSummary != "构建失败" {
		t.Fatalf("失败任务的结论不得被改写: %+v", persistedFailed.FinalResult)
	}
	persistedCancelled := f.task(t, cancelled.ID)
	if persistedCancelled.Status != domain.TaskStatusCancelled ||
		!persistedCancelled.CancelRequested {
		t.Fatal("取消记录不得被改写")
	}

	// 默认视图不再可见，剔除默认过滤后仍可读到原记录。
	requirements := f.base.requirements(t)
	if len(requirements) != 0 {
		t.Fatalf("删除后默认列表不应有需求: %d", len(requirements))
	}
	if _, err := f.service.Detail(f.base.ctx, result.Requirement.ID.String()); !errors.Is(
		err,
		app.ErrRequirementNotFound,
	) {
		t.Fatalf("删除后需求详情应按不存在处理，实际: %v", err)
	}
	if _, err := f.service.Progress(f.base.ctx, result.Requirement.ID.String()); !errors.Is(
		err,
		app.ErrRequirementNotFound,
	) {
		t.Fatalf("删除后需求进度应按不存在处理，实际: %v", err)
	}

	// 每个任务时间线各有一条需求删除事件，且带上 requirement_id。
	for _, created := range result.Tasks {
		events, listErr := f.base.db.ListEventsByTask(f.base.ctx, created.ID, storage.EventFilter{})
		if listErr != nil {
			t.Fatalf("读取任务事件失败: %v", listErr)
		}
		if len(events) != 1 || events[0].Type != domain.EventTypeRequirementDeleted {
			t.Fatalf("任务 %s 应有一条需求删除事件: %+v", created.ID, events)
		}
		var payload map[string]string
		if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
			t.Fatalf("解析事件内容失败: %v", err)
		}
		if payload["requirement_id"] != result.Requirement.ID.String() {
			t.Fatalf("事件内容应包含需求标识: %+v", payload)
		}
	}
}

// TestDeleteRejectsUnsettledTaskStatuses 校验不安全的任务状态一律拒绝删除。
func TestDeleteRejectsUnsettledTaskStatuses(t *testing.T) {
	t.Parallel()
	statuses := []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
		domain.TaskStatusDeveloping,
		domain.TaskStatusVerifying,
		domain.TaskStatusSaving,
		domain.TaskStatusRetrying,
		domain.TaskStatusCancelling,
		domain.TaskStatusPaused,
		domain.TaskStatusRecoveryRequired,
	}
	for _, status := range statuses {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			f := newDeleteFixture(t)
			result := f.confirm(t, 2)
			f.moveTask(t, result.Tasks[0].ID, status)
			f.moveTask(t, result.Tasks[1].ID, domain.TaskStatusFailed)

			_, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String())
			if !errors.Is(err, app.ErrRequirementStateConflict) {
				t.Fatalf("状态 %s 应拒绝删除，实际: %v", status, err)
			}
			f.requireNothingDeleted(t, result)
		})
	}
}

// TestDeleteRejectsUnknownTaskStatus 校验未知状态失败关闭。
func TestDeleteRejectsUnknownTaskStatus(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 1)
	f.forceTaskStatus(t, result.Tasks[0].ID, domain.TaskStatus("mystery"))

	_, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String())
	if !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("未知状态应拒绝删除，实际: %v", err)
	}
	f.requireNothingDeleted(t, result)
	persisted := f.task(t, result.Tasks[0].ID)
	if persisted.Status != domain.TaskStatus("mystery") {
		t.Fatalf("未知状态不得被映射为默认状态: %s", persisted.Status)
	}
}

// TestDeleteRejectsActiveWorkflowRun 校验任务终态但仍有活动执行记录时拒绝删除。
func TestDeleteRejectsActiveWorkflowRun(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 1)
	failed := f.moveTask(t, result.Tasks[0].ID, domain.TaskStatusFailed)
	step := f.mustStep(t, failed, 1)
	run, err := domain.NewWorkflowRun(failed.ID, []domain.Step{step}, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造工作流记录失败: %v", err)
	}
	if err := f.base.db.CreateWorkflowRun(f.base.ctx, run); err != nil {
		t.Fatalf("写入工作流记录失败: %v", err)
	}

	_, err = f.service.Delete(f.base.ctx, result.Requirement.ID.String())
	if !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("存在活动执行记录时应拒绝删除，实际: %v", err)
	}
	f.requireNothingDeleted(t, result)
}

// TestDeleteRejectsOccupiedStep 校验任务终态但步骤仍被占用时拒绝删除。
func TestDeleteRejectsOccupiedStep(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 1)
	failed := f.moveTask(t, result.Tasks[0].ID, domain.TaskStatusFailed)
	step := f.mustStep(t, failed, 1)
	if err := step.Transition(domain.StepStatusDeveloping, time.Now().UTC()); err != nil {
		t.Fatalf("步骤状态迁移失败: %v", err)
	}
	if err := f.base.db.UpdateStep(f.base.ctx, step); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}

	_, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String())
	if !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("步骤仍被占用时应拒绝删除，实际: %v", err)
	}
	f.requireNothingDeleted(t, result)
}

// TestDeleteRejectsUnresolvedCodeSave 校验外部副作用结果不明时拒绝删除。
func TestDeleteRejectsUnresolvedCodeSave(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 1)
	failed := f.moveTask(t, result.Tasks[0].ID, domain.TaskStatusFailed)
	branch, err := domain.TaskBranchName(failed.ID.String())
	if err != nil {
		t.Fatalf("生成任务分支名失败: %v", err)
	}
	record, err := domain.NewCodeSaveRecord(
		domain.ID(""),
		failed.ID.String(),
		"container-1",
		"docker-1",
		"origin",
		"ssh://git.example.com/team/confirm-repo",
		branch,
		"main",
		false,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	if err := f.base.db.CreateCodeSaveRecord(f.base.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}

	_, err = f.service.Delete(f.base.ctx, result.Requirement.ID.String())
	if !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("外部副作用未核验时应拒绝删除，实际: %v", err)
	}
	f.requireNothingDeleted(t, result)
}

// TestDeleteRejectsHeldExecutionGrant 校验已有执行权时拒绝删除，且不残留任何执行权。
func TestDeleteRejectsHeldExecutionGrant(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 3)
	held := result.Tasks[1].ID
	grant, acquired := f.coordination.TryAcquire(held)
	if !acquired {
		t.Fatal("测试应能取得任务执行权")
	}
	defer f.coordination.Release(grant)

	_, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String())
	if !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("存在执行占用时应拒绝删除，实际: %v", err)
	}
	f.requireNothingDeleted(t, result)
	// 失败路径必须释放此前取得的其他任务执行权。
	for _, task := range result.Tasks {
		if task.ID == held {
			continue
		}
		probe, ok := f.coordination.TryAcquire(task.ID)
		if !ok {
			t.Fatalf("失败后任务 %s 的执行权未被释放", task.ID)
		}
		f.coordination.Release(probe)
	}
}

// TestDeleteReleasesGrantsAfterSuccess 校验删除成功后全部执行权均已释放。
func TestDeleteReleasesGrantsAfterSuccess(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 2)
	if _, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String()); err != nil {
		t.Fatalf("删除需求失败: %v", err)
	}
	for _, task := range result.Tasks {
		probe, ok := f.coordination.TryAcquire(task.ID)
		if !ok {
			t.Fatalf("删除成功后任务 %s 的执行权未被释放", task.ID)
		}
		f.coordination.Release(probe)
	}
	// 重复删除此时按"已删除"处理，不会产生第二次成功。
	if _, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String()); !errors.Is(
		err,
		app.ErrRequirementNotFound,
	) {
		t.Fatalf("重复删除应按不存在处理，实际: %v", err)
	}
}

// TestDeleteHoldsGrantsDuringTransaction 校验删除事务期间其他执行入口无法取得执行权。
func TestDeleteHoldsGrantsDuringTransaction(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 2)
	blocking := &blockingDeleteStore{
		Store:   f.base.db,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	service, err := app.NewRequirementService(app.RequirementServiceOptions{
		Coordination: f.coordination,
		Store:        blocking,
	})
	if err != nil {
		t.Fatalf("构造阻塞需求服务失败: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, deleteErr := service.Delete(f.base.ctx, result.Requirement.ID.String())
		done <- deleteErr
	}()
	select {
	case <-blocking.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("删除事务未在超时内进入写入阶段")
	}
	for _, task := range result.Tasks {
		if probe, ok := f.coordination.TryAcquire(task.ID); ok {
			f.coordination.Release(probe)
			t.Fatalf("删除事务期间任务 %s 不应可被其他入口取得执行权", task.ID)
		}
	}
	close(blocking.release)
	select {
	case deleteErr := <-done:
		if deleteErr != nil {
			t.Fatalf("删除需求失败: %v", deleteErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("删除未在超时内完成")
	}
}

// TestConcurrentRequirementDeleteOnlyOneSucceeds 校验同一需求并发删除只有一个成功。
func TestConcurrentRequirementDeleteOnlyOneSucceeds(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 2)
	const goroutines = 4
	var wait sync.WaitGroup
	var mutex sync.Mutex
	success := 0
	failures := make([]error, 0, goroutines)
	start := make(chan struct{})
	for index := 0; index < goroutines; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String())
			mutex.Lock()
			defer mutex.Unlock()
			if err == nil {
				success++
				return
			}
			failures = append(failures, err)
		}()
	}
	close(start)
	wait.Wait()
	if success != 1 {
		t.Fatalf("并发删除应只有一次成功，实际 %d 次", success)
	}
	for _, err := range failures {
		if !errors.Is(err, app.ErrRequirementStateConflict) &&
			!errors.Is(err, app.ErrRequirementNotFound) {
			t.Fatalf("失败请求应返回冲突或不存在，实际: %v", err)
		}
	}
	for _, task := range result.Tasks {
		events, err := f.base.db.ListEventsByTask(f.base.ctx, task.ID, storage.EventFilter{})
		if err != nil {
			t.Fatalf("读取任务事件失败: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("任务 %s 的需求删除事件应只有一条，实际 %d 条", task.ID, len(events))
		}
	}
}

// TestDeleteKeepsOtherRequirementsRunning 校验删除一个需求不影响其他需求的执行占用。
func TestDeleteKeepsOtherRequirementsRunning(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	running := f.confirm(t, 2)
	idle := f.confirm(t, 2)
	grant, acquired := f.coordination.TryAcquire(running.Tasks[0].ID)
	if !acquired {
		t.Fatal("测试应能取得其他需求的执行权")
	}
	defer f.coordination.Release(grant)

	if _, err := f.service.Delete(f.base.ctx, idle.Requirement.ID.String()); err != nil {
		t.Fatalf("删除闲置需求失败: %v", err)
	}
	if f.requirement(t, running.Requirement.ID).DeletedAt != nil {
		t.Fatal("删除其他需求不得影响正在执行的需求")
	}
	if !f.coordination.Held(running.Tasks[0].ID) {
		t.Fatal("删除其他需求不得释放正在执行需求的执行权")
	}
	if _, err := f.base.db.GetActiveWorkflowRun(
		f.base.ctx,
		running.Tasks[0].ID,
	); err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("读取执行记录失败: %v", err)
	}
}

// TestDeleteRemovesTasksFromProjectFacts 校验删除后项目调度视图不再包含该需求的任务。
func TestDeleteRemovesTasksFromProjectFacts(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 2)
	f.ensureProjectForFacts(t)
	before, err := f.base.db.ListProjectTaskFacts(f.base.ctx, domain.ID(confirmProjectID))
	if err != nil {
		t.Fatalf("查询项目任务事实失败: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("删除前项目任务事实数量不符: %d", len(before))
	}
	if _, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String()); err != nil {
		t.Fatalf("删除需求失败: %v", err)
	}
	after, err := f.base.db.ListProjectTaskFacts(f.base.ctx, domain.ID(confirmProjectID))
	if err != nil {
		t.Fatalf("删除后查询项目任务事实失败: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("删除后项目调度不应再看到该需求的任务: %d", len(after))
	}
}

// TestDeleteKeepsExecutionHistory 校验删除后执行历史仍可按任务读取。
func TestDeleteKeepsExecutionHistory(t *testing.T) {
	t.Parallel()
	f := newDeleteFixture(t)
	result := f.confirm(t, 1)
	failed := f.moveTask(t, result.Tasks[0].ID, domain.TaskStatusFailed)
	step := f.mustStep(t, failed, 1)

	if _, err := f.service.Delete(f.base.ctx, result.Requirement.ID.String()); err != nil {
		t.Fatalf("删除需求失败: %v", err)
	}
	steps, err := f.base.db.ListStepsByTask(f.base.ctx, failed.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if len(steps) != 1 || steps[0].ID != step.ID {
		t.Fatalf("步骤记录应保留: %+v", steps)
	}
	events, err := f.base.db.ListEventsByTask(f.base.ctx, failed.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("事件时间线不应被清空")
	}
}

// requireNothingDeleted 断言需求与全部任务都没有被部分删除。
func (f *deleteFixture) requireNothingDeleted(t *testing.T, result app.ConfirmRequirementResult) {
	t.Helper()
	if f.requirement(t, result.Requirement.ID).DeletedAt != nil {
		t.Fatal("被拒绝时不得写入需求删除时间")
	}
	for _, created := range result.Tasks {
		persisted := f.task(t, created.ID)
		if persisted.DeletedAt != nil {
			t.Fatalf("被拒绝时任务 %s 不得被删除", created.ID)
		}
	}
	visible, err := f.base.db.ListTasks(f.base.ctx, storage.TaskFilter{
		RequirementID: result.Requirement.ID,
	})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(visible) != len(result.Tasks) {
		t.Fatalf("被拒绝时全部任务都应可见: %d/%d", len(visible), len(result.Tasks))
	}
}

// ensureProjectForFacts 确保测试项目已登记（确认流程已经创建）。
func (f *deleteFixture) ensureProjectForFacts(t *testing.T) {
	t.Helper()
	f.base.ensureProject(t)
}

// mustStep 在任务下写入一个待执行步骤。
func (f *deleteFixture) mustStep(t *testing.T, task domain.Task, sequence int) domain.Step {
	t.Helper()
	role, err := f.base.db.GetRole(f.base.ctx, domain.RoleIDDeveloper)
	if err != nil {
		t.Fatalf("读取开发角色失败: %v", err)
	}
	step, err := domain.NewStep(
		task.ID,
		sequence,
		"步骤标题",
		"步骤目标",
		"执行要求",
		*role,
		domain.VerificationSpec{
			AcceptanceCriteria: []string{"构建通过"},
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := f.base.db.CreateStep(f.base.ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return step
}

// blockingDeleteStore 在需求条件软删除处暂停，用于观察删除事务期间的执行权状态。
//
// 它只增加一个确定性屏障，不改变任何存储语义：真正执行的仍然是同一个 SQLite 事务。
type blockingDeleteStore struct {
	storage.Store
	entered chan struct{}
	release chan struct{}
}

// WithTx 在事务内换成带屏障的 Tx，使删除流程在写入需求时暂停。
func (s *blockingDeleteStore) WithTx(
	ctx context.Context,
	fn func(ctx context.Context, tx storage.Tx) error,
) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		return fn(ctx, &blockingDeleteTx{Tx: tx, entered: s.entered, release: s.release})
	})
}

// blockingDeleteTx 只拦截需求条件软删除，其余写入原样委派。
type blockingDeleteTx struct {
	storage.Tx
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// DeleteRequirement 在首次调用时通知测试并等待放行。
func (t *blockingDeleteTx) DeleteRequirement(
	ctx context.Context,
	id domain.ID,
	at time.Time,
) error {
	t.once.Do(func() {
		close(t.entered)
		<-t.release
	})
	return t.Tx.DeleteRequirement(ctx, id, at)
}

// 保证测试辅助类型满足仓储接口，编译期即可发现签名漂移。
var _ storage.Store = (*blockingDeleteStore)(nil)
