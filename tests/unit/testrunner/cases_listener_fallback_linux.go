//go:build linux

package testrunner

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 原 TestListenerPIDsWithSS 用真实 ss 输出验证主查询路径。
//
// 解析单测只覆盖合成样例：真实输出里的列宽、IPv6 写法与进程列必须再核验一次，样例与真实
// 命令的漂移才不会漏过。没有 ss 的环境按原因跳过，不伪造通过。
func runListenerPIDsWithSS(t *testrunner.Context) {
	if _, err := exec.LookPath("ss"); err != nil {
		t.Skipf("当前环境没有 ss，无法验证主查询路径: %v", err)
	}
	port, closer := listenLoopback(t)
	defer closer()
	assertOwnListener(t, port)
}

// 原 TestListenerPIDsFallback 守住 ss 不可用时的 /proc 回退。
//
// 容器里没有 iproute2、或 ss 因参数与版本差异失败时，查询失败必须换路径继续定位，不能
// 折算成"没有 listener"：两种失败方式都要仍能查到当前进程自己监听的端口。
func runListenerPIDsFallback(t *testrunner.Context) {
	port, closer := listenLoopback(t)
	defer closer()
	// 用例覆盖 PATH：两个子测试各自恢复，第二个子测试基于原始 PATH 追加替身目录。
	basePath := os.Getenv("PATH")
	t.Run("ss 不存在", func(t *testrunner.Context) {
		setPath(t, tempDir(t))
		assertOwnListener(t, port)
	})

	t.Run("ss 非零退出", func(t *testrunner.Context) {
		dir := tempDir(t)
		script := []byte("#!/bin/sh\necho 'ss: unknown option' >&2\nexit 2\n")
		if err := os.WriteFile(filepath.Join(dir, "ss"), script, 0o755); err != nil {
			t.Fatalf("写入 ss 替身失败: %v", err)
		}
		setPath(t, dir+string(os.PathListSeparator)+basePath)
		assertOwnListener(t, port)
	})
}

// listenLoopback 监听一个随机的本地端口，返回端口号与关闭函数。
func listenLoopback(t *testrunner.Context) (int, func()) {
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
func assertOwnListener(t *testrunner.Context, port int) {
	t.Helper()
	pids, err := testrunner.ListenerPIDs(context.Background(), port)
	if err != nil {
		t.Fatalf("未能定位端口 %d 的监听进程: %v", port, err)
	}
	if !slices.Contains(pids, os.Getpid()) {
		t.Fatalf("监听进程号 %v 不含当前进程 %d", pids, os.Getpid())
	}
}

// tempDir 创建用例临时目录，创建失败即终止用例。
//
// 统一执行器的 Context.TempDir 把错误交回用例（ROADMAP §9.2），这里集中处理一次。
func tempDir(t *testrunner.Context) string {
	t.Helper()
	dir, err := t.TempDir()
	if err != nil {
		t.Fatalf("创建用例临时目录失败: %v", err)
	}
	return dir
}

// setPath 覆盖 PATH 并在用例结束时恢复。
//
// 统一执行器不提供 testing.T.Setenv（ROADMAP §9.2）：复用 worker 在用例之间会重置环境
// 变量，这里再补一次用例内的恢复，同一条 Case 的多个子测试因此不会互相污染。
func setPath(t *testrunner.Context, value string) {
	t.Helper()
	previous, existed := os.LookupEnv("PATH")
	if err := os.Setenv("PATH", value); err != nil {
		t.Fatalf("设置 PATH 失败: %v", err)
	}
	t.Cleanup(func() {
		if existed {
			if err := os.Setenv("PATH", previous); err != nil {
				t.Errorf("恢复 PATH 失败: %v", err)
			}
			return
		}
		if err := os.Unsetenv("PATH"); err != nil {
			t.Errorf("恢复 PATH 失败: %v", err)
		}
	})
}
