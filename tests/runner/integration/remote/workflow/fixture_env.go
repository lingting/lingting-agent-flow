// 本文件是 tests/integration/remote/workflow/{e2e_env_test.go,e2e_remote_exec_test.go,
// main_test.go,workflow_test.go} 中基础设施的迁移结果：真实现场装配、连接建立、远程执行器
// 与容器登记。
//
// 改造点：参数类型由 *testing.T 换成 *testrunner.Context；用例工作空间改用编排进程下发
// 的目录（testkit.NewAt）；包级容器终检改为用例级终检；真实依赖授权改由编排进程下发；
// 其余语义保持原样。
package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 真实远程端到端用例使用的常量。
const (
	// e2eOpTimeout 是单次远程操作超时。
	e2eOpTimeout = 10 * time.Minute
	// e2eRunTimeout 是一次完整端到端运行的超时。
	e2eRunTimeout = 60 * time.Minute
	// workflowOpTimeout 是前置检查类用例使用的单次远程操作超时。
	workflowOpTimeout = 10 * time.Minute
)

// containerRun 是本次用例创建的远程测试容器登记表。
//
// 真实端到端链路通过生产服务创建容器，因此清理责任在统一启动之前登记，并在容器创建
// 成功后补写精确身份。每条 Case 由独立 worker 进程执行，因此这里等价于用例级登记表。
var containerRun = testkit.NewContainerRun("remote/workflow")

// guardContainerResiduals 在用例结束、逐个容器删除之后核验零残留。
//
// 调用时机固定在第一份资源登记之前：清理函数按注册顺序的相反顺序执行，因此容器删除与
// 连接关闭都由后注册的清理完成，本次核验最后运行。
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

// target 是一次真实远程工作流测试的连接目标。
type target struct {
	host    string
	user    string
	port    int
	keyPath string
	// knownHostsPaths 是参与校验的只读 known_hosts 文件列表。
	knownHostsPaths []string
	model           string
}

// loadTarget 读取并校验目标配置。
//
// 未启用远程 Docker 测试时输出 WARN 并跳过；开关已启用但目标缺失时说明缺少的配置项
// 并跳过。目标完整时由后续步骤真实连接，失败必须判定为测试失败。
func loadTarget(t *testrunner.Context) target {
	t.Helper()
	requireRealTests(t, "真实远程工作流集成测试")
	enabled, _ := testkit.LoadDockerConfig(t)
	if !enabled.RemoteEnabled {
		reason := fmt.Sprintf(
			"WARN [SKIP] 远程工作流测试需要把 %s 中的 docker.remote_enabled 设为 true",
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
	piConfig, _ := testkit.LoadPiConfig(t)
	model := strings.TrimSpace(piConfig.Model)
	if model == "" {
		model = testkit.DefaultPiModel
	}
	return target{
		host:            strings.TrimSpace(sshConfig.Host),
		user:            strings.TrimSpace(sshConfig.User),
		port:            sshConfig.Port,
		keyPath:         strings.TrimSpace(sshConfig.PrivateKeyPath),
		knownHostsPaths: testkit.KnownHostsSources(t),
		model:           model,
	}
}

// uniqueSuffix 返回本次测试使用的随机后缀。
func uniqueSuffix(t *testrunner.Context) string {
	t.Helper()
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buffer)
}

// e2eEnv 是一次真实远程端到端验收的公共现场。
//
// 它只做"建立真实连接、读取真实配置"这类公共准备，不替换任何外部依赖。
type e2eEnv struct {
	ctx       context.Context
	workspace *testkit.Workspace
	logger    *logging.Logger
	client    sshclient.Client
	runner    *docker.Runner
	exec      *remoteExecutor
	image     string
	model     string
	repo      testkit.GitConfig
	suffix    string
}

// newE2EEnv 装配真实远程现场；配置不完整时输出 WARN 并跳过。
func newE2EEnv(t *testrunner.Context) *e2eEnv {
	t.Helper()
	requireRealTests(t, "真实远程端到端验收")
	if report := e2eConfigReport(t); len(report) > 0 {
		reason := "WARN [SKIP] 真实远程端到端验收缺少配置: " + strings.Join(report, "；")
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	workspace := testkit.NewAt(t, t.WorkspaceDir())
	workspace.Report()
	ctx, cancel := context.WithTimeout(context.Background(), e2eRunTimeout)
	t.Cleanup(cancel)
	logger, err := testkit.Logger(logging.LevelDebug)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	env := &e2eEnv{
		ctx:       ctx,
		workspace: workspace,
		logger:    logger,
		model:     resolveModel(t),
		repo:      resolveGitConfig(t),
		image:     resolveAgentImage(t),
		suffix:    e2eRandomSuffix(t),
	}
	env.dial(t)
	return env
}

// e2eConfigReport 返回缺失的配置项说明。
func e2eConfigReport(t *testrunner.Context) []string {
	t.Helper()
	missing := make([]string, 0, 8)
	dockerConfig, _ := testkit.LoadDockerConfig(t)
	if !dockerConfig.RemoteEnabled {
		missing = append(missing, "docker.remote_enabled 未设为 true")
	}
	sshConfig, sshOK := testkit.LoadSSHConfig(t)
	if !sshOK || !sshConfig.Enabled {
		missing = append(missing, "ssh.enable 未设为 true")
	} else if strings.TrimSpace(sshConfig.Host) == "" || strings.TrimSpace(sshConfig.User) == "" {
		missing = append(missing, "ssh.host / ssh.user 缺失")
	} else if strings.TrimSpace(sshConfig.PrivateKeyPath) == "" {
		missing = append(missing, "ssh.private_key_path 缺失（本版本只支持私钥认证）")
	}
	gitConfig, _ := testkit.LoadGitConfig(t)
	if strings.TrimSpace(gitConfig.ResolvedRepository()) == "" {
		missing = append(missing, "git.repository 缺失")
	}
	if config, _ := testkit.LoadPiConfig(t); !config.Enabled {
		missing = append(missing, "pi.enable 未设为 true")
	}
	return missing
}

// resolveModel 返回真实调用使用的模型标识。
func resolveModel(t *testrunner.Context) string {
	t.Helper()
	config, _ := testkit.LoadPiConfig(t)
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = testkit.DefaultPiModel
	}
	t.Logf("PI_MODEL=%s", model)
	return model
}

// resolveGitConfig 返回真实 Git 仓库配置。
func resolveGitConfig(t *testrunner.Context) testkit.GitConfig {
	t.Helper()
	config, _ := testkit.LoadGitConfig(t)
	if missing := config.CredentialMissing(); len(missing) > 0 {
		t.Fatalf("Git 凭证不完整，缺少 %s", strings.Join(missing, ", "))
	}
	t.Logf("GIT_REPOSITORY=%s GIT_CREDENTIAL=%s",
		config.ResolvedRepository(), config.CredentialKind())
	return config
}

// resolveAgentImage 返回真实远程测试使用的任务镜像。
func resolveAgentImage(t *testrunner.Context) string {
	t.Helper()
	config, _ := testkit.LoadDockerConfig(t)
	image := strings.TrimSpace(config.AgentImage)
	if image == "" {
		image = "agentflow-test-agent:1.0.0"
	}
	return image
}

// e2eRandomSuffix 生成带随机后缀的标识，避免与其他测试资源冲突。
func e2eRandomSuffix(t *testrunner.Context) string {
	t.Helper()
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buffer)
}

// dial 建立真实 SSH 连接。
func (e *e2eEnv) dial(t *testrunner.Context) {
	t.Helper()
	sshConfig, _ := testkit.LoadSSHConfig(t)
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: testkit.KnownHostsSources(t),
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
	result, err := dialer.Dial(e.ctx, sshclient.Endpoint{
		Host:     sshConfig.Host,
		Port:     sshConfig.Port,
		Username: sshConfig.User,
	}, sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: sshConfig.PrivateKeyPath,
	})
	if err != nil {
		if sshclient.ErrorCodeOf(err) == sshclient.CodeHostKeyUnknown {
			t.Fatalf(
				"主机密钥未知，请用 OpenSSH 追加已核验的密钥后再运行: %s %s",
				result.HostKey.Algorithm, result.HostKey.Fingerprint,
			)
		}
		t.Fatalf("连接真实远程机器失败: %v", err)
	}
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		t.Fatalf("主机密钥应已被信任，实际: %s", result.HostKeyVerdict)
	}
	e.client = result.Client
	e.workspace.Cleanup(result.Client.Close)
	e.exec = newRemoteExecutor(t, result.Client)
	e.runner = e.exec.runner
	guardContainerResiduals(t, e.runner)
}

// remoteExecutor 把 agent.ContainerExecutor 与 source.ContainerExecutor 适配到真实
// 远程 Docker 与 SFTP 通道。
//
// 它不替换任何外部依赖：命令经 SSH 在容器内真实执行，文件经真实 SFTP 传输。
type remoteExecutor struct {
	client   sshclient.Client
	transfer sshclient.FileTransfer
	runner   *docker.Runner
}

// newRemoteExecutor 构造远程执行器。
func newRemoteExecutor(t *testrunner.Context, client sshclient.Client) *remoteExecutor {
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
		if closeErr := transfer.Close(); closeErr != nil {
			t.Logf("WARN 关闭 SFTP 通道失败: %v", closeErr)
		}
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
			ctx, request.DockerID, request.Command,
			strings.NewReader(string(request.Stdin)), request.Timeout,
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
		run, err = e.runner.ExecCommand(ctx, dockerID, command, e2eOpTimeout)
	} else {
		run, err = e.runner.ExecCommandWithInput(
			ctx, dockerID, command, strings.NewReader(stdin), e2eOpTimeout,
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
	run, runErr := e.runner.CopyFromContainer(ctx, dockerID, containerPath, hostPath, e2eOpTimeout)
	if runErr != nil {
		return runErr
	}
	if !run.Succeeded() {
		return fmt.Errorf("docker cp 失败（退出码 %d）: %s", run.ExitCode, run.Stderr)
	}
	return nil
}

// DownloadToLocal 把远程宿主机文件下载到本机。
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
