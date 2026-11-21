//go:build !linux && !windows

package testrunner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// lsofNoMatchExitCode 是 lsof 没有匹配项时的退出码。
const lsofNoMatchExitCode = 1

// queryPortListeners 用 lsof 查询监听指定端口的进程。
//
// lsof 在没有匹配项时以退出码 1 结束且输出为空：这属于"查询成功但没有 listener"，与命令
// 不存在、参数不被支持等执行失败必须区分，后者如实返回错误。
func queryPortListeners(ctx context.Context, port int) (listenerQuery, error) {
	query := listenerQuery{
		Port:    port,
		Command: fmt.Sprintf("lsof -nP -t -iTCP:%d -sTCP:LISTEN", port),
	}
	output, err := runListenerCommand(ctx, "lsof", "-nP", "-t", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN")
	query.Output = output
	var exitErr *exec.ExitError
	if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode() == lsofNoMatchExitCode) {
		return query, fmt.Errorf("查询端口 %d 的监听进程失败: %w", port, err)
	}
	parseLsofListeners(&query)
	return query, nil
}

// ParseListenerOutput 解析 lsof -nP -t 的原始输出，返回指定端口的监听进程号。
//
// lsof 的 -t 模式只输出进程号，一行一个；解析与命令执行分开暴露，供自检用例单独验证格式
// 变化，不必依赖本机是否有 listener。
func ParseListenerOutput(output string, port int) ([]int, error) {
	query := listenerQuery{Port: port, Command: "lsof -nP -t", Output: output}
	parseLsofListeners(&query)
	return listenerPIDsOf(query)
}

// parseLsofListeners 从 lsof -t 的输出里解析监听进程号。
func parseLsofListeners(query *listenerQuery) {
	for _, line := range strings.Split(query.Output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// -t 模式只输出进程号：非空却解析不出进程号说明输出格式已变，按矛盾报错。
		query.Matched = true
		pid, err := strconv.Atoi(trimmed)
		if err != nil {
			continue
		}
		query.PIDs = append(query.PIDs, pid)
	}
	query.PIDs = uniquePIDs(query.PIDs)
}
