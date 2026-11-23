package selfcheck

import (
	"net"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// ResourcesCase 验证资源链路：声明共享与独享资源，确认句柄注入后端口真实可连。
var ResourcesCase = testrunner.Register(testrunner.Case{
	ID: "selfcheck/resources",
	Resources: []testrunner.ResourceDecl{
		{Name: "shared-port", Type: testrunner.ResourceLocalPort, Scope: testrunner.ScopeRunShared},
		{Name: "own-port", Type: testrunner.ResourceLocalPort, Scope: testrunner.ScopeCaseExclusive},
	},
	Run: func(c *testrunner.Context) {
		for _, name := range []string{"shared-port", "own-port"} {
			handle, ok := c.Resource(name)
			if !ok {
				c.Fatalf("缺少资源 %q 的句柄", name)
			}
			address := handle.PayloadValue("address")
			if address == "" {
				c.Fatalf("资源 %q 的句柄缺少 address", name)
			}
			dialer := net.Dialer{Timeout: 2 * time.Second}
			conn, err := dialer.DialContext(c.Context(), "tcp", address)
			if err != nil {
				c.Errorf("资源 %q 的端口 %s 不可连接: %v", name, address, err)
				continue
			}
			_ = conn.Close()
			c.Logf("资源 %q 可用：%s", name, address)
		}
	},
})
