package scheduler

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是试点的执行集合名。
	caseGroup = "integration-local-scheduler"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	//
	// 迁移后用例文件不在该目录下，但 Package 沿用来源包路径：R2 冻结的包级串行表与
	// R0 清单都按来源路径组织，R5 迁移时不必改判定口径。
	packagePath = "tests/integration/local/scheduler"
	// databaseName 是用例内数据库资源的声明名。
	databaseName = "db"
)

// register 登记一条用例。
//
// 试点范围内的用例全部对应原 t.Parallel()：并发属性由 Case.Parallel 声明，具体并发
// 由调度器决定，用例无法通过代码自行扩大并发。
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
// 文件由编排进程的 sqlite-db 提供者创建并写入报告，用例只消费句柄里的路径；释放与
// 自证由提供者承担，因此"零残留"终检能覆盖到它。
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
