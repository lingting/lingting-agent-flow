// 本文件提供项目与任务用例所需的配置模板与数据库预置数据，对应源文件
// tests/e2e/cli/project_test.go 与 tests/e2e/cli/task_test.go 里的同名辅助函数。
//
// 数据库一律使用注入的 sqlite-db 句柄路径：预置数据与随后执行的 CLI 命令操作同一份库。
package cli

import (
	"context"
	"path/filepath"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// projectTestConfig 生成一份可用于项目命令的配置文件。
//
// 数据库来自注入句柄，工作区落在传入的用例工作空间内；配置只包含当前正式 Schema 中的
// 字段：角色不在配置里，由 seedProjectPrerequisites 显式写入测试数据库。
func projectTestConfig(t *testrunner.Context, ws *testkit.Workspace) string {
	t.Helper()
	content := `
app:
  name: agentflow
  log_level: error
storage:
  database_path: ` + filepath.ToSlash(databasePath(t)) + `
workspace:
  worktree_root: ` + filepath.ToSlash(ws.Path("worktrees")) + `
scheduler:
  max_concurrent_tasks: 1
  max_retries_per_step: 0
`
	return writeConfig(t, content)
}

// seedProjectPrerequisites 在测试数据库中写入执行节点与角色，返回节点标识。
//
// 任务创建会校验节点与角色是否真实存在，因此项目相关的建任务用例都要先落这些记录。
func seedProjectPrerequisites(t *testrunner.Context) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: databasePath(t)})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	now := time.Now().UTC()
	host, err := domain.NewExecutionHost(
		"", "测试节点", "127.0.0.1", 0, "deploy", "", now,
		domain.WithPrivateKey(writeTestPrivateKey(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	for _, seed := range []struct {
		id       string
		roleType domain.RoleType
	}{
		{"planner", domain.RoleTypePlanner},
		{"developer", domain.RoleTypeDeveloper},
	} {
		role, err := domain.NewRole(
			seed.id,
			seed.roleType,
			"provider/model-id",
			"角色提示词",
			domain.DefaultToolsPolicy(seed.roleType),
			now,
		)
		if err != nil {
			t.Fatalf("构造角色失败: %v", err)
		}
		role.ID = domain.ID(seed.id)
		if err := db.CreateRole(ctx, role); err != nil {
			t.Fatalf("写入角色失败: %v", err)
		}
	}
	return host.ID.String()
}

// projectAddArgs 返回一份完整的 project add 参数（不含 --config）。
func projectAddArgs(projectID, name string) []string {
	return []string{
		"project", "add",
		"--id", projectID,
		"--name", name,
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "go",
	}
}

// taskConfig 生成一份可用于任务命令的配置文件。
//
// 数据库来自注入句柄，工作区位于用例工作空间内；配置只包含当前正式 Schema 中的字段：
// 角色不在配置里，由 seedTaskPrerequisites 显式写入测试数据库。
func taskConfig(t *testrunner.Context, ws *testkit.Workspace) string {
	t.Helper()
	content := `
app:
  name: agentflow
  log_level: error
storage:
  database_path: ` + filepath.ToSlash(databasePath(t)) + `
workspace:
  worktree_root: ` + filepath.ToSlash(ws.Path("worktrees")) + `
scheduler:
  max_concurrent_tasks: 2
  max_retries_per_step: 2
`
	return writeConfig(t, content)
}

// seedTaskPrerequisites 在配置指定的数据库中写入执行节点与角色。
func seedTaskPrerequisites(t *testrunner.Context) domain.ExecutionHost {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sqlite.Open(ctx, sqlite.Options{Path: databasePath(t)})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	now := time.Now().UTC()
	host, err := domain.NewExecutionHost(
		"", "测试节点", "127.0.0.1", 0, "deploy", "", now,
		domain.WithPrivateKey(writeTestPrivateKey(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	for _, seed := range []struct {
		id       string
		roleType domain.RoleType
	}{
		{"planner", domain.RoleTypePlanner},
		{"developer", domain.RoleTypeDeveloper},
		{"validator", domain.RoleTypeValidator},
	} {
		role, err := domain.NewRole(
			seed.id,
			seed.roleType,
			"provider/model-id",
			"角色提示词",
			domain.DefaultToolsPolicy(seed.roleType),
			now,
		)
		if err != nil {
			t.Fatalf("构造角色失败: %v", err)
		}
		role.ID = domain.ID(seed.id)
		if err := db.CreateRole(ctx, role); err != nil {
			t.Fatalf("写入角色失败: %v", err)
		}
	}
	// 任务必须关联项目：预先写入一条可被建任务请求引用的项目记录。
	testkit.CreateProject(t, db, testkit.DefaultTestProjectID, now)
	return host
}
