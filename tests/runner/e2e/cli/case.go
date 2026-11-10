package cli

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "e2e-cli"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/e2e/cli"
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
//
// 只要用例把配置交给会打开数据库的命令，配置里的数据库路径就必须来自这条句柄：注入的路径
// 位于本次运行的资源目录下，释放时由提供者删除并自证不存在。
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
