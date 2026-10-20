// Package app_test 提供应用服务层的单元测试。
//
// 该文件只提供测试二进制替身入口与共享辅助函数：受控的子进程行为（例如"忽略
// stdin 关闭并长期挂起"）需要测试二进制本身充当 Pi，才能在没有真实 Pi CLI 的
// 机器上验证取消与超时是否真的传播到进程。
package app_test

import (
	"context"
	"os"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/tests/internal/fakepi"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestMain 让测试二进制同时充当假 Pi 子进程。
func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakepi.EnvScenario); scenario != "" {
		os.Exit(fakepi.Run())
	}
	os.Exit(m.Run())
}

// fakePiExecutable 返回可作为 Pi 使用的测试二进制路径。
func fakePiExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("获取测试可执行文件失败: %v", err)
	}
	return executable
}

// fakePiEnv 返回让测试二进制按指定场景运行的子进程环境。
func fakePiEnv(t *testing.T, scenario string) []string {
	t.Helper()
	report := testkit.New(t).Path("fake-pi-report.json")
	return append(
		os.Environ(),
		fakepi.EnvScenario+"="+scenario,
		fakepi.EnvReport+"="+report,
	)
}

// 编译期确认统一 Runner 满足后台服务契约。
var _ interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
} = (*app.AgentRunnerService)(nil)
