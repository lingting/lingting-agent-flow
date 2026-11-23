// Package testkit 是 V4 R6 的 unit 迁移包：把 tests/unit/testkit 的既有单元用例迁到统一
// 测试执行器。
//
// 迁移写法沿用 R5 冻结的约定（tests/unit/execenv/case.go）：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为 "unit-testkit/<原用例名转 kebab-case>"；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，断言与语句不需要改写；
//   - t.Run 子测试保留（Context.Run 是单层顺序子测试），整体结论仍属于该 Case；
//   - TestMain 删除：进程级日志、结论与运行根目录由执行器承担，本包不再有 _test.go。
//
// 命名边界（R6 §2）：本包 tests/unit/testkit 是一个 unit 用例包，只承载统一执行器登记的
// Case；共享辅助仍在 tests/testkit。两者同名不同职责，不得合并，也不得让本包重新长出具名
// 辅助：正文里的 sharedkit 前缀专指 tests/testkit。
package testkit

import "github.com/lingting/agentflow/internal/testrunner"

const (
	// caseGroup 是本包用例的分组名，也是本包验收的执行集合名。
	caseGroup = "unit-testkit"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/unit/testkit"
)

// register 登记一条用例。
//
// 源用例没有 t.Parallel()，因此全部按串行条目登记：串行作用域内独占执行，与 go test 里
// 未标记 t.Parallel 的用例语义一致；实际并发仍由调度器决定。
func register(name string, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       caseGroup + "/" + name,
		Package:  packagePath,
		Parallel: false,
		Run:      run,
	})
}
