// Package codesaver_remote 提供需要真实远程容器与真实 Git 的代码保存集成测试。
//
// 运行方式：
//
//	AGENTFLOW_TEST_REMOTE_DOCKER=1 \
//	AGENTFLOW_TEST_SSH_HOST=10.0.0.10 \
//	AGENTFLOW_TEST_SSH_USER=deploy \
//	AGENTFLOW_TEST_SSH_KEY_PATH=$HOME/.ssh/id_ed25519 \
//	go test -count=1 -v ./tests/integration/remote/codesaver/...
//
// 安全约束：
//   - 只有显式设置 AGENTFLOW_TEST_REMOTE_DOCKER=1 时才会修改真实远程资源；
//   - SSH 目标缺失时输出 WARN 并跳过；
//   - 目标配置完整但连接、认证、主机密钥校验失败或断言不成立时必须让测试失败；
//   - 只使用带随机后缀的容器与远程目录，只操作本次测试创建的资源；
//   - 不执行全局 prune，不删除共享镜像、共享缓存卷或其他测试的资源；
//   - 不在用户的真实主分支上执行合并：合并验证只在本次测试创建的临时 bare 仓库内进行。
package codesaver_remote

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

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 真实远程测试使用的环境变量。
const (
	// EnvRemoteDocker 是显式启用远程容器测试的开关。
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
	// EnvImage 是测试使用的任务镜像，必填。
	EnvImage = "AGENTFLOW_TEST_REMOTE_GIT_IMAGE"
	// defaultPort 是未指定端口时使用的 SSH 端口。
	defaultPort = 22
	// testBaseBranch 是测试仓库的基础分支名。
	testBaseBranch = "main"
	// testRemoteName 是测试仓库的远程名称。
	testRemoteName = "origin"
	// testWorkDir 是容器内的项目工作目录。
	//
	// 代码保存要求工作目录位于 /workspace 之下，因此测试容器直接使用该目录。
	testWorkDir = domain.DefaultContainerWorkDir
)

// remoteOpTimeout 是单次远程操作的超时上限。
const remoteOpTimeout = 10 * time.Minute

// target 是本次测试使用的远程目标与凭据。
type target struct {
	host       string
	user       string
	port       int
	keyPath    string
	knownHosts string
	image      string
}

// loadTarget 读取并校验远程测试目标。
//
// 开关未设置时跳过；开关已设置但目标不完整时说明缺少的变量并跳过；目标完整时
// 返回配置，由后续连接与断言决定测试是否失败。
func loadTarget(t *testing.T) target {
	t.Helper()
	if strings.TrimSpace(os.Getenv(EnvRemoteDocker)) != "1" {
		reason := "WARN [SKIP] codesaver remote tests: " + EnvRemoteDocker + " is not set to 1."
		t.Log(reason)
		t.Skip(reason)
	}
	missing := make([]string, 0, 4)
	read := func(key string) string {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			missing = append(missing, key)
		}
		return value
	}
	loaded := target{
		host:       read(EnvHost),
		user:       read(EnvUser),
		keyPath:    strings.TrimSpace(os.Getenv(EnvKeyPath)),
		knownHosts: strings.TrimSpace(os.Getenv(EnvKnownHosts)),
		image:      read(EnvImage),
	}
	if len(missing) > 0 {
		reason := "WARN [SKIP] codesaver remote tests: missing " + strings.Join(missing, ", ") + "."
		t.Log(reason)
		t.Skip(reason)
	}
	loaded.port = defaultPort
	if raw := strings.TrimSpace(os.Getenv(EnvPort)); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 65535 {
			t.Fatalf("%s 不是合法端口: %q", EnvPort, raw)
		}
		loaded.port = parsed
	}
	return loaded
}

// dial 建立经过主机密钥校验的 SSH 连接。
//
// 主机密钥校验始终开启：信任记录只来自环境变量指定的只读 known_hosts，以及测试
// 工作空间内的临时信任文件，绝不自动接受未知主机密钥。
func (t target) dial(t2 *testing.T, workspace *testkit.Workspace) sshclient.Client {
	t2.Helper()
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{t.knownHosts},
		StorePath:    filepath.Join(workspace.Root(), "known_hosts"),
	})
	if err != nil {
		t2.Fatalf("构造主机密钥库失败: %v", err)
	}
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{
		HostKeys:       store,
		ConnectTimeout: remoteOpTimeout,
	})
	if err != nil {
		t2.Fatalf("构造连接器失败: %v", err)
	}
	auth := sshclient.AuthConfig{Method: sshclient.AuthMethodAgent}
	if t.keyPath != "" {
		auth = sshclient.AuthConfig{
			Method:         sshclient.AuthMethodPrivateKey,
			PrivateKeyPath: t.keyPath,
		}
	}
	result, err := dialer.Dial(
		context.Background(),
		sshclient.Endpoint{Host: t.host, Port: t.port, Username: t.user},
		auth,
		nil,
	)
	if err != nil {
		t2.Fatalf(
			"SSH 连接失败（主机密钥 %s %s）: %v",
			result.HostKey.Algorithm,
			result.HostKey.Fingerprint,
			err,
		)
	}
	return result.Client
}

// randomSuffix 生成随机后缀，用于隔离本次测试创建的资源。
func randomSuffix(t *testing.T) string {
	t.Helper()
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buffer)
}

// containerExecutor 在真实容器内执行命令。
type containerExecutor struct {
	runner   *docker.Runner
	dockerID string
}

// ExecInContainer 在容器内执行一条命令。
func (e containerExecutor) ExecInContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	if timeout <= 0 {
		timeout = codesaver.DefaultProbeTimeout
	}
	return e.runner.ExecCommand(ctx, e.dockerID, command, timeout)
}

// containerGuard 返回本次测试创建的容器记录。
type containerGuard struct {
	container domain.Container
}

// EnsureExecutable 返回预设容器记录。
func (g containerGuard) EnsureExecutable(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	return g.container, nil
}

// memoryRecorder 在内存中保存代码保存记录。
type memoryRecorder struct {
	records []domain.CodeSaveRecord
}

// Create 保存新记录。
func (r *memoryRecorder) Create(ctx context.Context, record domain.CodeSaveRecord) error {
	r.records = append(r.records, record)
	return nil
}

// Update 更新记录。
func (r *memoryRecorder) Update(ctx context.Context, record domain.CodeSaveRecord) error {
	for index := range r.records {
		if r.records[index].ID == record.ID {
			r.records[index] = record
			return nil
		}
	}
	return fmt.Errorf("记录不存在: %s", record.ID)
}

// Get 读取记录。
func (r *memoryRecorder) Get(
	ctx context.Context,
	id domain.ID,
) (*domain.CodeSaveRecord, error) {
	for index := range r.records {
		if r.records[index].ID == id {
			return &r.records[index], nil
		}
	}
	return nil, fmt.Errorf("记录不存在: %s", id)
}

// ListByTask 按任务列出记录。
func (r *memoryRecorder) ListByTask(
	ctx context.Context,
	taskID string,
) ([]domain.CodeSaveRecord, error) {
	matched := make([]domain.CodeSaveRecord, 0, len(r.records))
	for _, record := range r.records {
		if record.TaskID == taskID {
			matched = append(matched, record)
		}
	}
	return matched, nil
}

// single 返回唯一的记录。
func (r *memoryRecorder) single(t *testing.T) domain.CodeSaveRecord {
	t.Helper()
	if len(r.records) != 1 {
		t.Fatalf("期望 1 条保存记录，实际 %d 条", len(r.records))
	}
	return r.records[0]
}

// TestSaveCodeInsideRemoteContainer 在真实远程容器内完成提交、推送与合并。
func TestSaveCodeInsideRemoteContainer(t *testing.T) {
	loaded := loadTarget(t)
	workspace := testkit.New(t)
	t.Logf("测试工作空间: %s", workspace.Root())
	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	client := loaded.dial(t, workspace)
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("关闭 SSH 连接失败: %v", closeErr)
		}
	}()
	runner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	suffix := randomSuffix(t)
	containerID, dockerID := createTestContainer(
		t,
		ctx,
		runner,
		loaded.image,
		suffix,
		testWorkDir,
	)
	defer func() {
		// 只删除本次测试创建并已核验所有权的容器。
		if _, err := runner.RemoveContainer(context.Background(), dockerID); err != nil {
			t.Logf("删除测试容器失败（需人工检查）: %v", err)
		}
	}()
	taskID := "task-" + suffix
	taskBranch, branchErr := domain.TaskBranchName(taskID)
	if branchErr != nil {
		t.Fatalf("生成任务分支失败: %v", branchErr)
	}
	barePath := prepareTestRepository(t, ctx, runner, dockerID, testWorkDir, taskBranch)
	t.Logf("测试仓库: %s（bare: %s）", testWorkDir, barePath)
	container := domain.Container{
		ID:          domain.NewID(),
		HostID:      domain.NewID(),
		DockerID:    dockerID,
		Name:        "agentflow-test-" + suffix,
		State:       domain.ContainerStateRunning,
		DockerState: domain.DockerStateRunning,
	}
	if containerID != "" && containerID != dockerID {
		t.Logf("容器登记标识与实际 ID 不同: %s / %s", containerID, dockerID)
	}
	recorder := &memoryRecorder{}
	saver, err := codesaver.New(codesaver.Options{
		Executor: containerExecutor{runner: runner, dockerID: dockerID},
		Guard:    containerGuard{container: container},
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("构造代码保存组件失败: %v", err)
	}
	// 在容器内修改代码，模拟 Agent 已经完成开发。
	writeContainerFile(t, ctx, runner, dockerID, testWorkDir, "feature.go")
	result, err := saver.Run(ctx, codesaver.Request{
		MachineID:      container.HostID.String(),
		ContainerID:    container.ID.String(),
		DockerID:       dockerID,
		TaskID:         taskID,
		WorkDir:        testWorkDir,
		RemoteName:     testRemoteName,
		BaseBranch:     testBaseBranch,
		MergeAfterPush: true,
		CommitMessage:  "AgentFlow: 远程容器代码保存测试",
		Author: domain.GitAuthor{
			Name:  "AgentFlow Tester",
			Email: "agentflow@example.com",
		},
	})
	if err != nil {
		t.Fatalf("远程容器内代码保存失败: %v", err)
	}
	record := recorder.single(t)
	if record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交状态应为 succeeded，实际 %s", record.CommitStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("推送状态应为 succeeded，实际 %s", record.PushStatus)
	}
	if record.MergeStatus != domain.MergeStatusSucceeded {
		t.Fatalf("合并状态应为 succeeded，实际 %s", record.MergeStatus)
	}
	if !result.Succeeded() {
		t.Fatal("完整保存流程应判定成功")
	}
	remoteTask := remoteRef(t, ctx, runner, dockerID, barePath, "refs/heads/"+record.TaskBranch)
	if remoteTask != record.CommitSHA {
		t.Fatalf("远程任务分支应为 %s，实际 %s", record.CommitSHA, remoteTask)
	}
	remoteBase := remoteRef(t, ctx, runner, dockerID, barePath, "refs/heads/"+testBaseBranch)
	if remoteBase != record.MergeSHA {
		t.Fatalf("远程基础分支应为 %s，实际 %s", record.MergeSHA, remoteBase)
	}
}

// createTestContainer 在远程机器上创建并启动一个测试容器。
//
// 返回容器登记标识与实际 Docker 容器 ID。容器名称带随机后缀，只操作本次测试
// 创建的资源。
func createTestContainer(
	t *testing.T,
	ctx context.Context,
	runner *docker.Runner,
	image, suffix, workDir string,
) (string, string) {
	t.Helper()
	name := "agentflow-codesaver-" + suffix
	args := []string{
		"run", "--detach", "--name", name,
		"--workdir", workDir,
		image,
		"sleep", "infinity",
	}
	command, err := sshclient.ShellCommand("docker", args...)
	if err != nil {
		t.Fatalf("构造容器创建命令失败: %v", err)
	}
	result, runErr := runner.Run(ctx, "docker run", command, remoteOpTimeout)
	if runErr != nil {
		t.Fatalf("创建测试容器失败: %v", runErr)
	}
	if !result.Succeeded() {
		t.Fatalf("创建测试容器失败: %s", result.Stderr)
	}
	dockerID := strings.TrimSpace(result.Stdout)
	if dockerID == "" {
		t.Fatalf("创建测试容器没有返回容器 ID")
	}
	t.Logf("测试容器: %s（%s）", name, dockerID)
	return name, dockerID
}

// prepareTestRepository 在容器内创建 bare 仓库、基础分支与任务分支。
//
// 返回容器内 bare 仓库路径与工作仓库路径。
func prepareTestRepository(
	t *testing.T,
	ctx context.Context,
	runner *docker.Runner,
	dockerID, workDir, taskBranch string,
) string {
	t.Helper()
	barePath := path.Join("/tmp", "agentflow-remote.git")
	script := strings.Join([]string{
		"set -e",
		"git config --global user.name 'AgentFlow Tester'",
		"git config --global user.email 'agentflow@example.com'",
		"git config --global init.defaultBranch " + testBaseBranch,
		"rm -rf " + barePath + " " + workDir,
		"git init --bare --initial-branch=" + testBaseBranch + " " + barePath,
		"git init --initial-branch=" + testBaseBranch + " " + workDir,
		"cd " + workDir,
		"git remote add " + testRemoteName + " " + barePath,
		"printf '初始内容\\n' > README.md",
		"git add --all",
		"git commit -m '初始提交'",
		"printf 'package app\\n' > app.go",
		"git add --all",
		"git commit -m '第二个提交'",
		"git push " + testRemoteName + " " + testBaseBranch,
		"git remote set-head " + testRemoteName + " " + testBaseBranch,
		"git checkout -b " + taskBranch,
	}, "\n")
	command, err := sshclient.ShellCommand("docker", "exec", dockerID, "sh", "-c", script)
	if err != nil {
		t.Fatalf("构造测试仓库初始化命令失败: %v", err)
	}
	result, runErr := runner.Run(ctx, "docker exec", command, remoteOpTimeout)
	if runErr != nil {
		t.Fatalf("初始化测试仓库失败: %v", runErr)
	}
	if !result.Succeeded() {
		t.Fatalf("初始化测试仓库失败: %s%s", result.Stdout, result.Stderr)
	}
	return barePath
}

// writeContainerFile 在容器内写入一个文件，模拟 Agent 产生的代码变更。
func writeContainerFile(
	t *testing.T,
	ctx context.Context,
	runner *docker.Runner,
	dockerID, workDir, name string,
) {
	t.Helper()
	script := "cd " + workDir + " && printf 'package feature\\n' > " + name
	command, err := sshclient.ShellCommand("docker", "exec", dockerID, "sh", "-c", script)
	if err != nil {
		t.Fatalf("构造写文件命令失败: %v", err)
	}
	result, runErr := runner.Run(ctx, "docker exec", command, remoteOpTimeout)
	if runErr != nil {
		t.Fatalf("在容器内写入文件失败: %v", runErr)
	}
	if !result.Succeeded() {
		t.Fatalf("在容器内写入文件失败: %s", result.Stderr)
	}
}

// remoteRef 读取容器内 bare 仓库中指定引用的 commit。
func remoteRef(
	t *testing.T,
	ctx context.Context,
	runner *docker.Runner,
	dockerID, barePath, ref string,
) string {
	t.Helper()
	script := "git -C " + barePath + " rev-parse --verify --quiet " + ref + "^{commit}"
	command, err := sshclient.ShellCommand("docker", "exec", dockerID, "sh", "-c", script)
	if err != nil {
		t.Fatalf("构造远程引用查询命令失败: %v", err)
	}
	result, runErr := runner.Run(ctx, "docker exec", command, remoteOpTimeout)
	if runErr != nil {
		t.Fatalf("查询远程引用 %s 失败: %v", ref, runErr)
	}
	if !result.Succeeded() {
		t.Fatalf("查询远程引用 %s 失败: %s", ref, result.Stderr)
	}
	return strings.TrimSpace(result.Stdout)
}
