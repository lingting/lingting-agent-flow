package release_test

import (
	"os"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestMain 把本次运行的完整测试输出写入本次运行根目录下的 test.log。
func TestMain(m *testing.M) {
	os.Exit(testkit.Main(m))
}
