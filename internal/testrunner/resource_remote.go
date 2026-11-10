package testrunner

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/sshclient"
)

// RemoteHost 是一台真实远程主机的 SSH 连接配置。
//
// 配置由调用方（R4.5 试点）从 tests/config/ssh.yml 装载后注入；提供者不读取配置文件、
// 不读取环境变量，也不允许关闭主机密钥校验。
type RemoteHost struct {
	// HostID 是配置内的主机标识，与声明 Spec 的 host_id 对应。
	HostID string
	// Address 是主机地址，不含端口。
	Address string
	// Port 是 SSH 端口，零值使用 22。
	Port int
	// User 是登录用户名。
	User string
	// PrivateKeyPath 是本机私钥文件路径。
	PrivateKeyPath string
	// KnownHostsPaths 是参与主机密钥校验的只读 known_hosts 文件。
	KnownHostsPaths []string
	// WorkspaceRoot 是远程测试根目录，空值使用 /tmp/agentflow-test/<run-id>。
	WorkspaceRoot string
}

// remoteHosts 是远程提供者共享的连接与命令执行器池。
//
// 连接由 internal/sshclient 建立（主机密钥必须校验通过），Docker 命令只经由
// internal/docker 的 Runner 执行；这里不引入第二套连接或命令封装。
type remoteHosts struct {
	hosts map[string]RemoteHost

	mu       sync.Mutex
	clients  map[string]sshclient.Client
	runners  map[string]*docker.Runner
	hostKeys map[string]string
}

// newRemoteHosts 以 host_id 为键建立主机配置索引。
func newRemoteHosts(hosts map[string]RemoteHost) *remoteHosts {
	index := make(map[string]RemoteHost, len(hosts))
	for id, host := range hosts {
		if strings.TrimSpace(host.HostID) == "" {
			host.HostID = strings.TrimSpace(id)
		}
		if host.HostID == "" {
			continue
		}
		index[host.HostID] = host
	}
	return &remoteHosts{
		hosts:    index,
		clients:  make(map[string]sshclient.Client),
		runners:  make(map[string]*docker.Runner),
		hostKeys: make(map[string]string),
	}
}

// config 返回主机配置；未配置的主机直接失败，不回退到本机执行。
func (r *remoteHosts) config(hostID string) (RemoteHost, error) {
	host, ok := r.hosts[strings.TrimSpace(hostID)]
	if !ok {
		return RemoteHost{}, fmt.Errorf(
			"主机 %q 没有注入连接配置：远程提供者需要真实主机，不能回退到本机执行",
			hostID,
		)
	}
	if strings.TrimSpace(host.Address) == "" || strings.TrimSpace(host.User) == "" {
		return RemoteHost{}, fmt.Errorf("主机 %q 的地址或用户名为空", hostID)
	}
	if strings.TrimSpace(host.PrivateKeyPath) == "" {
		return RemoteHost{}, fmt.Errorf("主机 %q 缺少私钥路径", hostID)
	}
	if len(host.KnownHostsPaths) == 0 {
		return RemoteHost{}, fmt.Errorf("主机 %q 缺少 known_hosts 配置，禁止跳过主机密钥校验", hostID)
	}
	return host, nil
}

// endpoint 返回主机的 SSH 描述，用于 docker-daemon 的 endpoint 字段。
func (r RemoteHost) endpoint() string {
	port := r.Port
	if port <= 0 {
		port = 22
	}
	return fmt.Sprintf("ssh://%s@%s:%d", r.User, r.Address, port)
}

// workspaceRoot 返回远程测试根目录。
func (r RemoteHost) workspaceRoot(runID string) string {
	if configured := strings.TrimSpace(r.WorkspaceRoot); configured != "" {
		return configured
	}
	token := strings.TrimSpace(runID)
	if token == "" {
		token = "manual"
	}
	return "/tmp/agentflow-test/" + token
}

// client 返回主机上复用的已认证连接，必要时建立连接。
func (r *remoteHosts) client(ctx context.Context, hostID string) (sshclient.Client, error) {
	host, err := r.config(hostID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	existing := r.clients[host.HostID]
	r.mu.Unlock()
	if existing != nil {
		return existing, nil
	}
	client, fingerprint, err := dialRemoteHost(ctx, host)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if current := r.clients[host.HostID]; current != nil {
		r.mu.Unlock()
		_ = client.Close()
		return current, nil
	}
	r.clients[host.HostID] = client
	r.hostKeys[host.HostID] = fingerprint
	r.mu.Unlock()
	return client, nil
}

// runner 返回主机上的 Docker 命令执行器，必要时基于共享连接构造。
func (r *remoteHosts) runner(ctx context.Context, hostID string) (*docker.Runner, error) {
	host, err := r.config(hostID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	existing := r.runners[host.HostID]
	r.mu.Unlock()
	if existing != nil {
		return existing, nil
	}
	client, err := r.client(ctx, host.HostID)
	if err != nil {
		return nil, err
	}
	runner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		return nil, fmt.Errorf("构造主机 %q 的 Docker 命令执行器失败: %w", host.HostID, err)
	}
	r.mu.Lock()
	r.runners[host.HostID] = runner
	r.mu.Unlock()
	return runner, nil
}

// hostKeyFingerprint 返回主机密钥指纹，未连接时为空。
func (r *remoteHosts) hostKeyFingerprint(hostID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hostKeys[hostID]
}

// connected 报告指定主机是否仍有共享连接。
func (r *remoteHosts) connected(hostID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clients[hostID] != nil
}

// closeHost 关闭指定主机的共享连接与执行器。
func (r *remoteHosts) closeHost(hostID string) error {
	r.mu.Lock()
	client := r.clients[hostID]
	delete(r.clients, hostID)
	delete(r.runners, hostID)
	delete(r.hostKeys, hostID)
	r.mu.Unlock()
	if client == nil {
		return nil
	}
	if err := client.Close(); err != nil {
		return fmt.Errorf("关闭主机 %q 的连接失败: %w", hostID, err)
	}
	return nil
}

// snapshot 返回仍保持连接的主机，供运行级终检比对。
func (r *remoteHosts) snapshot() []LiveResource {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.clients))
	for hostID := range r.clients {
		ids = append(ids, hostID)
	}
	sort.Strings(ids)
	entries := make([]LiveResource, 0, len(ids))
	for _, hostID := range ids {
		entries = append(entries, LiveResource{
			Handle: Handle{
				Name:    hostID,
				Type:    ResourceSSHHost,
				Scope:   ScopeRunShared,
				Payload: map[string]string{"host_id": hostID},
			},
			Detail: "仍保持的 SSH 连接 " + hostID,
		})
	}
	return entries
}

// dialRemoteHost 建立一条经过主机密钥校验的连接。
//
// 未知主机、主机密钥变化、私钥不可用都会返回错误：本包不允许以跳过校验的方式连接。
func dialRemoteHost(ctx context.Context, host RemoteHost) (sshclient.Client, string, error) {
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: host.KnownHostsPaths,
	})
	if err != nil {
		return nil, "", fmt.Errorf("读取主机 %q 的 known_hosts 失败: %w", host.HostID, err)
	}
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{HostKeys: store})
	if err != nil {
		return nil, "", fmt.Errorf("构造主机 %q 的连接器失败: %w", host.HostID, err)
	}
	port := host.Port
	if port <= 0 {
		port = 22
	}
	result, err := dialer.Dial(
		ctx,
		sshclient.Endpoint{Host: host.Address, Port: port, Username: host.User},
		sshclient.AuthConfig{
			Method:         sshclient.AuthMethodPrivateKey,
			PrivateKeyPath: host.PrivateKeyPath,
		},
	)
	if err != nil {
		return nil, "", fmt.Errorf("连接主机 %q 失败: %w", host.HostID, err)
	}
	if result.Client == nil {
		return nil, "", fmt.Errorf("连接主机 %q 未取得可用连接", host.HostID)
	}
	return result.Client, result.HostKey.Fingerprint, nil
}

// SSHHostProvider 分配一台真实远程主机的共享连接（run-shared）。
//
// 就绪判定是解析远程 Home：它证明连接、认证与远程命令执行都可用。释放即关闭共享连接，
// 因此同一台主机上的其他共享资源必须在它之前释放（运行级按逆序释放）。
type SSHHostProvider struct {
	hosts *remoteHosts
}

// NewSSHHostProvider 创建远程主机提供者。
func NewSSHHostProvider(hosts *remoteHosts) *SSHHostProvider {
	return &SSHHostProvider{hosts: hosts}
}

// Acquire 建立连接并解析远程 Home，句柄给出 host_id、address、user 与 port。
func (p *SSHHostProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	hostID := specValue(decl, "host_id")
	host, err := p.hosts.config(hostID)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	client, err := p.hosts.client(ctx, hostID)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	if _, err := sshclient.ResolveHome(ctx, client, 0); err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 时远程会话不可用: %w", decl.Name, err)
	}
	port := host.Port
	if port <= 0 {
		port = 22
	}
	handle, err := buildHandle(decl, map[string]string{
		"host_id": host.HostID,
		"address": host.Address,
		"user":    host.User,
		"port":    strconv.Itoa(port),
	}, p.hosts.hostKeyFingerprint(hostID))
	if err != nil {
		return Handle{}, err
	}
	return handle, nil
}

// Release 关闭该主机的共享连接，可重复调用。
func (p *SSHHostProvider) Release(ctx context.Context, handle Handle) error {
	return p.hosts.closeHost(handle.PayloadValue("host_id"))
}

// Verify 自证该主机不再保留共享连接。
func (p *SSHHostProvider) Verify(ctx context.Context, handle Handle) error {
	hostID := handle.PayloadValue("host_id")
	if p.hosts.connected(hostID) {
		return fmt.Errorf("主机 %q 的连接仍然保持，释放没有生效", hostID)
	}
	return nil
}

// Snapshot 返回仍保持连接的主机。
func (p *SSHHostProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	return p.hosts.snapshot(), nil
}

// SSHSessionProvider 分配一条用例独占的 SSH 连接。
//
// 会话是一条独立的连接（不与共享连接复用），因此一条会话的关闭不会影响其他用例。
type SSHSessionProvider struct {
	hosts    *remoteHosts
	mu       sync.Mutex
	sessions map[string]*sshSessionEntry
}

// sshSessionEntry 是一条已建立的会话记录。
type sshSessionEntry struct {
	handle Handle
	client sshclient.Client
}

// NewSSHSessionProvider 创建 SSH 会话提供者。
func NewSSHSessionProvider(hosts *remoteHosts) *SSHSessionProvider {
	return &SSHSessionProvider{hosts: hosts, sessions: make(map[string]*sshSessionEntry)}
}

// Acquire 建立独立连接并执行一次探测命令，句柄给出 session_id 与 host_id。
func (p *SSHSessionProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	hostID := specValue(decl, "host_id")
	host, err := p.hosts.config(hostID)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	client, _, err := dialRemoteHost(ctx, host)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	result, err := client.Run(ctx, "printf ok", sshclient.RunOptions{Timeout: resourceProbeTimeout})
	if err != nil || !result.Succeeded() {
		_ = client.Close()
		return Handle{}, fmt.Errorf("分配资源 %q 时会话探测失败: %v", decl.Name, runErrorText(err, result))
	}
	token, err := randomToken(6)
	if err != nil {
		_ = client.Close()
		return Handle{}, err
	}
	handle, err := buildHandle(decl, map[string]string{
		"session_id": token,
		"host_id":    host.HostID,
	}, "")
	if err != nil {
		_ = client.Close()
		return Handle{}, err
	}
	p.mu.Lock()
	p.sessions[token] = &sshSessionEntry{handle: handle, client: client}
	p.mu.Unlock()
	return handle, nil
}

// Release 关闭会话连接，并自证连接已不可用，可重复调用。
func (p *SSHSessionProvider) Release(ctx context.Context, handle Handle) error {
	sessionID := handle.PayloadValue("session_id")
	p.mu.Lock()
	entry := p.sessions[sessionID]
	delete(p.sessions, sessionID)
	p.mu.Unlock()
	if entry == nil {
		return nil
	}
	if err := entry.client.Close(); err != nil {
		return fmt.Errorf("关闭会话 %s 的连接失败: %w", sessionID, err)
	}
	probe, err := entry.client.Run(ctx, "printf ok", sshclient.RunOptions{Timeout: resourceProbeTimeout})
	if err == nil && probe.Succeeded() {
		return fmt.Errorf("会话 %s 关闭后仍然可以执行远程命令", sessionID)
	}
	return nil
}

// Verify 自证提供者不再持有该会话。
func (p *SSHSessionProvider) Verify(ctx context.Context, handle Handle) error {
	sessionID := handle.PayloadValue("session_id")
	p.mu.Lock()
	_, tracked := p.sessions[sessionID]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("会话 %s 仍被提供者持有，释放没有生效", sessionID)
	}
	return nil
}

// Snapshot 返回仍被持有的会话。
func (p *SSHSessionProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.sessions))
	for sessionID, entry := range p.sessions {
		entries = append(entries, LiveResource{
			Handle: entry.handle,
			Detail: "仍保持的 SSH 会话 " + sessionID,
		})
	}
	return entries, nil
}

// runErrorText 把命令结果与错误压成一句可读说明。
func runErrorText(err error, result sshclient.CommandResult) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("退出码 %d：%s", result.ExitCode, strings.TrimSpace(result.Stderr))
}

// runnerHostIDs 返回已构造 Docker 执行器的主机标识，按名称排序。
func (r *remoteHosts) runnerHostIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.runners))
	for hostID := range r.runners {
		ids = append(ids, hostID)
	}
	sort.Strings(ids)
	return ids
}
