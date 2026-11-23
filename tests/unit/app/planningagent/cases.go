// 本文件登记 tests/unit/app/planningagent 的 9 条用例，顺序与源文件 cases_lifecycle.go
// 一致，便于按包核对迁移数量。
//
// 源文件 cases_fixture.go 没有用例，只有测试现场与替身，因此不登记。9 条源用例都带
// t.Parallel()，全部按可并行条目登记。
package planningagent

var (
	_ = register(
		"planning-agent-service-rejects-call-before-start",
		runPlanningAgentServiceRejectsCallBeforeStart,
	)
	_ = register(
		"planning-agent-service-runs-role-after-start",
		runPlanningAgentServiceRunsRoleAfterStart,
	)
	_ = register(
		"planning-agent-service-rejects-call-after-stop",
		runPlanningAgentServiceRejectsCallAfterStop,
	)
	_ = register(
		"planning-agent-service-stop-cancels-inflight-call",
		runPlanningAgentServiceStopCancelsInflightCall,
	)
	_ = register(
		"planning-agent-service-stop-waits-for-convergence",
		runPlanningAgentServiceStopWaitsForConvergence,
	)
	_ = register(
		"planning-agent-service-stop-waits-for-multiple-calls",
		runPlanningAgentServiceStopWaitsForMultipleCalls,
	)
	_ = register(
		"planning-agent-service-admission-stops-at-boundary",
		runPlanningAgentServiceAdmissionStopsAtBoundary,
	)
	_ = register(
		"planning-agent-service-concurrent-stop-is-safe",
		runPlanningAgentServiceConcurrentStopIsSafe,
	)
	_ = register(
		"planning-agent-service-stop-reports-timeout",
		runPlanningAgentServiceStopReportsTimeout,
	)
)
