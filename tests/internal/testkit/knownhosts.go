// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 该文件集中处理真实 SSH 测试的 known_hosts 来源解析，使各远程测试目录的行为一致：
// tests/config/ssh.yml 中显式配置优先，未配置时复用本机用户 OpenSSH 的
// known_hosts，与产品 internal/app.SSHService 的默认行为一致。
//
// 产品侧 known_hosts 全程只读，测试同样只读：本文件把来源文件快照复制进当前用例
// 工作空间，校验时只读快照，避免直接读取用户真实文件，也避免与其他并发用例或用户
// 自己编辑 known_hosts 的操作互相干扰。
package testkit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// KnownHostsSources 返回参与主机密钥校验的只读 known_hosts 快照文件列表。
//
// 优先级：
//
//  1. tests/config/ssh.yml 中 ssh.known_hosts 指定的文件；
//  2. 未配置时复用本机用户 OpenSSH 的 ~/.ssh/known_hosts 与 known_hosts2。
//
// 每个存在的来源文件都会被复制到当前用例工作空间的 known_hosts-snapshot 目录，
// 返回的是快照路径：真实文件不会被读取第二次，也不会被任何代码写入。
// 不存在的来源直接跳过（用户从未连接过任何主机是正常情况）；显式配置但文件不可读
// 时视为配置错误并让测试失败，避免"配置了文件却仍然报主机密钥未知"的误判。
func KnownHostsSources(t *testing.T) []string {
	t.Helper()
	sources, explicit := knownHostsSourcePaths(t)
	if len(sources) == 0 {
		if explicit {
			t.Fatalf(
				"%s 中 ssh.known_hosts 指定的文件都不存在，无法参与主机密钥校验",
				ConfigPath(t, SSHConfigFile),
			)
		}
		return nil
	}
	snapshotDir := filepath.Join(Dir(t), "known_hosts-snapshot")
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		t.Fatalf("创建 known_hosts 快照目录失败: %s: %v", snapshotDir, err)
	}
	snapshots := make([]string, 0, len(sources))
	for index, source := range sources {
		target := filepath.Join(snapshotDir, fmt.Sprintf("%02d_%s", index, filepath.Base(source)))
		if err := copyKnownHostsFile(source, target); err != nil {
			t.Fatalf("复制 known_hosts 快照失败: %s -> %s: %v", source, target, err)
		}
		snapshots = append(snapshots, target)
	}
	return snapshots
}

// knownHostsSourcePaths 解析参与校验的 known_hosts 来源路径。
//
// explicit 表示这些路径来自配置而不是本机 OpenSSH 的默认位置。
func knownHostsSourcePaths(t *testing.T) (paths []string, explicit bool) {
	t.Helper()
	config, _ := LoadSSHConfig(t)
	for _, path := range config.KnownHosts {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		explicit = true
		if _, err := os.Stat(trimmed); err != nil {
			t.Fatalf(
				"%s 中 ssh.known_hosts 指向的文件不可读: %s: %v"+
					"（请修改配置，或删除该字段以复用本机 ~/.ssh/known_hosts）",
				ConfigPath(t, SSHConfigFile),
				trimmed,
				err,
			)
		}
		paths = append(paths, trimmed)
	}
	if len(paths) > 0 {
		return paths, explicit
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Logf("无法解析本机用户 Home，主机密钥校验没有可用的 known_hosts 来源: %v", err)
		return nil, false
	}
	for _, candidate := range []string{
		filepath.Join(home, ".ssh", "known_hosts"),
		filepath.Join(home, ".ssh", "known_hosts2"),
	} {
		if _, statErr := os.Stat(candidate); statErr != nil {
			continue
		}
		paths = append(paths, candidate)
	}
	return paths, false
}

// copyKnownHostsFile 把一个 known_hosts 文件复制到测试工作空间。
//
// 复制只读取来源文件，不修改它的内容与权限。
func copyKnownHostsFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		_ = input.Close()
	}()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, copyErr := io.Copy(output, input); copyErr != nil {
		_ = output.Close()
		return copyErr
	}
	return output.Close()
}

// DescribeKnownHosts 返回 known_hosts 来源的可读说明，用于失败提示与日志。
//
// 它列出的是真实来源文件与是否存在，便于区分"配置写错"与"代码问题"。
func DescribeKnownHosts(t *testing.T) string {
	t.Helper()
	sources, _ := knownHostsSourcePaths(t)
	if len(sources) == 0 {
		return "无（没有可用的 known_hosts 来源）"
	}
	return strings.Join(sources, "、")
}

// HostKeyHint 生成主机密钥未知时的排查提示。
//
// 它明确列出当前使用的 known_hosts 来源，避免用户误以为程序读不到自己的配置。
func HostKeyHint(t *testing.T, storePath string) string {
	t.Helper()
	return fmt.Sprintf(
		"已读取的 known_hosts: %s\n"+
			"若本机 OpenSSH 已信任该主机仍报未知，可在 %s 的 ssh.known_hosts 中显式指定文件；\n"+
			"确认指纹后用 OpenSSH 把服务端密钥追加进该文件（AgentFlow 只读，不会代为写入），"+
			"本次测试的信任记录文件为 %s。",
		DescribeKnownHosts(t),
		ConfigPath(t, SSHConfigFile),
		storePath,
	)
}
