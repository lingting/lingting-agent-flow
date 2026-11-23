// 本文件是 tests/e2e/cli/project_test.go 中 6 个用例的迁移结果。
//
// 这些用例都会打开数据库，因此声明一份用例独占的 sqlite-db：预置数据与随后执行的 CLI
// 命令操作同一份库，库文件随资源释放一起删除。
package cli

import (
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记本包 6 条用例，顺序与源文件一致。
var (
	_ = register("project-lifecycle", runProjectLifecycle, databaseDecl()...)
	_ = register("project-add-rejects-invalid-input", runProjectAddRejectsInvalidInput, databaseDecl()...)
	_ = register("project-add-rejects-duplicate-id", runProjectAddRejectsDuplicateID, databaseDecl()...)
	_ = register("task-create-uses-project-defaults", runTaskCreateUsesProjectDefaults, databaseDecl()...)
	_ = register("task-create-requires-project", runTaskCreateRequiresProject, databaseDecl()...)
	_ = register("task-create-rejects-unknown-project", runTaskCreateRejectsUnknownProject, databaseDecl()...)
)

// 原 TestProjectLifecycle 校验项目命令组的完整生命周期。
//
// 项目是任务的必填关联对象，因此"能否用 CLI 登记一个项目"直接决定 CLI 能否创建任务。
func runProjectLifecycle(t *testrunner.Context) {
	ws := caseWorkspace(t)
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

// 原 TestProjectAddRejectsInvalidInput 校验非法输入在写入前被拒绝。
func runProjectAddRejectsInvalidInput(t *testrunner.Context) {
	ws := caseWorkspace(t)
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

// 原 TestProjectAddRejectsDuplicateID 校验重复标识失败并给出可读原因。
func runProjectAddRejectsDuplicateID(t *testrunner.Context) {
	ws := caseWorkspace(t)
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

// 原 TestTaskCreateUsesProjectDefaults 校验建任务时未给出的执行配置从项目继承。
func runTaskCreateUsesProjectDefaults(t *testrunner.Context) {
	ws := caseWorkspace(t)
	configPath := projectTestConfig(t, ws)
	hostID := seedProjectPrerequisites(t)

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

// 原 TestTaskCreateRequiresProject 校验缺少项目标识时建任务失败。
func runTaskCreateRequiresProject(t *testrunner.Context) {
	ws := caseWorkspace(t)
	configPath := projectTestConfig(t, ws)
	hostID := seedProjectPrerequisites(t)

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

// 原 TestTaskCreateRejectsUnknownProject 校验项目不存在时建任务失败。
func runTaskCreateRejectsUnknownProject(t *testrunner.Context) {
	ws := caseWorkspace(t)
	configPath := projectTestConfig(t, ws)
	hostID := seedProjectPrerequisites(t)

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
