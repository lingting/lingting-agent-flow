// 本文件是 tests/integration/ssh/env.go 的迁移结果：目标配置装载、连接建立与工作空间。
//
// 改造点：参数类型由 *testing.T 换成 *testrunner.Context；用例工作空间改用编排进程
// 下发的目录（testkit.NewAt）；真实依赖授权改由编排进程下发；其余语义保持原样。
package ssh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/internal/testkit"
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
// 未在 tests/config/ssh.yml 中把 ssh.enable 设为 true 时输出警告并跳过；该开关是
// 该目录下所有真实连接用例的唯一启用入口，缺少配置文件同样视为未启用。
// 启用后必需配置缺失时同样输出警告并跳过，并说明缺少哪一项；配置完整但取值非法时
// 让用例直接失败，避免把配置错误当成缺少环境。
func loadTarget(t *testrunner.Context) target {
	t.Helper()
	requireRealTests(t, "真实 SSH 集成测试")
	config, ok := testkit.LoadSSHConfig(t)
	if !ok || !config.Enabled {
		reason := testkit.EnableReason(t, testkit.SSHConfigFile, "ssh.enable")
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	missing := make([]string, 0, 2)
	if strings.TrimSpace(config.Host) == "" {
		missing = append(missing, "ssh.host")
	}
	if strings.TrimSpace(config.User) == "" {
		missing = append(missing, "ssh.user")
	}
	if len(missing) > 0 {
		reason := testkit.SkipReason(t, testkit.SSHConfigFile, missing)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	if config.Port <= 0 || config.Port > 65535 {
		t.Fatalf("ssh.port 不是合法端口: %d", config.Port)
	}
	return target{
		host:            strings.TrimSpace(config.Host),
		user:            strings.TrimSpace(config.User),
		port:            config.Port,
		keyPath:         strings.TrimSpace(config.PrivateKeyPath),
		knownHostsPaths: testkit.KnownHostsSources(t),
	}
}

// logTarget 输出目标与 known_hosts 来源，便于区分"配置错误"与"代码问题"。
func logTarget(t *testrunner.Context, tg target) {
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

// describeKeyPath 描述私钥是否已配置。
//
// 只报告"已配置 / 未配置"，不回显路径：真实测试的日志会被收集，路径属于运行环境
// 信息，不需要出现在日志里。
func describeKeyPath(path string) string {
	if path == "" {
		return "未配置（ssh.private_key_path）"
	}
	return "已配置（ssh.private_key_path）"
}

// dial 建立经过主机密钥校验与身份认证的连接，并在用例结束时关闭。
func dial(t *testrunner.Context, ws *testkit.Workspace, tg target) sshclient.DialResult {
	t.Helper()
	logTarget(t, tg)
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

	// 本版本只支持私钥认证，因此真实远程测试必须提供私钥路径。
	if tg.keyPath == "" {
		t.Fatalf("真实 SSH 测试必须设置私钥路径（ssh.private_key_path，本版本只支持私钥认证）")
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

// newWorkspace 以编排进程下发的用例工作空间为根本地产物目录。
//
// 本机 known_hosts 快照、上传下载文件都放在该目录中，用例结束后保留。
func newWorkspace(t *testrunner.Context) *testkit.Workspace {
	t.Helper()
	ws := testkit.NewAt(t, t.WorkspaceDir())
	ws.Report()
	return ws
}
