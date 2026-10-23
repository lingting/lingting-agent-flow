// Package workflow_test 验证单任务端到端验收（P1-M5）的四类终局。
//
// 本文件覆盖场景 A：完整成功。
//
// 链路固定为：任务创建 → 统一启动入口 → 执行环境与工作区准备 → Planner 生成并
// 持久化计划 → 工作流启动 → Developer 执行 → 必需确定性检查 → Validator Agent
// 语义审查 → 代码保存 → 会话归档核验 → 领域收尾决策 → 任务与工作流终态持久化。
//
// 断言对象是数据库里的真实记录：Task、WorkflowRun、Step、Attempt、Session、
// ValidationRun、CodeSaveRecord 与事件，而不是方法的返回值。
package workflow_test

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
)

// e2eHappyReplies 返回成功路径的 Agent 调用序列。
//
// 调用顺序由生产编排决定：Planner 生成计划后，计划中的规划阶段步骤与每个开发
// 步骤都会各自执行一次 Developer 调用与一次 Validator 语义审查调用。
func e2eHappyReplies() []e2eAgentReply {
	return []e2eAgentReply{
		// Planner 生成计划。
		{status: agent.RunCompleted, finalText: e2ePlanJSON},
		// 计划中的规划阶段步骤（plan）。
		{status: agent.RunCompleted, finalText: "计划已生成"},
		{status: agent.RunCompleted, finalText: e2eValidatorPassJSON},
		// 第 1 个开发步骤。
		{status: agent.RunCompleted, finalText: "实现完成"},
		{status: agent.RunCompleted, finalText: e2eValidatorPassJSON},
		// 第 2 个开发步骤。
		{status: agent.RunCompleted, finalText: "测试补齐"},
		{status: agent.RunCompleted, finalText: e2eValidatorPassJSON},
	}
}

// TestE2EHappyPathReachesCompleted 验证一条任务从创建到交付成功。
func TestE2EHappyPathReachesCompleted(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eHappyReplies(),
	})
	// Developer 在真实工作仓库里产生改动，使代码保存有可提交内容。
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")

	launched := fixture.launchTask(t)
	if !launched.PlanPersisted {
		t.Fatalf("统一启动必须持久化计划: %+v", launched)
	}
	if !launched.WorkflowStarted {
		t.Fatalf("计划就绪后必须启动工作流: %+v", launched)
	}
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("成功路径应完成，实际 %s", formatE2ERun(result.Run))
	}
	// 数据库是唯一真相来源：任务与执行记录的终态都必须真实落盘。
	storedRun := fixture.reloadRun(t, result.Run.RunID)
	if storedRun.Status != domain.WorkflowRunCompleted {
		t.Fatalf("数据库中的执行记录应为 completed，实际 %s", storedRun.Status)
	}
	task := fixture.reloadTask(t)
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("任务应为 completed，实际 %s（%s）", task.Status, task.FailureSummary)
	}
	if task.FinalResult != domain.TaskResultSucceeded {
		t.Fatalf("任务最终结论应为 succeeded，实际 %s", task.FinalResult)
	}
	if task.CurrentStepID != nil || task.CurrentAttemptID != nil {
		t.Fatal("完成的任务必须释放执行占用")
	}
}

// TestE2EHappyPathPersistsPlanAndWorkspace 验证启动阶段留下的持久化事实。
func TestE2EHappyPathPersistsPlanAndWorkspace(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eHappyReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	fixture.settle(t)

	// 计划步骤必须真实落库：序号 1 是规划阶段，其余是开发步骤，且都带必需检查。
	steps := fixture.reloadSteps(t)
	if len(steps) != 3 {
		t.Fatalf("计划应有 1 个规划阶段与 2 个开发步骤，实际 %d 个", len(steps))
	}
	if steps[0].Type != domain.StepTypePlan {
		t.Fatalf("首个步骤必须是规划阶段，实际 %s", steps[0].Type)
	}
	for index, step := range steps {
		if step.Sequence != index+1 {
			t.Fatalf("步骤序号不连续: %d", step.Sequence)
		}
		if step.Status != domain.StepStatusCompleted {
			t.Fatalf("步骤 %d 应为 completed，实际 %s", step.Sequence, step.Status)
		}
		if step.CurrentAttemptID != nil {
			t.Fatalf("步骤 %d 完成后必须释放执行占用", step.Sequence)
		}
		if !step.VerificationSpec.HasCommands() {
			t.Fatalf("步骤 %d 缺少任务级必需命令", step.Sequence)
		}
	}
	// 工作区初始化必须留下成功记录，并关联当前容器与可核验 commit。
	records := fixture.workspaceInits(t)
	if len(records) != 1 {
		t.Fatalf("工作区初始化记录数量不符: %d", len(records))
	}
	if records[0].Status != domain.TaskWorkspaceInitStatusSucceeded {
		t.Fatalf("工作区初始化应成功，实际 %s", records[0].Status)
	}
	if records[0].ContainerID != e2eContainerID {
		t.Fatalf("工作区记录未关联当前容器: %s", records[0].ContainerID)
	}
	if records[0].CommitSHA == "" {
		t.Fatal("工作区初始化成功时必须给出可核验 commit")
	}
	inspect, fetch, _, _ := fixture.ports.counts()
	if inspect == 0 {
		t.Fatal("启动必须核验容器可用性")
	}
	if fetch != 1 {
		t.Fatalf("工作区初始化应执行一次代码获取，实际 %d 次", fetch)
	}
}

// TestE2EHappyPathPersistsExecutionRecords 验证执行记录四层关系与语义审查落库。
func TestE2EHappyPathPersistsExecutionRecords(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eHappyReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("成功路径应完成，实际 %s", formatE2ERun(result.Run))
	}

	steps := fixture.reloadSteps(t)
	// 每个可自动推进的步骤恰好产生一次 Attempt，并各自绑定一条 Session。
	sessionIDs := make(map[domain.ID]struct{}, len(steps))
	for _, step := range steps {
		attempts := fixture.attempts(t, step.ID)
		if len(attempts) != 1 {
			t.Fatalf("步骤 %d 应产生 1 次尝试，实际 %d", step.Sequence, len(attempts))
		}
		attempt := attempts[0]
		if attempt.TaskID != fixture.task.ID {
			t.Fatalf("尝试未关联任务: %s", attempt.TaskID)
		}
		if attempt.AttemptNumber != 1 {
			t.Fatalf("尝试序号应从 1 开始，实际 %d", attempt.AttemptNumber)
		}
		if len(attempt.SessionIDs) != 1 {
			t.Fatalf("尝试 %s 应关联 1 条会话，实际 %d", attempt.ID, len(attempt.SessionIDs))
		}
		sessionIDs[attempt.SessionIDs[0]] = struct{}{}
		// 每次尝试都必须留下独立的 Validation Run，并绑定该轮 Attempt。
		runs := fixture.validationRuns(t, attempt.ID)
		if len(runs) != 1 {
			t.Fatalf("尝试 %s 应有 1 条验证记录，实际 %d", attempt.ID, len(runs))
		}
		run := runs[0]
		if run.StepID != step.ID || run.TaskID != fixture.task.ID {
			t.Fatalf("验证记录归属不正确: %s / %s", run.StepID, run.TaskID)
		}
		if run.Status != domain.ValidationRunStatusCompleted {
			t.Fatalf("验证流程应为 completed，实际 %s", run.Status)
		}
		if run.Verdict != domain.VerificationVerdictPassed {
			t.Fatalf("验证结论应为 passed，实际 %s（%s）", run.Verdict, run.FailureSummary)
		}
		// 语义审查必须真实进入生产决策链并落库：结论由确定性检查 + 审查共同计算。
		if run.ReviewStatus != domain.ReviewStatusPassed {
			t.Fatalf("语义审查应为 passed，实际 %s（%s）", run.ReviewStatus, run.ReviewSummary)
		}
		if run.ValidatorSessionID == nil || run.ValidatorSessionID.IsEmpty() {
			t.Fatal("语义审查必须留下独立的 Validator 会话标识")
		}
		sessionIDs[*run.ValidatorSessionID] = struct{}{}
	}
	// 会话必须真实落库、已归档，并与执行记录一一对应。
	//
	// 计划生成是一次任务级调用，它同样留下独立会话：因此预期会话 = 1 条 Planner
	// 会话 + 每个可执行步骤的 Developer 与 Validator 会话。
	sessions := fixture.sessionsOf(t)
	if len(sessions) != len(sessionIDs)+1 {
		t.Fatalf("会话数量与执行记录不一致: %d != %d+1", len(sessions), len(sessionIDs))
	}
	plannerSessions := 0
	for _, session := range sessions {
		if session.TaskID != fixture.task.ID {
			t.Fatalf("会话归属不正确: %s", session.TaskID)
		}
		if session.StepID == nil && session.AttemptID == nil {
			// 任务级会话只能来自计划生成。
			plannerSessions++
			if session.RoleSnapshot.Type != domain.RoleTypePlanner {
				t.Fatalf("任务级会话必须来自规划角色，实际 %s", session.RoleSnapshot.Type)
			}
		} else if _, ok := sessionIDs[session.ID]; !ok {
			t.Fatalf("会话 %s 未出现在任何执行记录中", session.ID)
		}
		if session.ArchiveStatus != domain.SessionArchiveSucceeded {
			t.Fatalf("会话 %s 归档状态不符: %s", session.ID, session.ArchiveStatus)
		}
		if session.ArchivePath == "" {
			t.Fatalf("会话 %s 归档成功时必须给出归档位置", session.ID)
		}
		if session.Completeness != domain.SessionCompletenessComplete {
			t.Fatalf("会话 %s 完整性不符: %s", session.ID, session.Completeness)
		}
	}
	if plannerSessions != 1 {
		t.Fatalf("计划生成应留下 1 条任务级会话，实际 %d", plannerSessions)
	}
}

// TestE2EHappyPathSavesCodeToRemote 验证代码保存确实提交并推送了任务分支。
//
// 代码保存走真实 Git：提交与推送都在测试工作空间的真实仓库上完成，因此断言的是
// 真实产生的 commit 与真实推送后的远程引用，而不是替身返回的字段。
func TestE2EHappyPathSavesCodeToRemote(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eHappyReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("成功路径应完成，实际 %s", formatE2ERun(result.Run))
	}

	records := fixture.codeSaves(t)
	if len(records) != 1 {
		t.Fatalf("成功路径应产生 1 条代码保存记录，实际 %d", len(records))
	}
	record := records[0]
	if record.TaskID != fixture.task.ID.String() {
		t.Fatalf("代码保存记录未关联任务: %s", record.TaskID)
	}
	if record.ContainerID != e2eContainerID {
		t.Fatalf("代码保存记录未关联容器: %s", record.ContainerID)
	}
	if record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交状态应为 succeeded，实际 %s", record.CommitStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("推送状态应为 succeeded，实际 %s", record.PushStatus)
	}
	if record.PersistStatus.IsUnconfirmed() {
		t.Fatalf("保存记录必须已可靠落盘，实际 %s", record.PersistStatus)
	}
	// 默认不合并：开启代码保存本身不代表同意合并主分支。
	if record.MergeEnabled {
		t.Fatal("默认不应请求合并基础分支")
	}
	if record.MergeStatus != domain.MergeStatusNotRequested {
		t.Fatalf("默认合并状态应为 not_requested，实际 %s", record.MergeStatus)
	}
	// 真实 Git 事实：工作仓库 HEAD 与远程任务分支都指向记录中的 commit。
	if head := fixture.repo.rev(t, "HEAD"); head != record.CommitSHA {
		t.Fatalf("工作仓库 HEAD 应为 %s，实际 %s", record.CommitSHA, head)
	}
	remote := fixture.repo.bareRev(t, "refs/heads/"+record.TaskBranch)
	if remote != record.CommitSHA {
		t.Fatalf("远程任务分支应为 %s，实际 %s", record.CommitSHA, remote)
	}
	// 未请求合并时基础分支不得包含任务提交。
	if base := fixture.repo.bareRev(t, "refs/heads/"+e2eBaseBranch); base == record.CommitSHA {
		t.Fatal("未请求合并时基础分支不应包含任务提交")
	}
}

// TestE2EHappyPathLeavesAuditEvents 验证端到端链路留下可复核的事件时间线。
func TestE2EHappyPathLeavesAuditEvents(t *testing.T) {
	t.Parallel()
	fixture := newE2EFixture(t, e2eOptions{
		codeSaver:    true,
		agentReplies: e2eHappyReplies(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.launchTask(t)
	result := fixture.settle(t)
	if result.Run.Status != domain.WorkflowRunCompleted {
		t.Fatalf("成功路径应完成，实际 %s", formatE2ERun(result.Run))
	}
	// 事件在状态写入之后追加：按截止时间轮询，而不是假定"状态可见即事件已写入"。
	fixture.waitEvents(t,
		domain.EventTypeTaskStarted,
		domain.EventTypePlanCreated,
		domain.EventTypeWorkflowStarted,
		domain.EventTypeStepStarted,
		domain.EventTypeVerificationPassed,
		domain.EventTypeStepCompleted,
		domain.EventTypeWorkflowStepPassed,
		domain.EventTypeWorkflowCodeSaved,
		domain.EventTypeWorkflowCompleted,
	)
	steps := fixture.reloadSteps(t)
	// 修复流程不应被无故触发：每轮闭环都会记录一条 repair.round 事件，但成功路径
	// 只有 1 轮，且该轮必须是"验证通过、结束循环"的判定。
	repairRounds := 0
	for _, event := range fixture.events(t) {
		if event.Type != domain.EventTypeRepairRound {
			continue
		}
		repairRounds++
		payload := string(event.Payload)
		if !strings.Contains(payload, `"round":1`) {
			t.Fatalf("成功路径只应有第 1 轮修复判定，实际事件负载: %s", payload)
		}
	}
	if repairRounds != len(steps) {
		t.Fatalf("每个可执行步骤应记录 1 轮修复判定，实际 %d != %d", repairRounds, len(steps))
	}
	// Agent 调用次数必须与"每个步骤一次开发 + 一次审查"完全吻合。
	_, _, agentCalls, checkCalls := fixture.ports.counts()
	if agentCalls != len(e2eHappyReplies()) {
		t.Fatalf("Agent 调用次数不符: %d != %d", agentCalls, len(e2eHappyReplies()))
	}
	if checkCalls != 3 {
		t.Fatalf("确定性检查应执行 3 次（每个可执行步骤一次），实际 %d", checkCalls)
	}
}
