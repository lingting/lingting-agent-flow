// Package source 是 V3 R5 批次 B1 的用例包：把 tests/integration/local/source 的既有
// 集成用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）：
// 一条既有 func Test* 对应一条 Case，主体写成 func run*，t.Parallel() 换成 Case.Parallel，
// Case.Package 沿用来源包路径，资源只声明真正使用的类型。
//
// 本包不使用真实容器：执行替身把"容器内命令"映射到用例工作空间，真实 Git 在本地完成
// Clone 与分支操作，因此没有资源声明；缺少 git 时如实用例跳过，不静默通过。
package source

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-local-source"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/local/source"
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
