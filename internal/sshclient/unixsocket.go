package sshclient

import (
	"context"
	"fmt"
	"net"
	"time"
)

// 本地 Unix 套接字通道的默认参数。
const (
	// DefaultUnixSocketDialTimeout 是打开 Unix 套接字通道的超时时间。
	DefaultUnixSocketDialTimeout = 10 * time.Second
)

// UnixSocketDialer 通过已认证连接打开远端 Unix 域套接字。
//
// 该能力用于在不开放 Docker TCP 端口、不要求远端安装 curl 的前提下访问远端
// Docker Engine 的 Unix 套接字。实现依赖 OpenSSH 的
// direct-streamlocal@openssh.com 通道；服务端不支持时返回明确的错误，不会静默
// 回退到其他传输方式。
//
// 返回值是一个 net.Conn，其生命周期独立于连接上的其他操作：关闭它只释放该通道。
type UnixSocketDialer interface {
	DialUnixSocket(ctx context.Context, socketPath string) (net.Conn, error)
}

// DialUnixSocket 在已认证连接上打开远端 Unix 域套接字通道。
//
// 取消或超时时主动关闭底层通道，避免残留半开连接。远端套接字不存在、权限不足
// 或服务端不支持该通道类型时返回可诊断的错误。
func (c *client) DialUnixSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	if err := c.ensureOpen(); err != nil {
		return nil, err
	}
	if socketPath == "" {
		return nil, fmt.Errorf("sshclient: 远端 Unix 套接字路径不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sshclient: 打开远端 Unix 套接字前已取消: %w", err)
	}
	dialCtx, cancel := context.WithTimeout(ctx, DefaultUnixSocketDialTimeout)
	defer cancel()

	type dialResult struct {
		conn net.Conn
		err  error
	}
	done := make(chan dialResult, 1)
	go func() {
		conn, err := c.sshClient.Dial("unix", socketPath)
		done <- dialResult{conn: conn, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			return nil, fmt.Errorf(
				"sshclient: 打开远端 Unix 套接字 %s 失败: %w",
				socketPath,
				result.err,
			)
		}
		return result.conn, nil
	case <-dialCtx.Done():
		// 通道建立中的 goroutine 无法被中断；这里只保证调用方不会无限等待，
		// 并在通道建立成功后立即关闭它，避免资源泄漏。
		go func() {
			if result := <-done; result.conn != nil {
				_ = result.conn.Close()
			}
		}()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("sshclient: 打开远端 Unix 套接字被取消: %w", ctx.Err())
		}
		return nil, fmt.Errorf(
			"sshclient: 打开远端 Unix 套接字 %s 超时（%s）",
			socketPath,
			DefaultUnixSocketDialTimeout,
		)
	}
}
