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
