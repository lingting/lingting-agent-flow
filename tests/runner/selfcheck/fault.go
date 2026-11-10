package selfcheck

import (
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// FailCase 故意失败，用于验证失败结论与非零退出码。
var FailCase = testrunner.Register(testrunner.Case{
	ID: "selfcheck-fault/fail",
	Run: func(c *testrunner.Context) {
		c.Errorf("R1 自检：这是故意失败")
	},
})

// PanicCase 故意 panic，验证 panic 只影响 worker 进程且被如实记录。
var PanicCase = testrunner.Register(testrunner.Case{
	ID: "selfcheck-fault/panic",
	Run: func(c *testrunner.Context) {
		c.Logf("用例 %s 即将 panic", c.Name())
		panic("R1 自检：故意 panic")
	},
})

// TimeoutCase 故意超长运行，超时上限由用例自身声明。
var TimeoutCase = testrunner.Register(testrunner.Case{
	ID:      "selfcheck-fault/timeout",
	Timeout: time.Second,
	Run: func(c *testrunner.Context) {
		c.Logf("用例 %s 故意运行到超时", c.Name())
		time.Sleep(30 * time.Second)
	},
})
