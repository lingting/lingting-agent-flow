package testrunner

import (
	"context"
	"fmt"
	"net"
	"sync"
)

// LocalPortProvider 是 R1 内置的最小资源提供者：分配一个 127.0.0.1 上的监听端口。
//
// 监听器留在编排进程内并接受连接，因此 worker 拿到的 address 是真实可连通的；它用于验证
// "声明 → 分配 → 序列化 → worker 注入 → 释放"这条链路。其余资源类型的提供者在 R2 补齐。
type LocalPortProvider struct {
	mu        sync.Mutex
	listeners map[string]*localPortListener
}

// NewLocalPortProvider 创建一个本地端口提供者。
func NewLocalPortProvider() *LocalPortProvider {
	return &LocalPortProvider{listeners: make(map[string]*localPortListener)}
}

// Acquire 监听一个随机本地端口，句柄的 payload 只暴露 address。
func (p *LocalPortProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 前上下文已结束: %w", decl.Name, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的本地端口失败: %w", decl.Name, err)
	}
	entry := &localPortListener{listener: listener, done: make(chan struct{})}
	go entry.serve()
	key := localPortKey(decl.Name, listener.Addr().String())
	p.mu.Lock()
	p.listeners[key] = entry
	p.mu.Unlock()
	return Handle{
		Name:    decl.Name,
		Type:    decl.Type,
		Scope:   decl.Scope,
		Payload: map[string]string{"address": listener.Addr().String()},
	}, nil
}

// Release 关闭句柄对应的监听器，可重复调用。
func (p *LocalPortProvider) Release(ctx context.Context, handle Handle) error {
	key := localPortKey(handle.Name, handle.PayloadValue("address"))
	p.mu.Lock()
	entry := p.listeners[key]
	delete(p.listeners, key)
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

// Open 返回当前仍被占用的端口数量，供运行级终检使用。
func (p *LocalPortProvider) Open() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.listeners)
}

func localPortKey(name, address string) string {
	return name + "|" + address
}

// localPortListener 是一个只接受并立即关闭连接的监听器，可重复关闭。
type localPortListener struct {
	listener  net.Listener
	done      chan struct{}
	closeOnce sync.Once
}

func (l *localPortListener) serve() {
	defer close(l.done)
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

func (l *localPortListener) close() error {
	l.closeOnce.Do(func() {
		_ = l.listener.Close()
	})
	<-l.done
	return nil
}
