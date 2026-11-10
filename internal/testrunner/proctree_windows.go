//go:build windows

package testrunner

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// processGroupAttr 返回子进程的进程组属性。
//
// Windows 没有 POSIX 进程组，整棵进程树由 taskkill /T 覆盖，因此这里不需要额外属性。
func processGroupAttr() *syscall.SysProcAttr {
	return nil
}

// killProcessTree 用 taskkill /T /F 终止整棵进程树。
//
// 覆盖 worker → 用例启动的子进程 → Pi CLI / Docker CLI 这条链：exec.Cmd 的 Kill 只终止
// 直接子进程，无法回收孙进程。
func killProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	command := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	// 进程已经退出时 taskkill 返回 128，或用 "not found" 说明找不到进程，都按成功处理。
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 128 {
		return nil
	}
	if strings.Contains(strings.ToLower(string(output)), "not found") {
		return nil
	}
	return fmt.Errorf(
		"终止 worker 进程树失败: %w（taskkill 输出：%s）",
		err,
		strings.TrimSpace(string(output)),
	)
}
