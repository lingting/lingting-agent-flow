package resourcecheck

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
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
