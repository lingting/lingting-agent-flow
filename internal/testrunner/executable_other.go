//go:build !windows

package testrunner

// spawnableExecutable 直接返回执行器本身：非 Windows 平台没有"无扩展名时补 .exe"的启动语义，
// 无扩展名的二进制与带扩展名的二进制行为一致。
func spawnableExecutable(executable, _ string) (string, func(), error) {
	return executable, func() {}, nil
}
