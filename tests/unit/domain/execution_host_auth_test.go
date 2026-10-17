package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// TestNewExecutionHostDefaultsToAgentAuth 验证未指定认证方式时使用 SSH Agent。
func TestNewExecutionHostDefaultsToAgentAuth(t *testing.T) {
	t.Parallel()
	host, err := domain.NewExecutionHost("", "节点", "10.0.0.10", 0, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if host.AuthMethod != domain.AuthMethodAgent {
		t.Fatalf("默认认证方式应为 agent，实际: %s", host.AuthMethod)
	}
	if host.PrivateKeyPath != "" {
		t.Fatalf("默认不应有私钥路径，实际: %s", host.PrivateKeyPath)
	}
	if host.LastConnectStatus != domain.ConnectStatusUnknown {
		t.Fatalf("新建节点不应有连接结果，实际: %s", host.LastConnectStatus)
	}
}

// TestNewExecutionHostWithPrivateKeyAuth 验证私钥认证只保存文件路径。
func TestNewExecutionHostWithPrivateKeyAuth(t *testing.T) {
	t.Parallel()
	host, err := domain.NewExecutionHost(
		"",
		"节点",
		"10.0.0.10",
		22,
		"deploy",
		"",
		time.Now(),
		domain.WithAuth(domain.AuthMethodPrivateKey, "/home/user/.ssh/id_ed25519"),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if host.AuthMethod != domain.AuthMethodPrivateKey {
		t.Fatalf("认证方式不符: %s", host.AuthMethod)
	}
	if host.PrivateKeyPath != "/home/user/.ssh/id_ed25519" {
		t.Fatalf("私钥路径不符: %s", host.PrivateKeyPath)
	}
}

// TestNewExecutionHostRejectsPrivateKeyWithoutPath 验证私钥认证必须给出私钥路径。
func TestNewExecutionHostRejectsPrivateKeyWithoutPath(t *testing.T) {
	t.Parallel()
	_, err := domain.NewExecutionHost(
		"",
		"节点",
		"10.0.0.10",
		22,
		"deploy",
		"",
		time.Now(),
		domain.WithAuth(domain.AuthMethodPrivateKey, ""),
	)
	if err == nil {
		t.Fatal("缺少私钥路径时应当被拒绝")
	}
	if !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("应当是字段校验错误，实际: %v", err)
	}
}

// TestAuthMethodValidate 验证认证方式取值约束。
func TestAuthMethodValidate(t *testing.T) {
	t.Parallel()
	for _, method := range []domain.AuthMethod{
		domain.AuthMethodAgent,
		domain.AuthMethodPrivateKey,
		domain.AuthMethodPassword,
	} {
		if err := method.Validate(); err != nil {
			t.Fatalf("认证方式 %s 应当受支持: %v", method, err)
		}
	}
	if err := domain.AuthMethod("kerberos").Validate(); err == nil {
		t.Fatal("未知认证方式应当返回错误")
	}
}

// TestParseAuthMethod 验证认证方式解析对大小写与空白不敏感。
func TestParseAuthMethod(t *testing.T) {
	t.Parallel()
	method, err := domain.ParseAuthMethod("  Private_Key ")
	if err != nil {
		t.Fatalf("解析认证方式失败: %v", err)
	}
	if method != domain.AuthMethodPrivateKey {
		t.Fatalf("解析结果不符: %s", method)
	}
	if _, err := domain.ParseAuthMethod("unknown"); err == nil {
		t.Fatal("未知认证方式应当返回错误")
	}
}

// TestExecutionHostRejectsNonSHA256Fingerprint 验证主机密钥指纹格式约束。
func TestExecutionHostRejectsNonSHA256Fingerprint(t *testing.T) {
	t.Parallel()
	host, err := domain.NewExecutionHost("", "节点", "10.0.0.10", 22, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	host.HostKeyFingerprint = "MD5:aa:bb:cc"
	if err := host.Validate(); err == nil {
		t.Fatal("非 SHA256 指纹应当被拒绝")
	}
}

// TestRecordConnectResult 验证连接结果记录与敏感信息约束。
func TestRecordConnectResult(t *testing.T) {
	t.Parallel()
	host, err := domain.NewExecutionHost("", "节点", "10.0.0.10", 22, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	at := time.Now()
	if err := host.RecordConnectResult(domain.ConnectStatusFailed, "auth_failed", at); err != nil {
		t.Fatalf("记录连接结果失败: %v", err)
	}
	if host.LastConnectStatus != domain.ConnectStatusFailed {
		t.Fatalf("连接结果不符: %s", host.LastConnectStatus)
	}
	if host.LastConnectError != "auth_failed" {
		t.Fatalf("错误代码不符: %s", host.LastConnectError)
	}
	if host.LastConnectedAt == nil {
		t.Fatal("应当记录连接时间")
	}
	// 成功时必须清空错误代码，避免残留上一次失败信息。
	if err := host.RecordConnectResult(domain.ConnectStatusSuccess, "auth_failed", at); err != nil {
		t.Fatalf("记录连接结果失败: %v", err)
	}
	if host.LastConnectError != "" {
		t.Fatalf("成功时应当清空错误代码，实际: %s", host.LastConnectError)
	}
}

// TestRecordConnectResultRejectsFreeText 验证连接错误字段不能写入任意文本。
func TestRecordConnectResultRejectsFreeText(t *testing.T) {
	t.Parallel()
	host, err := domain.NewExecutionHost("", "节点", "10.0.0.10", 22, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	cases := []string{
		"SSH 连接失败: 密码错误 deploy@10.0.0.10",
		"auth failed: password=secret",
		"Auth_Failed",
		strings.Repeat("a", 100),
	}
	for _, code := range cases {
		if err := host.RecordConnectResult(domain.ConnectStatusFailed, code, time.Now()); err == nil {
			t.Fatalf("应当拒绝把自由文本写入连接错误字段: %q", code)
		}
	}
}

// TestTrustHostKey 验证主机密钥信任记录。
func TestTrustHostKey(t *testing.T) {
	t.Parallel()
	host, err := domain.NewExecutionHost("", "节点", "10.0.0.10", 22, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := host.TrustHostKey("ssh-ed25519", "SHA256:abc123", time.Now()); err != nil {
		t.Fatalf("记录主机密钥信任失败: %v", err)
	}
	if host.HostKeyAlgorithm != "ssh-ed25519" || host.HostKeyFingerprint != "SHA256:abc123" {
		t.Fatalf("信任信息不符: %+v", host)
	}
	if host.HostKeyTrustedAt == nil {
		t.Fatal("应当记录信任时间")
	}
	if err := host.TrustHostKey("ssh-ed25519", "abc123", time.Now()); err == nil {
		t.Fatal("缺少 SHA256: 前缀的指纹应当被拒绝")
	}
}
