//go:build !windows

package process

import (
	"os"
	"syscall"
)

// detachedAttributes 让后台进程进入新的会话。
//
// 新会话脱离当前终端：父进程退出或终端关闭都不会把后台进程一并带走，
// 后台进程也不再收到终端的作业控制信号。
func detachedAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// terminateSignal 请求后台进程有序退出。
//
// Unix 上使用 SIGTERM：后台进程可以按自己的关闭流程清理资源，而不是被立即杀掉。
func terminateSignal(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}
