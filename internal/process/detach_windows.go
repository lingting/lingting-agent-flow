//go:build windows

package process

import (
	"os"
	"syscall"
)

// Windows 进程创建标志。
const (
	// createNewProcessGroup 让后台进程自成一个进程组，不受父进程控制台的组信号影响。
	createNewProcessGroup = 0x00000200
	// detachedProcess 让后台进程不继承控制台，父进程退出或终端关闭不会结束它。
	detachedProcess = 0x00000008
)

// detachedAttributes 让后台进程脱离当前控制台并自成进程组。
//
// 后台进程没有控制台，因此它的输出只能是启动时传入的文件句柄；这也保证父进程退出
// 后它不会因为终端消失而收到控制台关闭事件。
func detachedAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}

// terminateSignal 请求后台进程退出。
//
// Windows 上无法向无控制台的进程发送有序退出信号，只能强制终止：调用方仍需按
// 宽限期等待，超时后再调用 Kill 兜底。
func terminateSignal(process *os.Process) error {
	return process.Kill()
}
