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

// writeConfig 在临时目录写入配置文件并返回其路径。
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}
	return path
}

// minimalConfig 是一份可以通过校验的最小配置。
const minimalConfig = `
app:
  name: agentflow
  log_level: debug
storage:
  database_path: .agentflow/agentflow.db
workspace:
  worktree_root: .agentflow/worktrees
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

// TestConfigValidateSuccess 校验合法配置的校验命令输出。
func TestConfigValidateSuccess(t *testing.T) {
	path := writeConfig(t, minimalConfig)
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
	path := writeConfig(t, strings.Replace(minimalConfig, "max_concurrent_tasks: 1", "max_concurrent_tasks: 0", 1))
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
	code, _, stderr := run(t, "config", "validate", "--config", filepath.Join(t.TempDir(), "absent.yaml"))
	if code == 0 {
		t.Fatal("缺失配置文件应返回非零退出码")
	}
	if !strings.Contains(stderr, "配置文件不存在") {
		t.Fatalf("错误信息应说明文件不存在，实际: %s", stderr)
	}
}

// TestAgentHelp 校验 agent 命令组与子命令出现在帮助中。
func TestAgentHelp(t *testing.T) {
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

// TestAgentTestRequiresFlags 校验测试命令要求显式指定参数。
func TestAgentTestRequiresFlags(t *testing.T) {
	code, _, stderr := run(t, "agent", "test")
	if code == 0 {
		t.Fatal("缺少必需参数时应返回非零退出码")
	}
	for _, flag := range []string{"model", "dir", "prompt"} {
		if !strings.Contains(stderr, flag) {
			t.Fatalf("错误信息应指出缺少 %s，实际: %s", flag, stderr)
		}
	}
}
