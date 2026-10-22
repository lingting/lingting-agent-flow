// Package app_test 提供计划生成的单元测试。
//
// 用例只依赖数据库与受控替身：Planner 调用由容器替身返回受控的最终文本，因此默认
// 执行路径不访问真实 Pi、模型、SSH 或 Docker，全部落在测试工作空间内。
package app_test

import (
	"context"
	"errors"
	"strings"
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

// validPlanJSON 是一份合法且可直接持久化的计划输出。
const validPlanJSON = `{"summary":"分两步实现需求","steps":[` +
	`{"id":"step-1","title":"实现接口","objective":"按需求实现接口",` +
	`"instructions":"只修改当前工作区","acceptance_criteria":["接口可以编译"]},` +
	`{"id":"step-2","title":"补齐测试","objective":"为接口补齐测试",` +
	`"instructions":"不要修改构建脚本","acceptance_criteria":["测试可以运行"]}]}`

// planFixture 是一套可发起计划生成的测试数据。
type planFixture struct {
	db         *sqlite.DB
	store      *planStore
	containers *taskWorkflowContainer
	service    *app.TaskWorkflowService
	task       domain.Task
	planner    domain.Role
	developer  domain.Role
	ctx        context.Context
	workspace  *testkit.Workspace
	now        time.Time
}

// planOptions 允许用例调整测试数据的构造方式。
type planOptions struct {
	// finalText 是 Planner 返回的最终文本。
	finalText string
	// reply 覆盖 Agent 调用的运行结果。
	reply fakeAgentReply
	// roleType 覆盖规划角色类型。
	roleType domain.RoleType
	// roleDisabled 表示规划角色被禁用。
	roleDisabled bool
	// noContainer 表示任务没有绑定容器。
	noContainer bool
	// taskStatus 覆盖任务初始状态。
	taskStatus domain.TaskStatus
	// existingSteps 表示任务已有计划步骤。
	existingSteps bool
	// noSessions 表示不提供会话服务。
	noSessions bool
	// createStepsErr 使计划写入步骤失败。
	createStepsErr error
	// updateTaskErr 使任务状态写入失败。
	updateTaskErr error
	// existingRun 表示预先写入一条未结束的工作流执行记录。
	existingRun bool
}

// planStore 包装真实存储，只把指定的写入方法替换为可控失败。
//
// 它是"计划事务写入失败"这条路径的注入点：业务代码不变，失败来自存储层，
// 因此验证的是真实的回滚与报告逻辑。
type planStore struct {
	app.TaskWorkflowStore
	updateTaskErr error
}

// UpdateTaskIfStatus 在配置了错误时返回该错误。
func (s *planStore) UpdateTaskIfStatus(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
) error {
	if s.updateTaskErr != nil {
		return s.updateTaskErr
	}
	return s.TaskWorkflowStore.UpdateTaskIfStatus(ctx, task, expected)
}

// newPlanFixture 在工作空间中准备一套可发起计划生成的测试数据。
func newPlanFixture(t *testing.T, options planOptions) *planFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	now := time.Now().UTC()
	roleType := domain.RoleTypePlanner
	if options.roleType != "" {
		roleType = options.roleType
	}
	planner := createManagementRole(t, db, ctx, roleType, "规划角色", now)
	developer := createManagementRole(t, db, ctx, domain.RoleTypeDeveloper, "开发角色", now)
	if options.roleDisabled {
		planner.Enabled = false
		planner.UpdatedAt = now
		if updateErr := db.UpdateRole(ctx, planner); updateErr != nil {
			t.Fatalf("禁用规划角色失败: %v", updateErr)
		}
	}
	host := createManagementHost(t, db, ctx, now)
	host = markHostNormal(t, db, ctx, host)
	requirement, err := domain.NewRequirement("需求标题", "需求正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "ssh://git.example.com/team/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &host.ID
	if !options.noContainer {
		task.ContainerID = "container-1"
	}
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
		Verification: domain.VerificationSpec{
			AcceptanceCriteria: []string{"任务级验收条件"},
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if options.taskStatus != "" {
		for _, status := range []domain.TaskStatus{
			domain.TaskStatusPreparing,
			options.taskStatus,
		} {
			if task.Status == status {
				continue
			}
			if transitionErr := task.Transition(status, now); transitionErr != nil {
				t.Fatalf("任务迁移到 %s 失败: %v", status, transitionErr)
			}
			if updateErr := db.UpdateTask(ctx, task); updateErr != nil {
				t.Fatalf("更新任务失败: %v", updateErr)
			}
		}
	}
	if options.existingSteps {
		step, stepErr := domain.NewAgentStep(
			task.ID,
			1,
			domain.StepTypeDevelop,
			"已存在的步骤",
			"按需求实现",
			"",
			developer,
			task.Config.Verification,
			domain.RetryConfig{MaxAttempts: 1},
			now,
		)
		if stepErr != nil {
			t.Fatalf("构造已有步骤失败: %v", stepErr)
		}
		if createErr := db.CreateSteps(ctx, []domain.Step{step}); createErr != nil {
			t.Fatalf("写入已有步骤失败: %v", createErr)
		}
	}
	if options.existingRun {
		run, runErr := domain.NewWorkflowRun(task.ID, []domain.Step{
			func() domain.Step {
				step, buildErr := domain.NewAgentStep(
					task.ID,
					1,
					domain.StepTypeDevelop,
					"工作流步骤",
					"按需求实现",
					"",
					developer,
					task.Config.Verification,
					domain.RetryConfig{MaxAttempts: 1},
					now,
				)
				if buildErr != nil {
					t.Fatalf("构造工作流步骤失败: %v", buildErr)
				}
				return step
			}(),
		}, now)
		if runErr != nil {
			t.Fatalf("构造执行记录失败: %v", runErr)
		}
		if createErr := db.CreateWorkflowRun(ctx, run); createErr != nil {
			t.Fatalf("写入执行记录失败: %v", createErr)
		}
	}
	store := &planStore{
		TaskWorkflowStore: db,
		updateTaskErr:     options.updateTaskErr,
	}
	containers := &taskWorkflowContainer{
		running:      true,
		agentReplies: []fakeAgentReply{planReply(options)},
	}
	serviceOptions := app.TaskWorkflowOptions{
		Store:      store,
		Steps:      &taskWorkflowRunner{db: db},
		Containers: containers,
		Logger:     logger.Slog(),
	}
	if !options.noSessions {
		sessions, sessionErr := app.NewSessionService(db, workspace.SessionRoot(), logger.Slog())
		if sessionErr != nil {
			t.Fatalf("构造会话服务失败: %v", sessionErr)
		}
		serviceOptions.Sessions = sessions
	}
	service, err := app.NewTaskWorkflowService(serviceOptions)
	if err != nil {
		t.Fatalf("构造工作流编排器失败: %v", err)
	}
	startTaskWorkflowService(t, service)
	return &planFixture{
		db:         db,
		store:      store,
		containers: containers,
		service:    service,
		task:       task,
		planner:    planner,
		developer:  developer,
		ctx:        ctx,
		workspace:  workspace,
		now:        now,
	}
}

// planReply 构造 Planner 调用的受控结果。
func planReply(options planOptions) fakeAgentReply {
	reply := fakeAgentReply{status: agent.RunCompleted, finalText: validPlanJSON}
	if options.reply.status != "" {
		reply = options.reply
	}
	if options.finalText != "" {
		reply.finalText = options.finalText
	}
	return reply
}

// steps 读取任务当前已持久化的步骤。
func (f *planFixture) steps(t *testing.T) []domain.Step {
	t.Helper()
	steps, err := f.db.ListStepsByTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return steps
}

// task 读取任务当前状态。
func (f *planFixture) taskStatus(t *testing.T) domain.Task {
	t.Helper()
	task, err := f.db.GetTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *task
}

// TestGeneratePlanPersistsPlan 校验合法输出被写入数据库并推进任务状态。
func TestGeneratePlanPersistsPlan(t *testing.T) {
	t.Parallel()
	fixture := newPlanFixture(t, planOptions{})

	result, err := fixture.service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("生成计划失败: %v", err)
	}
	if !result.Persisted {
		t.Fatalf("计划应写入数据库: %+v", result)
	}
	if result.TaskStatus != domain.TaskStatusPlanned {
		t.Fatalf("任务状态应为 planned，实际 %s", result.TaskStatus)
	}
	if result.Summary != "分两步实现需求" {
		t.Fatalf("计划摘要不符: %q", result.Summary)
	}
	if result.SessionID.IsEmpty() {
		t.Fatalf("必须记录规划会话标识")
	}
	steps := fixture.steps(t)
	// 计划固定以规划阶段步骤开头，其后是 Planner 生成的开发步骤。
	if len(steps) != 3 {
		t.Fatalf("步骤数量不符: %d", len(steps))
	}
	if steps[0].Type != domain.StepTypePlan {
		t.Fatalf("首个步骤必须是规划阶段: %s", steps[0].Type)
	}
	for index, step := range steps {
		if step.Sequence != index+1 {
			t.Fatalf("步骤序号不连续: %d", step.Sequence)
		}
		if index == 0 {
			continue
		}
		if step.Type != domain.StepTypeDevelop {
			t.Fatalf("步骤类型必须是开发步骤: %s", step.Type)
		}
		if step.Status != domain.StepStatusPending {
			t.Fatalf("新计划步骤应为 pending: %s", step.Status)
		}
		if !step.VerificationSpec.HasCommands() {
			t.Fatalf("任务级必需命令未合并进步骤 %d", step.Sequence)
		}
	}
	// 会话记录必须在进程启动之前写入并收敛为终态。
	sessions, err := fixture.db.ListSessionsByTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("一次规划应只创建一条会话，实际 %d", len(sessions))
	}
	if sessions[0].StepID != nil || sessions[0].AttemptID != nil {
		t.Fatalf("规划会话不应关联步骤或尝试: %+v", sessions[0])
	}
	if !sessions[0].Status.IsTerminal() {
		t.Fatalf("规划会话应收敛为终态: %s", sessions[0].Status)
	}
	// 计划摘要必须能从事件负载中追溯。
	events, err := fixture.db.ListEventsByTask(fixture.ctx, fixture.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type != domain.EventTypePlanCreated {
			continue
		}
		found = true
		if !strings.Contains(string(event.Payload), "分两步实现需求") {
			t.Fatalf("计划事件未包含摘要: %s", string(event.Payload))
		}
	}
	if !found {
		t.Fatalf("缺少 plan.created 事件")
	}
}

// TestGeneratePlanRejectsInvalidOutput 校验非法输出不会让任务进入 planned。
func TestGeneratePlanRejectsInvalidOutput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		finalText string
	}{
		{name: "空输出", finalText: "  "},
		{name: "自由文本", finalText: "我建议先实现接口，再补测试。"},
		{name: "非法 JSON", finalText: `{"summary":`},
		{name: "未知字段", finalText: `{"summary":"x","steps":[],"extra":1}`},
		{name: "空计划", finalText: `{"summary":"x","steps":[]}`},
		{name: "缺少验收条件", finalText: `{"summary":"x","steps":[{"id":"s1","title":"t","objective":"o"}]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPlanFixture(t, planOptions{finalText: testCase.finalText})
			result, err := fixture.service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
				TaskID: fixture.task.ID.String(),
			})
			if err == nil {
				t.Fatalf("非法输出必须返回错误")
			}
			if result.Persisted {
				t.Fatalf("非法输出不得写入计划")
			}
			if len(fixture.steps(t)) != 0 {
				t.Fatalf("非法输出不得留下任何步骤")
			}
			task := fixture.taskStatus(t)
			if task.Status == domain.TaskStatusPlanned {
				t.Fatalf("非法输出不得让任务进入 planned")
			}
			if task.Status != domain.TaskStatusFailed {
				t.Fatalf("任务应收敛为 failed，实际 %s", task.Status)
			}
		})
	}
}

// TestGeneratePlanConvergesOnCallFailure 校验调用失败按结论收敛任务状态。
func TestGeneratePlanConvergesOnCallFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		reply    fakeAgentReply
		expected domain.TaskStatus
	}{
		{
			name: "非零退出",
			reply: fakeAgentReply{
				status:   agent.RunFailed,
				failure:  "Pi 进程非零退出",
				exitCode: 1,
			},
			expected: domain.TaskStatusFailed,
		},
		{
			name: "调用超时",
			reply: fakeAgentReply{
				status:   agent.RunTimedOut,
				failure:  "调用超时被终止",
				exitCode: -1,
			},
			expected: domain.TaskStatusFailed,
		},
		{
			name: "调用方取消",
			reply: fakeAgentReply{
				status:   agent.RunCancelled,
				failure:  "调用方取消执行",
				exitCode: -1,
			},
			expected: domain.TaskStatusCancelled,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPlanFixture(t, planOptions{reply: testCase.reply})
			result, err := fixture.service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
				TaskID: fixture.task.ID.String(),
			})
			if err == nil {
				t.Fatalf("调用失败必须返回错误")
			}
			if result.Persisted {
				t.Fatalf("调用失败不得写入计划")
			}
			if len(fixture.steps(t)) != 0 {
				t.Fatalf("调用失败不得留下步骤")
			}
			task := fixture.taskStatus(t)
			if task.Status != testCase.expected {
				t.Fatalf("任务应收敛为 %s，实际 %s", testCase.expected, task.Status)
			}
		})
	}
}

// TestGeneratePlanRequiresSessions 校验缺少会话服务时直接拒绝。
func TestGeneratePlanRequiresSessions(t *testing.T) {
	t.Parallel()
	fixture := newPlanFixture(t, planOptions{noSessions: true})
	if _, err := fixture.service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
		TaskID: fixture.task.ID.String(),
	}); !errors.Is(err, app.ErrWorkflowPrerequisite) {
		t.Fatalf("缺少会话服务时错误类别不符: %v", err)
	}
}

// TestGeneratePlanRejectsPrerequisites 校验前置条件不满足时拒绝生成。
func TestGeneratePlanRejectsPrerequisites(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		options  planOptions
		expected error
	}{
		{
			name:     "缺少容器",
			options:  planOptions{noContainer: true},
			expected: app.ErrWorkflowConflict,
		},
		{
			name:     "已有计划",
			options:  planOptions{existingSteps: true},
			expected: app.ErrWorkflowConflict,
		},
		{
			name:     "已有未结束工作流",
			options:  planOptions{existingRun: true},
			expected: app.ErrWorkflowConflict,
		},
		{
			name:     "角色类型不符",
			options:  planOptions{roleType: domain.RoleTypeDeveloper},
			expected: app.ErrWorkflowPrerequisite,
		},
		{
			name:     "角色已禁用",
			options:  planOptions{roleDisabled: true},
			expected: app.ErrWorkflowPrerequisite,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPlanFixture(t, testCase.options)
			_, err := fixture.service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
				TaskID: fixture.task.ID.String(),
			})
			if err == nil {
				t.Fatalf("前置条件不满足时必须返回错误")
			}
			if !errors.Is(err, testCase.expected) {
				t.Fatalf("错误类别不符: %v", err)
			}
			if fixture.containers.agentCalls != 0 {
				t.Fatalf("前置条件不满足时不得调用 Planner")
			}
			if fixture.taskStatus(t).Status == domain.TaskStatusPlanned {
				t.Fatalf("拒绝时不得让任务进入 planned")
			}
		})
	}
}

// TestGeneratePlanRollsBackOnPersistFailure 校验计划写入失败整体回滚。
func TestGeneratePlanRollsBackOnPersistFailure(t *testing.T) {
	t.Parallel()
	fixture := newPlanFixture(t, planOptions{})
	// 通过状态条件更新失败模拟事务内写入失败：事务必须整体回滚，步骤不落库。
	fixture.store.updateTaskErr = storage.ErrConflict

	result, err := fixture.service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err == nil {
		t.Fatalf("写入失败必须返回错误")
	}
	if result.Persisted {
		t.Fatalf("写入失败不得报告计划已就绪")
	}
	if len(fixture.steps(t)) != 0 {
		t.Fatalf("事务回滚后不得留下步骤")
	}
}

// TestGeneratePlanRejectsConcurrentRequest 校验并发请求只有一个获得执行权。
func TestGeneratePlanRejectsConcurrentRequest(t *testing.T) {
	t.Parallel()
	fixture := newPlanFixture(t, planOptions{})
	// 先由另一个"进程"把任务推进到 planning：模拟跨进程并发已经占用。
	task := fixture.task
	if err := task.Transition(domain.TaskStatusPlanning, fixture.now); err != nil {
		t.Fatalf("任务迁移失败: %v", err)
	}
	if err := fixture.db.UpdateTask(fixture.ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	_, err := fixture.service.GeneratePlan(fixture.ctx, app.GeneratePlanRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err == nil {
		t.Fatalf("任务已被占用时必须返回错误")
	}
	if !errors.Is(err, app.ErrWorkflowConflict) {
		t.Fatalf("并发占用时错误类别不符: %v", err)
	}
	if fixture.containers.agentCalls != 0 {
		t.Fatalf("并发冲突时不得调用 Planner")
	}
}

// TestRecoverPlanGenerationConvergesPlanningTasks 校验启动恢复不重放 Planner 调用。
func TestRecoverPlanGenerationConvergesPlanningTasks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		steps    bool
		expected domain.TaskStatus
	}{
		{name: "已有步骤收敛为 planned", steps: true, expected: domain.TaskStatusPlanned},
		{name: "没有步骤收敛为需要核验", steps: false, expected: domain.TaskStatusRecoveryRequired},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPlanFixture(t, planOptions{existingSteps: testCase.steps})
			task := fixture.task
			if err := task.Transition(domain.TaskStatusPlanning, fixture.now); err != nil {
				t.Fatalf("任务迁移失败: %v", err)
			}
			if err := fixture.db.UpdateTask(fixture.ctx, task); err != nil {
				t.Fatalf("写入任务失败: %v", err)
			}
			// Start 内部会执行恢复：这里用第二个服务实例模拟服务重启。
			logger, err := testkit.Logger(logging.LevelError)
			if err != nil {
				t.Fatalf("构造日志器失败: %v", err)
			}
			sessions, err := app.NewSessionService(
				fixture.db,
				fixture.workspace.SessionRoot(),
				logger.Slog(),
			)
			if err != nil {
				t.Fatalf("构造会话服务失败: %v", err)
			}
			restarted, err := app.NewTaskWorkflowService(app.TaskWorkflowOptions{
				Store:      fixture.db,
				Steps:      &taskWorkflowRunner{db: fixture.db},
				Containers: &taskWorkflowContainer{running: true},
				Sessions:   sessions,
				Logger:     logger.Slog(),
			})
			if err != nil {
				t.Fatalf("构造工作流编排器失败: %v", err)
			}
			startTaskWorkflowService(t, restarted)
			if actual := fixture.taskStatus(t).Status; actual != testCase.expected {
				t.Fatalf("恢复后任务状态应为 %s，实际 %s", testCase.expected, actual)
			}
		})
	}
}
