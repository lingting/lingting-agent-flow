// 本文件是 tests/e2e/cli/docker_test.go 中 10 个用例的迁移结果。
//
// 除三个帮助类用例外，其余用例都把配置交给会读取执行节点记录的命令，因此声明一份
// 用例独占的 sqlite-db；这些命令都在参数或节点校验阶段失败，不接触真实 Docker 或远程机器。
package cli

import (
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记本包 10 条用例，顺序与源文件一致。
var (
	_ = register("docker-help", runDockerHelp)
	_ = register("docker-env-help", runDockerEnvHelp)
	_ = register("docker-container-help", runDockerContainerHelp)
	_ = register("docker-env-requires-machine", runDockerEnvRequiresMachine, databaseDecl()...)
	_ = register("docker-env-list-without-versions", runDockerEnvListWithoutVersions, databaseDecl()...)
	_ = register("docker-container-delete-requires-confirmation", runDockerContainerDeleteRequiresConfirmation, databaseDecl()...)
	_ = register("docker-container-detach-requires-confirmation", runDockerContainerDetachRequiresConfirmation, databaseDecl()...)
	_ = register("docker-container-lifecycle-rejects-success", runDockerContainerLifecycleRejectsSuccess, databaseDecl()...)
	_ = register("docker-container-restart-requires-disposition", runDockerContainerRestartRequiresDisposition, databaseDecl()...)
	_ = register("docker-container-create-rejects-base-environment", runDockerContainerCreateRejectsBaseEnvironment, databaseDecl()...)
)

// 原 TestDockerHelp 校验 docker 命令组暴露全部子命令。
func runDockerHelp(t *testrunner.Context) {
	code, stdout, stderr := run(t, "docker", "--help")
	if code != 0 {
		t.Fatalf("docker --help 应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"preflight", "init", "env", "container"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("帮助输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// 原 TestDockerEnvHelp 校验 docker env 命令组暴露全部子命令。
func runDockerEnvHelp(t *testrunner.Context) {
	code, stdout, stderr := run(t, "docker", "env", "--help")
	if code != 0 {
		t.Fatalf("docker env --help 应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"list", "show", "history", "edit", "build", "log"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("帮助输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// 原 TestDockerContainerHelp 校验 docker container 命令组暴露全部子命令。
func runDockerContainerHelp(t *testrunner.Context) {
	code, stdout, stderr := run(t, "docker", "container", "--help")
	if code != 0 {
		t.Fatalf("docker container --help 应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{
		"create", "list", "show", "logs", "exec",
		"start", "stop", "delete", "detach", "lifecycle", "restart",
	} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("帮助输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// 原 TestDockerEnvRequiresMachine 校验环境命令要求显式指定节点。
func runDockerEnvRequiresMachine(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(t, "docker", "env", "list", "--config", configPath)
	if code == 0 {
		t.Fatal("缺少 --machine 时应返回非零退出码")
	}
	if !strings.Contains(stderr, "machine") {
		t.Fatalf("错误信息应指出缺少 machine，实际: %s", stderr)
	}
}

// 原 TestDockerEnvListWithoutVersions 校验无环境版本时的提示。
func runDockerEnvListWithoutVersions(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, stdout, stderr := run(
		t,
		"docker", "env", "list",
		"--config", configPath,
		"--machine", "node-a",
	)
	if code == 0 {
		t.Fatalf("节点不存在时应返回非零退出码，输出: %s", stdout)
	}
	if !strings.Contains(stderr, "执行节点") {
		t.Fatalf("错误信息应说明节点问题，实际: %s", stderr)
	}
}

// 原 TestDockerContainerDeleteRequiresConfirmation 校验删除需要显式确认。
func runDockerContainerDeleteRequiresConfirmation(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(
		t,
		"docker", "container", "delete", "container-1",
		"--config", configPath,
	)
	if code == 0 {
		t.Fatal("缺少 --yes 时应返回非零退出码")
	}
	if !strings.Contains(stderr, "--yes") {
		t.Fatalf("错误信息应提示追加 --yes，实际: %s", stderr)
	}
}

// 原 TestDockerContainerDetachRequiresConfirmation 校验脱离管理需要显式确认。
func runDockerContainerDetachRequiresConfirmation(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(
		t,
		"docker", "container", "detach", "container-1",
		"--config", configPath,
	)
	if code == 0 {
		t.Fatal("缺少 --yes 时应返回非零退出码")
	}
	if !strings.Contains(stderr, "不可撤销") {
		t.Fatalf("错误信息应说明操作不可撤销，实际: %s", stderr)
	}
}

// 原 TestDockerContainerLifecycleRejectsSuccess 校验不得把命令成功当作任务成功。
func runDockerContainerLifecycleRejectsSuccess(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(
		t,
		"docker", "container", "lifecycle", "container-1",
		"--config", configPath,
		"--outcome", "succeeded",
	)
	if code == 0 {
		t.Fatal("直接使用成功结果时应返回非零退出码")
	}
	if !strings.Contains(stderr, "代码保存") {
		t.Fatalf("错误信息应说明本阶段未实现代码保存，实际: %s", stderr)
	}
}

// 原 TestDockerContainerRestartRequiresDisposition 校验重新开始必须显式处置旧容器。
func runDockerContainerRestartRequiresDisposition(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(
		t,
		"docker", "container", "restart",
		"--config", configPath,
		"--previous", "container-1",
	)
	if code == 0 {
		t.Fatal("缺少 --disposition 时应返回非零退出码")
	}
	if !strings.Contains(stderr, "disposition") {
		t.Fatalf("错误信息应指出缺少 disposition，实际: %s", stderr)
	}
}

// 原 TestDockerContainerCreateRejectsBaseEnvironment 校验任务容器必须使用语言环境。
func runDockerContainerCreateRejectsBaseEnvironment(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(
		t,
		"docker", "container", "create",
		"--config", configPath,
		"--machine", "node-a",
		"--type", "base",
		"--name", "base",
	)
	if code == 0 {
		t.Fatal("使用基础环境创建任务容器时应返回非零退出码")
	}
	if !strings.Contains(stderr, "语言环境") {
		t.Fatalf("错误信息应说明必须使用语言环境，实际: %s", stderr)
	}
}
