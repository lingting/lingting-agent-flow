package cli

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"
)

// ErrInteractiveUnavailable 表示当前运行环境不支持交互式输入。
//
// 本版本只支持无口令私钥认证，命令层不再收集任何凭据，因此该错误保留给
// 后续可能出现的交互式输入场景，当前不会被返回。
var ErrInteractiveUnavailable = errors.New("cli: 当前环境不支持交互式输入")

// credentialFlags 是凭据输入相关的命令行参数。
//
// 本版本只支持无口令私钥认证，连接时不需要任何凭据，因此不再注册
// --password-stdin、--passphrase-stdin 与 --ask-pass。
type credentialFlags struct{}

// addCredentialFlags 为命令注册凭据输入参数。
//
// 当前没有需要注册的参数：保留函数签名是为了让命令构造代码保持稳定，
// 并在将来需要时集中扩展。
func addCredentialFlags(_ *credentialFlags, _ *cobra.Command) {}

// credentialProviderFor 返回连接所需的凭据提供器。
//
// 本版本只支持无口令私钥认证，连接不使用任何凭据，因此始终返回 nil。
func credentialProviderFor(_ interface{}, _ credentialFlags) (any, error) {
	return nil, nil
}

// withOptionalTimeout 在超时大于 0 时附加截止时间，否则直接返回原上下文。
func withOptionalTimeout(ctx context.Context, timeout time.Duration) (
	context.Context,
	context.CancelFunc,
) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// describeAuthHint 返回认证方式的排查提示。
func describeAuthHint() string {
	return "本版本只支持无口令私钥认证，请用 --auth private_key --key <私钥路径> 配置节点"
}
