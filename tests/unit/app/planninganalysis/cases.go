// 本文件登记 tests/unit/app/planninganalysis 的 24 条用例，顺序与源文件一致，便于按包核对
// 迁移数量。
//
// 源文件 cases_fixture.go 没有用例，只有测试现场与替身，因此不登记。24 条源用例都带
// t.Parallel()，全部按可并行条目登记。
package planninganalysis

var (
	// cases_analysis.go

	_ = register("analyze-produces-single-revision", runAnalyzeProducesSingleRevision)
	_ = register("analyze-keeps-role-sessions-distinct", runAnalyzeKeepsRoleSessionsDistinct)
	_ = register("analyze-skips-planner-when-analyst-unusable", runAnalyzeSkipsPlannerWhenAnalystUnusable)
	_ = register(
		"analyze-fails-without-revision-when-planner-unusable",
		runAnalyzeFailsWithoutRevisionWhenPlannerUnusable,
	)
	_ = register(
		"analyze-rejects-concurrent-run-on-same-session",
		runAnalyzeRejectsConcurrentRunOnSameSession,
	)
	_ = register("analyze-isolates-different-sessions", runAnalyzeIsolatesDifferentSessions)
	_ = register("analyze-retry-after-failure", runAnalyzeRetryAfterFailure)
	_ = register(
		"analyze-records-failure-when-revision-not-persisted",
		runAnalyzeRecordsFailureWhenRevisionNotPersisted,
	)
	_ = register("analyze-result-survives-restart", runAnalyzeResultSurvivesRestart)
	_ = register("analyze-creates-no-formal-entities", runAnalyzeCreatesNoFormalEntities)
	_ = register("analyze-leaves-project-files-untouched", runAnalyzeLeavesProjectFilesUntouched)
	_ = register("analyze-with-unknown-session-reports-not-found", runAnalyzeWithUnknownSessionReportsNotFound)
	_ = register("analyze-requires-analyzing-session", runAnalyzeRequiresAnalyzingSession)

	// cases_persistence.go

	_ = register(
		"analyze-rolls-back-when-stage-message-not-persisted",
		runAnalyzeRollsBackWhenStageMessageNotPersisted,
	)
	_ = register(
		"analyze-rolls-back-when-status-update-conflicts",
		runAnalyzeRollsBackWhenStatusUpdateConflicts,
	)
	_ = register("analyze-rolls-back-when-claim-not-released", runAnalyzeRollsBackWhenClaimNotReleased)
	_ = register(
		"analyze-reports-not-persisted-when-failure-record-fails",
		runAnalyzeReportsNotPersistedWhenFailureRecordFails,
	)
	_ = register(
		"retry-analysis-recovers-claimless-analyzing-session",
		runRetryAnalysisRecoversClaimlessAnalyzingSession,
	)
	_ = register("retry-analysis-rejects-active-session", runRetryAnalysisRejectsActiveSession)

	// cases_recovery.go

	_ = register(
		"planning-analysis-recovery-converges-interrupted-claim",
		runPlanningAnalysisRecoveryConvergesInterruptedClaim,
	)
	_ = register(
		"planning-analysis-recovery-leaves-claimless-analyzing-untouched",
		runPlanningAnalysisRecoveryLeavesClaimlessAnalyzingUntouched,
	)
	_ = register(
		"planning-analysis-recovery-skips-non-analyzing-session",
		runPlanningAnalysisRecoverySkipsNonAnalyzingSession,
	)
	_ = register(
		"planning-analysis-recovery-skips-claim-taken-by-another-executor",
		runPlanningAnalysisRecoverySkipsClaimTakenByAnotherExecutor,
	)
	_ = register("planning-analysis-recovery-is-idempotent", runPlanningAnalysisRecoveryIsIdempotent)
)
