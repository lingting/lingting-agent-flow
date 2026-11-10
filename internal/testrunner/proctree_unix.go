//go:build !windows

package testrunner

import (
	"errors"
	"fmt"
	"syscall"
)

// processGroupAttr 让 worker 成为独立进程组，便于按组终止整棵进程树。
func processGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree 终止 worker 进程组，覆盖用例启动的所有子进程。
func killProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("终止 worker 进程组失败: %w", err)
	}
	return nil
}

// processAlive 报告指定进程号是否仍然存在。
//
// 用 signal 0 探测进程存在性：ESRCH 表示已退出，EPERM 表示进程存在但当前用户无权发信号。
func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	case errors.Is(err, syscall.EPERM):
		return true, nil
	default:
		return false, fmt.Errorf("查询进程 %d 失败: %w", pid, err)
	}
}
