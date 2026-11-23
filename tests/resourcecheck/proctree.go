package resourcecheck

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 进程树回收用例的时间预算。
const (
	// processTreeCaseTimeout 是该用例的执行上限。
	processTreeCaseTimeout = 90 * time.Second
	// processTreeWaitWindow 是等待监听就绪与等待端口关闭的各自上限。
	processTreeWaitWindow = 10 * time.Second
	// processTreePollInterval 是等待监听就绪与等待端口关闭的轮询间隔。
	processTreePollInterval = 50 * time.Millisecond
)

// ProcessTreeCase 验证进程树回收覆盖躲在另一个进程组里的后代。
//
// 现场来自前端 dev server 的真实故障：启动器把监听端口交给了新的进程组，只按进程组投递
// 信号时启动器退出、端口仍然可以连接。用例构造同样形状的进程树——根进程是独立进程组的
// 组长，监听进程用 setsid 进入新会话（进程组也因此变化），按根进程终止后监听必须停止。
var ProcessTreeCase = testrunner.Register(testrunner.Case{
	ID:      "resourcecheck/process-tree",
	Timeout: processTreeCaseTimeout,
	Run:     runProcessTree,
})

// runProcessTree 启动进程树、终止它，并确认换了进程组的监听进程一起退出。
func runProcessTree(c *testrunner.Context) {
	if runtime.GOOS == "windows" {
		c.Skipf("Windows 的进程树回收由 taskkill /T 覆盖，新进程组场景只在类 Unix 平台构造")
	}
	setsidPath, err := exec.LookPath("setsid")
	if err != nil {
		c.Skipf("当前环境没有 setsid，无法构造自行更换进程组的后代: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		c.Fatalf("获取执行器自身路径失败: %v", err)
	}
	address, err := freeLoopbackAddress()
	if err != nil {
		c.Fatalf("分配监听地址失败: %v", err)
	}
	// 根进程调用 wait 保持存活；监听进程由 setsid 启动。调用者不是进程组组长时 setsid
	// 直接 setsid+exec，因此监听进程仍是根进程的子进程，却已经不在根进程的进程组里：
	// 只按进程组终止会留下它继续监听端口。
	script := fmt.Sprintf("%q %q --serve %s & wait", setsidPath, executable, address)
	command := exec.Command("sh", "-c", script)
	command.SysProcAttr = testrunner.CommandProcessGroup()
	if err := command.Start(); err != nil {
		c.Fatalf("启动进程树失败: %v", err)
	}
	treeRoot := command.Process.Pid
	reaped := false
	reap := func() {
		if reaped {
			return
		}
		reaped = true
		_ = command.Wait()
	}
	c.Cleanup(func() {
		if err := testrunner.KillProcessTree(treeRoot); err != nil {
			c.Errorf("清理进程树失败: %v", err)
		}
		reap()
	})
	if err := waitListener(c, address); err != nil {
		c.Fatalf("进程树里的监听进程没有就绪: %v", err)
	}
	c.Logf("进程树已就绪：根进程 %d，监听地址 %s（监听进程在新的进程组里）", treeRoot, address)

	if err := testrunner.KillProcessTree(treeRoot); err != nil {
		c.Fatalf("终止进程树失败: %v", err)
	}
	if err := waitPortClosed(c, address); err != nil {
		c.Fatal(err)
	}
	reap()
	alive, err := testrunner.ProcessAlive(treeRoot)
	if err != nil {
		c.Fatalf("核验根进程 %d 是否存活失败: %v", treeRoot, err)
	}
	if alive {
		c.Fatalf("根进程 %d 在终止后仍然存活", treeRoot)
	}
	c.Logf("进程树已完整回收：根进程与更换了进程组的监听进程都已退出")
}

// freeLoopbackAddress 返回一个当前空闲的本地监听地址。
//
// 端口在监听进程启动前存在竞争窗口；用例随后会在限时内重试连接，窗口期失败表现为
// "监听进程没有就绪"，不会被当成通过。
func freeLoopbackAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return address, nil
}

// waitListener 在限时内等待监听地址可以连接。
func waitListener(c *testrunner.Context, address string) error {
	deadline := time.Now().Add(processTreeWaitWindow)
	for {
		conn, err := net.DialTimeout("tcp", address, processTreeWaitWindow)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s 在 %s 内没有监听: %w", address, processTreeWaitWindow, err)
		}
		select {
		case <-c.Context().Done():
			return fmt.Errorf("等待 %s 监听时用例上下文结束: %w", address, c.Context().Err())
		case <-time.After(processTreePollInterval):
		}
	}
}

// waitPortClosed 在限时内等待端口不再接受连接。
func waitPortClosed(c *testrunner.Context, address string) error {
	deadline := time.Now().Add(processTreeWaitWindow)
	for {
		conn, err := net.DialTimeout("tcp", address, processTreeWaitWindow)
		if err != nil {
			return nil
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			return fmt.Errorf("终止进程树后 %s 内端口 %s 仍可以连接", processTreeWaitWindow, address)
		}
		select {
		case <-c.Context().Done():
			return fmt.Errorf("等待端口 %s 关闭时用例上下文结束: %w", address, c.Context().Err())
		case <-time.After(processTreePollInterval):
		}
	}
}

// OrphanListenerCase 验证启动器退出后仍能定位并回收它留下的监听进程。
//
// 现场来自前端 dev server 的真实故障：pnpm / vp dev 先退出，实际监听由已经换了进程组的
// 进程持有，随后又被别的进程收养。此时从启动器 PID 出发的 PPID 与 PGID 两条线索都为空，
// 必须改用端口监听查询定位；否则端口会一直开着，而按启动器回收看不到任何"残留进程"。
var OrphanListenerCase = testrunner.Register(testrunner.Case{
	ID:      "resourcecheck/orphan-listener",
	Timeout: processTreeCaseTimeout,
	Run:     runOrphanListener,
})

// runOrphanListener 构造"启动器先退出、监听进程被收养"的进程树，并按端口监听查询回收。
func runOrphanListener(c *testrunner.Context) {
	if runtime.GOOS == "windows" {
		c.Skipf("孤儿监听进程场景用 setsid 构造，只在类 Unix 平台验证")
	}
	setsidPath, err := exec.LookPath("setsid")
	if err != nil {
		c.Skipf("当前环境没有 setsid，无法构造脱离会话的监听进程: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		c.Fatalf("获取执行器自身路径失败: %v", err)
	}
	address, err := freeLoopbackAddress()
	if err != nil {
		c.Fatalf("分配监听地址失败: %v", err)
	}
	port, err := listenerPort(address)
	if err != nil {
		c.Fatalf("%v", err)
	}
	c.Cleanup(func() {
		pids, pidErr := testrunner.ListenerPIDs(context.Background(), port)
		if pidErr != nil {
			c.Errorf("清理时无法定位端口 %d 的监听进程: %v", port, pidErr)
			return
		}
		for _, pid := range pids {
			if killErr := testrunner.KillProcessTree(pid); killErr != nil {
				c.Errorf("清理监听进程 %d 失败: %v", pid, killErr)
			}
		}
	})
	// 启动器把监听进程放进新会话后立即退出：监听进程随后被别的进程收养，PPID 与 PGID
	// 都不再指向启动器，按启动器进程树回收必然扑空。
	script := fmt.Sprintf("%q %q --serve %s >/dev/null 2>&1 & exit 0", setsidPath, executable, address)
	command := exec.Command("sh", "-c", script)
	if err := command.Start(); err != nil {
		c.Fatalf("启动孤儿监听进程失败: %v", err)
	}
	launcher := command.Process.Pid
	if err := command.Wait(); err != nil {
		c.Fatalf("等待启动器 %d 退出失败: %v", launcher, err)
	}
	alive, err := testrunner.ProcessAlive(launcher)
	if err != nil {
		c.Fatalf("核验启动器 %d 是否退出失败: %v", launcher, err)
	}
	if alive {
		c.Fatalf("启动器 %d 仍然存活，孤儿监听场景没有构造出来", launcher)
	}
	if err := waitListener(c, address); err != nil {
		c.Fatalf("孤儿监听进程没有就绪: %v", err)
	}
	listeners, err := testrunner.ListenerPIDs(c.Context(), port)
	if err != nil {
		c.Fatalf("无法定位孤儿监听进程: %v", err)
	}
	if len(listeners) == 0 {
		c.Fatalf("端口 %s 已可连接，但监听查询没有解析出任何进程", address)
	}
	c.Logf("孤儿监听进程已就绪：启动器 %d 已退出，监听进程 %v", launcher, listeners)

	for _, pid := range listeners {
		if err := testrunner.KillProcessTree(pid); err != nil {
			c.Fatalf("按监听进程号终止 %d 失败: %v", pid, err)
		}
	}
	if err := waitPortClosed(c, address); err != nil {
		c.Fatal(err)
	}
	for _, pid := range listeners {
		alive, err := testrunner.ProcessAlive(pid)
		if err != nil {
			c.Fatalf("核验监听进程 %d 是否存活失败: %v", pid, err)
		}
		if alive {
			c.Fatalf("监听进程 %d 在终止后仍然存活", pid)
		}
	}
	c.Logf("孤儿监听进程已按端口监听查询定位并回收：端口 %s 已关闭", address)
}

// listenerPort 从监听地址里取出端口号。
func listenerPort(address string) (int, error) {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return 0, fmt.Errorf("解析监听地址 %q 失败: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0, fmt.Errorf("监听地址 %q 的端口 %q 不是合法端口号: %w", address, portText, err)
	}
	return port, nil
}
