package sshclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lingting/agentflow/internal/sshclient"
)

// TestUnsupportedFactoryReportsNotImplemented 验证当前阶段不存在伪造的认证成功。
func TestUnsupportedFactoryReportsNotImplemented(t *testing.T) {
	factory := sshclient.UnsupportedFactory{}
	session, err := factory.Dial(
		context.Background(),
		sshclient.Endpoint{Host: "10.0.0.11", Port: 22, Username: "deploy"},
		sshclient.AuthConfig{Method: sshclient.AuthMethodPrivateKey},
		nil,
	)
	if err == nil {
		t.Fatal("未实现认证时应当返回错误")
	}
	if !errors.Is(err, sshclient.ErrNotImplemented) {
		t.Fatalf("应当返回未实现错误，实际: %v", err)
	}
	if session != nil {
		t.Fatal("未实现认证时不得返回会话")
	}
}

// TestAuthMethodValidate 验证认证方式取值约束。
func TestAuthMethodValidate(t *testing.T) {
	for _, method := range []sshclient.AuthMethod{
		sshclient.AuthMethodPassword,
		sshclient.AuthMethodPrivateKey,
		sshclient.AuthMethodAgent,
	} {
		if err := method.Validate(); err != nil {
			t.Fatalf("认证方式 %s 应当受支持: %v", method, err)
		}
	}
	if err := sshclient.AuthMethod("kerberos").Validate(); err == nil {
		t.Fatal("未知认证方式应当返回错误")
	}
}

// TestCredentialClear 验证凭据材料可以被清除。
func TestCredentialClear(t *testing.T) {
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

// TestEndpointAddress 验证地址拼接支持 IPv6。
func TestEndpointAddress(t *testing.T) {
	endpoint := sshclient.Endpoint{Host: "2001:db8::1", Port: 2222}
	if got := endpoint.Address(); got != "[2001:db8::1]:2222" {
		t.Fatalf("IPv6 地址拼接不符: %s", got)
	}
}
