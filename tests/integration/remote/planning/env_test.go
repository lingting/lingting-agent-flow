package planning_remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 真实规划运行时验收使用的固定取值。
const (
	// planningImage 是规划运行时真实验收使用的专用测试镜像。
	//
	// 它由本目录的 Dockerfile 定义：在通用测试镜像之上只设置 Pi 的配置目录，
	// 使只读 rootfs 的规划容器仍有一个可写且不落在项目代码目录中的 Pi 配置目录。
	planningImage = "agentflow-test-planning:1.0.0"
	// baseTestImage 是 planningImage 的构建基础镜像。
	baseTestImage = "agentflow-test-agent:1.0.0"
	// planningPiConfigDir 是规划容器内 Pi 的配置目录（规划工作区唯一可写目录之下）。
	//
	// 它必须与本目录 Dockerfile 中的 PI_CODING_AGENT_DIR 一致：规划容器 rootfs 只读，
	// 只有 /tmp 是可写 tmpfs，因此模型配置只能落在该目录。
	planningPiConfigDir = "/tmp/pi-agent"
	// planningWorkDir 是规划容器内项目代码目录，与产品默认值一致。
	planningWorkDir = domain.DefaultContainerWorkDir
	// planningBaseBranch 是测试项目使用的基础分支。
	planningBaseBranch = "main"

	// planningOpTimeout 是单次远程操作超时。
	planningOpTimeout = 3 * time.Minute
	// planningCommitTimeout 是快照准备（容器内 Clone）的超时。
	planningCommitTimeout = 10 * time.Minute
	// planningRunTimeout 是一次完整真实验收的超时。
	planningRunTimeout = 90 * time.Minute
)

// env 是一次真实规划运行时验收的公共现场。
//
// 它只负责"读取真实配置、建立真实连接、准备真实镜像"这类公共工作，不替换任何外部
// 依赖：生产服务在 fixture 中用真实 SSH、真实 Docker 与真实 Pi 装配。
type env struct {
	t         *testing.T
	ctx       context.Context
	workspace *testkit.Workspace
	logger    *logging.Logger
	// client 是独立于生产服务的控制连接：用于事后核验远程现场（容器状态、进程、
	// 卷）以及查询生产服务没有暴露的原始 docker inspect 字段。
	client      sshclient.Client
	runner      *docker.Runner
	sshConfig   testkit.SSHConfig
	dockerCfg   testkit.DockerConfig
	gitConfig   testkit.GitConfig
	model       string
	image       string
	suffix      string
	remoteRoot  string
	sessionRoot string
}

// newEnv 装配真实远程现场；配置不完整时输出 WARN 并跳过。
func newEnv(t *testing.T) *env {
	t.Helper()
	testkit.RequireRealTests(t, "真实规划运行时验收")
	sshConfig, dockerConfig, gitConfig := requireRealConfig(t)
	if missing := dockerConfig.Pi.Missing(); len(missing) > 0 {
		reason := testkit.SkipReason(t, testkit.DockerConfigFile, missing)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	if !dockerConfig.Pi.Enabled() {
		reason := testkit.SkipReason(t, testkit.DockerConfigFile, []string{
			"docker.pi.base_url", "docker.pi.api_key", "docker.pi.model",
		})
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	workspace := testkit.New(t)
	workspace.Report()
	ctx, cancel := context.WithTimeout(context.Background(), planningRunTimeout)
	t.Cleanup(cancel)
	logger, err := testkit.Logger(logging.LevelInfo)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	realEnv := &env{
		t:           t,
		ctx:         ctx,
		workspace:   workspace,
		logger:      logger,
		sshConfig:   sshConfig,
		dockerCfg:   dockerConfig,
		gitConfig:   gitConfig,
		model:       strings.TrimSpace(dockerConfig.Pi.Model),
		image:       planningImage,
		suffix:      randomSuffix(t),
		sessionRoot: workspace.SessionRoot(),
	}
	realEnv.dial(t)
	realEnv.remoteRoot = realEnv.prepareRemoteRoot(t)
	realEnv.ensureImage(t)
	return realEnv
}

// requireRealConfig 校验真实测试所需的 SSH / Docker / Git 配置。
//
// 配置缺失时输出明确的 WARN 并跳过，配置完整但连接失败时必须让测试失败：
// "没有配置"与"配置写错"必须可以区分。
func requireRealConfig(t *testing.T) (testkit.SSHConfig, testkit.DockerConfig, testkit.GitConfig) {
	t.Helper()
	dockerConfig, dockerOK := testkit.LoadDockerConfig(t)
	if !dockerOK || !dockerConfig.RemoteEnabled {
		reason := testkit.EnableReason(t, testkit.DockerConfigFile, "docker.remote_enabled")
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	sshConfig, sshOK := testkit.LoadSSHConfig(t)
	if !sshOK || !sshConfig.Enabled {
		reason := testkit.EnableReason(t, testkit.SSHConfigFile, "ssh.enable")
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	missing := make([]string, 0, 3)
	if strings.TrimSpace(sshConfig.Host) == "" {
		missing = append(missing, "ssh.host")
	}
	if strings.TrimSpace(sshConfig.User) == "" {
		missing = append(missing, "ssh.user")
	}
	if strings.TrimSpace(sshConfig.PrivateKeyPath) == "" {
		missing = append(missing, "ssh.private_key_path")
	}
	if len(missing) > 0 {
		reason := testkit.SkipReason(t, testkit.SSHConfigFile, missing)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	gitConfig, _ := testkit.LoadGitConfig(t)
	if strings.TrimSpace(gitConfig.ResolvedRepository()) == "" {
		reason := testkit.SkipReason(t, testkit.GitConfigFile, []string{"git.repository"})
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	if missing := gitConfig.CredentialMissing(); len(missing) > 0 {
		t.Fatalf("Git 凭证不完整，缺少 %s", strings.Join(missing, ", "))
	}
	return sshConfig, dockerConfig, gitConfig
}

// dial 建立独立的控制连接。
//
// 该连接只用于只读核验与测试侧的原始 docker inspect / docker pause 等现场操作，
// 生产服务仍然各自建立并管理自己的连接。
func (e *env) dial(t *testing.T) {
	t.Helper()
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
		Host:     e.sshConfig.Host,
		Port:     e.sshConfig.Port,
		Username: e.sshConfig.User,
	}, sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: e.sshConfig.PrivateKeyPath,
	})
	if err != nil {
		if sshclient.ErrorCodeOf(err) == sshclient.CodeHostKeyUnknown {
			t.Fatalf(
				"主机密钥未知，请用 OpenSSH 追加已核验的密钥后再运行: %s %s\n%s",
				result.HostKey.Algorithm,
				result.HostKey.Fingerprint,
				testkit.HostKeyHint(t, ""),
			)
		}
		t.Fatalf("连接真实远程机器失败: %v", err)
	}
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		t.Fatalf("主机密钥应已被信任，实际: %s", result.HostKeyVerdict)
	}
	e.client = result.Client
	e.workspace.Cleanup(result.Client.Close)
	runner, err := docker.NewRunner(result.Client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	e.runner = runner
}

// prepareRemoteRoot 创建本次用例专属的远程测试目录。
func (e *env) prepareRemoteRoot(t *testing.T) string {
	t.Helper()
	root, err := testkit.ResolveRemoteTestRoot(
		e.ctx,
		e.client,
		planningOpTimeout,
		testkit.RemoteTestPackagePlanning,
		e.suffix,
	)
	if err != nil {
		t.Fatalf("创建远程测试目录失败: %v", err)
	}
	t.Logf("REMOTE_TEST_ROOT=%s", root)
	return root
}

// ensureImage 确认规划运行时测试镜像在远端存在，缺失时按需构建。
//
// 镜像缺失属于真实环境问题：构建失败让测试失败，而不是跳过。共享测试镜像不会被删除。
func (e *env) ensureImage(t *testing.T) {
	t.Helper()
	if imageID, err := e.runner.InspectImageID(e.ctx, e.image); err == nil {
		t.Logf("TEST_IMAGE=%s IMAGE_ID=%s", e.image, imageID)
		return
	}
	baseID, err := e.runner.InspectImageID(e.ctx, baseTestImage)
	if err != nil {
		t.Fatalf(
			"规划测试镜像的基础镜像 %s 在远端不存在: %v；请先构建 tests/integration/remote/agent/Dockerfile",
			baseTestImage,
			err,
		)
	}
	t.Logf("BASE_TEST_IMAGE=%s IMAGE_ID=%s", baseTestImage, baseID)
	dockerfile, err := os.ReadFile(dockerfilePath(t))
	if err != nil {
		t.Fatalf("读取规划测试镜像 Dockerfile 失败: %v", err)
	}
	buildContext, err := docker.NewContext([]docker.ContextFile{
		{RelativePath: docker.DockerfileName, Content: dockerfile, Mode: 0o644},
	})
	if err != nil {
		t.Fatalf("构造规划测试镜像构建上下文失败: %v", err)
	}
	transfer, err := e.client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := transfer.Close(); closeErr != nil {
			t.Logf("WARN 关闭 SFTP 通道失败: %v", closeErr)
		}
	})
	remoteDir := path.Join(e.remoteRoot, "image")
	version := domain.EnvironmentVersion{
		HostID:           "remote-planning-test-host",
		Ref:              domain.EnvironmentRef{Type: domain.EnvironmentTypeBase, Name: "base"},
		Number:           1,
		DockerfileSHA256: buildContext.Hash(),
		ContextSHA256:    buildContext.Hash(),
		ImageRef:         e.image,
		Status:           domain.BuildStatusBuilding,
		CreatedAt:        time.Now().UTC(),
	}
	version.BuildConfigSHA256 = docker.BuildConfigHash(version, "")
	result, buildErr := e.runner.BuildImage(e.ctx, transfer, docker.BuildSpec{
		Version:          version,
		Context:          buildContext,
		RemoteContextDir: path.Join(remoteDir, "context"),
		LogPath:          path.Join(remoteDir, "build.log"),
	})
	if buildErr != nil {
		t.Fatalf(
			"构建规划测试镜像失败: %v\n远程日志: %s\n日志末尾:\n%s",
			buildErr,
			result.LogPath,
			result.LogTail,
		)
	}
	if strings.TrimSpace(result.ImageID) == "" {
		t.Fatalf("构建成功但未取得实际镜像 ID: %+v", result)
	}
	t.Logf("TEST_IMAGE_BUILT=%s IMAGE_ID=%s", e.image, result.ImageID)
}

// dockerfilePath 返回本目录 Dockerfile 的绝对路径。
func dockerfilePath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("解析包目录失败: %v", err)
	}
	return filepath.Join(dir, "Dockerfile")
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

// sha256Hex 返回给定文本的十六进制摘要，用于填充环境版本的构建摘要字段。
func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// anonymousHTTPSRepository 把配置中的仓库地址转换为匿名 HTTPS 形式。
//
// 规划快照由生产代码在一次性准备容器内 Clone，而产品不注入任何 Git 凭据；因此真实验收
// 要求测试仓库可以通过匿名 HTTPS 访问。这里只做 URL 形式转换，不引入、不保存任何凭据。
func anonymousHTTPSRepository(t *testing.T, configured string) string {
	t.Helper()
	trimmed := strings.TrimSpace(configured)
	if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") {
		return trimmed
	}
	sshForm := strings.TrimPrefix(trimmed, "ssh://")
	sshForm = strings.TrimPrefix(sshForm, "git@")
	if index := strings.Index(sshForm, ":"); index > 0 {
		host := sshForm[:index]
		rest := strings.TrimPrefix(sshForm[index+1:], "//")
		return "https://" + host + "/" + rest
	}
	if index := strings.Index(sshForm, "/"); index > 0 {
		return "https://" + sshForm
	}
	t.Fatalf("无法把仓库地址 %q 转换为匿名 HTTPS 形式", configured)
	return ""
}

// volumeRun 记录本次测试运行创建的快照卷，并在全部测试结束后核验零残留。
type volumeRun struct {
	label string

	mu    sync.Mutex
	names []string
}

// newVolumeRun 创建一个卷登记表。
func newVolumeRun(label string) *volumeRun {
	return &volumeRun{label: label}
}

// Track 登记一个快照卷名称，并在用例结束时核验它已不存在。
//
// 语义与 testkit.ContainerRun.Track 一致：删除结果不足以判定成功，必须重新核验真实
// 状态；卷仍然存在时让用例失败。
func (r *volumeRun) Track(t *testing.T, runner *docker.Runner, name string) {
	t.Helper()
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || runner == nil {
		return
	}
	r.mu.Lock()
	r.names = append(r.names, trimmed)
	r.mu.Unlock()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), planningOpTimeout)
		defer cancel()
		if err := verifyVolumeAbsent(ctx, runner, trimmed); err != nil {
			t.Errorf("测试快照卷清理未通过（运行 %s）: %v", r.label, err)
		}
	})
}

// RegisterFinalizer 注册包级快照卷零残留核验。
func (r *volumeRun) RegisterFinalizer() {
	testkit.RegisterFinalizer("remote-planning-volume-cleanup", func(ctx context.Context) (bool, error) {
		r.mu.Lock()
		names := append([]string(nil), r.names...)
		r.mu.Unlock()
		if len(names) == 0 {
			fmt.Fprintf(
				os.Stdout,
				"REMOTE_VOLUME_VERIFY run=%s status=SKIP reason=no_test_volume_created\n",
				r.label,
			)
			return true, nil
		}
		session, err := testkit.OpenRemoteDockerForFinalize(ctx)
		if err != nil {
			return false, err
		}
		if session.SkipReason != "" {
			fmt.Fprintf(
				os.Stdout,
				"WARN [SKIP] 测试快照卷最终核验未执行（运行 %s）: %s\n",
				r.label,
				session.SkipReason,
			)
			return true, nil
		}
		defer func() {
			if session.Close == nil {
				return
			}
			if closeErr := session.Close(); closeErr != nil {
				fmt.Fprintf(os.Stdout, "WARN 关闭最终核验连接失败: %v\n", closeErr)
			}
		}()
		issues := make([]string, 0, len(names))
		for _, name := range names {
			if err := verifyVolumeAbsent(ctx, session.Runner, name); err != nil {
				issues = append(issues, err.Error())
			}
		}
		for _, name := range names {
			status := "absent"
			for _, issue := range issues {
				if strings.Contains(issue, name) {
					status = "present"
				}
			}
			fmt.Fprintf(
				os.Stdout,
				"REMOTE_VOLUME_VERIFY run=%s volume=%s status=%s\n",
				r.label,
				name,
				status,
			)
		}
		if len(issues) > 0 {
			return false, fmt.Errorf("测试快照卷零残留核验未通过: %s", strings.Join(issues, "; "))
		}
		return false, nil
	})
}

// verifyVolumeAbsent 核验命名卷已不存在。
func verifyVolumeAbsent(ctx context.Context, runner *docker.Runner, name string) error {
	if runner == nil {
		return fmt.Errorf("缺少 Docker 执行器，无法核验卷 %s", name)
	}
	_, err := runner.InspectVolume(ctx, name)
	if err == nil {
		return fmt.Errorf("快照卷 %s 仍然存在", name)
	}
	if docker.ErrorCodeOf(err) == docker.CodeVolumeNotFound {
		return nil
	}
	if docker.ErrorCodeOf(err) == docker.CodeContainerNotFound {
		return nil
	}
	return fmt.Errorf("无法确认快照卷 %s 是否已删除: %w", name, err)
}
