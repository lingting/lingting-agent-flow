// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 该文件集中处理远程真实测试的资源目录约定：所有需要真实远程主机的测试都只在
// 解析出的远程 Home 下的 .lingting/agentflow-tests 中创建资源，并按测试包与单次
// 用例再分层，使不同测试包、不同用例、并行用例之间互不覆盖，也与生产工作空间
// (~/.lingting/agentflow) 完全隔离。
//
// 该约定只约束远程主机上的路径；本机测试产物仍然使用 os.TempDir() 下的
// lingting-agent-flow-tests，两者互不影响。
package testkit

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
)

// 远程真实测试资源目录约定。
const (
	// RemoteTestHomeDirName 是远程测试资源所在的隐藏目录，相对远程 Home。
	RemoteTestHomeDirName = ".lingting"
	// RemoteTestRootDirName 是统一存放远程真实测试资源的目录名。
	//
	// 旧版本使用 ~/agentflow-tests，该路径已废弃：新增或修改的测试一律使用
	// RemoteTestRootDirName，避免两套目录漂移。
	RemoteTestRootDirName = "agentflow-tests"
)

// 远程真实测试包的专属子目录名。
//
// 每个测试包只使用自己的子目录，禁止跨包复用，使清理与排查可以按测试包收敛。
const (
	// RemoteTestPackageAgent 是 tests/integration/remote/agent 的子目录。
	RemoteTestPackageAgent = "agent"
	// RemoteTestPackageDocker 是 tests/integration/remote/docker 的子目录。
	RemoteTestPackageDocker = "docker"
	// RemoteTestPackageCodeSaver 是 tests/integration/remote/codesaver 的子目录。
	RemoteTestPackageCodeSaver = "codesaver"
	// RemoteTestPackageWorkflow 是 tests/integration/remote/workflow 的子目录。
	RemoteTestPackageWorkflow = "workflow"
	// RemoteTestPackageSSH 是 tests/integration/ssh 的子目录。
	RemoteTestPackageSSH = "ssh"
)

// remoteTestPackages 是允许使用的测试包子目录白名单。
//
// 该白名单同时是全仓库唯一的目录来源：新增测试包只能在这里登记，避免各测试包
// 各自拼接路径导致目录漂移。
var remoteTestPackages = map[string]struct{}{
	RemoteTestPackageAgent:     {},
	RemoteTestPackageDocker:    {},
	RemoteTestPackageCodeSaver: {},
	RemoteTestPackageWorkflow:  {},
	RemoteTestPackageSSH:       {},
}

// defaultRemoteDirTimeout 是解析与创建远程目录的默认超时。
const defaultRemoteDirTimeout = 2 * time.Minute

// RemoteTestRoot 拼接远程真实测试目录的绝对路径。
//
// home 必须是 SSH 解析出的远程 Home 绝对路径：本函数不接受字面量 "~"，也不依赖
// Shell 的隐式展开。packageName 必须是已登记的测试包子目录，parts 是本次用例的
// 唯一标识等可选分层。
func RemoteTestRoot(home, packageName string, parts ...string) (string, error) {
	trimmedHome := strings.TrimSpace(home)
	if !strings.HasPrefix(trimmedHome, "/") {
		return "", fmt.Errorf("远程测试目录需要绝对路径 Home，实际为 %q", trimmedHome)
	}
	if strings.ContainsAny(trimmedHome, "\x00\n\r") {
		return "", fmt.Errorf("远程 Home 含有非法字符: %q", trimmedHome)
	}
	if _, ok := remoteTestPackages[packageName]; !ok {
		return "", fmt.Errorf("未登记的远程测试包子目录 %q", packageName)
	}
	segments := make([]string, 0, 4+len(parts))
	segments = append(
		segments,
		path.Clean(trimmedHome),
		RemoteTestHomeDirName,
		RemoteTestRootDirName,
		packageName,
	)
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if err := validateRemotePathSegment(trimmed); err != nil {
			return "", err
		}
		segments = append(segments, trimmed)
	}
	return path.Join(segments...), nil
}

// validateRemotePathSegment 校验单个路径片段。
//
// 片段来自测试自身生成的随机后缀或固定名称，这里额外校验，防止误用为任意路径
// 拼接入口（例如传入 "../" 逃逸出测试目录）。
func validateRemotePathSegment(segment string) error {
	if segment == "." || segment == ".." {
		return fmt.Errorf("远程测试路径片段不能是相对路径: %q", segment)
	}
	if len(segment) > 128 {
		return fmt.Errorf("远程测试路径片段过长: %q", segment)
	}
	for _, char := range segment {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_', char == '.':
		default:
			return fmt.Errorf("远程测试路径片段 %q 含有非法字符 %q", segment, char)
		}
	}
	return nil
}

// ResolveRemoteTestRoot 解析远程 Home 并创建本次用例专属的远程测试目录。
//
// 目录通过已认证连接上的固定命令 mkdir -p 创建，沿用与产品一致的 SSH 主机密钥
// 校验，不降低任何校验要求。
func ResolveRemoteTestRoot(
	ctx context.Context,
	client sshclient.Client,
	timeout time.Duration,
	packageName string,
	parts ...string,
) (string, error) {
	if client == nil {
		return "", fmt.Errorf("缺少 SSH 连接，无法解析远程测试目录")
	}
	if timeout <= 0 {
		timeout = defaultRemoteDirTimeout
	}
	home, err := sshclient.ResolveHome(ctx, client, timeout)
	if err != nil {
		return "", err
	}
	root, err := RemoteTestRoot(home, packageName, parts...)
	if err != nil {
		return "", err
	}
	if err := EnsureRemoteDir(ctx, client, root, timeout); err != nil {
		return "", err
	}
	return root, nil
}

// EnsureRemoteDir 在远程机器上创建目录（mkdir -p），已存在时视为成功。
func EnsureRemoteDir(
	ctx context.Context,
	client sshclient.Client,
	dir string,
	timeout time.Duration,
) error {
	if client == nil {
		return fmt.Errorf("缺少 SSH 连接，无法创建远程目录")
	}
	trimmed := strings.TrimSpace(dir)
	if !strings.HasPrefix(trimmed, "/") {
		return fmt.Errorf("远程目录必须是绝对路径: %q", trimmed)
	}
	if timeout <= 0 {
		timeout = defaultRemoteDirTimeout
	}
	command, err := sshclient.ShellCommand("mkdir", "-p", trimmed)
	if err != nil {
		return fmt.Errorf("构造远程目录创建命令失败: %w", err)
	}
	run, runErr := client.Run(ctx, command, sshclient.RunOptions{Timeout: timeout})
	if runErr != nil {
		return fmt.Errorf("创建远程目录 %s 失败: %w", trimmed, runErr)
	}
	if !run.Succeeded() {
		return fmt.Errorf(
			"创建远程目录 %s 失败: mkdir 退出码 %d: %s",
			trimmed,
			run.ExitCode,
			strings.TrimSpace(run.Stderr),
		)
	}
	return nil
}
