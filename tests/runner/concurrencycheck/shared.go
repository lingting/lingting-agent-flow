package concurrencycheck

import (
	"net"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 场景 3：运行级共享资源整次运行只分配一次。
//
// 两条用例声明同类型同名的 run-shared 端口：调度器只占用一次容量与独占键，编排进程
// 只分配一次，两条用例拿到同一句柄。两条 stdout.log 里的 address 必须一致，
// 报告 resources[] 里只出现一条 shared-port 记录。
var (
	SharedOne = sharedPortCase("concurrencycheck/shared-a")
	SharedTwo = sharedPortCase("concurrencycheck/shared-b")
)

// sharedPortCase 注册一条复用运行级共享端口的用例。
func sharedPortCase(id string) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       id,
		Parallel: true,
		Resources: []testrunner.ResourceDecl{{
			Name:  "shared-port",
			Type:  testrunner.ResourceLocalPort,
			Scope: testrunner.ScopeRunShared,
		}},
		Run: func(c *testrunner.Context) {
			handle, ok := c.Resource("shared-port")
			if !ok {
				c.Fatalf("缺少资源 shared-port 的句柄")
			}
			address := handle.PayloadValue("address")
			if address == "" {
				c.Fatalf("共享端口的句柄缺少 address")
			}
			conn, err := net.DialTimeout("tcp", address, 2*time.Second)
			if err != nil {
				c.Fatalf("共享端口 %s 不可连接: %v", address, err)
			}
			_ = conn.Close()
			c.Logf("共享端口 %s 可用", address)
			time.Sleep(holdWindow)
		},
	})
}
