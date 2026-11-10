// 本文件是 tests/e2e/cli/task_test.go 中 6 个用例的迁移结果。
//
// 这些用例都会打开数据库，因此声明一份用例独占的 sqlite-db：预置的执行节点与角色与随后
// 执行的 CLI 命令操作同一份库。
package cli

import (
	"context"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 登记本包 6 条用例，顺序与源文件一致。
var (
	_ = register("task-create-validates-before-execution", runTaskCreateValidatesBeforeExecution, databaseDecl()...)
	_ = register("task-create-rejects-missing-configuration", runTaskCreateRejectsMissingConfiguration, databaseDecl()...)
	_ = register("task-create-is-idempotent", runTaskCreateIsIdempotent, databaseDecl()...)
	_ = register("task-list-and-get", runTaskListAndGet, databaseDecl()...)
	_ = register("task-cancel-marks-request", runTaskCancelMarksRequest, databaseDecl()...)
	_ = register("task-run-requires-server", runTaskRunRequiresServer, databaseDecl()...)
)

// 原 TestTaskCreateValidatesBeforeExecution 校验任务创建在执行前完成配置校验。
func runTaskCreateValidatesBeforeExecution(t *testrunner.Context) {
	ws := caseWorkspace(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t)
	code, stdout, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--description", "为项目补充功能",
		"--host", host.ID.String(),
		"--project", testkit.DefaultTestProjectID,
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "go",
		"--planner-role", "planner",
		"--developer-role", "developer",
		"--validator-role", "validator",
		"--command", "build=go build ./...",
		"--acceptance", "构建通过",
		"--max-attempts", "2",
	)
	if code != 0 {
		t.Fatalf("创建任务应成功，退出码 %d，错误: %s", code, stderr)
	}
	for _, fragment := range []string{"任务:", "状态: queued", "构建通过"} {
		if fragment == "构建通过" {
			continue
		}
		if !strings.Contains(stdout, fragment) {
			t.Fatalf("输出缺少 %q: %s", fragment, stdout)
		}
	}
	if strings.Contains(stdout, "provider/model-id") {
		t.Fatalf("输出不应包含模型配置: %s", stdout)
	}
}

// 原 TestTaskCreateRejectsMissingConfiguration 校验缺少必填配置时直接失败。
func runTaskCreateRejectsMissingConfiguration(t *testrunner.Context) {
	ws := caseWorkspace(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t)
	// 显式给出非法的环境名称：应在创建任务前失败，不创建任何容器与任务记录。
	//
	// 注意：省略 --environment 不再算错误——未给出的字段从项目继承，只有显式给出
	// 非法取值才必须被拒绝。
	code, _, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
		"--project", testkit.DefaultTestProjectID,
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "INVALID ENV",
		"--planner-role", "planner",
		"--developer-role", "developer",
		"--command", "build=go build ./...",
	)
	if code == 0 {
		t.Fatal("环境名称非法时创建任务应失败")
	}
	if !strings.Contains(stderr, "环境配置非法") {
		t.Fatalf("失败时应输出可诊断错误: %s", stderr)
	}
	// 数据库不应留下任务记录。
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
	tasks, err := db.ListTasks(ctx, storage.TaskFilter{})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("配置非法时不应创建任务: %d", len(tasks))
	}
}

// 原 TestTaskCreateIsIdempotent 校验幂等键阻止重复创建任务。
func runTaskCreateIsIdempotent(t *testrunner.Context) {
	ws := caseWorkspace(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t)
	args := []string{
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
		"--project", testkit.DefaultTestProjectID,
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "go",
		"--planner-role", "planner",
		"--developer-role", "developer",
		"--idempotency-key", "request-1",
	}
	code, first, stderr := run(t, args...)
	if code != 0 {
		t.Fatalf("首次创建失败: %d %s", code, stderr)
	}
	code, second, stderr := run(t, args...)
	if code != 0 {
		t.Fatalf("重复创建应成功返回同一任务: %d %s", code, stderr)
	}
	if first != second {
		t.Fatalf("幂等键应返回同一任务:\n首次:\n%s\n再次:\n%s", first, second)
	}
}

// 原 TestTaskListAndGet 校验任务查询命令。
func runTaskListAndGet(t *testrunner.Context) {
	ws := caseWorkspace(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t)
	code, stdout, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
		"--project", testkit.DefaultTestProjectID,
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "go",
		"--planner-role", "planner",
		"--developer-role", "developer",
	)
	if code != 0 {
		t.Fatalf("创建任务失败: %d %s", code, stderr)
	}
	taskID := extractTaskID(t, stdout)

	code, listOut, listErr := run(t, "task", "list", "--config", configPath)
	if code != 0 {
		t.Fatalf("查询任务列表失败: %d %s", code, listErr)
	}
	if !strings.Contains(listOut, taskID) {
		t.Fatalf("任务列表缺少任务 %s: %s", taskID, listOut)
	}

	code, getOut, getErr := run(t, "task", "get", taskID, "--config", configPath)
	if code != 0 {
		t.Fatalf("查询任务详情失败: %d %s", code, getErr)
	}
	for _, fragment := range []string{"任务: " + taskID, "状态: queued", "最终结果"} {
		if !strings.Contains(getOut, fragment) {
			t.Fatalf("任务详情缺少 %q: %s", fragment, getOut)
		}
	}
}

// 原 TestTaskCancelMarksRequest 校验取消命令记录取消请求。
func runTaskCancelMarksRequest(t *testrunner.Context) {
	ws := caseWorkspace(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t)
	code, stdout, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
		"--project", testkit.DefaultTestProjectID,
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "go",
		"--planner-role", "planner",
		"--developer-role", "developer",
	)
	if code != 0 {
		t.Fatalf("创建任务失败: %d %s", code, stderr)
	}
	taskID := extractTaskID(t, stdout)
	code, cancelOut, cancelErr := run(t, "task", "cancel", taskID, "--config", configPath)
	if code != 0 {
		t.Fatalf("取消任务失败: %d %s", code, cancelErr)
	}
	// 任务从未进入执行阶段，取消必须直接收敛为 cancelled：停在 cancelling 会让
	// 一个没有任何执行者的任务只能靠重启恢复才收敛。
	for _, fragment := range []string{"状态: cancelled", "取消请求: 是"} {
		if !strings.Contains(cancelOut, fragment) {
			t.Fatalf("取消输出缺少 %q: %s", fragment, cancelOut)
		}
	}
}

// 原 TestTaskRunRequiresServer 校验 task run 通过本地服务执行，服务不可达时如实失败。
//
// 该用例不启动服务：它确认命令不会在没有服务的情况下退回本机执行，也不会伪造进度。
func runTaskRunRequiresServer(t *testrunner.Context) {
	ws := caseWorkspace(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t)
	code, stdout, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
		"--project", testkit.DefaultTestProjectID,
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "go",
		"--planner-role", "planner",
		"--developer-role", "developer",
	)
	if code != 0 {
		t.Fatalf("创建任务失败: %d %s", code, stderr)
	}
	taskID := extractTaskID(t, stdout)
	// 指向一个不可达的回环端口：命令必须失败并指出服务不可达。
	code, runOut, runErr := run(
		t,
		"task", "run", taskID,
		"--config", configPath,
		"--address", "127.0.0.1:1",
	)
	if code == 0 {
		t.Fatalf("服务不可达时 task run 必须失败: %s", runOut)
	}
	if !strings.Contains(runErr, "agentflow server") {
		t.Fatalf("错误信息应提示启动服务: %s", runErr)
	}
}
