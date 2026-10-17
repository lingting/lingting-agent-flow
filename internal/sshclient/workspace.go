package sshclient

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
)

// DefaultRemoteWorkspaceRoot 是远程 AgentFlow 工作空间的默认值。
//
// 路径中的 ~ 指远程 SSH 登录用户的 Home，不是本机用户的 Home；
// 因此必须先解析远程 Home，再拼接为绝对路径。
const DefaultRemoteWorkspaceRoot = "~/.lingting/agentflow"

// resolveHomeTimeout 是解析远程 Home 目录的超时时间。
const resolveHomeTimeout = 30 * time.Second

// Workspace 是已解析的远程工作空间信息。
type Workspace struct {
	// Configured 是节点配置中的原始工作空间路径，可能以 ~ 开头。
	Configured string
	// Home 是远程登录用户的 Home 目录绝对路径。
	Home string
	// Root 是解析后的远程工作空间绝对路径。
	Root string
	// Exists 表示工作空间目录是否已存在。
	Exists bool
	// Created 表示本次操作是否新建了工作空间目录。
	Created bool
	// Writable 表示工作空间目录是否可写。
	Writable bool
	// ResolvedAt 是解析完成的时间，便于诊断连接与权限问题。
	ResolvedAt time.Time
}

// resolveRemoteHomeCommand 是获取远程 Home 目录的固定命令模板。
//
// 该命令完全固定，不拼接任何用户提供的 shell 内容：SFTP 不会像交互式 Shell
// 那样展开 ~，因此必须先通过已认证连接取得远程登录用户的 Home。
const resolveRemoteHomeCommand = `printf %s "$HOME"`

// ResolveHome 通过已认证连接获取远程登录用户的 Home 目录。
func ResolveHome(ctx context.Context, client Client, timeout time.Duration) (string, error) {
	if client == nil {
		return "", fmt.Errorf("sshclient: 连接为空，无法解析远程 Home 目录")
	}
	if timeout <= 0 {
		timeout = resolveHomeTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := client.Run(runCtx, resolveRemoteHomeCommand, RunOptions{})
	if err != nil {
		return "", fmt.Errorf("sshclient: 解析远程 Home 目录失败: %w", err)
	}
	if !result.Succeeded() {
		return "", fmt.Errorf(
			"sshclient: 解析远程 Home 目录失败: 远程命令退出码 %d: %s",
			result.ExitCode,
			strings.TrimSpace(result.Stderr),
		)
	}
	home := strings.TrimSpace(result.Stdout)
	if home == "" {
		return "", fmt.Errorf("sshclient: 解析远程 Home 目录失败: 远程返回空路径")
	}
	normalized, err := NormalizeRemotePath(home)
	if err != nil {
		return "", fmt.Errorf("sshclient: 解析远程 Home 目录失败: %w", err)
	}
	return normalized, nil
}

// ResolveWorkspace 把节点配置的远程工作空间解析为绝对路径。
//
// 配置为空时使用 DefaultRemoteWorkspaceRoot；以 ~ 开头的路径按远程 Home 展开；
// 其余路径必须是绝对路径。解析结果不会被悄悄替换成其他系统目录。
func ResolveWorkspace(configured, home string) (string, error) {
	normalizedHome, err := NormalizeRemotePath(home)
	if err != nil {
		return "", fmt.Errorf("sshclient: 远程 Home 目录非法: %w", err)
	}
	raw := strings.TrimSpace(configured)
	if raw == "" {
		raw = DefaultRemoteWorkspaceRoot
	}
	if strings.ContainsRune(raw, '\\') {
		return "", fmt.Errorf(
			"%w: 远程工作空间 %q 必须使用 POSIX 分隔符 /",
			ErrInvalidRemotePath,
			configured,
		)
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", fmt.Errorf(
				"%w: 远程工作空间 %q 不能包含上级目录引用 ..",
				ErrInvalidRemotePath,
				configured,
			)
		}
	}
	var candidate string
	switch {
	case raw == "~":
		return "", fmt.Errorf("%w: 远程工作空间不能是 Home 目录本身", ErrInvalidRemotePath)
	case strings.HasPrefix(raw, "~/"):
		candidate = path.Join(normalizedHome, strings.TrimPrefix(raw, "~/"))
	case strings.HasPrefix(raw, "/"):
		candidate = raw
	default:
		return "", fmt.Errorf(
			"%w: 远程工作空间 %q 必须以 / 或 ~/ 开头",
			ErrInvalidRemotePath,
			configured,
		)
	}
	resolved, err := NormalizeRemotePath(candidate)
	if err != nil {
		return "", err
	}
	if resolved == "/" || resolved == normalizedHome {
		return "", fmt.Errorf(
			"%w: 远程工作空间不能是根目录或 Home 目录本身",
			ErrInvalidRemotePath,
		)
	}
	return resolved, nil
}

// EnsureWorkspace 解析远程工作空间，并按需创建目录并检查写入权限。
//
// create 为 false 时不创建目录，仅检查可访问性，目录不存在时 Exists 为 false。
// 该操作只在解析出的工作空间目录内进行，不会切换到其他系统目录。
func EnsureWorkspace(
	ctx context.Context,
	client Client,
	configured string,
	create bool,
) (Workspace, error) {
	workspace := Workspace{Configured: configured, ResolvedAt: time.Now().UTC()}
	if client == nil {
		return workspace, fmt.Errorf("sshclient: 连接为空，无法检查远程工作空间")
	}
	home, err := ResolveHome(ctx, client, 0)
	if err != nil {
		return workspace, err
	}
	workspace.Home = home
	root, err := ResolveWorkspace(configured, home)
	if err != nil {
		return workspace, err
	}
	workspace.Root = root
	transfer, err := client.SFTP()
	if err != nil {
		return workspace, err
	}
	defer func() {
		_ = transfer.Close()
	}()
	existed, err := remotePathExists(ctx, transfer, root)
	if err != nil {
		return workspace, err
	}
	workspace.Exists = existed
	if !existed && !create {
		return workspace, nil
	}
	if !existed {
		if err := transfer.MkdirAll(ctx, root); err != nil {
			return workspace, fmt.Errorf("sshclient: 创建工作空间失败: %s: %w", root, err)
		}
		workspace.Exists = true
		workspace.Created = true
	}
	info, err := transfer.Stat(ctx, root)
	if err != nil {
		return workspace, fmt.Errorf("sshclient: 检查工作空间失败: %s: %w", root, err)
	}
	if !info.IsDir {
		return workspace, fmt.Errorf("sshclient: 工作空间路径 %s 已存在但不是目录", root)
	}
	if err := checkWritable(ctx, client, root); err != nil {
		return workspace, err
	}
	workspace.Writable = true
	workspace.ResolvedAt = time.Now().UTC()
	return workspace, nil
}

// remotePathExists 判断远程路径是否存在。
func remotePathExists(ctx context.Context, transfer FileTransfer, target string) (bool, error) {
	if _, err := transfer.Stat(ctx, target); err != nil {
		if isSFTPNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("sshclient: 检查远程路径失败: %s: %w", target, err)
	}
	return true, nil
}

// checkWritable 通过固定命令检查目录是否可写。
//
// 使用 test -w 而不是创建探针文件，避免在工作空间中留下临时文件。
func checkWritable(ctx context.Context, client Client, directory string) error {
	command, err := ShellCommand("test", "-w", directory)
	if err != nil {
		return fmt.Errorf("sshclient: 构造写入权限检查命令失败: %w", err)
	}
	result, err := client.Run(ctx, command, RunOptions{})
	if err != nil {
		return fmt.Errorf("sshclient: 检查工作空间写入权限失败: %s: %w", directory, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("sshclient: 远程工作空间不可写: %s", directory)
	}
	return nil
}
