// 本文件登记 tests/unit/store 的 248 条用例，顺序与源文件一致，便于按包核对迁移数量。
//
// 247 条源用例带 t.Parallel()，按可并行条目登记；migrations-bootstrap 源用例没有
// t.Parallel()，按串行条目登记。每个源文件对应一个 cases_*.go，注释里保留源文件名。
package sqlite

var (
	// attempts_test.go

	_ = register("create-and-get-attempt", runCreateAndGetAttempt)
	_ = register("multiple-attempts-per-step-keep-history", runMultipleAttemptsPerStepKeepHistory)
	_ = register("duplicate-attempt-number-is-rejected", runDuplicateAttemptNumberIsRejected)
	_ = register("attempt-requires-positive-number", runAttemptRequiresPositiveNumber)
	_ = register("next-attempt-number", runNextAttemptNumber)
	_ = register("next-attempt-number-on-empty-step", runNextAttemptNumberOnEmptyStep)
	_ = register("attempt-interrupted-status-persists", runAttemptInterruptedStatusPersists)
	_ = register("attempt-accepts-unknown-status-at-database-level", runAttemptAcceptsUnknownStatusAtDatabaseLevel)
	_ = register("attempt-allows-orphan-step-id", runAttemptAllowsOrphanStepID)
	_ = register("get-attempt-not-found", runGetAttemptNotFound)
	_ = register("delete-step-keeps-attempt-history", runDeleteStepKeepsAttemptHistory)
	_ = register("attempt-git-state-round-trip", runAttemptGitStateRoundTrip)
	_ = register("attempt-without-git-state-keeps-null", runAttemptWithoutGitStateKeepsNull)
	_ = register("attempt-git-state-failed-read-is-preserved", runAttemptGitStateFailedReadIsPreserved)

	// code_fetches_test.go

	_ = register("code-fetch-record-lifecycle", runCodeFetchRecordLifecycle)
	_ = register("code-fetch-record-failure-keeps-stage", runCodeFetchRecordFailureKeepsStage)
	_ = register("code-fetch-records-listed-per-attempt", runCodeFetchRecordsListedPerAttempt)
	_ = register("code-fetch-record-rejects-unknown-id", runCodeFetchRecordRejectsUnknownID)
	_ = register("code-fetch-record-update-missing-returns-not-found", runCodeFetchRecordUpdateMissingReturnsNotFound)
	_ = register("code-fetch-record-accepts-unknown-enum-values", runCodeFetchRecordAcceptsUnknownEnumValues)
	_ = register("code-fetch-record-requires-commit-for-success", runCodeFetchRecordRequiresCommitForSuccess)
	_ = register("code-fetch-record-survives-task-deletion", runCodeFetchRecordSurvivesTaskDeletion)

	// code_saves_test.go

	_ = register("code-save-record-lifecycle", runCodeSaveRecordLifecycle)
	_ = register("code-save-records-are-independent", runCodeSaveRecordsAreIndependent)
	_ = register("code-save-record-requires-commit-sha", runCodeSaveRecordRequiresCommitSHA)
	_ = register("code-save-record-rejects-merge-without-request", runCodeSaveRecordRejectsMergeWithoutRequest)
	_ = register("code-save-record-not-found", runCodeSaveRecordNotFound)
	_ = register("code-save-record-concurrent-writes", runCodeSaveRecordConcurrentWrites)
	_ = register("code-save-record-recovery-fields", runCodeSaveRecordRecoveryFields)
	_ = register("code-save-record-recovery-status", runCodeSaveRecordRecoveryStatus)
	_ = register("code-save-record-legacy-rows-remain-readable", runCodeSaveRecordLegacyRowsRemainReadable)
	_ = register("code-save-record-accepts-unknown-recovery-values", runCodeSaveRecordAcceptsUnknownRecoveryValues)
	_ = register("code-save-recovery-write-is-conditional", runCodeSaveRecoveryWriteIsConditional)
	_ = register("code-save-recovery-write-keeps-execution-states", runCodeSaveRecoveryWriteKeepsExecutionStates)
	_ = register("code-save-recovery-write-missing-record", runCodeSaveRecoveryWriteMissingRecord)
	_ = register("code-save-recovery-result-persists-confirmed-stage", runCodeSaveRecoveryResultPersistsConfirmedStage)
	_ = register("code-save-recovery-result-rejects-stale-write", runCodeSaveRecoveryResultRejectsStaleWrite)
	_ = register("code-save-recovery-result-keeps-unrelated-fields", runCodeSaveRecoveryResultKeepsUnrelatedFields)

	// concurrency_test.go

	_ = register("parallel-databases-do-not-share-data", runParallelDatabasesDoNotShareData)
	_ = register("parallel-databases-use-distinct-files", runParallelDatabasesUseDistinctFiles)
	_ = register("concurrent-writes-to-shared-database-stay-consistent", runConcurrentWritesToSharedDatabaseStayConsistent)
	_ = register("connection-is-reusable-after-timeout", runConnectionIsReusableAfterTimeout)
	_ = register("closed-database-rejects-further-use", runClosedDatabaseRejectsFurtherUse)

	// environment_versions_test.go

	_ = register("environment-version-is-immutable", runEnvironmentVersionIsImmutable)
	_ = register("next-environment-version-number-is-monotonic", runNextEnvironmentVersionNumberIsMonotonic)
	_ = register("list-environment-versions-filters-by-environment", runListEnvironmentVersionsFiltersByEnvironment)
	_ = register("environment-version-rejects-invalid-hashes", runEnvironmentVersionRejectsInvalidHashes)
	_ = register("container-name-is-unique-per-host", runContainerNameIsUniquePerHost)
	_ = register("container-lifecycle-persists-detached-state", runContainerLifecyclePersistsDetachedState)
	_ = register("get-container-by-name", runGetContainerByName)
	_ = register("concurrent-environment-version-creation-does-not-collide", runConcurrentEnvironmentVersionCreationDoesNotCollide)
	_ = register("update-container-persists-docker-id", runUpdateContainerPersistsDockerID)

	// events_test.go

	_ = register("append-event-assigns-incremental-id", runAppendEventAssignsIncrementalID)
	_ = register("list-events-by-task-in-insertion-order", runListEventsByTaskInInsertionOrder)
	_ = register("list-events-filter-by-step-and-limit", runListEventsFilterByStepAndLimit)
	_ = register("event-with-session-reference", runEventWithSessionReference)
	_ = register("empty-event-payload-becomes-empty-object", runEmptyEventPayloadBecomesEmptyObject)
	_ = register("event-rejects-unknown-type", runEventRejectsUnknownType)
	_ = register("event-rejects-invalid-payload", runEventRejectsInvalidPayload)
	_ = register("event-allows-orphan-task-id", runEventAllowsOrphanTaskID)
	_ = register("event-json-column-requires-valid-json", runEventJSONColumnRequiresValidJSON)
	_ = register("status-update-and-event-commit-atomically", runStatusUpdateAndEventCommitAtomically)
	_ = register("status-update-and-event-roll-back-together", runStatusUpdateAndEventRollBackTogether)
	_ = register("full-history-chain", runFullHistoryChain)
	_ = register("list-events-orders-by-created-at-then-id", runListEventsOrdersByCreatedAtThenID)
	_ = register("list-events-by-type-and-offset", runListEventsByTypeAndOffset)
	_ = register("list-events-keeps-unknown-event-type", runListEventsKeepsUnknownEventType)

	// execution_hosts_test.go

	_ = register("create-and-get-execution-host", runCreateAndGetExecutionHost)
	_ = register("get-execution-host-not-found", runGetExecutionHostNotFound)
	_ = register("update-execution-host", runUpdateExecutionHost)
	_ = register("update-missing-execution-host-returns-not-found", runUpdateMissingExecutionHostReturnsNotFound)
	_ = register("list-execution-hosts-ordered-by-creation", runListExecutionHostsOrderedByCreation)
	_ = register("delete-execution-host", runDeleteExecutionHost)
	_ = register("set-default-execution-host-switches", runSetDefaultExecutionHostSwitches)
	_ = register("set-default-execution-host-is-idempotent", runSetDefaultExecutionHostIsIdempotent)
	_ = register("set-default-execution-host-not-found", runSetDefaultExecutionHostNotFound)
	_ = register("get-default-execution-host-not-found", runGetDefaultExecutionHostNotFound)
	_ = register("create-execution-host-rejects-second-default", runCreateExecutionHostRejectsSecondDefault)
	_ = register("delete-default-execution-host-leaves-no-default", runDeleteDefaultExecutionHostLeavesNoDefault)
	_ = register("create-execution-host-rejects-invalid-port", runCreateExecutionHostRejectsInvalidPort)
	_ = register("create-execution-host-rejects-duplicate-id", runCreateExecutionHostRejectsDuplicateID)
	_ = register("execution-host-transaction-rollback", runExecutionHostTransactionRollback)
	_ = register("execution-host-stores-no-credential-columns", runExecutionHostStoresNoCredentialColumns)
	_ = register("execution-host-auth-fields-round-trip", runExecutionHostAuthFieldsRoundTrip)
	_ = register("execution-host-private-key-requires-path", runExecutionHostPrivateKeyRequiresPath)
	_ = register("execution-host-rejects-plaintext-secret-columns", runExecutionHostRejectsPlaintextSecretColumns)

	// execution_records_test.go

	_ = register("start-attempt-allocates-sequential-numbers", runStartAttemptAllocatesSequentialNumbers)
	_ = register("start-attempt-is-concurrency-safe", runStartAttemptIsConcurrencySafe)
	_ = register("start-attempt-rejects-mismatched-task", runStartAttemptRejectsMismatchedTask)
	_ = register("create-attempt-rejects-mismatched-task", runCreateAttemptRejectsMismatchedTask)
	_ = register("update-attempt-if-status-rejects-stale-result", runUpdateAttemptIfStatusRejectsStaleResult)
	_ = register("list-attempt-page-paging-is-stable", runListAttemptPagePagingIsStable)
	_ = register("list-attempt-page-filters-by-status", runListAttemptPageFiltersByStatus)
	_ = register("update-step-if-status-rejects-stale-result", runUpdateStepIfStatusRejectsStaleResult)
	_ = register("step-failure-summary-round-trip", runStepFailureSummaryRoundTrip)
	_ = register("list-step-page-orders-by-sequence", runListStepPageOrdersBySequence)
	_ = register("list-step-page-rejects-missing-task", runListStepPageRejectsMissingTask)
	_ = register("session-archive-is-independent-from-execution-status", runSessionArchiveIsIndependentFromExecutionStatus)
	_ = register("session-archive-records-path", runSessionArchiveRecordsPath)
	_ = register("session-export-status-independent-from-archive", runSessionExportStatusIndependentFromArchive)
	_ = register("list-session-page-filters-by-attempt-and-role", runListSessionPageFiltersByAttemptAndRole)
	_ = register("session-accepts-unknown-archive-status", runSessionAcceptsUnknownArchiveStatus)
	_ = register("attempt-task-id-is-backfilled-from-steps", runAttemptTaskIDIsBackfilledFromSteps)
	_ = register("attempt-scope-requires-task-and-step", runAttemptScopeRequiresTaskAndStep)

	// migrations_test.go

	_ = registerSerial("migrations-bootstrap", runMigrationsBootstrap)

	// planning_analysis_claim_test.go

	_ = register("claim-planning-analysis-rejects-second-claim", runClaimPlanningAnalysisRejectsSecondClaim)
	_ = register("claim-planning-analysis-requires-analyzing-status", runClaimPlanningAnalysisRequiresAnalyzingStatus)
	_ = register("release-planning-analysis-keeps-session-facts", runReleasePlanningAnalysisKeepsSessionFacts)
	_ = register("claim-planning-analysis-serializes-concurrent-callers", runClaimPlanningAnalysisSerializesConcurrentCallers)
	_ = register("append-planning-revision-persists-snapshot-version", runAppendPlanningRevisionPersistsSnapshotVersion)
	_ = register("claim-planning-analysis-confirms-same-claim", runClaimPlanningAnalysisConfirmsSameClaim)
	_ = register("planning-analysis-claim-reads-current-claim", runPlanningAnalysisClaimReadsCurrentClaim)
	_ = register("list-planning-analysis-claims-filters-unclaimed", runListPlanningAnalysisClaimsFiltersUnclaimed)

	// planning_sessions_test.go

	_ = register("planning-session-store-round-trip", runPlanningSessionStoreRoundTrip)
	_ = register("planning-session-store-list-by-project", runPlanningSessionStoreListByProject)
	_ = register("planning-session-store-conditional-status-update", runPlanningSessionStoreConditionalStatusUpdate)
	_ = register("planning-session-message-sequence-allocation", runPlanningSessionMessageSequenceAllocation)
	_ = register("planning-session-message-concurrent-append", runPlanningSessionMessageConcurrentAppend)
	_ = register("planning-session-revision-round-trip", runPlanningSessionRevisionRoundTrip)
	_ = register("planning-session-revision-concurrent-append", runPlanningSessionRevisionConcurrentAppend)
	_ = register("planning-session-runtime-binding-cas", runPlanningSessionRuntimeBindingCAS)
	_ = register("planning-session-role-binding-cas", runPlanningSessionRoleBindingCAS)

	// projects_test.go

	_ = register("create-and-get-project", runCreateAndGetProject)
	_ = register("project-caches-round-trip", runProjectCachesRoundTrip)
	_ = register("create-project-rejects-duplicate-id", runCreateProjectRejectsDuplicateID)
	_ = register("get-project-not-found", runGetProjectNotFound)
	_ = register("update-project", runUpdateProject)
	_ = register("update-project-not-found", runUpdateProjectNotFound)
	_ = register("delete-project-is-logical", runDeleteProjectIsLogical)
	_ = register("list-projects-filters-deleted", runListProjectsFiltersDeleted)
	_ = register("project-keeps-unknown-environment-type", runProjectKeepsUnknownEnvironmentType)
	_ = register("project-default-host-round-trip", runProjectDefaultHostRoundTrip)
	_ = register("project-default-host-update-keeps-other-fields", runProjectDefaultHostUpdateKeepsOtherFields)
	_ = register("project-default-host-does-not-touch-task-snapshot", runProjectDefaultHostDoesNotTouchTaskSnapshot)

	// recovery_conditional_test.go

	_ = register("update-code-fetch-record-if-status-rejects-stale-status", runUpdateCodeFetchRecordIfStatusRejectsStaleStatus)
	_ = register("update-code-fetch-record-if-status-applies-matching-status", runUpdateCodeFetchRecordIfStatusAppliesMatchingStatus)
	_ = register("list-interrupted-code-fetch-records-only-active", runListInterruptedCodeFetchRecordsOnlyActive)
	_ = register("code-fetch-active-unique-index-rejects-second-active-record", runCodeFetchActiveUniqueIndexRejectsSecondActiveRecord)
	_ = register("update-task-workspace-init-if-status-rejects-stale-status", runUpdateTaskWorkspaceInitIfStatusRejectsStaleStatus)
	_ = register("update-container-if-state-rejects-stale-state", runUpdateContainerIfStateRejectsStaleState)
	_ = register("container-filter-by-task-and-orphans", runContainerFilterByTaskAndOrphans)

	// requirement_confirmation_test.go

	_ = register("confirmed-requirement-round-trip", runConfirmedRequirementRoundTrip)
	_ = register("manual-requirement-keeps-empty-confirmation-fields", runManualRequirementKeepsEmptyConfirmationFields)
	_ = register("requirement-sequence-unique-per-requirement", runRequirementSequenceUniquePerRequirement)
	_ = register("task-updates-keep-requirement-sequence", runTaskUpdatesKeepRequirementSequence)
	_ = register("list-requirements-by-project", runListRequirementsByProject)
	_ = register("task-list-ordered-by-sequence-independent-of-creation", runTaskListOrderedBySequenceIndependentOfCreation)

	// requirement_deletion_test.go

	_ = register("delete-requirement-is-conditional-and-hides-record", runDeleteRequirementIsConditionalAndHidesRecord)
	_ = register("delete-requirement-rejects-missing-and-empty-id", runDeleteRequirementRejectsMissingAndEmptyID)
	_ = register("delete-task-accepts-requirement-deletion-statuses", runDeleteTaskAcceptsRequirementDeletionStatuses)
	_ = register("delete-task-rejects-statuses-outside-allowed-set", runDeleteTaskRejectsStatusesOutsideAllowedSet)
	_ = register("requirement-deletion-hides-tasks-from-project-view", runRequirementDeletionHidesTasksFromProjectView)
	_ = register("requirement-deletion-rollback-keeps-tasks", runRequirementDeletionRollbackKeepsTasks)
	_ = register("requirement-deletion-racing-requirement-fails", runRequirementDeletionRacingRequirementFails)
	_ = register("concurrent-requirement-deletion-only-one-succeeds", runConcurrentRequirementDeletionOnlyOneSucceeds)
	_ = register("requirement-deletion-keeps-execution-history", runRequirementDeletionKeepsExecutionHistory)
	_ = register("unknown-task-status-survives-deletion-rules", runUnknownTaskStatusSurvivesDeletionRules)
	_ = register("requirement-deletion-idempotent-view", runRequirementDeletionIdempotentView)

	// roles_test.go

	_ = register("create-and-get-role", runCreateAndGetRole)
	_ = register("get-role-not-found", runGetRoleNotFound)
	_ = register("update-role", runUpdateRole)
	_ = register("update-missing-role-returns-not-found", runUpdateMissingRoleReturnsNotFound)
	_ = register("list-roles-ordered-by-creation", runListRolesOrderedByCreation)
	_ = register("create-role-rejects-invalid-value", runCreateRoleRejectsInvalidValue)
	_ = register("delete-role-referenced-by-history-is-soft-deleted", runDeleteRoleReferencedByHistoryIsSoftDeleted)
	_ = register("delete-role-keeps-row-for-history", runDeleteRoleKeepsRowForHistory)
	_ = register("delete-unreferenced-role", runDeleteUnreferencedRole)
	_ = register("seed-roles-fills-missing-fixed-roles", runSeedRolesFillsMissingFixedRoles)
	_ = register("seed-roles-keeps-existing-role-config", runSeedRolesKeepsExistingRoleConfig)

	// sessions_test.go

	_ = register("create-and-get-session", runCreateAndGetSession)
	_ = register("session-mark-running-persists-external-reference", runSessionMarkRunningPersistsExternalReference)
	_ = register("session-complete-persists-finished-at", runSessionCompletePersistsFinishedAt)
	_ = register("planner-session-without-step-and-attempt", runPlannerSessionWithoutStepAndAttempt)
	_ = register("attempt-can-have-developer-and-validator-sessions", runAttemptCanHaveDeveloperAndValidatorSessions)
	_ = register("session-scope-mismatch-is-rejected", runSessionScopeMismatchIsRejected)
	_ = register("session-allows-orphan-attempt-id", runSessionAllowsOrphanAttemptID)
	_ = register("session-allows-orphan-task-id", runSessionAllowsOrphanTaskID)
	_ = register("session-attempt-requires-step", runSessionAttemptRequiresStep)
	_ = register("session-accepts-unknown-statuses", runSessionAcceptsUnknownStatuses)
	_ = register("get-session-not-found", runGetSessionNotFound)
	_ = register("session-artifacts-are-persisted", runSessionArtifactsArePersisted)
	_ = register("session-export-failure-is-persisted", runSessionExportFailureIsPersisted)
	_ = register("session-missing-file-keeps-failed-status", runSessionMissingFileKeepsFailedStatus)

	// steps_test.go

	_ = register("create-and-get-step", runCreateAndGetStep)
	_ = register("step-role-snapshot-survives-role-update", runStepRoleSnapshotSurvivesRoleUpdate)
	_ = register("duplicate-step-sequence-is-rejected", runDuplicateStepSequenceIsRejected)
	_ = register("different-tasks-can-share-sequence", runDifferentTasksCanShareSequence)
	_ = register("step-requires-positive-sequence", runStepRequiresPositiveSequence)
	_ = register("create-steps-is-atomic", runCreateStepsIsAtomic)
	_ = register("create-steps-writes-all-in-order", runCreateStepsWritesAllInOrder)
	_ = register("step-transition-persists", runStepTransitionPersists)
	_ = register("step-accepts-unknown-status-and-type", runStepAcceptsUnknownStatusAndType)
	_ = register("step-json-columns-require-valid-json", runStepJSONColumnsRequireValidJSON)
	_ = register("step-allows-orphan-role-id", runStepAllowsOrphanRoleID)
	_ = register("delete-task-keeps-step-history", runDeleteTaskKeepsStepHistory)

	// steps_timestamps_test.go

	_ = register("step-timestamps-start-empty", runStepTimestampsStartEmpty)
	_ = register("step-started-at-persists", runStepStartedAtPersists)
	_ = register("step-finished-at-persists", runStepFinishedAtPersists)
	_ = register("step-failure-and-recovery-timestamps-persist", runStepFailureAndRecoveryTimestampsPersist)
	_ = register("step-re-execution-clears-finished-at-in-database", runStepReExecutionClearsFinishedAtInDatabase)
	_ = register("step-update-without-timestamps-keeps-them", runStepUpdateWithoutTimestampsKeepsThem)
	_ = register("step-timestamps-survive-unknown-status", runStepTimestampsSurviveUnknownStatus)
	_ = register("begin-step-execution-writes-started-at", runBeginStepExecutionWritesStartedAt)
	_ = register("begin-step-execution-rejects-second-occupancy", runBeginStepExecutionRejectsSecondOccupancy)
	_ = register("step-timestamps-survive-old-database-upgrade", runStepTimestampsSurviveOldDatabaseUpgrade)

	// task_management_test.go

	_ = register("list-task-page-returns-total-and-slice", runListTaskPageReturnsTotalAndSlice)
	_ = register("list-task-page-filters-by-status", runListTaskPageFiltersByStatus)
	_ = register("delete-task-marks-deleted-and-keeps-history", runDeleteTaskMarksDeletedAndKeepsHistory)
	_ = register("delete-task-rejects-unexpected-status", runDeleteTaskRejectsUnexpectedStatus)
	_ = register("delete-task-is-not-idempotent-at-storage-level", runDeleteTaskIsNotIdempotentAtStorageLevel)
	_ = register("delete-task-rejects-empty-expected-statuses", runDeleteTaskRejectsEmptyExpectedStatuses)
	_ = register("task-deleted-at-persists-and-round-trips", runTaskDeletedAtPersistsAndRoundTrips)
	_ = register("unknown-status-survives-soft-delete", runUnknownStatusSurvivesSoftDelete)
	_ = register("concurrent-delete-task-only-one-succeeds", runConcurrentDeleteTaskOnlyOneSucceeds)

	// task_workspace_inits_test.go

	_ = register("task-workspace-init-lifecycle", runTaskWorkspaceInitLifecycle)
	_ = register("task-workspace-init-listed-per-task", runTaskWorkspaceInitListedPerTask)
	_ = register("task-workspace-init-active-unique", runTaskWorkspaceInitActiveUnique)
	_ = register("task-workspace-init-list-interrupted", runTaskWorkspaceInitListInterrupted)
	_ = register("task-workspace-init-unknown-id-returns-not-found", runTaskWorkspaceInitUnknownIDReturnsNotFound)
	_ = register("task-workspace-init-update-missing-returns-not-found", runTaskWorkspaceInitUpdateMissingReturnsNotFound)
	_ = register("task-workspace-init-accepts-unknown-enum-values", runTaskWorkspaceInitAcceptsUnknownEnumValues)
	_ = register("task-workspace-init-requires-commit-for-success", runTaskWorkspaceInitRequiresCommitForSuccess)
	_ = register("task-workspace-init-survives-task-deletion", runTaskWorkspaceInitSurvivesTaskDeletion)

	// tasks_test.go

	_ = register("create-and-get-task", runCreateAndGetTask)
	_ = register("create-task-allows-orphan-requirement-id", runCreateTaskAllowsOrphanRequirementID)
	_ = register("task-transition-persists-worktree-and-step", runTaskTransitionPersistsWorktreeAndStep)
	_ = register("task-terminal-state-records-finished-at", runTaskTerminalStateRecordsFinishedAt)
	_ = register("list-tasks-filters-by-requirement-and-status", runListTasksFiltersByRequirementAndStatus)
	_ = register("same-requirement-can-have-multiple-tasks", runSameRequirementCanHaveMultipleTasks)
	_ = register("task-accepts-unknown-status-at-database-level", runTaskAcceptsUnknownStatusAtDatabaseLevel)
	_ = register("task-requires-repository-path-and-base-ref", runTaskRequiresRepositoryPathAndBaseRef)
	_ = register("delete-requirement-keeps-task-history", runDeleteRequirementKeepsTaskHistory)

	// validation_feedbacks_test.go

	_ = register("create-validation-failure-feedback", runCreateValidationFailureFeedback)
	_ = register("validation-failure-feedback-is-append-only", runValidationFailureFeedbackIsAppendOnly)
	_ = register("latest-validation-failure-feedback-by-step", runLatestValidationFailureFeedbackByStep)
	_ = register("validation-failure-feedback-not-found", runValidationFailureFeedbackNotFound)

	// validations_test.go

	_ = register("begin-validation-run-creates-running-record", runBeginValidationRunCreatesRunningRecord)
	_ = register("begin-validation-run-rejects-concurrent-start", runBeginValidationRunRejectsConcurrentStart)
	_ = register("begin-validation-run-rejects-stale-task-status", runBeginValidationRunRejectsStaleTaskStatus)
	_ = register("begin-validation-run-rejects-foreign-attempt", runBeginValidationRunRejectsForeignAttempt)
	_ = register("append-validation-check-results-and-finish", runAppendValidationCheckResultsAndFinish)
	_ = register("finish-validation-run-rejects-late-result", runFinishValidationRunRejectsLateResult)
	_ = register("finish-validation-run-requires-failure-summary", runFinishValidationRunRequiresFailureSummary)
	_ = register("fail-validation-run", runFailValidationRun)
	_ = register("validation-run-keeps-unknown-status", runValidationRunKeepsUnknownStatus)
	_ = register("list-validation-runs-by-attempt-keeps-history", runListValidationRunsByAttemptKeepsHistory)
	_ = register("finish-validation-run-persists-review", runFinishValidationRunPersistsReview)
	_ = register("finish-validation-run-persists-unknown-review-status", runFinishValidationRunPersistsUnknownReviewStatus)
)
