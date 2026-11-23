// 本文件登记 tests/unit/lifecycle 的 6 条用例，顺序与源文件 manager_test.go 一致，
// 便于按包核对迁移数量。
//
// 用例主体在 cases_manager.go 里；本包用例只使用受控服务替身，不占用编排进程资源。
package lifecycle

var (
	_ = register("stop-runs-in-reverse-start-order", runStopRunsInReverseStartOrder)
	_ = register("start-failure-stops-only-started-services-in-reverse", runStartFailureStopsOnlyStartedServicesInReverse)
	_ = register("stop-is-idempotent-and-reports-failures", runStopIsIdempotentAndReportsFailures)
	_ = register("stop-timeout-is-bounded", runStopTimeoutIsBounded)
	_ = register("runtime-fault-is-forwarded", runRuntimeFaultIsForwarded)
	_ = register("register-rejects-invalid-services", runRegisterRejectsInvalidServices)
)
