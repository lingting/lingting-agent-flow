// Package codesaver 是 V3 R5 批次 B3 的用例包：把 tests/integration/remote/codesaver 的
// 2 条真实远程代码保存用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）：
// 用例主体命名为 func run<原函数名去掉 Test 前缀>，一个既有 func Test* 对应一条 Case。
//
// 真实依赖：用例需要 tests/config/ssh.yml 的真实 SSH 目标与 tests/config/docker.yml 的
// docker.remote_enabled / docker.git_image；未启用或配置缺失时按缺少配置跳过。授权由
// 编排进程判定一次并下发，用例不再读取 CI 环境变量。
//
// 资源：R2 判定本包为「独享」。远程容器由用例创建并登记清理责任，用例结束时逐条删除、
// 按真实远程 docker ps -a 清单核验零残留；用例不声明远程资源，偏差与依据见
// .steps/V3/R5/README.md 的 B3 决策 D9。
package codesaver

import (
	"fmt"
	"os"

	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-remote-codesaver"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/remote/codesaver"
)

// register 登记一条用例。
//
// 两条用例都会在真实远程主机上创建容器并改写仓库，按 R2 的独享结论保持串行：
// Parallel 为 false 表示调度器在串行作用域内独占派发它。
func register(name string, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:      caseGroup + "/" + name,
		Package: packagePath,
		Run:     run,
	})
}

// requireRealTests 按编排进程下发的授权结论决定是否跳过。
func requireRealTests(t *testrunner.Context, suite string) {
	t.Helper()
	allowed, reason := t.RealTestsAllowed()
	if allowed {
		return
	}
	message := fmt.Sprintf("WARN [SKIP] 真实测试未获授权：%s；%s", suite, reason)
	fmt.Fprintln(os.Stdout, message)
	t.Skip(message)
}
