// Package sshclient 声明已认证 SSH 客户端的接口边界。
//
// 本阶段不提供任何可用的认证方式：包内不包含密码、私钥或 SSH Agent 的实现，
// 也没有可用的远程命令执行与文件传输逻辑。这里只定义边界，使后续加入认证方式时
// 不需要改动执行节点管理业务，只需注入新的 CredentialProvider 与 ClientFactory。
package sshclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// ErrNotImplemented 表示当前阶段尚未提供该能力的实现。
//
// 任何"未实现"的调用都必须返回该错误，不得伪造成功结果。
var ErrNotImplemented = errors.New("sshclient: 当前版本未提供已认证 SSH 能力")

// AuthMethod 标识一种 SSH 身份认证方式。
type AuthMethod string

// 计划支持的认证方式。
//
// 本阶段只用于标识与校验，没有任何一种被实现。
const (
	// AuthMethodPassword 表示密码认证。
	AuthMethodPassword AuthMethod = "password"
	// AuthMethodPrivateKey 表示私钥文件认证。
	AuthMethodPrivateKey AuthMethod = "private_key"
	// AuthMethodAgent 表示 SSH Agent 认证。
	AuthMethodAgent AuthMethod = "agent"
)

// Validate 校验认证方式取值。
func (m AuthMethod) Validate() error {
	switch m {
	case AuthMethodPassword, AuthMethodPrivateKey, AuthMethodAgent:
		return nil
	default:
		return fmt.Errorf("sshclient: 不支持的认证方式 %q", string(m))
	}
}

// AuthConfig 描述一次 SSH 认证的配置。
//
// 配置本身不保存任何凭据内容：凭据由 CredentialProvider 在执行期提供，
// 避免凭据进入数据库、配置文件或日志。
type AuthConfig struct {
	// Method 是认证方式。
	Method AuthMethod
	// KnownHostsPath 是主机密钥校验使用的 known_hosts 文件路径。
	//
	// 为空表示必须由实现方拒绝连接，不允许以"跳过主机校验"作为默认行为。
	KnownHostsPath string
	// Timeout 是认证阶段的超时时间，零值表示由实现方决定。
	TimeoutSeconds int
}

// Validate 校验认证配置。
func (c AuthConfig) Validate() error {
	if err := c.Method.Validate(); err != nil {
		return err
	}
	return nil
}

// Credential 是一次认证所需的凭据材料。
//
// 该结构只在内存中传递，不得写入日志、数据库或错误信息。
type Credential struct {
	// Method 是凭据对应的认证方式。
	Method AuthMethod
	// Secret 是凭据内容，具体含义由认证方式决定。
	Secret []byte
	// Passphrase 是私钥的解锁口令，仅在需要时使用。
	Passphrase []byte
}

// Clear 尽力清除内存中的凭据材料。
func (c *Credential) Clear() {
	for index := range c.Secret {
		c.Secret[index] = 0
	}
	for index := range c.Passphrase {
		c.Passphrase[index] = 0
	}
	c.Secret = nil
	c.Passphrase = nil
}

// CredentialProvider 在认证执行时提供凭据。
//
// 实现方可以来自交互式输入、操作系统密钥环或后续的凭据存储方案；
// 本阶段没有任何实现，调用方必须处理 ErrNotImplemented。
type CredentialProvider interface {
	// Method 返回该提供器支持的认证方式。
	Method() AuthMethod
	// Credential 返回本次认证使用的凭据，调用方在使用后应调用 Clear。
	Credential(ctx context.Context) (Credential, error)
}

// Endpoint 描述 SSH 连接目标。
type Endpoint struct {
	// Host 是 IP 地址或主机名。
	Host string
	// Port 是 SSH 端口。
	Port int
	// Username 是 SSH 登录用户名。
	Username string
}

// Address 返回 host:port 形式的地址。
func (e Endpoint) Address() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// Session 表示一个已认证的 SSH 会话。
//
// 本阶段没有任何实现；接口声明用于固定后续远程命令执行与文件传输的边界。
type Session interface {
	// Run 在远程执行命令并返回退出码与输出。
	Run(ctx context.Context, command string) (CommandResult, error)
	// Close 关闭会话及其底层连接。
	Close() error
}

// CommandResult 是远程命令的执行结果。
type CommandResult struct {
	// ExitCode 是远程命令的退出码。
	ExitCode int
	// Stdout 是标准输出内容。
	Stdout string
	// Stderr 是标准错误内容。
	Stderr string
}

// ClientFactory 根据认证配置与凭据提供器建立已认证会话。
//
// 新增密码、私钥或 Agent 认证时，只需提供新的 ClientFactory 实现并在组装处注入，
// 执行节点管理业务不需要改动。
type ClientFactory interface {
	// Dial 建立并认证一个 SSH 会话。
	Dial(ctx context.Context, endpoint Endpoint, config AuthConfig, provider CredentialProvider) (Session, error)
}

// UnsupportedFactory 是当前阶段的 ClientFactory 实现。
//
// 它明确返回 ErrNotImplemented，使调用方能够区分"能力缺失"与"认证失败"，
// 而不是得到伪造的成功结果。
type UnsupportedFactory struct{}

// Dial 始终返回 ErrNotImplemented。
func (UnsupportedFactory) Dial(
	_ context.Context,
	endpoint Endpoint,
	config AuthConfig,
	_ CredentialProvider,
) (Session, error) {
	return nil, fmt.Errorf(
		"%w: 无法连接到 %s（认证方式 %s）",
		ErrNotImplemented,
		endpoint.Address(),
		config.Method,
	)
}

// 编译期确认 UnsupportedFactory 满足接口约束。
var _ ClientFactory = UnsupportedFactory{}
