//go:build linux

// 本文件登记 tests/unit/testrunner 里 linux-only 的 2 条用例。
//
// 源文件 cases_listener_fallback_linux.go 带 //go:build linux：只有 Linux 上才有 /proc
// 回退与真实 ss 查询，跨平台的 cases.go 不能引用这两个用例主体。
package testrunner

var (
	_ = register("listener-pids-with-ss", runListenerPIDsWithSS)
	_ = register("listener-pids-fallback", runListenerPIDsFallback)
)
