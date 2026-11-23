// Package lifecycle 是 V4 R5 的 unit 迁移试点包：把 tests/unit/lifecycle 的既有单元
// 用例迁到统一测试执行器。
//
// 迁移写法沿用 V3 R4.5 冻结的约定（tests/integration/local/scheduler/doc.go），并按
// .steps/V4/R5/README.md §2 收敛：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为 "unit-lifecycle/<原用例名转 kebab-case>"，
//     顺序与源文件一致，便于按包核对迁移数量；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，断言与语句不需要改写；
//   - t.Parallel() 不再出现在主体里，并发属性由 Case.Parallel 声明，并发最终由调度器决定；
//   - TestMain 删除：进程级日志、结论与逐用例产物由执行器承担，本包不再有 _test.go。
//
// 用例只用受控服务替身，不启动真实后台服务，因此不声明编排进程资源。
package lifecycle

import "github.com/lingting/agentflow/internal/testrunner"

const (
	// caseGroup 是本包用例的分组名，也是试点验收的执行集合名。
	caseGroup = "unit-lifecycle"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/unit/lifecycle"
)

// register 登记一条用例。
//
// 用例主体沿用来源测试函数的命名（func run<原函数名去掉 Test 前缀>），便于与迁移前的
// go test 用例逐条对照；命名不参与注册校验，Package 只服务串行作用域与报告定位。
func register(name string, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       caseGroup + "/" + name,
		Package:  packagePath,
		Parallel: true,
		Run:      run,
	})
}
