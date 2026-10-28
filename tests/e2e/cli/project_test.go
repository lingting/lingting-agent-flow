package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// projectTestConfig 生成一份可用于项目命令的配置文件。
//
// 数据库与工作区都落在同一个测试工作空间内；这里显式接收 Workspace，避免多次调用
// testkit.Dir 得到不同目录（每个用例的数据库与配置必须指向同一处）。
func projectTestConfig(t *testing.T, ws *testkit.Workspace) string {
	t.Helper()
	content := `
app:
  name: agentflow
  log_level: error
storage:
  database_path: ` + ws.DatabasePath() + `
workspace:
  worktree_root: ` + ws.Path("worktrees") + `
scheduler:
  max_concurrent_tasks: 1
  max_retries_per_step: 0
roles:
  - id: planner
    name: 架构规划师
    type: planner
    runner: pi
    model: "provider/model-id"
    system_prompt: 生成开发计划
    tools_policy:
      mode: read_only
`
	return writeConfig(t, content)
}

// seedProjectPrerequisites 在测试数据库中写入执行节点与角色，返回节点标识。
//
// 任务创建会校验节点与角色是否真实存在，因此项目相关的建任务用例都要先落这些记录。
func seedProjectPrerequisites(t *testing.T, ws *testkit.Workspace) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: ws.DatabasePath()})
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
		domain.WithPrivateKey(testPrivateKeyPath(t)),
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

// TestProjectLifecycle 校验项目命令组的完整生命周期。
//
// 项目是任务的必填关联对象，因此"能否用 CLI 登记一个项目"直接决定 CLI 能否创建任务。
func TestProjectLifecycle(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := projectTestConfig(t, ws)

	args := append(projectAddArgs("demo-service", "示例服务"),
		"--config", configPath,
		"--environment-version", "v0001",
		"--cache", "go-mod",
	)
	code, stdout, stderr := run(t, args...)
	if code != 0 {
		t.Fatalf("新增项目失败: %d %s", code, stderr)
	}
	for _, fragment := range []string{"项目: demo-service", "示例服务", "基础分支: main", "go v0001"} {
		if !strings.Contains(stdout, fragment) {
			t.Fatalf("新增输出缺少 %q: %s", fragment, stdout)
		}
	}

	code, listOut, listErr := run(t, "project", "list", "--config", configPath)
	if code != 0 {
		t.Fatalf("查询项目列表失败: %d %s", code, listErr)
	}
	if !strings.Contains(listOut, "demo-service") {
		t.Fatalf("项目列表缺少记录: %s", listOut)
	}

	code, showOut, showErr := run(t, "project", "show", "demo-service", "--config", configPath)
	if code != 0 {
		t.Fatalf("查询项目详情失败: %d %s", code, showErr)
	}
	if !strings.Contains(showOut, "demo-service") {
		t.Fatalf("项目详情不符: %s", showOut)
	}

	code, updateOut, updateErr := run(
		t,
		"project", "update",
		"--config", configPath,
		"--id", "demo-service",
		"--name", "改名后的项目",
		"--repo", "ssh://git.example.com/team/other",
		"--base", "release/1.x",
		"--environment", "node",
	)
	if code != 0 {
		t.Fatalf("更新项目失败: %d %s", code, updateErr)
	}
	for _, fragment := range []string{"改名后的项目", "release/1.x", "node 当前可用版本"} {
		if !strings.Contains(updateOut, fragment) {
			t.Fatalf("更新输出缺少 %q: %s", fragment, updateOut)
		}
	}

	code, removeOut, removeErr := run(t, "project", "remove", "demo-service", "--config", configPath)
	if code != 0 {
		t.Fatalf("删除项目失败: %d %s", code, removeErr)
	}
	if !strings.Contains(removeOut, "demo-service") {
		t.Fatalf("删除输出不符: %s", removeOut)
	}

	// 删除后不再出现在列表里，也不能再被读取。
	code, listAfter, _ := run(t, "project", "list", "--config", configPath)
	if code != 0 {
		t.Fatal("删除后查询列表失败")
	}
	if strings.Contains(listAfter, "demo-service") {
		t.Fatalf("已删除项目不应出现在列表中: %s", listAfter)
	}
	if code, _, _ := run(t, "project", "show", "demo-service", "--config", configPath); code == 0 {
		t.Fatal("已删除项目不应可读取")
	}
}

// TestProjectAddRejectsInvalidInput 校验非法输入在写入前被拒绝。
func TestProjectAddRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := projectTestConfig(t, ws)
	cases := map[string][]string{
		"标识非法": {
			"--id", "Demo Service",
			"--name", "示例服务",
			"--repo", "ssh://git.example.com/team/repo",
			"--base", "main",
			"--environment", "go",
		},
		"仓库地址内嵌凭据": {
			"--id", "demo-service",
			"--name", "示例服务",
			"--repo", "https://user:token@git.example.com/r.git",
			"--base", "main",
			"--environment", "go",
		},
		"工作目录越界": {
			"--id", "demo-service",
			"--name", "示例服务",
			"--repo", "ssh://git.example.com/team/repo",
			"--base", "main",
			"--environment", "go",
			"--workdir", "/etc/project",
		},
	}
	for name, args := range cases {
		code, _, stderr := run(t, append([]string{"project", "add", "--config", configPath}, args...)...)
		if code == 0 {
			t.Fatalf("%s: 应当被拒绝", name)
		}
		if strings.TrimSpace(stderr) == "" {
			t.Fatalf("%s: 失败时应输出可诊断错误", name)
		}
	}
}

// TestProjectAddRejectsDuplicateID 校验重复标识失败并给出可读原因。
func TestProjectAddRejectsDuplicateID(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := projectTestConfig(t, ws)
	args := append(projectAddArgs("demo-service", "示例服务"), "--config", configPath)
	if code, _, stderr := run(t, args...); code != 0 {
		t.Fatalf("首次新增失败: %d %s", code, stderr)
	}
	code, _, stderr := run(t, args...)
	if code == 0 {
		t.Fatal("重复标识应当失败")
	}
	if !strings.Contains(stderr, "已存在") {
		t.Fatalf("错误信息应说明标识已存在: %s", stderr)
	}
}

// TestTaskCreateUsesProjectDefaults 校验建任务时未给出的执行配置从项目继承。
func TestTaskCreateUsesProjectDefaults(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := projectTestConfig(t, ws)
	hostID := seedProjectPrerequisites(t, ws)

	code, _, stderr := run(
		t,
		"project", "add",
		"--config", configPath,
		"--id", "demo-service",
		"--name", "示例服务",
		"--repo", "ssh://git.example.com/team/project-repo",
		"--base", "project-base",
		"--environment", "go",
	)
	if code != 0 {
		t.Fatalf("新增项目失败: %d %s", code, stderr)
	}

	// 只给出项目标识与必需的角色、节点：仓库、分支与环境来自项目。
	code, stdout, createErr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", hostID,
		"--project", "demo-service",
		"--planner-role", "planner",
		"--developer-role", "developer",
	)
	if code != 0 {
		t.Fatalf("创建任务失败: %d %s", code, createErr)
	}
	if !strings.Contains(stdout, "任务: ") {
		t.Fatalf("创建输出不符: %s", stdout)
	}
}

// TestTaskCreateRequiresProject 校验缺少项目标识时建任务失败。
func TestTaskCreateRequiresProject(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := projectTestConfig(t, ws)
	hostID := seedProjectPrerequisites(t, ws)

	code, _, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", hostID,
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "go",
		"--planner-role", "planner",
		"--developer-role", "developer",
	)
	if code == 0 {
		t.Fatal("缺少项目标识时创建任务应失败")
	}
	if !strings.Contains(stderr, "project") {
		t.Fatalf("错误信息应指出缺少项目参数: %s", stderr)
	}
}

// TestTaskCreateRejectsUnknownProject 校验项目不存在时建任务失败。
func TestTaskCreateRejectsUnknownProject(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := projectTestConfig(t, ws)
	hostID := seedProjectPrerequisites(t, ws)

	code, _, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", hostID,
		"--project", "missing-project",
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "go",
		"--planner-role", "planner",
		"--developer-role", "developer",
	)
	if code == 0 {
		t.Fatal("项目不存在时创建任务应失败")
	}
	if !strings.Contains(stderr, "missing-project") {
		t.Fatalf("错误信息应指出具体项目标识: %s", stderr)
	}
}
