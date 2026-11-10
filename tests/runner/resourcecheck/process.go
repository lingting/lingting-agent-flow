package resourcecheck

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// ChildProcessCase 验证 child-process 提供者：拿到的是真实存活于编排进程中的子进程。
var ChildProcessCase = testrunner.Register(testrunner.Case{
	ID: "resourcecheck/child-process",
	Resources: []testrunner.ResourceDecl{{
		Name: "child", Type: testrunner.ResourceChildProcess, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		handle, ok := c.Resource("child")
		if !ok {
			c.Fatalf("缺少资源 child 的句柄")
		}
		pid, err := strconv.Atoi(handle.PayloadValue("pid"))
		if err != nil || pid <= 0 {
			c.Fatalf("句柄的 pid %q 不是合法进程号", handle.PayloadValue("pid"))
		}
		executable := handle.PayloadValue("executable")
		info, err := os.Stat(executable)
		if err != nil || info.IsDir() {
			c.Fatalf("子进程可执行文件 %s 不可用: %v", executable, err)
		}
		c.Logf("子进程资源可用：pid=%d executable=%s", pid, executable)
	},
})

// ServiceProcessCase 验证 service-process 提供者：服务进程的 HTTP 端点真实可用。
var ServiceProcessCase = testrunner.Register(testrunner.Case{
	ID:      "resourcecheck/service-process",
	Timeout: 90 * time.Second,
	Resources: []testrunner.ResourceDecl{{
		Name: "service", Type: testrunner.ResourceServiceProcess, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		handle, ok := c.Resource("service")
		if !ok {
			c.Fatalf("缺少资源 service 的句柄")
		}
		if _, err := strconv.Atoi(handle.PayloadValue("pid")); err != nil {
			c.Fatalf("句柄的 pid %q 不是合法进程号", handle.PayloadValue("pid"))
		}
		if info, err := os.Stat(handle.PayloadValue("binary")); err != nil || info.IsDir() {
			c.Fatalf("服务可执行文件 %s 不可用: %v", handle.PayloadValue("binary"), err)
		}
		baseURL := handle.PayloadValue("base_url")
		client := &http.Client{Timeout: 10 * time.Second}
		request, err := http.NewRequestWithContext(c.Context(), http.MethodGet, baseURL, nil)
		if err != nil {
			c.Fatalf("构造请求失败: %v", err)
		}
		response, err := client.Do(request)
		if err != nil {
			c.Fatalf("请求 %s 失败: %v", baseURL, err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			c.Fatalf("%s 返回状态码 %d", baseURL, response.StatusCode)
		}
		c.Logf("服务进程可用：%s（pid %s）", baseURL, handle.PayloadValue("pid"))
	},
})
