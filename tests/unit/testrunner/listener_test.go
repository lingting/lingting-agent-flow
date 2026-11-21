package testrunner_test

import (
	"runtime"
	"slices"
	"testing"

	"github.com/lingting/agentflow/internal/testrunner"
)

// listenerQueryPort 是解析样例里的监听端口。
const listenerQueryPort = 39925

// listenerOutputFixture 返回当前平台的监听查询输出样例与样例里的监听进程号。
//
// 解析实现按平台分文件，样例也必须按平台选择：不能用 Linux 的样例验证 netstat 解析，也
// 不能在测试里复制一份解析逻辑。
func listenerOutputFixture() (string, []int) {
	switch runtime.GOOS {
	case "linux":
		return `LISTEN 0      511        127.0.0.1:39925      0.0.0.0:*    users:(("node",pid=5295,fd=22))
LISTEN 0      128        127.0.0.1:41000      0.0.0.0:*    users:(("other",pid=6001,fd=3))
LISTEN 0      511        127.0.0.1:39925      0.0.0.0:*    users:(("node",pid=5294,fd=21))
LISTEN 0      511            [::1]:39925         [::]:*    users:(("node",pid=5296,fd=21))
LISTEN 0      511        127.0.0.1:39925      0.0.0.0:*    users:(("node",pid=5294,fd=21))
`, []int{5294, 5295, 5296}
	case "windows":
		return `  TCP    127.0.0.1:39925        0.0.0.0:0              LISTENING       5295
  TCP    127.0.0.1:41000        0.0.0.0:0              LISTENING       6001
  TCP    127.0.0.1:39925        0.0.0.0:0              LISTENING       5294
  TCP    127.0.0.1:39925        0.0.0.0:0              LISTENING       5296
  TCP    127.0.0.1:39925        127.0.0.1:50000        ESTABLISHED     7001
`, []int{5294, 5295, 5296}
	default:
		return "5295\n5294\n5296\n5294\n", []int{5294, 5295, 5296}
	}
}

// pidlessFixture 返回"匹配到该端口却没有进程号"的输出样例。
//
// 这对应 ss 未带 -p 权限、netstat 进程列异常与 lsof 输出格式变化：监听项确实存在，只是
// 解析不出 listener，不能当成没有 listener。
func pidlessFixture() string {
	switch runtime.GOOS {
	case "linux":
		return "LISTEN 0      511        127.0.0.1:39925      0.0.0.0:*\n"
	case "windows":
		return "  TCP    127.0.0.1:39925        0.0.0.0:0              LISTENING       unknown\n"
	default:
		return "not-a-pid\n"
	}
}

// TestParseListenerOutput 守住监听查询输出的解析边界。
func TestParseListenerOutput(t *testing.T) {
	output, expected := listenerOutputFixture()

	t.Run("解析出全部监听进程号", func(t *testing.T) {
		pids, err := testrunner.ParseListenerOutput(output, listenerQueryPort)
		if err != nil {
			t.Fatalf("解析监听查询输出失败: %v", err)
		}
		if !slices.Equal(pids, expected) {
			t.Fatalf("监听进程号 = %v，期望 %v", pids, expected)
		}
	})

	t.Run("其它端口不匹配", func(t *testing.T) {
		pids, err := testrunner.ParseListenerOutput(output, listenerQueryPort+1)
		if err != nil {
			t.Fatalf("没有该端口的监听项不应报错: %v", err)
		}
		if len(pids) != 0 {
			t.Fatalf("端口不匹配时不应解析出监听进程，实际 %v", pids)
		}
	})

	t.Run("空输出按没有监听项处理", func(t *testing.T) {
		pids, err := testrunner.ParseListenerOutput("", listenerQueryPort)
		if err != nil {
			t.Fatalf("空输出不应报错: %v", err)
		}
		if len(pids) != 0 {
			t.Fatalf("空输出不应解析出监听进程，实际 %v", pids)
		}
	})

	t.Run("匹配到监听项却没有进程号按矛盾报错", func(t *testing.T) {
		pids, err := testrunner.ParseListenerOutput(pidlessFixture(), listenerQueryPort)
		if err == nil {
			t.Fatalf("端口 %d 已匹配到监听项却解析出 %v，必须报矛盾而不是空集合", listenerQueryPort, pids)
		}
	})
}
