package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// taskConfig 生成一份可用于任务命令的配置文件。
//
// 数据库与 Session 目录都位于测试工作空间内，避免写入真实数据目录。
func taskConfig(t *testing.T, ws *testkit.Workspace) string {
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
  max_concurrent_tasks: 2
  max_retries_per_step: 2
roles:
  - id: planner
    name: 架构规划师
    type: planner
    runner: pi
    model: "provider/model-id"
    system_prompt: 生成开发计划
    tools_policy:
      mode: read_only
  - id: developer
    name: 开发工程师
    type: developer
    runner: pi
    model: "provider/model-id"
    system_prompt: 实现代码
    tools_policy:
      mode: development
  - id: validator
    name: 验证工程师
    type: validator
    runner: pi
    model: "provider/model-id"
    system_prompt: 验证代码
    tools_policy:
      mode: verification
`
	return writeConfig(t, content)
}

// seedTaskPrerequisites 在配置指定的数据库中写入执行节点与角色。
func seedTaskPrerequisites(
	t *testing.T,
	ws *testkit.Workspace,
	configPath string,
) domain.ExecutionHost {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
	host, err := domain.NewExecutionHost("", "测试节点", "127.0.0.1", 0, "deploy", "", now)
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
	_ = configPath
	return host
}

// TestTaskCreateValidatesBeforeExecution 校验任务创建在执行前完成配置校验。
func TestTaskCreateValidatesBeforeExecution(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t, ws, configPath)
	code, stdout, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--description", "为项目补充功能",
		"--host", host.ID.String(),
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

// TestTaskCreateRejectsMissingConfiguration 校验缺少必填配置时直接失败。
func TestTaskCreateRejectsMissingConfiguration(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t, ws, configPath)
	// 缺少环境名称：应在创建任务前失败，不创建任何容器与任务记录。
	code, _, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
		"--repo", "ssh://git.example.com/team/repo",
		"--base", "main",
		"--environment", "",
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
	db, err := sqlite.Open(ctx, sqlite.Options{Path: ws.DatabasePath()})
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

// TestTaskCreateIsIdempotent 校验幂等键阻止重复创建任务。
func TestTaskCreateIsIdempotent(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t, ws, configPath)
	args := []string{
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
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

// TestTaskListAndGet 校验任务查询命令。
func TestTaskListAndGet(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t, ws, configPath)
	code, stdout, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
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

// TestTaskCancelMarksRequest 校验取消命令记录取消请求。
func TestTaskCancelMarksRequest(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t, ws, configPath)
	code, stdout, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
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
	for _, fragment := range []string{"状态: cancelling", "取消请求: 是"} {
		if !strings.Contains(cancelOut, fragment) {
			t.Fatalf("取消输出缺少 %q: %s", fragment, cancelOut)
		}
	}
}

// TestTaskRetryRequiresPreviousDisposition 校验重试必须显式选择旧容器处置方式。
func TestTaskRetryRequiresPreviousDisposition(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t, ws, configPath)
	code, stdout, stderr := run(
		t,
		"task", "create",
		"--config", configPath,
		"--title", "实现功能",
		"--host", host.ID.String(),
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
	// 缺少 --previous 时 cobra 会拒绝执行，命令不应静默处置旧容器。
	code, _, retryErr := run(t, "task", "retry", taskID, "--config", configPath)
	if code == 0 {
		t.Fatal("缺少 --previous 时重试必须失败")
	}
	if !strings.Contains(retryErr, "previous") {
		t.Fatalf("错误信息应指出缺少 --previous: %s", retryErr)
	}
	// 非法处置方式同样必须被拒绝。
	code, _, invalidErr := run(
		t,
		"task", "retry", taskID,
		"--config", configPath,
		"--previous", "keep",
	)
	if code == 0 {
		t.Fatal("非法处置方式必须被拒绝")
	}
	if !strings.Contains(invalidErr, "delete") && !strings.Contains(invalidErr, "detach") {
		t.Fatalf("错误信息应说明允许的处置方式: %s", invalidErr)
	}
}

// extractTaskID 从任务摘要输出中提取任务标识。
func extractTaskID(t *testing.T, stdout string) string {
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
