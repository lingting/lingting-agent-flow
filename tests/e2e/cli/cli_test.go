package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/cli"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// run 以给定参数执行一次 CLI，返回退出码与标准输出、标准错误。
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	application, err := app.New(app.Options{
		Build: app.BuildInfo{
			Version:   "1.2.3",
			Commit:    "abc1234",
			BuildDate: "2025-01-01T00:00:00Z",
		},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("构造应用实例失败: %v", err)
	}
	root := cli.NewRootCommand(application)
	root.SetArgs(args)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.ExecuteContext(context.Background()); err != nil {
		return 1, stdout.String(), stderr.String() + err.Error()
	}
	return 0, stdout.String(), stderr.String()
}

// 配置模板中的路径占位符，由 workspaceConfig 替换为测试工作空间内的绝对路径。
//
// 使用占位符而不是相对路径，是因为相对路径会按进程工作目录解析：go test 的工作
// 目录是包目录，直接把相对路径交给会打开数据库的命令会把 SQLite 文件写进仓库。
//
// 占位符以 "/" 开头且包含明显的测试标记，即使被误当作路径使用也会立刻失败，
// 而不是在包目录里悄悄创建出同名的 SQLite 文件。
const (
	configDatabasePlaceholder = "/__agentflow-test-missing-database__"
	configWorktreePlaceholder = "/__agentflow-test-missing-worktree__"
)

// workspaceConfig 生成一份数据库与工作区都落在测试工作空间内的配置。
//
// 所有会打开数据库的用例都必须使用它：数据库文件、WAL/SHM 与工作区目录因此只会
// 出现在 testkit.Dir(t) 的随机目录中，测试结束后保留便于排查。
func workspaceConfig(t *testing.T) string {
	t.Helper()
	return writeConfig(t, workspaceConfigContent(t))
}

// workspaceConfigContent 返回替换了工作空间绝对路径的配置内容。
//
// 需要在模板基础上再改写其他字段的用例用它，避免重复创建临时目录。
func workspaceConfigContent(t *testing.T) string {
	t.Helper()
	dir := testkit.Dir(t)
	return strings.NewReplacer(
		configDatabasePlaceholder,
		filepath.ToSlash(filepath.Join(dir, "agentflow.db")),
		configWorktreePlaceholder,
		filepath.ToSlash(filepath.Join(dir, "worktrees")),
	).Replace(minimalConfig)
}

// writeConfig 在临时目录写入配置文件并返回其路径。
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(testkit.Dir(t), "agentflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}
	return path
}

// minimalConfig 是一份可以通过校验的最小配置。
//
// 其中的数据库与工作区路径是**占位符**，由 workspaceConfig 替换为测试工作空间内的
// 绝对路径。任何会打开数据库的用例都必须使用 workspaceConfig 生成的配置，直接用
// 本常量会让 SQLite 文件按进程工作目录解析，写到包目录下。
const minimalConfig = `
app:
  name: agentflow
  log_level: debug
storage:
  database_path: ` + configDatabasePlaceholder + `
workspace:
  worktree_root: ` + configWorktreePlaceholder + `
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

// TestVersionCommand 校验 version 子命令输出注入的版本信息。
func TestVersionCommand(t *testing.T) {
	t.Parallel()
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

// TestRootHelp 校验根命令帮助包含子命令说明。
func TestRootHelp(t *testing.T) {
	t.Parallel()
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

// TestServerCommandHelp 校验 server 命令说明统一入口与配置项。
func TestServerCommandHelp(t *testing.T) {
	t.Parallel()
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

// TestWebCommandRemoved 校验旧的 web 命令已由 server 取代。
func TestWebCommandRemoved(t *testing.T) {
	t.Parallel()
	code, stdout, _ := run(t, "web")
	if code == 0 {
		t.Fatalf("web 命令应已移除，实际成功输出: %s", stdout)
	}
}

// TestServerFailsOnMissingConfig 校验配置缺失时服务不启动。
func TestServerFailsOnMissingConfig(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
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

// TestConfigValidateSuccess 校验合法配置的校验命令输出。
func TestConfigValidateSuccess(t *testing.T) {
	t.Parallel()
	path := workspaceConfig(t)
	code, stdout, stderr := run(t, "config", "validate", "--config", path)
	if code != 0 {
		t.Fatalf("配置校验应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "配置校验通过") {
		t.Fatalf("输出应包含校验通过信息，实际: %s", stdout)
	}
}

// TestConfigValidateFailure 校验非法配置导致非零退出码与明确错误信息。
func TestConfigValidateFailure(t *testing.T) {
	t.Parallel()
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

// TestConfigValidateMissingFile 校验缺失配置文件导致非零退出码。
func TestConfigValidateMissingFile(t *testing.T) {
	t.Parallel()
	code, _, stderr := run(t, "config", "validate", "--config", filepath.Join(testkit.Dir(t), "absent.yaml"))
	if code == 0 {
		t.Fatal("缺失配置文件应返回非零退出码")
	}
	if !strings.Contains(stderr, "配置文件不存在") {
		t.Fatalf("错误信息应说明文件不存在，实际: %s", stderr)
	}
}

// TestAgentHelp 校验 agent 命令组与子命令出现在帮助中。
func TestAgentHelp(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := run(t, "agent", "--help")
	if code != 0 {
		t.Fatalf("agent --help 应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"check", "test"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("帮助输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// TestAgentTestRequiresPrompt 校验测试命令要求显式指定提示词。
func TestAgentTestRequiresPrompt(t *testing.T) {
	t.Parallel()
	code, _, stderr := run(t, "agent", "test")
	if code == 0 {
		t.Fatal("缺少必需参数时应返回非零退出码")
	}
	if !strings.Contains(stderr, "prompt") {
		t.Fatalf("错误信息应指出缺少 prompt，实际: %s", stderr)
	}
}

// TestAgentTestMissingConfig 校验缺少配置文件时返回明确错误。
func TestAgentTestMissingConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(testkit.Dir(t), "absent.yaml")
	code, _, stderr := run(t, "agent", "test", "--config", path, "--prompt", "只回答 READY")
	if code == 0 {
		t.Fatal("缺少配置文件时应返回非零退出码")
	}
	if !strings.Contains(stderr, "配置文件不存在") {
		t.Fatalf("错误信息应说明配置文件不存在，实际: %s", stderr)
	}
}

// TestAgentCheckUnknownExecutable 校验 Pi 不可用时检查命令返回明确错误。
func TestAgentCheckUnknownExecutable(t *testing.T) {
	t.Setenv("PATH", testkit.Dir(t))
	code, _, stderr := run(t, "agent", "check")
	if code == 0 {
		t.Fatal("Pi 不可用时应返回非零退出码")
	}
	if !strings.Contains(stderr, "未找到可执行程序") {
		t.Fatalf("错误信息应说明可执行程序缺失，实际: %s", stderr)
	}
}

// TestWorkspaceConfigUsesAbsolutePaths 校验测试配置不把运行期数据指向相对路径。
//
// 相对路径会按进程工作目录解析：go test 的工作目录是包目录，因此把相对路径交给
// 会打开数据库的命令会把 SQLite 文件写进仓库。所有会打开数据库的用例都必须使用
// workspaceConfig 生成的绝对路径配置，本用例守住这一点。
func TestWorkspaceConfigUsesAbsolutePaths(t *testing.T) {
	t.Parallel()
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
	// 生成的路径必须落在测试工作空间内，而不是仓库目录。
	root := testkit.RunRoot(t)
	if !strings.HasPrefix(configValue(t, content, "database_path"), filepath.ToSlash(root)) {
		t.Fatalf("数据库路径必须位于测试根目录 %s 之下: %s", root, content)
	}
}

// configValue 读取配置内容中某个键的单行取值。
func configValue(t *testing.T, content, key string) string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(trimmed, key+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("配置中缺少 %s: %s", key, content)
	return ""
}
