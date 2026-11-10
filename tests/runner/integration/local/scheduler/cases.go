package scheduler

// 本文件是试点包的用例清单：一条既有 func Test* 对应一条 Case，分组与顺序与
// tests/integration/local/scheduler 下的源文件一致，便于与 R0 冻结清单逐条对账。
//
// 只有真正用到数据库的用例声明 sqlite-db：并发 Session 路径、归档不跨任务、并发工作区
// 初始化这三条只使用用例工作空间。
var (
	// scheduler_test.go

	schedulerDispatchesInQueueOrder = register(
		"scheduler-dispatches-in-queue-order", runSchedulerDispatchesInQueueOrder, databaseDecl()...,
	)
	schedulerRespectsConcurrencyLimit = register(
		"scheduler-respects-concurrency-limit", runSchedulerRespectsConcurrencyLimit, databaseDecl()...,
	)
	schedulerResumesAfterDispatchCompletes = register(
		"scheduler-resumes-after-dispatch-completes", runSchedulerResumesAfterDispatchCompletes, databaseDecl()...,
	)
	schedulerReleasesExecutionOnLaunchFailure = register(
		"scheduler-releases-execution-on-launch-failure", runSchedulerReleasesExecutionOnLaunchFailure,
		databaseDecl()...,
	)
	schedulerStopsWithoutDispatching = register(
		"scheduler-stops-without-dispatching", runSchedulerStopsWithoutDispatching, databaseDecl()...,
	)
	schedulerSkipsFailedAndPausedTasks = register(
		"scheduler-skips-failed-and-paused-tasks", runSchedulerSkipsFailedAndPausedTasks, databaseDecl()...,
	)
	schedulerClaimEventIsTraceable = register(
		"scheduler-claim-event-is-traceable", runSchedulerClaimEventIsTraceable, databaseDecl()...,
	)
	schedulerCountsExistingActiveTasks = register(
		"scheduler-counts-existing-active-tasks", runSchedulerCountsExistingActiveTasks, databaseDecl()...,
	)
	schedulerSkipsTaskWithActiveWorkflowRun = register(
		"scheduler-skips-task-with-active-workflow-run", runSchedulerSkipsTaskWithActiveWorkflowRun,
		databaseDecl()...,
	)
	schedulerRequiresPositiveLimit = register(
		"scheduler-requires-positive-limit", runSchedulerRequiresPositiveLimit, databaseDecl()...,
	)
	schedulerStatusReportsLimit = register(
		"scheduler-status-reports-limit", runSchedulerStatusReportsLimit, databaseDecl()...,
	)

	// requirement_delete_test.go

	schedulerSkipsTasksOfDeletedRequirement = register(
		"scheduler-skips-tasks-of-deleted-requirement", runSchedulerSkipsTasksOfDeletedRequirement, databaseDecl()...,
	)
	deleteRejectsWhileSchedulerDispatchInFlight = register(
		"delete-rejects-while-scheduler-dispatch-in-flight", runDeleteRejectsWhileSchedulerDispatchInFlight,
		databaseDecl()...,
	)
	concurrentDeleteAndSchedulerScanKeepFacts = register(
		"concurrent-delete-and-scheduler-scan-keep-facts", runConcurrentDeleteAndSchedulerScanKeepFacts,
		databaseDecl()...,
	)

	// retry_test.go

	retryFailedTaskStartsNewCycle = register(
		"retry-failed-task-starts-new-cycle", runRetryFailedTaskStartsNewCycle, databaseDecl()...,
	)
	retryRejectsNonFailedTask = register(
		"retry-rejects-non-failed-task", runRetryRejectsNonFailedTask, databaseDecl()...,
	)
	retryRejectsPendingSideEffect = register(
		"retry-rejects-pending-side-effect", runRetryRejectsPendingSideEffect, databaseDecl()...,
	)
	retryRejectsRecoveryResidue = register(
		"retry-rejects-recovery-residue", runRetryRejectsRecoveryResidue, databaseDecl()...,
	)
	retryReleasesExecutionOnLaunchFailure = register(
		"retry-releases-execution-on-launch-failure", runRetryReleasesExecutionOnLaunchFailure, databaseDecl()...,
	)
	retryConcurrentOnlyOneWins = register(
		"retry-concurrent-only-one-wins", runRetryConcurrentOnlyOneWins, databaseDecl()...,
	)
	retryEligibilityIsReadOnly = register(
		"retry-eligibility-is-read-only", runRetryEligibilityIsReadOnly, databaseDecl()...,
	)
	retryKeepsHistoricalRecords = register(
		"retry-keeps-historical-records", runRetryKeepsHistoricalRecords, databaseDecl()...,
	)
	retryDoesNotFakeSuccessOnDatabaseError = register(
		"retry-does-not-fake-success-on-database-error", runRetryDoesNotFakeSuccessOnDatabaseError,
		databaseDecl()...,
	)

	// concurrency_acceptance_test.go

	schedulerDoesNotExceedLimitUnderConcurrentScans = register(
		"scheduler-does-not-exceed-limit-under-concurrent-scans",
		runSchedulerDoesNotExceedLimitUnderConcurrentScans,
		databaseDecl()...,
	)
	schedulerReadFailureDoesNotFakeEmptyQueue = register(
		"scheduler-read-failure-does-not-fake-empty-queue", runSchedulerReadFailureDoesNotFakeEmptyQueue,
		databaseDecl()...,
	)
	schedulerLaunchFailureReleasesOnlyOwnExecution = register(
		"scheduler-launch-failure-releases-only-own-execution", runSchedulerLaunchFailureReleasesOnlyOwnExecution,
		databaseDecl()...,
	)
	cancelAndAdvanceRaceKeepsCancelIntent = register(
		"cancel-and-advance-race-keeps-cancel-intent", runCancelAndAdvanceRaceKeepsCancelIntent, databaseDecl()...,
	)
	retryRacesWithSchedulerForSameTask = register(
		"retry-races-with-scheduler-for-same-task", runRetryRacesWithSchedulerForSameTask, databaseDecl()...,
	)

	// isolation_test.go

	twoTasksUseIndependentContainers = register(
		"two-tasks-use-independent-containers", runTwoTasksUseIndependentContainers, databaseDecl()...,
	)
	taskContainerCannotBeReusedByAnotherTask = register(
		"task-container-cannot-be-reused-by-another-task", runTaskContainerCannotBeReusedByAnotherTask,
		databaseDecl()...,
	)
	workspaceInitRecordsDoNotCrossTasks = register(
		"workspace-init-records-do-not-cross-tasks", runWorkspaceInitRecordsDoNotCrossTasks, databaseDecl()...,
	)
	concurrentSessionPathsDoNotCollide = register(
		"concurrent-session-paths-do-not-collide", runConcurrentSessionPathsDoNotCollide,
	)
	archivingOneTaskKeepsAnotherTaskFiles = register(
		"archiving-one-task-keeps-another-task-files", runArchivingOneTaskKeepsAnotherTaskFiles,
	)
	sessionScopeRejectsInconsistentOwnership = register(
		"session-scope-rejects-inconsistent-ownership", runSessionScopeRejectsInconsistentOwnership,
		databaseDecl()...,
	)

	// resource_acceptance_test.go

	recoveringOneTaskKeepsAnotherTaskRecords = register(
		"recovering-one-task-keeps-another-task-records", runRecoveringOneTaskKeepsAnotherTaskRecords,
		databaseDecl()...,
	)
	concurrentContainerRecoveryKeepsOwnership = register(
		"concurrent-container-recovery-keeps-ownership", runConcurrentContainerRecoveryKeepsOwnership,
		databaseDecl()...,
	)
	ownershipMismatchBlocksAllDestructivePaths = register(
		"ownership-mismatch-blocks-all-destructive-paths", runOwnershipMismatchBlocksAllDestructivePaths,
		databaseDecl()...,
	)
	orphanContainerKeepsDiagnosticEvidence = register(
		"orphan-container-keeps-diagnostic-evidence", runOrphanContainerKeepsDiagnosticEvidence, databaseDecl()...,
	)
	unknownContainerKeepsEvidenceWhenUnreachable = register(
		"unknown-container-keeps-evidence-when-unreachable", runUnknownContainerKeepsEvidenceWhenUnreachable,
		databaseDecl()...,
	)
	reclaimPartialFailureKeepsRemainingScene = register(
		"reclaim-partial-failure-keeps-remaining-scene", runReclaimPartialFailureKeepsRemainingScene,
		databaseDecl()...,
	)
	concurrentWorkspaceInitDoesNotCrossTasks = register(
		"concurrent-workspace-init-does-not-cross-tasks", runConcurrentWorkspaceInitDoesNotCrossTasks,
	)
)
