// Package environment 是 V3 R5 批次 B2 的用例包：把 tests/integration/local/environment
// 的既有集成用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）。
// 用例用把远程路径映射到本机目录的文件传输替身代替真实 SFTP，因此不依赖真实远程机器、
// SSH 主机或 Docker Engine；准入校验用例覆盖"未通过预检的节点不得建立远程会话"。
//
// 资源：每条用数据库的用例独占一份 sqlite-db（路径只从注入句柄读取），不声明端口或
// 远程资源——用例自建的本机监听与临时目录都由用例自己承担。
package environment

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-local-environment"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/local/environment"
	// databaseName 是用例内数据库资源的声明名。
	databaseName = "db"
)

// register 登记一条用例。
//
// 用例主体命名为 func run<原函数名去掉 Test 前缀>，Register 据此推导来源测试函数名，
// --audit-coverage 用它对账 R0 冻结清单。
func register(
	name string,
	run func(*testrunner.Context),
	resources ...testrunner.ResourceDecl,
) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:        caseGroup + "/" + name,
		Package:   packagePath,
		Parallel:  true,
		Resources: resources,
		Run:       run,
	})
}

// databaseDecl 声明本用例独占的 SQLite 数据库文件。
func databaseDecl() []testrunner.ResourceDecl {
	return []testrunner.ResourceDecl{{
		Name:  databaseName,
		Type:  testrunner.ResourceSQLiteDB,
		Scope: testrunner.ScopeCaseExclusive,
	}}
}

// databasePath 返回注入的数据库文件路径。
func databasePath(t *testrunner.Context) string {
	handle, ok := t.Resource(databaseName)
	if !ok {
		t.Fatalf("缺少资源 %s 的句柄", databaseName)
	}
	return handle.PayloadValue("path")
}
