// Package validation_local 验证执行节点环境预检在本机进程内的完整流程。
//
// 测试在进程内启动一个真实的 SSH 服务端（真实握手、真实认证、真实主机密钥校验），
// 并让它同时提供：
//   - exec 会话：用于 SSH 检查与 Docker CLI 帮助信息检查；
//   - direct-streamlocal 通道：模拟远端 Docker Engine 的 Unix 套接字，
//     用于 Distribution Inspect 镜像元数据查询。
//
// 因此本测试覆盖的是真实的 SSH 与 HTTP 交互，而不是替身接口，同时不依赖外部
// SSH 机器、真实 Docker Engine 或网络。
package validation_local

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
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
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
	// status 是 Distribution Inspect 的 HTTP 状态码。
	status int
	// body 是响应体。
	body string
	// requests 记录收到的请求行。
	requests []string
	// delay 是响应前的延迟，用于让取消发生在查询过程中。
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
	t        *testing.T
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
func newTestServer(t *testing.T, options serverOptions) *testServer {
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
	case fields[0] == "printf":
		_, _ = channel.Write([]byte(remoteHome()))
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

	// 版本探测使用固定基准路径 /v1.52/version；镜像元数据查询使用
	// /v1.52/distribution/{name}/json。两者必须分别响应，否则无法区分
	// "API 基准可用" 与 "镜像元数据可查询"。
	if strings.Contains(line, "/version") {
		status, body := s.engineVersionResponse(line)
		s.writeEngineResponse(channel, status, body)
		return
	}

	if delay := s.options.engine.delay; delay > 0 {
		time.Sleep(delay)
	}
	s.writeEngineResponse(channel, s.options.engine.status, s.options.engine.body)
}

// engineVersionResponse 返回 Engine 版本接口的响应。
//
// 默认返回满足基线的响应；需要模拟 API 不兼容时由测试覆盖 apiRange 字段。
func (s *testServer) engineVersionResponse(line string) (int, string) {
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
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 404:
		return "Not Found"
	default:
		return "Error"
	}
}

// dockerHelpText 返回覆盖全部实际使用参数的帮助文本。
func dockerHelpText() string {
	return strings.Join([]string{
		"Options:",
		"--name --detach --workdir --label --mount",
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

// remoteHome 返回模拟的远程 Home 目录。
func remoteHome() string {
	slashed := filepath.ToSlash(os.TempDir())
	if !strings.HasPrefix(slashed, "/") {
		return "/" + slashed
	}
	return slashed
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
func newFixture(t *testing.T, options serverOptions) *fixture {
	t.Helper()
	server := newTestServer(t, options)
	host, port := server.endpoint()
	knownHostsPath := writeKnownHosts(t, host, port, server.hostKey.PublicKey())

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(testkit.Dir(t), "agentflow.db")})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	logger, err := logging.New(discard{}, logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	sshService, err := app.NewSSHService(db, logger, app.SSHOptions{
		KnownHostsPath: knownHostsPath,
		StorePath:      filepath.Join(testkit.Dir(t), "agentflow_known_hosts"),
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
func (f *fixture) validate(t *testing.T, ctx context.Context) app.ValidationOutcome {
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

// distributionBody 返回一份成功的镜像元数据响应。
func distributionBody() string {
	return `{"Descriptor":{"mediaType":"application/vnd.oci.image.index.v1+json",` +
		`"digest":"sha256:abcdef","platform":{"os":"linux","architecture":"amd64"}},` +
		`"Platforms":[{"os":"linux","architecture":"amd64"}]}`
}

// TestValidatePassesAllChecks 验证四项检查全部通过时总体状态为 normal。
func TestValidatePassesAllChecks(t *testing.T) {
	t.Parallel()
	engine := &engineBehaviour{status: 200, body: distributionBody()}
	fixture := newFixture(t, serverOptions{engine: engine})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Stale {
		t.Fatal("结果不应过期")
	}
	if outcome.Status != domain.ValidationStatusNormal {
		t.Fatalf("状态应为 normal: %s / %s", outcome.Status, outcome.Host.ValidationMessage)
	}
	if !outcome.Details.AllRequiredPassed() {
		t.Fatalf("四项检查应全部通过: %+v", outcome.Details.Checks)
	}
	if len(outcome.Details.Checks) != 4 {
		t.Fatalf("应执行四项检查: %d", len(outcome.Details.Checks))
	}
	versionCheck := outcome.Details.Checks[1]
	if versionCheck.Name != domain.ValidationCheckDockerVersion {
		t.Fatalf("第二项应为 Docker 版本检查: %s", versionCheck.Name)
	}
	imageCheck := outcome.Details.Checks[3]
	if len(imageCheck.Images) != 1 {
		t.Fatalf("应查询一个外部镜像: %+v", imageCheck.Images)
	}
	image := imageCheck.Images[0]
	if image.Reference != "node:22.19.0-bookworm-slim" {
		t.Fatalf("镜像引用不符: %s", image.Reference)
	}
	if image.Digest != "sha256:abcdef" {
		t.Fatalf("摘要不符: %s", image.Digest)
	}
	if len(image.Platforms) != 1 || image.Platforms[0] != "linux/amd64" {
		t.Fatalf("平台不符: %+v", image.Platforms)
	}
	// 能力边界说明必须存在，避免把 normal 误读为运行兼容性已验证。
	if len(outcome.Details.Capabilities) != 3 {
		t.Fatalf("能力边界说明缺失: %+v", outcome.Details.Capabilities)
	}
	// 请求必须是只读的：一次固定基准的版本探测 + 一次镜像元数据查询。
	if len(engine.requests) != 2 {
		t.Fatalf("应发起版本探测与镜像元数据查询各一次: %+v", engine.requests)
	}
	if !strings.Contains(engine.requests[0], "/v1.52/version") {
		t.Fatalf("版本探测必须使用固定 API 基准路径: %s", engine.requests[0])
	}
	if !strings.Contains(engine.requests[1], "/distribution/") ||
		!strings.Contains(engine.requests[1], "/json") {
		t.Fatalf("请求路径不符: %s", engine.requests[1])
	}
	for _, request := range engine.requests {
		if !strings.HasPrefix(request, "GET /v1.52/") {
			t.Fatalf("Engine API 请求必须使用固定基准路径: %s", request)
		}
	}
	// 校验只允许帮助命令，不得出现任何变更命令（create/run/pull 等）。
	// 校验只允许版本查询与帮助命令，不得出现 create / run / pull 等变更命令。
	for _, command := range fixture.server.recordedCommands() {
		if !strings.Contains(command, "docker ") {
			continue
		}
		if strings.Contains(command, " --help") {
			continue
		}
		if strings.Contains(command, "docker version") || strings.Contains(command, "docker info") {
			continue
		}
		t.Fatalf("校验执行了未预期的 Docker 命令: %s", command)
	}
}

// TestValidateFailsWhenSSHRejected 验证 SSH 认证失败时后续检查记为 not_run。
func TestValidateFailsWhenSSHRejected(t *testing.T) {
	t.Parallel()
	// 服务端只接受另一把公钥：客户端私钥认证必然被拒绝。
	otherKey := generateHostKey(t)
	fixture := newFixture(t, serverOptions{
		engine:        &engineBehaviour{status: 200},
		authorizedKey: otherKey.PublicKey(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome, err := fixture.validator.Validate(ctx, app.ValidationRequest{
		HostID: fixture.host.ID.String(),
	})
	if err != nil {
		t.Fatalf("校验本身不应返回错误: %v", err)
	}
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	if len(outcome.Details.Checks) != 4 {
		t.Fatalf("应保留四项检查结果: %d", len(outcome.Details.Checks))
	}
	if outcome.Details.Checks[0].Status.Passed() {
		t.Fatal("SSH 检查不应通过")
	}
	for _, check := range outcome.Details.Checks[1:] {
		if check.Status != domain.CheckStatusNotRun {
			t.Fatalf("SSH 失败时后续检查应为 not_run: %s=%s", check.Name, check.Status)
		}
	}
	if !strings.Contains(outcome.Details.Checks[0].Message, "认证") {
		t.Fatalf("失败原因应说明认证问题: %s", outcome.Details.Checks[0].Message)
	}
}

// TestValidateFailsWhenHelpExitNonZero 验证帮助命令非零退出导致 abnormal。
func TestValidateFailsWhenHelpExitNonZero(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, serverOptions{helpExitCode: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	if outcome.Details.Checks[2].Status.Passed() {
		t.Fatal("Docker 帮助检查不应通过")
	}
	if outcome.Details.Checks[3].Status != domain.CheckStatusNotRun {
		t.Fatalf("帮助检查失败时镜像检查应为 not_run: %s", outcome.Details.Checks[3].Status)
	}
}

// TestValidateFailsWhenOptionMissing 验证帮助信息缺少参数时给出定位信息。
func TestValidateFailsWhenOptionMissing(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, serverOptions{
		missingOption: "--mount",
		engine:        &engineBehaviour{status: 200, body: distributionBody()},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	check := outcome.Details.Checks[2]
	if check.Status.Passed() {
		t.Fatal("缺少参数时帮助检查不应通过")
	}
	if !strings.Contains(check.Message, "--mount") {
		t.Fatalf("失败原因应指明缺失参数: %s", check.Message)
	}
}

// TestValidateFailsWhenImageMissing 验证镜像不存在时判为 abnormal。
func TestValidateFailsWhenImageMissing(t *testing.T) {
	t.Parallel()
	engine := &engineBehaviour{status: 404, body: `{"message":"manifest unknown"}`}
	fixture := newFixture(t, serverOptions{engine: engine})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	check := outcome.Details.Checks[3]
	if check.Status.Passed() {
		t.Fatal("镜像不存在时检查不应通过")
	}
	if len(check.Images) != 1 {
		t.Fatalf("应记录镜像结果: %+v", check.Images)
	}
	if check.Images[0].Status != domain.CheckStatusFailed {
		t.Fatalf("镜像单项状态应为失败: %s", check.Images[0].Status)
	}
	if check.Images[0].ErrorCode == "" {
		t.Fatal("镜像失败应带错误代码")
	}
}

// TestValidateFailsWhenEngineUnsupported 验证 Engine 接口不兼容时判为 abnormal。
func TestValidateFailsWhenEngineUnsupported(t *testing.T) {
	t.Parallel()
	engine := &engineBehaviour{status: 400, body: `{"message":"bad request"}`}
	fixture := newFixture(t, serverOptions{engine: engine})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	if outcome.Details.Checks[3].Status.Passed() {
		t.Fatal("接口不兼容时镜像检查不应通过")
	}
}

// TestValidateRecordsTimestamps 验证校验结果记录了开始与完成时间。
func TestValidateRecordsTimestamps(t *testing.T) {
	t.Parallel()
	engine := &engineBehaviour{status: 200, body: distributionBody()}
	fixture := newFixture(t, serverOptions{engine: engine})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Host.ValidationStartedAt == nil {
		t.Fatal("应记录开始时间")
	}
	if outcome.Host.ValidationCompletedAt == nil {
		t.Fatal("应记录完成时间")
	}
	if outcome.Details.ValidatorVersion != domain.ValidatorVersion {
		t.Fatalf("应记录校验器版本: %s", outcome.Details.ValidatorVersion)
	}
	if outcome.Details.Generation != outcome.Host.ValidationGeneration {
		t.Fatalf("详情代次应与节点一致: %d", outcome.Details.Generation)
	}
}

// TestValidateCancellationConverges 验证取消时校验仍会收敛并释放连接。
func TestValidateCancellationConverges(t *testing.T) {
	t.Parallel()
	// 镜像查询被刻意放慢，使取消必然发生在检查过程中。
	engine := &engineBehaviour{status: 200, body: distributionBody(), delay: 300 * time.Millisecond}
	fixture := newFixture(t, serverOptions{engine: engine})
	// 校验已经开始后再取消：模拟 Server 关闭时中断在途校验。
	// 取消发生在检查过程中，因此结果必须收敛为 abnormal 而不是保持 checking。
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	defer cancel()
	outcome, err := fixture.validator.Validate(ctx, app.ValidationRequest{
		HostID: fixture.host.ID.String(),
	})
	if err != nil {
		t.Fatalf("校验本身不应返回错误: %v", err)
	}
	if outcome.Status == domain.ValidationStatusNormal {
		t.Fatal("取消的校验不得判为 normal")
	}
}

// TestValidateFailsWhenCLIVersionTooLow 验证 CLI 版本不足时不进入 normal。
func TestValidateFailsWhenCLIVersionTooLow(t *testing.T) {
	t.Parallel()
	engine := &engineBehaviour{status: 200, body: distributionBody()}
	fixture := newFixture(t, serverOptions{
		cliVersion: "28.5.2",
		engine:     engine,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	check := outcome.Details.Checks[1]
	if check.Name != domain.ValidationCheckDockerVersion {
		t.Fatalf("第二项应为 Docker 版本检查: %s", check.Name)
	}
	if check.Status.Passed() {
		t.Fatal("CLI 版本不足时版本检查不应通过")
	}
	if !strings.Contains(check.Message, docker.MinimumCLIVersion) {
		t.Fatalf("失败原因应给出最低版本要求: %s", check.Message)
	}
	if outcome.Details.Checks[2].Status != domain.CheckStatusNotRun {
		t.Fatalf("版本检查失败时帮助检查应为 not_run: %s", outcome.Details.Checks[2].Status)
	}
	if outcome.Details.Checks[3].Status != domain.CheckStatusNotRun {
		t.Fatalf("版本检查失败时镜像检查应为 not_run: %s", outcome.Details.Checks[3].Status)
	}
	// 版本检查失败时不得发起镜像元数据查询：CLI 版本不足时连 Engine 都不会访问。
	if len(engine.requests) != 0 {
		t.Fatalf("版本检查失败时不应查询 Engine: %+v", engine.requests)
	}
}

// TestValidateFailsWhenEngineVersionTooLow 验证 Engine 版本不足时不进入 normal。
func TestValidateFailsWhenEngineVersionTooLow(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, serverOptions{
		engineVersion: "28.5.2",
		engine:        &engineBehaviour{status: 200, body: distributionBody()},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	if outcome.Status != domain.ValidationStatusAbnormal {
		t.Fatalf("状态应为 abnormal: %s", outcome.Status)
	}
	check := outcome.Details.Checks[1]
	if check.Status.Passed() {
		t.Fatal("Engine 版本不足时版本检查不应通过")
	}
	if !strings.Contains(check.Message, docker.MinimumEngineVersion) {
		t.Fatalf("失败原因应给出最低版本要求: %s", check.Message)
	}
}

// TestValidateFailsWhenAPIIncompatible 验证 API 区间不含基准时不进入 normal。
func TestValidateFailsWhenAPIIncompatible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		apiRange docker.APIRange
	}{
		{name: "最高版本低于基准", apiRange: docker.APIRange{Maximum: "1.51", Minimum: "1.24"}},
		{name: "最低接受版本高于基准", apiRange: docker.APIRange{Maximum: "1.56", Minimum: "1.53"}},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newFixture(t, serverOptions{
				engine: &engineBehaviour{
					status:   200,
					body:     distributionBody(),
					apiRange: testCase.apiRange,
				},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			outcome := fixture.validate(t, ctx)
			if outcome.Status != domain.ValidationStatusAbnormal {
				t.Fatalf("状态应为 abnormal: %s", outcome.Status)
			}
			check := outcome.Details.Checks[1]
			if check.Status.Passed() {
				t.Fatal("API 基准不可用时版本检查不应通过")
			}
			if !strings.Contains(check.Message, docker.EngineAPIBaseVersion) {
				t.Fatalf("失败原因应给出 API 基准: %s", check.Message)
			}
		})
	}
}

// TestValidateVersionCheckRecordsActualVersions 验证版本检查记录实际版本与期望版本。
func TestValidateVersionCheckRecordsActualVersions(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, serverOptions{
		engine: &engineBehaviour{status: 200, body: distributionBody()},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome := fixture.validate(t, ctx)
	check := outcome.Details.Checks[1]
	if !check.Status.Passed() {
		t.Fatalf("版本检查应通过: %s", check.Message)
	}
	joined := strings.Join(check.Details, "\n")
	for _, expected := range []string{
		docker.MinimumCLIVersion,
		docker.MinimumEngineVersion,
		docker.EngineAPIBaseVersion,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("检查详情应包含 %s: %s", expected, joined)
		}
	}
}

// TestValidateRejectsTaskExecutionWhenAbnormal 验证未通过校验的节点被远程任务入口拒绝。
func TestValidateRejectsTaskExecutionWhenAbnormal(t *testing.T) {
	t.Parallel()
	host := domain.ExecutionHost{
		ID:               "0123456789abcdef0123456789abcdef",
		Name:             "版本不兼容节点",
		Enabled:          true,
		ValidationStatus: domain.ValidationStatusAbnormal,
		ValidationMessage: "Docker CLI 版本 28.5.2 低于要求的最低版本 " +
			docker.MinimumCLIVersion,
	}
	err := app.EnsureHostUsableForRemoteTask(host)
	if err == nil {
		t.Fatal("abnormal 节点必须被远程任务入口拒绝")
	}
	if !strings.Contains(err.Error(), docker.MinimumCLIVersion) {
		t.Fatalf("拒绝原因应包含版本信息: %v", err)
	}
}

// writePrivateKeyFile 生成一个测试用未加密私钥并返回其路径。
//
// 本版本只支持无口令私钥，因此测试也只生成未加密私钥。
func writePrivateKeyFile(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "agentflow-validation")
	if err != nil {
		t.Fatalf("编码测试私钥失败: %v", err)
	}
	path := filepath.Join(testkit.Dir(t), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	return path
}
