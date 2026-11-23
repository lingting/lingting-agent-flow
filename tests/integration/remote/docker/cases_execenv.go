// 本文件是 tests/integration/remote/docker/execenv_test.go 的迁移结果（1 条用例）。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、工作空间取编排进程下发的目录；
// 断言与远程操作保持原样。
package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记真实容器内执行环境 1 条用例。
var _ = register("exec-passes-workdir-and-env", runRemoteExecPassesWorkDirAndEnv)

// 原 TestRemoteExecPassesWorkDirAndEnv 验证真实容器内的命令执行能传递工作目录与环境变量。
//
// 该用例覆盖执行环境实际使用的 docker exec 参数（--workdir 与 --env），
// 确认它们在真实 Docker CLI 上可用，而不只是在命令构造层面正确。
func runRemoteExecPassesWorkDirAndEnv(t *testrunner.Context) {
	tg := requireEnabled(t)
	ws := newWorkspace(t)
	client := dial(t, ws, tg)

	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	testRoot := remoteTestRoot(t, ctx, client, randomSuffix(t))

	runner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	guardContainerResiduals(t, runner)
	preflight := runner.Preflight(ctx, docker.PreflightOptions{
		WorkspaceRoot:   testRoot,
		CreateWorkspace: true,
	})
	if !preflight.Ready() {
		t.Fatalf("真实远程 Docker 预检未通过(%s): %s", preflight.ErrorCode, preflight.Detail)
	}
	suffix := randomSuffix(t)
	imageRef := fmt.Sprintf("%s:%s", testImageRepository, suffix)
	imageID := buildTestImage(t, ctx, client, runner, testRoot, imageRef)
	container := newRemoteTestContainer(t, imageRef, imageID, suffix)
	dockerID := createAndStartTestContainer(t, ctx, runner, container, imageRef)
	t.Logf("TEST_CONTAINER=%s DOCKER_ID=%s", container.Name, dockerID)
	// 容器清理责任已在创建前登记，用例结束时会复核远程状态。

	// 准备容器内项目目录，模拟执行环境的工作区。
	if _, err := runner.ExecCommandIn(ctx, dockerID, []string{
		"sh", "-c", "mkdir -p " + domain.DefaultContainerWorkDir,
	}, docker.ExecOptions{
		WorkDir: domain.DefaultContainerWorkDir,
		Timeout: time.Minute,
	}); err != nil {
		t.Fatalf("准备容器内项目目录失败: %v", err)
	}

	run, err := runner.ExecCommandIn(ctx, dockerID, []string{
		"sh", "-c", `printf '%s:%s' "$AGENTFLOW_PROBE" "$(pwd)"`,
	}, docker.ExecOptions{
		WorkDir: domain.DefaultContainerWorkDir,
		Env:     []string{"AGENTFLOW_PROBE=execenv"},
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("容器内执行命令失败: %v", err)
	}
	output := strings.TrimSpace(run.Stdout)
	want := "execenv:" + domain.DefaultContainerWorkDir
	if output != want {
		t.Fatalf("工作目录与环境变量未正确传递: 期望 %q，实际 %q", want, output)
	}

	// 非零退出码必须被如实报告，不得当成执行成功。
	failed, failErr := runner.ExecCommandIn(ctx, dockerID, []string{
		"sh", "-c", "exit 7",
	}, docker.ExecOptions{
		WorkDir: domain.DefaultContainerWorkDir,
		Timeout: time.Minute,
	})
	if failErr == nil {
		t.Fatal("非零退出码必须返回错误")
	}
	if failed.ExitCode != 7 {
		t.Fatalf("退出码应透传，实际 %d", failed.ExitCode)
	}
	if failed.Succeeded() {
		t.Fatal("非零退出码不得判定成功")
	}
}
