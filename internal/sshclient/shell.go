package sshclient

import (
	"fmt"
	"strings"
	"unicode"
)

// ShellQuote 把单个参数转义为可安全嵌入 POSIX shell 命令行的形式。
//
// 远程命令以命令字符串传输，业务层构造命令时必须使用本方法转义每个参数，
// 禁止直接把用户输入拼接进 shell 命令。
func ShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if isShellSafe(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// ShellCommand 用固定命令模板与逐个转义后的参数构造远程命令。
//
// name 与 args 之间用空格分隔，每个参数都会被单独转义，
// 因此参数中的空格、引号、分号与管道符不会改变命令语义。
func ShellCommand(name string, args ...string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("sshclient: 远程命令名称不能为空")
	}
	if err := validateCommandName(name); err != nil {
		return "", err
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, name)
	for _, arg := range args {
		parts = append(parts, ShellQuote(arg))
	}
	return strings.Join(parts, " "), nil
}

// validateCommandName 校验命令名称只包含安全字符。
//
// 命令名称来自固定模板而不是用户输入；这里额外校验，防止模板被误用为
// 拼接任意 shell 内容的入口。
func validateCommandName(name string) error {
	for _, char := range name {
		switch {
		case unicode.IsLetter(char) && char < unicode.MaxASCII:
		case unicode.IsDigit(char) && char < unicode.MaxASCII:
		case char == '-' || char == '_' || char == '.' || char == '/':
		default:
			return fmt.Errorf(
				"sshclient: 远程命令名称只能包含字母、数字与 - _ . /，收到 %q",
				name,
			)
		}
	}
	return nil
}

// isShellSafe 判断参数是否可以不加引号直接使用。
func isShellSafe(value string) bool {
	for _, char := range value {
		switch {
		case unicode.IsLetter(char):
		case unicode.IsDigit(char):
		case char == '-' || char == '_' || char == '.' || char == '/' || char == ':' || char == '@':
		default:
			return false
		}
	}
	return true
}
