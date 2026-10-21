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

// closeGraceTimeout 是等待远程会话在关闭后结束的最长时间。
//
// 关闭会话通道后，服务端可能还需要一点时间才会发送退出状态；这里给一个很短的
// 宽限期，既能让本机资源及时释放，又不会因为服务端不响应而永久阻塞。
const closeGraceTimeout = 2 * time.Second

// client 是 Client 接口的实现，持有已认证的 SSH 连接。
type client struct {
	sshClient *ssh.Client
	endpoint  Endpoint
	keepAlive time.Duration

	// stopKeepAlive 关闭保活循环。
	stopKeepAlive chan struct{}

	mu     sync.Mutex
	closed bool
}

// newClient 包装已认证的连接，并按需启动保活探测。
func newClient(sshClient *ssh.Client, endpoint Endpoint, keepAlive time.Duration) *client {
	instance := &client{
		sshClient:     sshClient,
		endpoint:      endpoint,
		keepAlive:     keepAlive,
		stopKeepAlive: make(chan struct{}),
	}
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
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	stop := c.stopKeepAlive
	sshClient := c.sshClient
	c.mu.Unlock()
	// stopKeepAlive 只关闭一次；字段本身不再改写，避免保活协程读取时产生数据竞争。
	if stop != nil {
		close(stop)
	}
	if sshClient == nil {
		return nil
	}
	return sshClient.Close()
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
	for {
		select {
		case <-c.stopKeepAlive:
			return
		case <-ticker.C:
			if c.isClosed() {
				return
			}
			if _, _, err := c.sshClient.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				_ = c.Close()
				return
			}
		}
	}
}

// Run 执行远程命令并返回退出码与输出。
//
// 标准输出与标准错误分别收集，互不混淆；非零退出码通过 ExitCode 与结果状态表达。
// 输出超过上限时只保留前缀并标记截断，同时继续读取，避免远程进程因管道写满而阻塞。
//
// 取消或超时时的处理顺序是：关闭远程会话通道 → 在宽限期内等待会话结束 →
// 返回超时或取消错误。远程命令是否真的被终止无法由 SSH 协议保证，因此结果状态
// 只报告"本地已停止等待并关闭会话"，不声称远程进程一定已经退出。
func (c *client) Run(
	ctx context.Context,
	command string,
	options RunOptions,
) (CommandResult, error) {
	return c.runWithInput(ctx, command, nil, options)
}

// RunWithInput 执行远程命令并把 input 写入其标准输入。
//
// input 为 nil 时不提供标准输入；非 nil 时在命令启动后写入内容并立即关闭写入端，
// 使需要 EOF 才能继续的程序能够正常结束。
func (c *client) RunWithInput(
	ctx context.Context,
	command string,
	input io.Reader,
	options RunOptions,
) (CommandResult, error) {
	if input == nil {
		return c.Run(ctx, command, options)
	}
	return c.runWithInput(ctx, command, input, options)
}

// runWithInput 是 Run 与 RunWithInput 的共同实现。
//
// input 非 nil 时在会话启动后把内容写入标准输入并关闭写入端。标准输入写入与
// 输出读取并行进行，避免大输入与大输出互相阻塞。
func (c *client) runWithInput(
	ctx context.Context,
	command string,
	input io.Reader,
	options RunOptions,
) (CommandResult, error) {
	started := time.Now()
	result := CommandResult{Command: command, ExitCode: -1, Stdin: StdinResult{Status: StdinStatusNone}}
	if err := c.ensureOpen(); err != nil {
		result.Status = CommandStatusConnectionError
		result.Duration = time.Since(started)
		return result, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		result.Status = contextStatus(err)
		result.Duration = time.Since(started)
		return result, runContextError(ctx, command)
	}
	runCtx, cancel := contextWithRunTimeout(ctx, options.Timeout)
	defer cancel()
	session, err := c.sshClient.NewSession()
	if err != nil {
		result.Status = CommandStatusConnectionError
		result.Duration = time.Since(started)
		return result, fmt.Errorf("sshclient: 创建远程会话失败: %w", err)
	}
	stdout := newBoundedWriter(outputLimit(options.MaxOutputBytes))
	stderr := newBoundedWriter(outputLimit(options.MaxOutputBytes))
	session.Stdout = stdout
	session.Stderr = stderr
	stdin, err := attachStdin(session, input)
	if err != nil {
		_ = session.Close()
		result.Status = CommandStatusConnectionError
		result.Duration = time.Since(started)
		return result, err
	}
	// 会话已建立且命令已提交：从这一刻起退出码与输出才有意义。
	result.Started = true
	finished := make(chan error, 1)
	go func() {
		finished <- session.Run(command)
	}()
	// 写入与读取并行：输入较大时不会因为远程进程尚未开始读取而阻塞输出采集。
	writeDone := writeStdin(stdin, input)
	collect := func() StdinResult {
		if stdin == nil {
			return StdinResult{Status: StdinStatusNone}
		}
		return <-writeDone
	}
	select {
	case runErr := <-finished:
		// 命令正常结束：会话已由 ssh 库自行关闭，这里只补一次兜底关闭。
		_ = session.Close()
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.StdoutTruncated = stdout.Truncated()
		result.StderrTruncated = stderr.Truncated()
		result.Stdin = collect()
		result.Duration = time.Since(started)
		return completeRun(result, runErr)
	case <-runCtx.Done():
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.StdoutTruncated = stdout.Truncated()
		result.StderrTruncated = stderr.Truncated()
		result.Duration = time.Since(started)
		result.Status = contextStatus(runCtx.Err())
		// 本端已关闭会话通道，但无法确认远程进程是否被终止；
		// TerminationConfirmed 保持 false，如实记录这一限制。
		terminateSession(session, finished)
		result.Stdin = collectWithLimit(writeDone)
		return result, runContextError(ctx, command)
	}
}

// attachStdin 在需要时把标准输入接入会话。
//
// 只有在显式提供输入时才申请 stdin 管道：不需要输入的调用不应改变远程命令的
// 标准输入行为。
func attachStdin(session *ssh.Session, input io.Reader) (io.WriteCloser, error) {
	if input == nil {
		return nil, nil
	}
	writer, err := session.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sshclient: 创建标准输入管道失败: %w", err)
	}
	return writer, nil
}

// writeStdin 在后台写入标准输入并关闭写入端，结果通过返回的通道取得。
func writeStdin(writer io.WriteCloser, input io.Reader) <-chan StdinResult {
	if writer == nil {
		return nil
	}
	done := make(chan StdinResult, 1)
	go func() {
		defer close(done)
		written, writeErr := io.Copy(writer, input)
		closeErr := writer.Close()
		status := StdinResult{Status: StdinStatusWritten, Bytes: written}
		if writeErr != nil {
			status.Status = StdinStatusFailed
			status.Error = writeErr.Error()
			done <- status
			return
		}
		if closeErr != nil {
			status.Status = StdinStatusFailed
			status.Error = closeErr.Error()
		}
		done <- status
	}()
	return done
}

// collectWithLimit 在有限时间内等待标准输入写入结果。
//
// 取消或超时后远程会话可能已经关闭，写入协程仍可能阻塞在管道上；
// 此时不等待最终结果，只如实标记写入状态未知。
func collectWithLimit(done <-chan StdinResult) StdinResult {
	if done == nil {
		return StdinResult{Status: StdinStatusNone}
	}
	select {
	case result := <-done:
		return result
	case <-time.After(closeGraceTimeout):
		return StdinResult{Status: StdinStatusFailed, Error: "标准输入写入状态未能在宽限期内确认"}
	}
}

// terminateSession 尽力停止远程命令并释放本机资源。
//
// SSH 会话不是本机子进程：关闭会话通道会让服务端结束对应进程，但协议本身
// 不保证远程进程一定被终止。这里先尝试发送 SIGKILL，再关闭通道，并在宽限期内
// 等待会话结束，避免本机 goroutine 与文件描述符长期滞留。
func terminateSession(session *ssh.Session, finished <-chan error) {
	_ = session.Signal(ssh.SIGKILL)
	_ = session.Close()
	select {
	case <-finished:
	case <-time.After(closeGraceTimeout):
	}
}

// contextWithRunTimeout 把执行超时合并到上下文。
func contextWithRunTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// completeRun 把 ssh.Session.Run 的返回值转换为执行结果。
func completeRun(result CommandResult, runErr error) (CommandResult, error) {
	if runErr == nil {
		result.ExitCode = 0
		result.Status = CommandStatusSucceeded
		// 会话正常结束，可以确认远程命令已经终止。
		result.TerminationConfirmed = true
		return result, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(runErr, &exitErr) {
		// 非零退出码是远程命令的正常结果，不作为传输层错误。
		result.ExitCode = exitErr.ExitStatus()
		result.Status = CommandStatusExitError
		result.TerminationConfirmed = true
		return result, nil
	}
	var missingErr *ssh.ExitMissingError
	if errors.As(runErr, &missingErr) {
		result.ExitCode = -1
		result.Status = CommandStatusConnectionError
		return result, fmt.Errorf(
			"sshclient: 远程命令未返回退出码（连接可能已断开）: %w",
			runErr,
		)
	}
	result.ExitCode = -1
	result.Status = CommandStatusConnectionError
	return result, fmt.Errorf("sshclient: 执行远程命令失败: %w", runErr)
}

// runContextError 构造取消或超时的执行错误。
//
// 错误包装上下文错误，使调用方可以用 errors.Is 判断超时或取消。
func runContextError(ctx context.Context, command string) error {
	cause := context.Canceled
	if ctx != nil && ctx.Err() != nil {
		cause = ctx.Err()
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("sshclient: 远程命令执行超时: %s: %w", summarizeCommand(command), cause)
	}
	return fmt.Errorf("sshclient: 远程命令执行被取消: %s: %w", summarizeCommand(command), cause)
}

// contextStatus 把上下文错误映射为命令结果状态。
func contextStatus(err error) CommandStatus {
	if errors.Is(err, context.DeadlineExceeded) {
		return CommandStatusTimeout
	}
	return CommandStatusCancelled
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
