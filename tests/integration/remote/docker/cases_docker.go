// 本文件是 tests/integration/remote/docker/docker_test.go 中用例的迁移结果（2 条）。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、工作空间取编排进程下发的目录、
// 包级容器终检改为用例级终检；断言与远程操作保持原样。
package docker

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记远程 Docker 基础设施 2 条用例，顺序与源文件一致。
var (
	_ = register("preflight", runRemoteDockerPreflight)
	_ = register("container-lifecycle", runRemoteDockerContainerLifecycle)
)

// 原 TestRemoteDockerPreflight 验证真实远程机器的 Docker 预检。
//
// 该用例只做只读检查，只会在专属远程测试目录中创建目录。
func runRemoteDockerPreflight(t *testrunner.Context) {
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
	preflight := runner.Preflight(ctx, docker.PreflightOptions{
		WorkspaceRoot:   testRoot,
		CreateWorkspace: true,
	})
	t.Logf("PREFLIGHT=%+v", preflight)
	if !preflight.Ready() {
		t.Fatalf(
			"真实远程 Docker 预检未通过(%s): %s",
			preflight.ErrorCode,
			preflight.Detail,
		)
	}
	if preflight.CLIVersion == "" || preflight.EngineVersion == "" {
		t.Fatalf("预检应返回 Docker 版本信息: %+v", preflight)
	}
	// CLI 帮助校验是唯一拿真实 docker 输出核对 docker.RequiredCLICommands 的环节：
	// 夹具用例只验证判定逻辑，无法发现真实 CLI 改名或移除参数，因此这里必须真跑一遍。
	help := runner.CheckCLIHelp(ctx, docker.RequiredCLICommands(), cliHelpTimeout)
	t.Logf("CLI_HELP=%+v", help)
	if !help.Succeeded() {
		t.Fatalf(
			"真实远程 Docker CLI 帮助校验未通过(%s): %s（缺少 %v）",
			help.Status,
			help.Message,
			help.MissingOptions,
		)
	}
}

// 原 TestRemoteDockerContainerLifecycle 在真实远程机器上验证容器完整生命周期。
//
// 该用例只使用带随机后缀的测试镜像标签、容器名称与远程目录，并只清理自己创建的
// 测试资源；不会执行全局 prune，也不会删除共享镜像或缓存卷。
func runRemoteDockerContainerLifecycle(t *testrunner.Context) {
	tg := requireEnabled(t)
	ws := newWorkspace(t)
	client := dial(t, ws, tg)

	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	suffix := randomSuffix(t)
	testRoot := remoteTestRoot(t, ctx, client, suffix)

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
		t.Fatalf(
			"真实远程 Docker 预检未通过(%s): %s",
			preflight.ErrorCode,
			preflight.Detail,
		)
	}
	imageRef := fmt.Sprintf("%s:%s", testImageRepository, suffix)
	imageID := buildTestImage(t, ctx, client, runner, testRoot, imageRef)
	t.Logf("TEST_IMAGE=%s IMAGE_ID=%s", imageRef, imageID)

	container := newRemoteTestContainer(t, imageRef, imageID, suffix)
	dockerID := createAndStartTestContainer(t, ctx, runner, container, imageRef)
	t.Logf("TEST_CONTAINER=%s DOCKER_ID=%s", container.Name, dockerID)

	// 测试容器由 createAndStartTestContainer 在创建之前登记清理责任：用例结束时会
	// 停止并删除容器，并按真实远程状态核验它确实已不存在。

	verifyOwnershipAndRunning(t, ctx, runner, container, dockerID)
	verifyExecAndLogs(t, ctx, runner, dockerID)
	verifyStopAndDelete(t, ctx, runner, dockerID)
}
