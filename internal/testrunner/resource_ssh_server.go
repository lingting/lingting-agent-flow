package testrunner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// localSSHUser 是测试 SSH 服务端的登录用户名。
const localSSHUser = "tester"

// LocalSSHServerProvider 在编排进程内启动一个真实的 SSH/SFTP 服务端。
//
// 服务端只接受本次分配生成的一把客户端公钥，主机密钥也由本次分配生成：分配阶段会用
// 生成的私钥真实握手并执行一次命令，确认服务可用后才下发句柄。句柄给出 address、
// host_key（known_hosts 行）、user 与客户端私钥路径，用例可以直接用 sshclient 连接。
type LocalSSHServerProvider struct {
	root    string
	mu      sync.Mutex
	servers map[string]*localSSHServerEntry
}

// localSSHServerEntry 是一条已启动的测试 SSH 服务记录。
type localSSHServerEntry struct {
	handle    Handle
	listener  net.Listener
	config    *ssh.ServerConfig
	keyDir    string
	done      chan struct{}
	closeOnce sync.Once

	connMu sync.Mutex
	conns  map[net.Conn]struct{}
}

// NewLocalSSHServerProvider 创建一个以 root 为工作根目录的测试 SSH 服务提供者。
func NewLocalSSHServerProvider(root string) *LocalSSHServerProvider {
	return &LocalSSHServerProvider{root: root, servers: make(map[string]*localSSHServerEntry)}
}

// Acquire 启动服务端并完成一次真实握手，句柄给出连接与认证所需的全部信息。
func (p *LocalSSHServerProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	token, err := randomToken(4)
	if err != nil {
		return Handle{}, err
	}
	hostSigner, err := newEd25519Signer()
	if err != nil {
		return Handle{}, err
	}
	clientPublic, clientPrivatePEM, err := newEd25519ClientKey()
	if err != nil {
		return Handle{}, err
	}
	keyDir := filepath.Join(p.root, "local-ssh-server", safeName(decl.Name)+"-"+token)
	if err := os.MkdirAll(keyDir, 0o750); err != nil {
		return Handle{}, fmt.Errorf("创建资源 %q 的目录失败: %w", decl.Name, err)
	}
	keyPath := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(keyPath, clientPrivatePEM, 0o600); err != nil {
		return Handle{}, fmt.Errorf("写出资源 %q 的客户端私钥失败: %w", decl.Name, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的监听端口失败: %w", decl.Name, err)
	}
	config := localSSHServerConfig(hostSigner, clientPublic)
	entry := &localSSHServerEntry{
		listener: listener,
		config:   config,
		keyDir:   keyDir,
		done:     make(chan struct{}),
		conns:    make(map[net.Conn]struct{}),
	}
	go entry.serve()
	address := listener.Addr().String()
	if err := probeSSHLogin(ctx, address, clientPrivatePEM, hostSigner.PublicKey()); err != nil {
		_ = entry.close()
		_ = os.RemoveAll(keyDir)
		return Handle{}, fmt.Errorf("分配资源 %q 时 SSH 服务没有就绪: %w", decl.Name, err)
	}
	handle, err := buildHandle(decl, map[string]string{
		"address":          address,
		"host_key":         knownHostsLine(listener.Addr(), hostSigner.PublicKey()),
		"user":             localSSHUser,
		"private_key_path": keyPath,
	}, "")
	if err != nil {
		_ = entry.close()
		_ = os.RemoveAll(keyDir)
		return Handle{}, err
	}
	entry.handle = handle
	p.mu.Lock()
	p.servers[address] = entry
	p.mu.Unlock()
	return handle, nil
}

// Release 关闭监听器、断开全部连接并删除客户端私钥，可重复调用。
func (p *LocalSSHServerProvider) Release(ctx context.Context, handle Handle) error {
	address := handle.PayloadValue("address")
	p.mu.Lock()
	entry := p.servers[address]
	delete(p.servers, address)
	p.mu.Unlock()
	if entry == nil {
		return nil
	}
	if err := entry.close(); err != nil {
		return fmt.Errorf("释放资源 %q 时关闭服务失败: %w", handle.Name, err)
	}
	if err := os.RemoveAll(entry.keyDir); err != nil {
		return fmt.Errorf("删除资源 %q 的客户端私钥失败: %w", handle.Name, err)
	}
	return nil
}

// Verify 自证端口不再接受连接且客户端私钥已删除。
func (p *LocalSSHServerProvider) Verify(ctx context.Context, handle Handle) error {
	address := handle.PayloadValue("address")
	p.mu.Lock()
	_, tracked := p.servers[address]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("SSH 服务 %s 仍被提供者持有，释放没有生效", address)
	}
	if err := verifyPortClosed(ctx, address); err != nil {
		return err
	}
	return verifyFileAbsent(filepath.Dir(handle.PayloadValue("private_key_path")))
}

// Snapshot 返回仍在监听的测试 SSH 服务。
func (p *LocalSSHServerProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.servers))
	for address, entry := range p.servers {
		entries = append(entries, LiveResource{
			Handle: entry.handle,
			Detail: fmt.Sprintf("监听中的测试 SSH 服务 %s（活动连接 %d）", address, entry.activeConns()),
		})
	}
	return entries, nil
}

// localSSHServerConfig 构造只信任一把客户端公钥的服务端配置。
func localSSHServerConfig(hostKey ssh.Signer, authorized ssh.PublicKey) *ssh.ServerConfig {
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorized.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("资源提供者拒绝未授权的客户端公钥")
		},
	}
	config.AddHostKey(hostKey)
	return config
}

// serve 接受连接直到监听器被关闭。
func (e *localSSHServerEntry) serve() {
	defer close(e.done)
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			return
		}
		e.track(conn)
		go e.handleConn(conn)
	}
}

// handleConn 完成 SSH 握手并处理 session 通道。
func (e *localSSHServerEntry) handleConn(raw net.Conn) {
	defer e.untrack(raw)
	defer func() { _ = raw.Close() }()
	serverConn, channels, requests, err := ssh.NewServerConn(raw, e.config)
	if err != nil {
		return
	}
	defer func() { _ = serverConn.Close() }()
	go func() {
		for request := range requests {
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
		}
	}()
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "只支持 session 通道")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go e.serveSession(channel, channelRequests)
	}
}

// serveSession 支持 exec 与 sftp 子系统，其余请求一律拒绝。
func (e *localSSHServerEntry) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()
	for request := range requests {
		switch request.Type {
		case "subsystem":
			var payload struct{ Name string }
			if err := ssh.Unmarshal(request.Payload, &payload); err != nil || payload.Name != "sftp" {
				_ = request.Reply(false, nil)
				continue
			}
			if err := request.Reply(true, nil); err != nil {
				return
			}
			server, err := sftp.NewServer(channel)
			if err != nil {
				return
			}
			_ = server.Serve()
			_ = server.Close()
			return
		case "exec":
			if err := request.Reply(true, nil); err != nil {
				return
			}
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

// close 关闭监听器与全部活动连接，可重复调用。
func (e *localSSHServerEntry) close() error {
	e.closeOnce.Do(func() {
		_ = e.listener.Close()
		e.connMu.Lock()
		conns := make([]net.Conn, 0, len(e.conns))
		for conn := range e.conns {
			conns = append(conns, conn)
		}
		e.connMu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	<-e.done
	return nil
}

func (e *localSSHServerEntry) track(conn net.Conn) {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	e.conns[conn] = struct{}{}
}

func (e *localSSHServerEntry) untrack(conn net.Conn) {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	delete(e.conns, conn)
}

func (e *localSSHServerEntry) activeConns() int {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	return len(e.conns)
}

// newEd25519Signer 生成一个 ed25519 主机密钥。
func newEd25519Signer() (ssh.Signer, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成主机密钥失败: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, fmt.Errorf("构造主机密钥签名器失败: %w", err)
	}
	return signer, nil
}

// newEd25519ClientKey 生成客户端密钥对，返回公钥与 OpenSSH 私钥 PEM。
func newEd25519ClientKey() (ssh.PublicKey, []byte, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成客户端密钥失败: %w", err)
	}
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		return nil, nil, fmt.Errorf("构造客户端公钥失败: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		return nil, nil, fmt.Errorf("编码客户端私钥失败: %w", err)
	}
	return sshPublic, pem.EncodeToMemory(block), nil
}

// knownHostsLine 构造一行 known_hosts 记录；端口不是 22 时使用 [host]:port 写法。
func knownHostsLine(address net.Addr, key ssh.PublicKey) string {
	host, port, err := net.SplitHostPort(address.String())
	if err != nil {
		host = address.String()
		port = ""
	}
	hostField := host
	if port != "" && port != "22" {
		hostField = "[" + host + "]:" + port
	}
	return hostField + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// probeSSHLogin 用生成的客户端私钥真实握手并执行一次命令，用于就绪判定。
func probeSSHLogin(ctx context.Context, address string, privateKeyPEM []byte, hostKey ssh.PublicKey) error {
	signer, err := ssh.ParsePrivateKey(privateKeyPEM)
	if err != nil {
		return fmt.Errorf("解析客户端私钥失败: %w", err)
	}
	config := &ssh.ClientConfig{
		User:            localSSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         resourceProbeTimeout,
	}
	dialer := net.Dialer{Timeout: resourceProbeTimeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("连接测试 SSH 服务失败: %w", err)
	}
	clientConn, channels, requests, err := ssh.NewClientConn(rawConn, address, config)
	if err != nil {
		_ = rawConn.Close()
		return fmt.Errorf("测试 SSH 服务握手失败: %w", err)
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("打开测试 SSH 会话失败: %w", err)
	}
	defer func() { _ = session.Close() }()
	if err := session.Run("true"); err != nil {
		return fmt.Errorf("测试 SSH 服务执行命令失败: %w", err)
	}
	return nil
}
