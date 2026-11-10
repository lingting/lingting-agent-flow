// Package selfcheck 提供统一测试执行器的最小自检用例（V3 R1）。
//
// 这些用例只验证执行器自身的语义：通过、跳过、资源注入、失败、panic 与超时。它们不触碰
// 产品代码、真实 Pi / SSH / Docker 设备，也不进入 R5/R6 的迁移面。
package selfcheck

import "github.com/lingting/agentflow/internal/testrunner"

// PassCase 是最小的通过用例：写日志、建临时目录并正常返回。
var PassCase = testrunner.Register(testrunner.Case{
	ID: "selfcheck/pass",
	Run: func(c *testrunner.Context) {
		c.Logf("用例 %s 开始", c.Name())
		if _, err := c.TempDir(); err != nil {
			c.Fatalf("创建临时目录失败: %v", err)
		}
		allowed, reason := c.RealTestsAllowed()
		c.Logf("真实依赖授权：%t（%s）", allowed, reason)
	},
})
