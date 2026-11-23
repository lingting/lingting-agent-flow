// Package container 是 V4 R7 的 unit 迁移包：把 tests/unit/container 的既有单元用例迁到
// 统一测试执行器。
//
// 迁移写法沿用 R5 冻结的约定（tests/unit/execenv/case.go）：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为 "unit-container/<原用例名转 kebab-case>"；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，断言与语句不需要改写；
//   - t.Parallel() 不再出现在主体里，并发属性由 Case.Parallel 声明，并发最终由调度器决定；
//   - TestMain 删除：进程级日志、结论与运行根目录由执行器承担，本包不再有 _test.go。
//
// 本包用例只断言容器记录与 Docker 命令构造的纯规则：命令以参数切片形式核对，不执行
// docker、不连接外部进程，因此不声明 Resources，也不声明 DedicatedWorker。
package container

import "github.com/lingting/agentflow/internal/testrunner"

const (
	// caseGroup 是本包用例的分组名，也是本包验收的执行集合名。
	caseGroup = "unit-container"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/unit/container"
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
