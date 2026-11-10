// Package legacy 登记 R0 冻结清单里明确不再迁移的旧用例。
//
// 覆盖对账以清单为分母：条目消失但没在这里登记理由，`--audit-coverage` 会把它报成缺失并
// 退出非零。这里只登记"用例确实不适用"的条目，理由必须可核对。
package legacy

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

func init() {
	// R0 冻结清单采集完成后，本机 Pi 测试按 AGENTS.md 的硬性红线移除（commit 9515a72：
	// 禁止在测试中执行或探测本机 pi，Agent 相关验收改成远程任务容器内的真实 Pi）。这两条
	// 是当时用本机 pi 直接做能力探测的用例，没有可迁移的等价断言，因此不再重写。
	testrunner.DeclareDropped(
		"go:tests/integration/pi/real_pi_test.go:TestRealPiCheck",
		"本机 Pi 测试已按 AGENTS.md 红线移除（commit 9515a72），用例断言只对本机 pi 成立",
	)
	testrunner.DeclareDropped(
		"go:tests/integration/pi/real_pi_test.go:TestRealPiModelRecognition",
		"本机 Pi 测试已按 AGENTS.md 红线移除（commit 9515a72），模型清单探测改由容器内真实 Pi 承担",
	)
	// 同一次移除还删掉了 e2e/cli 里依赖"Pi 不可用"这一前提的用例：它只验证本机缺少 pi 时的
	// 错误文案，与容器内真实 Pi 的验收口径无关，也没有等价的迁移目标。
	testrunner.DeclareDropped(
		"go:tests/e2e/cli/cli_test.go:TestAgentCheckUnknownExecutable",
		"依赖本机 pi 缺失前提（commit 9515a72 随本机 Pi 测试一并移除），容器内真实 Pi 的验收不覆盖该断言",
	)
}
