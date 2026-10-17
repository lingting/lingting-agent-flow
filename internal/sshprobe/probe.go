// Package sshprobe 通过 TCP 探测目标地址是否提供 SSH 服务。
//
// 本包只验证网络可达性与 SSH 协议标识，不代表已经完成身份认证，也不提供
// 远程命令执行或文件传输能力。已认证的 SSH 会话由后续的 SSH 客户端实现负责，
// 边界通过本包的 Authenticator、SessionFactory 等接口声明。
package sshprobe

import (
	"context"
	"fmt"
	"time"
)

// 探测默认参数。
const (
	// DefaultTimeout 是未显式指定时的整体探测超时时间。
	DefaultTimeout = 5 * time.Second
	// DefaultMaxBannerBytes 是协议标识的最大读取长度。
	//
	// RFC 4253 规定标识行不超过 255 字节（含 CRLF），服务端还可能先发送若干
	// 额外的行，因此这里给出足够但有限的上限，避免异常服务端造成无限读取。
	DefaultMaxBannerBytes = 8192
)

// ErrorCode 是适合程序处理的探测错误类别。
type ErrorCode string

// 探测结果类别。
const (
	// CodeOK 表示已识别到有效的 SSH 协议标识。
	CodeOK ErrorCode = "ok"
	// CodeInvalidTarget 表示主机地址、端口或超时配置非法。
	CodeInvalidTarget ErrorCode = "invalid_target"
	// CodeUnreachable 表示无法建立 TCP 连接，例如连接被拒绝或域名无法解析。
	CodeUnreachable ErrorCode = "unreachable"
	// CodeTimeout 表示连接或读取协议标识超时。
	CodeTimeout ErrorCode = "timeout"
	// CodeNotSSH 表示端口可连接，但未返回符合 SSH 格式的协议标识。
	CodeNotSSH ErrorCode = "not_ssh"
	// CodeCancelled 表示调用方取消了探测。
	CodeCancelled ErrorCode = "cancelled"
)

// String 返回错误类别的字符串形式。
func (c ErrorCode) String() string {
	return string(c)
}

// Result 是一次 SSH 端点探测的结果。
//
// 该结果只描述网络与协议标识，不含任何认证信息；Authenticated 恒为 false，
// 用于避免调用方把"SSH 服务可达"误解为"已经登录成功"。
type Result struct {
	// Target 是被探测的 host:port 地址。
	Target string
	// Reachable 表示 TCP 连接是否建立成功。
	Reachable bool
	// SSHDetected 表示是否收到符合 SSH 协议格式的标识行。
	SSHDetected bool
	// Authenticated 表示是否已完成身份认证，本阶段恒为 false。
	Authenticated bool
	// Duration 是探测耗时。
	Duration time.Duration
	// ErrorCode 是探测结果类别，成功时为 CodeOK。
	ErrorCode ErrorCode
	// Message 是面向用户的诊断信息。
	Message string
}

// Succeeded 判断探测是否识别到有效的 SSH 服务。
func (r Result) Succeeded() bool {
	return r.SSHDetected && r.ErrorCode == CodeOK
}

// summary 返回成功探测的固定说明。
//
// 该文案刻意只描述协议标识，不包含"登录成功"或"环境已验证"之类的表述。
const successMessage = "SSH 服务可达，协议标识有效；尚未执行身份认证。"

// ProbeError 是探测失败的结构化错误。
type ProbeError struct {
	// Code 是错误类别。
	Code ErrorCode
	// Target 是被探测的地址。
	Target string
	// Detail 是可诊断的失败原因。
	Detail string
	// Cause 是底层错误，可通过 errors.Is / errors.As 判断。
	Cause error
}

// Error 实现 error 接口。
func (e *ProbeError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("SSH 探测失败(%s): %s", e.Code, e.Target)
	}
	return fmt.Sprintf("SSH 探测失败(%s): %s: %s", e.Code, e.Target, e.Detail)
}

// Unwrap 暴露底层错误。
func (e *ProbeError) Unwrap() error {
	return e.Cause
}

// newError 构造探测错误。
func newError(code ErrorCode, target, detail string, cause error) *ProbeError {
	return &ProbeError{Code: code, Target: target, Detail: detail, Cause: cause}
}

// cancelledError 返回上下文取消导致的探测错误。
func cancelledError(ctx context.Context, target string, cause error) *ProbeError {
	if ctx.Err() == context.DeadlineExceeded {
		return newError(CodeTimeout, target, "探测超时", cause)
	}
	return newError(CodeCancelled, target, "探测被取消", cause)
}
