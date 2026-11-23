package pilot

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// errExitCodeUnsupported 表示当前平台无法读取非子进程的退出码。
var errExitCodeUnsupported = errors.New("当前平台无法读取非子进程的退出码")

// waitExitCode 等待被测进程退出并返回退出码。
//
// Windows 下 os.Process.Wait 对任意同用户进程都可用：它只等待进程句柄并读取退出码，不接管
// 父子关系，用例因此能拿到"服务自己退出"的真实退出码。其它平台无法等待非子进程，返回
// errExitCodeUnsupported，由调用方改用 waitProcessGone 观察进程退出，并以服务日志里的
// 优雅关闭标记与端口核验作为依据。
func waitExitCode(process *os.Process, timeout time.Duration) (int, error) {
	if runtime.GOOS != "windows" {
		return 0, errExitCodeUnsupported
	}
	type waitResult struct {
		state *os.ProcessState
		err   error
	}
	done := make(chan waitResult, 1)
	go func() {
		state, err := process.Wait()
		done <- waitResult{state: state, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			var exitErr *exec.ExitError
			if errors.As(result.err, &exitErr) {
				return exitErr.ExitCode(), nil
			}
			return 0, fmt.Errorf("等待服务进程退出失败: %w", result.err)
		}
		return result.state.ExitCode(), nil
	case <-time.After(timeout):
		return 0, fmt.Errorf("等待服务进程退出超过 %s", timeout)
	}
}

// processGonePollInterval 是等待服务进程退出时的轮询间隔：进程消失后立即返回，不做固定休眠。
const processGonePollInterval = 50 * time.Millisecond

// waitProcessGone 在限时内等待服务进程真正从系统进程表中消失。
//
// 非 Windows 平台拿不到非子进程的退出码，但"进程是否还在"始终可观测：投递中断之后必须
// 等到 PID 消失再断言退出，否则会把正在退出的进程判成存活。等待超时按失败处理，不用服务
// 日志标记替代进程退出。
func waitProcessGone(c *testrunner.Context, pid int, timeout time.Duration) {
	c.Helper()
	ticker := time.NewTicker(processGonePollInterval)
	defer ticker.Stop()
	timeoutSignal := time.After(timeout)
	for {
		alive, err := testrunner.ProcessAlive(pid)
		if err != nil {
			c.Fatalf("无法确认服务进程 %d 是否退出: %v", pid, err)
		}
		if !alive {
			return
		}
		select {
		case <-c.Context().Done():
			c.Fatalf("等待服务进程 %d 退出时用例上下文结束: %v", pid, c.Context().Err())
		case <-timeoutSignal:
			c.Fatalf("服务进程 %d 在 %s 内仍未退出", pid, timeout)
		case <-ticker.C:
		}
	}
}
