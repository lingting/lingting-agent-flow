package workflow_test

import (
	"context"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/workflow"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"github.com/lingting/agentflow/tests/internal/workflowfake"
)

// newEngine 构造一个绑定到测试数据的工作流引擎。
func newEngine(
	t *testing.T,
	fixture *testkit.WorkflowFixture,
	ports *workflowfake.Ports,
) *workflow.Engine {
	t.Helper()
	// 会话元数据必须真实写入数据库：验证结果依赖会话外键。
	ports.Agents.Store = fixture.DB
	engine, err := workflow.NewEngine(workflow.Options{
		Store:       fixture.DB,
		Ports:       ports.Ports(),
		Owner:       "test-worker",
		MaxParallel: 4,
	})
	if err != nil {
		t.Fatalf("构造工作流引擎失败: %v", err)
	}
	return engine
}

// defaultPlan 是一份单步骤的合法计划。
func defaultPlan() string {
	return workflowfake.PlanJSON(
		"实现目标功能",
		workflowfake.PlanStep{
			Title:        "实现核心逻辑",
			Objective:    "提供可测试的实现",
			Instructions: "在现有代码上补充实现",
			Criteria:     "构建与测试通过",
		},
	)
}

// mustRunTask 推进任务并返回最终记录。
func mustRunTask(
	t *testing.T,
	fixture *testkit.WorkflowFixture,
	engine *workflow.Engine,
) (domain.Task, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runErr := engine.RunTask(ctx, fixture.Task.ID)
	return fixture.Reload(t, ctx), runErr
}

// TestEngineRejectsUnknownPorts 校验引擎拒绝缺少依赖的构造。
func TestEngineRejectsUnknownPorts(t *testing.T) {
	t.Parallel()
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	if _, err := workflow.NewEngine(workflow.Options{Store: fixture.DB}); err == nil {
		t.Fatal("缺少下层能力时构造应当失败")
	}
	ports := workflowfake.New()
	ports.CodeSaver = nil
	broken := workflow.Ports{
		Containers: ports.Containers,
		Source:     ports.Source,
		Agents:     ports.Agents,
		Commands:   ports.Commands,
	}
	if _, err := workflow.NewEngine(workflow.Options{Store: fixture.DB, Ports: broken}); err == nil {
		t.Fatal("缺少代码保存能力时构造应当失败")
	}
}

// TestRunTaskCompletesFullWorkflow 覆盖一次完整成功链路。
func TestRunTaskCompletesFullWorkflow(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	ports.Agents.SetReplies(
		domain.RoleTypeValidator,
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "实现符合验收条件")},
	)
	ports.Commands.Results = map[string]domain.CommandResult{
		"build": {ExitCode: 0, Certain: true},
	}
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Verification: domain.VerificationSpec{
			AcceptanceCriteria: []string{"构建通过"},
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
	})
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err != nil {
		t.Fatalf("推进任务失败: %v", err)
	}
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("任务状态应为 completed，实际: %s（%s）", task.Status, task.FailureSummary)
	}
	if task.FinalResult != domain.TaskResultSucceeded {
		t.Fatalf("任务结论应为 succeeded，实际: %s", task.FinalResult)
	}
	if task.FinishedAt == nil {
		t.Fatal("成功任务必须记录结束时间")
	}
	// 协调顺序：创建容器 → 获取代码 → 规划 → 开发 → 验证命令 → 辅助评估 → 代码保存 → 删除容器。
	calls := ports.Calls
	order := []string{
		"container.create",
		"source.fetch",
		"agent.run:planner",
		"agent.run:developer",
		"command.exec:build",
		"agent.run:validator",
		"codesaver.save",
		"container.dispose:delete",
	}
	lastIndex := -1
	for _, entry := range order {
		index := calls.Index(entry)
		if index < 0 {
			t.Fatalf("缺少调用 %s，实际顺序: %v", entry, calls.Snapshot())
		}
		if index <= lastIndex {
			t.Fatalf("调用顺序不符，%s 出现在 %d，期望在 %d 之后: %v",
				entry, index, lastIndex, calls.Snapshot())
		}
		lastIndex = index
	}
	if len(ports.Containers.Disposed) != 1 {
		t.Fatalf("成功任务应当删除受管理容器: %+v", ports.Containers.Disposed)
	}
	if ports.Containers.Disposed[0].Disposition != domain.RestartDispositionDelete {
		t.Fatalf("成功清理应使用 delete: %s", ports.Containers.Disposed[0].Disposition)
	}
}

// TestRequiredCommandFailureOverridesAgentPassed 校验必需检查失败不可被 Agent 覆盖。
func TestRequiredCommandFailureOverridesAgentPassed(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	ports.Agents.SetReplies(
		domain.RoleTypeValidator,
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "一切正常")},
	)
	ports.Commands.Results = map[string]domain.CommandResult{
		"test": {ExitCode: 1, Certain: true, Stderr: "FAIL: TestFoo"},
	}
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Retry: domain.RetryConfig{MaxAttempts: 1},
		Verification: domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "test", Command: "go", Args: []string{"test", "./..."}, Required: true},
			},
		},
	})
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("必需检查失败时任务不应成功")
	}
	if task.Status != domain.TaskStatusFailed {
		t.Fatalf("任务状态应为 failed，实际: %s", task.Status)
	}
	if task.FinalResult == domain.TaskResultSucceeded {
		t.Fatal("必需检查失败时任务结论不能为成功")
	}
	// 必需检查失败后不应调用辅助评估，也不应进入代码保存。
	if ports.Calls.Contains("agent.run:validator") {
		t.Fatalf("必需检查失败时应跳过辅助评估: %v", ports.Calls.Snapshot())
	}
	if ports.Calls.Contains("codesaver.save") {
		t.Fatalf("必需检查失败时不应保存代码: %v", ports.Calls.Snapshot())
	}
	if len(ports.Containers.Disposed) != 0 {
		t.Fatalf("失败任务默认保留容器现场: %+v", ports.Containers.Disposed)
	}
	ctx := context.Background()
	results := loadVerifications(t, fixture, ctx)
	if len(results) != 1 {
		t.Fatalf("验证结果数量不符: %d", len(results))
	}
	if results[0].Passed || results[0].Verdict != domain.VerificationVerdictFailed {
		t.Fatalf("必需检查失败时判定必须为 failed: %+v", results[0])
	}
	if !results[0].ReviewSkipped {
		t.Fatal("因必需检查失败跳过辅助评估时必须记录该事实")
	}
	if results[0].Assessment != nil {
		t.Fatal("跳过辅助评估时不应写入评估结果")
	}
}

// TestRetryCreatesNewAttemptsAndSessions 校验每次重试创建新的尝试与会话。
func TestRetryCreatesNewAttemptsAndSessions(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	ports.Agents.SetReplies(
		domain.RoleTypeDeveloper,
		workflowfake.AgentReply{
			Status:   agentFailedStatus(),
			Failure:  "开发调用失败",
			ExitCode: 1,
		},
		workflowfake.AgentReply{FinalText: "已完成实现"},
	)
	ports.Agents.SetReplies(
		domain.RoleTypeValidator,
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "实现符合验收条件")},
	)
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Retry: domain.RetryConfig{MaxAttempts: 3},
	})
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err != nil {
		t.Fatalf("推进任务失败: %v", err)
	}
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("重试后任务应成功，实际: %s（%s）", task.Status, task.FailureSummary)
	}
	ctx := context.Background()
	steps := developmentSteps(fixture.Steps(t, ctx))
	if len(steps) != 1 {
		t.Fatalf("开发步骤数量不符: %d", len(steps))
	}
	attempts := fixture.Attempts(t, ctx, steps[0].ID)
	if len(attempts) != 2 {
		t.Fatalf("重试必须创建新的尝试，实际数量: %d", len(attempts))
	}
	if attempts[0].Status != domain.AttemptStatusFailed {
		t.Fatalf("第一次尝试应保留失败记录: %s", attempts[0].Status)
	}
	if attempts[1].Status != domain.AttemptStatusCompleted {
		t.Fatalf("第二次尝试应成功: %s", attempts[1].Status)
	}
	if attempts[0].AttemptNumber != 1 || attempts[1].AttemptNumber != 2 {
		t.Fatalf("尝试序号必须递增: %+v", attempts)
	}
	// 每次 Agent 调用都有独立的 Session 标识与调用标识。
	recorded := ports.Agents.Recorded()
	if len(recorded) != 4 {
		t.Fatalf("Agent 调用次数不符: %d", len(recorded))
	}
	seen := make(map[string]struct{}, len(recorded))
	for _, invocation := range recorded {
		if invocation.Scope.StepID == nil || invocation.Scope.AttemptID == nil {
			t.Fatal("每次调用都必须带步骤与尝试归属")
		}
		key := invocation.Scope.AttemptID.String()
		if _, duplicated := seen[key]; duplicated && invocation.Role.Type == domain.RoleTypeDeveloper {
			t.Fatalf("同一尝试不应出现两次开发调用: %s", key)
		}
		seen[key] = struct{}{}
	}
}

// TestRetryLimitStopsTask 校验重试达到上限后任务失败。
func TestRetryLimitStopsTask(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	ports.Agents.SetReplies(
		domain.RoleTypeDeveloper,
		workflowfake.AgentReply{Status: agentFailedStatus(), Failure: "持续失败", ExitCode: 1},
		workflowfake.AgentReply{Status: agentFailedStatus(), Failure: "持续失败", ExitCode: 1},
	)
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Retry: domain.RetryConfig{MaxAttempts: 2},
	})
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("达到重试上限后任务应失败")
	}
	if task.Status != domain.TaskStatusFailed {
		t.Fatalf("任务状态应为 failed，实际: %s", task.Status)
	}
	ctx := context.Background()
	steps := developmentSteps(fixture.Steps(t, ctx))
	attempts := fixture.Attempts(t, ctx, steps[0].ID)
	if len(attempts) != 2 {
		t.Fatalf("尝试次数必须被上限约束，实际: %d", len(attempts))
	}
	if ports.Calls.Contains("codesaver.save") {
		t.Fatalf("失败任务不应保存代码: %v", ports.Calls.Snapshot())
	}
}

// TestPushSuccessButMergeConflictKeepsPushStatus 校验 Push 成功不被 Merge 冲突覆盖。
func TestPushSuccessButMergeConflictKeepsPushStatus(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	ports.Agents.SetReplies(
		domain.RoleTypeValidator,
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "实现符合验收条件")},
	)
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{MergeAfterPush: true})
	record := workflowfake.DefaultRecord(workflow.CodeSaveSpec{
		TaskID:         fixture.Task.ID.String(),
		ContainerID:    "container-1",
		BaseBranch:     "main",
		MergeAfterPush: true,
	})
	record.MergeStatus = domain.MergeStatusConflict
	record.MergeSHA = ""
	record.ErrorSummary = "合并冲突需要人工处理"
	ports.CodeSaver.Record = record
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("要求合并但合并冲突时任务不应完全成功")
	}
	if task.Status == domain.TaskStatusCompleted {
		t.Fatalf("合并冲突时任务不能标记为完成: %s", task.Status)
	}
	// 合并冲突属于需要人工处理的确定状态：任务不得报告成功，也不得被静默忽略。
	if task.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("合并冲突时应进入 recovery_required，实际: %s", task.Status)
	}
	// 已成功推送的事实必须保留：任务未成功不等于推送失败。
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("推送成功状态被覆盖: %s", record.PushStatus)
	}
	if record.MergeStatus != domain.MergeStatusConflict {
		t.Fatalf("合并冲突状态未保留: %s", record.MergeStatus)
	}
	if len(ports.Containers.Disposed) != 0 {
		t.Fatalf("失败任务应保留容器: %+v", ports.Containers.Disposed)
	}
}

// TestUnknownPushResultRequiresRecovery 校验推送结果未知时进入恢复状态。
func TestUnknownPushResultRequiresRecovery(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	ports.Agents.SetReplies(
		domain.RoleTypeValidator,
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "实现符合验收条件")},
	)
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	record := workflowfake.DefaultRecord(workflow.CodeSaveSpec{
		TaskID:      fixture.Task.ID.String(),
		ContainerID: "container-1",
		BaseBranch:  "main",
	})
	record.PushStatus = domain.PushStatusUnknown
	record.RemoteCommitSHA = ""
	ports.CodeSaver.Record = record
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("推送结果未知时任务不应成功")
	}
	if task.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("推送结果未知时应进入 recovery_required，实际: %s", task.Status)
	}
	if len(ports.Containers.Disposed) != 0 {
		t.Fatalf("结果未知时不得清理容器: %+v", ports.Containers.Disposed)
	}
}

// TestDetachedContainerIsNotTakenOver 校验已脱离管理的容器不会被自动接管。
func TestDetachedContainerIsNotTakenOver(t *testing.T) {
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
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("容器已脱离管理时任务不应继续推进")
	}
	if task.Status != domain.TaskStatusFailed {
		t.Fatalf("任务状态应为 failed，实际: %s", task.Status)
	}
	if ports.Calls.Contains("container.create") {
		t.Fatalf("不得为脱离管理的容器自动创建替代容器: %v", ports.Calls.Snapshot())
	}
	if ports.Calls.Contains("source.fetch") {
		t.Fatalf("容器不可用时不应继续获取代码: %v", ports.Calls.Snapshot())
	}
}

// TestCancelStopsWorkflowAndKeepsContainer 校验取消流程。
func TestCancelStopsWorkflowAndKeepsContainer(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	ports.Agents.SetReplies(
		domain.RoleTypeDeveloper,
		workflowfake.AgentReply{Status: agentCancelledStatus(), Failure: "调用方取消"},
	)
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Retry: domain.RetryConfig{MaxAttempts: 3},
	})
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("取消后推进应返回错误")
	}
	if task.Status != domain.TaskStatusCancelled {
		t.Fatalf("任务状态应为 cancelled，实际: %s", task.Status)
	}
	if task.FinalResult != domain.TaskResultCancelled {
		t.Fatalf("任务结论应为 cancelled，实际: %s", task.FinalResult)
	}
	if !task.CancelRequested {
		t.Fatal("取消必须持久化取消请求")
	}
	if len(ports.Containers.Disposed) != 0 {
		t.Fatalf("取消默认保留容器现场: %+v", ports.Containers.Disposed)
	}
	ctx := context.Background()
	steps := developmentSteps(fixture.Steps(t, ctx))
	if len(steps) != 1 {
		t.Fatalf("开发步骤数量不符: %d", len(steps))
	}
	attempts := fixture.Attempts(t, ctx, steps[0].ID)
	if len(attempts) != 1 {
		t.Fatalf("取消不应触发普通重试，尝试数量: %d", len(attempts))
	}
	if attempts[0].Status != domain.AttemptStatusCancelled {
		t.Fatalf("尝试状态应为 cancelled，实际: %s", attempts[0].Status)
	}
}

// TestSessionMissingIsRecordedHonestly 校验会话缺失被如实记录。
func TestSessionMissingIsRecordedHonestly(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.FailSessionRecord = true
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Retry: domain.RetryConfig{MaxAttempts: 1},
	})
	// 关闭辅助评估：本用例只关注会话缺失是否被如实记录。
	withoutValidator(t, fixture)
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err != nil {
		t.Fatalf("推进任务失败: %v", err)
	}
	ctx := context.Background()
	sessions := fixture.Sessions(t, ctx)
	if len(sessions) == 0 {
		t.Fatal("Agent 调用必须写入会话记录")
	}
	missing := 0
	for _, session := range sessions {
		if session.Completeness == domain.SessionCompletenessMissing {
			missing++
			if session.JSONLPath != "" {
				t.Fatalf("缺失会话不应记录 JSONL 路径: %+v", session)
			}
		}
	}
	if missing == 0 {
		t.Fatalf("会话缺失必须如实记录: %+v", sessions)
	}
	if task.Status != domain.TaskStatusCompleted && task.Status != domain.TaskStatusFailed {
		t.Fatalf("任务必须收敛到确定状态，实际: %s", task.Status)
	}
}

// TestPlanOutputMustBeStructured 校验 Planner 自由文本不会被当成计划。
func TestPlanOutputMustBeStructured(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(
		domain.RoleTypePlanner,
		workflowfake.AgentReply{FinalText: "我打算先读代码，然后改一改。"},
	)
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Retry: domain.RetryConfig{MaxAttempts: 1},
	})
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("非结构化计划不应被接受")
	}
	if task.Status != domain.TaskStatusFailed {
		t.Fatalf("任务状态应为 failed，实际: %s", task.Status)
	}
	if ports.Calls.Contains("agent.run:developer") {
		t.Fatalf("计划不可用时不应调用开发者: %v", ports.Calls.Snapshot())
	}
	ctx := context.Background()
	if steps := developmentSteps(fixture.Steps(t, ctx)); len(steps) != 0 {
		t.Fatalf("非法计划不应写入开发步骤: %d", len(steps))
	}
}

// TestPlanRejectsTooManySteps 校验计划步骤数上限。
func TestPlanRejectsTooManySteps(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	steps := make([]workflowfake.PlanStep, 0, 3)
	for index := 0; index < 3; index++ {
		steps = append(steps, workflowfake.PlanStep{
			Title:        "步骤",
			Objective:    "目标",
			Instructions: "要求",
			Criteria:     "验收",
		})
	}
	ports.Agents.SetReplies(
		domain.RoleTypePlanner,
		workflowfake.AgentReply{FinalText: workflowfake.PlanJSON("超出上限", steps...)},
	)
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Retry: domain.RetryConfig{MaxAttempts: 1},
	})
	ctx := context.Background()
	task := fixture.Reload(t, ctx)
	task.Config.MaxPlanSteps = 2
	if err := fixture.DB.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	engine := newEngine(t, fixture, ports)
	final, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("计划步骤数超过上限时应失败")
	}
	if final.Status != domain.TaskStatusFailed {
		t.Fatalf("任务状态应为 failed，实际: %s", final.Status)
	}
}

// TestCleanupFailureRequiresRecovery 校验清理失败不会被报告为完全成功。
func TestCleanupFailureRequiresRecovery(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: defaultPlan()})
	ports.Agents.SetReplies(
		domain.RoleTypeValidator,
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "实现符合验收条件")},
	)
	ports.Containers.DisposeErr = context.DeadlineExceeded
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{})
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("清理失败时推进应返回错误")
	}
	if task.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("清理失败时应进入 recovery_required，实际: %s", task.Status)
	}
	if task.Status == domain.TaskStatusCompleted {
		t.Fatal("清理失败时不能标记为完全成功")
	}
}

// TestAgentFailureKeepsContainer 校验 Agent 失败保留容器现场。
func TestAgentFailureKeepsContainer(t *testing.T) {
	t.Parallel()
	ports := workflowfake.New()
	ports.Agents.SetReplies(
		domain.RoleTypePlanner,
		workflowfake.AgentReply{
			Status:   agentFailedStatus(),
			Failure:  "规划进程异常退出",
			ExitCode: 2,
		},
	)
	fixture := testkit.NewWorkflowFixture(t, testkit.WorkflowOptions{
		Retry: domain.RetryConfig{MaxAttempts: 1},
	})
	engine := newEngine(t, fixture, ports)
	task, err := mustRunTask(t, fixture, engine)
	if err == nil {
		t.Fatal("规划失败时任务应失败")
	}
	if task.Status != domain.TaskStatusFailed {
		t.Fatalf("任务状态应为 failed，实际: %s", task.Status)
	}
	if len(ports.Containers.Disposed) != 0 {
		t.Fatalf("失败任务必须保留容器现场: %+v", ports.Containers.Disposed)
	}
	if !ports.Calls.Contains("source.fetch") {
		t.Fatalf("规划前应先获取代码: %v", ports.Calls.Snapshot())
	}
	if ports.Calls.Index("source.fetch") > ports.Calls.Index("agent.run:planner") {
		t.Fatalf("代码获取必须发生在规划之前: %v", ports.Calls.Snapshot())
	}
}
