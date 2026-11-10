package legacy

import (
	"github.com/lingting/agentflow/internal/testrunner"
)

// gitReason 是 workflow 真实 Git 用例被移除的统一理由。
//
// 这些用例把本机 git 可执行文件当作真实依赖：在用例工作空间里建 bare 仓库并执行
// clone / fetch / push。Windows 下用例工作空间的路径由运行 ID 与用例 ID 拼成，再拼上
// `remote.git/objects/tmp_objdir-incoming-*/xx/<38 位哈希>` 会超过 260 字符上限，Git 写入
// 对象文件失败（receive-pack 报 "Filename too long"，core.longpaths 对子进程写入不生效）。
// 按确认移除这批用例，不改写断言、不换成替身：真实 Git 链路的验收不再放在本机集成层。
const gitReason = "用例依赖本机真实 Git 执行 clone/push，Windows 下工作空间路径超过 MAX_PATH 无法承载，按确认移除"

func init() {
	// R5 B2 批次：tests/integration/local/workflow 的 e2e_* 与 repair_budget_* 用例全部
	// 依赖本机真实 Git，随批次迁移时按上面的理由整体移除。
	for _, name := range []string{
		"go:tests/integration/local/workflow/e2e_indeterminate_test.go:TestE2EIndeterminateCodeSaveRecordUnconfirmed",
		"go:tests/integration/local/workflow/e2e_indeterminate_test.go:TestE2EIndeterminateCodeSavePushUnknown",
		"go:tests/integration/local/workflow/e2e_indeterminate_test.go:TestE2EIndeterminateCloseoutWriteFails",
		"go:tests/integration/local/workflow/e2e_indeterminate_test.go:TestE2EIndeterminateCodeSaveError",
		"go:tests/integration/local/workflow/e2e_indeterminate_test.go:TestE2EIndeterminateWithoutCodeSaver",
		"go:tests/integration/local/workflow/e2e_indeterminate_test.go:TestE2EIndeterminateDoesNotResumeWithoutVerification",
		"go:tests/integration/local/workflow/e2e_repair_limit_test.go:TestE2ERepairLimitStopsWithoutDelivery",
		"go:tests/integration/local/workflow/e2e_repair_limit_test.go:TestE2ERepairLimitKeepsFailedHistory",
		"go:tests/integration/local/workflow/e2e_repair_limit_test.go:TestE2ERepairDisabledFailsWithoutRetry",
		"go:tests/integration/local/workflow/e2e_repair_limit_test.go:TestE2ERepairLimitPreservesContainer",
		"go:tests/integration/local/workflow/e2e_repair_limit_test.go:TestE2ERepairLimitSurvivesRestart",
		"go:tests/integration/local/workflow/e2e_repair_test.go:TestE2ERepairThenReverifyReachesCompleted",
		"go:tests/integration/local/workflow/e2e_repair_test.go:TestE2ERepairKeepsFailedRoundEvidence",
		"go:tests/integration/local/workflow/e2e_repair_test.go:TestE2ERepairFeedbackLinkedToFailedRun",
		"go:tests/integration/local/workflow/e2e_repair_test.go:TestE2ERepairPromptCarriesFeedback",
		"go:tests/integration/local/workflow/e2e_review_test.go:TestE2EValidatorReviewFailureBlocksCompletion",
		"go:tests/integration/local/workflow/e2e_review_test.go:TestE2EValidatorReviewFailureFeedsRepairLoop",
		"go:tests/integration/local/workflow/e2e_success_test.go:TestE2EHappyPathReachesCompleted",
		"go:tests/integration/local/workflow/e2e_success_test.go:TestE2EHappyPathPersistsPlanAndWorkspace",
		"go:tests/integration/local/workflow/e2e_success_test.go:TestE2EHappyPathPersistsExecutionRecords",
		"go:tests/integration/local/workflow/e2e_success_test.go:TestE2EHappyPathSavesCodeToRemote",
		"go:tests/integration/local/workflow/e2e_success_test.go:TestE2EHappyPathLeavesAuditEvents",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestRepairBudgetPersistsAcrossRestart",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestRepairBudgetExhaustedAfterRestartRejectsExecution",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestRepairBudgetRepeatedCallDoesNotResetBudget",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestOpenRepairCycleGrantsFreshBudget",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestOpenRepairCycleRejectsWhenBudgetRemains",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestOpenRepairCycleRejectsUnresolvedAttempt",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestRepairStopsWhenFeedbackWriteFails",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestRepairStopsWhenFeedbackReadFails",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestRepairIgnoresPreviousCycleFeedback",
		"go:tests/integration/local/workflow/repair_budget_test.go:TestOpenRepairCycleConcurrentOnlyOneSucceeds",
	} {
		testrunner.DeclareDropped(name, gitReason)
	}
}
