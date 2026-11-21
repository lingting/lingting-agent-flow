// Package planning 是 V3 R5 批次 B3 的用例包：把 tests/integration/remote/planning 的 9 条
// 真实规划运行时验收用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）：
// 用例主体命名为 func run<原函数名去掉 Test 前缀>，一个既有 func Test* 对应一条 Case。
//
// 真实依赖：用例需要 tests/config/ssh.yml 的真实 SSH 目标、tests/config/docker.yml 的
// docker.remote_enabled 与完整的 docker.pi（容器内 Pi 覆盖配置）、tests/config/git.yml 的
// 可访问仓库。未启用或配置缺失时按缺少配置跳过。授权由编排进程判定一次并下发，用例不再
// 读取 CI 环境变量。
//
// 资源：R2 判定本包为「独享」。每条用例独占一份 sqlite-db（路径只从注入句柄读取）；
// 规划容器与快照卷由被测服务创建，用例侧逐条登记清理责任，用例结束时删除并按真实远程
// 清单核验零残留。用例不声明远程资源，偏差与依据见 V3 R5 的 B3 决策 D9。
package planning

import (
	"fmt"
	"os"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-remote-planning"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/remote/planning"
	// databaseName 是用例内数据库资源的声明名。
	databaseName = "db"
)

// register 登记一条用例。
//
// 规划验收包含真实镜像构建、真实容器内 Pi 调用与多次服务重建，执行时间远超默认上限，
// 因此显式给出 Timeout；同时按 R2 的独享结论保持串行派发。
func register(name string, timeout time.Duration, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:        caseGroup + "/" + name,
		Package:   packagePath,
		Timeout:   timeout,
		Resources: databaseDecl(),
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
