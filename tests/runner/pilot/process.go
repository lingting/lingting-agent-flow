package pilot

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// errExitCodeUnsupported 表示当前平台无法读取非子进程的退出码。
var errExitCodeUnsupported = errors.New("当前平台无法读取非子进程的退出码")

// waitExitCode 等待被测进程退出并返回退出码。
//
// Windows 下 os.Process.Wait 对任意同用户进程都可用：它只等待进程句柄并读取退出码，不接管
// 父子关系，用例因此能拿到"服务自己退出"的真实退出码。其它平台无法等待非子进程，返回
// errExitCodeUnsupported，由调用方改用服务日志里的优雅关闭标记作为依据，并记录为已知差异。
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
