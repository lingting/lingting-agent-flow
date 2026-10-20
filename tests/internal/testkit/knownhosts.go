// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 该文件集中处理真实 SSH 测试的 known_hosts 来源解析，使各远程测试目录的行为一致：
// tests/config/ssh.yml 中显式配置优先，未配置时复用本机用户 OpenSSH 的
// known_hosts，与产品 internal/app.SSHService 的默认行为一致。
package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// KnownHostsSources 返回参与主机密钥校验的只读 known_hosts 文件列表。
//
// 优先级：
//
//  1. tests/config/ssh.yml 中 ssh.known_hosts 指定的文件；
//  2. 未配置时复用本机用户 OpenSSH 的 ~/.ssh/known_hosts 与 known_hosts2。
//
// 返回的文件可能不存在（用户从未连接过任何主机），调用方无需预先检查：
// 主机密钥库会跳过不存在的路径。显式配置但文件不可读时视为配置错误并让测试失败，
// 避免"配置了文件却仍然报主机密钥未知"的误判。
func KnownHostsSources(t *testing.T) []string {
	t.Helper()
	config, _ := LoadSSHConfig(t)
	configured := make([]string, 0, len(config.KnownHosts))
	for _, path := range config.KnownHosts {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		if _, err := os.Stat(trimmed); err != nil {
			t.Fatalf(
				"%s 中 ssh.known_hosts 指向的文件不可读: %s: %v"+
					"（请修改配置，或删除该字段以复用本机 ~/.ssh/known_hosts）",
				ConfigPath(t, SSHConfigFile),
				trimmed,
				err,
			)
		}
		configured = append(configured, trimmed)
	}
	if len(configured) > 0 {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Logf("无法解析本机用户 Home，主机密钥校验只能使用测试工作空间的信任记录: %v", err)
		return nil
	}
	return []string{
		filepath.Join(home, ".ssh", "known_hosts"),
		filepath.Join(home, ".ssh", "known_hosts2"),
	}
}

// DescribeKnownHosts 返回 known_hosts 来源的可读说明，用于失败提示与日志。
func DescribeKnownHosts(t *testing.T) string {
	t.Helper()
	sources := KnownHostsSources(t)
	if len(sources) == 0 {
		return "无（仅使用测试工作空间的信任记录）"
	}
	existing := make([]string, 0, len(sources))
	for _, path := range sources {
		if _, err := os.Stat(path); err == nil {
			existing = append(existing, path)
			continue
		}
		existing = append(existing, path+"（不存在）")
	}
	return strings.Join(existing, "、")
}

// HostKeyHint 生成主机密钥未知时的排查提示。
//
// 它明确列出当前使用的 known_hosts 来源，避免用户误以为程序读不到自己的配置。
func HostKeyHint(t *testing.T, storePath string) string {
	t.Helper()
	return fmt.Sprintf(
		"已读取的 known_hosts: %s\n"+
			"若本机 OpenSSH 已信任该主机仍报未知，可在 %s 的 ssh.known_hosts 中显式指定文件；\n"+
			"确认指纹后把记录写入 %s 即可复用（例如 host trust 命令）。",
		DescribeKnownHosts(t),
		ConfigPath(t, SSHConfigFile),
		storePath,
	)
}
