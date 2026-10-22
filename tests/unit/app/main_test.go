// Package app_test 提供应用服务层的单元测试。
//
// 本版本所有 Agent 调用都在远程容器内执行，应用层不再启动本机 Pi 子进程，
// 因此这里只需要一个普通的测试入口。
package app_test

import (
	"os"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestMain 把本次运行的完整测试输出写入本次运行根目录下的 test.log。
//
// 本版本不再需要测试二进制充当假 Pi：容器内 Agent 调用由受控替身提供，
// 应用层测试不启动任何本机子进程。
func TestMain(m *testing.M) {
	os.Exit(testkit.Main(m))
}
