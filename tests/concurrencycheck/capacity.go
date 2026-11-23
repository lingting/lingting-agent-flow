package concurrencycheck

import (
	"os"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 场景 5：资源容量上限。
//
// sqlite-db 的容量在 R3 收紧为 4：6 条用例各声明一个独享数据库并占用相同窗口时，任何
// 时刻最多 4 条在飞，其余排队而不是失败。--parallel 8 时报告里的 max_in_flight 必须
// 不超过 4、排队时长大于 0，结论全部通过。
var (
	CapacityOne   = capacityCase("concurrencycheck/capacity-1")
	CapacityTwo   = capacityCase("concurrencycheck/capacity-2")
	CapacityThree = capacityCase("concurrencycheck/capacity-3")
	CapacityFour  = capacityCase("concurrencycheck/capacity-4")
	CapacityFive  = capacityCase("concurrencycheck/capacity-5")
	CapacitySix   = capacityCase("concurrencycheck/capacity-6")
)

// capacityCase 注册一条声明独享 SQLite 数据库的占用用例。
func capacityCase(id string) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       id,
		Parallel: true,
		Resources: []testrunner.ResourceDecl{{
			Name:  "db",
			Type:  testrunner.ResourceSQLiteDB,
			Scope: testrunner.ScopeCaseExclusive,
		}},
		Run: func(c *testrunner.Context) {
			handle, ok := c.Resource("db")
			if !ok {
				c.Fatalf("缺少资源 db 的句柄")
			}
			path := handle.PayloadValue("path")
			info, err := os.Stat(path)
			if err != nil || info.Size() == 0 {
				c.Fatalf("数据库文件 %s 不可用: %v", path, err)
			}
			c.Logf("数据库 %s 就位", path)
			time.Sleep(holdWindow)
		},
	})
}
