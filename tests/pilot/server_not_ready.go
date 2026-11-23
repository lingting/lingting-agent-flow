package pilot

import (
	"strconv"
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 未就绪故障用例的时间预算：就绪探测只等一小会儿，失败原因必须是"没有就绪"。
const (
	// notReadyProbeTimeout 是故意探测不存在路径时的就绪等待上限。
	notReadyProbeTimeout = 8 * time.Second
	// notReadyCaseTimeout 是故障用例的执行上限。
	notReadyCaseTimeout = 120 * time.Second
)

// ServerNotReadyCase 是就绪判定的反证用例：服务起来了但端点是 404，用例必须在分配阶段失败。
//
// 它验证 R4 §7.2「服务未就绪时用例失败且失败原因明确」：提供者不得用"进程还活着"冒充"服务可用"，
// 也不得让用例主体带着未就绪的服务继续执行。
var ServerNotReadyCase = testrunner.Register(testrunner.Case{
	ID:       "pilot-fault/server-not-ready",
	Timeout:  notReadyCaseTimeout,
	Parallel: false,
	Resources: []testrunner.ResourceDecl{{
		Name:  "service",
		Type:  testrunner.ResourceServiceProcess,
		Scope: testrunner.ScopeCaseExclusive,
		Spec: map[string]string{
			"binary": serverBinaryPath(),
			"args":   serverArguments(),
			// 这个路径由服务返回 404，就绪探测必须一直失败到超时。
			"ready_path":       "/api/v1/pilot-not-ready",
			"ready_timeout_ms": strconv.Itoa(int(notReadyProbeTimeout.Milliseconds())),
		},
	}},
	Run: func(c *testrunner.Context) {
		c.Fatalf("服务未就绪时用例主体不应被执行")
	},
})
