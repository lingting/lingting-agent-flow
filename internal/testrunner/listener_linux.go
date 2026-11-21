//go:build linux

package testrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ssListenerPattern 匹配 ss -p 输出里的 "pid=<进程号>" 片段。
var ssListenerPattern = regexp.MustCompile(`pid=(\d+)`)

// queryPortListeners 查询监听指定端口的进程：优先用 ss，失败时退回 /proc 的 socket inode 关联。
//
// ss 不加端口过滤：过滤表达式与输出格式在不同版本上都能把查询失败伪装成空结果，因此统一取
// 全量监听列表再按解析后的本地端口匹配。ss 不可用或执行失败时不折算成空集合，改走 /proc。
func queryPortListeners(ctx context.Context, port int) (listenerQuery, error) {
	query := listenerQuery{Port: port, Command: "ss -H -ltnp"}
	output, err := runListenerCommand(ctx, "ss", "-H", "-ltnp")
	query.Output = output
	if err == nil {
		parseSSListeners(&query)
		return query, nil
	}
	fallback, fallbackErr := queryPortListenersProc(port)
	if fallbackErr != nil {
		return query, fmt.Errorf(
			"查询端口 %d 的监听进程失败: %w（/proc 回退同样失败: %v）",
			port,
			err,
			fallbackErr,
		)
	}
	return fallback, nil
}

// ParseListenerOutput 解析 ss -H -ltnp 的原始输出，返回指定端口的监听进程号。
//
// 解析与命令执行分开暴露：过滤表达式与输出格式随版本变化，"匹配到监听项却解析不出进程号"
// 必须能在自检里单独复现，不能只靠真实环境碰运气。
func ParseListenerOutput(output string, port int) ([]int, error) {
	query := listenerQuery{Port: port, Command: "ss -H -ltnp", Output: output}
	parseSSListeners(&query)
	return listenerPIDsOf(query)
}

// parseSSListeners 从 ss -H -ltnp 的全量输出里挑出监听指定端口的行。
//
// 列固定为状态、Recv-Q、Send-Q、本地地址、对端地址、进程：只按解析出的本地端口匹配，
// 进程号仍从同一行的 pid= 片段读取。
func parseSSListeners(query *listenerQuery) {
	for _, line := range strings.Split(query.Output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || localPort(fields[3]) != query.Port {
			continue
		}
		query.Matched = true
		for _, match := range ssListenerPattern.FindAllStringSubmatch(line, -1) {
			if pid, err := strconv.Atoi(match[1]); err == nil {
				query.PIDs = append(query.PIDs, pid)
			}
		}
	}
	query.PIDs = uniquePIDs(query.PIDs)
}

// queryPortListenersProc 用 /proc/net/tcp 的 socket inode 与 /proc/<pid>/fd 关联定位监听进程。
//
// ss 不可用（容器里没有 iproute2）时这是 Linux 上唯一不依赖外部命令的定位方式：监听表的
// inode 与进程打开的文件描述符指向同一个 socket。
func queryPortListenersProc(port int) (listenerQuery, error) {
	query := listenerQuery{Port: port, Command: "/proc/net/tcp 与 /proc/<pid>/fd"}
	inodes, lines, err := listeningSocketInodes(port)
	query.Output = strings.Join(lines, "\n")
	if err != nil {
		return query, err
	}
	if len(inodes) == 0 {
		return query, nil
	}
	query.Matched = true
	pids, err := socketInodePIDs(inodes)
	query.PIDs = pids
	if err != nil {
		return query, err
	}
	return query, nil
}

// listeningSocketInodes 返回监听指定端口的 socket inode 与匹配到的原始行。
func listeningSocketInodes(port int) ([]string, []string, error) {
	inodes := make([]string, 0, 2)
	lines := make([]string, 0, 2)
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		content, err := os.ReadFile(name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, fmt.Errorf("读取 %s 失败: %w", name, err)
		}
		rows := strings.Split(string(content), "\n")
		for _, row := range rows[1:] {
			fields := strings.Fields(row)
			// 0A 是 TCP 的 LISTEN 状态；inode 固定在第 10 列。
			if len(fields) < 10 || !strings.EqualFold(fields[3], "0A") {
				continue
			}
			if procLocalPort(fields[1]) != port {
				continue
			}
			inodes = append(inodes, fields[9])
			lines = append(lines, strings.TrimSpace(row))
		}
	}
	return inodes, lines, nil
}

// procLocalPort 解析 /proc/net/tcp 里 "0100007F:9C35" 形式的本地地址端口（十六进制）。
func procLocalPort(address string) int {
	index := strings.LastIndex(address, ":")
	if index < 0 || index+1 >= len(address) {
		return -1
	}
	port, err := strconv.ParseInt(strings.TrimSpace(address[index+1:]), 16, 32)
	if err != nil {
		return -1
	}
	return int(port)
}

// socketInodePIDs 扫描 /proc/<pid>/fd，返回打开这些 socket inode 的进程号。
//
// 单个进程读不到 fd 目录（已退出或属于其他用户）时跳过，不影响其它进程的关联结果。
func socketInodePIDs(inodes []string) ([]int, error) {
	wanted := make(map[string]struct{}, len(inodes))
	for _, inode := range inodes {
		wanted["socket:["+inode+"]"] = struct{}{}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("读取 /proc 失败: %w", err)
	}
	pids := make([]int, 0, 4)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if openedSocketInode(filepath.Join("/proc", entry.Name(), "fd"), wanted) {
			pids = append(pids, pid)
		}
	}
	return uniquePIDs(pids), nil
}

// openedSocketInode 报告进程的 fd 目录里是否有指向目标 socket inode 的描述符。
func openedSocketInode(dir string, wanted map[string]struct{}) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if _, exists := wanted[target]; exists {
			return true
		}
	}
	return false
}
