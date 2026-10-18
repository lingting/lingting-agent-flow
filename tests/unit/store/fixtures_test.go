package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// testContext 返回带超时的测试上下文。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// newTestDB 在本次测试工作空间中打开一个全新的数据库。
//
// 数据库文件永久保留在 TEST_ARTIFACT_DIR 之下，测试结束只关闭连接。
func newTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	return openTestDB(t, testkit.New(t).SQLitePath())
}

// openTestDB 打开指定路径的测试数据库，并在测试结束时关闭连接。
func openTestDB(t *testing.T, path string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(testContext(t), sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭测试数据库失败: %v", err)
		}
	})
	return db
}

// rawDB 打开一条绕过仓储层的独立连接，用于验证数据库自身约束。
//
// 连接在测试结束时关闭；SQLite 文件与 WAL 保留在测试工作空间中。
func rawDB(t *testing.T, db *sqlite.DB) *sql.DB {
	t.Helper()
	return testkit.OpenSQLite(t, db.Path())
}

// mustRole 创建一个测试角色并写入数据库。
func mustRole(t *testing.T, db *sqlite.DB, roleType domain.RoleType, name string) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		name,
		roleType,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(roleType),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(testContext(t), role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// mustRequirement 创建一个测试需求并写入数据库。
func mustRequirement(t *testing.T, db *sqlite.DB) domain.Requirement {
	t.Helper()
	requirement, err := domain.NewRequirement(
		"需求标题",
		"需求正文",
		domain.PriorityNormal,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(testContext(t), requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	return requirement
}

// mustTask 创建并写入一个测试任务。
func mustTask(t *testing.T, db *sqlite.DB, requirementID domain.ID) domain.Task {
	t.Helper()
	task, err := domain.NewTask(requirementID, "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := db.CreateTask(testContext(t), task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	return task
}

// mustStep 创建并写入一个测试步骤。
func mustStep(t *testing.T, db *sqlite.DB, task domain.Task, role domain.Role, sequence int) domain.Step {
	t.Helper()
	spec := domain.VerificationSpec{
		AcceptanceCriteria: []string{"构建通过"},
		Commands: []domain.VerificationCommand{
			{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
		},
	}
	step, err := domain.NewStep(
		task.ID,
		sequence,
		"步骤标题",
		"步骤目标",
		"执行要求",
		role,
		spec,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(testContext(t), step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	return step
}

// mustAttempt 创建并写入一次测试执行尝试。
func mustAttempt(t *testing.T, db *sqlite.DB, step domain.Step, number int) domain.Attempt {
	t.Helper()
	attempt, err := domain.NewAttempt(step.ID, number, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造执行尝试失败: %v", err)
	}
	if err := db.CreateAttempt(testContext(t), attempt); err != nil {
		t.Fatalf("写入执行尝试失败: %v", err)
	}
	return attempt
}

// mustSession 创建并写入一次测试会话。
func mustSession(
	t *testing.T,
	db *sqlite.DB,
	scope domain.SessionScope,
	role domain.Role,
) domain.Session {
	t.Helper()
	session, err := domain.NewSession(scope, role, "pi", "provider/model", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	if err := db.CreateSession(testContext(t), session); err != nil {
		t.Fatalf("写入会话失败: %v", err)
	}
	return session
}

// newFixture 构建一套最小可用的关联数据。
type fixture struct {
	planner     domain.Role
	developer   domain.Role
	validator   domain.Role
	requirement domain.Requirement
	task        domain.Task
	step        domain.Step
	attempt     domain.Attempt
}

// newFixture 创建角色、需求、任务、步骤与尝试。
func newFixture(t *testing.T, db *sqlite.DB) fixture {
	t.Helper()
	built := fixture{
		planner:     mustRole(t, db, domain.RoleTypePlanner, "规划师"),
		developer:   mustRole(t, db, domain.RoleTypeDeveloper, "开发工程师"),
		validator:   mustRole(t, db, domain.RoleTypeValidator, "验证工程师"),
		requirement: mustRequirement(t, db),
	}
	built.task = mustTask(t, db, built.requirement.ID)
	built.step = mustStep(t, db, built.task, built.developer, 1)
	built.attempt = mustAttempt(t, db, built.step, 1)
	return built
}
