// 本文件登记 tests/integration/local/workflow 的 49 条用例，顺序与源文件一致，
// 便于与 R0 冻结清单逐条对账。
//
// 用例主体在 cases_*.go 里，共用现场在 fixture_*.go 里；每条用例都打开自己的 SQLite
// 数据库，因此全部声明 sqlite-db，路径只从注入句柄读取；表驱动的多场景用例按场景份数
// 各声明一份（见 §5 多场景说明）。
//
// 依赖本机真实 Git 的 32 条用例（e2e_* 与 repair_budget_*）已按确认移除，理由登记在
// tests/runner/legacy；它们在本包里不再有用例主体与现场。
package workflow

var (
	// cancel_intent_test.go（5）
	_ = register("recover-cancel-intent-converges-task", runRecoverCancelIntentConvergesTask, databaseDecl()...)
	_ = register("recover-cancel-intent-blocks-new-workflow", runRecoverCancelIntentBlocksNewWorkflow, databaseDecl()...)
	_ = register("recover-cancel-intent-is-idempotent", runRecoverCancelIntentIsIdempotent, databaseDecl()...)
	_ = register("recover-cancel-intent-keeps-terminal-task", runRecoverCancelIntentKeepsTerminalTask, databaseDecl()...)
	_ = register("recover-cancel-intent-failure-blocks-startup", runRecoverCancelIntentFailureBlocksStartup, databaseDecl()...)

	// closeout_consistency_test.go（6）
	_ = register("closeout-writes-run-before-task", runCloseoutWritesRunBeforeTask, databaseDecl()...)
	_ = register("closeout-task-write-fails-after-run-completed", runCloseoutTaskWriteFailsAfterRunCompleted, databaseDecl()...)
	_ = register("closeout-run-write-fails-before-task-write", runCloseoutRunWriteFailsBeforeTaskWrite, databaseDecl()...)
	_ = register("restart-converges-task-stuck-in-saving", runRestartConvergesTaskStuckInSaving, databaseDecl()...)
	_ = register("restart-completes-task-from-completed-run", runRestartCompletesTaskFromCompletedRun, databaseDecl()...)
	_ = register("restart-flags-legacy-completed-with-recovery-run", runRestartFlagsLegacyCompletedWithRecoveryRun, databaseDecl()...)

	// delivery_chain_test.go（9）
	_ = register("delivery-chain-repair-then-code-save-archive-and-closeout", runDeliveryChainRepairThenCodeSaveArchiveAndCloseout, databaseDecl()...)
	_ = register("delivery-chain-stops-at-repair-limit", runDeliveryChainStopsAtRepairLimit, databaseDecl()...)
	_ = register("delivery-chain-stops-when-code-save-fails", runDeliveryChainStopsWhenCodeSaveFails, databaseDecl()...)
	_ = register("delivery-chain-stops-when-code-save-indeterminate", runDeliveryChainStopsWhenCodeSaveIndeterminate, databaseDecl()...)
	_ = register("delivery-chain-stops-when-code-save-record-unconfirmed", runDeliveryChainStopsWhenCodeSaveRecordUnconfirmed, databaseDecl()...)
	_ = register("delivery-chain-stops-without-code-saver", runDeliveryChainStopsWithoutCodeSaver, databaseDecl()...)
	_ = register("delivery-chain-stops-when-session-archive-incomplete", runDeliveryChainStopsWhenSessionArchiveIncomplete, databaseDeclCount(4)...)
	_ = register("delivery-chain-stops-when-session-query-fails", runDeliveryChainStopsWhenSessionQueryFails, databaseDecl()...)
	_ = register("delivery-chain-keeps-task-completed-when-closeout-fails", runDeliveryChainKeepsTaskCompletedWhenCloseoutFails, databaseDecl()...)

	// launch_chain_test.go（4）
	_ = register("launch-chain-reaches-planned-with-persisted-plan", runLaunchChainReachesPlannedWithPersistedPlan, databaseDecl()...)
	_ = register("launch-chain-is-idempotent-on-repeat", runLaunchChainIsIdempotentOnRepeat, databaseDecl()...)
	_ = register("launch-chain-stops-on-workspace-failure", runLaunchChainStopsOnWorkspaceFailure, databaseDecl()...)
	_ = register("launch-chain-rejects-uncertain-workspace-without-replay", runLaunchChainRejectsUncertainWorkspaceWithoutReplay, databaseDecl()...)

	// launch_test.go（9）
	_ = register("launch-persists-container-and-plan-across-stages", runLaunchPersistsContainerAndPlanAcrossStages, databaseDecl()...)
	_ = register("launch-does-not-create-second-container", runLaunchDoesNotCreateSecondContainer, databaseDecl()...)
	_ = register("launch-rejects-second-call-while-run-active", runLaunchRejectsSecondCallWhileRunActive, databaseDecl()...)
	_ = register("recover-step-occupancy-persists-recovery-required", runRecoverStepOccupancyPersistsRecoveryRequired, databaseDecl()...)
	_ = register("recover-step-occupancy-keeps-active-run-occupancy", runRecoverStepOccupancyKeepsActiveRunOccupancy, databaseDecl()...)
	_ = register("launch-initializes-workspace-before-plan", runLaunchInitializesWorkspaceBeforePlan, databaseDecl()...)
	_ = register("launch-stops-at-workspace-stage-when-uncertain", runLaunchStopsAtWorkspaceStageWhenUncertain, databaseDecl()...)
	_ = register("workspace-initialization-persists-across-restart", runWorkspaceInitializationPersistsAcrossRestart, databaseDecl()...)
	_ = register("workspace-interrupted-converges-to-uncertain", runWorkspaceInterruptedConvergesToUncertain, databaseDecl()...)

	// recovery_test.go（11）
	_ = register("recover-plan-generation-on-restart", runRecoverPlanGenerationOnRestart, databaseDeclCount(2)...)
	_ = register("recover-plan-generation-failure-blocks-startup", runRecoverPlanGenerationFailureBlocksStartup, databaseDeclCount(2)...)
	_ = register("recover-unfinished-run-failure-blocks-startup", runRecoverUnfinishedRunFailureBlocksStartup, databaseDecl()...)
	_ = register("recover-unfinished-run-on-restart", runRecoverUnfinishedRunOnRestart, databaseDecl()...)
	_ = register("recover-cancelling-on-restart", runRecoverCancellingOnRestart, databaseDeclCount(3)...)
	_ = register("recover-cancelling-failure-blocks-startup", runRecoverCancellingFailureBlocksStartup, databaseDecl()...)
	_ = register("recover-cancelling-active-run-read-failure", runRecoverCancellingActiveRunReadFailure, databaseDecl()...)
	_ = register("concurrent-recovery-converges-once", runConcurrentRecoveryConvergesOnce, databaseDecl()...)
	_ = register("plan-generation-interrupted-by-shutdown", runPlanGenerationInterruptedByShutdown, databaseDecl()...)
	_ = register("plan-generation-persists-across-restart", runPlanGenerationPersistsAcrossRestart, databaseDecl()...)
	_ = register("concurrent-generate-plan-rejects-second", runConcurrentGeneratePlanRejectsSecond, databaseDecl()...)

	// step_occupancy_test.go（5）
	_ = register("step-occupancy-converges-after-run-blocked", runStepOccupancyConvergesAfterRunBlocked, databaseDecl()...)
	_ = register("step-occupancy-keeps-running-workflow-owner", runStepOccupancyKeepsRunningWorkflowOwner, databaseDecl()...)
	_ = register("step-occupancy-converged-task-can-launch", runStepOccupancyConvergedTaskCanLaunch, databaseDecl()...)
	_ = register("step-occupancy-converges-once-across-restarts", runStepOccupancyConvergesOnceAcrossRestarts, databaseDecl()...)
	_ = register("step-occupancy-read-failure-does-not-converge", runStepOccupancyReadFailureDoesNotConverge, databaseDecl()...)
)
