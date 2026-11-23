// 本文件是 tests/integration/remote/docker/main_test.go 与 docker_test.go 中基础设施的
// 迁移结果：远程目标装载、容器登记与核验、测试镜像构建。
//
// 改造点：参数类型由 *testing.T 换成 *testrunner.Context；用例工作空间改用编排进程下发
// 的目录（testkit.NewAt）；包级最终核验改为用例级终检（一个 worker 进程只跑一条用例）；
// 真实依赖授权改由编排进程下发；其余语义保持原样。
package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/testkit"
)

// remoteOpTimeout 是单次远程操作的超时上限。
const remoteOpTimeout = 5 * time.Minute

// testImageRepository 是测试镜像的仓库名。
//
// 该仓库只由测试使用，与生产环境的 agentflow-* 镜像命名空间区分开。
const testImageRepository = "agentflow-test-lifecycle"

// containerRun 是本次用例创建的远程测试容器登记表。
//
// 每条 Case 由独立 worker 进程执行，因此这里等价于用例级登记表；登记项在用例结束时
// 逐条删除并核验，随后由 guardContainerResiduals 做一次清单级零残留核验。
var containerRun = testkit.NewContainerRun("remote/docker")

// target 是一次真实远程测试的连接目标。
type target struct {
	host    string
	user    string
	port    int
	keyPath string
	// knownHostsPaths 是参与校验的只读 known_hosts 文件列表。
	knownHostsPaths []string
}

// requireEnabled 检查远程 Docker 测试开关与目标配置。
//
// 开关未启用时输出明确警告并跳过，避免普通测试意外修改远程 Docker。
func requireEnabled(t *testrunner.Context) target {
	t.Helper()
	requireRealTests(t, "真实远程 Docker 集成测试")
	enabled, _ := testkit.LoadDockerConfig(t)
	if !enabled.RemoteEnabled {
		reason := fmt.Sprintf(
			"WARN [SKIP] remote docker integration tests: 请把 %s 中的 docker.remote_enabled 设为 true。",
			testkit.ConfigPath(t, testkit.DockerConfigFile),
		)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	sshConfig, sshOK := testkit.LoadSSHConfig(t)
	if !sshOK || !sshConfig.Enabled {
		reason := testkit.EnableReason(t, testkit.SSHConfigFile, "ssh.enable")
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	missing := make([]string, 0, 2)
	if strings.TrimSpace(sshConfig.Host) == "" {
		missing = append(missing, "ssh.host")
	}
	if strings.TrimSpace(sshConfig.User) == "" {
		missing = append(missing, "ssh.user")
	}
	if len(missing) > 0 {
		reason := testkit.SkipReason(t, testkit.SSHConfigFile, missing)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	if sshConfig.Port <= 0 || sshConfig.Port > 65535 {
		t.Fatalf("ssh.port 不是合法端口: %d", sshConfig.Port)
	}
	return target{
		host:            strings.TrimSpace(sshConfig.Host),
		user:            strings.TrimSpace(sshConfig.User),
		port:            sshConfig.Port,
		keyPath:         strings.TrimSpace(sshConfig.PrivateKeyPath),
		knownHostsPaths: testkit.KnownHostsSources(t),
	}
}

// dial 建立经过主机密钥校验与身份认证的连接，并在用例结束时关闭。
//
// 主机密钥校验与产品实现完全一致，不会为了通过测试而接受未知主机。
func dial(t *testrunner.Context, ws *testkit.Workspace, tg target) sshclient.Client {
	t.Helper()
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: tg.knownHostsPaths,
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{
		HostKeys:       store,
		ConnectTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造连接器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	t.Cleanup(cancel)
	// 本版本只支持私钥认证：真实远程测试必须提供私钥路径。
	if tg.keyPath == "" {
		t.Fatalf("真实远程测试必须设置私钥路径（本版本只支持私钥认证）")
	}
	auth := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: tg.keyPath,
	}
	result, err := dialer.Dial(ctx, sshclient.Endpoint{
		Host: tg.host, Port: tg.port, Username: tg.user,
	}, auth)
	if err != nil {
		if sshclient.ErrorCodeOf(err) == sshclient.CodeHostKeyUnknown {
			t.Fatalf(
				"主机密钥未知，请先核验指纹后写入 %s: %s %s",
				filepath.Join(ws.Root(), "known_hosts"),
				result.HostKey.Algorithm,
				result.HostKey.Fingerprint,
			)
		}
		t.Fatalf("连接真实远程机器失败: %v", err)
	}
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		t.Fatalf("主机密钥应已被信任，实际: %s", result.HostKeyVerdict)
	}
	ws.Cleanup(result.Client.Close)
	return result.Client
}

// newWorkspace 以编排进程下发的用例工作空间作为本地产物目录。
func newWorkspace(t *testrunner.Context) *testkit.Workspace {
	t.Helper()
	ws := testkit.NewAt(t, t.WorkspaceDir())
	ws.Report()
	return ws
}

// randomSuffix 返回随机的十六进制后缀，用于隔离远程测试资源。
func randomSuffix(t *testrunner.Context) string {
	t.Helper()
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buffer)
}

// remoteTestRoot 返回本次用例专属的远程测试目录。
//
// 目录统一位于远程 Home 下的 .lingting/agentflow-tests/docker，与生产工作空间
// (~/.lingting/agentflow) 完全隔离；路径解析与目录创建统一由 testkit 提供。
func remoteTestRoot(t *testrunner.Context, ctx context.Context, client sshclient.Client, suffix string) string {
	t.Helper()
	root, err := testkit.ResolveRemoteTestRoot(
		ctx,
		client,
		remoteOpTimeout,
		testkit.RemoteTestPackageDocker,
		suffix,
	)
	if err != nil {
		t.Fatalf("准备远程测试目录失败: %v", err)
	}
	t.Logf("REMOTE_TEST_DIR=%s", root)
	return root
}

// guardContainerResiduals 在用例结束、逐个容器删除之后核验零残留。
//
// 调用时机固定在第一个容器登记之前：清理函数按注册顺序的相反顺序执行，因此容器删除
// 与连接关闭都由后注册的清理完成，本次核验最后运行。
func guardContainerResiduals(t *testrunner.Context, runner *docker.Runner) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testkit.ContainerVerifyTimeout)
		defer cancel()
		finalizer := testkit.ContainerFinalizer{Run: containerRun, Runtime: runner}
		if err := finalizer.Finalize(ctx); err != nil {
			t.Errorf("测试容器零残留核验未通过（运行 %s）: %v", containerRun.Label(), err)
		}
	})
}

// buildTestImage 构建带随机标签的测试镜像，返回核实后的镜像 ID。
func buildTestImage(
	t *testrunner.Context,
	ctx context.Context,
	client sshclient.Client,
	runner *docker.Runner,
	testRoot, imageRef string,
) string {
	t.Helper()
	transfer, err := client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := transfer.Close(); closeErr != nil {
			t.Logf("WARN 关闭 SFTP 通道失败: %v", closeErr)
		}
	})
	buildContext, err := docker.NewContext([]docker.ContextFile{
		{
			RelativePath: docker.DockerfileName,
			Content: []byte(
				"FROM " + docker.DefaultBaseImage + "\n" +
					"LABEL agentflow.test=\"true\"\n" +
					"WORKDIR " + domain.DefaultContainerWorkDir + "\n" +
					"CMD [\"sh\", \"-c\", \"" + docker.DefaultRunContractCommand + "\"]\n",
			),
			Mode: 0o644,
		},
	})
	if err != nil {
		t.Fatalf("构造构建上下文失败: %v", err)
	}
	version := domain.EnvironmentVersion{
		HostID:           "remote-test-host",
		Ref:              domain.EnvironmentRef{Type: domain.EnvironmentTypeBase, Name: "base"},
		Number:           1,
		DockerfileSHA256: buildContext.Hash(),
		ContextSHA256:    buildContext.Hash(),
		ImageRef:         imageRef,
		Status:           domain.BuildStatusBuilding,
		CreatedAt:        time.Now().UTC(),
	}
	version.BuildConfigSHA256 = docker.BuildConfigHash(version, "")
	result, err := runner.BuildImage(ctx, transfer, docker.BuildSpec{
		Version:          version,
		Context:          buildContext,
		RemoteContextDir: path.Join(testRoot, "context"),
		LogPath:          path.Join(testRoot, "build.log"),
	})
	if err != nil {
		t.Fatalf("构建测试镜像失败: %v\n远程日志: %s\n日志末尾:\n%s", err, result.LogPath, result.LogTail)
	}
	if result.ImageID == "" {
		t.Fatalf("构建成功但未取得实际镜像 ID: %+v", result)
	}
	return result.ImageID
}

// newRemoteTestContainer 构造测试用容器登记记录。
func newRemoteTestContainer(
	t *testrunner.Context,
	imageRef, imageID, suffix string,
) domain.Container {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	container, err := domain.NewContainer(
		domain.NewID(),
		"remote-test-host",
		"agentflow-test-"+suffix,
		ref,
		"v0001",
		strings.Repeat("a", 64),
		imageID,
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	container.ImageID = imageID
	return container
}

// createAndStartTestContainer 创建并启动测试容器，返回 Docker 容器 ID。
func createAndStartTestContainer(
	t *testrunner.Context,
	ctx context.Context,
	runner *docker.Runner,
	container domain.Container,
	imageRef string,
) string {
	t.Helper()
	// 创建之前登记清理责任：此时容器名称已知，即使创建返回失败或后续启动失败，
	// 也不会因为提前退出而失去清理与核验能力。
	tracked := containerRun.Track(t, runner, testkit.TrackedContainer{
		Name:   container.Name,
		Record: &container,
	})
	dockerID, err := runner.CreateContainer(ctx, docker.CreateContainerSpec{
		Container: container,
		ImageRef:  imageRef,
		WorkDir:   domain.DefaultContainerWorkDir,
	})
	if err != nil {
		t.Fatalf("创建测试容器失败: %v", err)
	}
	tracked.DockerID = dockerID
	if err := runner.StartContainer(ctx, dockerID); err != nil {
		t.Fatalf("启动测试容器失败: %v", err)
	}
	return dockerID
}

// verifyOwnershipAndRunning 验证所有权标签与运行状态。
func verifyOwnershipAndRunning(
	t *testrunner.Context,
	ctx context.Context,
	runner *docker.Runner,
	container domain.Container,
	dockerID string,
) {
	t.Helper()
	inspect, err := runner.InspectContainer(ctx, dockerID)
	if err != nil {
		t.Fatalf("检查测试容器失败: %v", err)
	}
	if err := docker.VerifyOwnership(inspect, container); err != nil {
		t.Fatalf("测试容器所有权核验失败: %v", err)
	}
	if !inspect.Running {
		t.Fatalf("测试容器应在运行中: %+v", inspect)
	}
	if inspect.Name != container.Name {
		t.Fatalf("容器名称不符: 期望 %s，实际 %s", container.Name, inspect.Name)
	}
	if inspect.ImageID != container.ImageID {
		t.Fatalf("容器镜像 ID 不符: 期望 %s，实际 %s", container.ImageID, inspect.ImageID)
	}
}

// verifyExecAndLogs 验证容器内命令执行与日志读取。
func verifyExecAndLogs(
	t *testrunner.Context,
	ctx context.Context,
	runner *docker.Runner,
	dockerID string,
) {
	t.Helper()
	run, err := runner.ExecCommand(ctx, dockerID, []string{"sh", "-c", "echo agentflow-ok"}, time.Minute)
	if err != nil {
		t.Fatalf("容器内执行命令失败: %v", err)
	}
	if !strings.Contains(run.Stdout, "agentflow-ok") {
		t.Fatalf("容器内命令输出不符: %q", run.Stdout)
	}
	// 常驻容器可能没有输出，这里只要求日志读取本身成功。
	if _, err := runner.ContainerLogs(ctx, dockerID, 20); err != nil {
		t.Fatalf("读取容器日志失败: %v", err)
	}
}

// verifyStopAndDelete 验证停止与删除行为。
func verifyStopAndDelete(
	t *testrunner.Context,
	ctx context.Context,
	runner *docker.Runner,
	dockerID string,
) {
	t.Helper()
	if err := runner.StopContainer(ctx, dockerID, 10); err != nil {
		t.Fatalf("停止测试容器失败: %v", err)
	}
	inspect, err := runner.InspectContainer(ctx, dockerID)
	if err != nil {
		t.Fatalf("检查测试容器失败: %v", err)
	}
	if inspect.Running {
		t.Fatalf("停止后容器不应仍在运行: %+v", inspect)
	}
	result, err := runner.RemoveContainer(ctx, dockerID)
	if err != nil {
		t.Fatalf("删除测试容器失败: %v", err)
	}
	if !result.Removed {
		t.Fatalf("删除测试容器应成功: %+v", result)
	}
	// 再次删除同一个容器必须幂等成功。
	again, err := runner.RemoveContainer(ctx, dockerID)
	if err != nil {
		t.Fatalf("重复删除已不存在的容器应视为成功: %v", err)
	}
	if !again.Removed {
		t.Fatalf("重复删除应返回成功: %+v", again)
	}
	if _, err := runner.InspectContainer(ctx, dockerID); docker.ErrorCodeOf(err) != docker.CodeContainerNotFound {
		t.Fatalf("删除后容器不应存在: %v", err)
	}
}
