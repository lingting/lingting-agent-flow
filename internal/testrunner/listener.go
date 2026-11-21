package testrunner

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// listenerQuery 是一次端口监听进程查询的结构化结果。
//
// 三种情况必须能区分：查询命令执行失败、查询成功但没有该端口的监听项、查询结果里出现了
// 该端口的监听项却没有解析出进程号。只有第二种表示"没有 listener"；第三种是诊断矛盾，
// 不能折算成空集合。
type listenerQuery struct {
	// Port 是被查询的端口号。
	Port int
	// Command 是实际执行的查询命令，用于失败诊断。
	Command string
	// Output 是查询命令的原始输出，用于失败诊断。
	Output string
	// Matched 表示查询结果里出现了该端口的监听项。
	Matched bool
	// PIDs 是从查询结果里解析出的监听进程号。
	PIDs []int
}

// listenerDiscoveryTimeout 是等待端口监听项可被查询到的时间上限。
const listenerDiscoveryTimeout = 5 * time.Second

// listenerDiscoveryPollInterval 是等待端口监听项时的轮询间隔。
const listenerDiscoveryPollInterval = 100 * time.Millisecond

// ListenerPIDs 返回监听指定端口的进程号，供资源回收与残留诊断复用。
//
// 查询命令执行失败返回错误；端口确有监听项却解析不出进程号时同样返回错误。空结果只表示
// "查询成功但没有 listener"，不能被用来代替"无法确认"。
func ListenerPIDs(ctx context.Context, port int) ([]int, error) {
	query, err := queryPortListeners(ctx, port)
	if err != nil {
		return nil, err
	}
	return listenerPIDsOf(query)
}

// listenerPIDsOf 取出查询结果里的监听进程号，匹配到监听项却没有进程号时判为矛盾。
func listenerPIDsOf(query listenerQuery) ([]int, error) {
	if query.Matched && len(query.PIDs) == 0 {
		return nil, fmt.Errorf(
			"端口 %d 存在监听项却没有解析出监听进程：%s",
			query.Port,
			describeListenerQuery(query),
		)
	}
	return query.PIDs, nil
}

// waitPortListeners 在有界期限内轮询监听进程查询，直到查到该端口的监听进程。
//
// 就绪探测成功与监听项出现之间可能隔着极短的窗口，重试比立即失败可靠；期限用尽仍为空时
// 如实返回空结果，由调用方按"就绪探测与监听发现矛盾"处理。
func waitPortListeners(ctx context.Context, port int) (listenerQuery, error) {
	deadline := time.Now().Add(listenerDiscoveryTimeout)
	for {
		query, err := queryPortListeners(ctx, port)
		if err != nil {
			return query, err
		}
		pids, pidErr := listenerPIDsOf(query)
		if pidErr != nil {
			return query, pidErr
		}
		if len(pids) > 0 {
			return query, nil
		}
		if time.Now().After(deadline) {
			return query, nil
		}
		select {
		case <-ctx.Done():
			return query, fmt.Errorf("等待端口 %d 的监听进程时上下文已结束: %w", port, ctx.Err())
		case <-time.After(listenerDiscoveryPollInterval):
		}
	}
}

// describeListenerQuery 把查询命令与原始输出拼成诊断文本。
//
// 查询结果为空时诊断必须带命令与原始输出：否则"查询命令不可用"与"确实没有 listener"
// 在日志里无法区分。
func describeListenerQuery(query listenerQuery) string {
	output := strings.TrimSpace(query.Output)
	if output == "" {
		output = "<空>"
	}
	return fmt.Sprintf("查询命令 %s，原始输出 %s", query.Command, output)
}

// listenerPortOf 从地址或基地址里取出端口号。
//
// 基地址带 http:// 前缀，单独用 net.SplitHostPort 解析会失败，因此先去掉协议前缀。
func listenerPortOf(address string) (int, error) {
	trimmed := strings.TrimSpace(address)
	trimmed = strings.TrimPrefix(strings.TrimPrefix(trimmed, "http://"), "https://")
	_, portText, err := net.SplitHostPort(trimmed)
	if err != nil {
		return 0, fmt.Errorf("解析地址 %q 的端口失败: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0, fmt.Errorf("地址 %q 的端口 %q 不是合法端口号: %w", address, portText, err)
	}
	if port <= 0 {
		return 0, fmt.Errorf("地址 %q 的端口 %d 不合法", address, port)
	}
	return port, nil
}

// localPort 从 "地址:端口" 形式的本地地址里取出端口号，解析不出时返回 -1。
//
// ss、netstat 与 /proc/net/tcp 的地址列都可能是 IPv4、IPv6（[::]:端口）或通配（*:端口），
// 统一按最后一个冒号之后的部分解析。
func localPort(address string) int {
	index := strings.LastIndex(address, ":")
	if index < 0 || index+1 >= len(address) {
		return -1
	}
	port, err := strconv.Atoi(strings.TrimSpace(address[index+1:]))
	if err != nil {
		return -1
	}
	return port
}

// uniquePIDs 去重并升序排列进程号，保证句柄记录与诊断输出稳定。
func uniquePIDs(pids []int) []int {
	seen := make(map[int]struct{}, len(pids))
	unique := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, exists := seen[pid]; exists {
			continue
		}
		seen[pid] = struct{}{}
		unique = append(unique, pid)
	}
	sort.Ints(unique)
	return unique
}

// encodeListenerPIDs 把监听进程号写成句柄 payload 的紧凑形式。
func encodeListenerPIDs(pids []int) string {
	unique := uniquePIDs(pids)
	parts := make([]string, 0, len(unique))
	for _, pid := range unique {
		parts = append(parts, strconv.Itoa(pid))
	}
	return strings.Join(parts, ",")
}

// decodeListenerPIDs 解析句柄 payload 里的监听进程号；缺失或格式错误如实返回错误。
//
// 监听进程号是资源回收的句柄，缺失时不能用"释放时再查一次"掩盖：那时端口已经可能被
// 重新分配，无法证明回收的是本次启动的进程。
func decodeListenerPIDs(raw string) ([]int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("句柄没有记录 dev server 的监听进程号")
	}
	pids := make([]int, 0, 2)
	for _, part := range strings.Split(trimmed, ",") {
		pid, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("句柄里的监听进程号 %q 不合法", part)
		}
		pids = append(pids, pid)
	}
	return uniquePIDs(pids), nil
}

// runListenerCommand 执行一条监听查询命令，返回原始输出与错误。
//
// 合并 stderr 到输出：命令不存在或参数不被支持时，stderr 是唯一能说明查询失败的证据。
func runListenerCommand(ctx context.Context, name string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, resourceProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, name, args...).CombinedOutput()
	return string(output), err
}
