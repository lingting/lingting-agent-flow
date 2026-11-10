package concurrencycheck

import (
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 场景 6：用例超时。
//
// 超时用例由 worker 自己的 deadline 判 timeout；同一批里的伴随用例必须照常完成。
var TimeoutCase = testrunner.Register(testrunner.Case{
	ID:       "concurrencycheck-fault/timeout",
	Parallel: true,
	Timeout:  300 * time.Millisecond,
	Run: func(c *testrunner.Context) {
		c.Logf("用例 %s 故意运行到超时", c.Name())
		time.Sleep(30 * time.Second)
	},
})

// TimeoutCompanion 是超时场景的伴随用例。
var TimeoutCompanion = testrunner.Register(testrunner.Case{
	ID:       "concurrencycheck-fault/timeout-companion",
	Parallel: true,
	Run: func(c *testrunner.Context) {
		c.Logf("用例 %s 在超时场景中照常完成", c.Name())
	},
})

// 场景 7：panic 隔离。panic 只影响自身，运行继续并给出非零退出码。
var PanicCase = testrunner.Register(testrunner.Case{
	ID:       "concurrencycheck-fault/panic",
	Parallel: true,
	Run: func(c *testrunner.Context) {
		c.Logf("用例 %s 即将 panic", c.Name())
		panic("R3 自检：故意 panic")
	},
})

// PanicCompanion 是 panic 场景的伴随用例。
var PanicCompanion = testrunner.Register(testrunner.Case{
	ID:       "concurrencycheck-fault/panic-companion",
	Parallel: true,
	Run: func(c *testrunner.Context) {
		c.Logf("用例 %s 在 panic 场景中照常完成", c.Name())
	},
})

// 场景 8：取消。
//
// cancel-a / cancel-b 声明独享端口后一直占用，直到编排进程终止它们的进程树；
// cancel-c 只有在前面用例结束后才会被派发，因此取消后必须保持"未派发"。
var (
	CancelOne   = blockingCase("concurrencycheck-fault/cancel-a")
	CancelTwo   = blockingCase("concurrencycheck-fault/cancel-b")
	CancelThree = testrunner.Register(testrunner.Case{
		ID:       "concurrencycheck-fault/cancel-c",
		Parallel: true,
		Run: func(c *testrunner.Context) {
			c.Fatalf("用例 %s 在取消后不应被派发", c.Name())
		},
	})
)

// blockingCase 注册一条占用资源并一直阻塞的用例：只有进程树终止才能结束它。
func blockingCase(id string) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       id,
		Parallel: true,
		// 取消场景必须带 --selfcheck-cancel-on 才能在一秒内结束；误跑整组时最多等一分钟。
		Timeout: time.Minute,
		Resources: []testrunner.ResourceDecl{{
			Name:  "port",
			Type:  testrunner.ResourceLocalPort,
			Scope: testrunner.ScopeCaseExclusive,
		}},
		Run: func(c *testrunner.Context) {
			handle, ok := c.Resource("port")
			if !ok {
				c.Fatalf("缺少资源 port 的句柄")
			}
			if handle.PayloadValue("address") == "" {
				c.Fatalf("句柄缺少 address")
			}
			c.Logf("用例 %s 持有端口直到被取消", c.Name())
			for {
				time.Sleep(100 * time.Millisecond)
			}
		},
	})
}

// 场景 9：迟到结果。
//
// 用例超时后结论已定型，编排进程再用 --selfcheck-late-result 写出一份声称通过的
// result.json：结论必须保持 timeout，差异只出现在用例警告里。
var LateResultCase = testrunner.Register(testrunner.Case{
	ID:       "concurrencycheck-fault/late-result",
	Parallel: true,
	Timeout:  300 * time.Millisecond,
	Run: func(c *testrunner.Context) {
		c.Logf("用例 %s 故意超时，用于验证迟到结果无法改写结论", c.Name())
		time.Sleep(30 * time.Second)
	},
})
