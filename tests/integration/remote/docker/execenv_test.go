// 本文件验证真实远程机器上的执行环境命令执行能力。
//
// 用例复用同包的远程测试基础设施（tests/config/ssh.yml 与 tests/config/docker.yml），
// 只操作带随机后缀、属于本次测试的容器与远程目录；开关未启用时输出 WARN 并跳过。
package docker_remote

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// TestRemoteExecPassesWorkDirAndEnv 验证真实容器内的命令执行能传递工作目录与环境变量。
//
// 该用例覆盖执行环境实际使用的 docker exec 参数（--workdir 与 --env），
// 确认它们在真实 Docker CLI 上可用，而不只是在命令构造层面正确。
func TestRemoteExecPassesWorkDirAndEnv(t *testing.T) {
	t.Parallel()
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
	if !preflight.Ready() {
		t.Fatalf("真实远程 Docker 预检未通过(%s): %s", preflight.ErrorCode, preflight.Detail)
	}
	suffix := randomSuffix(t)
	imageRef := fmt.Sprintf("%s:%s", testImageRepository, suffix)
	imageID := buildTestImage(t, ctx, client, runner, testRoot, imageRef)
	container := newRemoteTestContainer(t, imageRef, imageID, suffix)
	dockerID := createAndStartTestContainer(t, ctx, runner, container, imageRef)
	t.Logf("TEST_CONTAINER=%s DOCKER_ID=%s", container.Name, dockerID)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if _, err := runner.RemoveContainer(cleanupCtx, dockerID); err != nil {
			t.Logf("WARN 清理测试容器失败: %v", err)
		}
	})

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
