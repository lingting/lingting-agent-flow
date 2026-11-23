// 本文件是 tests/integration/remote/codesaver/main_test.go 与 codesaver_test.go 的迁移结果：
// 远程目标装载、容器生命周期、测试替身与仓库准备。
//
// 改造点：参数类型由 *testing.T 换成 *testrunner.Context；用例工作空间改用编排进程下发
// 的目录（testkit.NewAt）；包级最终核验改为用例级终检（Case 由独立 worker 执行，一个
// 进程只跑一条用例）；真实依赖授权改由编排进程下发；其余语义保持原样。
package codesaver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/testkit"
)

const (
	// testBaseBranch 是测试仓库的基础分支名。
	testBaseBranch = "main"
	// testRemoteName 是测试仓库的远程名称。
	testRemoteName = "origin"
	// testWorkDir 是容器内的项目工作目录。
	//
	// 代码保存要求工作目录位于 /workspace 之下，因此测试容器直接使用该目录。
	testWorkDir = domain.DefaultContainerWorkDir
	// testRepositoryURL 是容器内工作仓库登记的远程地址。
	//
	// 代码保存只接受 ssh://、https:// 或 host:path 形式且不含内嵌凭据的地址，
	// 因此这里登记一个真实的 https 形式地址，再由容器内的 insteadOf 映射到本次
	// 测试创建的 bare 仓库；仓库访问策略本身保持不变。
	testRepositoryURL = "https://git.example.com/agentflow/remote-test.git"
	// testBareDir 是容器内 bare 仓库所在目录。
	testBareDir = "/tmp"
	// testBareName 是容器内 bare 仓库目录名。
	testBareName = "agentflow-remote.git"
)

// remoteOpTimeout 是单次远程操作的超时上限。
const remoteOpTimeout = 10 * time.Minute

// containerRun 是本次用例创建的远程测试容器登记表。
//
// 每条 Case 由独立 worker 进程执行，因此这里等价于用例级登记表；登记项在用例结束时
// 逐条删除并核验，随后由 guardContainerResiduals 做一次清单级零残留核验。
var containerRun = testkit.NewContainerRun("remote/codesaver")

// target 是本次测试使用的远程目标与凭据。
type target struct {
	host    string
	user    string
	port    int
	keyPath string
	// knownHostsPaths 是参与校验的只读 known_hosts 文件列表。
	knownHostsPaths []string
	image           string
}

// loadTarget 读取并校验远程测试目标。
//
// 开关未启用时跳过；开关已启用但目标不完整时说明缺少的配置项并跳过；目标完整时
// 返回配置，由后续连接与断言决定测试是否失败。
func loadTarget(t *testrunner.Context) target {
	t.Helper()
	requireRealTests(t, "真实远程代码保存集成测试")
	enabled, _ := testkit.LoadDockerConfig(t)
	if !enabled.RemoteEnabled {
		reason := fmt.Sprintf(
			"WARN [SKIP] codesaver remote tests: 请把 %s 中的 docker.remote_enabled 设为 true。",
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
	sshMissing := make([]string, 0, 2)
	if strings.TrimSpace(sshConfig.Host) == "" {
		sshMissing = append(sshMissing, "ssh.host")
	}
	if strings.TrimSpace(sshConfig.User) == "" {
		sshMissing = append(sshMissing, "ssh.user")
	}
	if len(sshMissing) > 0 {
		reason := testkit.SkipReason(t, testkit.SSHConfigFile, sshMissing)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	image := strings.TrimSpace(enabled.GitImage)
	if image == "" {
		reason := testkit.SkipReason(t, testkit.DockerConfigFile, []string{"docker.git_image"})
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
		image:           image,
	}
}

// dial 建立经过主机密钥校验的 SSH 连接。
//
// 主机密钥校验始终开启：信任记录只来自 ssh.yml 配置的只读 known_hosts，以及测试
// 工作空间内的临时信任文件，绝不自动接受未知主机密钥。
func (t target) dial(runner *testrunner.Context, workspace *testkit.Workspace) sshclient.Client {
	runner.Helper()
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: t.knownHostsPaths,
	})
	if err != nil {
		runner.Fatalf("构造主机密钥库失败: %v", err)
	}
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{
		HostKeys:       store,
		ConnectTimeout: remoteOpTimeout,
	})
	if err != nil {
		runner.Fatalf("构造连接器失败: %v", err)
	}
	// 本版本只支持私钥认证：真实远程测试必须提供私钥路径。
	if t.keyPath == "" {
		runner.Fatalf("真实远程测试必须设置私钥路径（本版本只支持私钥认证）")
	}
	auth := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: t.keyPath,
	}
	result, err := dialer.Dial(
		context.Background(),
		sshclient.Endpoint{Host: t.host, Port: t.port, Username: t.user},
		auth,
	)
	if err != nil {
		runner.Fatalf(
			"SSH 连接失败（主机密钥 %s %s）: %v",
			result.HostKey.Algorithm,
			result.HostKey.Fingerprint,
			err,
		)
	}
	return result.Client
}

// randomSuffix 生成随机后缀，用于隔离本次测试创建的资源。
func randomSuffix(t *testrunner.Context) string {
	t.Helper()
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buffer)
}

// newWorkspace 以编排进程下发的用例工作空间作为本地产物目录。
func newWorkspace(t *testrunner.Context) *testkit.Workspace {
	t.Helper()
	return testkit.NewAt(t, t.WorkspaceDir())
}

// guardContainerResiduals 在用例结束、逐个容器删除之后核验零残留。
//
// 调用时机固定在第一个容器登记之前：清理函数按注册顺序的相反顺序执行，因此
// 容器删除与连接关闭都由后注册的清理完成，本次核验最后运行。
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
func (r *memoryRecorder) single(t *testrunner.Context) domain.CodeSaveRecord {
	t.Helper()
	if len(r.records) != 1 {
		t.Fatalf("期望 1 条保存记录，实际 %d 条", len(r.records))
	}
	return r.records[0]
}

// createTestContainer 在远程机器上创建并启动一个受管理的测试容器。
//
// 容器名称带随机后缀并带 AgentFlow 管理标签，因此删除前可以核验所有权；清理责任
// 在创建之前登记，创建或启动失败都不会留下无法追踪的远程容器。
func createTestContainer(
	t *testrunner.Context,
	ctx context.Context,
	runner *docker.Runner,
	image, suffix, workDir string,
) domain.Container {
	t.Helper()
	imageID, err := runner.InspectImageID(ctx, image)
	if err != nil {
		t.Fatalf("查询测试镜像 %s 失败: %v", image, err)
	}
	record, err := domain.NewContainer(
		domain.NewID(),
		domain.ID("remote-codesaver-test-host"),
		"agentflow-codesaver-"+suffix,
		domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
		"v0001",
		strings.Repeat("a", 64),
		imageID,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	tracked := containerRun.Track(t, runner, testkit.TrackedContainer{
		Name:   record.Name,
		Record: &record,
	})
	dockerID, err := runner.CreateContainer(ctx, docker.CreateContainerSpec{
		Container: record,
		ImageRef:  image,
		WorkDir:   workDir,
	})
	if err != nil {
		t.Fatalf("创建测试容器失败: %v", err)
	}
	tracked.DockerID = dockerID
	if markErr := record.MarkCreated(dockerID, domain.DockerStateCreated, time.Now().UTC()); markErr != nil {
		t.Fatalf("记录容器创建结果失败: %v", markErr)
	}
	if startErr := runner.StartContainer(ctx, dockerID); startErr != nil {
		t.Fatalf("启动测试容器失败: %v", startErr)
	}
	if markErr := record.MarkRunning(time.Now().UTC()); markErr != nil {
		t.Fatalf("记录容器运行状态失败: %v", markErr)
	}
	t.Logf("测试容器: %s（%s）", record.Name, dockerID)
	return record
}

// prepareTestRepository 在容器内创建 bare 仓库、基础分支与任务分支。
//
// 返回容器内 bare 仓库路径与工作仓库路径。
func prepareTestRepository(
	t *testrunner.Context,
	ctx context.Context,
	runner *docker.Runner,
	dockerID, workDir, taskBranch string,
) string {
	t.Helper()
	barePath := path.Join(testBareDir, testBareName)
	script := strings.Join([]string{
		"set -e",
		// 容器的初始工作目录就是 workDir，先切到根目录，否则删除后 cwd 失效。
		"cd /",
		"git config --global user.name 'AgentFlow Tester'",
		"git config --global user.email 'agentflow@example.com'",
		"git config --global init.defaultBranch " + testBaseBranch,
		"rm -rf " + barePath + " " + workDir,
		"git init --bare --initial-branch=" + testBaseBranch + " " + barePath,
		"git init --initial-branch=" + testBaseBranch + " " + workDir,
		// 远程地址保持 https 形式以符合仓库访问策略，实际访问由 insteadOf 映射到
		// 本次测试创建的 bare 仓库，不会产生任何网络请求。
		"git config --global url." + barePath + ".insteadOf " + testRepositoryURL,
		"cd " + workDir,
		"git remote add " + testRemoteName + " " + testRepositoryURL,
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
	t *testrunner.Context,
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
	t *testrunner.Context,
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
