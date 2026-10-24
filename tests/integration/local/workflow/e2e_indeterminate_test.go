// Package workflow_test 验证单任务端到端验收（P1-M5）的四类终局。
//
// 本文件覆盖场景 D：结果无法确认。
//
// 覆盖三条代表性不确定路径，全部复用现有领域语义，不为故障覆盖引入新抽象：
//   - 代码保存记录未能可靠落盘（code_save_unconfirmed）；
//   - 代码保存的推送结果未知（code_save_indeterminate）；
//   - 收尾执行记录写入失败（closeout_failed）。
//
// 三者都断言：任务不得被判为 completed、工作流进入 recovery_required、已发生的外部
// 副作用不被盲目重放、执行记录与错误分类足以解释原因、失败现场不被无条件删除。
package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// e2eIndeterminateReplies 返回一条"全部验证通过"的 Agent 调用序列。
//
// 不确定结果只发生在交付阶段：前面的步骤必须真实通过，才能证明"结果无法确认"是
// 交付阶段的独立结论，而不是被验证失败掩盖。
func e2eIndeterminateReplies() []e2eAgentReply {
	return e2eHappyReplies()
}

// TestE2EIndeterminateCodeSaveRecordUnconfirmed 验证保存记录未可靠落盘时不判成功。
func TestE2EIndeterminateCodeSaveRecordUnconfirmed(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eIndeterminateReplies(),
		codeSaverWrapper: func(saver *e2eCodeSaver) {
			// 推送已经完成，但"推送结论"这一步记录写入失败：Git 结果确定、
			// 我们对结果的记录不可信。
			saver.failPersistWhen = func(record domain.CodeSaveRecord) bool {
				return record.PushStatus != domain.PushStatusPending
			}
		},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("记录未确认时应需要人工核验，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.ErrorClass != "code_save_unconfirmed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	// 数据库中的执行记录同样是 recovery_required，不得出现 completed。
	stored := fixture.reloadRun(t, result.Run.RunID)
	if stored.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("数据库中的执行记录应为 recovery_required，实际 %s", stored.Status)
	}
	task := fixture.reloadTask(t)
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("记录未确认时任务不得标记为完成")
	}
	if task.FinalResult == domain.TaskResultSucceeded {
		t.Fatalf("记录未确认时任务结论不得为 succeeded: %s", task.FinalResult)
	}
	// 数据库里的记录停留在最后一次成功写入的状态，因此它不包含推送结论：这正是
	// "记录未确认"的现场——数据库事实不足以证明保存成功，不能被当作成功证据。
	records := fixture.codeSaves(t)
	if len(records) > 1 {
		t.Fatalf("记录未确认时不应留下多条保存记录，实际 %d 条", len(records))
	}
	for _, record := range records {
		if record.PushStatus == domain.PushStatusSucceeded {
			t.Fatalf("记录写入失败时数据库不得出现推送成功的结论: %s", record.PushStatus)
		}
	}
	// 外部副作用不得被重放：推送命令只执行一次。
	if pushes := fixture.ports.gitPushCount(); pushes != 1 {
		t.Fatalf("结果不确定时不得重复推送，实际 %d 次", pushes)
	}
	// 失败现场必须保留：容器与任务关联都不被删除。
	assertE2EScenePreserved(t, fixture)
}

// TestE2EIndeterminateCodeSavePushUnknown 验证推送结果未知时不判成功也不重放。
func TestE2EIndeterminateCodeSavePushUnknown(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eIndeterminateReplies(),
		codeSaverWrapper: func(saver *e2eCodeSaver) {
			// 推送命令超时：远程可能已经接受该提交，因此结果必须按 unknown 处理。
			saver.pushUncertain = true
		},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("推送结果未知时应需要人工核验，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.ErrorClass != "code_save_indeterminate" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	stored := fixture.reloadRun(t, result.Run.RunID)
	if stored.Status == domain.WorkflowRunCompleted {
		t.Fatal("数据库中的执行记录不得是 completed")
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("推送结果未知时任务不得标记为完成")
	}
	// 唯一的保存记录必须留下 unknown 与可诊断原因，供人工核验使用。
	records := fixture.codeSaves(t)
	if len(records) != 1 {
		t.Fatalf("应留下 1 条保存记录供核验，实际 %d 条", len(records))
	}
	if records[0].PushStatus != domain.PushStatusUnknown {
		t.Fatalf("保存记录的推送状态应为 unknown，实际 %s", records[0].PushStatus)
	}
	if records[0].ErrorSummary == "" {
		t.Fatal("结果未知时必须留下可诊断的错误摘要")
	}
	// 不得盲目重放可能已生效的推送。
	if pushes := fixture.ports.gitPushCount(); pushes != 1 {
		t.Fatalf("结果未知时不得重复推送，实际 %d 次", pushes)
	}
	assertE2EScenePreserved(t, fixture)
}

// TestE2EIndeterminateCloseoutWriteFails 验证收尾记录写入失败不被报告为成功。
func TestE2EIndeterminateCloseoutWriteFails(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eIndeterminateReplies(),
		storeWrapper: func(store app.TaskWorkflowStore) app.TaskWorkflowStore {
			// 只让"收尾后写入 completed"这一步失败：阶段推进与任务状态写入成功，
			// 从而使"任务已置为 completed 但执行记录最终写入失败"这一现场真实成立。
			return &e2eFailingStore{
				TaskWorkflowStore:    store,
				failUpdateWhenStatus: domain.WorkflowRunCompleted,
			}
		},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("收尾记录写入失败时应需要人工核验，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.ErrorClass != "closeout_failed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	// 数据库中的执行记录同样不得是 completed：内存终态不能替代数据库事实。
	stored := fixture.reloadRun(t, result.Run.RunID)
	if stored.Status == domain.WorkflowRunCompleted {
		t.Fatal("执行记录最终写入失败时数据库中不得出现 completed")
	}
	if stored.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("数据库中的执行记录应为 recovery_required，实际 %s", stored.Status)
	}
	// 任务状态可能已经被推进到 completed（写入发生在记录之前），但工作流结论必须
	// 如实标注为需要人工核验，不能对外宣布任务完整成功。
	if value, err := fixture.workflow.GetWorkflowStatus(
		fixture.ctx,
		fixture.task.ID.String(),
	); err != nil || value.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("查询结果同样必须报告需要人工核验: %v", err)
	}
	assertE2EScenePreserved(t, fixture)
}

// TestE2EIndeterminateCodeSaveError 验证代码保存明确报错时停在可恢复状态。
func TestE2EIndeterminateCodeSaveError(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eIndeterminateReplies(),
		codeSaverWrapper: func(saver *e2eCodeSaver) {
			saver.saveErr = errors.New("容器内 Git 执行失败")
		},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("代码保存明确失败时应阻塞，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.ErrorClass != "code_save_failed" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("代码保存失败时任务不得标记为完成")
	}
	// 步骤本身确实通过验证：失败来自交付阶段而不是验证阶段。
	for _, step := range fixture.reloadSteps(t) {
		if step.Status != domain.StepStatusCompleted {
			t.Fatalf("步骤 %d 应已完成验证，实际 %s", step.Sequence, step.Status)
		}
	}
	assertE2EScenePreserved(t, fixture)
}

// TestE2EIndeterminateWithoutCodeSaver 验证缺少代码保存能力时不判成功。
func TestE2EIndeterminateWithoutCodeSaver(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		// 不装配代码保存能力：步骤仍会全部执行并验证，但任务不能判定为完整成功。
		agentReplies: e2eIndeterminateReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunBlocked {
		t.Fatalf("缺少代码保存能力时应阻塞，实际 %s", formatE2ERun(result.Run))
	}
	if result.Run.ErrorClass != "code_saver_unavailable" {
		t.Fatalf("错误分类不正确: %s", result.Run.ErrorClass)
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("缺少代码保存能力时任务不得标记为完成")
	}
	assertE2EScenePreserved(t, fixture)
}

// assertE2EScenePreserved 断言失败现场没有被无条件删除。
func assertE2EScenePreserved(t *testing.T, fixture *e2eFixture) {
	t.Helper()
	// 任务仍然关联原容器。
	task := fixture.reloadTask(t)
	if task.ContainerID != e2eContainerID {
		t.Fatalf("失败后任务必须保留容器关联，实际 %q", task.ContainerID)
	}
	// 容器仍处于受管理运行态，未被自动删除或脱离管理。
	container, err := fixture.db.GetContainer(fixture.ctx, domain.ID(e2eContainerID))
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if container.State != domain.ContainerStateRunning {
		t.Fatalf("失败后容器应仍处于受管理运行态，实际 %s", container.State)
	}
	if container.Detached {
		t.Fatal("失败后不得自动脱离容器管理")
	}
	// 执行记录、会话与验证证据全部保留。
	if len(fixture.reloadSteps(t)) == 0 {
		t.Fatal("失败现场必须保留计划步骤")
	}
	if len(fixture.sessionsOf(t)) == 0 {
		t.Fatal("失败现场必须保留会话记录")
	}
}

// e2eFailingStore 包装真实存储，只把指定终态的写入替换为可控失败。
type e2eFailingStore struct {
	app.TaskWorkflowStore
	// failUpdateWhenStatus 非空时只让写入该目标状态的尝试失败。
	failUpdateWhenStatus domain.WorkflowRunStatus
}

// UpdateWorkflowRun 在目标状态匹配时返回错误。
//
// 失败来自存储层而不是业务代码：验证的是真实的错误传播与"不得把收尾报告为完成"
// 的语义。
func (s *e2eFailingStore) UpdateWorkflowRun(
	ctx context.Context,
	run domain.WorkflowRun,
) error {
	if s.failUpdateWhenStatus != "" && run.Status == s.failUpdateWhenStatus {
		return errors.New("数据库写入失败")
	}
	return s.TaskWorkflowStore.UpdateWorkflowRun(ctx, run)
}

// TestE2EIndeterminateDoesNotResumeWithoutVerification 验证未核验的未决副作用阻止恢复。
//
// 收尾停在 recovery_required 之后，任务并不自动继续。恢复入口必须先读持久化记录：
// 只要仍有结果不明的 commit / push / merge 且尚未核验，就拒绝推进并给出可定位原因。
// 这保证"结果不确定"不会被一次恢复请求悄悄变成"重放一次外部副作用"。
func TestE2EIndeterminateDoesNotResumeWithoutVerification(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eIndeterminateReplies(),
		codeSaverWrapper: func(saver *e2eCodeSaver) {
			saver.pushUncertain = true
		},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("推送结果未知时应需要人工核验，实际 %s", formatE2ERun(result.Run))
	}
	pushesBefore := fixture.ports.gitPushCount()
	// 直接请求恢复：未核验的记录必须阻止推进。
	resumed, err := fixture.workflow.ResumeWorkflow(fixture.ctx, app.ResumeWorkflowRequest{
		TaskID: fixture.task.ID.String(),
		RunID:  result.Run.RunID.String(),
	})
	if err == nil {
		t.Fatalf("未核验的未决副作用必须阻止恢复，实际返回状态 %s", resumed.Status)
	}
	if !errors.Is(err, app.ErrWorkflowPrerequisite) &&
		!errors.Is(err, app.ErrWorkflowConflict) {
		t.Fatalf("恢复应被明确拒绝，实际 %v", err)
	}
	// 执行记录仍停在 recovery_required，没有被恢复请求改写。
	stored := fixture.reloadRun(t, result.Run.RunID)
	if stored.Status != domain.WorkflowRunRecoveryRequired {
		t.Fatalf("拒绝恢复不得改写执行记录，实际 %s", stored.Status)
	}
	// 外部副作用没有被重放。
	if pushes := fixture.ports.gitPushCount(); pushes != pushesBefore {
		t.Fatalf("拒绝恢复时不得重放推送，实际从 %d 变为 %d 次", pushesBefore, pushes)
	}
	if task := fixture.reloadTask(t); task.Status == domain.TaskStatusCompleted {
		t.Fatal("结果未知时任务不得标记为完成")
	}
	assertE2EScenePreserved(t, fixture)
}
