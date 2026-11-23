// Package server 是 V4 R6 的 unit 迁移包：把 tests/unit/server 的既有单元用例迁到统一测试
// 执行器。
//
// 迁移写法沿用 R5 冻结的约定（tests/unit/execenv/case.go）：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为 "unit-server/<原用例名转 kebab-case>"；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，断言与语句不需要改写；
//   - t.Parallel() 不再出现在主体里，并发属性由 Case.Parallel 声明，并发最终由调度器决定；
//   - t.Chdir 没有对应 API（ROADMAP §9.2）：server-lock-path-matches-relative-path 在用例内
//     用 chdir 辅助切换并在用例结束时切回，因此按串行条目登记；
//   - TestMain 删除：进程级日志、结论与运行根目录由执行器承担，本包不再有 _test.go。
//
// 命名：本包是被测包 internal/server 的用例包，包名与被测包相同（Go 允许，正文里的
// server.X 专指 internal/server）。
//
// 隔离：用例既不开固定端口，也不共享目录——HTTP 监听一律用 127.0.0.1:0 由系统分配端口，
// 数据库、锁文件与符号链接夹具都建在 testkit.Dir(t) 的独占目录里；唯一改动的进程级状态是
// 工作目录，chdir 会切回，worker 在用例之间还会按 W3 重置。因此本包不声明 DedicatedWorker。
package server

import "github.com/lingting/agentflow/internal/testrunner"

const (
	// caseGroup 是本包用例的分组名，也是本包验收的执行集合名。
	caseGroup = "unit-server"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/unit/server"
)

// register 登记一条可并行用例，对应源文件里带 t.Parallel() 的用例。
func register(name string, run func(*testrunner.Context)) testrunner.Case {
	return registerCase(name, run, true)
}

// registerSerial 登记一条串行用例，对应源文件里没有 t.Parallel() 的用例。
func registerSerial(name string, run func(*testrunner.Context)) testrunner.Case {
	return registerCase(name, run, false)
}

// registerCase 是 register 与 registerSerial 的共同实现。
func registerCase(name string, run func(*testrunner.Context), parallel bool) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       caseGroup + "/" + name,
		Package:  packagePath,
		Parallel: parallel,
		Run:      run,
	})
}
