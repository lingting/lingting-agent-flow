// Package ssh 是 V3 R5 批次 B3 的用例包：把 tests/integration/ssh 的 6 条真实 SSH 用例
// 迁到统一测试执行器。
//
// 迁移写法沿用 R4.5 试点冻结的约定（tests/integration/local/scheduler/doc.go）：
// 用例主体命名为 func run<原函数名去掉 Test 前缀>，一个既有 func Test* 对应一条 Case。
//
// 真实依赖：用例连接 tests/config/ssh.yml 配置的真实远程机器，主机密钥校验不可关闭，
// 未启用或配置缺失时按缺少配置跳过，不建立任何远程连接。
//
// 资源：R2 判定本包为「共享 ssh-host + 独享 ssh-session」。迁移后用例自己用 testkit
// 建立连接（配置与 known_hosts 快照都在用例侧），不声明远程资源，因此编排进程不额外
// 持有一份连接；偏差与依据见 V3 R5 的 B3 决策 D9。
package ssh

import (
	"fmt"
	"os"

	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// caseGroup 是本包用例的分组名，也是批次验收的执行集合名。
	caseGroup = "integration-ssh"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/integration/ssh"
)

// register 登记一条用例。
//
// 真实 SSH 用例都会发起远程连接，因此按来源 go test 的写法保持并行属性：调度器决定
// 并发度，用例自身不再声明并发。
func register(name string, run func(*testrunner.Context)) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       caseGroup + "/" + name,
		Package:  packagePath,
		Parallel: true,
		Run:      run,
	})
}

// requireRealTests 按编排进程下发的授权结论决定是否跳过。
//
// 授权只在编排进程判定一次（V3 R1 §6），用例不再读取 CI 环境变量，避免测试侧出现
// 第二套门禁。未授权时如实记为 SKIP，不计入通过。
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
