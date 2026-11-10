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

// managedProcessGroupAttr 让托管子进程成为独立进程组，与 worker 的处理保持一致。
//
// 独立进程组是中断投递的前提：只有组首进程才能按 -pid 收到信号，其它用例的进程不会
// 被同一次投递连带终止。
func managedProcessGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// InterruptProcess 向托管进程所在的进程组投递 SIGINT，用于请求优雅停止。
func InterruptProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("投递中断信号失败: 进程号 %d 不合法", pid)
	}
	if err := syscall.Kill(-pid, syscall.SIGINT); err != nil {
		return fmt.Errorf("向进程组 %d 投递 SIGINT 失败: %w", pid, err)
	}
	return nil
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
