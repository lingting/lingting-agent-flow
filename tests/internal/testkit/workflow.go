package testkit

import (
	"context"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// WorkflowFixture 是一套完整的工作流测试数据。
//
// 它覆盖 Task、Step、Attempt、Session 的关联，以及工作流引擎需要的角色与执行节点，
// 使测试可以专注于状态机与执行顺序，而不必重复构造前置数据。
type WorkflowFixture struct {
	DB          *sqlite.DB
	Requirement domain.Requirement
	Host        domain.ExecutionHost
	Planner     domain.Role
	Developer   domain.Role
	Validator   domain.Role
	Task        domain.Task
}

// WorkflowOptions 约束工作流测试数据的构造。
type WorkflowOptions struct {
	// MergeAfterPush 表示任务配置要求合并基础分支。
	MergeAfterPush bool
	// Verification 是任务级必需验证命令。
	Verification domain.VerificationSpec
	// Retry 是任务级重试策略。
	Retry domain.RetryConfig
	// Goal 是任务目标，为空时使用默认值。
	Goal string
	// Now 是构造数据使用的时间。
	Now time.Time
}

// NewWorkflowFixture 在工作空间中创建数据库并写入完整的工作流测试数据。
func NewWorkflowFixture(t *testing.T, options WorkflowOptions) *WorkflowFixture {
	t.Helper()
	ws := New(t)
	path := ws.SQLitePath()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	fixture := &WorkflowFixture{DB: db}
	fixture.Planner = mustCreateRole(t, db, ctx, domain.RoleTypePlanner, "规划师", now)
	fixture.Developer = mustCreateRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	fixture.Validator = mustCreateRole(t, db, ctx, domain.RoleTypeValidator, "验证工程师", now)
	fixture.Host = mustCreateHost(t, db, ctx, now)
	fixture.Requirement = mustCreateRequirement(t, db, ctx, now)
	fixture.Task = mustCreateTask(t, db, ctx, fixture, options, now)
	return fixture
}

// mustCreateRole 创建并写入一个测试角色。
func mustCreateRole(
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

// mustCreateHost 创建并写入一个测试执行节点。
func mustCreateHost(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.ExecutionHost {
	t.Helper()
	host, err := domain.NewExecutionHost("", "测试节点", "127.0.0.1", 0, "deploy", "", now)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host
}

// mustCreateRequirement 创建并写入一个测试需求。
func mustCreateRequirement(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.Requirement {
	t.Helper()
	requirement, err := domain.NewRequirement(
		"测试需求",
		"测试需求正文",
		domain.PriorityNormal,
		now,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	return requirement
}

// mustCreateTask 创建并写入一个带完整工作流配置的任务。
func mustCreateTask(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	fixture *WorkflowFixture,
	options WorkflowOptions,
	now time.Time,
) domain.Task {
	t.Helper()
	task, err := domain.NewTask(
		fixture.Requirement.ID,
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
	goal := options.Goal
	if goal == "" {
		goal = "实现一个可测试的功能"
	}
	task.Config = domain.TaskConfig{
		Goal:            goal,
		Environment:     environment,
		Verification:    options.Verification,
		Retry:           options.Retry,
		MergeAfterPush:  options.MergeAfterPush,
		PlannerRoleID:   fixture.Planner.ID,
		DeveloperRoleID: fixture.Developer.ID,
		ValidatorRoleID: fixture.Validator.ID,
	}
	task.HostID = &fixture.Host.ID
	if err := task.Validate(); err != nil {
		t.Fatalf("任务配置非法: %v", err)
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return task
}

// CreateStep 创建并写入一个开发步骤。
func (f *WorkflowFixture) CreateStep(
	t *testing.T,
	ctx context.Context,
	sequence int,
	spec domain.VerificationSpec,
	maxAttempts int,
) domain.Step {
	t.Helper()
	step, err := domain.NewAgentStep(
		f.Task.ID,
		sequence,
		domain.StepTypeDevelop,
		"步骤 "+itoa(sequence),
		"实现第 "+itoa(sequence)+" 部分功能",
		"按验收条件实现",
		f.Developer,
		spec,
		domain.RetryConfig{MaxAttempts: maxAttempts},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := f.DB.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return step
}

// Reload 重新读取任务，用于核验持久化结果。
func (f *WorkflowFixture) Reload(t *testing.T, ctx context.Context) domain.Task {
	t.Helper()
	task, err := f.DB.GetTask(ctx, f.Task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return *task
}

// Steps 读取任务的全部步骤。
func (f *WorkflowFixture) Steps(t *testing.T, ctx context.Context) []domain.Step {
	t.Helper()
	steps, err := f.DB.ListStepsByTask(ctx, f.Task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return steps
}

// Attempts 读取某个步骤的全部尝试。
func (f *WorkflowFixture) Attempts(t *testing.T, ctx context.Context, stepID domain.ID) []domain.Attempt {
	t.Helper()
	attempts, err := f.DB.ListAttemptsByStep(ctx, stepID)
	if err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	return attempts
}

// Sessions 读取任务的全部会话。
func (f *WorkflowFixture) Sessions(t *testing.T, ctx context.Context) []domain.Session {
	t.Helper()
	sessions, err := f.DB.ListSessionsByTask(ctx, f.Task.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	return sessions
}

// Events 读取任务的全部事件。
func (f *WorkflowFixture) Events(t *testing.T, ctx context.Context) []domain.Event {
	t.Helper()
	events, err := f.DB.ListEventsByTask(ctx, f.Task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	return events
}

// itoa 把整数转换为十进制字符串。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	buffer := make([]byte, 0, 8)
	for value > 0 {
		buffer = append([]byte{byte('0' + value%10)}, buffer...)
		value /= 10
	}
	return string(buffer)
}
