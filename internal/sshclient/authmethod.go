package sshclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// authResult 是一次认证方式的准备结果。
type authResult struct {
	// methods 是交给 SSH 客户端的认证方式，至少包含一项。
	methods []ssh.AuthMethod
	// cleanup 在连接建立或失败后释放认证期间持有的资源。
	cleanup func()
}

// buildAuthMethods 按认证配置与凭据构造 SSH 认证方式。
//
// 每种认证方式都有独立的失败类别，避免把私钥不可读、Agent 不可用或凭据缺失
// 包装成含糊的"连接失败"。
func buildAuthMethods(
	ctx context.Context,
	config AuthConfig,
	provider CredentialProvider,
) (authResult, error) {
	switch config.Method {
	case AuthMethodPassword:
		return buildPasswordAuth(ctx, provider)
	case AuthMethodPrivateKey:
		return buildPrivateKeyAuth(ctx, config, provider)
	case AuthMethodAgent:
		return buildAgentAuth(config)
	default:
		return authResult{}, newConnectError(
			CodeInvalidConfig,
			"",
			"不支持的认证方式 "+config.Method.String(),
			nil,
		)
	}
}

// buildPasswordAuth 构造密码认证。
//
// 密码只在内存中传递，不会出现在错误信息、日志或数据库里。
func buildPasswordAuth(ctx context.Context, provider CredentialProvider) (authResult, error) {
	credential, err := requestCredential(ctx, provider, AuthMethodPassword)
	if err != nil {
		return authResult{}, err
	}
	secret := string(credential.Secret)
	credential.Clear()
	return authResult{
		methods: []ssh.AuthMethod{ssh.Password(secret)},
		cleanup: func() {},
	}, nil
}

// buildPrivateKeyAuth 构造私钥文件认证。
//
// 私钥文件只被读取，不会被复制或修改；受口令保护的私钥需要提供器给出 passphrase。
func buildPrivateKeyAuth(
	ctx context.Context,
	config AuthConfig,
	provider CredentialProvider,
) (authResult, error) {
	path := strings.TrimSpace(config.PrivateKeyPath)
	if path == "" {
		return authResult{}, newConnectError(
			CodeInvalidConfig,
			"",
			"私钥认证必须指定私钥文件路径",
			nil,
		)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return authResult{}, newConnectError(
			CodePrivateKey,
			"",
			fmt.Sprintf("无法读取私钥文件 %s: %s", path, describeFileError(err)),
			err,
		)
	}
	signer, err := parsePrivateKey(data, provider, ctx)
	clearBytes(data)
	if err != nil {
		return authResult{}, err
	}
	return authResult{
		methods: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		cleanup: func() {},
	}, nil
}

// parsePrivateKey 解析私钥，必要时用提供器给出的口令解锁。
func parsePrivateKey(
	data []byte,
	provider CredentialProvider,
	ctx context.Context,
) (ssh.Signer, error) {
	signer, err := ssh.ParsePrivateKey(data)
	if err == nil {
		return signer, nil
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil, newConnectError(
			CodePrivateKey,
			"",
			"私钥格式不受支持: "+describeKeyError(err),
			err,
		)
	}
	credential, credentialErr := requestCredential(ctx, provider, AuthMethodPrivateKey)
	if credentialErr != nil {
		return nil, newConnectError(
			CodePrivateKey,
			"",
			"私钥受口令保护，但没有可用的口令",
			credentialErr,
		)
	}
	passphrase := credential.Passphrase
	signer, err = ssh.ParsePrivateKeyWithPassphrase(data, passphrase)
	clearBytes(passphrase)
	credential.Clear()
	if err != nil {
		return nil, newConnectError(
			CodePrivateKey,
			"",
			"私钥口令错误或私钥不可用: "+describeKeyError(err),
			err,
		)
	}
	return signer, nil
}

// buildAgentAuth 构造 SSH Agent 认证。
func buildAgentAuth(config AuthConfig) (authResult, error) {
	socket := strings.TrimSpace(config.AgentSocket)
	if socket == "" {
		socket = strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK"))
	}
	if socket == "" {
		return authResult{}, newConnectError(
			CodeAgentUnavailable,
			"",
			"未找到 SSH Agent（环境变量 SSH_AUTH_SOCK 为空）",
			nil,
		)
	}
	conn, err := net.DialTimeout("unix", socket, DefaultAgentDialTimeout)
	if err != nil {
		return authResult{}, newConnectError(
			CodeAgentUnavailable,
			"",
			"无法连接 SSH Agent: "+describeFileError(err),
			err,
		)
	}
	agentClient := agent.NewClient(conn)
	identities, err := agentClient.List()
	if err != nil {
		_ = conn.Close()
		return authResult{}, newConnectError(
			CodeAgentUnavailable,
			"",
			"读取 SSH Agent 身份列表失败: "+describeKeyError(err),
			err,
		)
	}
	if len(identities) == 0 {
		_ = conn.Close()
		return authResult{}, newConnectError(
			CodeAgentUnavailable,
			"",
			"SSH Agent 中没有可用身份",
			nil,
		)
	}
	return authResult{
		methods: []ssh.AuthMethod{ssh.PublicKeysCallback(agentClient.Signers)},
		cleanup: func() {
			_ = conn.Close()
		},
	}, nil
}

// DefaultAgentDialTimeout 是连接 SSH Agent 的超时时间。
const DefaultAgentDialTimeout = 5 * time.Second

// requestCredential 从提供器获取凭据并校验其与认证方式匹配。
func requestCredential(
	ctx context.Context,
	provider CredentialProvider,
	method AuthMethod,
) (Credential, error) {
	if provider == nil {
		return Credential{}, newConnectError(
			CodeNoCredential,
			"",
			"未提供凭据提供器，无法完成"+methodLabel(method)+"认证",
			ErrNoCredential,
		)
	}
	if declared := provider.Method(); declared != method {
		return Credential{}, newConnectError(
			CodeInvalidConfig,
			"",
			fmt.Sprintf("凭据提供器支持 %s，与认证方式 %s 不匹配", declared, method),
			nil,
		)
	}
	credential, err := provider.Credential(ctx)
	if err != nil {
		code := CodeNoCredential
		if errors.Is(err, ErrCredentialCancelled) || errors.Is(err, context.Canceled) {
			code = CodeCancelled
		}
		return Credential{}, newConnectError(
			code,
			"",
			"获取"+methodLabel(method)+"凭据失败: "+err.Error(),
			err,
		)
	}
	return credential, nil
}

// methodLabel 返回认证方式的中文名称。
func methodLabel(method AuthMethod) string {
	switch method {
	case AuthMethodPassword:
		return "密码"
	case AuthMethodPrivateKey:
		return "私钥"
	case AuthMethodAgent:
		return "SSH Agent"
	default:
		return string(method)
	}
}

// describeFileError 把文件与套接字错误转换为不含敏感内容的说明。
func describeFileError(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "文件不存在"
	case errors.Is(err, os.ErrPermission):
		return "没有访问权限"
	default:
		return "无法访问"
	}
}

// describeKeyError 把密钥相关错误转换为可诊断但不含敏感内容的说明。
func describeKeyError(err error) string {
	if err == nil {
		return ""
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return "私钥受口令保护"
	}
	message := err.Error()
	if len(message) > 160 {
		message = message[:160]
	}
	return message
}

// clearBytes 尽力清除内存中的敏感字节。
func clearBytes(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
