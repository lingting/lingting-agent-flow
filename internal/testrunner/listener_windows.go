//go:build windows

package testrunner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// queryPortListeners 用 netstat 查询监听指定端口的进程。
//
// netstat 没有按端口过滤的参数，因此取全量 TCP 监听表后按解析出的本地端口匹配；命令失败
// 如实返回错误，不折算成"没有 listener"。
func queryPortListeners(ctx context.Context, port int) (listenerQuery, error) {
	query := listenerQuery{Port: port, Command: "netstat -ano -p tcp"}
	output, err := runListenerCommand(ctx, "netstat", "-ano", "-p", "tcp")
	query.Output = output
	if err != nil {
		return query, fmt.Errorf("查询端口 %d 的监听进程失败: %w", port, err)
	}
	parseNetstatListeners(&query)
	return query, nil
}

// ParseListenerOutput 解析 netstat -ano -p tcp 的原始输出，返回指定端口的监听进程号。
//
// 解析与命令执行分开暴露：netstat 的列宽与地址写法随系统版本变化，供自检用例单独验证。
func ParseListenerOutput(output string, port int) ([]int, error) {
	query := listenerQuery{Port: port, Command: "netstat -ano -p tcp", Output: output}
	parseNetstatListeners(&query)
	return listenerPIDsOf(query)
}

// parseNetstatListeners 从 netstat -ano 的输出里挑出监听指定端口的行。
//
// 列固定为协议、本地地址、对端地址、状态、进程号：只接受 LISTENING 行，避免把已建立的
// 连接也算成本次资源的监听进程。
func parseNetstatListeners(query *listenerQuery) {
	for _, line := range strings.Split(query.Output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		if !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		if localPort(fields[1]) != query.Port {
			continue
		}
		query.Matched = true
		pid, err := strconv.Atoi(fields[4])
		if err != nil {
			continue
		}
		query.PIDs = append(query.PIDs, pid)
	}
	query.PIDs = uniquePIDs(query.PIDs)
}
