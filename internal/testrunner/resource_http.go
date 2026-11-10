package testrunner

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// LocalHTTPServerProvider 在编排进程内启动一个只返回 200 的 HTTP 服务。
//
// 它对应 R0 词汇表里的 local-http-server：用例只需要一个真实可访问的 HTTP 端点。
// 分配阶段会先探测端点返回 200，确认服务真的就绪后才下发句柄。
type LocalHTTPServerProvider struct {
	mu      sync.Mutex
	servers map[string]*httpServerEntry
}

// httpServerEntry 是一条已启动的 HTTP 服务记录。
type httpServerEntry struct {
	handle   Handle
	server   *http.Server
	listener net.Listener
	done     chan struct{}
}

// NewLocalHTTPServerProvider 创建一个进程内 HTTP 服务提供者。
func NewLocalHTTPServerProvider() *LocalHTTPServerProvider {
	return &LocalHTTPServerProvider{servers: make(map[string]*httpServerEntry)}
}

// Acquire 启动服务并等待端点就绪，句柄的 payload 只暴露 base_url。
func (p *LocalHTTPServerProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的监听端口失败: %w", decl.Name, err)
	}
	baseURL := "http://" + listener.Addr().String()
	server := &http.Server{
		Handler:           http.HandlerFunc(serveLocalHTTPProbe),
		ReadHeaderTimeout: resourceProbeTimeout,
	}
	entry := &httpServerEntry{
		server:   server,
		listener: listener,
		done:     make(chan struct{}),
	}
	go func() {
		defer close(entry.done)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			return
		}
	}()
	if err := waitHTTPReady(ctx, baseURL, resourceProbeTimeout); err != nil {
		_ = server.Close()
		return Handle{}, fmt.Errorf("分配资源 %q 时服务没有就绪: %w", decl.Name, err)
	}
	handle, err := buildHandle(decl, map[string]string{"base_url": baseURL}, "")
	if err != nil {
		_ = server.Close()
		return Handle{}, err
	}
	entry.handle = handle
	p.mu.Lock()
	p.servers[baseURL] = entry
	p.mu.Unlock()
	return handle, nil
}

// Release 关闭服务与监听器，可重复调用。
func (p *LocalHTTPServerProvider) Release(ctx context.Context, handle Handle) error {
	baseURL := handle.PayloadValue("base_url")
	p.mu.Lock()
	entry := p.servers[baseURL]
	delete(p.servers, baseURL)
	p.mu.Unlock()
	if entry == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		_ = entry.server.Close()
		<-entry.done
		return fmt.Errorf("释放资源 %q 时上下文已结束: %w", handle.Name, err)
	}
	_ = entry.server.Close()
	<-entry.done
	return nil
}

// Verify 自证服务端点在释放后不再响应。
func (p *LocalHTTPServerProvider) Verify(ctx context.Context, handle Handle) error {
	baseURL := handle.PayloadValue("base_url")
	p.mu.Lock()
	_, tracked := p.servers[baseURL]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("服务 %s 仍被提供者持有，释放没有生效", baseURL)
	}
	return verifyHTTPGone(ctx, baseURL)
}

// Snapshot 返回仍在监听的服务。
func (p *LocalHTTPServerProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.servers))
	for baseURL, entry := range p.servers {
		entries = append(entries, LiveResource{Handle: entry.handle, Detail: "监听中的 HTTP 服务 " + baseURL})
	}
	return entries, nil
}

// serveLocalHTTPProbe 是本地 HTTP 服务的固定响应：任何路径都返回 200。
func serveLocalHTTPProbe(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("agentflow-test-resource\n"))
}
