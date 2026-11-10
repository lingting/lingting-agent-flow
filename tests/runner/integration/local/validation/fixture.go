// 环境预检用例共用的现场：迁移自 tests/integration/local/validation/validation_test.go 的辅助部分。
//
// 测试服务是真实的 SSH 服务端：真实握手、真实认证、真实主机密钥校验，并额外模拟远端
// Docker Engine 的 Unix 套接字，用于固定 API 基准的版本探测。
package validation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// testUser 是测试 SSH 服务的登录用户名。
const testUser = "validator"

// dockerSocketPath 是测试中约定的远端 Docker 套接字路径。
//
// 与 docker.DefaultDockerSocketPath 保持一致，使被测代码无需为测试改造。
const dockerSocketPath = "/var/run/docker.sock"

// engineBehaviour 控制假 Docker Engine 的响应。
type engineBehaviour struct {
	// requests 记录收到的请求行。
	requests []string
	// delay 是响应前的延迟，用于让取消发生在版本探测过程中。
	delay time.Duration
	// apiRange 覆盖 Engine 版本接口声明的 API 区间，零值表示满足基线。
	apiRange docker.APIRange
}

// serverOptions 控制假 SSH 服务的行为。
type serverOptions struct {
	// missingOption 非空时从帮助文本中删除该选项，用于验证参数核对。
	missingOption string
	// helpExitCode 非 0 时帮助命令以该退出码结束。
	helpExitCode int
	// rejectCommand 非空时该远程命令返回 127。
	rejectCommand string
	// cliVersion 覆盖 docker version 报告的 CLI 版本。
	cliVersion string
	// engineVersion 覆盖 docker info 报告的 Engine 版本。
	engineVersion string
	// engine 是假 Docker Engine 的行为。
	engine *engineBehaviour
	// authorizedKey 非空时只接受该公钥，用于验证认证失败路径。
	authorizedKey ssh.PublicKey
}

// testServer 是进程内的测试 SSH 服务端。
type testServer struct {
	t        *testrunner.Context
	listener net.Listener
	hostKey  ssh.Signer
	config   *ssh.ServerConfig
	options  serverOptions

	mu      sync.Mutex
	closed  bool
	done    chan struct{}
	command []string
}

// newTestServer 启动测试 SSH 服务。
func newTestServer(t *testrunner.Context, options serverOptions) *testServer {
	t.Helper()
	server := &testServer{t: t, options: options, done: make(chan struct{})}
	server.hostKey = generateHostKey(t)
	server.config = &ssh.ServerConfig{
		// 本版本只支持私钥认证：测试服务只校验用户名，公钥由客户端私钥提供。
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if conn.User() != testUser {
				return nil, errors.New("认证失败")
			}
			if options.authorizedKey != nil &&
				string(options.authorizedKey.Marshal()) != string(key.Marshal()) {
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
func generateHostKey(t *testrunner.Context) ssh.Signer {
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

// endpoint 返回服务监听地址。
func (s *testServer) endpoint() (string, int) {
	address, ok := s.listener.Addr().(*net.TCPAddr)
	if !ok {
		s.t.Fatalf("监听地址类型不符: %T", s.listener.Addr())
	}
	return address.IP.String(), address.Port
}

// recordedCommands 返回收到的全部 exec 命令。
func (s *testServer) recordedCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.command...)
}

// acceptLoop 接受连接。
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

// handleConn 完成握手并分派通道。
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
	for newChannel := range channels {
		go s.handleChannel(newChannel)
	}
}

// handleChannel 按通道类型分派。
func (s *testServer) handleChannel(newChannel ssh.NewChannel) {
	switch newChannel.ChannelType() {
	case "session":
		s.handleSession(newChannel)
	case "direct-streamlocal@openssh.com":
		s.handleStreamLocal(newChannel)
	default:
		_ = newChannel.Reject(ssh.UnknownChannelType, "不支持的通道类型")
	}
}

// handleSession 处理 exec 请求。
func (s *testServer) handleSession(newChannel ssh.NewChannel) {
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return
	}
	defer func() {
		_ = channel.Close()
	}()
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		_ = request.Reply(true, nil)
		s.mu.Lock()
		s.command = append(s.command, payload.Command)
		s.mu.Unlock()
		code := s.runCommand(channel, payload.Command)
		status := struct{ Status uint32 }{Status: uint32(code)}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&status))
		return
	}
}

// runCommand 执行受支持的测试命令并返回退出码。
func (s *testServer) runCommand(channel ssh.Channel, command string) int {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return 1
	}
	if s.options.rejectCommand != "" && strings.HasPrefix(command, s.options.rejectCommand) {
		_, _ = channel.Stderr().Write([]byte("command not found\n"))
		return 127
	}
	switch {
	case fields[0] == "true":
		return 0
	case fields[0] == "env":
		// 版本探测通过 env -u DOCKER_API_VERSION docker ... 隔离遗留变量，
		// 因此这里必须先剥掉 env 前缀再分派到 docker。
		return s.runEnvWrapped(channel, fields[1:])
	case fields[0] == "docker":
		return s.runDocker(channel, fields[1:])
	default:
		_, _ = channel.Stderr().Write([]byte("unknown command\n"))
		return 127
	}
}

// runEnvWrapped 处理 env 前缀后的 docker 命令。
func (s *testServer) runEnvWrapped(channel ssh.Channel, fields []string) int {
	index := 0
	for index < len(fields) {
		field := fields[index]
		switch {
		case field == "-u":
			index += 2
			continue
		case strings.Contains(field, "="):
			index++
			continue
		}
		break
	}
	if index >= len(fields) || fields[index] != "docker" {
		_, _ = channel.Stderr().Write([]byte("unexpected env command\n"))
		return 64
	}
	return s.runDocker(channel, fields[index+1:])
}

// runDocker 输出模拟的 docker 版本信息与帮助信息。
//
// 版本探测与帮助检查都必须被真实响应：前者返回满足基线的 CLI / Engine 版本，
// 后者返回覆盖全部实际使用参数的帮助文本。任何其他 docker 命令都会返回明确失败，
// 便于测试发现校验流程中混入了变更命令（create / run / pull 等）。
func (s *testServer) runDocker(channel ssh.Channel, args []string) int {
	if len(args) == 0 {
		_, _ = channel.Write([]byte("Usage: docker [OPTIONS] COMMAND\n"))
		return 0
	}
	// 帮助检查优先：`docker version --help` 与 `docker info --help` 必须返回帮助文本，
	// 不能因为子命令同名而被误当成版本查询。
	if args[len(args)-1] == "--help" {
		if s.options.helpExitCode != 0 {
			_, _ = channel.Stderr().Write([]byte("help failed\n"))
			return s.options.helpExitCode
		}
		text := dockerHelpText()
		if s.options.missingOption != "" {
			text = removeOption(text, s.options.missingOption)
		}
		// 帮助文本写入标准错误，验证检查会同时读取两个输出流。
		_, _ = channel.Stderr().Write([]byte(text))
		return 0
	}
	switch args[0] {
	case "version":
		_, _ = channel.Write([]byte(s.cliVersion() + "\n"))
		return 0
	case "info":
		_, _ = channel.Write([]byte(s.engineVersion() + "\n"))
		return 0
	}
	_, _ = channel.Stderr().Write([]byte("unexpected non-help docker command\n"))
	return 64
}

// cliVersion 返回假 CLI 报告的版本。
func (s *testServer) cliVersion() string {
	if s.options.cliVersion != "" {
		return s.options.cliVersion
	}
	return docker.MinimumCLIVersion
}

// engineVersion 返回假 Engine 报告的版本。
func (s *testServer) engineVersion() string {
	if s.options.engineVersion != "" {
		return s.options.engineVersion
	}
	return docker.MinimumEngineVersion
}

// handleStreamLocal 处理远端 Unix 套接字通道，模拟 Docker Engine HTTP 接口。
func (s *testServer) handleStreamLocal(newChannel ssh.NewChannel) {
	// 载荷结构必须与 x/crypto/ssh 的 direct-streamlocal 消息完全一致，
	// 否则 Unmarshal 会因为剩余字节不匹配而失败。
	var payload struct {
		SocketPath string
		Reserved0  string
		Reserved1  uint32
	}
	if err := ssh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
		_ = newChannel.Reject(ssh.Prohibited, "无法解析套接字路径")
		return
	}
	if payload.SocketPath != dockerSocketPath {
		_ = newChannel.Reject(ssh.Prohibited, "未知套接字路径")
		return
	}
	if s.options.engine == nil {
		_ = newChannel.Reject(ssh.Prohibited, "未配置假 Engine")
		return
	}
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return
	}
	defer func() {
		_ = channel.Close()
	}()
	go ssh.DiscardRequests(requests)
	buffer := make([]byte, 8192)
	count, readErr := channel.Read(buffer)
	if readErr != nil {
		return
	}
	request := string(buffer[:count])
	line := request
	if index := strings.Index(request, "\r\n"); index >= 0 {
		line = request[:index]
	}
	s.mu.Lock()
	s.options.engine.requests = append(s.options.engine.requests, line)
	s.mu.Unlock()

	if delay := s.options.engine.delay; delay > 0 {
		time.Sleep(delay)
	}
	// 版本探测使用固定基准路径 /v1.52/version，这是预检唯一允许的 Engine API 请求。
	if !strings.Contains(line, "/version") {
		// 未预期的 Engine API 请求（例如已移除的 Distribution Inspect）必须让断言失败。
		s.t.Errorf("收到未预期的 Engine API 请求: %s", line)
		s.writeEngineResponse(channel, 404, `{"message":"unexpected request"}`)
		return
	}
	status, body := s.engineVersionResponse()
	s.writeEngineResponse(channel, status, body)
}

// engineVersionResponse 返回 Engine 版本接口的响应。
//
// 默认返回满足基线的响应；需要模拟 API 不兼容时由测试覆盖 apiRange 字段。
func (s *testServer) engineVersionResponse() (int, string) {
	s.mu.Lock()
	apiRange := s.options.engine.apiRange
	s.mu.Unlock()
	maximum := apiRange.Maximum
	if maximum == "" {
		maximum = docker.EngineAPIBaseVersion
	}
	minimum := apiRange.Minimum
	if minimum == "" {
		minimum = "1.24"
	}
	body := fmt.Sprintf(
		`{"ApiVersion":%q,"MinAPIVersion":%q,"Version":%q}`,
		maximum,
		minimum,
		s.engineVersion(),
	)
	return 200, body
}

// writeEngineResponse 写出一条最小的 Engine API 响应。
func (s *testServer) writeEngineResponse(channel ssh.Channel, status int, body string) {
	response := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n"+
			"Connection: close\r\n\r\n%s",
		status,
		httpStatusText(status),
		len(body),
		body,
	)
	_, _ = channel.Write([]byte(response))
}

// httpStatusText 返回状态码对应的原因短语。
func httpStatusText(status int) string {
	switch status {
	case 200:
		return "OK"
	case 404:
		return "Not Found"
	default:
		return "Error"
	}
}

// dockerHelpText 返回覆盖全部实际使用参数的帮助文本。
//
// 内容必须与 internal/docker 的 RequiredCLICommands 保持一致：真实校验会逐项核对这些
// 选项，缺少任一项都会被判定为 abnormal，因此这里不能裁剪生产代码要求核验的参数。
func dockerHelpText() string {
	return strings.Join([]string{
		"Options:",
		"--name --detach --workdir --label --mount --read-only --tmpfs",
		"--time --force --interactive --env --tail",
		"--rm --entrypoint --all --filter --format",
		"--tag --file --build-arg --no-cache --progress",
		"",
	}, "\n")
}

// removeOption 从帮助文本中删除指定选项。
func removeOption(text, option string) string {
	return strings.ReplaceAll(text, option, "--removed-option")
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

// writeKnownHosts 把测试服务的主机密钥写入 known_hosts。
func writeKnownHosts(t *testrunner.Context, host string, port int, key ssh.PublicKey) string {
	t.Helper()
	workspace := testkit.NewAt(t, t.WorkspaceDir())
	path := filepath.Join(workspace.Dir("ssh"), "known_hosts")
	address := net.JoinHostPort(host, strconv.Itoa(port))
	line := knownhosts.Line([]string{knownhosts.Normalize(address)}, key) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}
	return path
}

// writePrivateKeyFile 生成一个测试用未加密私钥并返回其路径。
//
// 本版本只支持无口令私钥，因此测试也只生成未加密私钥。
func writePrivateKeyFile(t *testrunner.Context) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "agentflow-validation")
	if err != nil {
		t.Fatalf("编码测试私钥失败: %v", err)
	}
	workspace := testkit.NewAt(t, t.WorkspaceDir())
	path := filepath.Join(workspace.Dir("keys"), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	return path
}

// fixture 是一次校验测试的运行环境。
type fixture struct {
	store     *sqlite.DB
	host      domain.ExecutionHost
	validator *app.HostValidator
	server    *testServer
	// keyPath 是节点使用的本机私钥路径。
	keyPath string
}

// newFixture 启动测试服务并构造校验器与执行节点。
func newFixture(t *testrunner.Context, options serverOptions) *fixture {
	t.Helper()
	server := newTestServer(t, options)
	host, port := server.endpoint()
	knownHostsPath := writeKnownHosts(t, host, port, server.hostKey.PublicKey())

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: databasePath(t)})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	sshService, err := app.NewSSHService(db, logger, app.SSHOptions{
		KnownHostsPath: knownHostsPath,
	})
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	validator, err := app.NewHostValidator(app.HostValidationOptions{
		SSH:    sshService,
		Store:  db,
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("构造校验器失败: %v", err)
	}
	keyPath := writePrivateKeyFile(t)
	entity, err := domain.NewExecutionHost(
		"", "预检节点", host, port, testUser, "/tmp/agentflow-validation", time.Now(),
		domain.WithPrivateKey(keyPath),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, entity); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return &fixture{
		store:     db,
		host:      entity,
		validator: validator,
		server:    server,
		keyPath:   keyPath,
	}
}

// validate 使用测试密码执行一次校验。
func (f *fixture) validate(t *testrunner.Context, ctx context.Context) app.ValidationOutcome {
	t.Helper()
	outcome, err := f.validator.Validate(ctx, app.ValidationRequest{
		HostID: f.host.ID.String(),
	})
	if err != nil {
		t.Fatalf("执行校验失败: %v", err)
	}
	return outcome
}

// discard 丢弃日志输出。
type discard struct{}

// Write 丢弃内容。
func (discard) Write(p []byte) (int, error) { return len(p), nil }
