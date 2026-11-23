// 本文件登记 tests/unit/testrunner 的跨平台用例，顺序与源文件一致，便于按包核对迁移数量。
//
// listener_fallback_linux_test.go 对应的两条用例只在 Linux 构建里出现，登记写在
// cases_listener_linux.go：跨平台文件引用不到 linux-only 的函数。
package testrunner

var (
	// listener_test.go

	_ = register("parse-listener-output", runParseListenerOutput)
)
