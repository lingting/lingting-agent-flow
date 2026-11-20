// Package workflow 是 V3 R5 批次 B3 的用例包：把 tests/integration/remote/workflow 的 7 条
// 真实远程端到端与项目级验收用例迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/runner/integration/local/scheduler/doc.go）：
// 用例主体命名为 func run<原函数名去掉 Test 前缀>，一个既有 func Test* 对应一条 Case。
//
// 真实依赖：用例需要 tests/config/ssh.yml 的真实 SSH 目标、tests/config/docker.yml 的
// docker.remote_enabled 与 docker.pi、tests/config/git.yml 的可访问仓库与
// tests/config/pi.yml 的 pi.enable。未启用或配置缺失时按缺少配置跳过；授权由编排进程
// 判定一次并下发，用例不再读取 CI 环境变量。
//
// 资源：R2 原判定本包为「独享 + 整包串行」，R7 D6 复核后解除整包串行——每条用例的
// 远端工作空间（带随机后缀）、数据库、容器名与分支都按用例隔离，设备级容器核验也
// 只把 testkit 前缀的容器当作测试资源。任务容器仍由用例登记清理责任，用例结束时逐条
// 删除并按真实远程 docker ps -a 清单核验零残留。用例不声明远程资源，偏差与依据见
// .steps/V3/R5/README.md 的 B3 决策 D9。
package workflow

import (
	"fmt"
	"os"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-remote-workflow"
	// packagePath 是本包用例的来源 go test 包，也是整包串行判定的输入。
	packagePath = "tests/integration/remote/workflow"
	// databaseName 是用例内数据库资源的声明名。
	databaseName = "db"
)

// register 登记一条用例。
//
// 端到端链路包含两次真实镜像构建、容器内 Git 与 Pi 调用，执行时间远超默认上限，
// 因此显式给出 Timeout。R7 D6 复核后本包不再整包串行：远端工作空间、数据库、
// 容器名与分支都按用例隔离，用例声明自并行，并发上限仍只由 --parallel 决定。
func register(name string, timeout time.Duration, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:        caseGroup + "/" + name,
		Package:   packagePath,
		Timeout:   timeout,
		Parallel:  true,
		Resources: databaseDecl(),
		Run:       run,
	})
}

// registerWithoutDB 登记一条不使用数据库的用例。
//
// 前置检查类用例只读取配置与版本信息，不打开 SQLite，因此不声明数据库资源。
func registerWithoutDB(name string, timeout time.Duration, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       caseGroup + "/" + name,
		Package:  packagePath,
		Timeout:  timeout,
		Parallel: true,
		Run:      run,
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

// workflowCaseTimeout 是本包用例的执行上限。
const workflowCaseTimeout = 90 * time.Minute

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
