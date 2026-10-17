package sshclient

import (
	"errors"
	"fmt"
)

// ErrorCode 是适合程序处理的连接错误类别。
//
// 用户名、地址与端口相同不代表认证成功，因此连接失败必须按阶段区分：
// TCP 不可达、SSH 握手失败、主机密钥不可信、认证失败是四类不同的问题。
type ErrorCode string

// 连接错误类别。
const (
	// CodeOK 表示连接与认证成功。
	CodeOK ErrorCode = "ok"
	// CodeInvalidConfig 表示连接配置或凭据配置非法。
	CodeInvalidConfig ErrorCode = "invalid_config"
	// CodeUnreachable 表示无法建立 TCP 连接。
	CodeUnreachable ErrorCode = "unreachable"
	// CodeHandshakeFailed 表示 TCP 已连接但 SSH 协议握手失败。
	CodeHandshakeFailed ErrorCode = "handshake_failed"
	// CodeHostKeyUnknown 表示主机密钥未知，需要用户确认信任。
	CodeHostKeyUnknown ErrorCode = "host_key_unknown"
	// CodeHostKeyMismatch 表示主机密钥与已信任的记录不一致。
	CodeHostKeyMismatch ErrorCode = "host_key_mismatch"
	// CodeHostKeyRevoked 表示主机密钥已被标记为吊销。
	CodeHostKeyRevoked ErrorCode = "host_key_revoked"
	// CodeAuthFailed 表示身份认证失败。
	CodeAuthFailed ErrorCode = "auth_failed"
	// CodeNoCredential 表示没有可用的凭据。
	CodeNoCredential ErrorCode = "no_credential"
	// CodePrivateKey 表示私钥文件不可读、格式不支持或口令错误。
	CodePrivateKey ErrorCode = "private_key_error"
	// CodeAgentUnavailable 表示 SSH Agent 不可用或没有可用身份。
	CodeAgentUnavailable ErrorCode = "agent_unavailable"
	// CodeTimeout 表示连接或认证超时。
	CodeTimeout ErrorCode = "timeout"
	// CodeCancelled 表示调用方取消。
	CodeCancelled ErrorCode = "cancelled"
)

// String 返回错误类别的字符串形式。
func (c ErrorCode) String() string {
	return string(c)
}

// ConnectError 是带阶段信息的连接错误。
//
// 该错误只携带分类代码与可读说明，不携带密码、私钥内容或私钥口令。
type ConnectError struct {
	// Code 是错误类别。
	Code ErrorCode
	// Address 是连接目标地址，形如 host:port。
	Address string
	// Detail 是可读的失败原因，不包含凭据内容。
	Detail string
	// HostKey 在主机密钥相关的失败中给出可核验信息。
	HostKey *HostKeyInfo
	// Cause 是底层错误，可通过 errors.Is / errors.As 判断。
	Cause error
}

// Error 实现 error 接口。
func (e *ConnectError) Error() string {
	detail := e.Detail
	if detail == "" {
		detail = defaultDetail(e.Code)
	}
	return fmt.Sprintf("SSH 连接失败(%s): %s: %s", e.Code, e.Address, detail)
}

// Unwrap 暴露底层错误。
func (e *ConnectError) Unwrap() error {
	return e.Cause
}

// Is 使同类错误可以通过 errors.Is 比较错误类别。
func (e *ConnectError) Is(target error) bool {
	var other *ConnectError
	if !errors.As(target, &other) {
		return false
	}
	return other.Code == e.Code
}

// defaultDetail 返回错误类别的默认说明。
func defaultDetail(code ErrorCode) string {
	switch code {
	case CodeUnreachable:
		return "无法连接目标地址"
	case CodeHandshakeFailed:
		return "SSH 协议握手失败"
	case CodeHostKeyUnknown:
		return "主机密钥未知，需要确认后信任"
	case CodeHostKeyMismatch:
		return "主机密钥与已信任记录不一致"
	case CodeHostKeyRevoked:
		return "主机密钥已被标记为吊销"
	case CodeAuthFailed:
		return "身份认证失败"
	case CodeNoCredential:
		return "没有可用的凭据"
	case CodePrivateKey:
		return "私钥不可用"
	case CodeAgentUnavailable:
		return "SSH Agent 不可用"
	case CodeTimeout:
		return "连接超时"
	case CodeCancelled:
		return "连接被取消"
	default:
		return "连接失败"
	}
}

// newConnectError 构造连接错误。
func newConnectError(code ErrorCode, address, detail string, cause error) *ConnectError {
	return &ConnectError{Code: code, Address: address, Detail: detail, Cause: cause}
}

// withAddress 为尚未关联地址的连接错误补上目标地址。
//
// 认证方式准备阶段发生在建立连接之前，此时错误里没有地址；
// 补上地址后，所有连接错误都能定位到具体节点。
func withAddress(err error, address string) error {
	var target *ConnectError
	if !errors.As(err, &target) {
		return err
	}
	if target.Address != "" {
		return err
	}
	target.Address = address
	return err
}

// ErrorCodeOf 提取错误的分类代码，非连接错误返回空字符串。
func ErrorCodeOf(err error) ErrorCode {
	var target *ConnectError
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}
