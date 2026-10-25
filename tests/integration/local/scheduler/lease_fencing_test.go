// 任务级执行权（任务租约）与单步骤执行入口的一致性测试。
//
// 用例使用真实 SQLite 与正式迁移，验证的核心约束是：
//   - 同一任务的任务级工作流与单步骤入口竞争时只有一个获得执行权；
//   - 旧代次的执行者不能占用步骤、不能写入任务状态；
//   - 两个不同任务可以在各自租约下并发推进，互不共享可写状态；
//   - 用户取消不需要取得任务租约。
package scheduler_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// TestSingleStepAndWorkflowCompeteForSameLease 验证两类入口竞争同一任务执行权。
//
// 单步骤入口与任务级工作流推进的是同一份工作区、容器与 Session：如果两者同时
// 持有执行权，就会出现"两套所有权同时改同一份现场"。因此它们必须竞争同一份
// 任务租约，且只有一个能成功。
func TestSingleStepAndWorkflowCompeteForSameLease(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)

	// 任务级工作流先取得执行权。
	workflowClaim, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "workflow-" + domain.NewID().String(),
		Statuses: domain.ExplicitWorkflowStatuses(),
	})
	if err != nil {
		t.Fatalf("工作流领取执行权失败: %v", err)
	}
	// 单步骤入口此时必须被拒绝：它用的是同一份任务租约。
	_, err = fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "step-" + domain.NewID().String(),
		Statuses: domain.ExplicitStepExecutionStatuses(),
	})
	if err == nil {
		t.Fatal("工作流持有执行权时，单步骤入口不得同时取得执行权")
	}
	if !errors.Is(err, app.ErrTaskLeaseConflict) {
		t.Fatalf("拒绝原因应为执行权冲突，实际 %v", err)
	}
	// 释放后单步骤入口才能领取。
	if releaseErr := fixture.leases.Release(fixture.ctx, workflowClaim.Handle); releaseErr != nil {
		t.Fatalf("释放工作流执行权失败: %v", releaseErr)
	}
	stepClaim, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "step-" + domain.NewID().String(),
		Statuses: domain.ExplicitStepExecutionStatuses(),
	})
	if err != nil {
		t.Fatalf("释放后单步骤入口应能领取执行权: %v", err)
	}
	if stepClaim.Handle.Epoch != workflowClaim.Handle.Epoch+1 {
		t.Fatalf("第二次领取的代次应递增，实际 %d → %d",
			workflowClaim.Handle.Epoch, stepClaim.Handle.Epoch)
	}
}

// TestStaleLeaseCannotOccupyStep 验证旧代次不能占用步骤。
//
// 现场：执行者 A 领取租约，随后任务被 B 接管。A 再尝试占用步骤时必须被数据库
// 条件拒绝——这是"旧执行者不得继续修改新代次现场"的落点。
func TestStaleLeaseCannotOccupyStep(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)
	step := fixture.writeStep(t, task.ID, 1)

	claimA, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "owner-a",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	})
	if err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	// 让 A 的租约过期，然后由 B 接管。
	fixture.expireLease(t, task.ID)
	claimB, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "owner-b",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	})
	if err != nil {
		t.Fatalf("接管失败: %v", err)
	}
	if claimB.Handle.Epoch <= claimA.Handle.Epoch {
		t.Fatalf("接管后代次必须递增，实际 %d → %d", claimA.Handle.Epoch, claimB.Handle.Epoch)
	}
	// A 用旧代次占用步骤：必须被租约栅栏拒绝。
	_, err = fixture.db.BeginStepExecution(fixture.ctx, storage.StepExecutionStartRequest{
		TaskID:             task.ID,
		StepID:             step.ID,
		ExpectedTaskStatus: domain.TaskStatusPlanned,
		ExpectedStepStatus: domain.StepStatusPending,
		Lease:              &claimA.Handle,
		At:                 fixture.now,
	})
	if err == nil {
		t.Fatal("旧代次的执行者不得占用步骤")
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("拒绝原因应为条件冲突，实际 %v", err)
	}
	// 新代次占用必须成功。
	started, err := fixture.db.BeginStepExecution(fixture.ctx, storage.StepExecutionStartRequest{
		TaskID:             task.ID,
		StepID:             step.ID,
		ExpectedTaskStatus: domain.TaskStatusPlanned,
		ExpectedStepStatus: domain.StepStatusPending,
		Lease:              &claimB.Handle,
		At:                 fixture.now,
	})
	if err != nil {
		t.Fatalf("新代次占用步骤失败: %v", err)
	}
	if started.Attempt.Status != domain.AttemptStatusRunning {
		t.Fatalf("占用后尝试应为 running，实际 %s", started.Attempt.Status)
	}
}

// TestStaleLeaseCannotWriteTaskStatus 验证旧代次不能写入任务状态。
func TestStaleLeaseCannotWriteTaskStatus(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusPlanned)

	claimA, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "owner-a",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	})
	if err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	fixture.expireLease(t, task.ID)
	if _, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "owner-b",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	}); err != nil {
		t.Fatalf("接管失败: %v", err)
	}
	current, err := fixture.db.GetTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	stale := *current
	if err := stale.Transition(domain.TaskStatusDeveloping, fixture.now); err != nil {
		t.Fatalf("推进任务状态失败: %v", err)
	}
	err = fixture.leases.UpdateGuarded(
		fixture.ctx,
		stale,
		[]domain.TaskStatus{domain.TaskStatusPlanned},
		claimA.Handle,
	)
	if err == nil {
		t.Fatal("旧代次的执行者不得写入任务状态")
	}
	if !errors.Is(err, app.ErrTaskLeaseLost) {
		t.Fatalf("拒绝原因应为执行权丢失，实际 %v", err)
	}
}

// TestTwoTasksAdvanceConcurrentlyUnderOwnLeases 验证两个任务可以并发推进。
//
// 两个任务各自持有自己的任务租约、各自的步骤与容器：它们必须都能成功占用步骤，
// 且彼此的 Attempt 与容器归属互不干扰。
func TestTwoTasksAdvanceConcurrentlyUnderOwnLeases(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	taskA := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	taskB := fixture.queuedTask(t, domain.PriorityNormal, fixture.now.Add(time.Second))
	fixture.moveTaskTo(t, taskA.ID, domain.TaskStatusPlanned)
	fixture.moveTaskTo(t, taskB.ID, domain.TaskStatusPlanned)
	stepA := fixture.writeStep(t, taskA.ID, 1)
	stepB := fixture.writeStep(t, taskB.ID, 1)
	containerA := fixture.attachContainer(t, taskA.ID, "container-a")
	containerB := fixture.attachContainer(t, taskB.ID, "container-b")

	type outcome struct {
		taskID    domain.ID
		attemptID domain.ID
		container string
		err       error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	run := func(task domain.Task, step domain.Step, containerID string) {
		<-start
		claim, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
			TaskID:   task.ID,
			Owner:    "owner-" + task.ID.String(),
			Statuses: domain.ExplicitStepExecutionStatuses(),
		})
		if err != nil {
			results <- outcome{taskID: task.ID, err: err}
			return
		}
		started, beginErr := fixture.db.BeginStepExecution(
			fixture.ctx,
			storage.StepExecutionStartRequest{
				TaskID:             task.ID,
				StepID:             step.ID,
				ExpectedTaskStatus: domain.TaskStatusPlanned,
				ExpectedStepStatus: domain.StepStatusPending,
				ContainerID:        containerID,
				DockerID:           "docker-" + containerID,
				Lease:              &claim.Handle,
				At:                 fixture.now,
			},
		)
		results <- outcome{
			taskID:    task.ID,
			attemptID: started.Attempt.ID,
			container: started.Attempt.ContainerID,
			err:       beginErr,
		}
	}
	go run(taskA, stepA, containerA)
	go run(taskB, stepB, containerB)
	close(start)

	seen := make(map[domain.ID]outcome, 2)
	for index := 0; index < 2; index++ {
		item := <-results
		if item.err != nil {
			t.Fatalf("任务 %s 并发占用步骤失败: %v", item.taskID, item.err)
		}
		seen[item.taskID] = item
	}
	if seen[taskA.ID].attemptID == seen[taskB.ID].attemptID {
		t.Fatal("两个任务的执行尝试必须不同")
	}
	if seen[taskA.ID].container == seen[taskB.ID].container {
		t.Fatal("两个任务的执行容器必须不同")
	}
	// 数据库里的步骤占用必须各自指向自己的尝试。
	for _, pair := range []struct {
		task domain.Task
		step domain.Step
	}{
		{task: taskA, step: stepA},
		{task: taskB, step: stepB},
	} {
		stored, err := fixture.db.GetStep(fixture.ctx, pair.step.ID)
		if err != nil {
			t.Fatalf("读取步骤失败: %v", err)
		}
		if stored.CurrentAttemptID == nil {
			t.Fatalf("步骤 %s 应被本次尝试占用", stored.ID)
		}
		if *stored.CurrentAttemptID != seen[pair.task.ID].attemptID {
			t.Fatalf("步骤 %s 的占用尝试不符", stored.ID)
		}
	}
}

// TestCancelDoesNotRequireTaskLease 验证用户取消不需要任务执行权。
//
// 取消意图必须能被持久化：如果取消也要先取得任务租约，那么在被异常执行者占满
// 名额时用户将无法停止任务。
func TestCancelDoesNotRequireTaskLease(t *testing.T) {
	t.Parallel()
	fixture := newSchedulerFixture(t)
	task := fixture.queuedTask(t, domain.PriorityNormal, fixture.now)
	fixture.moveTaskTo(t, task.ID, domain.TaskStatusDeveloping)
	// 让另一个执行者占住任务租约。
	if _, err := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "holder",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	}); err != nil {
		t.Fatalf("领取执行权失败: %v", err)
	}
	// 用户取消走任务管理服务：它不需要任务租约。
	service, err := app.NewTaskService(app.TaskServiceOptions{Store: fixture.db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	cancelled, err := service.CancelTask(fixture.ctx, task.ID.String())
	if err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	if !cancelled.CancelRequested {
		t.Fatal("取消意图必须被持久化")
	}
	// 运行中的任务只记录取消意图并进入 cancelling，实际停止由执行者确认：
	// 这里断言"没有伪造已停止"（不是 cancelled、没有结论与结束时间），而不是断言
	// 任务停留在执行阶段的中间状态。
	if cancelled.Status != domain.TaskStatusCancelling {
		t.Fatalf("运行中任务取消后应进入 cancelling，实际 %s", cancelled.Status)
	}
	if cancelled.FinalResult != domain.TaskResultNone || cancelled.FinishedAt != nil {
		t.Fatalf("尚未确认停止时不得写入结论与结束时间: %+v", cancelled)
	}
	// 取消意图落盘后，旧执行者不得再领取执行权。
	if _, claimErr := fixture.leases.Claim(fixture.ctx, app.TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "late",
		Statuses: domain.ExplicitStepExecutionStatuses(),
	}); claimErr == nil {
		t.Fatal("已请求取消的任务不得被再次领取执行权")
	}
}

// moveTaskTo 把任务推进到指定状态。
func (f *schedulerFixture) moveTaskTo(t *testing.T, taskID domain.ID, status domain.TaskStatus) {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	for _, next := range statusPath(task.Status, status) {
		if err := task.Transition(next, f.now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", next, err)
		}
	}
	if err := f.db.UpdateTask(f.ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
}

// statusPath 返回从当前状态到目标状态的合法迁移路径。
func statusPath(from, to domain.TaskStatus) []domain.TaskStatus {
	if from == to {
		return nil
	}
	switch to {
	case domain.TaskStatusPlanned:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanning,
			domain.TaskStatusPlanned,
		}
	case domain.TaskStatusDeveloping:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanning,
			domain.TaskStatusPlanned,
			domain.TaskStatusDeveloping,
		}
	default:
		return []domain.TaskStatus{to}
	}
}

// writeStep 写入一个待执行的开发步骤。
func (f *schedulerFixture) writeStep(
	t *testing.T,
	taskID domain.ID,
	sequence int,
) domain.Step {
	t.Helper()
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		f.now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := f.db.CreateRole(f.ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	step, err := domain.NewAgentStep(
		taskID,
		sequence,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现",
		"",
		role,
		domain.VerificationSpec{},
		domain.RetryConfig{MaxAttempts: 1},
		f.now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := f.db.CreateStep(f.ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return step
}

// attachContainer 写入一条属于该任务的容器记录并把引用写回任务。
func (f *schedulerFixture) attachContainer(
	t *testing.T,
	taskID domain.ID,
	label string,
) string {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	container, err := domain.NewContainer(
		"",
		*task.HostID,
		"agentflow-"+label,
		domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		domain.FormatVersionLabel(1),
		isolationBuildHash,
		isolationImageID,
		f.now,
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	container.TaskID = &taskID
	container.DockerID = "docker-" + container.ID.String()
	container.State = domain.ContainerStateRunning
	container.DockerState = domain.DockerStateRunning
	if err := f.db.CreateContainer(f.ctx, container); err != nil {
		t.Fatalf("写入容器记录失败: %v", err)
	}
	updated := *task
	if err := updated.SetContainer(container.ID.String()); err != nil {
		t.Fatalf("设置任务容器失败: %v", err)
	}
	if err := f.db.UpdateTaskIfStatus(f.ctx, updated, []domain.TaskStatus{task.Status}); err != nil {
		t.Fatalf("更新任务容器失败: %v", err)
	}
	return container.ID.String()
}

// expireLease 把任务的租约有效期改到过去，模拟持有者未续租。
func (f *schedulerFixture) expireLease(t *testing.T, taskID domain.ID) {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	past := f.now.Add(-time.Minute)
	task.LeaseExpiresAt = &past
	if err := f.db.UpdateTask(f.ctx, *task); err != nil {
		t.Fatalf("写入过期租约失败: %v", err)
	}
}
