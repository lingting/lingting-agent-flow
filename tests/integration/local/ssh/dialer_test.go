package sshclient_test

import (
	"context"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"golang.org/x/crypto/ssh"
)

// testContext 返回带超时的测试上下文。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// newStore 构造指向临时目录的主机密钥库。
func newStore(t *testing.T, trustedPaths ...string) *sshclient.KnownHostsStore {
	t.Helper()
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: trustedPaths,
		StorePath:    filepath.Join(testkit.Dir(t), "known_hosts"),
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	return store
}

// newDialer 构造使用给定主机密钥库的连接器。
func newDialer(t *testing.T, store sshclient.HostKeyStore) *sshclient.Dialer {
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

// passwordAuth 返回密码认证配置与提供器。
func passwordAuth(password string) (sshclient.AuthConfig, sshclient.CredentialProvider) {
	return sshclient.AuthConfig{Method: sshclient.AuthMethodPassword},
		sshclient.PasswordProvider{Password: []byte(password)}
}

// TestDialRequiresHostKeyStore 验证未提供主机密钥校验时不允许建立连接器。
func TestDialRequiresHostKeyStore(t *testing.T) {
	t.Parallel()
	if _, err := sshclient.NewDialer(sshclient.DialOptions{}); err == nil {
		t.Fatal("缺少主机密钥校验实现时应当返回错误")
	}
}

// TestDialUnknownHostKey 验证未知主机密钥被拒绝并给出指纹。
func TestDialUnknownHostKey(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	dialer := newDialer(t, newStore(t))
	config, provider := passwordAuth(testPassword)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err == nil {
		t.Fatal("未知主机密钥应当拒绝连接")
	}
	if result.Client != nil {
		t.Fatal("拒绝连接时不应返回已认证连接")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeHostKeyUnknown {
		t.Fatalf("错误类别应为 host_key_unknown，实际: %s", code)
	}
	if result.HostKey.Fingerprint != server.hostKeyFingerprint() {
		t.Fatalf("应报告服务端实际指纹，实际: %s", result.HostKey.Fingerprint)
	}
	if result.HostKeyVerdict != sshclient.VerdictUnknown {
		t.Fatalf("校验结论应为 unknown，实际: %s", result.HostKeyVerdict)
	}
}

// TestDialTrustedHostKeySucceeds 验证已信任主机密钥可以完成密码认证。
func TestDialTrustedHostKeySucceeds(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config, provider := passwordAuth(testPassword)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err != nil {
		t.Fatalf("已信任主机应连接成功: %v", err)
	}
	defer func() {
		if closeErr := result.Client.Close(); closeErr != nil {
			t.Errorf("关闭连接失败: %v", closeErr)
		}
	}()
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		t.Fatalf("校验结论应为 trusted，实际: %s", result.HostKeyVerdict)
	}
	expected := sshclient.Endpoint{Host: host, Port: port}.Address()
	if result.Client.Endpoint().Address() != expected {
		t.Fatalf("连接目标不符: %s", result.Client.Endpoint().Address())
	}
}

// TestDialHostKeyMismatch 验证主机密钥变化时拒绝连接并报告两边指纹。
func TestDialHostKeyMismatch(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	// 用另一把主机密钥写入信任记录，模拟服务端密钥被替换。
	other := newTestServer(t)
	store := newStore(t, writeKnownHosts(t, host, port, other.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config, provider := passwordAuth(testPassword)
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err == nil {
		t.Fatal("主机密钥变化时应当拒绝连接")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeHostKeyMismatch {
		t.Fatalf("错误类别应为 host_key_mismatch，实际: %s", code)
	}
	for _, expected := range []string{server.hostKeyFingerprint(), other.hostKeyFingerprint()} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("错误信息应同时包含实际与已信任指纹 %s，实际: %v", expected, err)
		}
	}
}

// TestDialAuthFailure 验证认证失败与握手失败的区分。
func TestDialAuthFailure(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config, provider := passwordAuth("wrong-password")
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err == nil {
		t.Fatal("错误密码应当认证失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeAuthFailed {
		t.Fatalf("错误类别应为 auth_failed，实际: %s", code)
	}
}

// TestDialUnreachable 验证 TCP 不可达与认证失败被区分。
func TestDialUnreachable(t *testing.T) {
	t.Parallel()
	listener := newTestServer(t)
	host, port := listener.endpoint()
	listener.close()
	dialer := newDialer(t, newStore(t))
	config, provider := passwordAuth(testPassword)
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err == nil {
		t.Fatal("不可达地址应当连接失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeUnreachable {
		t.Fatalf("错误类别应为 unreachable，实际: %s", code)
	}
}

// TestDialHandshakeFailed 验证非 SSH 服务被识别为握手失败。
func TestDialHandshakeFailed(t *testing.T) {
	t.Parallel()
	address, stop := startPlainTCPServer(t, "HTTP/1.1 400 Bad Request\r\n\r\n")
	defer stop()
	dialer := newDialer(t, newStore(t))
	config, provider := passwordAuth(testPassword)
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: address.Host, Port: address.Port, Username: testUser,
	}, config, provider)
	if err == nil {
		t.Fatal("非 SSH 服务应当握手失败")
	}
	code := sshclient.ErrorCodeOf(err)
	if code != sshclient.CodeHandshakeFailed && code != sshclient.CodeTimeout {
		t.Fatalf("错误类别应为 handshake_failed，实际: %s", code)
	}
}

// TestDialMissingCredential 验证没有凭据时立即失败而不是挂起。
func TestDialMissingCredential(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config := sshclient.AuthConfig{Method: sshclient.AuthMethodPassword}
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, nil)
	if err == nil {
		t.Fatal("缺少凭据提供器时应当失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeNoCredential {
		t.Fatalf("错误类别应为 no_credential，实际: %s", code)
	}
}

// TestPasswordProviderReportsNoCredential 验证空密码返回 ErrNoCredential。
func TestPasswordProviderReportsNoCredential(t *testing.T) {
	t.Parallel()
	provider := sshclient.PasswordProvider{}
	_, err := provider.Credential(context.Background())
	if !errors.Is(err, sshclient.ErrNoCredential) {
		t.Fatalf("应当返回 ErrNoCredential，实际: %v", err)
	}
}

// TestCredentialClear 验证凭据材料可以被清除。
func TestCredentialClear(t *testing.T) {
	t.Parallel()
	credential := sshclient.Credential{
		Method:     sshclient.AuthMethodPassword,
		Secret:     []byte("secret"),
		Passphrase: []byte("pass"),
	}
	credential.Clear()
	if credential.Secret != nil || credential.Passphrase != nil {
		t.Fatalf("凭据应当被清除: %+v", credential)
	}
}

// TestPrivateKeyAuth 验证私钥认证可以建立连接。
func TestPrivateKeyAuth(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	keyPath := writePrivateKey(t, "", false)
	config := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: keyPath,
	}
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, nil)
	if err != nil {
		t.Fatalf("私钥认证应成功: %v", err)
	}
	if err := result.Client.Close(); err != nil {
		t.Errorf("关闭连接失败: %v", err)
	}
}

// TestPrivateKeyAuthWithPassphrase 验证受口令保护的私钥可以解锁。
func TestPrivateKeyAuthWithPassphrase(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	keyPath := writePrivateKey(t, "test-passphrase", true)
	config := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: keyPath,
	}
	provider := sshclient.PassphraseProvider{Passphrase: []byte("test-passphrase")}
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err != nil {
		t.Fatalf("带口令的私钥认证应成功: %v", err)
	}
	if err := result.Client.Close(); err != nil {
		t.Errorf("关闭连接失败: %v", err)
	}
}

// TestPrivateKeyWrongPassphrase 验证错误口令返回可诊断错误。
func TestPrivateKeyWrongPassphrase(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	keyPath := writePrivateKey(t, "test-passphrase", true)
	config := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: keyPath,
	}
	provider := sshclient.PassphraseProvider{Passphrase: []byte("wrong")}
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err == nil {
		t.Fatal("错误口令应当失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodePrivateKey {
		t.Fatalf("错误类别应为 private_key_error，实际: %s", code)
	}
}

// TestPrivateKeyMissingFile 验证私钥文件不可读时返回可诊断错误。
func TestPrivateKeyMissingFile(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: filepath.Join(testkit.Dir(t), "missing_key"),
	}
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, nil)
	if err == nil {
		t.Fatal("私钥文件不存在时应当失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodePrivateKey {
		t.Fatalf("错误类别应为 private_key_error，实际: %s", code)
	}
}

// TestPrivateKeyInvalidFormat 验证私钥格式不受支持时返回可诊断错误。
func TestPrivateKeyInvalidFormat(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	keyPath := filepath.Join(testkit.Dir(t), "bad_key")
	if err := os.WriteFile(keyPath, []byte("not a private key"), 0o600); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	config := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: keyPath,
	}
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, nil)
	if err == nil {
		t.Fatal("非法私钥应当失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodePrivateKey {
		t.Fatalf("错误类别应为 private_key_error，实际: %s", code)
	}
}

// TestAgentUnavailable 验证 SSH Agent 不可用时返回明确错误。
func TestAgentUnavailable(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config := sshclient.AuthConfig{
		Method:      sshclient.AuthMethodAgent,
		AgentSocket: filepath.Join(testkit.Dir(t), "missing-agent.sock"),
	}
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, nil)
	if err == nil {
		t.Fatal("Agent 不可用时应当失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeAgentUnavailable {
		t.Fatalf("错误类别应为 agent_unavailable，实际: %s", code)
	}
}

// TestAgentWithoutSocket 验证未配置 Agent 套接字时返回明确错误。
func TestAgentWithoutSocket(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config := sshclient.AuthConfig{Method: sshclient.AuthMethodAgent}
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, nil)
	if err == nil {
		t.Fatal("缺少 Agent 套接字时应当失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeAgentUnavailable {
		t.Fatalf("错误类别应为 agent_unavailable，实际: %s", code)
	}
}

// TestEndpointAddress 验证地址拼接支持 IPv6。
func TestEndpointAddress(t *testing.T) {
	t.Parallel()
	endpoint := sshclient.Endpoint{Host: "2001:db8::1", Port: 2222}
	if got := endpoint.Address(); got != "[2001:db8::1]:2222" {
		t.Fatalf("IPv6 地址拼接不符: %s", got)
	}
}

// TestAuthConfigValidate 验证认证配置校验。
func TestAuthConfigValidate(t *testing.T) {
	t.Parallel()
	cases := map[string]sshclient.AuthConfig{
		"未知认证方式": {Method: "kerberos"},
		"私钥缺少路径": {Method: sshclient.AuthMethodPrivateKey},
	}
	for name, config := range cases {
		if err := config.Validate(); err == nil {
			t.Fatalf("%s 应当被拒绝", name)
		}
	}
}

// writePrivateKey 生成一个测试私钥文件并返回其路径。
func writePrivateKey(t *testing.T, passphrase string, encrypted bool) string {
	t.Helper()
	path := filepath.Join(testkit.Dir(t), "id_test")
	data := generatePrivateKeyPEM(t, passphrase, encrypted)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	return path
}

// generatePrivateKeyPEM 生成 OpenSSH 格式的私钥内容。
func generatePrivateKeyPEM(t *testing.T, passphrase string, encrypted bool) []byte {
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
