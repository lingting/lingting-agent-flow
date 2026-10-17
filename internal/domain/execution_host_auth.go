package domain

import (
	"strings"
	"unicode"
)

// AuthMethod 标识执行节点使用的 SSH 身份认证方式。
//
// 该类型只描述认证方式的种类，不保存任何凭据：密码、私钥口令等敏感材料只能
// 通过内存中的凭据提供器传递，禁止写入数据库、配置文件或日志。
type AuthMethod string

// 受支持的 SSH 认证方式。
const (
	// AuthMethodAgent 表示通过标准 SSH Agent 获取签名身份。
	//
	// 这是新建节点的默认认证方式：它不需要在配置中保存任何机密材料。
	AuthMethodAgent AuthMethod = "agent"
	// AuthMethodPrivateKey 表示使用本机私钥文件认证。
	AuthMethodPrivateKey AuthMethod = "private_key"
	// AuthMethodPassword 表示使用密码认证。
	//
	// 密码默认不持久化，只在单次连接过程中通过凭据提供器在内存中传递。
	AuthMethodPassword AuthMethod = "password"
)

// String 返回认证方式的字符串形式。
func (m AuthMethod) String() string {
	return string(m)
}

// Validate 校验认证方式是否受支持。
func (m AuthMethod) Validate() error {
	switch m {
	case AuthMethodAgent, AuthMethodPrivateKey, AuthMethodPassword:
		return nil
	default:
		return invalidField("ExecutionHost", "auth_method", "不支持的认证方式 "+string(m))
	}
}

// ParseAuthMethod 解析命令行或配置中给出的认证方式名称。
func ParseAuthMethod(raw string) (AuthMethod, error) {
	method := AuthMethod(strings.ToLower(strings.TrimSpace(raw)))
	if err := method.Validate(); err != nil {
		return "", err
	}
	return method, nil
}

// ConnectStatus 表示最近一次连接测试的结果类别。
//
// 空值表示尚未执行过连接测试；这里只保存结果类别，不保存失败详情，
// 避免敏感信息经由诊断字段进入数据库。
type ConnectStatus string

// 连接结果类别。
const (
	// ConnectStatusUnknown 表示尚未执行过连接测试。
	ConnectStatusUnknown ConnectStatus = ""
	// ConnectStatusSuccess 表示最近一次连接测试通过。
	ConnectStatusSuccess ConnectStatus = "success"
	// ConnectStatusFailed 表示最近一次连接测试失败。
	ConnectStatusFailed ConnectStatus = "failed"
)

// String 返回连接结果类别的字符串形式。
func (s ConnectStatus) String() string {
	return string(s)
}

// Validate 校验连接结果类别。
func (s ConnectStatus) Validate() error {
	switch s {
	case ConnectStatusUnknown, ConnectStatusSuccess, ConnectStatusFailed:
		return nil
	default:
		return invalidField("ExecutionHost", "last_connect_status", "未知的连接结果类别 "+string(s))
	}
}

// maxConnectErrorCodeLength 是连接错误代码的最大长度。
const maxConnectErrorCodeLength = 64

// validateConnectErrorCode 校验连接错误代码。
//
// 只允许小写字母、数字与下划线，从结构上避免把包含主机、用户名或凭据的错误
// 消息整体写入数据库。
func validateConnectErrorCode(code string) error {
	if code == "" {
		return nil
	}
	if len(code) > maxConnectErrorCodeLength {
		return invalidField("ExecutionHost", "last_connect_error", "连接错误代码过长")
	}
	for _, char := range code {
		if unicode.IsLower(char) || unicode.IsDigit(char) || char == '_' {
			continue
		}
		return invalidField(
			"ExecutionHost",
			"last_connect_error",
			"连接错误代码只能包含小写字母、数字与下划线",
		)
	}
	return nil
}

// validatePrivateKeyPath 校验本机私钥文件路径。
//
// 这里只校验书写形式，文件是否存在、格式是否合法由连接阶段判断；
// AgentFlow 只读取该文件，不会复制或修改其内容。
func validatePrivateKeyPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if len(path) > 4096 {
		return invalidField("ExecutionHost", "private_key_path", "私钥文件路径过长")
	}
	for _, char := range path {
		if unicode.IsControl(char) {
			return invalidField("ExecutionHost", "private_key_path", "私钥文件路径不能包含控制字符")
		}
	}
	return nil
}

// validateHostKeyFingerprint 校验主机密钥指纹的书写形式。
func validateHostKeyFingerprint(fingerprint string) error {
	if fingerprint == "" {
		return nil
	}
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		return invalidField("ExecutionHost", "host_key_fingerprint", "主机密钥指纹必须以 SHA256: 开头")
	}
	return nil
}

// validateAuthConfig 校验认证方式与私钥路径的组合是否自洽。
func validateAuthConfig(host ExecutionHost) error {
	if err := host.AuthMethod.Validate(); err != nil {
		return err
	}
	if err := validatePrivateKeyPath(host.PrivateKeyPath); err != nil {
		return err
	}
	if host.AuthMethod == AuthMethodPrivateKey && strings.TrimSpace(host.PrivateKeyPath) == "" {
		return invalidField("ExecutionHost", "private_key_path", "私钥认证必须指定私钥文件路径")
	}
	return nil
}

// HostOption 在构造执行节点时覆盖可选配置。
//
// 使用函数选项而不是扩展 NewExecutionHost 的参数列表，使既有调用点保持兼容。
type HostOption func(*ExecutionHost)

// WithAuth 指定认证方式与私钥文件路径。
//
// 该方法只写入非机密的认证配置；密码与私钥口令永远不会进入实体。
func WithAuth(method AuthMethod, privateKeyPath string) HostOption {
	return func(host *ExecutionHost) {
		host.AuthMethod = method
		host.PrivateKeyPath = strings.TrimSpace(privateKeyPath)
	}
}
