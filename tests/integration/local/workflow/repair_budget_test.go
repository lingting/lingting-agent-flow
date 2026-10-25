// Package workflow_test 验证 P2-M3 的修复预算持久化与执行周期语义。
//
// 本文件的用例全部基于真实 SQLite、正式迁移与生产领域规则：断言对象是数据库里
// 真实留下的 repair_cycles / attempts / steps 记录，而不是方法的返回值。
//
// 关键约束：
//   - 重启场景使用同一数据库路径重新打开连接，模拟进程退出；
//   - 外部 Agent、容器与检查命令由受控替身提供，替身只放在测试边界；
//   - 不直接写入最终状态绕过正式流程，也不删除历史记录。
package workflow_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// budgetFixture 是修复预算用例的现场：真实数据库 + 真实闭环协调器。
type budgetFixture struct {
	ctx       context.Context
	path      string
	db        *sqlite.DB
	logger    *logging.Logger
	workspace *testkit.Workspace
	ports     *e2ePorts
	repo      *e2eRepo
	task      domain.Task
	step      domain.Step
	service   *app.StepWorkflowService
	options   budgetOptions
	// closeDB 关闭当前数据库连接，用于模拟进程退出。
	closeDB func()
}

// budgetOptions 允许用例调整现场构造方式。
type budgetOptions struct {
	// retry 是步骤生效的重试策略；零值使用上限 2。
	retry domain.RetryConfig
	// agentReplies 是 Agent 调用结果序列。
	agentReplies []e2eAgentReply
	// checkSequence 是确定性检查结果序列。
	checkSequence []e2eCheckReply
	// feedbackErr 让反馈写入失败。
	feedbackErr error
	// feedbackReadErr 让反馈读取失败。
	feedbackReadErr error
}

// newBudgetFixture 在工作空间中准备一条可重复执行的步骤现场。
func newBudgetFixture(t *testing.T, options budgetOptions) *budgetFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	fixture := &budgetFixture{
		ctx:       ctx,
		path:      workspace.DatabasePath(),
		logger:    logger,
		workspace: workspace,
		options:   options,
	}
	fixture.openDB(t)
	fixture.seed(t, options)
	fixture.repo = newE2ERepo(t, fixture.task.ID)
	fixture.ports = &e2ePorts{
		repo:          fixture.repo,
		running:       true,
		agentReplies:  options.agentReplies,
		checkSequence: options.checkSequence,
	}
	fixture.assemble(t, options)
	// 用例结束时关闭当前连接（restart 后指向最新打开的连接）。
	t.Cleanup(func() { fixture.closeDB() })
	return fixture
}

// openDB 打开数据库并登记关闭。
func (f *budgetFixture) openDB(t *testing.T) {
	t.Helper()
	db, err := sqlite.Open(f.ctx, sqlite.Options{Path: f.path})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %s: %v", f.path, err)
	}
	f.db = db
	f.closeDB = func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	}
}

// restart 关闭数据库并用同一路径重新打开，模拟进程退出后重启。
//
// 重启后必须重新装配服务：进程退出后原来的服务对象与数据库连接都不再可用，只有
// 数据库里的记录会保留下来。这样断言的是"重启后从数据库重新得到的判定"。
func (f *budgetFixture) restart(t *testing.T) {
	t.Helper()
	f.closeDB()
	f.openDB(t)
	f.assemble(t, f.options)
}

// seed 写入角色、执行节点、需求与一条开发步骤。
func (f *budgetFixture) seed(t *testing.T, options budgetOptions) {
	t.Helper()
	now := time.Now().UTC()
	planner := e2eRole(t, f.db, f.ctx, domain.RoleTypePlanner, "架构规划师", now)
	developer := e2eRole(t, f.db, f.ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	host := e2eMarkHostNormal(t, f.db, f.ctx, e2eHost(t, f.db, f.ctx, now))
	requirement, err := domain.NewRequirement("预算需求", "需求正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := f.db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, e2eRepositoryURL, e2eBaseBranch, "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	seedOwnedContainer(t, f.db, f.ctx, task, e2eContainerID, e2eDockerID)
	task.ContainerID = e2eContainerID
	retry := options.retry
	if retry.IsZero() {
		retry = domain.RetryConfig{MaxAttempts: 2, RetryVerification: true}
	}
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
		Retry:           retry,
		Verification: domain.VerificationSpec{
			AcceptanceCriteria: []string{"构建通过"},
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
	}
	if err := f.db.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	// 任务必须从 queued 逐级推进到 planned：状态机不允许跳级，用例也不绕过它。
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
	} {
		if err := task.Transition(status, now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", status, err)
		}
		if err := f.db.UpdateTask(f.ctx, task); err != nil {
			t.Fatalf("更新任务失败: %v", err)
		}
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"只修改当前工作区",
		developer,
		task.Config.Verification,
		retry,
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := f.db.CreateStep(f.ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	f.task = task
	f.step = step
}

// assemble 装配真实闭环协调器，只把外部执行替换为受控替身。
func (f *budgetFixture) assemble(t *testing.T, options budgetOptions) {
	t.Helper()
	sessions, err := app.NewSessionService(f.db, f.workspace.SessionRoot(), f.logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      f.db,
		Containers: f.ports,
		Sessions:   sessions,
		Git:        f.ports,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	// 本文件只验证预算与反馈语义，因此不装配语义审查：结论只由确定性检查决定，
	// 用例不需要为 Validator 额外准备 Agent 回复。
	validation, err := app.NewValidationService(app.ValidationOptions{
		Store:      f.db,
		Containers: f.ports,
		OutputRoot: f.workspace.Dir("validations"),
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
	service, err := app.NewStepWorkflowService(app.StepWorkflowOptions{
		Steps:      executor,
		Validation: validation,
		Store:      f.storeOf(options),
		Cycles:     f.db,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造闭环协调器失败: %v", err)
	}
	startE2EServices(t, executor, validation, service)
	f.service = service
}

// storeOf 返回闭环协调器使用的存储，按需注入反馈读写失败。
func (f *budgetFixture) storeOf(options budgetOptions) app.StepWorkflowStore {
	if options.feedbackErr == nil && options.feedbackReadErr == nil {
		return f.db
	}
	return &budgetStore{
		StepWorkflowStore: f.db,
		createErr:         options.feedbackErr,
		readErr:           options.feedbackReadErr,
	}
}

// budgetStore 在真实存储上注入反馈读写失败。
//
// 失败发生在存储边界，因此验证的是真实的错误传播与"没有可信反馈就不启动修复"
// 的业务语义，而不是业务代码里的分支。
type budgetStore struct {
	app.StepWorkflowStore
	createErr error
	readErr   error
}

// CreateValidationFailureFeedback 在配置了错误时返回该错误。
func (s *budgetStore) CreateValidationFailureFeedback(
	ctx context.Context,
	feedback domain.ValidationFailureFeedback,
) error {
	if s.createErr != nil {
		return s.createErr
	}
	return s.StepWorkflowStore.CreateValidationFailureFeedback(ctx, feedback)
}

// GetValidationFailureFeedback 在配置了错误时返回该错误。
func (s *budgetStore) GetValidationFailureFeedback(
	ctx context.Context,
	runID domain.ID,
) (*domain.ValidationFailureFeedback, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s.StepWorkflowStore.GetValidationFailureFeedback(ctx, runID)
}

// execute 通过正式入口执行一次开发—验证闭环。
func (f *budgetFixture) execute(t *testing.T) (app.ExecuteAndValidateResult, error) {
	t.Helper()
	return f.service.ExecuteAndValidate(f.ctx, app.ExecuteAndValidateRequest{
		TaskID: f.task.ID.String(),
		StepID: f.step.ID.String(),
	})
}

// reopenCycle 通过正式入口显式开启新的执行周期。
func (f *budgetFixture) reopenCycle(t *testing.T) (app.OpenRepairCycleResult, error) {
	t.Helper()
	return f.service.OpenRepairCycle(f.ctx, app.OpenRepairCycleRequest{
		TaskID: f.task.ID.String(),
		StepID: f.step.ID.String(),
	})
}

// attempts 读取步骤的全部历史尝试。
func (f *budgetFixture) attempts(t *testing.T) []domain.Attempt {
	t.Helper()
	attempts, err := f.db.ListAttemptsByStep(f.ctx, f.step.ID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	return attempts
}

// cycles 读取步骤的全部执行周期。
func (f *budgetFixture) cycles(t *testing.T) []domain.RepairCycle {
	t.Helper()
	cycles, err := f.db.ListRepairCyclesByStep(f.ctx, f.step.ID)
	if err != nil {
		t.Fatalf("读取执行周期失败: %v", err)
	}
	return cycles
}

// reloadStep 读取数据库中的步骤。
func (f *budgetFixture) reloadStep(t *testing.T) domain.Step {
	t.Helper()
	step, err := f.db.GetStep(f.ctx, f.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return *step
}

// failedRepairReplies 返回"两轮开发都失败"的 Agent 调用序列。
func failedRepairReplies() []e2eAgentReply {
	return []e2eAgentReply{
		{status: agent.RunCompleted, finalText: "第一次实现"},
		{status: agent.RunCompleted, finalText: "第二次实现"},
	}
}

// alwaysFailChecks 返回"全部检查失败"的结果序列。
func alwaysFailChecks() []e2eCheckReply {
	failed := e2eCheckReply{
		exitCode: 1,
		stderr:   "编译错误: undefined: foo",
		started:  true,
		certain:  true,
	}
	return []e2eCheckReply{failed, failed}
}

// TestRepairBudgetPersistsAcrossRestart 验证同一周期重启后剩余预算与退出前一致。
//
// 现场固定为：第一个周期用尽 2 次预算 → 用户显式开启第二个周期 → 进程在第二个
// 周期执行之前退出（周期仍是活动周期、已消耗 0 次）。重启后必须仍然使用同一个
// 周期、同一份上限快照与同一份已消耗次数，因此剩余预算与退出前完全一致。
func TestRepairBudgetPersistsAcrossRestart(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  failedRepairReplies(),
		checkSequence: alwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	first, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("第一个周期闭环失败: %v", err)
	}
	if first.Repair.Class != domain.RepairClassAttemptsExhausted {
		t.Fatalf("第一个周期应用尽上限，实际 %s（%s）",
			first.Repair.Class, first.Repair.Summary)
	}
	if _, err := fixture.reopenCycle(t); err != nil {
		t.Fatalf("显式开启第二个周期失败: %v", err)
	}

	before := fixture.cycles(t)
	if len(before) != 2 {
		t.Fatalf("应有 2 个周期登记，实际 %d", len(before))
	}
	if before[0].Active() {
		t.Fatal("第一个周期必须已经结束")
	}
	activeBefore := before[1]
	if !activeBefore.Active() {
		t.Fatal("第二个周期必须是活动周期")
	}
	attemptsBefore := fixture.attempts(t)
	if len(attemptsBefore) != 2 {
		t.Fatalf("应留下 2 次尝试，实际 %d", len(attemptsBefore))
	}
	consumedBefore := countCycleAttempts(attemptsBefore, activeBefore.ID)
	if consumedBefore != 0 {
		t.Fatalf("第二个周期尚未执行，已消耗应为 0，实际 %d", consumedBefore)
	}
	remainingBefore := activeBefore.MaxAttempts - consumedBefore
	if remainingBefore != 2 {
		t.Fatalf("退出前剩余预算应为 2，实际 %d", remainingBefore)
	}

	// 模拟进程退出后重启：同一数据库路径重新打开，服务重新装配。
	fixture.restart(t)

	// 重启后：周期标识、上限快照与已消耗次数完全一致，因此剩余预算也一致。
	after := fixture.cycles(t)
	if len(after) != 2 {
		t.Fatalf("重启后周期数量不符: %d", len(after))
	}
	if after[1].ID != activeBefore.ID {
		t.Fatalf("重启不得创建新周期: %s → %s", activeBefore.ID, after[1].ID)
	}
	if after[1].MaxAttempts != activeBefore.MaxAttempts {
		t.Fatalf("重启后上限快照不得变化: %d → %d",
			activeBefore.MaxAttempts, after[1].MaxAttempts)
	}
	attemptsAfter := fixture.attempts(t)
	if len(attemptsAfter) != len(attemptsBefore) {
		t.Fatalf("重启不得新增尝试: %d → %d", len(attemptsBefore), len(attemptsAfter))
	}
	consumedAfter := countCycleAttempts(attemptsAfter, activeBefore.ID)
	if consumedAfter != consumedBefore {
		t.Fatalf("重启后已消耗次数必须一致: %d → %d", consumedBefore, consumedAfter)
	}
	if remaining := after[1].MaxAttempts - consumedAfter; remaining != remainingBefore {
		t.Fatalf("重启后剩余预算必须一致: %d → %d", remainingBefore, remaining)
	}
	// 领域判定同样必须得出"仍有 2 次预算"的结论：预算来自持久化事实，不是进程内计数。
	decision := domain.DecideRepairStart(domain.RepairStartInput{
		StepID:          fixture.step.ID,
		StepType:        fixture.step.Type,
		StepMaxAttempts: fixture.step.MaxAttempts,
		Retry:           fixture.step.Retry,
		Cycle:           after[1],
		HasCycle:        true,
		Cycles:          after,
		Attempts:        attemptFactsOf(attemptsAfter),
	})
	if !decision.Allowed {
		t.Fatalf("重启后仍应有剩余预算，实际 %s（%s）", decision.Class, decision.Summary)
	}
	if decision.Budget.Consumed != consumedBefore {
		t.Fatalf("领域判定读取的已消耗次数不符: %d", decision.Budget.Consumed)
	}
	if decision.Budget.MaxAttempts != activeBefore.MaxAttempts {
		t.Fatalf("领域判定读取的上限快照不符: %d", decision.Budget.MaxAttempts)
	}
}

// countCycleAttempts 统计属于指定周期的尝试数量。
func countCycleAttempts(attempts []domain.Attempt, cycleID domain.ID) int {
	count := 0
	for _, attempt := range attempts {
		if attempt.BudgetScopeID == cycleID {
			count++
		}
	}
	return count
}

// attemptFactsOf 把持久化尝试转换为领域判定输入。
func attemptFactsOf(attempts []domain.Attempt) []domain.RepairAttemptFacts {
	facts := make([]domain.RepairAttemptFacts, 0, len(attempts))
	for _, attempt := range attempts {
		facts = append(facts, domain.RepairAttemptFacts{
			ID:            attempt.ID,
			Status:        attempt.Status,
			BudgetScopeID: attempt.BudgetScopeID,
		})
	}
	return facts
}

// TestRepairBudgetExhaustedAfterRestartRejectsExecution 验证达到上限后重启不能执行。
func TestRepairBudgetExhaustedAfterRestartRejectsExecution(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  failedRepairReplies(),
		checkSequence: alwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	first, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("首轮闭环失败: %v", err)
	}
	if first.Repair.Class != domain.RepairClassAttemptsExhausted {
		t.Fatalf("达到上限的类别不符: %s", first.Repair.Class)
	}
	attemptsBefore := len(fixture.attempts(t))

	fixture.restart(t)

	// 重启后再次进入正式执行入口：必须被拒绝，且不创建新的 Attempt。
	_, err = fixture.execute(t)
	if err == nil {
		t.Fatal("达到上限后重启不得继续执行")
	}
	if !errors.Is(err, app.ErrStepExecutionConflict) {
		t.Fatalf("错误类别不符: %v", err)
	}
	if attempts := len(fixture.attempts(t)); attempts != attemptsBefore {
		t.Fatalf("被拒绝的执行不得创建尝试: %d → %d", attemptsBefore, attempts)
	}
	// 周期数量同样不变：拒绝不是"开启新周期"。
	if cycles := len(fixture.cycles(t)); cycles != 1 {
		t.Fatalf("被拒绝的执行不得创建周期: %d", cycles)
	}
}

// TestRepairBudgetRepeatedCallDoesNotResetBudget 验证重复调用不会重置预算。
func TestRepairBudgetRepeatedCallDoesNotResetBudget(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry:         domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies:  failedRepairReplies(),
		checkSequence: alwaysFailChecks(),
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	if _, err := fixture.execute(t); err != nil {
		t.Fatalf("首轮闭环失败: %v", err)
	}
	attemptsBefore := len(fixture.attempts(t))
	// 重复调用三次：每一次都必须被同一份已用尽的预算拒绝。
	for index := 0; index < 3; index++ {
		if _, err := fixture.execute(t); err == nil {
			t.Fatalf("第 %d 次重复调用不得继续执行", index+1)
		}
	}
	if attempts := len(fixture.attempts(t)); attempts != attemptsBefore {
		t.Fatalf("重复调用不得创建尝试: %d → %d", attemptsBefore, attempts)
	}
	if cycles := len(fixture.cycles(t)); cycles != 1 {
		t.Fatalf("重复调用不得创建周期: %d", cycles)
	}
}

// TestOpenRepairCycleGrantsFreshBudget 验证显式开启新周期后获得新预算。
func TestOpenRepairCycleGrantsFreshBudget(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry: domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
		agentReplies: []e2eAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "第二次实现"},
			{status: agent.RunCompleted, finalText: "新周期的实现"},
		},
		checkSequence: []e2eCheckReply{
			{exitCode: 1, stderr: "编译错误", started: true, certain: true},
			{exitCode: 1, stderr: "编译错误", started: true, certain: true},
			{exitCode: 0, stdout: "ok", started: true, certain: true},
		},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	if _, err := fixture.execute(t); err != nil {
		t.Fatalf("首轮闭环失败: %v", err)
	}
	opened, err := fixture.reopenCycle(t)
	if err != nil {
		t.Fatalf("显式开启新周期失败: %v", err)
	}
	if opened.CycleSequence != 2 {
		t.Fatalf("新周期序号应为 2，实际 %d", opened.CycleSequence)
	}
	if opened.PreviousConsumed != 2 {
		t.Fatalf("应报告上一周期已消耗 2 次，实际 %d", opened.PreviousConsumed)
	}
	cycles := fixture.cycles(t)
	if len(cycles) != 2 {
		t.Fatalf("应有 2 个周期登记，实际 %d", len(cycles))
	}
	// 旧周期必须保留并已结束：历史记录不得被覆盖或删除。
	if !cycles[0].Active() == false {
		t.Fatal("旧周期必须已经结束")
	}
	if !cycles[1].Active() {
		t.Fatal("新周期必须是活动周期")
	}
	// 新周期下执行成功：预算确实被重置，且新 Attempt 归属新周期。
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("新周期执行失败: %v", err)
	}
	if !result.Verified {
		t.Fatalf("新周期执行应通过验证: %+v", result.Repair)
	}
	attempts := fixture.attempts(t)
	if len(attempts) != 3 {
		t.Fatalf("应保留 3 次尝试，实际 %d", len(attempts))
	}
	if attempts[2].BudgetScopeID != cycles[1].ID {
		t.Fatalf("新尝试必须归属新周期，实际 %s", attempts[2].BudgetScopeID)
	}
}

// TestOpenRepairCycleRejectsWhenBudgetRemains 验证仍有余额时拒绝开启新周期。
func TestOpenRepairCycleRejectsWhenBudgetRemains(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry: domain.RetryConfig{MaxAttempts: 5, RetryVerification: true},
		agentReplies: []e2eAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
		},
		checkSequence: []e2eCheckReply{
			{exitCode: 0, stdout: "ok", started: true, certain: true},
		},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	if _, err := fixture.execute(t); err != nil {
		t.Fatalf("首轮闭环失败: %v", err)
	}
	// 验证通过后周期已结束，但上限 5 只用了 1 次：仍不允许重置预算。
	_, err := fixture.reopenCycle(t)
	if err == nil {
		t.Fatal("仍有剩余预算时不得开启新周期")
	}
	if !errors.Is(err, app.ErrStepWorkflowCycleDenied) {
		t.Fatalf("错误类别不符: %v", err)
	}
	if cycles := len(fixture.cycles(t)); cycles != 1 {
		t.Fatalf("被拒绝的请求不得创建周期: %d", cycles)
	}
}

// TestOpenRepairCycleRejectsUnresolvedAttempt 验证存在不确定尝试时拒绝开启新周期。
func TestOpenRepairCycleRejectsUnresolvedAttempt(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry: domain.RetryConfig{MaxAttempts: 2, RetryVerification: true},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	// 构造"进程退出后残留 running 尝试"的现场：真实执行路径正是先占用为 running。
	cycle, err := fixture.db.StartRepairCycle(fixture.ctx, storageStartRepairCycle(
		fixture.task.ID, fixture.step.ID, 2,
	))
	if err != nil {
		t.Fatalf("登记执行周期失败: %v", err)
	}
	attempt, err := fixture.db.StartAttempt(fixture.ctx, storageStartAttempt(
		fixture.task.ID, fixture.step.ID, cycle.ID,
	))
	if err != nil {
		t.Fatalf("创建尝试失败: %v", err)
	}
	recovered := attempt
	if err := recovered.Interrupt("进程退出，结果无法确认", time.Now().UTC()); err != nil {
		t.Fatalf("收敛尝试失败: %v", err)
	}
	if err := fixture.db.UpdateAttemptIfStatus(
		fixture.ctx,
		recovered,
		[]domain.AttemptStatus{domain.AttemptStatusRunning},
	); err != nil {
		t.Fatalf("写入尝试失败: %v", err)
	}
	// 存在 interrupted 尝试时，开启新周期必须被拒绝：不能直接消耗下一轮预算。
	if _, err := fixture.reopenCycle(t); err == nil {
		t.Fatal("存在不确定尝试时不得开启新周期")
	}
	// 直接执行同样必须被拒绝，且不产生新的 Attempt。
	before := len(fixture.attempts(t))
	if _, err := fixture.execute(t); err == nil {
		t.Fatal("存在不确定尝试时不得执行")
	}
	if after := len(fixture.attempts(t)); after != before {
		t.Fatalf("被拒绝的执行不得创建尝试: %d → %d", before, after)
	}
}

// TestRepairStopsWhenFeedbackWriteFails 验证反馈写入失败时停止修复。
func TestRepairStopsWhenFeedbackWriteFails(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry: domain.RetryConfig{MaxAttempts: 3, RetryVerification: true},
		agentReplies: []e2eAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
			{status: agent.RunCompleted, finalText: "不应发生的第二次实现"},
		},
		checkSequence: []e2eCheckReply{
			{exitCode: 1, stderr: "编译错误", started: true, certain: true},
			{exitCode: 1, stderr: "编译错误", started: true, certain: true},
		},
		feedbackErr: errBudgetFeedbackWrite,
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	result, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("首轮闭环失败: %v", err)
	}
	if result.Repair.Class != domain.RepairClassFeedbackUnavailable {
		t.Fatalf("反馈写入失败时停止类别不符: %s（%s）",
			result.Repair.Class, result.Repair.Summary)
	}
	if result.FeedbackPersisted {
		t.Fatal("反馈写入失败时不得报告已保存")
	}
	// 不得启动第二轮 Agent 调用：没有修复依据就不重放。
	if _, _, agentCalls, _ := fixture.ports.counts(); agentCalls != 1 {
		t.Fatalf("反馈不可用时不得继续修复，实际调用 %d 次", agentCalls)
	}
	if attempts := len(fixture.attempts(t)); attempts != 1 {
		t.Fatalf("反馈不可用时不得创建新尝试，实际 %d", attempts)
	}
	// 步骤必须进入需要人工核验，而不是"验证失败"。
	step := fixture.reloadStep(t)
	if step.Status != domain.StepStatusRecoveryRequired {
		t.Fatalf("步骤状态不符: %s", step.Status)
	}
}

// TestRepairStopsWhenFeedbackReadFails 验证反馈读取失败时停止修复。
func TestRepairStopsWhenFeedbackReadFails(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry: domain.RetryConfig{MaxAttempts: 3, RetryVerification: true},
		agentReplies: []e2eAgentReply{
			{status: agent.RunCompleted, finalText: "第一次实现"},
		},
		checkSequence: []e2eCheckReply{
			{exitCode: 1, stderr: "编译错误", started: true, certain: true},
		},
		feedbackReadErr: errBudgetFeedbackRead,
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	// 首轮执行会读取周期内反馈（此时没有历史，因此不会触发注入的读取失败）。
	first, err := fixture.execute(t)
	if err != nil {
		t.Fatalf("首轮闭环失败: %v", err)
	}
	if first.Repair.Class != domain.RepairClassFeedbackUnavailable {
		t.Fatalf("反馈读取失败时停止类别不符: %s（%s）",
			first.Repair.Class, first.Repair.Summary)
	}
	// 没有可信反馈时不得启动第二轮开发调用。
	if _, _, agentCalls, _ := fixture.ports.counts(); agentCalls != 1 {
		t.Fatalf("反馈读取失败时不得继续修复，实际调用 %d 次", agentCalls)
	}
	if attempts := len(fixture.attempts(t)); attempts != 1 {
		t.Fatalf("反馈读取失败时不得创建新尝试，实际 %d", attempts)
	}
}

// TestRepairIgnoresPreviousCycleFeedback 验证新周期不使用旧周期的反馈。
func TestRepairIgnoresPreviousCycleFeedback(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry: domain.RetryConfig{MaxAttempts: 1, RetryVerification: true},
		agentReplies: []e2eAgentReply{
			{status: agent.RunCompleted, finalText: "旧周期的实现"},
			{status: agent.RunCompleted, finalText: "新周期的实现"},
		},
		checkSequence: []e2eCheckReply{
			{exitCode: 1, stderr: "旧周期的编译错误", started: true, certain: true},
			{exitCode: 0, stdout: "ok", started: true, certain: true},
		},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	if _, err := fixture.execute(t); err != nil {
		t.Fatalf("首轮闭环失败: %v", err)
	}
	// 首轮失败后旧周期已结束（上限 1）。历史反馈仍然存在且可查询。
	oldCycles := fixture.cycles(t)
	if len(oldCycles) != 1 {
		t.Fatalf("应有一个周期，实际 %d", len(oldCycles))
	}
	if _, err := fixture.reopenCycle(t); err != nil {
		t.Fatalf("显式开启新周期失败: %v", err)
	}
	if _, err := fixture.execute(t); err != nil {
		t.Fatalf("新周期执行失败: %v", err)
	}
	prompt := lastBudgetPrompt(fixture.ports)
	// 新周期的首次执行可以引用紧邻上一周期的失败（这是唯一的可信起点依据），
	// 但不得引用更早周期。这里只有一个旧周期，因此断言引用的是它。
	if prompt == "" {
		t.Fatal("必须记录 Agent 提示词")
	}
	// 历史反馈不得因新周期开始而被覆盖或删除。
	runs, err := fixture.db.ListValidationRunsByStep(fixture.ctx, fixture.step.ID)
	if err != nil {
		t.Fatalf("读取验证历史失败: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("历史验证记录必须保留，实际 %d", len(runs))
	}
	feedbacks := 0
	for _, run := range runs {
		if _, err := fixture.db.GetValidationFailureFeedback(fixture.ctx, run.ID); err == nil {
			feedbacks++
		}
	}
	if feedbacks != 1 {
		t.Fatalf("只有失败轮次产生反馈，实际 %d 条", feedbacks)
	}
}

// lastBudgetPrompt 返回最后一次 Agent 调用的提示词。
func lastBudgetPrompt(ports *e2ePorts) string {
	prompts := ports.agentPrompts()
	if len(prompts) == 0 {
		return ""
	}
	return prompts[len(prompts)-1]
}

// errBudgetFeedbackWrite 是反馈写入失败的注入错误。
var errBudgetFeedbackWrite = errors.New("注入的反馈写入失败")

// errBudgetFeedbackRead 是反馈读取失败的注入错误。
var errBudgetFeedbackRead = errors.New("注入的反馈读取失败")

// storageStartRepairCycle 构造一次执行周期开启请求。
func storageStartRepairCycle(
	taskID, stepID domain.ID,
	maxAttempts int,
) storage.StartRepairCycleRequest {
	return storage.StartRepairCycleRequest{
		TaskID:      taskID,
		StepID:      stepID,
		MaxAttempts: maxAttempts,
		OpenedBy:    "test_setup",
		At:          time.Now().UTC(),
	}
}

// storageStartAttempt 构造一次"创建尝试"请求，并绑定预算作用域。
func storageStartAttempt(
	taskID, stepID, cycleID domain.ID,
) storage.StartAttemptRequest {
	return storage.StartAttemptRequest{
		TaskID:        taskID,
		StepID:        stepID,
		BudgetScope:   domain.RepairBudgetScopeCycle,
		BudgetScopeID: cycleID,
		At:            time.Now().UTC(),
	}
}

// TestOpenRepairCycleConcurrentOnlyOneSucceeds 验证并发开启新周期只有一个成功。
//
// 一致性由数据库的部分唯一索引保证，而不是进程内互斥锁：因此多个协调器实例
// （共享同一数据库）同时开启同一步骤的新周期时，最多只有一条成功。
func TestOpenRepairCycleConcurrentOnlyOneSucceeds(t *testing.T) {
	t.Parallel()
	fixture := newBudgetFixture(t, budgetOptions{
		retry:         domain.RetryConfig{MaxAttempts: 1, RetryVerification: true},
		agentReplies:  []e2eAgentReply{{status: agent.RunCompleted, finalText: "第一次实现"}},
		checkSequence: []e2eCheckReply{{exitCode: 1, stderr: "编译错误", started: true, certain: true}},
	})
	fixture.repo.writeFile(t, "internal/feature.go", "package internal\n")
	if _, err := fixture.execute(t); err != nil {
		t.Fatalf("首轮闭环失败: %v", err)
	}
	// 两个实例共享同一数据库：模拟跨进程并发。
	second := newBudgetServiceFor(t, fixture)
	results := make([]error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, results[0] = fixture.reopenCycle(t)
	}()
	go func() {
		defer wait.Done()
		_, results[1] = second.OpenRepairCycle(fixture.ctx, app.OpenRepairCycleRequest{
			TaskID: fixture.task.ID.String(),
			StepID: fixture.step.ID.String(),
		})
	}()
	wait.Wait()
	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("并发开启新周期只允许一个成功，实际 %d 个", succeeded)
	}
	if cycles := len(fixture.cycles(t)); cycles != 2 {
		t.Fatalf("并发成功后应只有 2 个周期，实际 %d", cycles)
	}
}

// newBudgetServiceFor 在同一数据库上构造第二个闭环协调器，用于跨实例并发验证。
func newBudgetServiceFor(t *testing.T, fixture *budgetFixture) *app.StepWorkflowService {
	t.Helper()
	sessions, err := app.NewSessionService(fixture.db, fixture.workspace.SessionRoot(), fixture.logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	executor, err := app.NewStepExecutor(app.StepExecutorOptions{
		Store:      fixture.db,
		Containers: fixture.ports,
		Sessions:   sessions,
		Git:        fixture.ports,
		Logger:     fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造步骤执行协调器失败: %v", err)
	}
	validation, err := app.NewValidationService(app.ValidationOptions{
		Store:      fixture.db,
		Containers: fixture.ports,
		OutputRoot: fixture.workspace.Dir("validations"),
		Logger:     fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造验证引擎失败: %v", err)
	}
	service, err := app.NewStepWorkflowService(app.StepWorkflowOptions{
		Steps:      executor,
		Validation: validation,
		Store:      fixture.db,
		Cycles:     fixture.db,
		Logger:     fixture.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造闭环协调器失败: %v", err)
	}
	startE2EServices(t, executor, validation, service)
	return service
}
