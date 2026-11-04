// Package planningagent_test 覆盖规划 Agent 服务的生命周期准入、停止收敛与跨进程租约。
//
// 用例只验证已实现的业务行为：准入边界、取消传播、停止等待，以及"同一规划会话只有一个
// 执行者"的持久化租约（含续期、失效取消与收敛等待）。规划角色调用的业务校验、容器准备
// 与真实 Pi 执行不在这里重复验证。
//
// 规划运行时的释放保护同样在这里验证：`PlanningRuntimeService.Release` 与 `RunRole` /
// `Prepare` 竞争同一份数据库租约，冲突时不得进入任何远程删除或绑定清理。
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
