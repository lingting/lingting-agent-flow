// Package agent_remote 提供需要真实远程 Linux 机器、真实 Docker 与容器内真实
// Pi CLI 的端到端集成测试。
//
// 运行方式（Git Bash / Linux / macOS）：
//
//	AGENTFLOW_TEST_REMOTE_DOCKER=1 \
//	AGENTFLOW_TEST_SSH_HOST=10.0.0.10 \
//	AGENTFLOW_TEST_SSH_USER=deploy \
//	AGENTFLOW_TEST_SSH_KEY_PATH=$HOME/.ssh/id_ed25519 \
//	go test -count=1 -v ./tests/integration/remote/agent/...
//
// PowerShell 示例：
//
//	$env:AGENTFLOW_TEST_REMOTE_DOCKER="1"
//	$env:AGENTFLOW_TEST_SSH_HOST="10.0.0.10"
//	$env:AGENTFLOW_TEST_SSH_USER="deploy"
//	go test -count=1 -v ./tests/integration/remote/agent/...
//
// 安全约束：
//   - 只有显式设置 AGENTFLOW_TEST_REMOTE_DOCKER=1 时才连接远程 Docker；
//   - SSH 目标缺失时输出 WARN 并跳过；目标完整但认证、主机密钥校验失败或
//     Docker 不可用时必须让测试失败；
//   - 只操作本次测试创建并已核验所有权的容器与远程目录；
//   - 不执行全局 prune，不删除共享镜像、共享缓存卷或其他测试的资源；
//   - 始终使用正式的主机密钥校验，禁止关闭验证或自动接受未知主机。
package agent_remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
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

// 真实远程测试使用的环境变量。
const (
	// EnvRemoteDocker 是显式启用远程 Docker 测试的开关。
	EnvRemoteDocker = "AGENTFLOW_TEST_REMOTE_DOCKER"
	// EnvHost 是 SSH 服务器地址，必填。
	EnvHost = "AGENTFLOW_TEST_SSH_HOST"
	// EnvUser 是登录用户名，必填。
	EnvUser = "AGENTFLOW_TEST_SSH_USER"
	// EnvPort 是 SSH 端口，可选，默认 22。
	EnvPort = "AGENTFLOW_TEST_SSH_PORT"
	// EnvKeyPath 是测试使用的私钥路径，可选；为空时使用 SSH Agent。
	EnvKeyPath = "AGENTFLOW_TEST_SSH_KEY_PATH"
	// EnvKnownHosts 是只读的 known_hosts 文件，可选。
	EnvKnownHosts = "AGENTFLOW_TEST_SSH_KNOWN_HOSTS"
	// EnvModel 覆盖真实模型标识。
	EnvModel = "AGENTFLOW_TEST_PI_MODEL"
	// EnvImage 覆盖测试使用的镜像引用。
	EnvImage = "AGENTFLOW_TEST_REMOTE_AGENT_IMAGE"
	// EnvRepository 覆盖测试使用的仓库地址。
	EnvRepository = "AGENTFLOW_TEST_REMOTE_GIT_REPOSITORY"
	// DefaultModel 是真实调用的默认模型，必须是免费模型。
	DefaultModel = "group/free"
	// DefaultImage 是测试使用的默认镜像引用。
	//
	// 该镜像由 tests/integration/remote/agent/Dockerfile 构建，包含固定版本的
	// Git 与 Pi CLI，构建方式见该文件头部说明。
	DefaultImage = "agentflow-test-agent:1.0.0"
	// defaultPort 是未指定端口时使用的 SSH 端口。
	defaultPort = 22
)

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
func requireEnabled(t *testing.T) target {
	t.Helper()
	if strings.TrimSpace(os.Getenv(EnvRemoteDocker)) != "1" {
		reason := fmt.Sprintf(
			"WARN [SKIP] remote agent integration tests: set %s=1 to enable.",
			EnvRemoteDocker,
		)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	host := strings.TrimSpace(os.Getenv(EnvHost))
	user := strings.TrimSpace(os.Getenv(EnvUser))
	if host == "" || user == "" {
		missing := make([]string, 0, 2)
		if host == "" {
			missing = append(missing, EnvHost)
		}
		if user == "" {
			missing = append(missing, EnvUser)
		}
		reason := fmt.Sprintf(
			"WARN [SKIP] remote agent integration tests: missing %s.",
			strings.Join(missing, ", "),
		)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	port := defaultPort
	if raw := strings.TrimSpace(os.Getenv(EnvPort)); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", EnvPort, err)
		}
		port = parsed
	}
	return target{
		host:            host,
		user:            user,
		port:            port,
		keyPath:         strings.TrimSpace(os.Getenv(EnvKeyPath)),
		knownHostsPaths: testkit.KnownHostsSources(t),
	}
}

// resolveModel 返回本次真实调用使用的模型标识。
func resolveModel(t *testing.T) string {
	t.Helper()
	model := strings.TrimSpace(os.Getenv(EnvModel))
	if model == "" {
		model = DefaultModel
	}
	t.Logf("PI_MODEL=%s", model)
	return model
}

// resolveImage 返回测试使用的镜像引用。
func resolveImage() string {
	image := strings.TrimSpace(os.Getenv(EnvImage))
	if image == "" {
		image = DefaultImage
	}
	return image
}

// resolveRepository 返回测试使用的仓库地址。
func resolveRepository(t *testing.T) string {
	t.Helper()
	repository := strings.TrimSpace(os.Getenv(EnvRepository))
	if repository == "" {
		reason := fmt.Sprintf(
			"WARN [SKIP] remote agent integration tests: set %s to a repository "+
				"reachable from the remote machine (missing).",
			EnvRepository,
		)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	return repository
}

// dial 建立经过主机密钥校验与身份认证的连接。
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

// CopyFromContainer 用 docker cp 把容器内文件复制到远程临时目录。
func (e *remoteExecutor) CopyFromContainer(
	ctx context.Context,
	dockerID, containerPath, hostPath string,
) error {
	if err := e.transfer.MkdirAll(ctx, path.Dir(hostPath)); err != nil {
		return err
	}
	command, err := sshclient.ShellCommand(
		"docker", "cp", dockerID+":"+containerPath, hostPath,
	)
	if err != nil {
		return err
	}
	run, runErr := e.runner.Run(ctx, "docker cp", command, remoteOpTimeout)
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
	image := resolveImage()
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
	dockerID, err := exec.runner.CreateContainer(ctx, docker.CreateContainerSpec{
		Container: record,
		ImageRef:  image,
		WorkDir:   containerWorkDir,
	})
	if err != nil {
		t.Fatalf("创建测试容器失败: %v", err)
	}
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
	// 只删除本次测试创建的容器；删除前再次核验所有权。
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
		defer cancel()
		observed, inspectErr := exec.runner.InspectContainer(cleanupCtx, dockerID)
		if inspectErr != nil {
			t.Logf("清理前未找到测试容器（可能已删除）: %v", inspectErr)
			return
		}
		if ownershipErr := docker.VerifyOwnership(observed, record); ownershipErr != nil {
			t.Logf("跳过清理：容器所有权不符: %v", ownershipErr)
			return
		}
		if _, removeErr := exec.runner.RemoveContainer(cleanupCtx, dockerID); removeErr != nil {
			t.Logf("删除测试容器失败: %v", removeErr)
		}
	})
	t.Logf("TEST_CONTAINER=%s (%s)", record.ID, dockerID)
	return record
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
	repository := resolveRepository(t)
	ws := testkit.New(t)
	ws.Report()
	client := dial(t, ws, tg)
	exec := newRemoteExecutor(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), remoteRunTimeout)
	t.Cleanup(cancel)

	suffix := randomSuffix(t)
	record := createContainer(t, ctx, exec, suffix)
	probeContainerTools(t, ctx, exec, record.DockerID)

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
	ws := testkit.New(t)
	ws.Report()
	client := dial(t, ws, tg)
	exec := newRemoteExecutor(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), remoteRunTimeout)
	t.Cleanup(cancel)

	suffix := randomSuffix(t)
	record := createContainer(t, ctx, exec, suffix)
	probeContainerTools(t, ctx, exec, record.DockerID)

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
