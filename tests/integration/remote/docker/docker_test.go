// Package docker_remote 提供需要真实远程 Linux 机器与真实 Docker Engine 的集成测试。
//
// 运行方式：
//
//	cp tests/config/example.ssh.yml tests/config/ssh.yml       # 填写远程目标
//	cp tests/config/example.docker.yml tests/config/docker.yml # 设置 remote_enabled: true
//	go test -count=1 -v ./tests/integration/remote/docker/...
//
// 配置项来自 tests/config/ssh.yml 与 tests/config/docker.yml，字段说明见对应的
// 示例文件。
//
// 安全约束：
//   - 只有 tests/config/docker.yml 中 docker.remote_enabled 为 true 时才会修改真实远程 Docker；
//   - SSH 目标缺失时输出 WARN 并跳过；
//   - 目标配置完整但连接、认证、主机密钥校验失败或 Docker 不可用时必须让测试失败；
//   - 所有镜像标签、容器名称与远程目录都带随机后缀，并只操作本次测试创建的资源；
//   - 不执行全局 prune，不删除共享镜像、共享缓存卷或其他测试的资源。
package docker_remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// remoteOpTimeout 是单次远程操作的超时上限。
const remoteOpTimeout = 5 * time.Minute

// testImageRepository 是测试镜像的仓库名。
//
// 该仓库只由测试使用，与生产环境的 agentflow-* 镜像命名空间区分开。
const testImageRepository = "agentflow-test-lifecycle"

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
func requireEnabled(t *testing.T) target {
	t.Helper()
	enabled, _ := testkit.LoadDockerConfig(t)
	if !enabled.RemoteEnabled {
		reason := fmt.Sprintf(
			"WARN [SKIP] remote docker integration tests: 请把 %s 中的 docker.remote_enabled 设为 true。",
			testkit.ConfigPath(t, testkit.DockerConfigFile),
		)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	sshConfig, _ := testkit.LoadSSHConfig(t)
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

// dial 建立经过主机密钥校验与身份认证的连接，并在测试结束时关闭。
//
// 主机密钥校验与产品实现完全一致，不会为了通过测试而接受未知主机。
func dial(t *testing.T, ws *testkit.Workspace, tg target) sshclient.Client {
	t.Helper()
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: tg.knownHostsPaths,
		// 信任记录写在测试工作空间中，不修改用户真实的 known_hosts。
		StorePath: filepath.Join(ws.Root(), "known_hosts"),
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

// newWorkspace 创建本次用例的测试工作空间并输出其绝对路径。
func newWorkspace(t *testing.T) *testkit.Workspace {
	t.Helper()
	ws := testkit.New(t)
	ws.Report()
	return ws
}

// randomSuffix 返回随机的十六进制后缀，用于隔离远程测试资源。
func randomSuffix(t *testing.T) string {
	t.Helper()
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buffer)
}

// remoteTestRoot 返回本次用例专属的远程测试目录。
//
// 该目录位于远程 Home 下的 agentflow-tests 中，与生产工作空间完全隔离。
func remoteTestRoot(t *testing.T, ctx context.Context, client sshclient.Client, suffix string) string {
	t.Helper()
	home, err := sshclient.ResolveHome(ctx, client, remoteOpTimeout)
	if err != nil {
		t.Fatalf("解析远程 Home 失败: %v", err)
	}
	root := path.Join(home, "agentflow-tests", "docker", suffix)
	t.Logf("REMOTE_TEST_DIR=%s", root)
	return root
}

// TestRemoteDockerPreflight 验证真实远程机器的 Docker 预检。
//
// 该用例只做只读检查，只会在专属远程测试目录中创建目录。
func TestRemoteDockerPreflight(t *testing.T) {
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
}

// TestRemoteDockerContainerLifecycle 在真实远程机器上验证容器完整生命周期。
//
// 该用例只使用带随机后缀的测试镜像标签、容器名称与远程目录，并只清理自己创建的
// 测试资源；不会执行全局 prune，也不会删除共享镜像或缓存卷。
func TestRemoteDockerContainerLifecycle(t *testing.T) {
	t.Parallel()
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

	// 测试结束后只删除本次创建并已核验所有权的测试容器。
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if _, err := runner.RemoveContainer(cleanupCtx, dockerID); err != nil {
			t.Logf("WARN 清理测试容器失败: %v", err)
		}
	})

	verifyOwnershipAndRunning(t, ctx, runner, container, dockerID)
	verifyExecAndLogs(t, ctx, runner, dockerID)
	verifyStopAndDelete(t, ctx, runner, dockerID)
}

// buildTestImage 构建带随机标签的测试镜像，返回核实后的镜像 ID。
func buildTestImage(
	t *testing.T,
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
	t *testing.T,
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
	t *testing.T,
	ctx context.Context,
	runner *docker.Runner,
	container domain.Container,
	imageRef string,
) string {
	t.Helper()
	dockerID, err := runner.CreateContainer(ctx, docker.CreateContainerSpec{
		Container: container,
		ImageRef:  imageRef,
		WorkDir:   domain.DefaultContainerWorkDir,
	})
	if err != nil {
		t.Fatalf("创建测试容器失败: %v", err)
	}
	if err := runner.StartContainer(ctx, dockerID); err != nil {
		t.Fatalf("启动测试容器失败: %v", err)
	}
	return dockerID
}

// verifyOwnershipAndRunning 验证所有权标签与运行状态。
func verifyOwnershipAndRunning(
	t *testing.T,
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
	t *testing.T,
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
	t *testing.T,
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
