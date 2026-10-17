package sshclient

import (
	"fmt"
	"path"
	"strings"
)

// ErrInvalidRemotePath 表示远程路径非法。
var ErrInvalidRemotePath = fmt.Errorf("sshclient: 远程路径非法")

// NormalizeRemotePath 校验并规范化远程 POSIX 路径。
//
// 远程节点是 Linux，因此这里只使用 POSIX 规则，不使用本机的文件路径规则：
//   - 空路径与仅含空白的路径被拒绝；
//   - 反斜杠与控制字符被拒绝，避免与本机路径混用；
//   - 相对路径被拒绝，远程操作只接受绝对路径；
//   - 出现 .. 段时直接拒绝，防止目录穿越。
func NormalizeRemotePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 路径不能为空", ErrInvalidRemotePath)
	}
	if strings.ContainsRune(trimmed, '\\') {
		return "", fmt.Errorf("%w: %q 必须使用 POSIX 分隔符 /", ErrInvalidRemotePath, raw)
	}
	for _, char := range trimmed {
		if char == 0 || char == '\n' || char == '\r' || char == '\t' {
			return "", fmt.Errorf("%w: %q 不能包含控制字符", ErrInvalidRemotePath, raw)
		}
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("%w: %q 必须是绝对路径", ErrInvalidRemotePath, raw)
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: %q 不能包含上级目录引用 ..", ErrInvalidRemotePath, raw)
		}
	}
	cleaned := path.Clean(trimmed)
	if !strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("%w: %q 规范化后不是绝对路径", ErrInvalidRemotePath, raw)
	}
	return cleaned, nil
}

// JoinRemotePath 在已规范化的远程绝对路径下拼接子路径。
//
// 子路径可以是多级相对路径，但不能是绝对路径，也不能包含 .. 段。
func JoinRemotePath(base, child string) (string, error) {
	normalizedBase, err := NormalizeRemotePath(base)
	if err != nil {
		return "", err
	}
	trimmedChild := strings.TrimSpace(child)
	if trimmedChild == "" {
		return "", fmt.Errorf("%w: 子路径不能为空", ErrInvalidRemotePath)
	}
	if strings.ContainsRune(trimmedChild, '\\') {
		return "", fmt.Errorf("%w: %q 必须使用 POSIX 分隔符 /", ErrInvalidRemotePath, child)
	}
	if strings.HasPrefix(trimmedChild, "/") {
		return "", fmt.Errorf("%w: %q 不能是绝对路径", ErrInvalidRemotePath, child)
	}
	for _, segment := range strings.Split(trimmedChild, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: %q 不能包含上级目录引用 ..", ErrInvalidRemotePath, child)
		}
	}
	joined := path.Clean(path.Join(normalizedBase, trimmedChild))
	if !strings.HasPrefix(joined, normalizedBase) {
		return "", fmt.Errorf("%w: %q 超出基准目录 %s", ErrInvalidRemotePath, child, normalizedBase)
	}
	return joined, nil
}

// IsWithinRemotePath 判断 child 是否位于 base 之内（含 base 自身）。
//
// 两个路径都会先规范化；任一路径非法时返回 false。
func IsWithinRemotePath(base, child string) bool {
	normalizedBase, err := NormalizeRemotePath(base)
	if err != nil {
		return false
	}
	normalizedChild, err := NormalizeRemotePath(child)
	if err != nil {
		return false
	}
	if normalizedChild == normalizedBase {
		return true
	}
	return strings.HasPrefix(normalizedChild, normalizedBase+"/")
}

// validateLocalPath 校验本机文件路径。
//
// 本机路径由调用方按当前操作系统规则提供，这里只拒绝空路径与控制字符。
func validateLocalPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("sshclient: 本机路径不能为空")
	}
	for _, char := range trimmed {
		if char == 0 || char == '\n' || char == '\r' {
			return "", fmt.Errorf("sshclient: 本机路径 %q 不能包含控制字符", raw)
		}
	}
	return trimmed, nil
}
