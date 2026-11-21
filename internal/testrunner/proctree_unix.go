//go:build !windows

package testrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

// killProcessTree 终止 pid 及其整棵进程树，覆盖该进程启动的所有子进程。
func killProcessTree(pid int) error {
	_, err := terminateProcessTree(pid)
	return err
}

// terminateProcessTree 终止 pid 及其整棵进程树，并返回终止前采集到的进程信息。
//
// 只按进程组发信号不够：树里的后代可能把自己放进新的进程组，组信号因此漏掉真正监听
// 端口的进程。采集必须在终止之前完成——根进程一退出，父子链就断了，只剩进程组还能
// 说明谁与它同属一棵树。终止顺序从叶到根，最后再补一次组信号，覆盖采集之后新生的后代。
func terminateProcessTree(pid int) ([]processInfo, error) {
	if pid <= 0 {
		return nil, nil
	}
	members, err := processTreeMembers(pid)
	if err != nil {
		return nil, fmt.Errorf("采集进程 %d 的进程树失败: %w", pid, err)
	}
	var killErr error
	for index := len(members) - 1; index >= 0; index-- {
		if err := killProcess(members[index].PID); err != nil {
			killErr = errors.Join(killErr, err)
		}
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		killErr = errors.Join(killErr, fmt.Errorf("终止进程组 %d 失败: %w", pid, err))
	}
	if killErr != nil {
		return members, fmt.Errorf("终止进程树失败: %w", killErr)
	}
	return members, nil
}

// killProcess 向单个进程投递 SIGKILL，进程已退出不算失败。
func killProcess(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("终止进程 %d 失败: %w", pid, err)
	}
	return nil
}

// processTreeMembers 返回 pid 及其整棵进程树：父子链可达的后代与同一进程组的成员。
//
// 两种来源都要：后代可能自行创建新进程组，从而躲开按组投递的信号；启动器也可能先于
// 后代退出，使父子链断开，此时只剩进程组还能说明它们同属一棵树。
func processTreeMembers(pid int) ([]processInfo, error) {
	if pid <= 0 {
		return nil, nil
	}
	all, err := listProcesses()
	if err != nil {
		return nil, err
	}
	byPID := make(map[int]processInfo, len(all))
	children := make(map[int][]int, len(all))
	for _, info := range all {
		byPID[info.PID] = info
		children[info.PPID] = append(children[info.PPID], info.PID)
	}
	collected := make(map[int]struct{})
	if _, exists := byPID[pid]; exists {
		collected[pid] = struct{}{}
	}
	for queue := []int{pid}; len(queue) > 0; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			if _, seen := collected[child]; seen {
				continue
			}
			collected[child] = struct{}{}
			queue = append(queue, child)
		}
	}
	for _, info := range all {
		if info.PGID == pid {
			collected[info.PID] = struct{}{}
		}
	}
	members := make([]processInfo, 0, len(collected))
	for id := range collected {
		if info, exists := byPID[id]; exists {
			members = append(members, info)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].PID < members[j].PID })
	return members, nil
}

// listProcesses 用 ps 列出全部进程的父子关系、进程组与状态。
//
// 用 ps 而不是直接读 /proc：Linux 之外的类 Unix 平台没有 /proc，ps 的输出格式在这些
// 平台上一致。列顺序与解析必须一起改。
func listProcesses() ([]processInfo, error) {
	output, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,pgid=,stat=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("列举进程失败: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	infos := make([]processInfo, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		pgid, pgidErr := strconv.Atoi(fields[2])
		if pidErr != nil || ppidErr != nil || pgidErr != nil {
			continue
		}
		infos = append(infos, processInfo{
			PID:  pid,
			PPID: ppid,
			PGID: pgid,
			Stat: fields[3],
			Args: strings.Join(fields[4:], " "),
		})
	}
	return infos, nil
}

// processAlive 报告指定进程号是否仍然存在。
//
// 用 signal 0 探测进程存在性：ESRCH 表示已退出，EPERM 表示进程存在但当前用户无权发信号。
// 已经结束、只等父进程回收的进程（zombie）不再执行代码也不再持有端口，按已退出处理。
func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	zombie, err := processZombie(pid)
	if err != nil {
		return false, err
	}
	if zombie {
		return false, nil
	}
	err = syscall.Kill(pid, 0)
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

// processZombie 报告进程是否已经结束、只等父进程回收。
//
// 只有 Linux 的 /proc 提供这个状态；其它平台读不到该文件时返回 false，由 signal 0 判定。
func processZombie(pid int) (bool, error) {
	content, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("读取进程 %d 的状态失败: %w", pid, err)
	}
	// stat 的第二个字段是括号里的命令名，它可能含空格与括号，因此从最后一个右括号之后取状态。
	text := string(content)
	closing := strings.LastIndex(text, ")")
	if closing < 0 || closing+2 >= len(text) {
		return false, nil
	}
	return text[closing+2] == 'Z', nil
}

// describePortOccupied 在端口自证失败后补充占用该端口的进程信息。
//
// 自证结论不因诊断而改变：端口仍然可以连接就是释放失败，诊断只负责说明是谁占着它。查询
// 没有解析出进程号时按矛盾处理并输出原始输出：TCP 连接成功已经证明监听项存在。
func describePortOccupied(ctx context.Context, cause error, address string) error {
	port, err := listenerPortOf(address)
	if err != nil {
		return fmt.Errorf("%w（%v）", cause, err)
	}
	query, queryErr := queryPortListeners(ctx, port)
	if queryErr != nil {
		return fmt.Errorf("%w（无法定位监听进程：%v）", cause, queryErr)
	}
	if len(query.PIDs) == 0 {
		return fmt.Errorf(
			"%w（端口 %d 仍可连接但查询没有解析出监听进程：%s）",
			cause,
			port,
			describeListenerQuery(query),
		)
	}
	return fmt.Errorf("%w（监听进程：%s）", cause, describeProcesses(listenerInfos(query.PIDs)))
}

// listenerInfos 把监听进程号补全为进程观测信息，进程已退出时至少保留进程号。
func listenerInfos(pids []int) []processInfo {
	infos := make([]processInfo, 0, len(pids))
	byPID := make(map[int]processInfo, len(pids))
	if all, err := listProcesses(); err == nil {
		for _, info := range all {
			byPID[info.PID] = info
		}
	}
	for _, pid := range pids {
		info, exists := byPID[pid]
		if !exists {
			info = processInfo{PID: pid}
		}
		infos = append(infos, info)
	}
	return infos
}

// terminateProcessTrees 终止多个进程号各自的进程树，并返回终止前采集到的全部进程信息。
//
// 多个监听进程可能共享同一个监听 socket，必须全部回收；已经退出的进程号按空树处理，不把
// ESRCH 当成失败。终止失败与采集结果一并返回：等待进程消失仍然需要这份清单。
func terminateProcessTrees(pids []int) ([]processInfo, error) {
	members := make([]processInfo, 0, len(pids))
	var killErr error
	seen := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, exists := seen[pid]; exists {
			continue
		}
		seen[pid] = struct{}{}
		collected, err := terminateProcessTree(pid)
		members = append(members, collected...)
		if err != nil {
			killErr = errors.Join(killErr, err)
		}
	}
	return members, killErr
}
