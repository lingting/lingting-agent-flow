// Package execenv 是 V3 R5 批次 B2 的用例包：把 tests/integration/local/execenv 的既有
// 集成用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）。
// 用例的镜像、容器与 SSH 执行全部是受控替身：不访问真实 Docker Engine、SSH 主机或远程
// 机器，因此容器创建、核验、释放与命令执行的边界都能在任意开发机上验证。
//
// 资源：本包不声明编排进程资源——替身不产生真实容器、镜像或远程目录，工作空间由用例
// 自己使用，没有需要分配与自证的资源。
package execenv

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-local-execenv"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/local/execenv"
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
