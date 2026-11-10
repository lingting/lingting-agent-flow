// 本文件是 tests/e2e/cli/cli_test.go 中 14 个用例的迁移结果。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、配置与私钥改落在用例工作空间内、数据库
// 改为注入的 sqlite-db 句柄；断言与注释保持原样。
package cli

import (
	"path/filepath"
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记本包 14 条用例，顺序与源文件一致，便于与 R0 冻结清单逐条对账。
var (
	_ = register("version-command", runVersionCommand)
	_ = register("root-help", runRootHelp)
	_ = register("server-command-help", runServerCommandHelp)
	_ = register("web-command-removed", runWebCommandRemoved)
	_ = register("server-fails-on-missing-config", runServerFailsOnMissingConfig)
	_ = register("config-validate-success", runConfigValidateSuccess, databaseDecl()...)
	_ = register("config-validate-failure", runConfigValidateFailure, databaseDecl()...)
	_ = register("config-validate-missing-file", runConfigValidateMissingFile)
	_ = register("agent-help", runAgentHelp)
	_ = register("agent-run-requires-model-and-prompt", runAgentRunRequiresModelAndPrompt)
	_ = register("agent-run-rejects-unknown-container", runAgentRunRejectsUnknownContainer)
	_ = register("workspace-config-uses-absolute-paths", runWorkspaceConfigUsesAbsolutePaths, databaseDecl()...)
	_ = register("version-command-output-format", runVersionCommandOutputFormat)
	_ = register("version-flag-matches-version-command", runVersionFlagMatchesVersionCommand)
)

// 原 TestVersionCommand 校验 version 子命令输出注入的版本信息。
func runVersionCommand(t *testrunner.Context) {
	code, stdout, stderr := run(t, "version")
	if code != 0 {
		t.Fatalf("version 应成功退出，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"1.2.3", "abc1234", "2025-01-01T00:00:00Z"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("version 输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// 原 TestRootHelp 校验根命令帮助包含子命令说明。
func runRootHelp(t *testrunner.Context) {
	code, stdout, stderr := run(t, "--help")
	if code != 0 {
		t.Fatalf("--help 应成功退出，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"config", "version"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("帮助输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// 原 TestServerCommandHelp 校验 server 命令说明统一入口与配置项。
func runServerCommandHelp(t *testrunner.Context) {
	code, stdout, stderr := run(t, "server", "--help")
	if code != 0 {
		t.Fatalf("server --help 应成功退出，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"--config", "--address", "shutdown_timeout_seconds", "127.0.0.1"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("server 帮助输出缺少 %q，实际: %s", expected, stdout)
		}
	}
}

// 原 TestWebCommandRemoved 校验旧的 web 命令已由 server 取代。
func runWebCommandRemoved(t *testrunner.Context) {
	code, stdout, _ := run(t, "web")
	if code == 0 {
		t.Fatalf("web 命令应已移除，实际成功输出: %s", stdout)
	}
}

// 原 TestServerFailsOnMissingConfig 校验配置缺失时服务不启动。
func runServerFailsOnMissingConfig(t *testrunner.Context) {
	workspace := caseWorkspace(t)
	missing := filepath.Join(workspace.Root(), "missing.yaml")
	code, stdout, stderr := run(t, "server", "--config", missing, "--address", "127.0.0.1:0")
	if code == 0 {
		t.Fatalf("配置缺失时 server 应失败，实际成功输出: %s", stdout)
	}
	if !strings.Contains(stderr, "配置文件不存在") {
		t.Fatalf("错误信息应说明配置不存在，实际: %s", stderr)
	}
	if strings.Contains(stdout, "服务已启动") {
		t.Fatalf("配置失败时不应打印启动成功: %s", stdout)
	}
}

// 原 TestConfigValidateSuccess 校验合法配置的校验命令输出。
func runConfigValidateSuccess(t *testrunner.Context) {
	path := workspaceConfig(t)
	code, stdout, stderr := run(t, "config", "validate", "--config", path)
	if code != 0 {
		t.Fatalf("配置校验应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "配置校验通过") {
		t.Fatalf("输出应包含校验通过信息，实际: %s", stdout)
	}
}

// 原 TestConfigValidateFailure 校验非法配置导致非零退出码与明确错误信息。
func runConfigValidateFailure(t *testrunner.Context) {
	content := strings.Replace(workspaceConfigContent(t), "max_concurrent_tasks: 1", "max_concurrent_tasks: 0", 1)
	path := writeConfig(t, content)
	code, _, stderr := run(t, "config", "validate", "--config", path)
	if code == 0 {
		t.Fatal("非法配置应返回非零退出码")
	}
	if !strings.Contains(stderr, "scheduler.max_concurrent_tasks") {
		t.Fatalf("错误信息应指出具体字段，实际: %s", stderr)
	}
}

// 原 TestConfigValidateMissingFile 校验缺失配置文件导致非零退出码。
func runConfigValidateMissingFile(t *testrunner.Context) {
	code, _, stderr := run(t, "config", "validate", "--config", filepath.Join(t.WorkspaceDir(), "absent.yaml"))
	if code == 0 {
		t.Fatal("缺失配置文件应返回非零退出码")
	}
	if !strings.Contains(stderr, "配置文件不存在") {
		t.Fatalf("错误信息应说明文件不存在，实际: %s", stderr)
	}
}

// 原 TestAgentHelp 校验 agent 命令组与子命令出现在帮助中。
//
// 源用例同时要求帮助里出现 check；check 子命令已随 commit e36ecff 移除，该断言在迁移前
// 就已经失效，这里只保留仍然存在的 run 断言（差异已登记在 .steps/V3/R5/README.md §5.2）。
func runAgentHelp(t *testrunner.Context) {
	code, stdout, stderr := run(t, "agent", "--help")
	if code != 0 {
		t.Fatalf("agent --help 应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"run"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("帮助输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// 原 TestAgentRunRequiresModelAndPrompt 校验容器内调用要求显式指定模型与提示词。
//
// 本版本只支持在远程容器内执行 Agent 调用，因此 `agent run` 是唯一的执行入口，
// 角色类型、模型与提示词都必须显式给出：角色类型决定工具策略的系统能力上限。
func runAgentRunRequiresModelAndPrompt(t *testrunner.Context) {
	code, _, stderr := run(t, "agent", "run", "container-1")
	if code == 0 {
		t.Fatal("缺少必需参数时应返回非零退出码")
	}
	for _, expected := range []string{"model", "prompt"} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("错误信息应指出缺少 %s，实际: %s", expected, stderr)
		}
	}
}

// 原 TestAgentRunRejectsUnknownContainer 校验容器不存在时如实返回错误。
//
// 该命令只在受管理的容器内执行：容器记录不存在时直接失败，不会回退到本机执行。
func runAgentRunRejectsUnknownContainer(t *testrunner.Context) {
	code, _, stderr := run(
		t,
		"agent",
		"run",
		"container-not-exists",
		"--role-type",
		"developer",
		"--permissions",
		"read",
		"--model",
		"group/free",
		"--prompt",
		"只回答 READY",
	)
	if code == 0 {
		t.Fatal("容器不存在时应返回非零退出码")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Fatal("错误信息不能为空")
	}
}

// 原 TestWorkspaceConfigUsesAbsolutePaths 校验测试配置不把运行期数据指向相对路径。
//
// 相对路径会按进程工作目录解析，因此把相对路径交给会打开数据库的命令会把 SQLite 文件写进
// 仓库或用例工作空间之外。迁移后数据库路径来自注入的 sqlite-db 句柄、工作区路径落在用例
// 工作空间内：两者都必须是绝对路径，并且不能指向当前目录。
func runWorkspaceConfigUsesAbsolutePaths(t *testrunner.Context) {
	content := workspaceConfigContent(t)
	for _, key := range []string{"database_path", "worktree_root"} {
		value := configValue(t, content, key)
		if !filepath.IsAbs(value) {
			t.Fatalf("%s 必须是绝对路径，实际 %q", key, value)
		}
	}
	// 模板本身不应残留占位符。
	if strings.Contains(content, configDatabasePlaceholder) ||
		strings.Contains(content, configWorktreePlaceholder) {
		t.Fatalf("配置模板仍有未替换的占位符: %s", content)
	}
	// 生成的路径必须落在用例范围内，而不是仓库目录。
	if configValue(t, content, "database_path") != filepath.ToSlash(databasePath(t)) {
		t.Fatalf("数据库路径必须来自注入的资源句柄: %s", content)
	}
	root := filepath.ToSlash(t.WorkspaceDir())
	if !strings.HasPrefix(configValue(t, content, "worktree_root"), root) {
		t.Fatalf("工作区路径必须位于用例工作空间 %s 之下: %s", root, content)
	}
}

// 原 TestVersionCommandOutputFormat 校验 version 输出包含完整版本元数据字段。
//
// 发布制品的可核对性依赖这几个字段：版本号对应 Git 标签，提交对应实际参与构建的源码，
// 构建时间是 UTC 时间，Go 与平台来自真实工具链与编译目标。缺任何一项用户都无法判断
// 手上的二进制由哪份代码产出。
func runVersionCommandOutputFormat(t *testrunner.Context) {
	code, stdout, stderr := run(t, "version")
	if code != 0 {
		t.Fatalf("version 应成功退出，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"agentflow 1.2.3", "提交: abc1234", "构建时间: 2025-01-01T00:00:00Z", "Go: go", "平台: "} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("version 输出缺少 %q，实际: %s", expected, stdout)
		}
	}
	// 本地开发构建注入 1.2.3，输出不得出现 dev 之类的默认值覆盖注入值。
	if strings.Contains(stdout, "agentflow dev") {
		t.Fatalf("version 输出不应回落到默认版本: %s", stdout)
	}
}

// 原 TestVersionFlagMatchesVersionCommand 校验 --version 与 version 子命令输出一致。
//
// 两个入口共用同一份格式化实现，任何一处单独拼装都会让"版本信息"出现两套口径。
func runVersionFlagMatchesVersionCommand(t *testrunner.Context) {
	code, fromCommand, stderr := run(t, "version")
	if code != 0 {
		t.Fatalf("version 应成功退出，实际退出码 %d，错误: %s", code, stderr)
	}
	code, fromFlag, stderr := run(t, "--version")
	if code != 0 {
		t.Fatalf("--version 应成功退出，实际退出码 %d，错误: %s", code, stderr)
	}
	if fromCommand != fromFlag {
		t.Fatalf("--version 与 version 输出必须一致:\n--version:%s\nversion:%s", fromFlag, fromCommand)
	}
}
