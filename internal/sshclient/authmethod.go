package sshclient

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
)

// authResult 是一次认证方式的准备结果。
type authResult struct {
	// methods 是交给 SSH 客户端的认证方式，至少包含一项。
	methods []ssh.AuthMethod
	// cleanup 在连接建立或失败后释放认证期间持有的资源。
	cleanup func()
}

// buildAuthMethods 按认证配置构造 SSH 认证方式。
//
// 本版本只支持本机私钥文件认证，因此不存在"凭据缺失"或"Agent 不可用"这两类
// 失败路径；失败只会来自私钥路径非法、文件不可读或私钥格式不受支持。
func buildAuthMethods(config AuthConfig) (authResult, error) {
	switch config.Method {
	case AuthMethodPrivateKey:
		return buildPrivateKeyAuth(config)
	default:
		return authResult{}, newConnectError(
			CodeInvalidConfig,
			"",
			"不支持的认证方式 "+config.Method.String()+"（本版本只支持 private_key）",
			nil,
		)
	}
}

// buildPrivateKeyAuth 构造私钥文件认证。
//
// 私钥文件只被读取，不会被复制或修改；不支持受口令保护的私钥，遇到加密私钥时
// 返回明确错误，而不是挂起等待口令输入。
func buildPrivateKeyAuth(config AuthConfig) (authResult, error) {
	path, err := NormalizePrivateKeyPath(config.PrivateKeyPath)
	if err != nil {
		return authResult{}, err
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
	signer, parseErr := parsePrivateKey(data)
	clearBytes(data)
	if parseErr != nil {
		return authResult{}, parseErr
	}
	return authResult{
		methods: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		cleanup: func() {},
	}, nil
}

// parsePrivateKey 解析未加密的私钥。
//
// 受口令保护的私钥不在支持范围内：返回明确的不支持说明，而不是要求交互输入口令。
func parsePrivateKey(data []byte) (ssh.Signer, error) {
	signer, err := ssh.ParsePrivateKey(data)
	if err == nil {
		return signer, nil
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return nil, newConnectError(
			CodePrivateKey,
			"",
			"不支持受口令保护的私钥，请改用无口令私钥文件",
			err,
		)
	}
	return nil, newConnectError(
		CodePrivateKey,
		"",
		"私钥格式不受支持: "+describeKeyError(err),
		err,
	)
}

// describeFileError 把文件错误转换为不含敏感内容的说明。
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

// ctxErr 返回上下文错误，未取消时返回 nil。
//
// 该辅助函数被远程命令执行与文件传输共同使用，因此保留在本包内。
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
