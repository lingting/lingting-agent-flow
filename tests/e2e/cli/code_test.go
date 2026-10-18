// 代码保存相关 CLI 的端到端测试。
//
// 这些用例只验证命令注册、参数校验、状态输出与只读命令行为，不连接远程机器。
package cli_test

import (
	"strings"
	"testing"
)

// TestCodeCommandHelp 校验 code 命令组与其子命令都已注册。
func TestCodeCommandHelp(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := run(t, "code", "--help")
	if code != 0 {
		t.Fatalf("code --help 应成功退出，实际 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"save", "push", "merge", "status"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("code --help 应列出 %s 子命令: %s", expected, stdout)
		}
	}
	if !strings.Contains(stdout, "agent/<task-id>") {
		t.Fatalf("code --help 应说明任务分支规则: %s", stdout)
	}
}

// TestCodeSaveRequiresTask 校验缺少 --task 时命令被拒绝。
func TestCodeSaveRequiresTask(t *testing.T) {
	t.Parallel()
	code, _, stderr := run(t, "code", "save", "container-1")
	if code == 0 {
		t.Fatal("缺少 --task 时不应成功")
	}
	if !strings.Contains(stderr, "task") {
		t.Fatalf("错误信息应指出缺少 --task: %s", stderr)
	}
}

// TestCodeSaveRequiresContainer 校验缺少容器参数时命令被拒绝。
func TestCodeSaveRequiresContainer(t *testing.T) {
	t.Parallel()
	code, _, _ := run(t, "code", "save", "--task", "task-1")
	if code == 0 {
		t.Fatal("缺少容器参数时不应成功")
	}
}

// TestCodeMergeRequiresConfirmation 校验非交互执行时合并需要显式确认。
func TestCodeMergeRequiresConfirmation(t *testing.T) {
	t.Parallel()
	code, _, stderr := run(t, "code", "merge", "--task", "task-1", "container-1")
	if code == 0 {
		t.Fatal("缺少 --yes 时不应执行合并")
	}
	if !strings.Contains(stderr, "--yes") {
		t.Fatalf("错误信息应提示追加 --yes: %s", stderr)
	}
}

// TestCodeStatusRequiresTask 校验缺少 --task 时状态命令被拒绝。
func TestCodeStatusRequiresTask(t *testing.T) {
	t.Parallel()
	code, _, stderr := run(t, "code", "status")
	if code == 0 {
		t.Fatal("缺少 --task 时不应成功")
	}
	if !strings.Contains(stderr, "task") {
		t.Fatalf("错误信息应指出缺少 --task: %s", stderr)
	}
}

// TestCodeStatusEmptyTask 校验没有任何记录时输出明确提示。
func TestCodeStatusEmptyTask(t *testing.T) {
	t.Parallel()
	configPath := workspaceConfig(t)
	code, stdout, stderr := run(t, "code", "status", "--config", configPath, "--task", "task-1")
	if code != 0 {
		t.Fatalf("查询不存在的记录应成功退出，实际 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "没有该任务的代码保存记录") {
		t.Fatalf("输出应说明没有记录: %s", stdout)
	}
}
