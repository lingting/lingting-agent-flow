// 本文件是 tests/e2e/cli/code_test.go 中 9 个用例的迁移结果。
//
// 代码保存相关 CLI 的端到端测试：这些用例只验证命令注册、参数校验、状态输出与只读命令
// 行为，不连接远程机器。
package cli

import (
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记本包 9 条用例，顺序与源文件一致。
var (
	_ = register("code-command-help", runCodeCommandHelp)
	_ = register("code-init-requires-task", runCodeInitRequiresTask)
	_ = register("code-init-requires-step-and-attempt", runCodeInitRequiresStepAndAttempt)
	_ = register("code-init-help-explains-boundaries", runCodeInitHelpExplainsBoundaries)
	_ = register("code-save-requires-task", runCodeSaveRequiresTask)
	_ = register("code-save-requires-container", runCodeSaveRequiresContainer)
	_ = register("code-merge-requires-confirmation", runCodeMergeRequiresConfirmation)
	_ = register("code-status-requires-task", runCodeStatusRequiresTask)
	_ = register("code-status-empty-task", runCodeStatusEmptyTask, databaseDecl()...)
)

// 原 TestCodeCommandHelp 校验 code 命令组与其子命令都已注册。
func runCodeCommandHelp(t *testrunner.Context) {
	code, stdout, stderr := run(t, "code", "--help")
	if code != 0 {
		t.Fatalf("code --help 应成功退出，实际 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"init", "save", "push", "merge", "status"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("code --help 应列出 %s 子命令: %s", expected, stdout)
		}
	}
	if !strings.Contains(stdout, "agent/<task-id>") {
		t.Fatalf("code --help 应说明任务分支规则: %s", stdout)
	}
}

// 原 TestCodeInitRequiresTask 校验代码初始化缺少 --task 时被拒绝。
func runCodeInitRequiresTask(t *testrunner.Context) {
	code, _, stderr := run(t, "code", "init")
	if code == 0 {
		t.Fatal("缺少 --task 时不应成功")
	}
	if !strings.Contains(stderr, "task") {
		t.Fatalf("错误信息应指出缺少 --task: %s", stderr)
	}
}

// 原 TestCodeInitRequiresStepAndAttempt 校验代码初始化必须给出步骤与尝试。
func runCodeInitRequiresStepAndAttempt(t *testrunner.Context) {
	for _, args := range [][]string{
		{"code", "init", "--task", "task-1"},
		{"code", "init", "--task", "task-1", "--step", "step-1"},
	} {
		code, _, stderr := run(t, args...)
		if code == 0 {
			t.Fatalf("%v 不应成功", args)
		}
		if !strings.Contains(stderr, "step") && !strings.Contains(stderr, "attempt") {
			t.Fatalf("错误信息应指出缺少的参数: %s", stderr)
		}
	}
}

// 原 TestCodeInitHelpExplainsBoundaries 校验帮助文本说明幂等与非覆盖规则。
func runCodeInitHelpExplainsBoundaries(t *testrunner.Context) {
	code, stdout, stderr := run(t, "code", "init", "--help")
	if code != 0 {
		t.Fatalf("code init --help 应成功退出，实际 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"agent/<task-id>", "skipped", "认证"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("帮助文本应说明 %q: %s", expected, stdout)
		}
	}
}

// 原 TestCodeSaveRequiresTask 校验缺少 --task 时命令被拒绝。
func runCodeSaveRequiresTask(t *testrunner.Context) {
	code, _, stderr := run(t, "code", "save", "container-1")
	if code == 0 {
		t.Fatal("缺少 --task 时不应成功")
	}
	if !strings.Contains(stderr, "task") {
		t.Fatalf("错误信息应指出缺少 --task: %s", stderr)
	}
}

// 原 TestCodeSaveRequiresContainer 校验缺少容器参数时命令被拒绝。
func runCodeSaveRequiresContainer(t *testrunner.Context) {
	code, _, _ := run(t, "code", "save", "--task", "task-1")
	if code == 0 {
		t.Fatal("缺少容器参数时不应成功")
	}
}

// 原 TestCodeMergeRequiresConfirmation 校验非交互执行时合并需要显式确认。
func runCodeMergeRequiresConfirmation(t *testrunner.Context) {
	code, _, stderr := run(t, "code", "merge", "--task", "task-1", "container-1")
	if code == 0 {
		t.Fatal("缺少 --yes 时不应执行合并")
	}
	if !strings.Contains(stderr, "--yes") {
		t.Fatalf("错误信息应提示追加 --yes: %s", stderr)
	}
}

// 原 TestCodeStatusRequiresTask 校验缺少 --task 时状态命令被拒绝。
func runCodeStatusRequiresTask(t *testrunner.Context) {
	code, _, stderr := run(t, "code", "status")
	if code == 0 {
		t.Fatal("缺少 --task 时不应成功")
	}
	if !strings.Contains(stderr, "task") {
		t.Fatalf("错误信息应指出缺少 --task: %s", stderr)
	}
}

// 原 TestCodeStatusEmptyTask 校验没有任何记录时输出明确提示。
func runCodeStatusEmptyTask(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, stdout, stderr := run(t, "code", "status", "--config", configPath, "--task", "task-1")
	if code != 0 {
		t.Fatalf("查询不存在的记录应成功退出，实际 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "没有该任务的代码保存记录") {
		t.Fatalf("输出应说明没有记录: %s", stdout)
	}
}
