package concurrencycheck

import (
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// holdWindow 是一条用例的占用窗口：用例在这段时间里保持"在跑"，让调度器有机会
// 同时派发其他用例。它不是时序断言，报告里的峰值在飞数才是判定依据。
const holdWindow = 250 * time.Millisecond

// 场景 1：无资源用例并行。
//
// 4 条用例都不声明资源、都允许并行、占用窗口相同。--parallel 4 时报告里的
// max_in_flight 必须达到 4，总耗时明显小于串行累加。
var (
	ParallelOne   = holdParallelCase("concurrencycheck/parallel-a")
	ParallelTwo   = holdParallelCase("concurrencycheck/parallel-b")
	ParallelThree = holdParallelCase("concurrencycheck/parallel-c")
	ParallelFour  = holdParallelCase("concurrencycheck/parallel-d")
)

// holdParallelCase 注册一条无资源、允许并行的占用用例。
func holdParallelCase(id string) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       id,
		Parallel: true,
		Run: func(c *testrunner.Context) {
			c.Logf("用例 %s 占用中", c.Name())
			time.Sleep(holdWindow)
		},
	})
}
