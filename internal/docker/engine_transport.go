package docker

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Docker Engine API 直连访问的默认参数。
const (
	// DefaultEngineAPIProbeTimeout 是单次 Engine API 探测的超时时间。
	DefaultEngineAPIProbeTimeout = 8 * time.Second
	// DefaultDockerSocketPath 是远端 Docker Engine 的默认 Unix 套接字路径。
	DefaultDockerSocketPath = "/var/run/docker.sock"
	// maxEngineAPIResponseBytes 是 Engine API 响应体的读取上限。
	maxEngineAPIResponseBytes = 1 << 20
)

// unixSocketTransport 把 HTTP 请求转发到一条已建立的 Unix 套接字通道。
//
// 通道由 SSH 连接提供，因此每个 Runner 调用只使用一条通道，不使用连接池。
type unixSocketTransport struct {
	conn net.Conn
}

// RoundTrip 在 Unix 套接字通道上完成一次 HTTP 往返。
//
// net/http 只在传输层实现 CancelRequest 或自行监听上下文时才会中断请求，
// 自定义传输不会自动获得该行为。这里显式监听请求上下文：取消或超时时关闭
// 通道，使阻塞中的写入与读取立即返回，避免请求在给定时限之外继续挂起。
func (t *unixSocketTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.conn == nil {
		return nil, fmt.Errorf("docker: Unix 套接字通道未建立")
	}
	ctx := request.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = t.conn.Close()
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		<-watcherDone
	}()
	// SSH 通道不支持 SetDeadline（会返回 "deadline not supported"），
	// 因此截止时间只能通过关闭通道来生效，取消与超时由上面的监听协程处理。
	if err := request.Write(t.conn); err != nil {
		return nil, t.contextError(ctx, err)
	}
	response, err := http.ReadResponse(bufio.NewReader(t.conn), request)
	if err != nil {
		return nil, t.contextError(ctx, err)
	}
	return response, nil
}

// contextError 优先返回上下文错误，使取消与超时能被正确分类。
func (t *unixSocketTransport) contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}
