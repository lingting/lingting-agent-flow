//go:build linux

// 本文件登记 tests/unit/testrunner 里 Linux-only 的用例。
//
// 包括 /proc 监听回退、真实 ss 查询与 /proc 进程状态读取期间的退出竞态；
// 跨平台文件不能引用这些用例主体。
package testrunner

var (
	_ = register("listener-pids-with-ss", runListenerPIDsWithSS)
	_ = register("listener-pids-fallback", runListenerPIDsFallback)
	_ = register("process-gone-error", runProcessGoneError)
)
