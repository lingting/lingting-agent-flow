package testrunner

import (
	"context"
	"fmt"
	"net"
	"sync"
)

// LocalPortProvider 分配一个 127.0.0.1 上的监听端口。
//
// 监听器留在编排进程内并接受连接，因此 worker 拿到的 address 是真实可连通的：它用于
// 验证"声明 → 分配 → 序列化 → worker 注入 → 释放 → 自证"这条完整链路。
type LocalPortProvider struct {
	mu    sync.Mutex
	ports map[string]*localPortEntry
}

// localPortEntry 是一条已分配的端口记录。
type localPortEntry struct {
	handle    Handle
	listener  net.Listener
	done      chan struct{}
	closeOnce sync.Once
}

// NewLocalPortProvider 创建一个本地端口提供者。
func NewLocalPortProvider() *LocalPortProvider {
	return &LocalPortProvider{ports: make(map[string]*localPortEntry)}
}

// Acquire 监听一个随机本地端口，句柄的 payload 只暴露 address。
func (p *LocalPortProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的本地端口失败: %w", decl.Name, err)
	}
	address := listener.Addr().String()
	handle, err := buildHandle(decl, map[string]string{"address": address}, "")
	if err != nil {
		_ = listener.Close()
		return Handle{}, err
	}
	entry := &localPortEntry{handle: handle, listener: listener, done: make(chan struct{})}
	go entry.serve()
	p.mu.Lock()
	p.ports[address] = entry
	p.mu.Unlock()
	return handle, nil
}

// Release 关闭句柄对应的监听器，可重复调用。
func (p *LocalPortProvider) Release(ctx context.Context, handle Handle) error {
	address := handle.PayloadValue("address")
	p.mu.Lock()
	entry := p.ports[address]
	delete(p.ports, address)
	p.mu.Unlock()
	if entry == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		_ = entry.close()
		return fmt.Errorf("释放资源 %q 时上下文已结束: %w", handle.Name, err)
	}
	return entry.close()
}

// Verify 自证端口在释放后不再接受连接。
func (p *LocalPortProvider) Verify(ctx context.Context, handle Handle) error {
	address := handle.PayloadValue("address")
	p.mu.Lock()
	_, tracked := p.ports[address]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("端口 %s 仍被提供者持有，释放没有生效", address)
	}
	return verifyPortClosed(ctx, address)
}

// Snapshot 返回仍被占用的端口。
func (p *LocalPortProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.ports))
	for address, entry := range p.ports {
		entries = append(entries, LiveResource{
			Handle: entry.handle,
			Detail: "监听中的本地端口 " + address,
		})
	}
	return entries, nil
}

func (l *localPortEntry) serve() {
	defer close(l.done)
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

func (l *localPortEntry) close() error {
	l.closeOnce.Do(func() {
		_ = l.listener.Close()
	})
	<-l.done
	return nil
}
