package concurrencycheck

import (
	"os"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 场景 2：同一独占键互斥。
//
// 两条用例都声明 git-worktree 且都不给 repo（默认编排进程的工作目录），因此独占键
// "git-worktree|repo=<工作目录>" 相同：调度器必须让它们依次获取，报告里后一条的排队
// 时长大于 0，两条用例的 [worker 启动, 结束] 区间不重叠。
var (
	ExclusiveOne = holdWorktreeCase("concurrencycheck/exclusive-a")
	ExclusiveTwo = holdWorktreeCase("concurrencycheck/exclusive-b")
)

// holdWorktreeCase 注册一条声明 Git 工作区的用例：工作区可用后保持占用窗口。
func holdWorktreeCase(id string) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       id,
		Parallel: true,
		Timeout:  2 * time.Minute,
		Resources: []testrunner.ResourceDecl{{
			Name:  "tree",
			Type:  testrunner.ResourceGitWorktree,
			Scope: testrunner.ScopeCaseExclusive,
		}},
		Run: func(c *testrunner.Context) {
			handle, ok := c.Resource("tree")
			if !ok {
				c.Fatalf("缺少资源 tree 的句柄")
			}
			path := handle.PayloadValue("path")
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				c.Fatalf("工作区 %s 不可用: %v", path, err)
			}
			c.Logf("工作区 %s 就位", path)
			time.Sleep(holdWindow)
		},
	})
}
