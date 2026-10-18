// Package ssh_integration 提供需要真实远程 Linux 机器的 SSH 集成测试入口。
//
// 运行方式：
//
//	AGENTFLOW_TEST_SSH_HOST=10.0.0.10 \
//	AGENTFLOW_TEST_SSH_USER=deploy \
//	AGENTFLOW_TEST_SSH_KEY_PATH=$HOME/.ssh/id_ed25519 \
//	go test -count=1 -v ./tests/integration/ssh/...
//
// 必需目标变量缺失时输出 WARN 并跳过；目标配置完整但连接、认证或主机密钥校验
// 失败时必须让测试失败，不得当作缺少可选依赖跳过。
package ssh_integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 真实 SSH 测试使用的环境变量。
const (
	// EnvHost 是 SSH 服务器地址，必填。
	EnvHost = "AGENTFLOW_TEST_SSH_HOST"
	// EnvUser 是登录用户名，必填。
	EnvUser = "AGENTFLOW_TEST_SSH_USER"
	// EnvPort 是 SSH 端口，可选，默认 22。
	EnvPort = "AGENTFLOW_TEST_SSH_PORT"
	// EnvKeyPath 是测试使用的私钥路径，可选；为空时使用 SSH Agent。
	EnvKeyPath = "AGENTFLOW_TEST_SSH_KEY_PATH"
	// EnvKnownHosts 是只读的 known_hosts 文件，可选。
	//
	// 未设置时复用本机用户 OpenSSH 的 known_hosts；取值见 testkit.KnownHostsEnv。
	EnvKnownHosts = testkit.KnownHostsEnv
	// defaultPort 是未指定端口时使用的 SSH 端口。
	defaultPort = 22
)

// target 是一次真实 SSH 测试的连接目标。
type target struct {
	host    string
	user    string
	port    int
	keyPath string
	// knownHostsPaths 是参与校验的只读 known_hosts 文件列表。
	knownHostsPaths []string
}

// loadTarget 读取并校验目标配置。
//
// 必需变量缺失时输出警告并跳过；只配置了一部分时说明缺少哪一项再跳过。
func loadTarget(t *testing.T) target {
	t.Helper()
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
			"WARN [SKIP] SSH integration tests: set AGENTFLOW_TEST_SSH_HOST and "+
				"AGENTFLOW_TEST_SSH_USER to enable (missing: %s).",
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

// logTarget 输出目标与 known_hosts 来源，便于区分"配置错误"与"代码问题"。
func logTarget(t *testing.T, tg target) {
	t.Helper()
	t.Logf(
		"SSH 目标: %s@%s:%d，私钥: %s",
		tg.user,
		tg.host,
		tg.port,
		describeKeyPath(tg.keyPath),
	)
	t.Logf("主机密钥来源: %s", testkit.DescribeKnownHosts(t))
}

// describeKeyPath 描述私钥来源，未指定时说明使用 SSH Agent。
func describeKeyPath(path string) string {
	if path == "" {
		return "SSH Agent"
	}
	return path
}

// dial 建立经过主机密钥校验与身份认证的连接，并在测试结束时关闭。
func dial(t *testing.T, ws *testkit.Workspace, tg target) sshclient.DialResult {
	t.Helper()
	logTarget(t, tg)
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

	auth := sshclient.AuthConfig{Method: sshclient.AuthMethodAgent}
	if tg.keyPath != "" {
		auth = sshclient.AuthConfig{
			Method:         sshclient.AuthMethodPrivateKey,
			PrivateKeyPath: tg.keyPath,
		}
	}
	result, err := dialer.Dial(ctx, sshclient.Endpoint{
		Host: tg.host, Port: tg.port, Username: tg.user,
	}, auth, nil)
	if err != nil {
		if sshclient.ErrorCodeOf(err) == sshclient.CodeHostKeyUnknown {
			storePath := filepath.Join(ws.Root(), "known_hosts")
			t.Fatalf(
				"主机密钥未知，请先核验指纹后写入 %s: %s %s\n%s",
				storePath,
				result.HostKey.Algorithm,
				result.HostKey.Fingerprint,
				testkit.HostKeyHint(t, storePath),
			)
		}
		t.Fatalf("连接真实远程机器失败: %v", err)
	}
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		t.Fatalf("主机密钥应已被信任，实际: %s", result.HostKeyVerdict)
	}
	ws.Cleanup(result.Client.Close)
	return result
}

// newWorkspace 创建本次用例的测试工作空间并输出其绝对路径。
//
// 本机数据库、known_hosts、上传下载文件都放在该目录中，测试结束后保留。
func newWorkspace(t *testing.T) *testkit.Workspace {
	t.Helper()
	ws := testkit.New(t)
	ws.Report()
	return ws
}
