// Package docker 通过已认证的 SSH 连接在远程 Linux 机器上操作 Docker。
//
// 本包只负责远程 Docker 的命令构造、结果解析与错误分类：
//   - 所有命令通过 sshclient 的远程执行接口运行，不开放 Docker TCP 端口，
//     不创建反向隧道，也不要求本机访问远程 Docker Socket；
//   - 每个参数都经过 POSIX shell 转义，用户输入不会被拼进 shell 字符串；
//   - 预期输出使用机器可解析的 JSON 格式，构建日志写入远程文件并只回传有界摘要；
//   - 超时或连接中断时结果状态标记为"不确定"，调用方必须先核验远程状态再决定重试。
//
// 本包不包含容器生命周期业务规则，那部分位于 domain 与 app 层。
package docker

import (
	"errors"
	"fmt"
	"strings"
)

// ErrorCode 是远程 Docker 操作的可诊断错误类别。
//
// 不同原因必须区分：Docker 未安装、服务不可用、权限不足与 SSH 认证失败
// 对应完全不同的处理方式，不能用同一个"失败"掩盖。
type ErrorCode string

// 远程 Docker 错误类别。
const (
	// CodeOK 表示操作成功。
	CodeOK ErrorCode = "ok"
	// CodeSSHUnavailable 表示 SSH 连接不可用，远程命令未执行或结果未知。
	CodeSSHUnavailable ErrorCode = "ssh_unavailable"
	// CodeCLIMissing 表示远程机器上没有 docker 命令。
	CodeCLIMissing ErrorCode = "docker_cli_missing"
	// CodeDaemonUnavailable 表示 Docker CLI 存在但守护进程不可访问。
	CodeDaemonUnavailable ErrorCode = "docker_daemon_unavailable"
	// CodePermissionDenied 表示当前 SSH 用户没有使用 Docker 的权限。
	CodePermissionDenied ErrorCode = "docker_permission_denied"
	// CodeVersionUnsupported 表示 Docker 版本低于所需能力。
	CodeVersionUnsupported ErrorCode = "docker_version_unsupported"
	// CodeDiskInsufficient 表示远程存储空间不足。
	CodeDiskInsufficient ErrorCode = "disk_insufficient"
	// CodeInvalidArgument 表示命令参数不合法。
	CodeInvalidArgument ErrorCode = "invalid_argument"
	// CodeBuildFailed 表示镜像构建失败。
	CodeBuildFailed ErrorCode = "build_failed"
	// CodeImageMissing 表示构建后未找到预期镜像。
	CodeImageMissing ErrorCode = "image_missing"
	// CodeRunContractUnsupported 表示镜像不满足常驻运行契约。
	CodeRunContractUnsupported ErrorCode = "run_contract_unsupported"
	// CodeContainerNotFound 表示目标容器不存在。
	CodeContainerNotFound ErrorCode = "container_not_found"
	// CodeContainerConflict 表示容器名称冲突或容器已存在。
	CodeContainerConflict ErrorCode = "container_conflict"
	// CodeOwnershipMismatch 表示容器标签与登记记录不一致。
	CodeOwnershipMismatch ErrorCode = "ownership_mismatch"
	// CodeDetached 表示容器已脱离 AgentFlow 管理，拒绝自动操作。
	CodeDetached ErrorCode = "container_detached"
	// CodeStateUncertain 表示远程状态无法确认，必须先核验再重试。
	CodeStateUncertain ErrorCode = "remote_state_uncertain"
	// CodeCommandFailed 表示远程命令以非零退出码结束。
	CodeCommandFailed ErrorCode = "command_failed"
)

// String 返回错误类别的字符串形式。
func (c ErrorCode) String() string {
	return string(c)
}

// Error 是带类别与操作上下文的远程 Docker 错误。
//
// 该错误不携带凭据内容；Detail 只包含命令名、退出码与经过裁剪的远程输出片段。
type Error struct {
	// Code 是错误类别。
	Code ErrorCode
	// Op 是失败的操作名，例如 "docker build"。
	Op string
	// Detail 是可读的失败原因。
	Detail string
	// Cause 是底层错误，可通过 errors.Is 判断超时或取消。
	Cause error
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		return fmt.Sprintf("远程 Docker 操作失败(%s): %s", e.Code, e.Op)
	}
	return fmt.Sprintf("远程 Docker 操作失败(%s): %s: %s", e.Code, e.Op, detail)
}

// Unwrap 暴露底层错误。
func (e *Error) Unwrap() error {
	return e.Cause
}

// Is 使同类错误可以通过 errors.Is 比较错误类别。
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return other.Code == e.Code
}

// ErrorCodeOf 从错误链中提取远程 Docker 错误类别，未分类时返回 CodeOK。
func ErrorCodeOf(err error) ErrorCode {
	if err == nil {
		return CodeOK
	}
	var structured *Error
	if errors.As(err, &structured) {
		return structured.Code
	}
	return CodeCommandFailed
}

// newError 构造远程 Docker 错误。
func newError(code ErrorCode, op, detail string, cause error) *Error {
	return &Error{Code: code, Op: op, Detail: detail, Cause: cause}
}

// ClassifyDockerMessage 依据远程输出判断失败原因。
//
// Docker 的错误消息在 CLI 与服务端之间稳定，因此可以按消息特征分类；
// 无法识别时返回 CodeCommandFailed，由调用方按普通命令失败处理。
func ClassifyDockerMessage(output string) ErrorCode {
	message := strings.ToLower(output)
	switch {
	case strings.Contains(message, "permission denied while trying to connect"),
		strings.Contains(message, "permission denied"),
		strings.Contains(message, "got permission denied"):
		return CodePermissionDenied
	case strings.Contains(message, "command not found"),
		strings.Contains(message, "docker: not found"):
		return CodeCLIMissing
	case strings.Contains(message, "cannot connect to the docker daemon"),
		strings.Contains(message, "is the docker daemon running"),
		strings.Contains(message, "error during connect"),
		strings.Contains(message, "docker.sock: connect"):
		return CodeDaemonUnavailable
	case strings.Contains(message, "no such container"),
		strings.Contains(message, "no such object"),
		strings.Contains(message, "no such image"):
		return CodeContainerNotFound
	case strings.Contains(message, "is already in use by container"),
		strings.Contains(message, "conflict. the container name"):
		return CodeContainerConflict
	default:
		return CodeCommandFailed
	}
}

// summarizeOutput 裁剪远程输出，避免把完整日志写入错误信息。
func summarizeOutput(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	// 只保留最后几行：Docker 的失败原因通常在末尾。
	const keepLines = 6
	if len(lines) > keepLines {
		lines = lines[len(lines)-keepLines:]
	}
	summary := strings.Join(lines, " | ")
	const limit = 600
	if len(summary) > limit {
		return summary[:limit] + "..."
	}
	return summary
}
