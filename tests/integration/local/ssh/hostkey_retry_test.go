package sshclient_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// newMultiKeyServer 启动同时提供多种主机密钥的本地 SSH 服务。
//
// 服务端按给定顺序注册主机密钥，握手时由客户端请求的算法决定实际使用哪一个。
func newMultiKeyServer(t *testing.T, keys ...ssh.Signer) *testServer {
	t.Helper()
	server := &testServer{t: t, done: make(chan struct{})}
	server.config = &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if conn.User() == testUser && string(password) == testPassword {
				return nil, nil
			}
			return nil, errors.New("认证失败")
		},
	}
	for _, key := range keys {
		server.config.AddHostKey(key)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试 SSH 服务失败: %v", err)
	}
	server.listener = listener
	server.hostKey = keys[0]
	go server.acceptLoop()
	t.Cleanup(server.close)
	return server
}

// generateECDSASigner 生成一次性 ECDSA 主机密钥。
func generateECDSASigner(t *testing.T) ssh.Signer {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成 ECDSA 主机密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("构造 ECDSA 签名器失败: %v", err)
	}
	return signer
}

// writeKnownHostsLines 把多条 known_hosts 记录写入同一文件并返回路径。
func writeKnownHostsLines(t *testing.T, entries []struct {
	key  ssh.PublicKey
	host string
	port int
}) string {
	t.Helper()
	path := filepath.Join(testkit.Dir(t), "known_hosts")
	content := ""
	for _, entry := range entries {
		address := net.JoinHostPort(entry.host, strconv.Itoa(entry.port))
		content += knownhosts.Line([]string{knownhosts.Normalize(address)}, entry.key) + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}
	return path
}

// TestDialFallsBackToNextRecordedHostKeyType 验证多类型记录时按顺序协商、首个成功即返回。
//
// 场景：known_hosts 先记录了一条已失效的 ECDSA 记录，随后记录当前有效的 ed25519 记录。
// 逐个协商的实现应先用 ECDSA 失败（服务端已不再提供该密钥），再用 ed25519 成功；
// 若只把两种类型一次性交给库协商，服务端会选中 ECDSA 而直接失败。
func TestDialFallsBackToNextRecordedHostKeyType(t *testing.T) {
	t.Parallel()
	serverKey := generateHostKey(t)
	server := newMultiKeyServer(t, serverKey)
	host, port := server.endpoint()

	staleKey := generateECDSASigner(t).PublicKey()
	knownHosts := writeKnownHostsLines(t, []struct {
		key  ssh.PublicKey
		host string
		port int
	}{
		{key: staleKey, host: host, port: port},
		{key: serverKey.PublicKey(), host: host, port: port},
	})

	store := newStore(t, knownHosts)
	dialer := newDialer(t, store)
	config, provider := passwordAuth(testPassword)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err != nil {
		t.Fatalf("存在有效记录时应连接成功，实际失败: %v", err)
	}
	defer func() {
		if closeErr := result.Client.Close(); closeErr != nil {
			t.Errorf("关闭连接失败: %v", closeErr)
		}
	}()
	if result.HostKey.Algorithm != serverKey.PublicKey().Type() {
		t.Fatalf("应协商到有效记录的类型 %s，实际 %s", serverKey.PublicKey().Type(), result.HostKey.Algorithm)
	}
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		t.Fatalf("主机密钥应判定为 trusted，实际 %s", result.HostKeyVerdict)
	}
}

// TestDialPrefersFirstRecordedHostKeyType 验证记录顺序决定优先协商的类型。
//
// 服务端同时提供 ed25519 与 ECDSA，known_hosts 两条都记录且 ECDSA 在前：
// 应协商到 ECDSA（首条记录），而不是 ed25519。
func TestDialPrefersFirstRecordedHostKeyType(t *testing.T) {
	t.Parallel()
	ed25519Key := generateHostKey(t)
	ecdsaKey := generateECDSASigner(t)
	server := newMultiKeyServer(t, ed25519Key, ecdsaKey)
	host, port := server.endpoint()

	knownHosts := writeKnownHostsLines(t, []struct {
		key  ssh.PublicKey
		host string
		port int
	}{
		{key: ecdsaKey.PublicKey(), host: host, port: port},
		{key: ed25519Key.PublicKey(), host: host, port: port},
	})

	dialer := newDialer(t, newStore(t, knownHosts))
	config, provider := passwordAuth(testPassword)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer func() {
		if closeErr := result.Client.Close(); closeErr != nil {
			t.Errorf("关闭连接失败: %v", closeErr)
		}
	}()
	if result.HostKey.Algorithm != ecdsaKey.PublicKey().Type() {
		t.Fatalf("应协商到首条记录的类型 %s，实际 %s", ecdsaKey.PublicKey().Type(), result.HostKey.Algorithm)
	}
}

// TestDialReportsFirstHostKeyFailureWhenAllTypesFail 验证全部已记录类型都失败时返回首个结论。
func TestDialReportsFirstHostKeyFailureWhenAllTypesFail(t *testing.T) {
	t.Parallel()
	server := newMultiKeyServer(t, generateHostKey(t))
	host, port := server.endpoint()

	knownHosts := writeKnownHostsLines(t, []struct {
		key  ssh.PublicKey
		host string
		port int
	}{
		{key: generateHostKey(t).PublicKey(), host: host, port: port},
		{key: generateECDSASigner(t).PublicKey(), host: host, port: port},
	})

	dialer := newDialer(t, newStore(t, knownHosts))
	config, provider := passwordAuth(testPassword)
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err == nil {
		t.Fatal("所有已记录类型都不可信时应当失败")
	}
	// 首条记录是 ed25519 但密钥已变化，服务端仍提供 ed25519：应报告密钥不一致。
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeHostKeyMismatch {
		t.Fatalf("应报告首个类型的 host_key_mismatch，实际 %s: %v", code, err)
	}
}
