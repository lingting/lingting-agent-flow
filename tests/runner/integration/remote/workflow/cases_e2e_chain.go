// 本文件是 tests/integration/remote/workflow/e2e_chain_test.go 中用例与断言辅助的迁移结果
// （1 条用例）。
//
// 改造点：函数头改成 func run*、工作空间与数据库取编排进程下发的资源、包级容器终检改为
// 用例级终检；断言与远程操作保持原样。本用例按来源 go test 的写法本来就不并行。
package workflow

import (
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记真实远程全链路 1 条用例。
var _ = register("e2e-full-chain", workflowCaseTimeout, runRemoteE2EFullChain)

// 原 TestRemoteE2EFullChain 在真实远程环境执行完整链路，覆盖成功与失败两类终局。
//
// 断言只依赖持久化事实与领域规则，不假设模型输出特定文本：真实模型可能生成不同
// 的计划或代码，但"计划必须落库""状态迁移必须合法""失败必须如实收敛"这些事实
// 与模型输出无关。
func runRemoteE2EFullChain(t *testrunner.Context) {
	fixture := newE2EFixture(t)
	fixture.prepareEnvironmentVersion(t)

	launched, err := fixture.launch.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID:      fixture.task.ID.String(),
		PlanTimeout: e2eOpTimeout,
		Timeout:     e2eOpTimeout,
	})
	if err != nil {
		// 真实链路失败必须带可定位的阶段信息，而不是笼统报错。
		fixture.dumpFailure(t)
		t.Fatalf("真实远程统一启动失败（阶段 %s，状态 %s）: %v",
			launched.FailureStage, launched.TaskStatus, err)
	}
	if !launched.PlanPersisted {
		t.Fatalf("真实远程启动必须持久化计划: %+v", launched)
	}
	if !launched.WorkflowStarted {
		t.Fatalf("计划就绪后必须启动工作流: %+v", launched)
	}
	t.Logf("LAUNCH stage=%s status=%s container=%s commit=%s steps=%d",
		launched.Stage, launched.TaskStatus, launched.ContainerID,
		launched.WorkspaceCommit, len(launched.PlanSteps))

	// 计划必须真实落库，且首个步骤是规划阶段。
	steps := fixture.steps(t)
	if len(steps) < 2 {
		t.Fatalf("真实计划应至少包含规划阶段与一个开发步骤，实际 %d 个", len(steps))
	}
	if steps[0].Type != domain.StepTypePlan {
		t.Fatalf("首个步骤必须是规划阶段，实际 %s", steps[0].Type)
	}
	for index, step := range steps {
		if step.Sequence != index+1 {
			t.Fatalf("步骤序号不连续: %d", step.Sequence)
		}
		// 规划阶段只负责规划：任务级代码验收命令只能在 Developer 产出之后成立，
		// 因此它不得出现在规划阶段步骤上。
		if step.Type == domain.StepTypePlan {
			if step.VerificationSpec.HasCommands() {
				t.Fatalf("规划阶段步骤不得携带代码验证命令: %v", step.VerificationSpec.Commands)
			}
			continue
		}
		// 任务级必需检查必须完整合并进每一个开发步骤：Planner 不能通过省略命令取消它。
		command, ok := findVerificationCommand(step, e2eRequiredCheckName)
		if !ok {
			t.Fatalf("开发步骤 %d 缺少任务级必需命令 %s", step.Sequence, e2eRequiredCheckName)
		}
		if !command.Required {
			t.Fatalf("开发步骤 %d 的 %s 检查必须是必需检查", step.Sequence, e2eRequiredCheckName)
		}
		if !hasRequiredVerificationCommand(step) {
			t.Fatalf("开发步骤 %d 缺少可执行的必需检查", step.Sequence)
		}
	}
	// 容器必须真实创建并关联到任务。
	stored, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if strings.TrimSpace(stored.ContainerID) == "" {
		t.Fatal("真实远程启动必须把容器关联写回任务")
	}
	container, err := fixture.db.GetContainer(fixture.ctx, domain.ID(stored.ContainerID))
	if err != nil {
		t.Fatalf("读取容器记录失败: %v", err)
	}
	if container.DockerID == "" {
		t.Fatal("容器记录必须带有真实 Docker 容器 ID")
	}
	t.Logf("REMOTE_CONTAINER=%s DOCKER_ID=%s", container.ID, container.DockerID)

	// 工作流由编排器在后台推进：等待它停止自动推进后核对终态。
	result := fixture.settle(t)
	t.Logf("WORKFLOW status=%s stage=%s class=%s summary=%s",
		result.Run.Status, result.Run.Stage, result.Run.ErrorClass, result.Run.ErrorSummary)

	// 无论成功或失败，执行记录必须真实落库，且状态必须是领域规则允许的取值。
	storedRun, err := fixture.db.GetWorkflowRun(fixture.ctx, result.Run.RunID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if storedRun.Status != result.Run.Status {
		t.Fatalf("数据库中的执行记录状态与返回值不一致: %s != %s",
			storedRun.Status, result.Run.Status)
	}
	if storedRun.Status == domain.WorkflowRunRunning ||
		storedRun.Status == domain.WorkflowRunPending {
		t.Fatalf("执行记录不得停留在未收敛状态: %s", storedRun.Status)
	}
	// 步骤必须都已收敛：不存在仍在执行占用的步骤。
	for _, step := range fixture.steps(t) {
		switch step.Status {
		case domain.StepStatusRunning, domain.StepStatusDeveloping, domain.StepStatusVerifying:
			t.Fatalf("步骤 %d 仍处于执行占用 %s，工作流不得报告已停止",
				step.Sequence, step.Status)
		}
	}
	// 只有全部必需条件满足时才允许完成；否则必须给出可解释的停止原因。
	finalTask, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	t.Logf("FINAL_TASK status=%s result=%s failure=%s",
		finalTask.Status, finalTask.FinalResult, finalTask.FailureSummary)
	// 分阶段证据必须先于终局分支核对：无论终局是 completed 还是停在交付阶段，
	// 规划、开发、确定性验证与语义审查都必须真实留下记录。
	fixture.assertStageEvidence(t, container.ID.String())
	if storedRun.Status == domain.WorkflowRunCompleted {
		if finalTask.Status != domain.TaskStatusCompleted {
			t.Fatalf("工作流完成时任务必须为 completed，实际 %s", finalTask.Status)
		}
		if finalTask.FinalResult != domain.TaskResultSucceeded {
			t.Fatalf("完成的任务结论必须为 succeeded，实际 %s", finalTask.FinalResult)
		}
		// 真实交付必须留下代码保存记录与已归档会话。
		records, err := fixture.db.ListCodeSaveRecordsByTask(
			fixture.ctx, fixture.task.ID.String(),
		)
		if err != nil {
			t.Fatalf("读取代码保存记录失败: %v", err)
		}
		if len(records) == 0 {
			t.Fatal("工作流完成时必须留下代码保存记录")
		}
		record := records[len(records)-1]
		if record.PushStatus != domain.PushStatusSucceeded {
			t.Fatalf("代码保存必须推送成功，实际 %s（%s）",
				record.PushStatus, record.ErrorSummary)
		}
		if record.CommitSHA == "" {
			t.Fatal("代码保存记录必须带有真实 commit SHA")
		}
		t.Logf("CODE_SAVE commit=%s push=%s remote=%s branch=%s",
			record.CommitSHA, record.PushStatus, record.RemoteCommitSHA, record.TaskBranch)
		assertDeliveryRecord(t, record, fixture.task.ID)
		if record.RemoteCommitSHA == "" || record.RemoteCommitSHA != record.CommitSHA {
			t.Fatalf("推送成功后远程任务分支必须指向本次提交: remote=%q commit=%q",
				record.RemoteCommitSHA, record.CommitSHA)
		}
		sessions, err := fixture.db.ListSessionsByTask(fixture.ctx, fixture.task.ID)
		if err != nil {
			t.Fatalf("读取会话失败: %v", err)
		}
		if len(sessions) == 0 {
			t.Fatal("工作流完成时必须留下会话记录")
		}
		for _, session := range sessions {
			if session.ArchiveStatus != domain.SessionArchiveSucceeded {
				t.Fatalf("会话 %s 必须归档成功，实际 %s", session.ID, session.ArchiveStatus)
			}
		}
		t.Logf("SESSIONS=%d 全部已归档", len(sessions))
		return
	}
	// 未完成时必须停在领域规则允许的停止状态，并给出可解释的原因。
	switch storedRun.Status {
	case domain.WorkflowRunBlocked, domain.WorkflowRunRecoveryRequired:
	default:
		t.Fatalf("未完成的工作流必须停在 blocked 或 recovery_required，实际 %s",
			storedRun.Status)
	}
	if finalTask.Status == domain.TaskStatusCompleted {
		t.Fatal("工作流未完成时任务不得标记为完成")
	}
	if strings.TrimSpace(storedRun.ErrorClass) == "" {
		t.Fatal("未完成的工作流必须给出可诊断的错误分类")
	}
	// 停在交付阶段是可解释的真实终局：前面的开发与验证确实全部通过，只有代码保存
	// 未成功。把这类停止与"开发/验证阶段失败"混为一谈会掩盖真实链路已经走通的事实。
	records, err := fixture.db.ListCodeSaveRecordsByTask(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("停在交付阶段时必须留下代码保存记录，实际分类 %s", storedRun.ErrorClass)
	}
	record := records[len(records)-1]
	if record.CommitSHA == "" {
		t.Fatalf("代码保存记录必须带有真实 commit SHA（分类 %s）", storedRun.ErrorClass)
	}
	// 提交必须真实成功：这是"Agent 真的改动了容器内代码"的证据。
	if record.CommitStatus != domain.CommitStatusSucceeded &&
		record.CommitStatus != domain.CommitStatusNoChanges {
		t.Fatalf("代码保存的提交必须真实成功，实际 %s（%s）",
			record.CommitStatus, record.ErrorSummary)
	}
	assertDeliveryRecord(t, record, fixture.task.ID)
	// 推送未成功时必须给出可解释的原因，且不得伪造成成功。
	if record.PushStatus != domain.PushStatusSucceeded &&
		strings.TrimSpace(record.ErrorSummary) == "" {
		t.Fatalf("推送未成功时必须给出可诊断原因，实际状态 %s", record.PushStatus)
	}
	t.Logf("STOPPED_AT_DELIVERY class=%s commit=%s push=%s reason=%s",
		storedRun.ErrorClass, record.CommitSHA, record.PushStatus, record.ErrorSummary)
}

// dumpFailure 输出失败现场的可诊断信息。
func (f *e2eFixture) dumpFailure(t *testrunner.Context) {
	t.Helper()
	t.Logf("TEST_ARTIFACT_DIR=%s", f.workspace.Root())
	task, err := f.db.GetTask(f.ctx, f.task.ID)
	if err == nil {
		t.Logf("TASK status=%s failure=%s", task.Status, task.FailureSummary)
	}
	inits, err := f.db.ListTaskWorkspaceInitsByTask(f.ctx, f.task.ID.String())
	if err == nil {
		for _, init := range inits {
			t.Logf("WORKSPACE_INIT status=%s stage=%s error=%s/%s",
				init.Status, init.Stage, init.ErrorCode, init.ErrorSummary)
		}
	}
}

// assertStageEvidence 核对 M1 退出条件要求的分阶段真实证据。
//
// 断言只依赖已经持久化的事实，不依赖模型文本：规划、开发、确定性验证与语义审查必须
// 各自留下真实执行记录，角色工具策略不得突破能力上限。终局是 completed 还是停在交付
// 阶段都不改变这些要求：只有开发与验证真实通过，才可能进入交付阶段。
func (f *e2eFixture) assertStageEvidence(t *testrunner.Context, containerID string) {
	t.Helper()
	sessions, err := f.db.ListSessionsByTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取任务会话失败: %v", err)
	}
	// 规划会话必须由任务配置的方案规划角色真实发起，且只读。
	planner := requireRoleSession(
		t, sessions, domain.RoleTypePlanner, f.task.Config.PlannerRoleID, "规划",
	)
	assertReadOnlyTools(t, planner)
	run, attempt := f.requireDeveloperEvidence(t, containerID)
	developer := requireAttemptSession(t, sessions, attempt)
	// 开发会话使用的仍然是任务配置的开发者角色，工具必须覆盖写入与命令执行。
	if developer.RoleID != f.task.Config.DeveloperRoleID {
		t.Fatalf("开发会话必须由任务配置的开发者角色发起: %s != %s",
			developer.RoleID, f.task.Config.DeveloperRoleID)
	}
	assertDeveloperTools(t, developer)
	validator := requireReviewSession(t, sessions, run, f.task.Config.ValidatorRoleID)
	assertReadOnlyTools(t, validator)
	assertReviewEvidence(t, run, validator)
}

// requireRoleSession 返回指定角色类型中真实完成的那次会话。
//
// 会话状态必须是 completed 且运行结果为 completed：没有完成痕迹的会话不能当作
// "该阶段真实执行过"的证据。
func requireRoleSession(
	t *testrunner.Context,
	sessions []domain.Session,
	roleType domain.RoleType,
	roleID domain.ID,
	label string,
) domain.Session {
	t.Helper()
	candidates := make([]domain.Session, 0, 1)
	for _, session := range sessions {
		if session.RoleSnapshot.Type == roleType {
			candidates = append(candidates, session)
		}
	}
	if len(candidates) == 0 {
		t.Fatalf("%s阶段必须留下 %s 角色的会话记录", label, roleType)
	}
	for index := len(candidates) - 1; index >= 0; index-- {
		session := candidates[index]
		if session.Status != domain.SessionStatusCompleted ||
			session.RunStatus != domain.SessionRunCompleted {
			continue
		}
		if session.RoleID != roleID {
			t.Fatalf("%s会话必须由任务配置的角色发起: %s != %s", label, session.RoleID, roleID)
		}
		return session
	}
	t.Fatalf("%s阶段没有真实完成的 %s 会话记录", label, roleType)
	return domain.Session{}
}

// assertReadOnlyTools 校验会话的角色策略落在只读上限内，且实际生效工具不含写入与执行。
func assertReadOnlyTools(t *testrunner.Context, session domain.Session) {
	t.Helper()
	roleType := session.RoleSnapshot.Type
	if err := domain.ValidateToolsPolicyForRole(roleType, session.RoleSnapshot.ToolsPolicy); err != nil {
		t.Fatalf("%s 会话 %s 的工具策略超出角色能力上限: %v", roleType, session.ID, err)
	}
	tools, err := domain.EffectivePiTools(roleType, session.RoleSnapshot.ToolsPolicy)
	if err != nil {
		t.Fatalf("计算 %s 会话 %s 的生效工具失败: %v", roleType, session.ID, err)
	}
	for _, forbidden := range e2eReadOnlyForbiddenTools {
		if e2eContainsTool(tools, forbidden) {
			t.Fatalf("%s 会话 %s 不得获得工具 %s，实际工具 %v",
				roleType, session.ID, forbidden, tools)
		}
	}
	if !e2eContainsTool(tools, domain.PiToolRead) {
		t.Fatalf("%s 会话 %s 必须能读取代码与验证证据，实际工具 %v", roleType, session.ID, tools)
	}
	t.Logf("READONLY_SESSION=%s role=%s model=%s tools=%s",
		session.ID, roleType, session.RoleSnapshot.Model, strings.Join(tools, ","))
}

// assertDeveloperTools 校验开发会话的工具确实覆盖读取、写入与命令执行。
func assertDeveloperTools(t *testrunner.Context, session domain.Session) {
	t.Helper()
	roleType := domain.RoleTypeDeveloper
	if err := domain.ValidateToolsPolicyForRole(roleType, session.RoleSnapshot.ToolsPolicy); err != nil {
		t.Fatalf("开发会话 %s 的工具策略超出开发者能力上限: %v", session.ID, err)
	}
	tools, err := domain.EffectivePiTools(roleType, session.RoleSnapshot.ToolsPolicy)
	if err != nil {
		t.Fatalf("计算开发会话 %s 的生效工具失败: %v", session.ID, err)
	}
	for _, required := range []string{
		domain.PiToolRead,
		domain.PiToolEdit,
		domain.PiToolWrite,
		domain.PiToolBash,
	} {
		if !e2eContainsTool(tools, required) {
			t.Fatalf("开发会话 %s 缺少工具 %s，实际工具 %v", session.ID, required, tools)
		}
	}
	t.Logf("DEVELOPER_SESSION=%s role=%s model=%s tools=%s",
		session.ID, roleType, session.RoleSnapshot.Model, strings.Join(tools, ","))
}

// requireDeveloperEvidence 找出一个真实完成开发的步骤，并核对确定性验证证据。
//
// 只有"验证结论为 passed 且关联到真实执行尝试"的开发步骤才算走过链路：计划里的
// 步骤、模型文本里的"已完成"都不构成证据。
func (f *e2eFixture) requireDeveloperEvidence(
	t *testrunner.Context,
	containerID string,
) (domain.ValidationRun, domain.Attempt) {
	t.Helper()
	for _, step := range f.steps(t) {
		if step.Type != domain.StepTypeDevelop {
			continue
		}
		attempts, err := f.db.ListAttemptsByStep(f.ctx, step.ID)
		if err != nil {
			t.Fatalf("读取步骤 %d 的执行尝试失败: %v", step.Sequence, err)
		}
		if len(attempts) == 0 {
			continue
		}
		runs, err := f.db.ListValidationRunsByStep(f.ctx, step.ID)
		if err != nil {
			t.Fatalf("读取步骤 %d 的验证记录失败: %v", step.Sequence, err)
		}
		for _, run := range runs {
			if run.Verdict != domain.VerificationVerdictPassed {
				continue
			}
			attempt, found := findAttempt(attempts, run.AttemptID)
			if !found {
				t.Fatalf("验证记录 %s 关联的执行尝试 %s 不属于步骤 %d",
					run.ID, run.AttemptID, step.Sequence)
			}
			f.assertValidationEvidence(t, step, run, attempt, containerID)
			return run, attempt
		}
	}
	t.Fatal("没有任何开发步骤留下结论为 passed 的确定性验证记录")
	return domain.ValidationRun{}, domain.Attempt{}
}

// assertValidationEvidence 校验确定性验证记录与必需检查结果的真实性。
func (f *e2eFixture) assertValidationEvidence(
	t *testrunner.Context,
	step domain.Step,
	run domain.ValidationRun,
	attempt domain.Attempt,
	containerID string,
) {
	t.Helper()
	if run.Status != domain.ValidationRunStatusCompleted {
		t.Fatalf("验证记录 %s 的流程状态必须为 completed，实际 %s", run.ID, run.Status)
	}
	if run.ContainerID == "" || run.ContainerID != containerID {
		t.Fatalf("验证必须在本次任务容器内执行: 记录 %q，任务容器 %q", run.ContainerID, containerID)
	}
	if attempt.Status != domain.AttemptStatusCompleted {
		t.Fatalf("通过验证的执行尝试必须已完成，实际 %s（%s）", attempt.Status, attempt.FailureSummary)
	}
	results, err := f.db.ListValidationCheckResults(f.ctx, run.ID)
	if err != nil {
		t.Fatalf("读取验证检查结果失败: %v", err)
	}
	result, found := findCheckResult(results, e2eRequiredCheckName)
	if !found {
		t.Fatalf("验证记录 %s 缺少必需检查 %s 的执行结果", run.ID, e2eRequiredCheckName)
	}
	if !result.Required {
		t.Fatalf("检查 %s 必须是必需检查", e2eRequiredCheckName)
	}
	if result.Status != domain.ValidationCheckStatusPassed {
		t.Fatalf("检查 %s 必须真实通过，实际 %s（%s）",
			e2eRequiredCheckName, result.Status, result.FailureReason)
	}
	if result.ExitCode != 0 {
		t.Fatalf("检查 %s 通过时退出码必须为 0，实际 %d", e2eRequiredCheckName, result.ExitCode)
	}
	// 开发必须真实发生在容器内工作区：Git 快照可核验时要求确有未提交变更。
	if attempt.GitAfter != nil && attempt.GitAfter.StatusRead {
		if !attempt.GitAfter.Dirty {
			t.Fatalf("开发步骤必须真实修改容器内工作区，实际 %s", attempt.GitAfter.Summary())
		}
		t.Logf("DEVELOP_GIT branch=%s head=%s summary=%s",
			attempt.GitAfter.Branch, attempt.GitAfter.Head, attempt.GitAfter.Summary())
	}
	t.Logf("DEVELOP_STEP=%d ATTEMPT=%s ATTEMPT_STATUS=%s VALIDATION_RUN=%s CHECKS=%d/%d CHECK=%s exit=%d map=%s spec=%s",
		step.Sequence, attempt.ID, attempt.Status, run.ID,
		run.PassedChecks, run.TotalChecks, e2eRequiredCheckName, result.ExitCode,
		run.ExecutionEnv, run.Workspace)
}

// requireAttemptSession 返回与执行尝试对应的开发会话，并校验归属关系一致。
func requireAttemptSession(
	t *testrunner.Context,
	sessions []domain.Session,
	attempt domain.Attempt,
) domain.Session {
	t.Helper()
	for _, session := range sessions {
		if session.AttemptID == nil || *session.AttemptID != attempt.ID {
			continue
		}
		if session.RoleSnapshot.Type != domain.RoleTypeDeveloper {
			continue
		}
		if session.Status != domain.SessionStatusCompleted ||
			session.RunStatus != domain.SessionRunCompleted {
			t.Fatalf("开发会话 %s 必须真实完成，实际状态 %s/%s",
				session.ID, session.Status, session.RunStatus)
		}
		if session.StepID == nil || *session.StepID != attempt.StepID {
			t.Fatalf("开发会话 %s 的步骤与执行尝试 %s 不一致", session.ID, attempt.ID)
		}
		if !e2eContainsID(attempt.SessionIDs, session.ID) {
			t.Fatalf("执行尝试 %s 必须关联开发会话 %s", attempt.ID, session.ID)
		}
		return session
	}
	t.Fatalf("执行尝试 %s 必须留下真实完成的开发会话", attempt.ID)
	return domain.Session{}
}

// requireReviewSession 返回验证记录关联的语义审查会话，并校验角色类型与归属。
func requireReviewSession(
	t *testrunner.Context,
	sessions []domain.Session,
	run domain.ValidationRun,
	roleID domain.ID,
) domain.Session {
	t.Helper()
	if run.ValidatorSessionID == nil {
		t.Fatalf("验证记录 %s 没有记录语义审查会话", run.ID)
	}
	sessionID := *run.ValidatorSessionID
	if sessionID.IsEmpty() {
		t.Fatalf("验证记录 %s 的语义审查会话标识为空", run.ID)
	}
	for _, session := range sessions {
		if session.ID != sessionID {
			continue
		}
		if session.RoleSnapshot.Type != domain.RoleTypeValidator {
			t.Fatalf("语义审查会话 %s 的角色类型必须是 validator，实际 %s",
				session.ID, session.RoleSnapshot.Type)
		}
		if session.RoleID != roleID {
			t.Fatalf("语义审查会话必须由任务配置的验证角色发起: %s != %s",
				session.RoleID, roleID)
		}
		if session.Status != domain.SessionStatusCompleted ||
			session.RunStatus != domain.SessionRunCompleted {
			t.Fatalf("语义审查会话 %s 必须真实完成，实际状态 %s/%s",
				session.ID, session.Status, session.RunStatus)
		}
		return session
	}
	t.Fatalf("语义审查会话 %s 必须是本任务的会话", sessionID)
	return domain.Session{}
}

// assertReviewEvidence 校验语义审查真的给出了可解释的通过结论。
func assertReviewEvidence(t *testrunner.Context, run domain.ValidationRun, validator domain.Session) {
	t.Helper()
	if run.ReviewStatus != domain.ReviewStatusPassed {
		t.Fatalf("语义审查必须真实给出 passed，实际 %s（%s）", run.ReviewStatus, run.ReviewSummary)
	}
	if run.Assessment == nil || !run.Assessment.Passed {
		t.Fatalf("语义审查通过必须带有可解释的评估结果，实际 %+v", run.Assessment)
	}
	if validator.AttemptID == nil || *validator.AttemptID != run.AttemptID {
		t.Fatalf("语义审查会话 %s 必须关联验证记录 %s 的执行尝试", validator.ID, run.ID)
	}
	if strings.TrimSpace(run.ReviewSummary) == "" {
		t.Fatal("语义审查必须留下可复核的说明")
	}
	t.Logf("VALIDATOR_SESSION=%s review=%s evidence=%v summary=%s",
		validator.ID, run.ReviewStatus, run.Assessment.Evidence, run.ReviewSummary)
}

// assertDeliveryRecord 核对交付记录的分支唯一性与"不自动合并"约束。
func assertDeliveryRecord(t *testrunner.Context, record domain.CodeSaveRecord, taskID domain.ID) {
	t.Helper()
	expected := domain.TaskBranchPrefix + taskID.String()
	if record.TaskBranch != expected {
		t.Fatalf("任务分支必须是本任务专属的唯一分支: %q != %q", record.TaskBranch, expected)
	}
	if record.MergeEnabled {
		t.Fatalf("本次交付不得请求合并基础分支: %s", record.TaskBranch)
	}
	if record.MergeStatus != domain.MergeStatusNotRequested {
		t.Fatalf("本次交付不得合并基础分支，实际 %s", record.MergeStatus)
	}
}

// findAttempt 按标识查找执行尝试。
func findAttempt(attempts []domain.Attempt, attemptID domain.ID) (domain.Attempt, bool) {
	for _, attempt := range attempts {
		if attempt.ID == attemptID {
			return attempt, true
		}
	}
	return domain.Attempt{}, false
}

// findCheckResult 按检查标识查找检查结果。
func findCheckResult(
	results []domain.ValidationCheckResult,
	checkID string,
) (domain.ValidationCheckResult, bool) {
	for _, result := range results {
		if result.CheckID == checkID {
			return result, true
		}
	}
	return domain.ValidationCheckResult{}, false
}

// findVerificationCommand 按名称查找步骤上的验证命令。
func findVerificationCommand(
	step domain.Step,
	name string,
) (domain.VerificationCommand, bool) {
	for _, command := range step.VerificationSpec.Commands {
		if command.Name == name {
			return command, true
		}
	}
	return domain.VerificationCommand{}, false
}

// hasRequiredVerificationCommand 判断步骤是否带有可执行的必需检查。
func hasRequiredVerificationCommand(step domain.Step) bool {
	for _, command := range step.VerificationSpec.Commands {
		if command.IsRequired() {
			return true
		}
	}
	return false
}

// e2eContainsTool 判断工具列表是否包含指定工具。
func e2eContainsTool(tools []string, target string) bool {
	for _, tool := range tools {
		if tool == target {
			return true
		}
	}
	return false
}

// e2eContainsID 判断标识列表是否包含指定标识。
func e2eContainsID(ids []domain.ID, target domain.ID) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
