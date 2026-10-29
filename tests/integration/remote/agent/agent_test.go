// Package agent_remote 提供需要真实远程 Linux 机器、真实 Docker 与容器内真实
// Pi CLI 的端到端集成测试。
//
// 运行方式（Git Bash / Linux / macOS）：
//
//	cp tests/config/example.ssh.yml tests/config/ssh.yml          # 开启 ssh.enable 并填写远程目标
//	cp tests/config/example.docker.yml tests/config/docker.yml    # 开启 remote_enabled，可选填 docker.pi
//	cp tests/config/example.pi.yml tests/config/pi.yml            # 按需填写模型（enable 只控制本机真实 Pi 测试）
//	cp tests/config/example.git.yml tests/config/git.yml          # 填写可访问的仓库
//	go test -count=1 -v ./tests/integration/remote/agent/...
//
// 配置项来自 tests/config 下的 ssh.yml、docker.yml、pi.yml 与 git.yml，
// 字段说明见对应的示例文件。
//
// 安全约束：
//   - 只有 tests/config/docker.yml 中 docker.remote_enabled 为 true 时才连接远程 Docker；
//   - SSH 目标缺失时输出 WARN 并跳过；目标完整但认证、主机密钥校验失败或
//     Docker 不可用时必须让测试失败；
//   - 只操作本次测试创建并已核验所有权的容器与远程目录；
//   - 不执行全局 prune，不删除共享镜像、共享缓存卷或其他测试的资源；
//   - 始终使用正式的主机密钥校验，禁止关闭验证或自动接受未知主机；
//   - 填写 docker.pi 时只覆盖容器内 Pi 自己的配置目录，密钥不写入日志或宿主机。
package agent_remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// DefaultModel 是真实调用的默认模型，必须是免费模型。
const DefaultModel = testkit.DefaultPiModel

// DefaultImage 是测试使用的默认镜像引用。
//
// 该镜像由 tests/integration/remote/agent/Dockerfile 构建，包含固定版本的
// Git 与 Pi CLI，构建方式见该文件头部说明。
const DefaultImage = "agentflow-test-agent:1.0.0"

// 远程操作超时。
const (
	remoteOpTimeout   = 5 * time.Minute
	remoteRunTimeout  = 10 * time.Minute
	containerWorkDir  = "/workspace/project"
	containerSessions = "/tmp/agentflow/sessions"
)

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
			"WARN [SKIP] remote agent integration tests: 请把 %s 中的 docker.remote_enabled 设为 true。",
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

// resolveModel 返回本次真实调用使用的模型标识。
//
// 取值来自 tests/config/pi.yml 的 pi.model，未填写时使用默认免费模型。
func resolveModel(t *testing.T) string {
	t.Helper()
	config, _ := testkit.LoadPiConfig(t)
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = DefaultModel
	}
	t.Logf("PI_MODEL=%s", model)
	return model
}

// resolveImage 返回测试使用的镜像引用。
//
// 取值来自 tests/config/docker.yml 的 docker.agent_image，未配置时使用默认镜像。
func resolveImage(t *testing.T) string {
	t.Helper()
	config, _ := testkit.LoadDockerConfig(t)
	image := strings.TrimSpace(config.AgentImage)
	if image == "" {
		image = DefaultImage
	}
	return image
}

// resolveContainerPi 返回容器内 Pi 的可选覆盖配置。
//
// 取值来自 tests/config/docker.yml 的 docker.pi；未填写时返回未启用，容器内 Pi
// 沿用镜像内已有配置。只填写了一部分时输出 WARN 并跳过，并指明缺少的配置项，
// 避免把配置错误当成"容器内没有凭据"。
func resolveContainerPi(t *testing.T) testkit.ContainerPiConfig {
	t.Helper()
	config, _ := testkit.LoadDockerConfig(t)
	if missing := config.Pi.Missing(); len(missing) > 0 {
		reason := testkit.SkipReason(t, testkit.DockerConfigFile, missing)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	return config.Pi
}

// applyContainerPi 把可选的容器内 Pi 配置写入容器，并返回实际使用的模型标识。
//
// 覆盖配置未启用时保持容器内 Pi 原样，模型仍取 pi.yml。启用时直接覆盖容器内 Pi
// 自己的配置目录（容器内 $HOME/.pi/agent）下的 models.json 与 settings.json：
// 这两个文件只存在于容器内部文件系统，不写入项目代码目录、不写入远程项目配置目录、
// 不挂载宿主机目录，容器删除后随之消失；密钥不会被写入日志。
func applyContainerPi(
	t *testing.T,
	ctx context.Context,
	exec *remoteExecutor,
	dockerID string,
	config testkit.ContainerPiConfig,
	fallbackModel string,
) string {
	t.Helper()
	if !config.Enabled() {
		t.Logf("CONTAINER_PI_CONFIG=none（沿用容器内已有配置）")
		return fallbackModel
	}
	home, err := containerHome(ctx, exec, dockerID)
	if err != nil {
		t.Fatalf("解析容器内 Home 失败: %v", err)
	}
	dir := home + "/" + testkit.ContainerPiConfigDir
	if err := execContainerCommand(ctx, exec, dockerID, []string{"mkdir", "-p", dir}); err != nil {
		t.Fatalf("创建容器内 Pi 配置目录失败: %v", err)
	}
	write := func(name string, content []byte) {
		t.Helper()
		command := []string{
			"sh", "-c",
			fmt.Sprintf("cat > %s && chmod 600 %s", name, name),
		}
		run, runErr := exec.runner.ExecCommandWithInput(
			ctx,
			dockerID,
			command,
			strings.NewReader(string(content)),
			remoteOpTimeout,
		)
		if runErr != nil || !run.Succeeded() {
			t.Fatalf("写入容器内 Pi 配置 %s 失败: %v（退出码 %d）: %s",
				name, runErr, run.ExitCode, summarize(run.Stderr))
		}
	}
	write(dir+"/models.json", testkit.ContainerPiModelsFile(config))
	write(dir+"/settings.json", testkit.ContainerPiSettingsFile(config))
	t.Logf("CONTAINER_PI_CONFIG=%s（provider %s，model %s，thinking %s）",
		dir, config.ResolvedProvider(), config.Model, config.ThinkingLevel)
	return strings.TrimSpace(config.Model)
}

// containerHome 返回容器内登录用户的 Home 目录。
//
// 与产品解析远程 Home 的方式一致：命令完全固定，不拼接任何外部内容。
func containerHome(ctx context.Context, exec *remoteExecutor, dockerID string) (string, error) {
	run, err := exec.runner.ExecCommand(
		ctx,
		dockerID,
		[]string{"sh", "-c", `printf %s "$HOME"`},
		remoteOpTimeout,
	)
	if err != nil {
		return "", err
	}
	if !run.Succeeded() {
		return "", fmt.Errorf("查询容器内 Home 退出码 %d: %s", run.ExitCode, summarize(run.Stderr))
	}
	home := strings.TrimSpace(run.Stdout)
	if !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("容器内 Home 不是绝对路径: %q", home)
	}
	return home, nil
}

// execContainerCommand 在容器内执行一条命令，非零退出码视为失败。
func execContainerCommand(
	ctx context.Context,
	exec *remoteExecutor,
	dockerID string,
	command []string,
) error {
	run, err := exec.runner.ExecCommand(ctx, dockerID, command, remoteOpTimeout)
	if err != nil {
		return err
	}
	if !run.Succeeded() {
		return fmt.Errorf("容器内命令退出码 %d: %s", run.ExitCode, summarize(run.Stderr))
	}
	return nil
}

// ensureImage 确认测试镜像在远端存在，缺失时按需在远端构建。
//
// 该镜像由 tests/integration/remote/agent/Dockerfile 定义，基础镜像固定为
// node:22.19.0-bookworm-slim。远端机器通常无法访问 Docker Hub，因此这里只在
// 镜像确实缺失时才执行构建，并直接使用远端本地已有的基础镜像。
//
// 构建失败时让测试失败：镜像不可用属于真实环境问题，不能当成跳过。
func ensureImage(
	t *testing.T,
	ctx context.Context,
	client sshclient.Client,
	runner *docker.Runner,
	image string,
) {
	t.Helper()
	if _, err := runner.InspectImageID(ctx, image); err == nil {
		t.Logf("TEST_IMAGE_PRESENT=%s", image)
		return
	}
	dockerfile, err := os.ReadFile(dockerfilePath(t))
	if err != nil {
		t.Fatalf("读取测试 Dockerfile 失败: %v", err)
	}
	buildContext, err := docker.NewContext([]docker.ContextFile{
		{
			RelativePath: docker.DockerfileName,
			Content:      dockerfile,
			Mode:         0o644,
		},
	})
	if err != nil {
		t.Fatalf("构造测试镜像构建上下文失败: %v", err)
	}
	transfer, err := client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := transfer.Close(); closeErr != nil {
			t.Logf("WARN 关闭 SFTP 通道失败: %v", closeErr)
		}
	})
	remoteDir := path.Join(remoteTempRoot(t, ctx, client), "agent-image-"+randomSuffix(t))
	version := domain.EnvironmentVersion{
		HostID:           "remote-agent-test-host",
		Ref:              domain.EnvironmentRef{Type: domain.EnvironmentTypeBase, Name: "base"},
		Number:           1,
		DockerfileSHA256: buildContext.Hash(),
		ContextSHA256:    buildContext.Hash(),
		ImageRef:         image,
		Status:           domain.BuildStatusBuilding,
		CreatedAt:        time.Now().UTC(),
	}
	version.BuildConfigSHA256 = docker.BuildConfigHash(version, "")
	result, buildErr := runner.BuildImage(ctx, transfer, docker.BuildSpec{
		Version:          version,
		Context:          buildContext,
		RemoteContextDir: path.Join(remoteDir, "context"),
		LogPath:          path.Join(remoteDir, "build.log"),
	})
	if buildErr != nil {
		t.Fatalf(
			"按需构建测试镜像失败: %v\n远程日志: %s\n日志末尾:\n%s",
			buildErr,
			result.LogPath,
			result.LogTail,
		)
	}
	if result.ImageID == "" {
		t.Fatalf("构建成功但未取得实际镜像 ID: %+v", result)
	}
	t.Logf("TEST_IMAGE_BUILT=%s IMAGE_ID=%s", image, result.ImageID)
}

// dockerfilePath 返回测试 Dockerfile 的绝对路径。
func dockerfilePath(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试源码路径")
	}
	return filepath.Join(filepath.Dir(currentFile), "Dockerfile")
}

// remoteTempRoot 返回本次用例在远端 Home 下的专属临时根目录。
//
// 目录统一位于远程 Home 下的 .lingting/agentflow-tests/agent，与生产工作空间
// (~/.lingting/agentflow) 完全隔离；解析与创建统一由 testkit 提供。
func remoteTempRoot(t *testing.T, ctx context.Context, client sshclient.Client) string {
	t.Helper()
	root, err := testkit.ResolveRemoteTestRoot(
		ctx,
		client,
		remoteOpTimeout,
		testkit.RemoteTestPackageAgent,
	)
	if err != nil {
		t.Fatalf("准备远程测试目录失败: %v", err)
	}
	t.Logf("REMOTE_TEST_ROOT=%s", root)
	return root
}

// resolveGitConfig 返回测试使用的 Git 仓库与凭证配置。
//
// 取值来自 tests/config/git.yml；仓库地址留空时输出警告并跳过，因为远端通常无法
// 访问任意外部仓库。账号密码只填写一半属于配置错误，直接让测试失败，而不是当成
// "没有凭据"静默降级为匿名访问。
func resolveGitConfig(t *testing.T) testkit.GitConfig {
	t.Helper()
	config, _ := testkit.LoadGitConfig(t)
	if config.ResolvedRepository() == "" {
		reason := testkit.SkipReason(t, testkit.GitConfigFile, []string{"git.repository"})
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	if missing := config.CredentialMissing(); len(missing) > 0 {
		t.Fatalf(
			"%s 中的 Git 凭证不完整，缺少 %s",
			testkit.ConfigPath(t, testkit.GitConfigFile),
			strings.Join(missing, ", "),
		)
	}
	return config
}

// applyGitCredentials 把 git.yml 中的可选凭证注入容器内临时目录。
//
// 凭证只存在于容器内部文件系统：不写入项目代码目录、不写入远程项目配置目录、
// 不挂载宿主机目录，容器删除后随之消失；私钥只被读取后写入容器内临时文件。
func applyGitCredentials(
	t *testing.T,
	ctx context.Context,
	exec *remoteExecutor,
	dockerID string,
	config testkit.GitConfig,
) {
	t.Helper()
	auth, err := testkit.ApplyGitCredentials(
		ctx,
		exec,
		dockerID,
		config,
		"/tmp/agentflow-credentials-"+randomSuffix(t),
	)
	if err != nil {
		t.Fatalf("注入容器内 Git 凭证失败: %v", err)
	}
	t.Logf("CONTAINER_GIT_CREDENTIAL=%s", auth.Kind)
}

// dial 建立经过主机密钥校验与身份认证的连接。
func dial(t *testing.T, ws *testkit.Workspace, tg target) sshclient.Client {
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

// randomSuffix 生成带随机后缀的标识，避免与其他测试资源冲突。
func randomSuffix(t *testing.T) string {
	t.Helper()
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buffer)
}

// remoteExecutor 把 agent.ContainerExecutor 与 source.ContainerExecutor 适配到真实
// 远程 Docker 与 SFTP 通道。
type remoteExecutor struct {
	client   sshclient.Client
	transfer sshclient.FileTransfer
	runner   *docker.Runner
}

// newRemoteExecutor 构造远程执行器。
func newRemoteExecutor(t *testing.T, client sshclient.Client) *remoteExecutor {
	t.Helper()
	runner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	transfer, err := client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	t.Cleanup(func() {
		_ = transfer.Close()
	})
	return &remoteExecutor{client: client, transfer: transfer, runner: runner}
}

// Exec 在容器内执行命令并写入标准输入。
func (e *remoteExecutor) Exec(
	ctx context.Context,
	request agent.ContainerExecRequest,
) (agent.ContainerExecResult, error) {
	var (
		run docker.CommandResult
		err error
	)
	if len(request.Stdin) == 0 {
		run, err = e.runner.ExecCommand(ctx, request.DockerID, request.Command, request.Timeout)
	} else {
		run, err = e.runner.ExecCommandWithInput(
			ctx,
			request.DockerID,
			request.Command,
			strings.NewReader(string(request.Stdin)),
			request.Timeout,
		)
	}
	return agent.ContainerExecResult{
		ExitCode:        run.ExitCode,
		Stdout:          run.Stdout,
		Stderr:          run.Stderr,
		StdoutTruncated: run.Truncated,
		StderrTruncated: run.Truncated,
		StdinStatus:     run.Stdin.Status,
		StdinBytes:      run.Stdin.Bytes,
		StdinError:      run.Stdin.Error,
		Duration:        run.Duration,
		TimedOut:        run.TimedOut(),
		Cancelled:       run.Cancelled(),
		Certain:         run.Certain,
	}, err
}

// ExecContainer 在容器内执行命令并写入标准输入，供 testkit 注入 Git 凭证使用。
func (e *remoteExecutor) ExecContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	stdin string,
) (testkit.GitCredentialResult, error) {
	var (
		run docker.CommandResult
		err error
	)
	if stdin == "" {
		run, err = e.runner.ExecCommand(ctx, dockerID, command, remoteOpTimeout)
	} else {
		run, err = e.runner.ExecCommandWithInput(
			ctx,
			dockerID,
			command,
			strings.NewReader(stdin),
			remoteOpTimeout,
		)
	}
	if err != nil {
		return testkit.GitCredentialResult{}, err
	}
	return testkit.GitCredentialResult{
		ExitCode: run.ExitCode,
		Stdout:   run.Stdout,
		Stderr:   run.Stderr,
	}, nil
}

// CopyFromContainer 用 docker cp 把容器内文件复制到远程临时目录。
func (e *remoteExecutor) CopyFromContainer(
	ctx context.Context,
	dockerID, containerPath, hostPath string,
) error {
	if err := e.transfer.MkdirAll(ctx, path.Dir(hostPath)); err != nil {
		return err
	}
	// 统一走 docker.Runner 的命令构造入口：测试替身不得自行拼接 Docker 命令行，
	// 否则会绕过 DOCKER_API_VERSION 的统一控制。
	run, runErr := e.runner.CopyFromContainer(
		ctx,
		dockerID,
		containerPath,
		hostPath,
		remoteOpTimeout,
	)
	if runErr != nil {
		return runErr
	}
	if !run.Succeeded() {
		return fmt.Errorf("docker cp 失败（退出码 %d）: %s", run.ExitCode, run.Stderr)
	}
	return nil
}

// DownloadToLocal 把远程临时文件下载到本机。
func (e *remoteExecutor) DownloadToLocal(
	ctx context.Context,
	hostPath, localPath string,
	maxBytes int64,
) (int64, error) {
	if err := e.transfer.Download(ctx, hostPath, localPath, sshclient.DownloadOptions{
		Overwrite:    true,
		CreateParent: true,
		MaxBytes:     maxBytes,
	}); err != nil {
		return 0, err
	}
	info, err := e.transfer.Stat(ctx, hostPath)
	if err != nil {
		return 0, nil
	}
	return info.Size, nil
}

// RemoveHostFile 删除远程临时文件。
func (e *remoteExecutor) RemoveHostFile(ctx context.Context, hostPath string) error {
	return e.transfer.RemoveAll(ctx, hostPath)
}

// ExecInContainer 在容器内执行命令，满足 source.ContainerExecutor。
func (e *remoteExecutor) ExecInContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	return e.runner.ExecCommand(ctx, dockerID, command, timeout)
}

// guard 核验容器所有权与运行状态。
type guard struct {
	runner *docker.Runner
	record domain.Container
}

// EnsureExecutable 核验容器归属与运行状态。
func (g *guard) EnsureExecutable(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	inspect, err := g.runner.InspectContainer(ctx, g.record.DockerID)
	if err != nil {
		return domain.Container{}, err
	}
	if err := docker.VerifyOwnership(inspect, g.record); err != nil {
		return domain.Container{}, err
	}
	if !inspect.Running {
		return domain.Container{}, fmt.Errorf("容器 %s 当前未运行", g.record.ID)
	}
	return g.record, nil
}

// createContainer 创建并启动一个带随机后缀的受管理测试容器。
func createContainer(
	t *testing.T,
	ctx context.Context,
	exec *remoteExecutor,
	suffix string,
) domain.Container {
	t.Helper()
	image := resolveImage(t)
	ensureImage(t, ctx, exec.client, exec.runner, image)
	containerID := domain.NewID()
	name := "agentflow-test-agent-" + suffix
	record, err := domain.NewContainer(
		containerID,
		domain.ID("remote-agent-test-host"),
		name,
		domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
		"v0001",
		strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("b", 64),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	if err := exec.runner.CheckRunContract(ctx, image); err != nil {
		t.Fatalf("镜像 %s 不满足运行契约: %v", image, err)
	}
	// 创建之前登记清理责任：容器名称已知，创建或启动失败也不会失去清理能力。
	tracked := containerRun.Track(t, exec.runner, testkit.TrackedContainer{
		Name:   record.Name,
		Record: &record,
	})
	dockerID, err := exec.runner.CreateContainer(ctx, docker.CreateContainerSpec{
		Container: record,
		ImageRef:  image,
		WorkDir:   containerWorkDir,
	})
	if err != nil {
		t.Fatalf("创建测试容器失败: %v", err)
	}
	// 取得真实容器 ID 后立即补写登记信息，后续步骤失败也能精确清理。
	tracked.DockerID = dockerID
	if err := record.MarkCreated(dockerID, domain.DockerStateCreated, time.Now()); err != nil {
		t.Fatalf("记录容器创建结果失败: %v", err)
	}
	if err := exec.runner.StartContainer(ctx, dockerID); err != nil {
		t.Fatalf("启动测试容器失败: %v", err)
	}
	if err := record.MarkRunning(time.Now()); err != nil {
		t.Fatalf("记录容器运行状态失败: %v", err)
	}
	inspect, err := exec.runner.InspectContainer(ctx, dockerID)
	if err != nil {
		t.Fatalf("核验测试容器失败: %v", err)
	}
	if err := docker.VerifyOwnership(inspect, record); err != nil {
		t.Fatalf("测试容器所有权核验失败: %v", err)
	}
	if !inspect.Running {
		t.Fatalf("测试容器未进入运行状态: %s", inspect.State)
	}
	// 只删除本次测试创建的容器；清理会在删除前重新核验所有权，并在删除后核验容器
	// 确实已不存在。
	t.Logf("TEST_CONTAINER=%s (%s)", record.ID, dockerID)
	return record
}

// requireContainerModel 确认容器内 Pi 已配置可用模型，否则明确跳过。
//
// 容器内 Pi 的模型凭据来自镜像内的用户配置，属于测试无法自行提供的外部依赖：
// 本机环境变量不会、也不应被注入测试容器。凭据缺失时输出 WARN [SKIP] 并说明原因，
// 不能把它记成通过，也不能把它伪装成 Docker 兼容性问题。
func requireContainerModel(
	t *testing.T,
	ctx context.Context,
	exec *remoteExecutor,
	dockerID, model string,
) {
	t.Helper()
	list, listErr := exec.runner.ExecCommand(
		ctx, dockerID, []string{"pi", "--list-models"}, remoteOpTimeout,
	)
	if listErr != nil || !list.Succeeded() {
		reason := fmt.Sprintf(
			"WARN [SKIP] remote agent integration tests: 容器内 pi --list-models 失败: %v（退出码 %d）: %s",
			listErr,
			list.ExitCode,
			summarize(list.Stderr),
		)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	available := strings.TrimSpace(list.Stdout)
	if strings.Contains(available, "No models available") {
		reason := "WARN [SKIP] remote agent integration tests: 容器内 Pi 未配置模型凭据" +
			"（No models available），无法执行真实模型调用。"
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	if !strings.Contains(available, model) {
		reason := fmt.Sprintf(
			"WARN [SKIP] remote agent integration tests: 容器内 Pi 未提供模型 %s。可用模型:\n%s",
			model,
			available,
		)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	t.Logf("CONTAINER_MODEL=%s", model)
}

// summarize 截断容器内命令输出，避免把整份模型列表写进失败信息。
func summarize(output string) string {
	trimmed := strings.TrimSpace(output)
	const limit = 300
	if len(trimmed) > limit {
		return trimmed[:limit] + "..."
	}
	return trimmed
}

// probeContainerTools 确认容器内 Git 与 Pi 可用。
//
// 该检查在容器内执行，因此本机是否安装 Pi 与本次测试无关。
func probeContainerTools(t *testing.T, ctx context.Context, exec *remoteExecutor, dockerID string) {
	t.Helper()
	git, gitErr := exec.runner.ExecCommand(
		ctx, dockerID, []string{"git", "--version"}, remoteOpTimeout,
	)
	if gitErr != nil || !git.Succeeded() {
		t.Fatalf("容器内没有可用的 Git: %v（退出码 %d）", gitErr, git.ExitCode)
	}
	t.Logf("CONTAINER_GIT=%s", strings.TrimSpace(git.Stdout))
	piVersion, piErr := exec.runner.ExecCommand(
		ctx, dockerID, []string{"pi", "--version"}, remoteOpTimeout,
	)
	if piErr != nil || !piVersion.Succeeded() {
		t.Fatalf("容器内没有可用的 Pi CLI: %v（退出码 %d）", piErr, piVersion.ExitCode)
	}
	t.Logf("CONTAINER_PI=%s", strings.TrimSpace(piVersion.Stdout))
}

// TestRemoteSourceFetch 验证代码确实被获取到远程任务容器内部。
func TestRemoteSourceFetch(t *testing.T) {
	t.Parallel()
	tg := requireEnabled(t)
	gitConfig := resolveGitConfig(t)
	repository := gitConfig.ResolvedRepository()
	ws := testkit.New(t)
	ws.Report()
	client := dial(t, ws, tg)
	exec := newRemoteExecutor(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), remoteRunTimeout)
	t.Cleanup(cancel)

	suffix := randomSuffix(t)
	record := createContainer(t, ctx, exec, suffix)
	probeContainerTools(t, ctx, exec, record.DockerID)

	applyGitCredentials(t, ctx, exec, record.DockerID, gitConfig)
	taskID := "task-" + suffix
	provider, err := source.NewProvider(source.Options{
		Executor: exec,
		Guard:    &guard{runner: exec.runner, record: record},
	})
	if err != nil {
		t.Fatalf("构造代码来源失败: %v", err)
	}
	result, err := provider.Fetch(ctx, source.Request{
		TaskID:        taskID,
		ContainerID:   record.ID.String(),
		RepositoryURL: repository,
	})
	if err != nil {
		t.Fatalf("远程代码获取失败: %v", err)
	}
	if result.Commit == "" {
		t.Fatalf("结果缺少 commit SHA")
	}
	if result.TaskBranch != "agent/"+taskID {
		t.Fatalf("期望任务分支 agent/%s，实际 %q", taskID, result.TaskBranch)
	}
	// 代码必须在容器内部，且容器内目录确实存在 Git 工作区。
	check, checkErr := exec.runner.ExecCommand(ctx, record.DockerID, []string{
		"sh", "-c",
		"test -d " + containerWorkDir + "/.git && git -C " + containerWorkDir + " rev-parse HEAD",
	}, remoteOpTimeout)
	if checkErr != nil || !check.Succeeded() {
		t.Fatalf("容器内工作区校验失败: %v（退出码 %d）", checkErr, check.ExitCode)
	}
	containerCommit := strings.TrimSpace(check.Stdout)
	if containerCommit != result.Commit {
		t.Fatalf("容器内 commit 与结果不一致: %s != %s", containerCommit, result.Commit)
	}
	t.Logf("CONTAINER_COMMIT=%s", containerCommit)
	t.Logf("BASE_BRANCH=%s", result.BaseBranch)
	// 远程宿主机上不应出现项目代码目录。
	hostCheck, hostErr := exec.runner.Run(
		ctx,
		"host check",
		"test -d "+containerWorkDir+" && echo present || echo absent",
		remoteOpTimeout,
	)
	if hostErr != nil {
		t.Fatalf("检查宿主机目录失败: %v", hostErr)
	}
	t.Logf("HOST_WORKDIR=%s", strings.TrimSpace(hostCheck.Stdout))
}

// TestRemoteAgentRunSessions 验证容器内 Pi 调用、独立 Session 与本机持久化。
func TestRemoteAgentRunSessions(t *testing.T) {
	t.Parallel()
	tg := requireEnabled(t)
	model := resolveModel(t)
	containerPi := resolveContainerPi(t)
	ws := testkit.New(t)
	ws.Report()
	client := dial(t, ws, tg)
	exec := newRemoteExecutor(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), remoteRunTimeout)
	t.Cleanup(cancel)

	suffix := randomSuffix(t)
	record := createContainer(t, ctx, exec, suffix)
	probeContainerTools(t, ctx, exec, record.DockerID)
	model = applyContainerPi(t, ctx, exec, record.DockerID, containerPi, model)
	requireContainerModel(t, ctx, exec, record.DockerID, model)

	sessionRoot := ws.SessionRoot()
	runner, err := pi.NewContainerRunner(pi.Options{
		Timeout:       remoteRunTimeout,
		ExportTimeout: remoteOpTimeout,
		GraceTimeout:  5 * time.Second,
	}, exec, &guard{runner: exec.runner, record: record})
	if err != nil {
		t.Fatalf("构造容器内 Runner 失败: %v", err)
	}
	// 两次调用必须使用不同的调用标识与 Session 文件。
	invocationIDs := make([]string, 0, 2)
	sessionPaths := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		result, runErr := runner.Run(ctx, agent.RunRequest{
			SessionID:   fmt.Sprintf("session-%s-%d", suffix, index),
			TaskID:      "task-" + suffix,
			RoleType:    "developer",
			Model:       model,
			Prompt:      "只回答 READY 这一个词，不要使用任何工具。",
			SessionRoot: sessionRoot,
			Timeout:     remoteRunTimeout,
			Container: &agent.ContainerTarget{
				ContainerID: record.ID.String(),
				DockerID:    record.DockerID,
				WorkDir:     containerWorkDir,
				SessionDir:  containerSessions,
			},
		}, nil)
		if result.Session.JSONLPath != "" {
			t.Logf("SESSION_JSONL_%d=%s", index, result.Session.JSONLPath)
		}
		if runErr != nil {
			t.Fatalf("第 %d 次远程 Agent 调用失败: %v（Session: %s）",
				index+1, runErr, result.Session.JSONLPath)
		}
		if result.Status != agent.RunCompleted {
			t.Fatalf("第 %d 次调用期望状态 %s，实际 %s",
				index+1, agent.RunCompleted, result.Status)
		}
		if result.ContainerID != record.ID.String() {
			t.Fatalf("结果中的容器标识不符: %s", result.ContainerID)
		}
		if result.DockerID != record.DockerID {
			t.Fatalf("结果中的 Docker 容器 ID 不符: %s", result.DockerID)
		}
		if result.Session.Completeness != agent.SessionComplete {
			t.Fatalf("第 %d 次调用期望 Session 完整，实际 %s（%s）",
				index+1, result.Session.Completeness, result.Session.Error)
		}
		if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
			t.Fatalf("本机 Session 文件不存在: %v", statErr)
		}
		invocationIDs = append(invocationIDs, result.InvocationID)
		sessionPaths = append(sessionPaths, result.Session.JSONLPath)
	}
	if invocationIDs[0] == invocationIDs[1] {
		t.Fatalf("两次调用使用了相同的调用标识: %s", invocationIDs[0])
	}
	if sessionPaths[0] == sessionPaths[1] {
		t.Fatalf("两次调用使用了相同的 Session 文件: %s", sessionPaths[0])
	}
	// 删除容器后本机 Session 仍可读。
	if _, removeErr := exec.runner.RemoveContainer(ctx, record.DockerID); removeErr != nil {
		t.Fatalf("删除测试容器失败: %v", removeErr)
	}
	for _, sessionPath := range sessionPaths {
		content, readErr := os.ReadFile(sessionPath)
		if readErr != nil {
			t.Fatalf("容器删除后本机 Session 应仍可读: %v", readErr)
		}
		if len(content) == 0 {
			t.Fatalf("本机 Session 内容为空: %s", sessionPath)
		}
	}
	t.Logf("INVOCATION_IDS=%s,%s", invocationIDs[0], invocationIDs[1])
}
