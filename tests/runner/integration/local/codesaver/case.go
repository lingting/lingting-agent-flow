// Package codesaver 是 V3 R5 批次 B1 的用例包：把 tests/integration/local/codesaver 的
// 既有集成用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）。
// 用例在用例工作空间内创建真实 Git 工作仓库与 bare 仓库，并把"容器内命令"映射到本机
// 执行：提交、推送、远程引用核验与合并都经过真实 Git，因此不需要 SSH、Docker 或外网。
//
// 本包不声明资源：仓库与执行替身都是用例自己的现场，缺少 git 时如实用例跳过。
package codesaver

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-local-codesaver"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/local/codesaver"
)

// register 登记一条用例。
//
// 用例主体命名为 func run<原函数名去掉 Test 前缀>，Register 据此推导来源测试函数名，
// --audit-coverage 用它对账 R0 冻结清单。
func register(name string, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       caseGroup + "/" + name,
		Package:  packagePath,
		Parallel: true,
		Run:      run,
	})
}
