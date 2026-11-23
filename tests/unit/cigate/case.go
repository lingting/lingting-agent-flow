// Package cigate 是 V4 R6 的 unit 迁移包：把 tests/unit/cigate 的既有单元用例迁到统一
// 测试执行器。
//
// 迁移写法沿用 R5 冻结的约定（tests/unit/execenv/case.go）：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为 "unit-cigate/<原用例名转 kebab-case>"；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，断言与语句不需要改写；
//   - t.Run 子测试保留（Context.Run 是单层顺序子测试），整体结论仍属于该 Case；
//     子测试里的 t.Parallel() 删除：并发只由调度器按 Case.Parallel 决定；
//   - TestMain 删除：进程级日志、结论与运行根目录由执行器承担，本包不再有 _test.go。
//
// 命名：本包是被测包 internal/cigate 的用例包，包名与被测包相同（Go 允许，正文里的
// cigate.X 专指 internal/cigate）。
//
// 用例只做纯逻辑判定与受控替身，不起外部进程、不占用编排资源，因此不声明 Resources，
// 也不声明 DedicatedWorker。
package cigate

import "github.com/lingting/agentflow/internal/testrunner"

const (
	// caseGroup 是本包用例的分组名，也是本包验收的执行集合名。
	caseGroup = "unit-cigate"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/unit/cigate"
)

// register 登记一条可并行用例，对应源文件里带 t.Parallel() 的用例。
func register(name string, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       caseGroup + "/" + name,
		Package:  packagePath,
		Parallel: true,
		Run:      run,
	})
}
