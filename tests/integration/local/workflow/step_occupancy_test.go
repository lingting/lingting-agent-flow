// Package workflow_test 验证 B1 修复后的步骤占用收敛语义。
//
// 用例使用真实 SQLite 与正式的 app.TaskLaunchService / app.TaskWorkflowService：
// 断言的对象是数据库里真实留下的 Task / WorkflowRun / Step / Attempt 记录，而不是
// 方法的返回值。Agent 执行与容器核验由受控替身提供，恢复链路本身不重放任何调用。
package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// startOccupiedStep 把步骤推进到指定占用状态并登记一次执行尝试。
//
// 它与真实执行路径一致：先创建 running 尝试，再让步骤进入占用状态并关联该尝试。
// 只有这样，收敛时才需要同时验证"步骤状态被改写"与"尝试占用被释放"。
func startOccupiedStep(
	t *testing.T,
	fixture *launchFixture,
	step domain.Step,
	status domain.StepStatus,
) domain.Step {
	t.Helper()
	attempt, err := fixture.db.StartAttempt(fixture.ctx, storage.StartAttemptRequest{
		TaskID: step.TaskID,
		StepID: step.ID,
		At:     fixture.now,
	})
	if err != nil {
		t.Fatalf("创建执行尝试失败: %v", err)
	}
	// 状态机只允许 pending → running（以及 running → verifying）：真实执行路径
	// 也正是先占用为 running，再由闭环推进到 verifying。按同一路径迁移，避免用
	// 非法迁移伪造现场。
	for _, next := range []domain.StepStatus{
		domain.StepStatusRunning,
		status,
	} {
		if step.Status == next {
			continue
		}
		if err := step.Transition(next, fixture.now); err != nil {
			t.Fatalf("迁移步骤到 %s 失败: %v", next, err)
		}
	}
	if err := step.SetCurrentAttempt(&attempt.ID); err != nil {
		t.Fatalf("记录尝试占用失败: %v", err)
	}
	if err := fixture.db.UpdateStep(fixture.ctx, step); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	return step
}

// TestStepOccupancyConvergesAfterRunBlocked 验证 B1 的完整锁死场景被解除。
//
// 故障现场：进程在 Pi 调用期间退出，步骤停在 developing，执行记录停在 running。
// 重启后 TaskWorkflowService 先把执行记录收敛为 blocked，TaskLaunchService 再收敛
// 步骤占用。修复前 blocked 记录被当作"仍有未结束执行"，步骤永远得不到收敛，
// 任务既无法 Launch 也无法 ResumeWorkflow。
func TestStepOccupancyConvergesAfterRunBlocked(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	steps := fixture.writeSteps(t, 1)
	step := startOccupiedStep(t, fixture, steps[0], domain.StepStatusVerifying)

	run, err := domain.NewWorkflowRun(fixture.task.ID, steps, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	// 租约已经过期：说明持有者没有续租，进程已经退出。
	if err := run.Start("worker-crashed", time.Minute, fixture.now.Add(-time.Hour)); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}

	// 第一步：编排器的启动恢复把过期 running 收敛为 blocked。
	workflow := fixture.workflowService(t)
	blocked, err := fixture.db.GetWorkflowRun(fixture.ctx, run.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if blocked.Status != domain.WorkflowRunBlocked {
		t.Fatalf("过期的 running 应被收敛为 blocked，实际 %s", blocked.Status)
	}
	if blocked.ErrorClass != "worker_interrupted" {
		t.Fatalf("收敛原因应为 worker_interrupted，实际 %q", blocked.ErrorClass)
	}

	// 第二步：启动服务收敛步骤占用。blocked 记录不再持有执行权，不得阻止收敛。
	service := fixture.launchService(
		t,
		&launchEnvironment{containerID: "container-launch"},
		&launchContainers{usable: true},
		&launchPlanner{},
		&launchWorkspaceStub{},
		&launchWorkflow{},
	)
	if err := service.RecoverStepOccupancy(fixture.ctx); err != nil {
		t.Fatalf("收敛步骤占用失败: %v", err)
	}
	stored, err := fixture.db.GetStep(fixture.ctx, step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stored.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤应收敛为 recovery_required，实际 %s", stored.Status)
	}
	if stored.CurrentAttemptID != nil {
		t.Fatal("收敛后必须释放尝试占用，否则用户无法显式重新执行")
	}
	if stored.FailureSummary == "" {
		t.Fatal("收敛后必须写入可读摘要")
	}
	// 无法确认结果时不得把步骤标记为成功。
	if stored.Status.IsTerminal() {
		t.Fatalf("结果不确定时不得进入终态，实际 %s", stored.Status)
	}
	_ = workflow
}

// TestStepOccupancyKeepsLiveLeaseOwner 验证仍持有有效租约的执行不被抢占。
//
// 判据必须是"执行权"而不是"存在未结束记录"：否则一个仍在长调用中的正常执行会被
// 启动恢复误判为残留（B6 的另一面）。
func TestStepOccupancyKeepsLiveLeaseOwner(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	steps := fixture.writeSteps(t, 1)
	step := startOccupiedStep(t, fixture, steps[0], domain.StepStatusVerifying)

	run, err := domain.NewWorkflowRun(fixture.task.ID, steps, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	// 租约仍然有效：另一个进程可能正在推进同一任务。
	if err := run.Start("worker-live", time.Hour, fixture.now); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}

	service := fixture.launchService(
		t,
		&launchEnvironment{containerID: "container-launch"},
		&launchContainers{usable: true},
		&launchPlanner{},
		&launchWorkspaceStub{},
		&launchWorkflow{},
	)
	if err := service.RecoverStepOccupancy(fixture.ctx); err != nil {
		t.Fatalf("收敛步骤占用失败: %v", err)
	}
	stored, err := fixture.db.GetStep(fixture.ctx, step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stored.Status != domain.StepStatusVerifying {
		t.Fatalf("有效执行权存在时不得抢占，实际 %s", stored.Status)
	}
	if stored.CurrentAttemptID == nil {
		t.Fatal("未收敛时不得释放尝试占用")
	}
}

// TestStepOccupancyConvergedTaskCanLaunch 验证收敛后任务能通过正式入口继续处理。
//
// B1 的关键验收点：不能只是把 running 改成 recovery_required，却没有任何正式入口
// 可以核验或处理现场。
func TestStepOccupancyConvergedTaskCanLaunch(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	steps := fixture.writeSteps(t, 1)
	step := startOccupiedStep(t, fixture, steps[0], domain.StepStatusRunning)

	run, err := domain.NewWorkflowRun(fixture.task.ID, steps, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("worker-crashed", time.Minute, fixture.now.Add(-time.Hour)); err != nil {
		t.Fatalf("登记执行权失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}

	// 先收敛执行记录，再收敛步骤占用：两者都通过正式启动恢复完成。
	fixture.workflowService(t)
	launchService := fixture.launchService(
		t,
		&launchEnvironment{containerID: "container-launch"},
		&launchContainers{usable: true},
		&launchPlanner{},
		&launchWorkspaceStub{},
		&launchWorkflow{},
	)
	if err := launchService.RecoverStepOccupancy(fixture.ctx); err != nil {
		t.Fatalf("收敛步骤占用失败: %v", err)
	}
	stored, err := fixture.db.GetStep(fixture.ctx, step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stored.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤应收敛为 recovery_required，实际 %s", stored.Status)
	}

	// 收敛后的现场必须能通过正式的步骤执行入口重新处理：人工核验后重新执行步骤。
	// 这里只验证准入判定不再被残留占用挡住，不启动真实 Agent。
	if !stored.Status.CanStartExecution() {
		t.Fatalf("recovery_required 步骤必须允许显式重新执行，实际状态 %s", stored.Status)
	}
	if stored.CurrentAttemptID != nil {
		t.Fatal("残留占用未被释放，正式入口无法重新执行该步骤")
	}
	// 任务层的恢复入口也必须重新可用：阻塞的只有执行记录，不是任务本身。
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status.IsFinished() {
		t.Fatalf("收敛后任务不应处于已结束状态，实际 %s", task.Status)
	}
}

// TestStepOccupancyConvergesOnceAcrossRestarts 验证并发/重复收敛是幂等的。
//
// 两个实例共享同一数据库同时启动：状态迁移以状态为前置条件，只有一个能写入，
// 因此最终状态唯一且恢复事件只有一条。
func TestStepOccupancyConvergesOnceAcrossRestarts(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	steps := fixture.writeSteps(t, 1)
	step := startOccupiedStep(t, fixture, steps[0], domain.StepStatusVerifying)

	services := make([]*app.TaskLaunchService, 2)
	for index := range services {
		services[index] = fixture.launchService(
			t,
			&launchEnvironment{containerID: "container-launch"},
			&launchContainers{usable: true},
			&launchPlanner{},
			&launchWorkspaceStub{},
			&launchWorkflow{},
		)
	}
	for _, service := range services {
		if err := service.RecoverStepOccupancy(fixture.ctx); err != nil {
			t.Fatalf("重复收敛步骤占用必须幂等: %v", err)
		}
	}
	stored, err := fixture.db.GetStep(fixture.ctx, step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stored.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤应收敛为 recovery_required，实际 %s", stored.Status)
	}
	events := fixture.events(t, fixture.task.ID)
	if count := countEvents(events, domain.EventTypeTaskRecoveryRequired); count != 1 {
		t.Fatalf("重复收敛只允许写一条恢复事件，实际 %d 条", count)
	}
}

// TestStepOccupancyReadFailureDoesNotConverge 验证读取执行记录失败时不抢占。
//
// 读取失败不能被当作"没有未结束执行"：那会让恢复在没有证据的情况下改写步骤状态。
func TestStepOccupancyReadFailureDoesNotConverge(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	steps := fixture.writeSteps(t, 1)
	step := startOccupiedStep(t, fixture, steps[0], domain.StepStatusVerifying)

	store := &failingLaunchStore{
		TaskLaunchStore: fixture.db,
		activeRunErr:    errors.New("数据库读取失败"),
	}
	service, err := app.NewTaskLaunchService(app.TaskLaunchOptions{
		Store:      store,
		Containers: &launchContainers{usable: true},
		Logger:     fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造启动服务失败: %v", err)
	}
	if err := service.RecoverStepOccupancy(fixture.ctx); err == nil {
		t.Fatal("读取未结束执行失败时必须返回错误")
	}
	stored, err := fixture.db.GetStep(fixture.ctx, step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if stored.Status != domain.StepStatusVerifying {
		t.Fatalf("读取失败时不得改写步骤状态，实际 %s", stored.Status)
	}
}

// failingLaunchStore 包装真实存储，只把指定方法替换为可控失败。
type failingLaunchStore struct {
	app.TaskLaunchStore
	activeRunErr error
}

// GetActiveWorkflowRun 在配置了错误时返回该错误。
func (s *failingLaunchStore) GetActiveWorkflowRun(
	ctx context.Context,
	taskID domain.ID,
) (*domain.WorkflowRun, error) {
	if s.activeRunErr != nil {
		return nil, s.activeRunErr
	}
	return s.TaskLaunchStore.GetActiveWorkflowRun(ctx, taskID)
}
