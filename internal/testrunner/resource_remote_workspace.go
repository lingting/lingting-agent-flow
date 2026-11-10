package testrunner

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lingting/agentflow/internal/sshclient"
)

// RemoteWorkspaceProvider 在一台远程主机上分配用例独占的临时目录。
//
// 目录只在本次运行的远程测试根目录（默认 /tmp/agentflow-test/<run-id>）下创建：声明给出
// 的 path 要么是相对路径，要么仍落在该根目录内的绝对路径，否则直接失败。释放用 SFTP
// RemoveAll，自证用远程 test -d 确认目录不存在。
type RemoteWorkspaceProvider struct {
	hosts      *remoteHosts
	runID      string
	mu         sync.Mutex
	workspaces map[string]*remoteWorkspaceEntry
}

// remoteWorkspaceEntry 是一条已创建的远程目录记录。
type remoteWorkspaceEntry struct {
	handle Handle
	hostID string
}

// NewRemoteWorkspaceProvider 创建远程工作目录提供者。
func NewRemoteWorkspaceProvider(hosts *remoteHosts, runID string) *RemoteWorkspaceProvider {
	return &RemoteWorkspaceProvider{
		hosts:      hosts,
		runID:      strings.TrimSpace(runID),
		workspaces: make(map[string]*remoteWorkspaceEntry),
	}
}

// Acquire 创建远程目录，句柄给出 host_id 与解析后的绝对路径。
func (p *RemoteWorkspaceProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	hostID := specValue(decl, "host_id")
	host, err := p.hosts.config(hostID)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	target, err := p.targetPath(host, decl)
	if err != nil {
		return Handle{}, err
	}
	client, err := p.hosts.client(ctx, hostID)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	workspace, err := sshclient.EnsureWorkspace(ctx, client, target, true)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的远程目录失败: %w", decl.Name, err)
	}
	if !workspace.Writable {
		return Handle{}, fmt.Errorf("远程目录 %s 不可写", workspace.Root)
	}
	handle, err := buildHandle(decl, map[string]string{
		"host_id": host.HostID,
		"path":    workspace.Root,
	}, "")
	if err != nil {
		return Handle{}, err
	}
	p.mu.Lock()
	p.workspaces[workspace.Root] = &remoteWorkspaceEntry{handle: handle, hostID: host.HostID}
	p.mu.Unlock()
	return handle, nil
}

// Release 递归删除远程目录，可重复调用。
func (p *RemoteWorkspaceProvider) Release(ctx context.Context, handle Handle) error {
	path := handle.PayloadValue("path")
	p.mu.Lock()
	entry := p.workspaces[path]
	p.mu.Unlock()
	if entry == nil {
		return nil
	}
	client, err := p.hosts.client(ctx, entry.hostID)
	if err != nil {
		return fmt.Errorf("释放资源 %q 时无法取得连接: %w", handle.Name, err)
	}
	transfer, err := client.SFTP()
	if err != nil {
		return fmt.Errorf("释放资源 %q 时打开 SFTP 通道失败: %w", handle.Name, err)
	}
	removeErr := transfer.RemoveAll(ctx, path)
	closeErr := transfer.Close()
	if removeErr != nil {
		return fmt.Errorf("删除远程目录 %s 失败: %w", path, removeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("关闭资源 %q 的 SFTP 通道失败: %w", handle.Name, closeErr)
	}
	p.mu.Lock()
	delete(p.workspaces, path)
	p.mu.Unlock()
	return nil
}

// Verify 自证远程目录在释放后不再存在。
func (p *RemoteWorkspaceProvider) Verify(ctx context.Context, handle Handle) error {
	path := handle.PayloadValue("path")
	hostID := handle.PayloadValue("host_id")
	p.mu.Lock()
	_, tracked := p.workspaces[path]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("远程目录 %s 仍被提供者持有，释放没有生效", path)
	}
	client, err := p.hosts.client(ctx, hostID)
	if err != nil {
		return fmt.Errorf("核验远程目录 %s 时无法取得连接: %w", path, err)
	}
	exists, err := remoteDirectoryExists(ctx, client, path)
	if err != nil {
		return fmt.Errorf("无法确认远程目录 %s 是否已删除: %w", path, err)
	}
	if exists {
		return fmt.Errorf("远程目录 %s 在释放后仍然存在", path)
	}
	return nil
}

// Snapshot 返回仍被持有的远程目录。
func (p *RemoteWorkspaceProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.workspaces))
	for path, entry := range p.workspaces {
		entries = append(entries, LiveResource{
			Handle: entry.handle,
			Detail: fmt.Sprintf("主机 %s 上仍存在的远程目录 %s", entry.hostID, path),
		})
	}
	return entries, nil
}

// targetPath 计算本次分配要创建的远程目录，并保证它落在测试根目录内。
func (p *RemoteWorkspaceProvider) targetPath(host RemoteHost, decl ResourceDecl) (string, error) {
	root := strings.TrimSuffix(host.workspaceRoot(p.runID), "/")
	if root == "" || root == "/" {
		return "", fmt.Errorf("主机 %q 的远程测试根目录不合法: %q", host.HostID, root)
	}
	configured := specValue(decl, "path")
	if configured == "" {
		token, err := randomToken(4)
		if err != nil {
			return "", err
		}
		return root + "/" + safeName(decl.Name) + "-" + token, nil
	}
	target := configured
	if !strings.HasPrefix(target, "/") {
		target = root + "/" + target
	}
	if target == root || !strings.HasPrefix(target, root+"/") {
		return "", fmt.Errorf(
			"资源 %q 的远程目录 %q 不在测试根目录 %s 下",
			decl.Name,
			configured,
			root,
		)
	}
	return target, nil
}

// remoteDirectoryExists 用固定的 test -d 命令判断远程目录是否存在。
func remoteDirectoryExists(ctx context.Context, client sshclient.Client, path string) (bool, error) {
	command, err := sshclient.ShellCommand("test", "-d", path)
	if err != nil {
		return false, fmt.Errorf("构造远程目录检查命令失败: %w", err)
	}
	result, err := client.Run(ctx, command, sshclient.RunOptions{Timeout: resourceProbeTimeout})
	if err != nil {
		return false, err
	}
	if !result.Started {
		return false, fmt.Errorf("远程目录检查命令没有执行")
	}
	switch result.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("远程目录检查命令退出码 %d", result.ExitCode)
	}
}
