package workflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// restartFixture 是一套可跨越"进程退出 → 重新打开数据库"的测试现场。
//
// 数据库文件、Session 根目录与私钥都落在同一个用例工作空间内；重启只是关闭连接后
// 用同一路径重新打开，因此断言的是真实落盘的持久化事实。
type restartFixture struct {
	ctx       context.Context
	workspace *testkit.Workspace
	path      string
	db        *sqlite.DB
	logger    *logging.Logger
	now       time.Time
	planner   domain.Role
	developer domain.Role
	host      domain.ExecutionHost
}

// newRestartFixture 在工作空间中创建数据库、角色、执行节点与需求。
func newRestartFixture(t *testing.T) *restartFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	workspace := testkit.New(t)
	path := filepath.Join(workspace.Root(), "agentflow.db")
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	fixture := &restartFixture{
		ctx:       ctx,
		workspace: workspace,
		path:      path,
		logger:    logger,
		now:       time.Now().UTC(),
	}
	fixture.db = fixture.open(t)
	seeds, err := app.RoleSeeds(&config.Config{
		Roles: []config.RoleConfig{
			{
				ID:           "planner",
				Name:         "架构规划师",
				Type:         domain.RoleTypePlanner,
				Runner:       config.RunnerPi,
				Model:        "group/free",
				SystemPrompt: "规划提示词",
			},
			{
				ID:           "developer",
				Name:         "开发工程师",
				Type:         domain.RoleTypeDeveloper,
				Runner:       config.RunnerPi,
				Model:        "group/free",
				SystemPrompt: "开发提示词",
			},
		},
	})
	if err != nil {
		t.Fatalf("转换角色失败: %v", err)
	}
	if _, err := fixture.db.SeedRoles(ctx, seeds); err != nil {
		t.Fatalf("初始化角色失败: %v", err)
	}
	planner, err := fixture.db.GetRole(ctx, "planner")
	if err != nil {
		t.Fatalf("读取规划角色失败: %v", err)
	}
	developer, err := fixture.db.GetRole(ctx, "developer")
	if err != nil {
		t.Fatalf("读取开发角色失败: %v", err)
	}
	fixture.planner = *planner
	fixture.developer = *developer
	fixture.host = fixture.createHost(t)
	return fixture
}

// open 打开（或重新打开）数据库并注册关闭清理。
func (f *restartFixture) open(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(f.ctx, sqlite.Options{Path: f.path})
	if err != nil {
		t.Fatalf("打开数据库失败: %s: %v", f.path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	return db
}

// restart 关闭当前连接并用同一文件重新打开，模拟进程退出后重新启动。
func (f *restartFixture) restart(t *testing.T) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	f.db = f.open(t)
}

// createHost 写入一个已通过环境预检的执行节点。
func (f *restartFixture) createHost(t *testing.T) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost(
		"",
		"测试节点",
		"10.0.0.1",
		22,
		"deploy",
		"/srv/agentflow",
		f.now,
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := f.db.CreateExecutionHost(f.ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	stored, err := f.db.GetExecutionHost(f.ctx, host.ID)
	if err != nil {
		t.Fatalf("读取执行节点失败: %v", err)
	}
	details := domain.ValidationDetails{
		ValidatorVersion: domain.ValidatorVersion,
		Checks: []domain.ValidationCheck{
			{Name: domain.ValidationCheckSSHConnection, Status: domain.CheckStatusPassed},
		},
	}
	if err := stored.RecordValidationResult(
		stored.ValidationGeneration,
		details,
		f.now,
	); err != nil {
		t.Fatalf("记录校验结果失败: %v", err)
	}
	if _, err := f.db.UpdateExecutionHostValidation(
		f.ctx,
		*stored,
		stored.ValidationGeneration,
	); err != nil {
		t.Fatalf("写入校验结果失败: %v", err)
	}
	return *stored
}

// createTask 写入一个绑定容器与角色的任务，并推进到指定状态。
func (f *restartFixture) createTask(
	t *testing.T,
	status domain.TaskStatus,
	cancelRequested bool,
) domain.Task {
	t.Helper()
	requirement, err := domain.NewRequirement(
		"需求标题",
		"需求正文",
		domain.PriorityNormal,
		f.now,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := f.db.CreateRequirement(f.ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(
		requirement.ID,
		"ssh://git.example.com/team/repo",
		"main",
		"",
		f.now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &f.host.ID
	task.ContainerID = "container-1"
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   f.planner.ID,
		DeveloperRoleID: f.developer.ID,
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
	for _, next := range statusPath(status) {
		if task.Status == next {
			continue
		}
		if err := task.Transition(next, f.now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", next, err)
		}
		if err := f.db.UpdateTask(f.ctx, task); err != nil {
			t.Fatalf("更新任务失败: %v", err)
		}
	}
	if cancelRequested {
		if err := task.RequestCancel(f.now); err != nil {
			t.Fatalf("记录取消请求失败: %v", err)
		}
		if err := f.db.UpdateTask(f.ctx, task); err != nil {
			t.Fatalf("更新任务失败: %v", err)
		}
	}
	return task
}

// statusPath 返回从 queued 合法到达目标状态的最小迁移路径。
func statusPath(target domain.TaskStatus) []domain.TaskStatus {
	switch target {
	case domain.TaskStatusPlanning:
		return []domain.TaskStatus{domain.TaskStatusPreparing, domain.TaskStatusPlanning}
	case domain.TaskStatusPlanned:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanning,
			domain.TaskStatusPlanned,
		}
	case domain.TaskStatusCancelling:
		return []domain.TaskStatus{domain.TaskStatusPreparing, domain.TaskStatusCancelling}
	case domain.TaskStatusDeveloping:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanning,
			domain.TaskStatusPlanned,
			domain.TaskStatusDeveloping,
		}
	default:
		return nil
	}
}

// writePlanSteps 直接写入一组计划步骤，模拟"事务已提交"的现场。
func (f *restartFixture) writePlanSteps(t *testing.T, task domain.Task, count int) []domain.Step {
	t.Helper()
	steps := make([]domain.Step, 0, count)
	for index := 0; index < count; index++ {
		step, err := domain.NewAgentStep(
			task.ID,
			index+1,
			domain.StepTypeDevelop,
			"实现步骤",
			"按需求实现",
			"",
			f.developer,
			task.Config.Verification,
			domain.RetryConfig{MaxAttempts: 1},
			f.now,
		)
		if err != nil {
			t.Fatalf("构造步骤失败: %v", err)
		}
		steps = append(steps, step)
	}
	if err := f.db.CreateSteps(f.ctx, steps); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return steps
}

// startService 构造并启动一个编排器实例。
func (f *restartFixture) startService(
	t *testing.T,
	store app.TaskWorkflowStore,
	containers app.WorkflowContainerPort,
	runner app.WorkflowStepRunner,
) (*app.TaskWorkflowService, error) {
	t.Helper()
	if store == nil {
		store = f.db
	}
	if containers == nil {
		containers = &fakeContainer{running: true}
	}
	if runner == nil {
		runner = &fakeRunner{}
	}
	sessions, err := app.NewSessionService(f.db, f.workspace.SessionRoot(), f.logger.Slog())
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	service, err := app.NewTaskWorkflowService(app.TaskWorkflowOptions{
		Store:      store,
		Steps:      runner,
		Containers: containers,
		Sessions:   sessions,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造工作流编排器失败: %v", err)
	}
	return service, service.Start(f.ctx)
}

// mustStartService 构造并启动编排器，启动失败时终止用例。
func (f *restartFixture) mustStartService(
	t *testing.T,
	store app.TaskWorkflowStore,
	containers app.WorkflowContainerPort,
	runner app.WorkflowStepRunner,
) *app.TaskWorkflowService {
	t.Helper()
	service, err := f.startService(t, store, containers, runner)
	if err != nil {
		t.Fatalf("启动工作流编排器失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := service.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止工作流编排器失败: %v", stopErr)
		}
	})
	return service
}

// task 读取任务当前状态。
func (f *restartFixture) task(t *testing.T, id domain.ID) domain.Task {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, id)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *task
}

// steps 读取任务当前步骤。
func (f *restartFixture) steps(t *testing.T, id domain.ID) []domain.Step {
	t.Helper()
	steps, err := f.db.ListStepsByTask(f.ctx, id)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return steps
}

// events 读取任务事件。
func (f *restartFixture) events(t *testing.T, id domain.ID) []domain.Event {
	t.Helper()
	events, err := f.db.ListEventsByTask(f.ctx, id, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	return events
}

// sessions 读取任务会话。
func (f *restartFixture) sessions(t *testing.T, id domain.ID) []domain.Session {
	t.Helper()
	sessions, err := f.db.ListSessionsByTask(f.ctx, id)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	return sessions
}

// runs 读取任务的执行记录。
func (f *restartFixture) runs(t *testing.T, id domain.ID) []domain.WorkflowRun {
	t.Helper()
	runs, err := f.db.ListWorkflowRunsByTask(f.ctx, id)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	return runs
}

// countEvents 统计指定类型的事件数量。
func countEvents(events []domain.Event, eventType domain.EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

// assertNoRunningResidue 断言不存在 running 的会话或执行记录。
func assertNoRunningResidue(t *testing.T, fixture *restartFixture, taskID domain.ID) {
	t.Helper()
	for _, session := range fixture.sessions(t, taskID) {
		if session.Status == domain.SessionStatusRunning ||
			session.Status == domain.SessionStatusStarting {
			t.Fatalf("会话 %s 仍停留在 %s", session.ID, session.Status)
		}
	}
	for _, run := range fixture.runs(t, taskID) {
		if run.Status == domain.WorkflowRunRunning {
			t.Fatalf("执行记录 %s 仍停留在 running", run.ID)
		}
	}
}

// discardWriter 丢弃日志输出。
type discardWriter struct{}

// Write 丢弃全部写入内容。
func (discardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

// TestRecoverPlanGenerationOnRestart 验证重启后按持久化事实收敛计划生成现场。
//
// 两条路径都必须被覆盖：事务已提交（有步骤 → planned）与无法确认（无步骤 →
// recovery_required）。两者都不得重放 Planner。
func TestRecoverPlanGenerationOnRestart(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		steps       int
		expected    domain.TaskStatus
		expectEvent domain.EventType
	}{
		{
			name:        "计划已落库收敛为 planned",
			steps:       2,
			expected:    domain.TaskStatusPlanned,
			expectEvent: domain.EventTypePlanCreated,
		},
		{
			name:        "没有步骤收敛为需要核验",
			steps:       0,
			expected:    domain.TaskStatusRecoveryRequired,
			expectEvent: domain.EventTypeTaskRecoveryRequired,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRestartFixture(t)
			task := fixture.createTask(t, domain.TaskStatusPlanning, false)
			if testCase.steps > 0 {
				fixture.writePlanSteps(t, task, testCase.steps)
			}
			fixture.restart(t)
			containers := &fakeContainer{running: true}
			runner := &fakeRunner{}
			fixture.mustStartService(t, nil, containers, runner)

			recovered := fixture.task(t, task.ID)
			if recovered.Status != testCase.expected {
				t.Fatalf("重启后任务状态应为 %s，实际 %s", testCase.expected, recovered.Status)
			}
			if testCase.expected == domain.TaskStatusRecoveryRequired {
				if recovered.FailureSummary == "" {
					t.Fatal("收敛为 recovery_required 时必须给出可读原因")
				}
			}
			if testCase.steps > 0 && len(fixture.steps(t, task.ID)) != testCase.steps {
				t.Fatalf("步骤不应被改写: %d", len(fixture.steps(t, task.ID)))
			}
			events := fixture.events(t, task.ID)
			if countEvents(events, testCase.expectEvent) != 1 {
				t.Fatalf("恢复事件 %s 数量不符: %+v", testCase.expectEvent, events)
			}
			if containers.callCount() != 0 {
				t.Fatalf("恢复不得重放 Planner，实际调用 %d 次", containers.callCount())
			}
			if runner.callCount() != 0 {
				t.Fatalf("恢复不得执行步骤，实际调用 %d 次", runner.callCount())
			}
		})
	}
}

// TestRecoverPlanGenerationFailureBlocksStartup 验证恢复写入失败时启动失败。
//
// 恢复结论没有落库时服务继续启动，会让残留现场被当作已收敛而无人处理。
func TestRecoverPlanGenerationFailureBlocksStartup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		store  *failingStore
		status domain.TaskStatus
	}{
		{
			name:   "读取计划步骤失败",
			store:  &failingStore{listStepsErr: errors.New("数据库读取失败")},
			status: domain.TaskStatusPlanning,
		},
		{
			name:   "写入任务状态失败",
			store:  &failingStore{updateTaskErr: errors.New("数据库写入失败")},
			status: domain.TaskStatusPlanning,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRestartFixture(t)
			task := fixture.createTask(t, domain.TaskStatusPlanning, false)
			fixture.restart(t)
			testCase.store.TaskWorkflowStore = fixture.db
			if _, err := fixture.startService(t, testCase.store, nil, nil); err == nil {
				t.Fatal("恢复失败时启动必须返回错误")
			}
			// 现场保持原状：既不推进状态，也不追加"已恢复"的事件。
			if actual := fixture.task(t, task.ID).Status; actual != testCase.status {
				t.Fatalf("恢复失败时任务状态不得变化，实际 %s", actual)
			}
			events := fixture.events(t, task.ID)
			if countEvents(events, domain.EventTypePlanCreated) != 0 ||
				countEvents(events, domain.EventTypeTaskRecoveryRequired) != 0 {
				t.Fatalf("恢复失败时不得写入恢复事件: %+v", events)
			}
		})
	}
}

// TestRecoverUnfinishedRunFailureBlocksStartup 验证执行记录恢复失败时启动失败。
func TestRecoverUnfinishedRunFailureBlocksStartup(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusPlanned, false)
	steps := fixture.writePlanSteps(t, task, 1)
	run := writeStaleRun(t, fixture, task, steps)
	fixture.restart(t)
	store := &failingStore{
		TaskWorkflowStore: fixture.db,
		updateRunErr:      errors.New("数据库写入失败"),
	}
	if _, err := fixture.startService(t, store, nil, nil); err == nil {
		t.Fatal("执行记录恢复失败时启动必须返回错误")
	}
	stored, err := fixture.db.GetWorkflowRun(fixture.ctx, run.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if stored.Status != domain.WorkflowRunRunning {
		t.Fatalf("恢复失败时执行记录必须保持原状，实际 %s", stored.Status)
	}
}

// writeStaleRun 写入一条"上一次进程退出留下的 running 残留"。
func writeStaleRun(
	t *testing.T,
	fixture *restartFixture,
	task domain.Task,
	steps []domain.Step,
) domain.WorkflowRun {
	t.Helper()
	run, err := domain.NewWorkflowRun(task.ID, steps, fixture.now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("构造执行记录失败: %v", err)
	}
	if err := run.Start("dead-worker", time.Millisecond, fixture.now.Add(-time.Hour)); err != nil {
		t.Fatalf("模拟残留执行失败: %v", err)
	}
	if err := fixture.db.CreateWorkflowRun(fixture.ctx, run); err != nil {
		t.Fatalf("写入执行记录失败: %v", err)
	}
	return run
}

// TestRecoverUnfinishedRunOnRestart 验证重启后未结束的执行记录被收敛。
func TestRecoverUnfinishedRunOnRestart(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusPlanned, false)
	steps := fixture.writePlanSteps(t, task, 1)
	run := writeStaleRun(t, fixture, task, steps)
	fixture.restart(t)
	fixture.mustStartService(t, nil, nil, nil)

	stored, err := fixture.db.GetWorkflowRun(fixture.ctx, run.ID)
	if err != nil {
		t.Fatalf("读取执行记录失败: %v", err)
	}
	if stored.Status != domain.WorkflowRunBlocked {
		t.Fatalf("租约过期的残留应收敛为 blocked，实际 %s", stored.Status)
	}
	if stored.ErrorClass != "worker_interrupted" {
		t.Fatalf("错误分类不符: %s", stored.ErrorClass)
	}
	assertNoRunningResidue(t, fixture, task.ID)
}

// TestRecoverCancellingOnRestart 验证残留 cancelling 任务按证据收敛。
//
// 三条路径的判定规则集中在 domain.DecideCancellationRecovery：存在未结束执行或
// 缺少取消请求时不得判定取消生效。
func TestRecoverCancellingOnRestart(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		cancelRequested bool
		withStaleRun    bool
		expected        domain.TaskStatus
		expectedEvent   domain.EventType
	}{
		{
			name:            "已记录取消请求且无未结束执行收敛为已取消",
			cancelRequested: true,
			expected:        domain.TaskStatusCancelled,
			expectedEvent:   domain.EventTypeTaskCancelled,
		},
		{
			name:            "缺少取消请求收敛为需要核验",
			cancelRequested: false,
			expected:        domain.TaskStatusRecoveryRequired,
			expectedEvent:   domain.EventTypeTaskRecoveryRequired,
		},
		{
			name:            "存在未结束执行收敛为需要核验",
			cancelRequested: true,
			withStaleRun:    true,
			expected:        domain.TaskStatusRecoveryRequired,
			expectedEvent:   domain.EventTypeTaskRecoveryRequired,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRestartFixture(t)
			task := fixture.createTask(
				t,
				domain.TaskStatusCancelling,
				testCase.cancelRequested,
			)
			if testCase.withStaleRun {
				steps := fixture.writePlanSteps(t, task, 1)
				writeStaleRun(t, fixture, task, steps)
			}
			fixture.restart(t)
			fixture.mustStartService(t, nil, nil, nil)

			recovered := fixture.task(t, task.ID)
			if recovered.Status != testCase.expected {
				t.Fatalf("重启后任务状态应为 %s，实际 %s", testCase.expected, recovered.Status)
			}
			if testCase.expected == domain.TaskStatusCancelled {
				if recovered.FinalResult != domain.TaskResultCancelled {
					t.Fatalf("收敛为已取消时必须写入取消结论，实际 %q", recovered.FinalResult)
				}
			}
			events := fixture.events(t, task.ID)
			if countEvents(events, testCase.expectedEvent) != 1 {
				t.Fatalf("恢复事件 %s 数量不符: %+v", testCase.expectedEvent, events)
			}
			assertNoRunningResidue(t, fixture, task.ID)
		})
	}
}

// TestRecoverCancellingFailureBlocksStartup 验证取消现场恢复失败时启动失败。
func TestRecoverCancellingFailureBlocksStartup(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusCancelling, true)
	fixture.restart(t)
	store := &failingStore{
		TaskWorkflowStore: fixture.db,
		updateTaskErr:     errors.New("数据库写入失败"),
	}
	if _, err := fixture.startService(t, store, nil, nil); err == nil {
		t.Fatal("取消现场恢复失败时启动必须返回错误")
	}
	if actual := fixture.task(t, task.ID).Status; actual != domain.TaskStatusCancelling {
		t.Fatalf("恢复失败时任务状态不得变化，实际 %s", actual)
	}
}

// TestRecoverCancellingActiveRunReadFailure 验证读取未结束执行失败时不得判定取消生效。
func TestRecoverCancellingActiveRunReadFailure(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusCancelling, true)
	fixture.restart(t)
	store := &failingStore{
		TaskWorkflowStore: fixture.db,
		activeRunErr:      errors.New("数据库读取失败"),
	}
	if _, err := fixture.startService(t, store, nil, nil); err == nil {
		t.Fatal("读取未结束执行失败时启动必须返回错误")
	}
	if actual := fixture.task(t, task.ID).Status; actual != domain.TaskStatusCancelling {
		t.Fatalf("恢复失败时任务状态不得变化，实际 %s", actual)
	}
}

// TestConcurrentRecoveryConvergesOnce 验证并发恢复不会重复推进同一任务。
//
// 两个实例共享同一数据库文件同时启动：状态迁移以状态为前置条件，只有一个能写入，
// 因此最终状态唯一且恢复事件只有一条。
func TestConcurrentRecoveryConvergesOnce(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusPlanning, false)
	fixture.writePlanSteps(t, task, 1)
	fixture.restart(t)

	// 两个实例必须同时进入恢复：用同一启动信号放行，避免顺序执行掩盖竞争。
	services := make([]*app.TaskWorkflowService, 2)
	errs := make([]error, 2)
	var ready, done sync.WaitGroup
	ready.Add(2)
	done.Add(2)
	for index := range services {
		index := index
		go func() {
			defer done.Done()
			sessions, err := app.NewSessionService(
				fixture.db,
				fixture.workspace.SessionRoot(),
				fixture.logger.Slog(),
			)
			if err != nil {
				errs[index] = err
				ready.Done()
				return
			}
			service, err := app.NewTaskWorkflowService(app.TaskWorkflowOptions{
				Store:      fixture.db,
				Steps:      &fakeRunner{},
				Containers: &fakeContainer{running: true},
				Sessions:   sessions,
				Logger:     fixture.logger.Slog(),
			})
			if err != nil {
				errs[index] = err
				ready.Done()
				return
			}
			services[index] = service
			ready.Done()
			ready.Wait()
			errs[index] = service.Start(fixture.ctx)
		}()
	}
	done.Wait()
	// 两个实例都可能成功启动：恢复是"尽力收敛"的幂等操作，竞争结果由数据库状态
	// 前置条件决定，而不是由启动返回值决定。因此这里断言持久化事实，而不是成功计数。
	for _, err := range errs {
		if err != nil && !errors.Is(err, app.ErrWorkflowConflict) {
			t.Fatalf("并发恢复只允许状态冲突，实际: %v", err)
		}
	}
	for _, service := range services {
		if service == nil {
			continue
		}
		service := service
		t.Cleanup(func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if stopErr := service.Stop(stopCtx); stopErr != nil {
				t.Errorf("停止工作流编排器失败: %v", stopErr)
			}
		})
	}
	recovered := fixture.task(t, task.ID)
	if recovered.Status != domain.TaskStatusPlanned {
		t.Fatalf("并发恢复后任务状态应为 planned，实际 %s", recovered.Status)
	}
	events := fixture.events(t, task.ID)
	if count := countEvents(events, domain.EventTypePlanCreated); count > 1 {
		t.Fatalf("并发恢复不得重复写入恢复事件，实际 %d 条", count)
	}
}

// TestPlanGenerationInterruptedByShutdown 验证服务关闭时在途 Planner 不留下 running。
//
// 关闭发生在 Planner 调用进行中：调用被取消，任务必须收敛为需要人工核验，会话与
// 执行记录都不得停留在 running，也不得写入任何计划步骤。
func TestPlanGenerationInterruptedByShutdown(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusPreparing, false)

	containers := &fakeContainer{running: true}
	service := fixture.mustStartService(t, nil, containers, nil)
	// 在 Planner 调用期间停止服务：模拟进程关闭中断在途调用。
	//
	// waitForCancel 让替身一直等到本次调用的上下文被取消后才返回，从而把
	// "中断发生在调用进行中"变成确定的先后关系：没有它，替身可能在 Stop 传播到
	// 本次调用之前就返回结果，结论便取决于 goroutine 调度顺序。
	containers.setReplies(agentReply{
		status:        agent.RunCancelled,
		failure:       "服务关闭取消在途调用",
		exitCode:      -1,
		waitForCancel: true,
		duringCall: func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := service.Stop(stopCtx); err != nil {
				t.Errorf("停止工作流编排器失败: %v", err)
			}
		},
	})
	if _, err := service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
		TaskID: task.ID.String(),
	}); err == nil {
		t.Fatal("服务关闭中断的规划调用必须返回错误")
	}

	recovered := fixture.task(t, task.ID)
	if recovered.Status != domain.TaskStatusRecoveryRequired {
		t.Fatalf("中断的规划应收敛为 recovery_required，实际 %s", recovered.Status)
	}
	if len(fixture.steps(t, task.ID)) != 0 {
		t.Fatal("中断的规划不得留下步骤")
	}
	if count := countEvents(fixture.events(t, task.ID), domain.EventTypePlanCreated); count != 0 {
		t.Fatalf("中断的规划不得写入计划事件，实际 %d 条", count)
	}
	assertNoRunningResidue(t, fixture, task.ID)

	// 重启后不得出现新的 running 残留，也不得重放 Planner。
	fixture.restart(t)
	restarted := &fakeContainer{running: true}
	fixture.mustStartService(t, nil, restarted, nil)
	if restarted.callCount() != 0 {
		t.Fatalf("重启恢复不得重放 Planner，实际调用 %d 次", restarted.callCount())
	}
	assertNoRunningResidue(t, fixture, task.ID)
}

// TestPlanGenerationPersistsAcrossRestart 验证真实调用成功后计划在重启后仍可用。
func TestPlanGenerationPersistsAcrossRestart(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusPreparing, false)
	service := fixture.mustStartService(t, nil, nil, nil)
	result, err := service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
		TaskID: task.ID.String(),
	})
	if err != nil {
		t.Fatalf("生成计划失败: %v", err)
	}
	if !result.Persisted {
		t.Fatal("成功生成计划时必须报告已持久化")
	}
	fixture.restart(t)
	restarted := &fakeContainer{running: true}
	fixture.mustStartService(t, nil, restarted, nil)

	recovered := fixture.task(t, task.ID)
	if recovered.Status != domain.TaskStatusPlanned {
		t.Fatalf("重启后任务状态应为 planned，实际 %s", recovered.Status)
	}
	steps := fixture.steps(t, task.ID)
	if len(steps) != len(result.Steps) {
		t.Fatalf("重启后步骤数量不符: %d != %d", len(steps), len(result.Steps))
	}
	if restarted.callCount() != 0 {
		t.Fatalf("重启恢复不得重放 Planner，实际调用 %d 次", restarted.callCount())
	}
	// 已确认成功的计划不得被恢复流程改写为需要核验。
	if count := countEvents(
		fixture.events(t, task.ID),
		domain.EventTypeTaskRecoveryRequired,
	); count != 0 {
		t.Fatalf("已完成的计划不得被标记为需要核验，实际 %d 条", count)
	}
}

// TestConcurrentGeneratePlanRejectsSecond 验证跨进程并发只有一个能生成计划。
func TestConcurrentGeneratePlanRejectsSecond(t *testing.T) {
	t.Parallel()
	fixture := newRestartFixture(t)
	task := fixture.createTask(t, domain.TaskStatusPreparing, false)
	service := fixture.mustStartService(t, nil, nil, nil)

	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	results := make([]error, 2)
	for index := range results {
		index := index
		go func() {
			defer waitGroup.Done()
			_, results[index] = service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
				TaskID: task.ID.String(),
			})
		}()
	}
	waitGroup.Wait()

	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("并发规划只允许一个成功，实际成功 %d 次", succeeded)
	}
	steps := fixture.steps(t, task.ID)
	if len(steps) == 0 {
		t.Fatal("成功的规划必须留下计划步骤")
	}
	if count := countEvents(fixture.events(t, task.ID), domain.EventTypePlanCreated); count != 1 {
		t.Fatalf("计划事件只能写入一次，实际 %d 条", count)
	}
}
