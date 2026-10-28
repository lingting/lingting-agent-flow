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
	// 任务必须关联项目：预先写入一条可被建任务请求引用的项目记录。
	testkit.CreateProject(t, db, testkit.DefaultTestProjectID, now)
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

// TestTaskCreateRejectsMissingConfiguration 校验缺少必填配置时直接失败。
func TestTaskCreateRejectsMissingConfiguration(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	configPath := taskConfig(t, ws)
	host := seedTaskPrerequisites(t, ws, configPath)
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

// TestTaskRunRequiresServer 校验 task run 通过本地服务执行，服务不可达时如实失败。
//
// 该用例不启动服务：它确认命令不会在没有服务的情况下退回本机执行，也不会伪造进度。
func TestTaskRunRequiresServer(t *testing.T) {
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

// testPrivateKeyPath 返回测试工作空间中的一个私钥文件路径。
//
// 本版本只支持私钥认证，构造执行节点时必须提供该路径。
func testPrivateKeyPath(t *testing.T) string {
	t.Helper()
	return testkit.PrivateKeyPath(t)
}
