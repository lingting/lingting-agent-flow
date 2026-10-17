package sshclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNoCredential 表示当前执行环境没有可用的凭据。
//
// 非交互执行遇到该错误时必须直接失败，不得挂起等待输入。
var ErrNoCredential = errors.New("sshclient: 没有可用的凭据")

// ErrCredentialCancelled 表示调用方放弃了凭据输入。
var ErrCredentialCancelled = errors.New("sshclient: 凭据输入被取消")

// AuthMethod 标识一种 SSH 身份认证方式。
type AuthMethod string

// 受支持的认证方式。
const (
	// AuthMethodPassword 表示密码认证。
	AuthMethodPassword AuthMethod = "password"
	// AuthMethodPrivateKey 表示本机私钥文件认证。
	AuthMethodPrivateKey AuthMethod = "private_key"
	// AuthMethodAgent 表示标准 SSH Agent 认证。
	AuthMethodAgent AuthMethod = "agent"
)

// String 返回认证方式的字符串形式。
func (m AuthMethod) String() string {
	return string(m)
}

// Validate 校验认证方式取值。
func (m AuthMethod) Validate() error {
	switch m {
	case AuthMethodPassword, AuthMethodPrivateKey, AuthMethodAgent:
		return nil
	default:
		return fmt.Errorf("sshclient: 不支持的认证方式 %q", string(m))
	}
}

// AuthConfig 描述一次 SSH 认证的非机密配置。
//
// 配置本身不保存任何凭据内容：密码与私钥口令由 CredentialProvider 在执行期提供，
// 避免凭据进入数据库、配置文件或日志。
type AuthConfig struct {
	// Method 是认证方式。
	Method AuthMethod
	// PrivateKeyPath 是本机私钥文件路径，仅私钥认证使用。
	PrivateKeyPath string
	// AgentSocket 是 SSH Agent 套接字地址，为空时使用环境变量 SSH_AUTH_SOCK。
	AgentSocket string
}

// Validate 校验认证配置的自洽性。
func (c AuthConfig) Validate() error {
	if err := c.Method.Validate(); err != nil {
		return err
	}
	if c.Method == AuthMethodPrivateKey && strings.TrimSpace(c.PrivateKeyPath) == "" {
		return fmt.Errorf("sshclient: 私钥认证必须指定私钥文件路径")
	}
	return nil
}

// Credential 是一次认证所需的凭据材料。
//
// 该结构只在内存中传递，不得写入日志、数据库或错误信息；使用后应调用 Clear。
type Credential struct {
	// Method 是凭据对应的认证方式。
	Method AuthMethod
	// Secret 是凭据内容，密码认证时为密码。
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

// Empty 判断凭据是否没有任何内容。
func (c Credential) Empty() bool {
	return len(c.Secret) == 0 && len(c.Passphrase) == 0
}

// CredentialProvider 在认证执行时提供凭据。
//
// 实现方可以来自交互式隐藏输入、操作系统密钥环或后续的凭据存储方案。
// 未实现该方法时返回 ErrNoCredential，使非交互执行立即失败而不是挂起。
type CredentialProvider interface {
	// Method 返回该提供器支持的认证方式。
	Method() AuthMethod
	// Credential 返回本次认证使用的凭据，调用方在使用后应调用 Clear。
	Credential(ctx context.Context) (Credential, error)
}

// PasswordProvider 是从内存中取密码的 CredentialProvider。
//
// 它用于自动化场景：调用方自行从安全的凭据存储取得密码后传入，
// 密码不会被 AgentFlow 持久化。
type PasswordProvider struct {
	// Password 是内存中的密码内容，读取时复制一份交给连接实现。
	Password []byte
}

// Method 返回密码认证方式。
func (p PasswordProvider) Method() AuthMethod {
	return AuthMethodPassword
}

// Credential 返回密码凭据的副本。
func (p PasswordProvider) Credential(ctx context.Context) (Credential, error) {
	if err := ctxErr(ctx); err != nil {
		return Credential{}, err
	}
	if len(p.Password) == 0 {
		return Credential{}, fmt.Errorf("%w: 未提供密码", ErrNoCredential)
	}
	return Credential{Method: AuthMethodPassword, Secret: cloneBytes(p.Password)}, nil
}

// PassphraseProvider 是为私钥提供解锁口令的 CredentialProvider。
type PassphraseProvider struct {
	// Passphrase 是内存中的私钥口令，读取时复制一份交给连接实现。
	Passphrase []byte
}

// Method 返回私钥认证方式。
func (p PassphraseProvider) Method() AuthMethod {
	return AuthMethodPrivateKey
}

// Credential 返回私钥口令凭据的副本。
func (p PassphraseProvider) Credential(ctx context.Context) (Credential, error) {
	if err := ctxErr(ctx); err != nil {
		return Credential{}, err
	}
	if len(p.Passphrase) == 0 {
		return Credential{}, fmt.Errorf("%w: 未提供私钥口令", ErrNoCredential)
	}
	return Credential{Method: AuthMethodPrivateKey, Passphrase: cloneBytes(p.Passphrase)}, nil
}

// ctxErr 返回上下文错误，未取消时返回 nil。
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// cloneBytes 复制字节切片，避免调用方与提供器共享同一份内存。
func cloneBytes(source []byte) []byte {
	if len(source) == 0 {
		return nil
	}
	cloned := make([]byte, len(source))
	copy(cloned, source)
	return cloned
}
