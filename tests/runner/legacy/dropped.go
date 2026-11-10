// Package legacy 登记 R0 冻结清单里明确不再迁移的旧用例。
//
// 覆盖对账以清单为分母：条目消失但没在这里登记理由，`--audit-coverage` 会把它报成缺失并
// 退出非零。这里只登记"用例确实不适用"的条目，理由必须可核对。
package legacy

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

// fetcherReason 是本机 Git 假容器集成测试整包移除的理由。
//
// tests/integration/local/fetcher 把容器命令映射到宿主机目录、用本机 Git 执行 clone 与分支创建，
// 既不能证明远程容器内 Git 可用，也不能证明容器内的真实执行结果。Git 获取代码的验收改由远程
// 容器用例承担（TestRemoteSourceFetch、TestRemoteE2EFullChain）。
const fetcherReason = "用例用宿主机目录与本机 Git 模拟容器，不验证远程容器内真实 Git 与文件系统"

// hostPiReason 是宿主侧 Pi 单元用例移除的理由：两者只覆盖宿主侧的路径与配置字符串拼接，
// 不验证远程容器内固定版本 Pi 的实际 Session 布局与配置解析。
const hostPiReason = "用例只覆盖宿主侧 Pi 路径与配置拼接，不验证容器内固定版本 Pi 的实际行为"

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
	// 本机 Git 假容器集成测试整包移除：容器执行只允许发生在远程任务容器内，宿主机目录加
	// 本机 Git 的用例不能作为产品运行环境的验收依据，Git 获取代码改由远程容器用例验收。
	for _, name := range []string{
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherAuthFailureIsExplicit",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherBaseBranchDetection",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherCancelledBeforeStart",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherCloneFailureKeepsStage",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherCloneSucceeds",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherGitUnavailable",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherKindIsGit",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherRejectsCredentialURL",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherRejectsInvalidTaskID",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherRejectsNonEmptyTarget",
		"go:tests/integration/local/fetcher/fetcher_test.go:TestFetcherRejectsUnknownSourceType",
		"go:tests/integration/local/fetcher/task_workspace_test.go:TestTaskWorkspaceRefusesNonEmptyWorkDir",
		"go:tests/integration/local/fetcher/task_workspace_test.go:TestTaskWorkspaceSkipsSecondRun",
		"go:tests/integration/local/fetcher/task_workspace_test.go:TestTaskWorkspaceUsesSharedFetcher",
	} {
		testrunner.DeclareDropped(name, fetcherReason)
	}
	// 宿主侧 Pi 单元用例同步移除：Session 布局与模型标识改由容器内真实 Pi 的远程用例验收。
	for _, name := range []string{
		"go:tests/unit/agent/pi/session_path_test.go:TestSessionLayoutDirectory",
		"go:tests/unit/agent/pi/session_path_test.go:TestSessionLayoutPathsAreAbsolute",
		"go:tests/unit/agent/pi/session_path_test.go:TestSessionLayoutRequiresSessionID",
		"go:tests/unit/agent/pi/session_path_test.go:TestSessionLayoutSanitizesSegments",
		"go:tests/unit/testkit/pi_config_test.go:TestPiConfigResolvedModel",
	} {
		testrunner.DeclareDropped(name, hostPiReason)
	}
}
