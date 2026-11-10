package resourcecheck

import "github.com/lingting/agentflow/internal/testrunner"

// MissingSpecCase 验证契约必填键缺失时必须在分配阶段失败。
//
// 声明只给 host_id、不给 image：分配前的契约校验直接失败，因此不会尝试连接远程主机。
var MissingSpecCase = testrunner.Register(testrunner.Case{
	ID: "resourcecheck-fault/missing-spec",
	Resources: []testrunner.ResourceDecl{{
		Name:  "container",
		Type:  testrunner.ResourceRemoteContainer,
		Scope: testrunner.ScopeCaseExclusive,
		Spec:  map[string]string{"host_id": "resourcecheck-unconfigured"},
	}},
	Run: func(c *testrunner.Context) {
		c.Fatalf("缺少 image 的声明不应被分配到资源")
	},
})

// UnimplementedCase 验证前端三类资源在 R2 分配阶段给出明确错误。
var UnimplementedCase = testrunner.Register(testrunner.Case{
	ID: "resourcecheck-fault/unimplemented",
	Resources: []testrunner.ResourceDecl{{
		Name: "browser", Type: testrunner.ResourceBrowser, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		c.Fatalf("browser 资源在 R2 不应被分配")
	},
})

// ReleaseFailureCase 用于验证零残留终检本身。
//
// 正常运行时用例主体通过；配合 --selfcheck-fail-release local-port 时，端口释放必定失败，
// 终检应判 failed、退出码非零并保留全部产物。
var ReleaseFailureCase = testrunner.Register(testrunner.Case{
	ID: "resourcecheck-fault/release-failed",
	Resources: []testrunner.ResourceDecl{{
		Name: "port", Type: testrunner.ResourceLocalPort, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		handle, ok := c.Resource("port")
		if !ok {
			c.Fatalf("缺少资源 port 的句柄")
		}
		if handle.PayloadValue("address") == "" {
			c.Fatalf("句柄缺少 address")
		}
		c.Logf("用例主体正常结束；释放失败由 --selfcheck-fail-release 注入")
	},
})
