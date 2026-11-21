//go:build linux

package testrunner_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lingting/agentflow/internal/testrunner"
)

// TestListenerPIDsWithSS 用真实 ss 输出验证主查询路径。
//
// 解析单测只覆盖合成样例：真实输出里的列宽、IPv6 写法与进程列必须再核验一次，样例与真实
// 命令的漂移才不会漏过。没有 ss 的环境按原因跳过，不伪造通过。
func TestListenerPIDsWithSS(t *testing.T) {
	if _, err := exec.LookPath("ss"); err != nil {
		t.Skipf("当前环境没有 ss，无法验证主查询路径: %v", err)
	}
	port, closer := listenLoopback(t)
	defer closer()
	assertOwnListener(t, port)
}

// TestListenerPIDsFallback 守住 ss 不可用时的 /proc 回退。
//
// 容器里没有 iproute2、或 ss 因参数与版本差异失败时，查询失败必须换路径继续定位，不能
// 折算成"没有 listener"：两种失败方式都要仍能查到当前进程自己监听的端口。
func TestListenerPIDsFallback(t *testing.T) {
	port, closer := listenLoopback(t)
	defer closer()

	t.Run("ss 不存在", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		assertOwnListener(t, port)
	})

	t.Run("ss 非零退出", func(t *testing.T) {
		dir := t.TempDir()
		script := []byte("#!/bin/sh\necho 'ss: unknown option' >&2\nexit 2\n")
		if err := os.WriteFile(filepath.Join(dir, "ss"), script, 0o755); err != nil {
			t.Fatalf("写入 ss 替身失败: %v", err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		assertOwnListener(t, port)
	})
}

// listenLoopback 监听一个随机的本地端口，返回端口号与关闭函数。
func listenLoopback(t *testing.T) (int, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听本地端口失败: %v", err)
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatalf("监听地址 %s 不是 TCP 地址", listener.Addr())
	}
	return tcpAddr.Port, func() { _ = listener.Close() }
}

// assertOwnListener 断言指定端口能被定位到当前测试进程。
func assertOwnListener(t *testing.T, port int) {
	t.Helper()
	pids, err := testrunner.ListenerPIDs(context.Background(), port)
	if err != nil {
		t.Fatalf("未能定位端口 %d 的监听进程: %v", port, err)
	}
	if !slices.Contains(pids, os.Getpid()) {
		t.Fatalf("监听进程号 %v 不含当前进程 %d", pids, os.Getpid())
	}
}
