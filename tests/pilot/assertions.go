package pilot

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// healthProbeTimeout 是用例侧健康检查的等待上限。
const healthProbeTimeout = 10 * time.Second

// assertIsServerBinary 确认句柄指向真正的被测服务，而不是自检用的替身服务。
//
// 声明里给了 binary，提供者因此不会退回自检服务；这里再从用例侧核对一次，避免配置漂移后
// 用例仍然"通过"。
func assertIsServerBinary(c *testrunner.Context, handle testrunner.Handle) {
	expected := filepath.Base(serverBinaryPath())
	actual := filepath.Base(handle.PayloadValue("binary"))
	if actual != expected {
		c.Fatalf("句柄的 binary 是 %q，期望被测服务 %q", actual, expected)
	}
	info, err := os.Stat(handle.PayloadValue("binary"))
	if err != nil || info.IsDir() {
		c.Fatalf("被测服务可执行文件 %s 不可用: %v", handle.PayloadValue("binary"), err)
	}
}

// assertHealthy 用健康检查确认服务真的可用。
//
// 就绪判定由提供者完成，用例侧只做复核：响应必须是健康检查 JSON，自检服务的固定文本不算通过。
func assertHealthy(c *testrunner.Context, baseURL string) {
	client := &http.Client{Timeout: healthProbeTimeout}
	request, err := http.NewRequestWithContext(c.Context(), http.MethodGet, baseURL, nil)
	if err != nil {
		c.Fatalf("构造健康检查请求失败: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		c.Fatalf("健康检查 %s 失败: %v", baseURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		c.Fatalf("读取健康检查响应失败: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		c.Fatalf("%s 返回状态码 %d: %s", baseURL, response.StatusCode, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(string(body), `"status"`) {
		c.Fatalf("%s 的响应不是健康检查 JSON，被测进程不是真实服务: %s", baseURL, strings.TrimSpace(string(body)))
	}
}

// serviceAddress 从健康检查地址里取出服务监听的 host:port。
func serviceAddress(c *testrunner.Context, baseURL string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		c.Fatalf("句柄的 base_url %q 不是合法 URL: %v", baseURL, err)
	}
	if parsed.Host == "" {
		c.Fatalf("句柄的 base_url %q 缺少主机与端口", baseURL)
	}
	return parsed.Host
}

// assertProcessGone 确认进程真的消失。
//
// 它与 waitExitCode 的返回值相互独立：退出码只说明 Wait 拿到了一个状态，进程是否还在系统里
// 要单独核对。
func assertProcessGone(c *testrunner.Context, pid int) {
	alive, err := testrunner.ProcessAlive(pid)
	if err != nil {
		c.Fatalf("无法确认服务进程 %d 是否存活: %v", pid, err)
	}
	if alive {
		c.Fatalf("服务进程 %d 在退出后仍然存活", pid)
	}
}

// assertPortRebindable 确认端口在服务退出后可以重新绑定。
func assertPortRebindable(c *testrunner.Context, address string) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		c.Fatalf("服务退出后无法重新绑定 %s: %v", address, err)
	}
	if err := listener.Close(); err != nil {
		c.Fatalf("关闭重新绑定的监听失败: %v", err)
	}
	c.Logf("端口 %s 在服务退出后可以重新绑定", address)
}

// assertGracefulShutdownLogged 确认服务自己写下了优雅关闭标记。
//
// 标记是"服务收到中断并走完关闭流程"的直接证据：被强制终止的进程不会写出这一行。
// 服务进程的输出由提供者写入 <用例工作空间>/process 目录。
func assertGracefulShutdownLogged(c *testrunner.Context, workspace string) {
	pattern := filepath.Join(workspace, serviceLogDir, "*.log")
	logs, err := filepath.Glob(pattern)
	if err != nil {
		c.Fatalf("列举服务进程输出文件失败: %v", err)
	}
	if len(logs) == 0 {
		c.Fatalf("没有找到服务进程输出文件（%s）", pattern)
	}
	sort.Strings(logs)
	logPath := logs[len(logs)-1]
	content, err := os.ReadFile(logPath)
	if err != nil {
		c.Fatalf("读取服务进程输出失败: %s: %v", logPath, err)
	}
	if !strings.Contains(string(content), gracefulShutdownMarker) {
		c.Fatalf(
			"服务进程输出里没有优雅关闭标记 %q，本次不是优雅关闭（输出见 %s）",
			gracefulShutdownMarker,
			logPath,
		)
	}
	c.Logf("服务进程输出包含优雅关闭标记：%s", logPath)
}
