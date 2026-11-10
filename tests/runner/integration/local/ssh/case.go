// Package ssh 是 V3 R5 批次 B1 的用例包：把 tests/integration/local/ssh 的既有集成用例
// 迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）。
// 用例在进程内启动真实 SSH/SFTP 服务端（真实握手、认证、主机密钥校验与真实 SFTP 传输），
// 不依赖外部服务或网络下载。
//
// 资源：R2 判定本包为"共享（本地 SSH 服务端独享）"，用例自己起服务端并让操作系统分配
// 临时端口，因此不需要编排进程预先分配端口或 ssh-server 资源。
package ssh

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-local-ssh"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/local/ssh"
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
