// 本文件是 tests/integration/local/ssh 中连接与传输辅助的迁移结果：拨号、主机密钥库、
// 私钥文件、SFTP 通道与轮询等待。
package ssh

import (
	"context"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// keySeq 给同一用例内的多份私钥文件分配不同文件名。
var keySeq atomic.Int64

// caseWorkspace 返回挂在用例工作空间下的辅助组件。
func caseWorkspace(t *testrunner.Context) *testkit.Workspace {
	t.Helper()
	return testkit.NewAt(t, t.WorkspaceDir())
}

// testContext 返回带超时的测试上下文。
func testContext(t *testrunner.Context) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// newStore 构造指向用例工作空间 known_hosts 的主机密钥库。
//
// 主机密钥库只读：未给出 trustedPaths 时使用一个空文件，避免空路径集合。
func newStore(t *testrunner.Context, trustedPaths ...string) *sshclient.KnownHostsStore {
	t.Helper()
	paths := trustedPaths
	if len(paths) == 0 {
		paths = []string{writeEmptyKnownHostsFile(t)}
	}
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: paths,
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	return store
}

// writeEmptyKnownHostsFile 在用例工作空间写入一个空 known_hosts 并返回路径。
func writeEmptyKnownHostsFile(t *testrunner.Context) string {
	t.Helper()
	path := filepath.Join(caseWorkspace(t).Dir("known-hosts-empty"), "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}
	return path
}

// newDialer 构造使用给定主机密钥库的连接器。
func newDialer(t *testrunner.Context, store sshclient.HostKeyStore) *sshclient.Dialer {
	t.Helper()
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{
		HostKeys:       store,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造连接器失败: %v", err)
	}
	return dialer
}

// privateKeyAuth 返回私钥认证配置。
//
// 本版本只支持私钥认证，因此所有连接测试都通过私钥文件完成认证。
func privateKeyAuth(t *testrunner.Context) sshclient.AuthConfig {
	t.Helper()
	return sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: writePrivateKey(t, "", false),
	}
}

// dialTestServer 按给定的主机密钥库连接测试服务并返回已认证连接。
func dialTestServer(
	t *testrunner.Context,
	server *testServer,
	store sshclient.HostKeyStore,
) sshclient.Client {
	t.Helper()
	host, port := server.endpoint()
	dialer := newDialer(t, store)
	config := privateKeyAuth(t)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config)
	if err != nil {
		t.Fatalf("建立测试连接失败: %v", err)
	}
	return result.Client
}

// newAuthenticatedClient 启动测试服务并建立一条已认证连接。
func newAuthenticatedClient(t *testrunner.Context) (*testServer, sshclient.Client) {
	t.Helper()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config := privateKeyAuth(t)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config)
	if err != nil {
		t.Fatalf("建立测试连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := result.Client.Close(); closeErr != nil {
			t.Errorf("关闭测试连接失败: %v", closeErr)
		}
	})
	return server, result.Client
}

// openTransfer 打开一条 SFTP 通道并在测试结束时关闭。
func openTransfer(t *testrunner.Context, client sshclient.Client) sshclient.FileTransfer {
	t.Helper()
	transfer, err := client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := transfer.Close(); closeErr != nil {
			t.Errorf("关闭 SFTP 通道失败: %v", closeErr)
		}
	})
	return transfer
}

// tempRemoteDir 生成一个临时远程目录路径。
func tempRemoteDir(t *testrunner.Context) string {
	t.Helper()
	return toRemotePath(caseWorkspace(t).Dir("remote"))
}

// tempRemotePath 生成一个临时远程文件路径。
func tempRemotePath(t *testrunner.Context) string {
	t.Helper()
	return tempRemoteDir(t) + "/remote.txt"
}

// toRemotePath 把本机临时目录转换为测试 SFTP 服务可识别的远程路径。
//
// 测试服务运行在本机，因此把本机绝对路径转写为 /<绝对路径> 形式：
// pkg/sftp 在 Windows 上会把它还原为同样的本机路径，从而让测试覆盖真实的
// SFTP 协议交互，而不引入与平台相关的分支逻辑。
func toRemotePath(localPath string) string {
	slashed := filepath.ToSlash(localPath)
	if strings.HasPrefix(slashed, "/") {
		return slashed
	}
	return "/" + slashed
}

// waitFor 在超时前轮询条件，用于验证异步资源释放。
func waitFor(t *testrunner.Context, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s（等待超时 %s）", message, timeout)
}

// writePrivateKey 生成一个测试私钥文件并返回其路径。
func writePrivateKey(t *testrunner.Context, passphrase string, encrypted bool) string {
	t.Helper()
	dir := caseWorkspace(t).Dir("keys")
	path := filepath.Join(dir, fmt.Sprintf("id_test-%d", keySeq.Add(1)))
	data := generatePrivateKeyPEM(t, passphrase, encrypted)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	return path
}

// generatePrivateKeyPEM 生成 OpenSSH 格式的私钥内容。
func generatePrivateKeyPEM(t *testrunner.Context, passphrase string, encrypted bool) []byte {
	t.Helper()
	_, privateKey := generateEd25519KeyPair(t)
	var (
		block *pem.Block
		err   error
	)
	if encrypted {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(
			privateKey,
			"test",
			[]byte(passphrase),
		)
	} else {
		block, err = ssh.MarshalPrivateKey(privateKey, "test")
	}
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	return pem.EncodeToMemory(block)
}

// mustParsePrivateKey 解析测试私钥文件，用于取得对应的公钥。
func mustParsePrivateKey(t *testrunner.Context, path string) ssh.Signer {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取测试私钥失败: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatalf("解析测试私钥失败: %v", err)
	}
	return signer
}
