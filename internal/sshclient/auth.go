package sshclient

import (
	"fmt"
	"strings"
)

// AuthMethod 标识一种 SSH 身份认证方式。
//
// 本版本只支持本机私钥文件认证：SSH Agent 与密码认证已移除，凭据既不需要
// 也不可能通过环境变量或内存提供器进入连接流程。
type AuthMethod string

// 受支持的认证方式。
const (
	// AuthMethodPrivateKey 表示本机私钥文件认证。
	AuthMethodPrivateKey AuthMethod = "private_key"
)

// String 返回认证方式的字符串形式。
func (m AuthMethod) String() string {
	return string(m)
}

// Validate 校验认证方式取值。
func (m AuthMethod) Validate() error {
	if m == AuthMethodPrivateKey {
		return nil
	}
	return fmt.Errorf("sshclient: 不支持的认证方式 %q（本版本只支持 %s）", string(m), AuthMethodPrivateKey)
}

// AuthConfig 描述一次 SSH 认证的非机密配置。
//
// 配置只保存本机私钥文件路径，不保存任何凭据内容；私钥文件只被读取，
// 不会被复制、修改或写入数据库与日志。
type AuthConfig struct {
	// Method 是认证方式，只允许 AuthMethodPrivateKey。
	Method AuthMethod
	// PrivateKeyPath 是本机私钥文件路径。
	PrivateKeyPath string
}

// Validate 校验认证配置的自洽性。
func (c AuthConfig) Validate() error {
	if err := c.Method.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.PrivateKeyPath) == "" {
		return fmt.Errorf("sshclient: 私钥认证必须指定私钥文件路径")
	}
	return nil
}
