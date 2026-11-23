package selfcheck

import "github.com/lingting/agentflow/internal/testrunner"

// SkipCase 验证跳过路径：跳过不等于通过，必须带上原因。
var SkipCase = testrunner.Register(testrunner.Case{
	ID: "selfcheck/skip",
	Run: func(c *testrunner.Context) {
		c.Skipf("R1 自检：跳过路径，不执行任何外部操作")
	},
})
