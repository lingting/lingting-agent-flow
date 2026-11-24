//go:build windows

package testrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// processGroupAttr 返回 worker 子进程的进程组属性。
//
// Windows 没有 POSIX 进程组，整棵进程树由 taskkill /T 覆盖，因此这里不需要额外属性。
func processGroupAttr() *syscall.SysProcAttr {
	return nil
}

// managedProcessGroupAttr 让托管子进程在自己的进程组里启动。
//
// Windows 下必须带 CREATE_NEW_PROCESS_GROUP：只有进程是组首进程时，才能用
// GenerateConsoleCtrlEvent 按组投递 CTRL_BREAK，让被测服务走优雅关闭（V3 R4 §4.3）。
func managedProcessGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// InterruptProcess 向托管进程所在的进程组投递一次中断，用于请求优雅停止。
//
// Windows 没有 SIGTERM，且 CTRL_C 无法按进程组投递（同控制台的其它进程也会收到），因此只投递
// CTRL_BREAK，由信号接收方的 Go 运行时映射成 os.Interrupt。投递失败如实返回错误：调用方据此
// 决定退回强制终止，并把「本次没有走优雅关闭」记录为已知差异。
func InterruptProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("投递中断信号失败: 进程号 %d 不合法", pid)
	}
	const ctrlBreakEvent = 1
	generate := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
	result, _, callErr := generate.Call(uintptr(ctrlBreakEvent), uintptr(pid))
	if result == 0 {
		return fmt.Errorf("向进程组 %d 投递 CTRL_BREAK 失败: %w", pid, callErr)
	}
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

// terminateProcessTree 用 taskkill /T /F 终止整棵进程树。
//
// 返回值为空：Windows 没有与 ps 等价的父子关系查询，回收结果由启动器自身的 Wait 与
// 端口自证确认，不在这里假装采集到了进程清单。
func terminateProcessTree(pid int) ([]processInfo, error) {
	return nil, killProcessTree(pid)
}

// IsProcessGoneError 判断进程查询是否遇到了进程已退出的错误。
//
// Windows 的进程查询不读取 /proc；这里保留跨平台 API，识别标准文件不存在错误。
func IsProcessGoneError(err error) bool {
	return err != nil && os.IsNotExist(err)
}

// describePortOccupied 在端口自证失败后补充占用该端口的进程号。
//
// Windows 没有与 ps 等价的父子关系查询，诊断只给出 netstat 查到的监听进程号；自证结论不因
// 诊断而改变：端口仍然可以连接就是释放失败。
func describePortOccupied(ctx context.Context, cause error, address string) error {
	port, err := listenerPortOf(address)
	if err != nil {
		return fmt.Errorf("%w（%v）", cause, err)
	}
	pids, queryErr := ListenerPIDs(ctx, port)
	if queryErr != nil {
		return fmt.Errorf("%w（无法定位监听进程：%v）", cause, queryErr)
	}
	if len(pids) == 0 {
		return fmt.Errorf("%w（端口 %d 仍可连接但查询没有解析出监听进程）", cause, port)
	}
	return fmt.Errorf("%w（监听进程号：%v）", cause, pids)
}

// terminateProcessTrees 用 taskkill /T /F 逐个终止这些进程树。
//
// 返回值为空：Windows 没有与 ps 等价的父子关系查询，回收结果由进程存活自证确认。
func terminateProcessTrees(pids []int) ([]processInfo, error) {
	var killErr error
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if err := killProcessTree(pid); err != nil {
			killErr = errors.Join(killErr, err)
		}
	}
	return nil, killErr
}

// processAlive 报告指定进程号是否仍然存在。
//
// Windows 没有可靠的 signal 0 语义，这里用 tasklist 按 PID 过滤：只有输出里出现该 PID 列
// 才认为进程仍在。查询失败时返回错误，由调用方判为"无法确认"而不是"已退出"。
func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	command := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV")
	output, err := command.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf(
			"查询进程 %d 失败: %w（tasklist 输出：%s）",
			pid,
			err,
			strings.TrimSpace(string(output)),
		)
	}
	return strings.Contains(string(output), `,"`+strconv.Itoa(pid)+`",`), nil
}
