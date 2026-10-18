package workflow_test

import (
	"context"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/internal/workflow"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"github.com/lingting/agentflow/tests/internal/workflowfake"
)

// TestWorkflowSurvivesDatabaseReopen 校验任务执行历史在进程重启后仍可查询。
func TestWorkflowSurvivesDatabaseReopen(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	path := ws.SQLitePath()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	db := openDatabase(t, path)
	ports := workflowfake.New()
	ports.Agents.Store = db
	ports.Agents.SetReplies(domain.RoleTypePlanner, workflowfake.AgentReply{FinalText: plan()})
	ports.Agents.SetReplies(
		domain.RoleTypeValidator,
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "符合验收条件")},
	)
	fixture := seedFixture(t, db, ctx)
	engine := newEngine(t, db, ports)
	if err := engine.RunTask(ctx, fixture.Task.ID); err != nil {
		t.Fatalf("推进任务失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 重新打开同一个数据库文件：历史必须完整可读。
	reopened := openDatabase(t, path)
	task, err := reopened.GetTask(ctx, fixture.Task.ID)
	if err != nil {
		t.Fatalf("重启后读取任务失败: %v", err)
	}
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("重启后任务状态不符: %s", task.Status)
	}
	steps, err := reopened.ListStepsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("重启后读取步骤失败: %v", err)
	}
	if len(developmentSteps(steps)) != 1 {
		t.Fatalf("重启后步骤数量不符: %d", len(steps))
	}
	sessions, err := reopened.ListSessionsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("重启后读取会话失败: %v", err)
	}
	// 规划、开发与辅助评估各自使用独立 Session。
	if len(sessions) != 3 {
		t.Fatalf("重启后会话数量不符: %d", len(sessions))
	}
	invocations := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if session.InvocationID == "" {
			t.Fatalf("会话必须记录调用标识: %+v", session)
		}
		if _, duplicated := invocations[session.InvocationID]; duplicated {
			t.Fatalf("会话调用标识重复: %s", session.InvocationID)
		}
		invocations[session.InvocationID] = struct{}{}
	}
	events, err := reopened.ListEventsByTask(ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("重启后读取事件失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("重启后事件历史不应为空")
	}
}

// TestConcurrentTasksStayIsolated 校验两个任务并发执行时不互相干扰。
func TestConcurrentTasksStayIsolated(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	path := ws.SQLitePath()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	db := openDatabase(t, path)

	ports := workflowfake.New()
	ports.Agents.Store = db
	ports.Agents.SetReplies(
		domain.RoleTypePlanner,
		workflowfake.AgentReply{FinalText: plan()},
		workflowfake.AgentReply{FinalText: plan()},
	)
	ports.Agents.SetReplies(
		domain.RoleTypeValidator,
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "符合验收条件")},
		workflowfake.AgentReply{FinalText: workflowfake.AssessmentJSON(true, "符合验收条件")},
	)
	first := seedFixture(t, db, ctx)
	second := seedFixture(t, db, ctx)
	engine := newEngine(t, db, ports)
	results := engine.RunTasks(ctx, []domain.ID{first.Task.ID, second.Task.ID})
	for taskID, runErr := range results {
		if runErr != nil {
			t.Fatalf("任务 %s 推进失败: %v", taskID, runErr)
		}
	}
	for _, taskID := range []domain.ID{first.Task.ID, second.Task.ID} {
		task, err := db.GetTask(ctx, taskID)
		if err != nil {
			t.Fatalf("读取任务失败: %v", err)
		}
		if task.Status != domain.TaskStatusCompleted {
			t.Fatalf("任务 %s 状态不符: %s", taskID, task.Status)
		}
		steps, err := db.ListStepsByTask(ctx, taskID)
		if err != nil {
			t.Fatalf("读取步骤失败: %v", err)
		}
		for _, step := range steps {
			if step.TaskID != taskID {
				t.Fatalf("步骤归属串扰: 步骤 %s 属于 %s，期望 %s", step.ID, step.TaskID, taskID)
			}
		}
		sessions, err := db.ListSessionsByTask(ctx, taskID)
		if err != nil {
			t.Fatalf("读取会话失败: %v", err)
		}
		for _, session := range sessions {
			if session.TaskID != taskID {
				t.Fatalf("会话归属串扰: 会话 %s 属于 %s", session.ID, session.TaskID)
			}
		}
	}
}

// openDatabase 打开数据库并注册关闭逻辑。
func openDatabase(t *testing.T, path string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %s: %v", path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	return db
}

// workflowFixture 是一次集成测试的任务数据。
type workflowFixture struct {
	Task domain.Task
}

// seedFixture 在给定数据库中写入一套完整任务数据。
func seedFixture(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
) workflowFixture {
	t.Helper()
	now := time.Now().UTC()
	planner := createRole(t, db, ctx, domain.RoleTypePlanner, "规划师-"+now.String(), now)
	developer := createRole(t, db, ctx, domain.RoleTypeDeveloper, "开发-"+now.String(), now)
	validator := createRole(t, db, ctx, domain.RoleTypeValidator, "验证-"+now.String(), now)
	host, err := domain.NewExecutionHost("", "节点-"+now.String(), "127.0.0.1", 0, "deploy", "", now)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	requirement, err := domain.NewRequirement("集成需求", "集成需求正文", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(
		requirement.ID,
		"ssh://git.example.com/team/repo",
		"main",
		"",
		now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	environment, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造环境引用失败: %v", err)
	}
	task.Config = domain.TaskConfig{
		Goal:            "实现集成测试目标",
		Environment:     environment,
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
		ValidatorRoleID: validator.ID,
	}
	task.HostID = &host.ID
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return workflowFixture{Task: task}
}

// createRole 创建并写入一个角色。
func createRole(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	roleType domain.RoleType,
	name string,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		name,
		roleType,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(roleType),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// newEngine 构造绑定到数据库的工作流引擎。
func newEngine(
	t *testing.T,
	db *sqlite.DB,
	ports *workflowfake.Ports,
) *workflow.Engine {
	t.Helper()
	engine, err := workflow.NewEngine(workflow.Options{
		Store:       db,
		Ports:       ports.Ports(),
		Owner:       "integration-worker",
		MaxParallel: 4,
	})
	if err != nil {
		t.Fatalf("构造工作流引擎失败: %v", err)
	}
	return engine
}

// plan 返回一份合法的单步骤计划。
func plan() string {
	return workflowfake.PlanJSON(
		"实现目标",
		workflowfake.PlanStep{
			Title:        "实现功能",
			Objective:    "提供实现",
			Instructions: "按验收条件实现",
			Criteria:     "构建通过",
		},
	)
}

// developmentSteps 过滤出开发步骤。
func developmentSteps(steps []domain.Step) []domain.Step {
	development := make([]domain.Step, 0, len(steps))
	for _, step := range steps {
		if step.Type == domain.StepTypeDevelop {
			development = append(development, step)
		}
	}
	return development
}
