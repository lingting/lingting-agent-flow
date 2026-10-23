// Package workflow_remote 提供需要真实远程 SSH、Docker 与容器内 Pi CLI 的单任务
// 端到端验收，以及用于定位真实链路阻塞点的诊断用例。
//
// 该目录与 tests/integration/local/workflow/ 的分工：
//   - 本地目录用受控替身覆盖四类终局，可在任意开发机重复执行；
//   - 本目录只用真实依赖（真实 SSH、真实 Docker、容器内真实 Git、容器内真实 Pi 与
//     真实模型），用于验证真实环境下链路确实成立。
//
// 安全约束（与仓库测试规范一致）：只操作带随机后缀、属于本次测试的容器与远程目录；
// 不执行全局 prune；主机密钥校验不可关闭；测试产物保留，不自动清理。
package workflow_remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 真实远程端到端用例使用的常量。
const (
	// e2eWorkDir 是容器内项目工作目录，与产品默认值一致。
	e2eWorkDir = domain.DefaultContainerWorkDir
	// e2eContainerSessions 是容器内临时 Session 目录，与产品默认值一致。
	e2eContainerSessions = "/tmp/agentflow/sessions"
	// e2eOpTimeout 是单次远程操作超时。
	e2eOpTimeout = 10 * time.Minute
	// e2eRunTimeout 是一次完整端到端运行的超时。
	e2eRunTimeout = 60 * time.Minute
)

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
func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	if report := e2eConfigReport(t); len(report) > 0 {
		reason := "WARN [SKIP] 真实远程端到端验收缺少配置: " + strings.Join(report, "；")
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	workspace := testkit.New(t)
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
		suffix:    randomSuffix(t),
	}
	env.dial(t)
	return env
}

// e2eConfigReport 返回缺失的配置项说明。
func e2eConfigReport(t *testing.T) []string {
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
func resolveModel(t *testing.T) string {
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
func resolveGitConfig(t *testing.T) testkit.GitConfig {
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
func resolveAgentImage(t *testing.T) string {
	t.Helper()
	config, _ := testkit.LoadDockerConfig(t)
	image := strings.TrimSpace(config.AgentImage)
	if image == "" {
		image = "agentflow-test-agent:1.0.0"
	}
	return image
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

// dial 建立真实 SSH 连接。
func (e *e2eEnv) dial(t *testing.T) {
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
}

// TestRemoteE2EPreconditions 校验真实端到端验收的前置条件并输出实际环境信息。
//
// 该用例不创建容器、不调用模型：它把"配置不全导致的跳过"与"配置齐全后的真实失败"
// 区分开，并记录实际观测到的 Docker 版本与模型标识，便于事后复核。
func TestRemoteE2EPreconditions(t *testing.T) {
	t.Parallel()
	env := newE2EEnv(t)
	// 预检校验工作空间是否可写，因此这里使用远程机器上真实存在的目录：
	// 不创建任何测试专属目录，也不修改远端任何内容。
	preflight := env.runner.Preflight(env.ctx, docker.PreflightOptions{
		WorkspaceRoot:   "/tmp",
		CreateWorkspace: false,
	})
	if !preflight.Ready() {
		t.Fatalf("远程 Docker 预检未通过(%s): %s", preflight.ErrorCode, preflight.Detail)
	}
	t.Logf("REMOTE_DOCKER CLI=%s ENGINE=%s API=%s MIN_API=%s",
		preflight.CLIVersion, preflight.EngineVersion,
		preflight.APIVersion, preflight.MinAPIVersion)
	if preflight.CLIVersion != docker.MinimumCLIVersion &&
		preflight.CLIVersion < docker.MinimumCLIVersion {
		t.Fatalf("Docker CLI 版本低于基线 %s: %s",
			docker.MinimumCLIVersion, preflight.CLIVersion)
	}
	if preflight.EngineVersion < docker.MinimumEngineVersion {
		t.Fatalf("Docker Engine 版本低于基线 %s: %s",
			docker.MinimumEngineVersion, preflight.EngineVersion)
	}
	t.Logf("GIT_REPOSITORY=%s", env.repo.ResolvedRepository())
	t.Logf("PI_MODEL=%s", env.model)
}
