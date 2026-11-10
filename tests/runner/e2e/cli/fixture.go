// 本文件提供迁移后用例共用的 CLI 执行入口与配置、断言辅助函数，对应源文件
// tests/e2e/cli/cli_test.go 里的 run、workspaceConfig、writeConfig、configValue 等。
package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	agentflowcli "github.com/lingting/agentflow/internal/cli"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// run 以给定参数执行一次 CLI，返回退出码与标准输出、标准错误。
func run(t *testrunner.Context, args ...string) (int, string, string) {
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
	root := agentflowcli.NewRootCommand(application)
	root.SetArgs(args)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.ExecuteContext(context.Background()); err != nil {
		return 1, stdout.String(), stderr.String() + err.Error()
	}
	return 0, stdout.String(), stderr.String()
}

// caseWorkspace 返回用例工作空间，替代 go test 里的 testkit.New：worker 里没有运行根目录，
// 工作空间由编排进程下发。
func caseWorkspace(t *testrunner.Context) *testkit.Workspace {
	t.Helper()
	return testkit.NewAt(t, t.WorkspaceDir())
}

// 配置模板中的路径占位符，由 workspaceConfig 替换为用例范围内的绝对路径。
//
// 使用占位符而不是相对路径，是因为相对路径会按进程工作目录解析：直接把相对路径交给会打开
// 数据库的命令会把 SQLite 文件写进当前目录（仓库或用例工作空间之外）。
//
// 占位符以 "/" 开头且包含明显的测试标记，即使被误当作路径使用也会立刻失败，而不是在当前
// 目录里悄悄创建出同名的 SQLite 文件。
const (
	configDatabasePlaceholder = "/__agentflow-test-missing-database__"
	configWorktreePlaceholder = "/__agentflow-test-missing-worktree__"
)

// workspaceConfig 生成一份数据库来自注入句柄、工作区落在用例工作空间内的配置。
//
// 所有会打开数据库的用例都必须使用它：数据库文件与 WAL/SHM 只会出现在注入的资源路径上，
// 随资源释放一起删除，不会残留到运行目录里。
func workspaceConfig(t *testrunner.Context) string {
	t.Helper()
	return writeConfig(t, workspaceConfigContent(t))
}

// workspaceConfigContent 返回替换了用例范围绝对路径的配置内容。
//
// 需要在模板基础上再改写其他字段的用例用它，避免重复创建配置文件。
func workspaceConfigContent(t *testrunner.Context) string {
	t.Helper()
	return strings.NewReplacer(
		configDatabasePlaceholder,
		filepath.ToSlash(databasePath(t)),
		configWorktreePlaceholder,
		filepath.ToSlash(caseWorkspace(t).Path("worktrees")),
	).Replace(minimalConfig)
}

// writeConfig 在用例工作空间内写入配置文件并返回其路径。
func writeConfig(t *testrunner.Context, content string) string {
	t.Helper()
	path := filepath.Join(caseWorkspace(t).Dir("config"), "agentflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}
	return path
}

// minimalConfig 是一份可以通过校验的最小配置。
//
// 配置只包含当前正式 Schema 中的字段：角色不是配置项，需要角色的用例改为往测试数据库
// 写入角色记录，不能靠配置回填。
//
// 其中的数据库与工作区路径是**占位符**，由 workspaceConfig 替换为用例范围内的绝对路径。
// 任何会打开数据库的用例都必须使用 workspaceConfig 生成的配置，直接用本常量会让 SQLite
// 文件按进程工作目录解析，写到用例范围之外。
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
`

// configValue 读取配置内容中某个键的单行取值。
func configValue(t *testrunner.Context, content, key string) string {
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

// writeTestPrivateKey 生成一个测试用未加密私钥并返回其路径。
func writeTestPrivateKey(t *testrunner.Context) string {
	t.Helper()
	return testkit.PrivateKeyIn(t, caseWorkspace(t).Dir("keys"))
}

// extractFingerprint 从输出中提取 SHA-256 指纹。
func extractFingerprint(t *testrunner.Context, output string) string {
	t.Helper()
	const prefix = "SHA256:"
	index := strings.Index(output, prefix)
	if index < 0 {
		t.Fatalf("输出中找不到指纹: %s", output)
	}
	rest := output[index:]
	end := strings.IndexAny(rest, " \r\n\t")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// extractTaskID 从任务摘要输出中提取任务标识。
func extractTaskID(t *testrunner.Context, stdout string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "任务: ") {
			continue
		}
		id := strings.TrimSpace(strings.TrimPrefix(trimmed, "任务: "))
		if id != "" {
			return id
		}
	}
	t.Fatalf("输出中没有任务标识: %s", stdout)
	return ""
}
