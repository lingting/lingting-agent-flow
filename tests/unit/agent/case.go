// Package agent 是 V4 R7 的 unit 迁移包：把 tests/unit/agent 的既有单元用例迁到统一测试
// 执行器。
//
// 迁移写法沿用 R5 冻结的约定（tests/unit/execenv/case.go）：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为 "unit-agent/<原用例名转 kebab-case>"；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，断言与语句不需要改写；
//   - t.Parallel() 不再出现在主体里，并发属性由 Case.Parallel 声明，并发最终由调度器决定；
//   - TestMain 删除：进程级日志、结论与运行根目录由执行器承担，本包不再有 _test.go。
//
// 本包用例用受控替身替代 Pi 与容器执行进程：命令构造、会话归档、结果分类与收尾终止都走真实
// 代码路径，但不启动 docker、不连接 SSH、不运行本机 Pi。用例自带临时目录与替身状态，没有包级
// 可变状态，因此不声明 Resources，也不声明 DedicatedWorker。
package agent

import "github.com/lingting/agentflow/internal/testrunner"

const (
	// caseGroup 是本包用例的分组名，也是本包验收的执行集合名。
	caseGroup = "unit-agent"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/unit/agent"
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
