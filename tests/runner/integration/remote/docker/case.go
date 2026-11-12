// Package docker 是 V3 R5 批次 B3 的用例包：把 tests/integration/remote/docker 的 6 条
// 真实远程 Docker 用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）：
// 用例主体命名为 func run<原函数名去掉 Test 前缀>，一个既有 func Test* 对应一条 Case。
//
// 真实依赖：用例需要 tests/config/ssh.yml 的真实 SSH 目标与 tests/config/docker.yml 的
// docker.remote_enabled；未启用或配置缺失时按缺少配置跳过。授权由编排进程判定一次并
// 下发，用例不再读取 CI 环境变量。
//
// 资源：R2 判定本包为「独享」。远程镜像、容器与目录都带随机后缀，容器由用例创建并登记
// 清理责任，用例结束时逐条删除并按真实远程 docker ps -a 清单核验零残留；用数据库的
// 用例独占一份 sqlite-db。用例不声明远程资源，偏差与依据见 .steps/V3/R5/README.md 的
// B3 决策 D9。
//
// 并发：本包用例一律串行（Parallel 为 false，见 D12）。用例级终检读的是整台机器的容器
// 清单，只要有别的用例同时在创建测试容器，就会被判成"未登记容器"。原包级 TestMain 终检在
// 全部用例结束后才跑，不会遇到这种并发；统一执行器里一条用例就是一个 worker 进程，
// 因此只能靠包内串行保持同一语义。
package docker

import (
	"fmt"
	"os"

	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-remote-docker"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/remote/docker"
	// databaseName 是用例内数据库资源的声明名。
	databaseName = "db"
)

// register 登记一条用例。
func register(
	name string,
	run func(*testrunner.Context),
	resources ...testrunner.ResourceDecl,
) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:        caseGroup + "/" + name,
		Package:   packagePath,
		Parallel:  false,
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

// requireRealTests 按编排进程下发的授权结论决定是否跳过。
func requireRealTests(t *testrunner.Context, suite string) {
	t.Helper()
	allowed, reason := t.RealTestsAllowed()
	if allowed {
		return
	}
	message := fmt.Sprintf("WARN [SKIP] 真实测试未获授权：%s；%s", suite, reason)
	fmt.Fprintln(os.Stdout, message)
	t.Skip(message)
}
