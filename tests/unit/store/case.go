// Package sqlite 是 V4 R8 的 unit 迁移包：把 tests/unit/store 的既有仓储用例迁到统一测试
// 执行器。
//
// 迁移写法沿用 R5 冻结的约定（tests/unit/execenv/case.go）：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为 "unit-store/<原用例名转 kebab-case>"；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，断言与语句不需要改写；
//   - t.Parallel() 不再出现在主体里，并发属性由 Case.Parallel 声明，并发最终由调度器决定；
//   - TestMain 删除：进程级日志、结论与运行根目录由执行器承担，本包不再有 _test.go。
//
// 命名：本包是被测包 internal/storage/sqlite 的用例包，包名与被测包相同（Go 允许，正文里的
// sqlite.X 专指 internal/storage/sqlite）；目录名仍是 store，Package 也填来源路径。
//
// 隔离：每条用例在 testkit.New(t) 的独占目录里打开自己的数据库文件，连接由 t.Cleanup 关闭，
// 数据库文件与 WAL 随产物保留；不共享数据库句柄、不用固定端口、不启动外部进程，包级可变
// 状态只有 requirementDeleteAt 这样的固定时间值。因此不声明 Resources，也不声明
// DedicatedWorker。
package sqlite

import "github.com/lingting/agentflow/internal/testrunner"

const (
	// caseGroup 是本包用例的分组名，也是本包验收的执行集合名。
	caseGroup = "unit-store"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/unit/store"
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
