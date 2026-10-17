package sshprobe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Target 是 SSH 探测的目标地址。
type Target struct {
	// Host 是 IP 地址或主机名，不含端口。
	Host string
	// Port 是 TCP 端口。
	Port int
}

// Validate 校验目标地址。
func (t Target) Validate() error {
	host := strings.TrimSpace(t.Host)
	if host == "" {
		return fmt.Errorf("sshprobe: 主机地址不能为空")
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("sshprobe: 主机地址不能包含空白字符")
	}
	if t.Port < 1 || t.Port > 65535 {
		return fmt.Errorf("sshprobe: 端口必须在 1-65535 之间，收到 %d", t.Port)
	}
	return nil
}

// Address 返回 host:port 形式的地址，IPv6 会自动加方括号。
func (t Target) Address() string {
	return net.JoinHostPort(strings.TrimSpace(t.Host), strconv.Itoa(t.Port))
}

// Dialer 是建立 TCP 连接的最小抽象，便于测试替换为受控实现。
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Options 是探测器的构造参数。
type Options struct {
	// Timeout 是单次探测的整体超时，零值使用 DefaultTimeout。
	Timeout time.Duration
	// MaxBannerBytes 是协议标识的最大读取长度，零值使用 DefaultMaxBannerBytes。
	MaxBannerBytes int
	// Dialer 用于建立 TCP 连接，为空时使用 net.Dialer。
	Dialer Dialer
}

// Prober 执行 SSH 端点探测。
//
// Prober 不持有可变状态，可以并发使用。
type Prober struct {
	timeout        time.Duration
	maxBannerBytes int
	dialer         Dialer
}

// NewProber 构造探测器并校验参数。
func NewProber(options Options) (*Prober, error) {
	timeout := options.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("sshprobe: 探测超时必须大于 0")
	}
	maxBannerBytes := options.MaxBannerBytes
	if maxBannerBytes == 0 {
		maxBannerBytes = DefaultMaxBannerBytes
	}
	if maxBannerBytes < 0 {
		return nil, fmt.Errorf("sshprobe: 协议标识读取上限必须大于 0")
	}
	dialer := options.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	return &Prober{timeout: timeout, maxBannerBytes: maxBannerBytes, dialer: dialer}, nil
}

// Probe 探测目标地址是否提供 SSH 服务。
//
// 返回的 Result 始终反映真实探测结果；同时返回 error 表示探测失败，
// 调用方可用 errors.As 取得 *ProbeError 判断错误类别。
// 成功时 Result.Authenticated 恒为 false：本阶段不执行任何身份认证。
func (p *Prober) Probe(ctx context.Context, target Target) (Result, error) {
	started := time.Now()
	if err := target.Validate(); err != nil {
		result := Result{Target: target.Address(), ErrorCode: CodeInvalidTarget}
		result.Duration = time.Since(started)
		result.Message = err.Error()
		return result, newError(CodeInvalidTarget, result.Target, err.Error(), err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	if err := probeCtx.Err(); err != nil {
		return p.fail(started, target, CodeCancelled, "探测被取消", err)
	}

	conn, err := p.dialer.DialContext(probeCtx, "tcp", target.Address())
	if err != nil {
		return p.dialFailure(started, target, probeCtx, err)
	}
	return p.readIdentifier(started, target, probeCtx, conn)
}

// dialFailure 把建立连接阶段的错误转换为探测结果。
func (p *Prober) dialFailure(
	started time.Time,
	target Target,
	ctx context.Context,
	err error,
) (Result, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		code, message := contextFailure(ctxErr)
		result := p.buildResult(started, target)
		result.ErrorCode = code
		result.Message = message
		return result, newError(code, result.Target, message, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		message := "连接目标地址超时"
		result := p.buildResult(started, target)
		result.ErrorCode = CodeTimeout
		result.Message = message
		return result, newError(CodeTimeout, result.Target, message, err)
	}
	message := "无法连接目标地址"
	result := p.buildResult(started, target)
	result.ErrorCode = CodeUnreachable
	result.Message = message
	return result, newError(CodeUnreachable, result.Target, message, err)
}

// readIdentifier 读取并校验服务端协议标识，结束后关闭连接。
func (p *Prober) readIdentifier(
	started time.Time,
	target Target,
	ctx context.Context,
	conn net.Conn,
) (Result, error) {
	// 取消时主动关闭连接，使阻塞中的读取立即返回，避免等待整个超时时间。
	closed := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()
	defer func() {
		close(closed)
		<-watchDone
		_ = conn.Close()
	}()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(deadline); err != nil {
			message := "设置读取超时失败"
			result := p.buildResult(started, target)
			result.Reachable = true
			result.ErrorCode = CodeNotSSH
			result.Message = message
			return result, newError(CodeNotSSH, result.Target, message, err)
		}
	}

	line, readErr := readBannerLine(conn, p.maxBannerBytes)
	if readErr != nil {
		return p.readFailure(started, target, ctx, readErr)
	}
	return p.evaluate(started, target, line)
}

// readFailure 把读取协议标识阶段的错误转换为探测结果。
func (p *Prober) readFailure(
	started time.Time,
	target Target,
	ctx context.Context,
	err error,
) (Result, error) {
	result := p.buildResult(started, target)
	result.Reachable = true
	if ctxErr := ctx.Err(); ctxErr != nil {
		code, message := contextFailure(ctxErr)
		result.ErrorCode = code
		result.Message = message
		return result, newError(code, result.Target, message, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		message := "等待 SSH 协议标识超时"
		result.ErrorCode = CodeTimeout
		result.Message = message
		return result, newError(CodeTimeout, result.Target, message, err)
	}
	message := "连接已建立，但未返回 SSH 协议标识"
	if errors.Is(err, errBannerTooLong) {
		message = "连接已建立，但返回内容超过协议标识长度上限"
	}
	result.ErrorCode = CodeNotSSH
	result.Message = message
	return result, newError(CodeNotSSH, result.Target, message, err)
}

// evaluate 判断读到的内容是否为有效的 SSH 协议标识。
func (p *Prober) evaluate(started time.Time, target Target, line string) (Result, error) {
	result := p.buildResult(started, target)
	result.Reachable = true
	if line == "" {
		message := "连接已建立，但未返回 SSH 协议标识"
		result.ErrorCode = CodeNotSSH
		result.Message = message
		return result, newError(CodeNotSSH, result.Target, message, nil)
	}
	if !isSSHIdentifier(line) {
		message := fmt.Sprintf(
			"连接已建立，但返回内容不是有效的 SSH 协议标识: %q",
			truncateForMessage(line, 64),
		)
		result.ErrorCode = CodeNotSSH
		result.Message = message
		return result, newError(CodeNotSSH, result.Target, message, nil)
	}
	result.SSHDetected = true
	result.ErrorCode = CodeOK
	result.Message = successMessage
	return result, nil
}

// buildResult 构造带目标地址与耗时的结果骨架。
func (p *Prober) buildResult(started time.Time, target Target) Result {
	return Result{Target: target.Address(), Duration: time.Since(started)}
}

// fail 构造不涉及连接失败的探测结果。
func (p *Prober) fail(
	started time.Time,
	target Target,
	code ErrorCode,
	message string,
	cause error,
) (Result, error) {
	result := p.buildResult(started, target)
	result.ErrorCode = code
	result.Message = message
	return result, newError(code, result.Target, message, cause)
}

// contextFailure 把上下文错误映射为探测错误类别与说明。
func contextFailure(err error) (ErrorCode, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeTimeout, "探测超时"
	}
	return CodeCancelled, "探测被取消"
}

// errBannerTooLong 表示服务端返回内容超过协议标识长度上限。
var errBannerTooLong = errors.New("sshprobe: 协议标识超过长度上限")

// readBannerLine 从连接读取首个 SSH 协议标识行。
//
// 按 RFC 4253，服务端在标识行之前可以发送若干额外行；这里跳过其他内容，
// 直到读到标识行、达到长度上限或连接结束。返回值不含行尾。
func readBannerLine(conn net.Conn, maxBytes int) (string, error) {
	reader := bufio.NewReaderSize(conn, maxBytes)
	total := 0
	for {
		line, err := reader.ReadString('\n')
		total += len(line)
		if total > maxBytes {
			return "", errBannerTooLong
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if len(trimmed) > maxBannerLineLength {
			return "", errBannerTooLong
		}
		if isSSHIdentifier(trimmed) {
			return trimmed, nil
		}
		if err != nil {
			return "", err
		}
	}
}
