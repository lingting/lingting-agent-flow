package agent_remote

import (
	"os"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// containerRun 是本次测试进程创建的远程测试容器登记表。
//
// 所有测试容器都在创建之前登记清理责任；测试包结束时会按真实远程 docker ps -a
// 清单核验本次运行没有留下任何测试容器。
var containerRun = testkit.NewContainerRun("remote/agent")

// TestMain 把本次运行的完整测试输出写入本次运行根目录下的 test.log。
//
// 统一入口保证逐用例结论、WARN [SKIP] 跳过原因与 slog 日志都能事后复核；
// 最终核验在全部测试结束后、日志刷新之前执行，核验失败会让进程以非零码退出。
func TestMain(m *testing.M) {
	testkit.RegisterContainerFinalizer(containerRun)
	os.Exit(testkit.Main(m))
}
