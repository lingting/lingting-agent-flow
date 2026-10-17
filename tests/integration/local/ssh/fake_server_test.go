package sshclient_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/tests/internal/testkit"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// testUser 是测试 SSH 服务的登录用户名。
const testUser = "tester"

// testPassword 是测试 SSH 服务的登录密码。
const testPassword = "test-password"

// testServerOptions 控制测试 SSH 服务的行为。
type testServerOptions struct {
	// AuthorizedKey 非空时只接受该公钥认证；为空时接受任意公钥。
	AuthorizedKey ssh.PublicKey
	// RejectPassword 为 true 时拒绝密码认证，用于验证只接受私钥的场景。
	RejectPassword bool
}

// testServer 是本地测试用 SSH/SFTP 服务端。
//
// 它只用于测试：真实握手、真实认证、真实主机密钥校验与真实 SFTP 传输，
// 但不依赖任何外部服务或网络下载。
type testServer struct {
	t        *testing.T
	listener net.Listener
	hostKey  ssh.Signer
	config   *ssh.ServerConfig
	options  testServerOptions

	mu sync.Mutex
	// sessions 是当前活动的会话通道数量，用于验证资源释放。
	sessions int
	// connections 是当前活动的 SSH 连接数量。
	connections int
	// executing 是当前正在执行命令的会话数量，用于验证取消一个命令不影响其他调用。
	executing int
	// maxExecuting 是观察到的并发执行命令数量峰值。
	maxExecuting int
	closed       bool
	done         chan struct{}
}

// newTestServer 启动一个本地测试 SSH 服务。
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	return newTestServerWithOptions(t, testServerOptions{})
}

// newTestServerWithOptions 按指定选项启动一个本地测试 SSH 服务。
func newTestServerWithOptions(t *testing.T, options testServerOptions) *testServer {
	t.Helper()
	server := &testServer{t: t, done: make(chan struct{}), options: options}
	server.hostKey = generateHostKey(t)
	server.config = &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if options.RejectPassword {
				return nil, errors.New("认证失败")
			}
			if conn.User() == testUser && string(password) == testPassword {
				return nil, nil
			}
			return nil, errors.New("认证失败")
		},
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if conn.User() != testUser {
				return nil, errors.New("认证失败")
			}
			if options.AuthorizedKey != nil &&
				string(options.AuthorizedKey.Marshal()) != string(key.Marshal()) {
				return nil, errors.New("认证失败")
			}
			return nil, nil
		},
	}
	server.config.AddHostKey(server.hostKey)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试 SSH 服务失败: %v", err)
	}
	server.listener = listener
	go server.acceptLoop()
	t.Cleanup(server.close)
	return server
}

// generateHostKey 生成一次性主机密钥。
func generateHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成主机密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("构造主机密钥签名器失败: %v", err)
	}
	return signer
}

// endpoint 返回连接该服务的测试端点。
func (s *testServer) endpoint() (host string, port int) {
	address, ok := s.listener.Addr().(*net.TCPAddr)
	if !ok {
		s.t.Fatalf("监听地址类型不符: %T", s.listener.Addr())
	}
	return address.IP.String(), address.Port
}

// hostKeyFingerprint 返回测试服务主机密钥的 SHA-256 指纹。
func (s *testServer) hostKeyFingerprint() string {
	return ssh.FingerprintSHA256(s.hostKey.PublicKey())
}

// acceptLoop 接受连接并逐个处理。
func (s *testServer) acceptLoop() {
	defer close(s.done)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

// handleConn 完成一次 SSH 握手并按通道类型提供服务。
func (s *testServer) handleConn(raw net.Conn) {
	defer func() {
		_ = raw.Close()
	}()
	serverConn, channels, requests, err := ssh.NewServerConn(raw, s.config)
	if err != nil {
		return
	}
	defer func() {
		_ = serverConn.Close()
	}()
	go ssh.DiscardRequests(requests)
	s.trackConnection(1)
	defer s.trackConnection(-1)
	for newChannel := range channels {
		go s.handleChannel(newChannel)
	}
}

// trackSession 记录当前活动会话数，用于验证资源释放。
func (s *testServer) trackSession(delta int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions += delta
}

// trackConnection 记录当前活动的 SSH 连接数。
func (s *testServer) trackConnection(delta int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connections += delta
}

// activeSessions 返回当前活动会话数。
func (s *testServer) activeSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

// activeConnections 返回当前活动的 SSH 连接数。
func (s *testServer) activeConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections
}

// beginExec 记录一个开始执行的命令会话。
func (s *testServer) beginExec() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executing++
	if s.executing > s.maxExecuting {
		s.maxExecuting = s.executing
	}
}

// endExec 记录一个执行结束的命令会话。
func (s *testServer) endExec() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executing--
}

// executingPeak 返回观察到的并发执行命令数量峰值。
func (s *testServer) executingPeak() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxExecuting
}

// handleChannel 按通道类型分派处理。
func (s *testServer) handleChannel(newChannel ssh.NewChannel) {
	switch newChannel.ChannelType() {
	case "session":
		s.handleSession(newChannel)
	case "direct-tcpip":
		_ = newChannel.Reject(ssh.Prohibited, "不支持端口转发")
	default:
		_ = newChannel.Reject(ssh.UnknownChannelType, "不支持的通道类型")
	}
}

// handleSession 处理会话通道：支持 exec 与 sftp 子系统。
//
// exec 在独立 goroutine 中执行，使主循环能够继续处理 signal 请求；
// 客户端关闭会话通道时请求循环结束，长命令通过 closed 通道感知并结束。
func (s *testServer) handleSession(newChannel ssh.NewChannel) {
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return
	}
	closed := make(chan struct{})
	defer func() {
		close(closed)
		_ = channel.Close()
	}()
	s.trackSession(1)
	defer s.trackSession(-1)
	for request := range requests {
		switch request.Type {
		case "exec":
			// 回放 exit-status 前不能关闭通道，因此把执行放到独立 goroutine，
			// 并让请求循环继续处理 signal 与关闭事件。
			_ = request.Reply(true, nil)
			go s.handleExec(channel, request, closed)
		case "subsystem":
			if !s.handleSubsystem(channel, request) {
				return
			}
			return
		case "signal":
			// 测试服务不真正转发信号：客户端中断命令依赖关闭会话通道。
			_ = request.Reply(true, nil)
		case "env", "pty-req":
			_ = request.Reply(true, nil)
		default:
			_ = request.Reply(false, nil)
		}
	}
}

// execRequest 是 exec 请求的参数。
type execRequest struct {
	Command string
}

// handleExec 执行一条受控的测试命令。
//
// 只支持测试所需的固定命令，避免在测试里实现完整的 shell。
func (s *testServer) handleExec(
	channel ssh.Channel,
	request *ssh.Request,
	closed <-chan struct{},
) {
	var payload execRequest
	if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
		return
	}
	// exec 请求已在 handleSession 中回复，这里只执行命令并回放退出状态。
	s.beginExec()
	code := s.runCommand(channel, payload.Command, closed)
	s.endExec()
	status := struct{ Status uint32 }{Status: uint32(code)}
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&status))
	// 命令结束后必须关闭通道，客户端才会认为会话已结束并返回 Wait。
	_ = channel.Close()
}

// runCommand 执行受支持的测试命令并返回退出码。
func (s *testServer) runCommand(
	channel ssh.Channel,
	command string,
	closed <-chan struct{},
) int {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return 1
	}
	switch fields[0] {
	case "printf":
		_, _ = channel.Write([]byte(testRemoteHome()))
		return 0
	case "id":
		_, _ = channel.Write([]byte(testUser + "\n"))
		return 0
	case "test":
		return 0
	case "fail":
		_, _ = channel.Write([]byte("stdout-line\n"))
		_, _ = channel.Stderr().Write([]byte("stderr-line\n"))
		return 7
	case "stream":
		_, _ = channel.Write([]byte("out"))
		_, _ = channel.Stderr().Write([]byte("err"))
		return 0
	case "big":
		_, _ = channel.Write([]byte(strings.Repeat("x", 64*1024)))
		return 0
	case "slow":
		// 客户端关闭会话后立即结束，用于验证取消与超时确实关闭了远程会话，
		// 而不是只让本地停止等待。
		select {
		case <-time.After(5 * time.Second):
		case <-closed:
		}
		return 0
	case "exit":
		if len(fields) < 2 {
			return 1
		}
		code, err := strconv.Atoi(fields[1])
		if err != nil {
			return 1
		}
		_, _ = channel.Write([]byte("exiting\n"))
		return code
	case "sleepy":
		// 与 slow 相同，但用于并发场景下观察多个命令是否同时在执行。
		select {
		case <-time.After(400 * time.Millisecond):
		case <-closed:
		}
		return 0
	default:
		_, _ = channel.Stderr().Write([]byte("unknown command\n"))
		return 127
	}
}

// handleSubsystem 处理 sftp 子系统请求。
func (s *testServer) handleSubsystem(channel ssh.Channel, request *ssh.Request) bool {
	var payload struct{ Name string }
	if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
		_ = request.Reply(false, nil)
		return false
	}
	if payload.Name != "sftp" {
		_ = request.Reply(false, nil)
		return true
	}
	_ = request.Reply(true, nil)
	server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(os.TempDir()))
	if err != nil {
		return false
	}
	if err := server.Serve(); err != nil && !errors.Is(err, io.EOF) {
		s.t.Logf("测试 SFTP 服务结束: %v", err)
	}
	_ = server.Close()
	return false
}

// close 关闭测试服务。
func (s *testServer) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.listener.Close()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
	}
}

// writeKnownHosts 把测试服务的主机密钥写入 known_hosts 文件。
func writeKnownHosts(t *testing.T, host string, port int, key ssh.PublicKey) string {
	t.Helper()
	path := filepath.Join(testkit.Dir(t), "known_hosts")
	address := net.JoinHostPort(host, strconv.Itoa(port))
	line := knownhosts.Line([]string{knownhosts.Normalize(address)}, key) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}
	return path
}

// generateEd25519KeyPair 生成测试用 Ed25519 密钥对。
func generateEd25519KeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥对失败: %v", err)
	}
	return publicKey, privateKey
}

// plainAddress 是本地普通 TCP 服务地址。
type plainAddress struct {
	Host string
	Port int
}

// startPlainTCPServer 启动一个返回固定内容的普通 TCP 服务。
func startPlainTCPServer(t *testing.T, banner string) (plainAddress, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试 TCP 服务失败: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() {
					_ = conn.Close()
				}()
				_, _ = conn.Write([]byte(banner))
				time.Sleep(200 * time.Millisecond)
			}(conn)
		}
	}()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	stop := func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	return plainAddress{Host: address.IP.String(), Port: address.Port}, stop
}

// testRemoteHome 返回测试 SSH 服务报告的远程 Home 目录。
//
// 远程节点按 Linux 语义处理，因此这里始终给出 POSIX 绝对路径；
// 该路径与测试 SFTP 服务的根目录映射一致，使工作空间操作可以真实落盘。
func testRemoteHome() string {
	slashed := filepath.ToSlash(os.TempDir())
	if !strings.HasPrefix(slashed, "/") {
		return "/" + slashed
	}
	return slashed
}
