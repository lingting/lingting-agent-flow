// Package testrunner 是 V4 R6 的 unit 迁移包：把 tests/unit/testrunner 的既有单元用例迁到
// 统一测试执行器。
//
// 迁移写法沿用 R5 冻结的约定（tests/unit/execenv/case.go）：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为 "unit-testrunner/<原用例名转 kebab-case>"；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，断言与语句不需要改写；
//   - t.Run 子测试保留（Context.Run 是单层顺序子测试），整体结论仍属于该 Case；
//   - TestMain 删除：进程级日志、结论与运行根目录由执行器承担，本包不再有 _test.go。
//
// 命名：本包是被测包 internal/testrunner 的用例包，包名与被测包相同（Go 允许，正文里的
// testrunner.X 专指 internal/testrunner），与 tests/unit/testkit 的共享辅助边界无关。
//
// 隔离：源用例里有两条会改写进程级状态——listener-pids-fallback 覆盖 PATH、
// listener-pids-with-ss 依赖真实 ss。两者都在用例内自行恢复，且 worker 在用例之间按 W3
// 重置环境变量与工作目录，因此不声明 DedicatedWorker。
package testrunner

import "github.com/lingting/agentflow/internal/testrunner"

const (
	// caseGroup 是本包用例的分组名，也是本包验收的执行集合名。
	caseGroup = "unit-testrunner"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/unit/testrunner"
)

// register 登记一条用例。
//
// 源用例都没有 t.Parallel()：它们读取真实进程的监听表、或改写 PATH 后再调用同一套查询，
// 因此全部按串行条目登记，在串行作用域内独占执行。
func register(name string, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       caseGroup + "/" + name,
		Package:  packagePath,
		Parallel: false,
		Run:      run,
	})
}
