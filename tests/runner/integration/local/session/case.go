// Package session 是 V3 R5 批次 B2 的用例包：把 tests/integration/local/session 的既有
// 集成用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）。
// 用例把容器、远程宿主机与本机文件系统映射到各自的工作空间，因此三段式传输、字节校验、
// 原子提交与失败保留都在真实文件系统上验证，不依赖 SSH 主机或 Docker Engine。
//
// 资源：本包不声明资源——三条路径都在用例自己的工作空间里，Session 目录与临时目录由
// 用例自己创建，没有编排进程需要预先分配的资源。
package session

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-local-session"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/local/session"
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
