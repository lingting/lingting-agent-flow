package sshclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// 连接默认参数。
const (
	// DefaultConnectTimeout 是建立 TCP 连接与完成认证的默认超时时间。
	DefaultConnectTimeout = 15 * time.Second
	// DefaultKeepAliveInterval 是连接保活探测的默认间隔。
	DefaultKeepAliveInterval = 30 * time.Second
)

// DialOptions 是构造 Dialer 的参数。
type DialOptions struct {
	// HostKeys 是主机密钥校验与信任记录，必填。
	HostKeys HostKeyStore
	// ConnectTimeout 是 TCP 连接与认证的超时时间，零值使用 DefaultConnectTimeout。
	ConnectTimeout time.Duration
	// KeepAliveInterval 是连接保活间隔，零值使用 DefaultKeepAliveInterval；
	// 负值表示关闭保活。
	KeepAliveInterval time.Duration
	// ClientVersion 是客户端 SSH 标识，为空时使用库默认值。
	ClientVersion string
}

// Dialer 建立经过主机密钥验证与身份认证的 SSH 连接。
//
// Dialer 不持有连接状态，可以并发使用；每次 Dial 都会重新校验主机密钥。
type Dialer struct {
	hostKeys          HostKeyStore
	connectTimeout    time.Duration
	keepAliveInterval time.Duration
	clientVersion     string
}

// NewDialer 构造连接器。
//
// 未提供主机密钥校验实现时直接失败：本包不允许以跳过主机校验的方式建立连接。
func NewDialer(options DialOptions) (*Dialer, error) {
	if options.HostKeys == nil {
		return nil, fmt.Errorf("sshclient: 必须提供主机密钥校验实现，禁止跳过主机密钥验证")
	}
	connectTimeout := options.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = DefaultConnectTimeout
	}
	keepAlive := options.KeepAliveInterval
	if keepAlive == 0 {
		keepAlive = DefaultKeepAliveInterval
	}
	return &Dialer{
		hostKeys:          options.HostKeys,
		connectTimeout:    connectTimeout,
		keepAliveInterval: keepAlive,
		clientVersion:     strings.TrimSpace(options.ClientVersion),
	}, nil
}

// DialResult 是一次连接尝试的完整结果。
//
// 即使返回 error，HostKey 也会在握手阶段取得主机密钥后填入，
// 便于调用方在未知主机场景向用户展示指纹并请求确认。
type DialResult struct {
	// Client 是已认证的连接，失败时为 nil。
	Client Client
	// HostKey 是服务端提供的主机密钥信息，未取得时为空。
	HostKey HostKeyInfo
	// HostKeyVerdict 是主机密钥校验结论，未执行校验时为空。
	HostKeyVerdict HostKeyVerdict
	// Duration 是连接耗时。
	Duration time.Duration
}

// Dial 建立并认证一个 SSH 连接。
//
// 失败时返回的错误是 *ConnectError，其 Code 明确区分 TCP 不可达、SSH 握手失败、
// 主机密钥不可信、认证失败与凭据缺失。
func (d *Dialer) Dial(
	ctx context.Context,
	endpoint Endpoint,
	config AuthConfig,
	provider CredentialProvider,
) (DialResult, error) {
	started := time.Now()
	if err := validateEndpoint(endpoint); err != nil {
		return DialResult{Duration: time.Since(started)}, newConnectError(
			CodeInvalidConfig,
			endpoint.Address(),
			err.Error(),
			err,
		)
	}
	if err := config.Validate(); err != nil {
		return DialResult{Duration: time.Since(started)}, newConnectError(
			CodeInvalidConfig,
			endpoint.Address(),
			err.Error(),
			err,
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DialResult{Duration: time.Since(started)}, newConnectError(
			contextCode(err),
			endpoint.Address(),
			"连接前已被取消或超时",
			err,
		)
	}
	auth, err := buildAuthMethods(ctx, config, provider)
	if err != nil {
		// 认证方式准备阶段的错误还没有连接地址，这里补上目标地址，
		// 使错误信息始终能定位到具体节点。
		return DialResult{Duration: time.Since(started)}, withAddress(err, endpoint.Address())
	}
	defer auth.cleanup()

	dialCtx, cancel := context.WithTimeout(ctx, d.connectTimeout)
	defer cancel()
	conn, err := d.dialTCP(dialCtx, endpoint)
	if err != nil {
		return DialResult{Duration: time.Since(started)}, d.tcpError(dialCtx, endpoint, err)
	}
	handshake := &hostKeyHandshake{store: d.hostKeys, host: endpoint.Host, port: endpoint.Port}
	clientConfig := &ssh.ClientConfig{
		User:            endpoint.Username,
		Auth:            auth.methods,
		HostKeyCallback: handshake.callback,
		Timeout:         d.connectTimeout,
		ClientVersion:   d.clientVersion,
	}
	client, err := d.handshake(dialCtx, conn, endpoint, clientConfig)
	result := DialResult{
		HostKey:        handshake.presented,
		HostKeyVerdict: handshake.verdict,
		Duration:       time.Since(started),
	}
	if err != nil {
		return result, d.handshakeError(dialCtx, endpoint, handshake, err)
	}
	result.Client = newClient(client, endpoint, d.keepAliveInterval)
	return result, nil
}

// dialTCP 建立到目标地址的 TCP 连接。
func (d *Dialer) dialTCP(ctx context.Context, endpoint Endpoint) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: d.connectTimeout}
	return dialer.DialContext(ctx, "tcp", endpoint.Address())
}

// ProbeHostKey 只完成 SSH 握手并返回服务端主机密钥，不执行身份认证。
//
// 该方法用于在用户确认信任之前展示指纹；它同样不允许跳过主机密钥校验，
// 校验结论通过返回的 *ConnectError 表达，调用方据此决定是否请求用户确认。
func (d *Dialer) ProbeHostKey(ctx context.Context, endpoint Endpoint) (HostKeyInfo, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return HostKeyInfo{}, newConnectError(CodeInvalidConfig, endpoint.Address(), err.Error(), err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, d.connectTimeout)
	defer cancel()
	conn, err := d.dialTCP(probeCtx, endpoint)
	if err != nil {
		return HostKeyInfo{}, d.tcpError(probeCtx, endpoint, err)
	}
	handshake := &hostKeyHandshake{store: d.hostKeys, host: endpoint.Host, port: endpoint.Port}
	// 只要求握手成功：认证阶段使用空的认证方式列表，握手完成后立即关闭连接。
	clientConfig := &ssh.ClientConfig{
		User:            endpoint.Username,
		Auth:            nil,
		HostKeyCallback: handshake.callback,
		Timeout:         d.connectTimeout,
		ClientVersion:   d.clientVersion,
	}
	client, handshakeErr := d.handshake(probeCtx, conn, endpoint, clientConfig)
	if client != nil {
		_ = client.Close()
	}
	if handshakeErr != nil {
		// 认证失败不影响主机密钥结论：此时已经取得并校验了主机密钥。
		if handshake.failure != nil {
			return handshake.presented, handshake.failure
		}
		if handshake.presented.Fingerprint != "" {
			return handshake.presented, nil
		}
		return HostKeyInfo{}, d.handshakeError(probeCtx, endpoint, handshake, handshakeErr)
	}
	return handshake.presented, nil
}

// handshake 在已建立的 TCP 连接上完成 SSH 握手与认证。
func (d *Dialer) handshake(
	ctx context.Context,
	conn net.Conn,
	endpoint Endpoint,
	config *ssh.ClientConfig,
) (*ssh.Client, error) {
	type outcome struct {
		client *ssh.Client
		err    error
	}
	// ssh.NewClientConn 不接受上下文，通过关闭连接响应取消与超时。
	done := make(chan outcome, 1)
	watcher := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-watcher:
		}
	}()
	go func() {
		clientConn, channels, requests, err := ssh.NewClientConn(conn, endpoint.Address(), config)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		done <- outcome{client: ssh.NewClient(clientConn, channels, requests)}
	}()
	result := <-done
	close(watcher)
	if result.err != nil {
		_ = conn.Close()
		return nil, result.err
	}
	return result.client, nil
}

// tcpError 把 TCP 连接阶段的错误转换为连接错误。
func (d *Dialer) tcpError(ctx context.Context, endpoint Endpoint, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return newConnectError(
			contextCode(ctxErr),
			endpoint.Address(),
			"连接目标地址超时或已取消",
			err,
		)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return newConnectError(CodeTimeout, endpoint.Address(), "连接目标地址超时", err)
	}
	return newConnectError(
		CodeUnreachable,
		endpoint.Address(),
		"无法连接目标地址（TCP 层失败，未进入 SSH 握手）: "+netErrorDetail(err),
		err,
	)
}

// handshakeError 把握手与认证阶段的错误转换为连接错误。
func (d *Dialer) handshakeError(
	ctx context.Context,
	endpoint Endpoint,
	handshake *hostKeyHandshake,
	err error,
) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return newConnectError(
			contextCode(ctxErr),
			endpoint.Address(),
			"SSH 握手或认证超时",
			err,
		)
	}
	if handshake.failure != nil {
		return handshake.failure
	}
	if isAuthFailure(err) {
		return newConnectError(
			CodeAuthFailed,
			endpoint.Address(),
			"SSH 身份认证被拒绝（用户名、密码、私钥或 Agent 身份不被接受）",
			err,
		)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return newConnectError(CodeTimeout, endpoint.Address(), "SSH 握手超时", err)
	}
	return newConnectError(
		CodeHandshakeFailed,
		endpoint.Address(),
		"SSH 协议握手失败: "+netErrorDetail(err),
		err,
	)
}

// isAuthFailure 判断握手错误是否由认证失败导致。
//
// x/crypto/ssh 没有导出认证失败的哨兵错误，只能依据其固定文案判断；
// 这里同时检查底层错误，避免包装后遗漏。
func isAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	const marker = "unable to authenticate"
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), marker) {
			return true
		}
	}
	return false
}

// contextCode 把上下文错误映射为连接错误类别。
func contextCode(err error) ErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeTimeout
	}
	return CodeCancelled
}

// netErrorDetail 返回不含敏感信息的网络错误说明。
func netErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err.Error()
	}
	message := err.Error()
	if len(message) > 200 {
		message = message[:200]
	}
	return message
}

// validateEndpoint 校验连接目标。
func validateEndpoint(endpoint Endpoint) error {
	if strings.TrimSpace(endpoint.Host) == "" {
		return fmt.Errorf("sshclient: 主机地址不能为空")
	}
	if endpoint.Port < 1 || endpoint.Port > 65535 {
		return fmt.Errorf("sshclient: 端口必须在 1-65535 之间，收到 %d", endpoint.Port)
	}
	if strings.TrimSpace(endpoint.Username) == "" {
		return fmt.Errorf("sshclient: SSH 登录用户名不能为空")
	}
	return nil
}

// Address 返回 host:port 形式的地址，IPv6 会自动加方括号。
func (e Endpoint) Address() string {
	return net.JoinHostPort(strings.TrimSpace(e.Host), strconv.Itoa(e.Port))
}

// hostKeyHandshake 是主机密钥校验回调的持有者。
//
// 它记录服务端提供的主机密钥与校验结论，使 Dial 在握手失败时仍然可以
// 向调用方报告指纹与拒绝原因。
type hostKeyHandshake struct {
	store     HostKeyStore
	host      string
	port      int
	presented HostKeyInfo
	verdict   HostKeyVerdict
	failure   *ConnectError
}

// callback 实现 ssh.HostKeyCallback。
func (h *hostKeyHandshake) callback(_ string, _ net.Addr, key ssh.PublicKey) error {
	check, err := h.store.Check(h.host, h.port, key)
	h.presented = check.Presented
	h.verdict = check.Verdict
	if err != nil {
		h.failure = h.buildError(check, err)
		return h.failure
	}
	switch check.Verdict {
	case VerdictTrusted:
		return nil
	case VerdictUnknown:
		h.failure = h.buildError(check, nil)
		return h.failure
	case VerdictMismatch:
		h.failure = h.buildError(check, nil)
		return h.failure
	case VerdictRevoked:
		h.failure = h.buildError(check, nil)
		return h.failure
	default:
		h.failure = h.buildError(check, nil)
		return h.failure
	}
}

// buildError 依据校验结论构造连接错误。
func (h *hostKeyHandshake) buildError(check HostKeyCheck, cause error) *ConnectError {
	address := h.presented.Address()
	var code ErrorCode
	var detail string
	switch check.Verdict {
	case VerdictUnknown:
		code = CodeHostKeyUnknown
		detail = fmt.Sprintf(
			"未知主机密钥，需要确认后信任: %s %s",
			h.presented.Algorithm,
			h.presented.Fingerprint,
		)
	case VerdictMismatch:
		code = CodeHostKeyMismatch
		detail = fmt.Sprintf(
			"主机密钥与已信任记录不一致，已拒绝连接。实际: %s %s；已信任: %s",
			h.presented.Algorithm,
			h.presented.Fingerprint,
			formatTrustedKeys(check.Trusted),
		)
	case VerdictRevoked:
		code = CodeHostKeyRevoked
		detail = fmt.Sprintf(
			"主机密钥已被标记为吊销，已拒绝连接: %s %s",
			h.presented.Algorithm,
			h.presented.Fingerprint,
		)
	default:
		code = CodeHostKeyUnknown
		detail = "无法确认主机密钥是否可信"
	}
	failure := newConnectError(code, address, detail, cause)
	failure.HostKey = &h.presented
	return failure
}

// formatTrustedKeys 把已信任的主机密钥格式化为可核验文本。
func formatTrustedKeys(trusted []HostKeyInfo) string {
	if len(trusted) == 0 {
		return "（无记录）"
	}
	parts := make([]string, 0, len(trusted))
	for _, info := range trusted {
		parts = append(parts, info.Algorithm+" "+info.Fingerprint)
	}
	return strings.Join(parts, "，")
}
