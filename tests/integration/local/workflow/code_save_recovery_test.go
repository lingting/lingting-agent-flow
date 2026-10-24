// 代码保存恢复对工作流推进的门控集成测试。
//
// 覆盖的核心约束：收尾停在 blocked / recovery_required 之后，恢复会再次执行代码
// 保存。此前若已经产生过结果不明的 commit / push / merge，盲目重放会产生重复提交
// 或覆盖远程历史。因此恢复入口必须先读持久化记录，由
// domain.DecideTaskCodeSaveResume 判定是否允许推进。
//
// 用例使用真实 SQLite 与真实 app.TaskWorkflowService，只把容器核验与步骤闭环替换
// 为受控替身：断言的是"恢复请求被拒绝"与"记录未被改写"这两个持久化事实。
package workflow_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// recoveryGateFixture 构造一个"任务已阻塞、存在代码保存记录"的现场。
type recoveryGateFixture struct {
	*restartFixture
	task domain.Task
	run  domain.WorkflowRun
}

// newRecoveryGateFixture 准备任务、步骤、阻塞的执行记录与一份代码保存记录。
func newRecoveryGateFixture(
	t *testing.T,
	mutate func(*domain.CodeSaveRecord),
) *recoveryGateFixture {
	t.Helper()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusDeveloping, false)
	steps := fixture.writePlanSteps(t, task, 1)
	// 写入一条已停止的执行记录：恢复入口必须能从它继续。
	run, err := domain.NewWorkflowRun(task.ID, steps, fixture.now)
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	begin, err := fixture.db.GetWorkflowRun(fixture.ctx, run.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if err := begin.Start("worker-1", time.Minute, fixture.now); err != nil {
		t.Fatalf("启动执行记录失败: %v", err)
	}
	if err := fixture.db.UpdateWorkflowRun(fixture.ctx, *begin); err != nil {
		t.Fatalf("推进执行记录失败: %v", err)
	}
	stopped, err := fixture.db.GetWorkflowRun(fixture.ctx, run.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if err := stopped.Stop(
		domain.WorkflowRunBlocked,
		"code_save_indeterminate",
		"代码保存结果无法确认",
		fixture.now,
	); err != nil {
		t.Fatalf("停止执行记录失败: %v", err)
	}
	if err := fixture.db.UpdateWorkflowRun(fixture.ctx, *stopped); err != nil {
		t.Fatalf("写入停止状态失败: %v", err)
	}
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		task.ID.String(),
		"container-1",
		"docker-container-1",
		"origin",
		"",
		"agent/"+task.ID.String(),
		"main",
		false,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	record.CommitStatus = domain.CommitStatusSucceeded
	record.CommitSHA = "1111111111111111111111111111111111111111"
	if mutate != nil {
		mutate(&record)
	}
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	return &recoveryGateFixture{restartFixture: fixture, task: task, run: *stopped}
}

// resume 发起一次恢复请求。
func (f *recoveryGateFixture) resume(t *testing.T) error {
	t.Helper()
	service := f.mustStartService(t, nil, &fakeContainer{running: true}, &fakeRunner{})
	_, err := service.ResumeWorkflow(f.ctx, app.ResumeWorkflowRequest{
		TaskID: f.task.ID.String(),
		RunID:  f.run.ID.String(),
	})
	return err
}

// TestResumeWorkflowRejectsPendingCodeSave 验证存在未决外部副作用时拒绝恢复。
//
// 关键约束：数据库中的 pending 不能证明推送没有发生。恢复必须先做只读核验，
// 因此这里返回前置条件错误，并且不启动任何步骤执行。
func TestResumeWorkflowRejectsPendingCodeSave(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		mutate        func(*domain.CodeSaveRecord)
		expectedClass string
	}{
		{
			name:          "推送仍为 pending",
			mutate:        func(record *domain.CodeSaveRecord) {},
			expectedClass: "code_save_indeterminate",
		},
		{
			name: "推送结果未知",
			mutate: func(record *domain.CodeSaveRecord) {
				record.PushStatus = domain.PushStatusUnknown
			},
			expectedClass: "code_save_indeterminate",
		},
		{
			name: "推送冲突",
			mutate: func(record *domain.CodeSaveRecord) {
				record.PushStatus = domain.PushStatusConflict
			},
			expectedClass: "code_save_indeterminate",
		},
		{
			name: "记录写入未确认",
			mutate: func(record *domain.CodeSaveRecord) {
				record.PushStatus = domain.PushStatusSucceeded
				record.RemoteCommitSHA = record.CommitSHA
				record.PersistStatus = domain.PersistStatusUnconfirmed
			},
			expectedClass: "code_save_unconfirmed",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRecoveryGateFixture(t, testCase.mutate)
			err := fixture.resume(t)
			if err == nil {
				t.Fatal("存在未决外部副作用时必须拒绝恢复")
			}
			if !errors.Is(err, app.ErrWorkflowPrerequisite) {
				t.Fatalf("应返回前置条件错误，实际 %v", err)
			}
			if !containsErrorText(err, testCase.expectedClass) {
				t.Fatalf("错误必须带可定位的分类 %q，实际 %v", testCase.expectedClass, err)
			}
			// 现场没有被改写：执行记录与代码保存记录都保持原样。
			stored, readErr := fixture.db.GetWorkflowRun(fixture.ctx, fixture.run.ID)
			if readErr != nil {
				t.Fatalf("读取执行记录失败: %v", readErr)
			}
			if stored.Status != domain.WorkflowRunBlocked {
				t.Fatalf("拒绝恢复不得改写执行记录状态，实际 %s", stored.Status)
			}
			records, listErr := fixture.db.ListCodeSaveRecordsByTask(
				fixture.ctx,
				fixture.task.ID.String(),
			)
			if listErr != nil {
				t.Fatalf("读取代码保存记录失败: %v", listErr)
			}
			if len(records) != 1 {
				t.Fatalf("拒绝恢复不得追加保存记录，实际 %d 条", len(records))
			}
		})
	}
}

// TestResumeWorkflowRejectsUnverifiableCodeSave 验证无法确认时禁止自动推进。
//
// 与"需要核验"不同：这类记录已经核验过但拿不到可信事实，继续恢复只能靠人工判断，
// 因此返回状态冲突而不是前置条件错误。
func TestResumeWorkflowRejectsUnverifiableCodeSave(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryGateFixture(t, func(record *domain.CodeSaveRecord) {
		record.RecoveryStatus = domain.RecoveryStatusUnverifiable
	})
	err := fixture.resume(t)
	if err == nil {
		t.Fatal("核验无法确认时必须禁止自动推进")
	}
	if !errors.Is(err, app.ErrWorkflowConflict) {
		t.Fatalf("应返回状态冲突，实际 %v", err)
	}
	stored, readErr := fixture.db.GetWorkflowRun(fixture.ctx, fixture.run.ID)
	if readErr != nil {
		t.Fatalf("读取执行记录失败: %v", readErr)
	}
	if stored.Status != domain.WorkflowRunBlocked {
		t.Fatalf("拒绝恢复不得改写执行记录状态，实际 %s", stored.Status)
	}
}

// TestResumeWorkflowAcceptsConvergedCodeSave 验证已收敛的记录不阻塞恢复。
//
// 已核验确认未发生时允许继续：恢复不会被"看上去还有 pending"卡死，真正的副作用
// 仍由代码保存组件在执行前重新核验前置条件。
func TestResumeWorkflowAcceptsConvergedCodeSave(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryGateFixture(t, func(record *domain.CodeSaveRecord) {
		record.RecoveryStatus = domain.RecoveryStatusConfirmedNotApplied
	})
	err := fixture.resume(t)
	if err != nil {
		t.Fatalf("确认未发生后应允许恢复，实际 %v", err)
	}
	// 恢复确实启动了：执行记录不再停留在 blocked。
	stored, readErr := fixture.db.GetWorkflowRun(fixture.ctx, fixture.run.ID)
	if readErr != nil {
		t.Fatalf("读取执行记录失败: %v", readErr)
	}
	if stored.Status == domain.WorkflowRunBlocked {
		t.Fatal("允许恢复时执行记录必须离开 blocked")
	}
}

// TestCodeSaveRecoveryServiceReportsPendingRecords 验证启动扫描能发现未决记录。
//
// 扫描是只读的：它只追加一条可追溯事件，不重放 Git 操作，也不改写任务状态。
func TestCodeSaveRecoveryServiceReportsPendingRecords(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusDeveloping, false)
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		task.ID.String(),
		"container-1",
		"docker-container-1",
		"origin",
		"",
		"agent/"+task.ID.String(),
		"main",
		false,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	record.PersistStatus = domain.PersistStatusUnconfirmed
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	service, err := app.NewCodeSaveRecoveryService(app.CodeSaveRecoveryOptions{
		Store:  fixture.db,
		Logger: fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造恢复扫描服务失败: %v", err)
	}
	if err := service.Start(fixture.ctx); err != nil {
		t.Fatalf("扫描未决记录失败: %v", err)
	}
	events := fixture.events(t, task.ID)
	if countEvents(events, domain.EventTypeCodeSaveRecoveryRequired) != 1 {
		t.Fatalf("应写入 1 条恢复待处理事件，实际事件 %d 条", len(events))
	}
	// 扫描不推进任务状态。
	if recovered := fixture.task(t, task.ID); recovered.Status != domain.TaskStatusDeveloping {
		t.Fatalf("扫描不得推进任务状态，实际 %s", recovered.Status)
	}
	// 扫描不改写记录。
	stored, readErr := fixture.db.GetCodeSaveRecord(fixture.ctx, record.ID)
	if readErr != nil {
		t.Fatalf("读取代码保存记录失败: %v", readErr)
	}
	if stored.PushStatus != domain.PushStatusPending {
		t.Fatalf("扫描不得改写推送状态，实际 %s", stored.PushStatus)
	}
	if stored.RecoveryStatus != "" {
		t.Fatalf("扫描不得写入恢复结论，实际 %s", stored.RecoveryStatus)
	}
}

// TestCodeSaveRecoveryServiceIgnoresFinishedTasks 验证已结束任务不被扫描推进。
func TestCodeSaveRecoveryServiceIgnoresFinishedTasks(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusDeveloping, false)
	// 把任务收敛为取消：已结束的任务不再产生新的恢复事件。
	reloaded := fixture.task(t, task.ID)
	if err := reloaded.Transition(domain.TaskStatusCancelling, fixture.now); err != nil {
		t.Fatalf("任务迁移失败: %v", err)
	}
	if err := fixture.db.UpdateTask(fixture.ctx, reloaded); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	if err := reloaded.Finish(domain.TaskResultCancelled, "已取消", fixture.now); err != nil {
		t.Fatalf("结束任务失败: %v", err)
	}
	if err := reloaded.Transition(domain.TaskStatusCancelled, fixture.now); err != nil {
		t.Fatalf("任务迁移失败: %v", err)
	}
	if err := fixture.db.UpdateTask(fixture.ctx, reloaded); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		task.ID.String(),
		"container-1",
		"docker-container-1",
		"origin",
		"",
		"agent/"+task.ID.String(),
		"main",
		false,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	service, err := app.NewCodeSaveRecoveryService(app.CodeSaveRecoveryOptions{
		Store:  fixture.db,
		Logger: fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造恢复扫描服务失败: %v", err)
	}
	if err := service.Start(fixture.ctx); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if countEvents(fixture.events(t, task.ID), domain.EventTypeCodeSaveRecoveryRequired) != 0 {
		t.Fatal("已结束的任务不应产生恢复待处理事件")
	}
}

// containsErrorText 判断错误文本是否包含指定片段。
//
// 错误分类必须能从错误文本中读出：编排器把稳定分类与可读说明一起返回，因此调用方
// （HTTP Handler 与 CLI）不需要解析深层错误类型就能定位原因。
func containsErrorText(err error, fragment string) bool {
	if err == nil || fragment == "" {
		return false
	}
	return strings.Contains(err.Error(), fragment)
}
