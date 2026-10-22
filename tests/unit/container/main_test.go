package container_test

import (
	"os"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestMain 把本次运行的完整测试输出写入本次运行根目录下的 test.log。
//
// 统一入口保证逐用例结论、WARN [SKIP] 跳过原因与 slog 日志都能事后复核，
// 不需要重跑测试来确认现场。
func TestMain(m *testing.M) {
	os.Exit(testkit.Main(m))
}
