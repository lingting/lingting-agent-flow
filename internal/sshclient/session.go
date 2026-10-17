package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// client 是 Client 接口的实现，持有已认证的 SSH 连接。
type client struct {
	sshClient *ssh.Client
	endpoint  Endpoint
	keepAlive time.Duration

	mu     sync.Mutex
	closed bool
}

// newClient 包装已认证的连接，并按需启动保活探测。
func newClient(sshClient *ssh.Client, endpoint Endpoint, keepAlive time.Duration) *client {
	instance := &client{sshClient: sshClient, endpoint: endpoint, keepAlive: keepAlive}
	if keepAlive > 0 {
		go instance.keepAliveLoop()
	}
	return instance
}

// Endpoint 返回本次连接的目标。
func (c *client) Endpoint() Endpoint {
	return c.endpoint
}

// Close 关闭连接，重复调用返回 nil。
func (c *client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.sshClient == nil {
		return nil
	}
	return c.sshClient.Close()
}

// isClosed 判断连接是否已关闭。
func (c *client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// keepAliveLoop 周期性发送保活请求，及时发现已断开的连接。
//
// 保活失败即认为连接不可用，此时关闭连接，使后续操作立即返回错误，
// 而不是在长时间静默后才发现连接已断开。
func (c *client) keepAliveLoop() {
	ticker := time.NewTicker(c.keepAlive)
	defer ticker.Stop()
	for range ticker.C {
		if c.isClosed() {
			return
		}
		if _, _, err := c.sshClient.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			_ = c.Close()
			return
		}
	}
}

// Run 执行远程命令并返回退出码与输出。
//
// 标准输出与标准错误分别收集，互不混淆；非零退出码通过 ExitCode 表达。
// 输出超过上限时只保留前缀并标记截断，同时继续读取，避免远程进程因管道写满而阻塞。
func (c *client) Run(
	ctx context.Context,
	command string,
	options RunOptions,
) (CommandResult, error) {
	started := time.Now()
	result := CommandResult{Command: command, ExitCode: -1}
	if err := c.ensureOpen(); err != nil {
		result.Duration = time.Since(started)
		return result, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		result.Duration = time.Since(started)
		return result, err
	}
	session, err := c.sshClient.NewSession()
	if err != nil {
		result.Duration = time.Since(started)
		return result, fmt.Errorf("sshclient: 创建远程会话失败: %w", err)
	}
	defer func() {
		_ = session.Close()
	}()
	stdout := newBoundedWriter(outputLimit(options.MaxOutputBytes))
	stderr := newBoundedWriter(outputLimit(options.MaxOutputBytes))
	session.Stdout = stdout
	session.Stderr = stderr
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- session.Run(command)
	}()
	select {
	case runErr := <-finished:
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.StdoutTruncated = stdout.Truncated()
		result.StderrTruncated = stderr.Truncated()
		result.Duration = time.Since(started)
		return completeRun(result, runErr)
	case <-runCtx.Done():
		// 取消或超时时关闭会话，使远程命令尽快结束，避免资源长期占用。
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.StdoutTruncated = stdout.Truncated()
		result.StderrTruncated = stderr.Truncated()
		result.Duration = time.Since(started)
		return result, runContextError(ctx, command)
	}
}

// completeRun 把 ssh.Session.Run 的返回值转换为执行结果。
func completeRun(result CommandResult, runErr error) (CommandResult, error) {
	if runErr == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(runErr, &exitErr) {
		// 非零退出码是远程命令的正常结果，不作为传输层错误。
		result.ExitCode = exitErr.ExitStatus()
		return result, nil
	}
	var missingErr *ssh.ExitMissingError
	if errors.As(runErr, &missingErr) {
		result.ExitCode = -1
		return result, fmt.Errorf(
			"sshclient: 远程命令未返回退出码（连接可能已断开）: %w",
			runErr,
		)
	}
	result.ExitCode = -1
	return result, fmt.Errorf("sshclient: 执行远程命令失败: %w", runErr)
}

// runContextError 构造取消或超时的执行错误。
func runContextError(ctx context.Context, command string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("sshclient: 远程命令执行超时: %s: %w", summarizeCommand(command), ctx.Err())
	}
	return fmt.Errorf("sshclient: 远程命令执行被取消: %s: %w", summarizeCommand(command), ctx.Err())
}

// ensureOpen 确认连接仍然可用。
func (c *client) ensureOpen() error {
	if c.isClosed() {
		return fmt.Errorf("sshclient: 连接已关闭")
	}
	if c.sshClient == nil {
		return fmt.Errorf("sshclient: 连接未初始化")
	}
	return nil
}

// outputLimit 返回实际使用的输出上限。
func outputLimit(requested int) int {
	if requested > 0 {
		return requested
	}
	return DefaultMaxOutputBytes
}

// summarizeCommand 截断命令用于错误信息，避免把完整命令写入日志。
func summarizeCommand(command string) string {
	const limit = 80
	if len(command) <= limit {
		return command
	}
	return command[:limit] + "..."
}

// boundedWriter 是只保留前 limit 字节的并发安全写入器。
//
// 超出上限的内容被丢弃并记录截断标记，但始终报告全部字节已写入，
// 使远程命令不会因为管道写满而阻塞。
type boundedWriter struct {
	mu        sync.Mutex
	limit     int
	written   int
	truncated bool
	buffer    bytes.Buffer
}

// newBoundedWriter 创建带长度上限的写入器。
func newBoundedWriter(limit int) *boundedWriter {
	return &boundedWriter{limit: limit}
}

// Write 追加数据并裁剪到上限之内。
func (w *boundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - w.written
	if remaining <= 0 {
		if len(p) > 0 {
			w.truncated = true
		}
		return len(p), nil
	}
	if len(p) > remaining {
		w.truncated = true
		w.buffer.Write(p[:remaining])
		w.written = w.limit
		return len(p), nil
	}
	w.buffer.Write(p)
	w.written += len(p)
	return len(p), nil
}

// String 返回当前保留的内容。
func (w *boundedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

// Truncated 判断内容是否被截断。
func (w *boundedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

// 编译期确认实现满足接口约束。
var _ Client = (*client)(nil)
var _ io.Writer = (*boundedWriter)(nil)
