package sshclient

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NormalizePrivateKeyPath 校验并规范化本机私钥文件路径。
//
// 处理规则：
//   - 去掉两侧空白，空路径被拒绝；
//   - 以 ~/ 或 ~\ 开头的路径按本机用户 Home 展开，避免依赖调用方自行展开；
//   - 相对路径按当前工作目录转换为绝对路径；
//   - 路径必须指向已存在的普通文件，目录与特殊文件被拒绝。
//
// 这里只做路径层面的检查，不读取也不修改私钥内容；文件权限不足会在实际读取时
// 以 CodePrivateKey 报告。返回的错误是 *ConnectError，类别为 CodePrivateKey
// 或 CodeInvalidConfig。
func NormalizePrivateKeyPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", newConnectError(
			CodeInvalidConfig,
			"",
			"私钥认证必须指定私钥文件路径",
			nil,
		)
	}
	for _, char := range trimmed {
		if char == 0 || char == '\n' || char == '\r' {
			return "", newConnectError(
				CodePrivateKey,
				"",
				"私钥文件路径不能包含控制字符",
				nil,
			)
		}
	}
	expanded, err := expandHomePath(trimmed)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", newConnectError(
			CodePrivateKey,
			"",
			fmt.Sprintf("无法解析私钥文件路径 %s: %s", trimmed, describeFileError(err)),
			err,
		)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", newConnectError(
			CodePrivateKey,
			"",
			fmt.Sprintf("私钥文件不可用 %s: %s", absolute, describeFileError(err)),
			err,
		)
	}
	if info.IsDir() {
		return "", newConnectError(
			CodePrivateKey,
			"",
			fmt.Sprintf("私钥路径 %s 是目录，必须指向私钥文件", absolute),
			nil,
		)
	}
	if !info.Mode().IsRegular() {
		return "", newConnectError(
			CodePrivateKey,
			"",
			fmt.Sprintf("私钥路径 %s 不是普通文件", absolute),
			nil,
		)
	}
	return absolute, nil
}

// expandHomePath 把以 ~ 开头的路径展开为本机用户 Home 下的路径。
//
// 只支持 "~" 与 "~/..."（含 Windows 的 "~\"）：其他用户的 "~user/..." 形式
// 需要跨平台账号查询，不属于本步骤范围，直接返回明确错误。
func expandHomePath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", newConnectError(
			CodePrivateKey,
			"",
			fmt.Sprintf("无法解析本机用户 Home 目录以展开 %s: %s", path, describeFileError(err)),
			err,
		)
	}
	if strings.TrimSpace(home) == "" {
		return "", newConnectError(
			CodePrivateKey,
			"",
			fmt.Sprintf("本机用户 Home 目录为空，无法展开 %s", path),
			nil,
		)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
