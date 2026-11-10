package concurrencycheck

import (
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 场景 4：包级串行声明。
//
// 前两条用例都声明了 R2 冻结的整包串行包 tests/integration/remote/workflow：即使它们
// 允许并行，也必须依次执行。第三条用例声明普通包 tests/integration/local/scheduler，
// 可以与它们重叠。报告里前两条的区间不重叠，且与第三条的区间重叠。
//
// 这里刻意使用真实的包路径：包级串行声明只能来自用例声明与 R2 的冻结表，调度器不允许
// 内置针对具体包的例外。
const (
	serialPackage     = "tests/integration/remote/workflow"
	concurrentPackage = "tests/integration/local/scheduler"
)

var (
	SerialWorkflowOne = serialPackageCase("concurrencycheck/serial-a", serialPackage)
	SerialWorkflowTwo = serialPackageCase("concurrencycheck/serial-b", serialPackage)
	SerialFreePackage = serialPackageCase("concurrencycheck/serial-c", concurrentPackage)
)

// serialPackageCase 注册一条声明所属 Go 测试包的占用用例。
func serialPackageCase(id, pkg string) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       id,
		Package:  pkg,
		Parallel: true,
		Run: func(c *testrunner.Context) {
			c.Logf("用例 %s 声明的包是 %s", c.Name(), pkg)
			time.Sleep(holdWindow)
		},
	})
}
