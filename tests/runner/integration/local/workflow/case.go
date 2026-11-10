// Package workflow 是 V3 R5 批次 B2 的用例包：把 tests/integration/local/workflow 的既有
// 集成用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）。
// 用例的容器、Agent、检查与代码保存全部是受控替身，因此启动链、修复循环、取消收敛、
// 重启恢复与闭环一致性都能在本机验证，不依赖真实 Docker Engine、SSH 主机或 Pi。
//
// 资源：每条用例独占一份 sqlite-db（路径只从注入句柄读取）；表驱动的多场景用例按场景
// 份数各声明一份，场景之间不共用数据库文件。本包被 R2 判定为 exclusive，
// 但结论来自"用例关闭服务"这一批内互斥语义，而不是编排进程需要预先分配的共享资源：
// 服务在用例进程内构造与关闭，容器与远程目录都是替身，没有残留可以留给终检。
//
// 子测试：表驱动写法里的 t.Run 在统一执行器中没有独立结论（R5 D6），由 testrunner.Context
// 的 Run 就地按顺序执行，子测试名写入日志，结论仍属于整条 Case。
package workflow

import (
	"fmt"

	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-local-workflow"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/local/workflow"
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
// 用例的"重启"路径需要关闭连接后按同一路径重新打开，因此路径必须来自提供者：
// 用例自己拼路径会让释放与自证脱离终检。
func databaseDecl() []testrunner.ResourceDecl {
	return databaseDeclCount(1)
}

// databaseDeclCount 声明 count 份独占的 SQLite 数据库文件。
//
// 表驱动的多场景用例要为每个场景准备互相独立的现场：共用一个数据库文件会让场景之间
// 的数据互相污染（同一节点、容器与任务的唯一约束会被前一个场景占住）。份数不得超过
// sqlite-db 的容量 4，否则调度器会把这条用例判为无法调度。
func databaseDeclCount(count int) []testrunner.ResourceDecl {
	decls := make([]testrunner.ResourceDecl, 0, count)
	for index := 1; index <= count; index++ {
		decls = append(decls, testrunner.ResourceDecl{
			Name:  sceneDatabaseName(index),
			Type:  testrunner.ResourceSQLiteDB,
			Scope: testrunner.ScopeCaseExclusive,
		})
	}
	return decls
}

// databasePath 返回注入的数据库文件路径（单场景用例）。
func databasePath(t *testrunner.Context) string {
	return databasePathAt(t, 1)
}

// databasePathAt 返回第 scene 个场景的数据库文件路径（场景序号从 1 开始）。
func databasePathAt(t *testrunner.Context, scene int) string {
	name := sceneDatabaseName(scene)
	handle, ok := t.Resource(name)
	if !ok {
		t.Fatalf("缺少资源 %s 的句柄", name)
	}
	return handle.PayloadValue("path")
}

// sceneDatabaseName 返回第 scene 个场景的数据库资源名。
func sceneDatabaseName(scene int) string {
	if scene <= 1 {
		return databaseName
	}
	return fmt.Sprintf("%s%d", databaseName, scene)
}
