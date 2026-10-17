package sshprobe

import (
	"strings"
)

// sshIdentifierPrefix 是 SSH 协议标识行的固定前缀。
//
// 参见 RFC 4253 第 4.2 节：服务端标识形如 "SSH-2.0-softwareversion"。
const sshIdentifierPrefix = "SSH-"

// maxBannerLineLength 是单行协议标识的最大字节数（含 CRLF）。
const maxBannerLineLength = 255

// isSSHIdentifier 判断一行内容是否为符合格式的 SSH 协议标识。
//
// 规则来自 RFC 4253：
//   - 必须以 "SSH-" 开头；
//   - 后面是协议版本号与软件版本，形如 "2.0-OpenSSH_9.6"；
//   - 版本号只包含可打印 ASCII，且不以空白开头或结尾。
func isSSHIdentifier(line string) bool {
	if !strings.HasPrefix(line, sshIdentifierPrefix) {
		return false
	}
	rest := strings.TrimPrefix(line, sshIdentifierPrefix)
	if rest == "" {
		return false
	}
	major, minor, software, ok := splitSSHVersion(rest)
	if !ok {
		return false
	}
	if major == "" || minor == "" {
		return false
	}
	for _, char := range major + minor {
		if char < '0' || char > '9' {
			return false
		}
	}
	// 软件版本可以为空，但非空时必须是可打印 ASCII 且不含空白。
	if software == "" {
		return true
	}
	for _, char := range software {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

// splitSSHVersion 把 "2.0-OpenSSH_9.6" 拆分为主版本、次版本与软件版本。
//
// 软件版本可以包含额外的连字符，因此只在第一个连字符处切分。
func splitSSHVersion(rest string) (major, minor, software string, ok bool) {
	version, software, _ := strings.Cut(rest, "-")
	major, minor, found := strings.Cut(version, ".")
	if !found {
		return "", "", "", false
	}
	return major, minor, software, true
}

// truncateForMessage 把读取到的内容裁剪为可放入诊断信息的短片段。
//
// 该片段只用于本地诊断，不作为可信业务数据，也不会写入凭据。
func truncateForMessage(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "..."
}
