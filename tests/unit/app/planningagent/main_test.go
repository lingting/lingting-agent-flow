// Package planningagent_test 覆盖规划 Agent 服务的生命周期准入与停止收敛。
//
// 用例只验证本阶段实现的业务行为：准入边界、取消传播与停止等待。规划角色调用的业务
// 校验、容器准备与真实 Pi 执行不在这里重复验证。
package planningagent_test

import (
	"os"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestMain 把本次运行的完整测试输出写入本次运行根目录下的 test.log。
func TestMain(m *testing.M) {
	os.Exit(testkit.Main(m))
}
