package testkit

// Tester 是测试辅助函数需要的最小用例上下文能力。
//
// *testing.T 与统一测试执行器的 *testrunner.Context 都满足它，因此同一套辅助组件可以
// 被 go test 用例与新执行器用例共用，不需要为两侧各写一份实现。
type Tester interface {
	// Helper 把调用它的函数标记为辅助函数，失败定位会跳过该函数。
	Helper()
	// Logf 写一行不改变结论的日志。
	Logf(format string, args ...any)
	// Errorf 记录一次失败，用例继续执行后续断言。
	Errorf(format string, args ...any)
	// Fatalf 记录一次失败并立即结束用例主体。
	Fatalf(format string, args ...any)
	// Cleanup 注册用例结束时执行的清理函数，按注册顺序的相反顺序执行。
	Cleanup(fn func())
	// Skip 记录跳过原因并立即结束用例主体。
	Skip(args ...any)
}
