// Package app 是 V4 R8 的 unit 迁移包：把 tests/unit/app 的既有应用服务层用例迁到统一测试
// 执行器。
//
// 迁移写法沿用 R5 冻结的约定（tests/unit/execenv/case.go）：
//
//   - 一条既有 func Test* 对应一条 Case，ID 为 "unit-app/<原用例名转 kebab-case>"；
//   - 用例主体写成 func run*(t *testrunner.Context)，参数名沿用 t，断言与语句不需要改写；
//   - t.Parallel() 不再出现在主体里（含 t.Run 子测试），并发属性由 Case.Parallel 声明；
//   - t.Run 子测试保留为 Context.Run，结论仍属于该 Case；
//   - TestMain 删除：进程级日志、结论与运行根目录由执行器承担，本包不再有 _test.go。
//
// 命名：本包是被测包 internal/app 的用例包，包名与被测包相同（Go 允许，正文里的 app.X 专指
// internal/app）。
//
// 隔离：所有资源都建在用例独占目录里——数据库由 testkit.New(t) 打开、连接由 t.Cleanup 关闭，
// 远程节点、Docker 与 Git 一律走受控替身，不启动真实外部进程；没有 os.Chdir、t.Setenv、
// t.TempDir；包级可变状态为空，sync.Once 只出现在用例内构造的替身类型上。需要真实监听的用例
// 用 127.0.0.1:0 由系统分配端口，不占固定端口。因此不声明 Resources，也不声明
// DedicatedWorker。
package app

import "github.com/lingting/agentflow/internal/testrunner"

const (
	// caseGroup 是本包用例的分组名，也是本包验收的执行集合名。
	caseGroup = "unit-app"
	// packagePath 是本包用例的来源 go test 包，也是串行作用域的判定输入。
	packagePath = "tests/unit/app"
)

// register 登记一条可并行用例，对应源文件里带 t.Parallel() 的用例。
func register(name string, run func(*testrunner.Context)) testrunner.Case {
	return registerCase(name, run, true)
}

// registerSerial 登记一条串行用例，对应源文件里没有 t.Parallel() 的用例。
func registerSerial(name string, run func(*testrunner.Context)) testrunner.Case {
	return registerCase(name, run, false)
}

// registerCase 是 register 与 registerSerial 的共同实现。
func registerCase(name string, run func(*testrunner.Context), parallel bool) testrunner.Case {
	return testrunner.Register(testrunner.Case{
		ID:       caseGroup + "/" + name,
		Package:  packagePath,
		Parallel: parallel,
		Run:      run,
	})
}
