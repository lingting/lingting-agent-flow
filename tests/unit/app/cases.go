// 本文件登记 tests/unit/app 的 406 条用例，顺序与源文件一致，便于按包核对迁移数量。
//
// 370 条源用例带 t.Parallel()，按可并行条目登记；36 条源用例
// 没有 t.Parallel()，按串行条目登记。每个源文件对应一个 cases_*.go，注释里保留源文件名。
package app

var (
	// code_initialize_test.go

	_ = register("initialize-succeeds", runInitializeSucceeds)
	_ = register("initialize-rejects-foreign-attempt", runInitializeRejectsForeignAttempt)
	_ = register("initialize-rejects-unknown-step", runInitializeRejectsUnknownStep)
	_ = register("initialize-requires-running-container", runInitializeRequiresRunningContainer)
	_ = register("initialize-rejects-detached-container", runInitializeRejectsDetachedContainer)
	_ = register("initialize-requires-container-attempt-binding", runInitializeRequiresContainerAttemptBinding)
	_ = register("initialize-requires-container-attempt-registered", runInitializeRequiresContainerAttemptRegistered)
	_ = register("initialize-skips-already-initialized-attempt", runInitializeSkipsAlreadyInitializedAttempt)
	_ = register("initialize-failure-records-stage", runInitializeFailureRecordsStage)
	_ = register("initialize-timeout-maps-to-timed-out", runInitializeTimeoutMapsToTimedOut)
	_ = register("initialize-cancel-maps-to-cancelled", runInitializeCancelMapsToCancelled)
	_ = register("initialize-uncertain-maps-to-uncertain", runInitializeUncertainMapsToUncertain)
	_ = register("initialize-rejects-invalid-input", runInitializeRejectsInvalidInput)
	_ = register("initialize-rejects-invalid-work-dir", runInitializeRejectsInvalidWorkDir)
	_ = register("initialize-rejects-missing-container", runInitializeRejectsMissingContainer)
	_ = register("initialize-persists-event", runInitializePersistsEvent)
	_ = register("initialize-rejects-cancelled-task", runInitializeRejectsCancelledTask)
	_ = register("initialize-keeps-work-dir-under-workspace", runInitializeKeepsWorkDirUnderWorkspace)

	// code_save_query_test.go

	_ = register("code-save-query-returns-task-records", runCodeSaveQueryReturnsTaskRecords)
	_ = register("code-save-query-orders-newest-first", runCodeSaveQueryOrdersNewestFirst)
	_ = register("code-save-query-rejects-deleted-task", runCodeSaveQueryRejectsDeletedTask)
	_ = register("code-save-query-rejects-unknown-task", runCodeSaveQueryRejectsUnknownTask)
	_ = register("code-save-query-rejects-invalid-task-i-d", runCodeSaveQueryRejectsInvalidTaskID)
	_ = register("code-save-query-returns-empty-list", runCodeSaveQueryReturnsEmptyList)
	_ = register("code-save-query-storage-failure-is-not-not-found", runCodeSaveQueryStorageFailureIsNotNotFound)
	_ = register("code-save-query-is-read-only", runCodeSaveQueryIsReadOnly)
	_ = register("code-save-query-requires-store", runCodeSaveQueryRequiresStore)
	_ = register("code-save-query-store-has-no-remote-dependency", runCodeSaveQueryStoreHasNoRemoteDependency)

	// container_exec_state_test.go

	_ = register("container-service-exec-requires-actively-running", runContainerServiceExecRequiresActivelyRunning)
	_ = register("container-service-exec-runs-on-confirmed-running-container", runContainerServiceExecRunsOnConfirmedRunningContainer)
	_ = register("container-service-run-agent-requires-actively-running", runContainerServiceRunAgentRequiresActivelyRunning)
	_ = register("container-service-exec-skips-remote-effects-when-inspect-uncertain", runContainerServiceExecSkipsRemoteEffectsWhenInspectUncertain)
	_ = register("container-service-exec-and-stop-serialize-on-container-lock", runContainerServiceExecAndStopSerializeOnContainerLock)

	// container_fixture_test.go

	// container_recovery_state_test.go

	_ = register("classify-container-observation-status-matrix", runClassifyContainerObservationStatusMatrix)
	_ = register("classify-container-observation-reports-indeterminate", runClassifyContainerObservationReportsIndeterminate)
	_ = register("container-recovery-service-keeps-unstable-container-unchanged", runContainerRecoveryServiceKeepsUnstableContainerUnchanged)

	// container_remote_fixture_test.go

	// container_start_state_test.go

	_ = register("container-service-start-converges-stale-running-state", runContainerServiceStartConvergesStaleRunningState)
	_ = register("container-service-start-converges-unknown-state", runContainerServiceStartConvergesUnknownState)
	_ = register("container-service-start-converges-created-docker-state", runContainerServiceStartConvergesCreatedDockerState)
	_ = register("container-service-start-does-not-start-already-running-container", runContainerServiceStartDoesNotStartAlreadyRunningContainer)
	_ = register("container-service-start-refuses-unconfirmed-docker-states", runContainerServiceStartRefusesUnconfirmedDockerStates)
	_ = register("container-service-start-keeps-state-when-inspect-uncertain", runContainerServiceStartKeepsStateWhenInspectUncertain)
	_ = register("container-service-start-refuses-when-state-persist-fails", runContainerServiceStartRefusesWhenStatePersistFails)
	_ = register("container-service-start-keeps-persisted-state-when-registry-sync-fails", runContainerServiceStartKeepsPersistedStateWhenRegistrySyncFails)
	_ = register("container-service-start-refuses-docker-id-mismatch", runContainerServiceStartRefusesDockerIDMismatch)
	_ = register("container-service-start-concurrent-calls-start-once", runContainerServiceStartConcurrentCallsStartOnce)

	// container_stop_test.go

	_ = register("container-service-stop-confirms-verified-state", runContainerServiceStopConfirmsVerifiedState)
	_ = register("container-service-stop-already-stopped-converges", runContainerServiceStopAlreadyStoppedConverges)
	_ = register("container-service-stop-converges-when-stop-command-uncertain", runContainerServiceStopConvergesWhenStopCommandUncertain)
	_ = register("container-service-stop-reports-container-still-running", runContainerServiceStopReportsContainerStillRunning)
	_ = register("container-service-stop-marks-unknown-when-state-cannot-be-confirmed", runContainerServiceStopMarksUnknownWhenStateCannotBeConfirmed)
	_ = register("container-service-stop-refuses-detached-container", runContainerServiceStopRefusesDetachedContainer)
	_ = register("container-service-stop-refuses-missing-record", runContainerServiceStopRefusesMissingRecord)
	_ = register("container-service-run-agent-stops-planning-container-on-uncertain", runContainerServiceRunAgentStopsPlanningContainerOnUncertain)
	_ = register("container-service-run-agent-refuses-stop-on-planning-label-mismatch", runContainerServiceRunAgentRefusesStopOnPlanningLabelMismatch)
	_ = register("container-service-run-agent-refuses-stop-on-docker-id-mismatch", runContainerServiceRunAgentRefusesStopOnDockerIDMismatch)
	_ = register("container-service-run-agent-does-not-stop-task-container", runContainerServiceRunAgentDoesNotStopTaskContainer)
	_ = register("container-service-run-agent-reloads-stopped-state-with-cancelled-context", runContainerServiceRunAgentReloadsStoppedStateWithCancelledContext)
	_ = register("container-service-run-agent-returns-persisted-unknown-state", runContainerServiceRunAgentReturnsPersistedUnknownState)
	_ = register("container-service-stop-does-not-treat-paused-as-stopped", runContainerServiceStopDoesNotTreatPausedAsStopped)
	_ = register("container-service-stop-refuses-to-record-stopped-for-unconfirmed-states", runContainerServiceStopRefusesToRecordStoppedForUnconfirmedStates)
	_ = register("container-service-stop-concurrent-calls-stop-once", runContainerServiceStopConcurrentCallsStopOnce)

	// coordination_test.go

	_ = register("planning-session-coordinator-reclaims-idle-locks", runPlanningSessionCoordinatorReclaimsIdleLocks)
	_ = register("environment-build-locks-reclaims-idle-locks", runEnvironmentBuildLocksReclaimsIdleLocks)
	_ = register("planning-session-coordinator-serializes-same-session", runPlanningSessionCoordinatorSerializesSameSession)
	_ = register("environment-build-locks-serializes-same-environment", runEnvironmentBuildLocksSerializesSameEnvironment)
	_ = register("planning-session-coordinator-allows-parallel-sessions", runPlanningSessionCoordinatorAllowsParallelSessions)
	_ = register("environment-build-locks-allows-parallel-environments", runEnvironmentBuildLocksAllowsParallelEnvironments)

	// environment_build_manager_test.go

	_ = register("build-manager-acceptance-and-dedupe", runBuildManagerAcceptanceAndDedupe)
	_ = register("build-manager-rejects-unknown-targets", runBuildManagerRejectsUnknownTargets)
	_ = register("build-manager-submit-before-start-is-rejected", runBuildManagerSubmitBeforeStartIsRejected)
	_ = register("build-manager-converges-leftover-building", runBuildManagerConvergesLeftoverBuilding)
	_ = register("build-manager-stop-cancels-in-flight-build", runBuildManagerStopCancelsInFlightBuild)
	_ = register("environment-version-conditional-update-guards-stale-result", runEnvironmentVersionConditionalUpdateGuardsStaleResult)

	// environments_query_test.go

	_ = register("environment-list-summarizes-versions", runEnvironmentListSummarizesVersions)
	_ = register("environment-list-distinguishes-current-from-latest", runEnvironmentListDistinguishesCurrentFromLatest)
	_ = register("environment-list-without-usable-version-keeps-current-empty", runEnvironmentListWithoutUsableVersionKeepsCurrentEmpty)
	_ = register("environment-list-is-sorted-by-version", runEnvironmentListIsSortedByVersion)
	_ = register("environment-list-empty-host-returns-empty-list", runEnvironmentListEmptyHostReturnsEmptyList)
	_ = register("environment-list-rejects-unknown-host", runEnvironmentListRejectsUnknownHost)
	_ = register("environment-list-rejects-empty-host", runEnvironmentListRejectsEmptyHost)
	_ = register("environment-get-version-returns-metadata", runEnvironmentGetVersionReturnsMetadata)
	_ = register("environment-get-version-rejects-missing-version", runEnvironmentGetVersionRejectsMissingVersion)
	_ = register("environment-get-version-rejects-invalid-input", runEnvironmentGetVersionRejectsInvalidInput)
	_ = register("environment-get-without-versions-returns-not-found", runEnvironmentGetWithoutVersionsReturnsNotFound)
	_ = register("environment-query-storage-failure-is-not-an-empty-list", runEnvironmentQueryStorageFailureIsNotAnEmptyList)
	_ = register("environment-query-service-requires-store", runEnvironmentQueryServiceRequiresStore)

	// execution_feedback_test.go

	_ = register("get-validation-failure-feedback-returns-persisted-feedback", runGetValidationFailureFeedbackReturnsPersistedFeedback)
	_ = register("get-validation-failure-feedback-rejects-deleted-task", runGetValidationFailureFeedbackRejectsDeletedTask)
	_ = register("get-validation-failure-feedback-rejects-unknown-step", runGetValidationFailureFeedbackRejectsUnknownStep)
	_ = register("get-validation-failure-feedback-rejects-foreign-step", runGetValidationFailureFeedbackRejectsForeignStep)
	_ = register("get-validation-failure-feedback-rejects-unknown-run", runGetValidationFailureFeedbackRejectsUnknownRun)
	_ = register("get-validation-failure-feedback-rejects-foreign-run", runGetValidationFailureFeedbackRejectsForeignRun)
	_ = register("get-validation-failure-feedback-rejects-run-without-feedback", runGetValidationFailureFeedbackRejectsRunWithoutFeedback)
	_ = register("get-validation-failure-feedback-rejects-invalid-i-ds", runGetValidationFailureFeedbackRejectsInvalidIDs)
	_ = register("get-validation-failure-feedback-storage-failure-is-not-not-found", runGetValidationFailureFeedbackStorageFailureIsNotNotFound)
	_ = register("execution-record-service-requires-store", runExecutionRecordServiceRequiresStore)

	// hosts_test.go

	_ = register("create-execution-host-with-defaults", runCreateExecutionHostWithDefaults)
	_ = register("create-execution-host-with-explicit-workspace", runCreateExecutionHostWithExplicitWorkspace)
	_ = register("create-execution-host-as-default", runCreateExecutionHostAsDefault)
	_ = register("create-execution-host-rejects-invalid-input", runCreateExecutionHostRejectsInvalidInput)
	_ = register("set-default-rejects-disabled-host", runSetDefaultRejectsDisabledHost)
	_ = register("set-default-requires-existing-host", runSetDefaultRequiresExistingHost)
	_ = register("update-execution-host-changes-fields", runUpdateExecutionHostChangesFields)
	_ = register("update-execution-host-switches-default", runUpdateExecutionHostSwitchesDefault)
	_ = register("remove-execution-host", runRemoveExecutionHost)
	_ = register("remove-execution-host-rejected-with-active-task", runRemoveExecutionHostRejectedWithActiveTask)
	_ = register("remove-execution-host-keeps-history-references", runRemoveExecutionHostKeepsHistoryReferences)
	_ = register("probe-execution-host-detects-s-s-h", runProbeExecutionHostDetectsSSH)
	_ = register("probe-execution-host-not-s-s-h", runProbeExecutionHostNotSSH)
	_ = register("probe-execution-host-missing", runProbeExecutionHostMissing)
	_ = register("ssh-service-rejects-missing-store", runSSHServiceRejectsMissingStore)
	_ = register("ssh-service-known-hosts-paths", runSSHServiceKnownHostsPaths)
	_ = register("test-connection-unreachable", runTestConnectionUnreachable)
	_ = register("test-connection-rejects-disabled-host", runTestConnectionRejectsDisabledHost)
	_ = register("test-connection-missing-host", runTestConnectionMissingHost)
	_ = register("ensure-workspace-missing-host", runEnsureWorkspaceMissingHost)
	_ = register("trust-host-key-requires-fingerprint", runTrustHostKeyRequiresFingerprint)
	_ = register("probe-host-key-missing-host", runProbeHostKeyMissingHost)

	// plan_generation_test.go

	_ = register("generate-plan-persists-plan", runGeneratePlanPersistsPlan)
	_ = register("generate-plan-rejects-invalid-output", runGeneratePlanRejectsInvalidOutput)
	_ = register("generate-plan-converges-on-call-failure", runGeneratePlanConvergesOnCallFailure)
	_ = register("generate-plan-requires-sessions", runGeneratePlanRequiresSessions)
	_ = register("generate-plan-rejects-prerequisites", runGeneratePlanRejectsPrerequisites)
	_ = register("generate-plan-rolls-back-on-persist-failure", runGeneratePlanRollsBackOnPersistFailure)
	_ = register("generate-plan-rejects-concurrent-request", runGeneratePlanRejectsConcurrentRequest)
	_ = register("recover-plan-generation-converges-planning-tasks", runRecoverPlanGenerationConvergesPlanningTasks)

	// planning_recovery_test.go

	_ = register("planning-role-restarts-same-container-after-interrupted-call", runPlanningRoleRestartsSameContainerAfterInterruptedCall)
	_ = register("planning-role-does-not-retry-after-uncertain-termination", runPlanningRoleDoesNotRetryAfterUncertainTermination)
	_ = register("planning-runtime-prepare-restarts-stopped-container", runPlanningRuntimePrepareRestartsStoppedContainer)
	_ = register("planning-runtime-prepare-refuses-unstable-container", runPlanningRuntimePrepareRefusesUnstableContainer)
	_ = register("planning-role-converges-stale-running-state-after-interrupted-call", runPlanningRoleConvergesStaleRunningStateAfterInterruptedCall)
	_ = register("planning-role-recovers-after-stop-state-persist-failed", runPlanningRoleRecoversAfterStopStatePersistFailed)
	_ = register("planning-role-converges-after-server-restart", runPlanningRoleConvergesAfterServerRestart)
	_ = register("planning-role-refuses-recovery-on-ownership-mismatch", runPlanningRoleRefusesRecoveryOnOwnershipMismatch)
	_ = register("planning-role-refuses-recovery-on-unconfirmed-remote-state", runPlanningRoleRefusesRecoveryOnUnconfirmedRemoteState)
	_ = register("planning-role-recovery-holds-session-coordinator", runPlanningRoleRecoveryHoldsSessionCoordinator)

	// planning_runtime_fixture_test.go

	// planning_runtime_test.go

	_ = registerSerial("planning-runtime-inspect-reports-present-resources", runPlanningRuntimeInspectReportsPresentResources)
	_ = registerSerial("planning-runtime-inspect-reports-absent-resources", runPlanningRuntimeInspectReportsAbsentResources)
	_ = registerSerial("planning-runtime-inspect-reports-unconfirmed-resources", runPlanningRuntimeInspectReportsUnconfirmedResources)
	_ = registerSerial("planning-runtime-inspect-reports-ownership-mismatch", runPlanningRuntimeInspectReportsOwnershipMismatch)
	_ = registerSerial("planning-runtime-inspect-reports-commit-mismatch", runPlanningRuntimeInspectReportsCommitMismatch)
	_ = registerSerial("planning-runtime-inspect-without-binding-does-not-touch-remote", runPlanningRuntimeInspectWithoutBindingDoesNotTouchRemote)
	_ = registerSerial("planning-runtime-cleanup-preparation-removes-stopped-container", runPlanningRuntimeCleanupPreparationRemovesStoppedContainer)
	_ = registerSerial("planning-runtime-cleanup-preparation-is-idempotent-when-absent", runPlanningRuntimeCleanupPreparationIsIdempotentWhenAbsent)
	_ = registerSerial("planning-runtime-cleanup-preparation-refuses-running-container", runPlanningRuntimeCleanupPreparationRefusesRunningContainer)
	_ = registerSerial("planning-runtime-cleanup-preparation-refuses-unconfirmed-state", runPlanningRuntimeCleanupPreparationRefusesUnconfirmedState)
	_ = registerSerial("planning-runtime-cleanup-preparation-refuses-foreign-ownership", runPlanningRuntimeCleanupPreparationRefusesForeignOwnership)
	_ = registerSerial("planning-runtime-cleanup-preparation-keeps-scene-on-uncertain-removal", runPlanningRuntimeCleanupPreparationKeepsSceneOnUncertainRemoval)
	_ = registerSerial("planning-runtime-cleanup-preparation-requires-confirmation-after-removal", runPlanningRuntimeCleanupPreparationRequiresConfirmationAfterRemoval)
	_ = registerSerial("planning-runtime-recover-snapshot-persists-commit", runPlanningRuntimeRecoverSnapshotPersistsCommit)
	_ = registerSerial("planning-runtime-recover-snapshot-is-idempotent-with-same-commit", runPlanningRuntimeRecoverSnapshotIsIdempotentWithSameCommit)
	_ = registerSerial("planning-runtime-recover-snapshot-refuses-different-commit", runPlanningRuntimeRecoverSnapshotRefusesDifferentCommit)
	_ = registerSerial("planning-runtime-recover-snapshot-refuses-present-prep-container", runPlanningRuntimeRecoverSnapshotRefusesPresentPrepContainer)
	_ = registerSerial("planning-runtime-recover-snapshot-refuses-incomplete-snapshot", runPlanningRuntimeRecoverSnapshotRefusesIncompleteSnapshot)
	_ = registerSerial("planning-runtime-recover-snapshot-keeps-latest-binding-on-conflict", runPlanningRuntimeRecoverSnapshotKeepsLatestBindingOnConflict)
	_ = registerSerial("planning-runtime-discard-incomplete-snapshot-removes-volume", runPlanningRuntimeDiscardIncompleteSnapshotRemovesVolume)
	_ = registerSerial("planning-runtime-discard-incomplete-snapshot-refuses-bound-snapshot", runPlanningRuntimeDiscardIncompleteSnapshotRefusesBoundSnapshot)
	_ = registerSerial("planning-runtime-discard-incomplete-snapshot-refuses-prep-container", runPlanningRuntimeDiscardIncompleteSnapshotRefusesPrepContainer)
	_ = registerSerial("planning-runtime-discard-incomplete-snapshot-refuses-ownership-mismatch", runPlanningRuntimeDiscardIncompleteSnapshotRefusesOwnershipMismatch)
	_ = registerSerial("planning-runtime-discard-incomplete-snapshot-keeps-binding-on-uncertain-removal", runPlanningRuntimeDiscardIncompleteSnapshotKeepsBindingOnUncertainRemoval)
	_ = registerSerial("planning-runtime-discard-incomplete-snapshot-requires-confirmation", runPlanningRuntimeDiscardIncompleteSnapshotRequiresConfirmation)
	_ = registerSerial("planning-runtime-release-stops-when-container-removal-uncertain", runPlanningRuntimeReleaseStopsWhenContainerRemovalUncertain)
	_ = registerSerial("planning-runtime-release-keeps-binding-when-snapshot-removal-uncertain", runPlanningRuntimeReleaseKeepsBindingWhenSnapshotRemovalUncertain)
	_ = registerSerial("planning-runtime-release-completes-when-resources-confirmed-absent", runPlanningRuntimeReleaseCompletesWhenResourcesConfirmedAbsent)
	_ = registerSerial("planning-runtime-release-deletes-owned-resources", runPlanningRuntimeReleaseDeletesOwnedResources)
	_ = registerSerial("planning-runtime-release-keeps-binding-on-ownership-mismatch", runPlanningRuntimeReleaseKeepsBindingOnOwnershipMismatch)
	_ = registerSerial("planning-runtime-release-keeps-binding-on-container-inspection-uncertain", runPlanningRuntimeReleaseKeepsBindingOnContainerInspectionUncertain)
	_ = registerSerial("planning-runtime-release-keeps-latest-binding-on-conflict", runPlanningRuntimeReleaseKeepsLatestBindingOnConflict)
	_ = registerSerial("planning-runtime-operations-serialize-on-same-session", runPlanningRuntimeOperationsSerializeOnSameSession)
	_ = registerSerial("planning-runtime-operations-run-in-parallel-across-sessions", runPlanningRuntimeOperationsRunInParallelAcrossSessions)
	_ = registerSerial("planning-runtime-queued-operations-skip-remote-on-cancellation", runPlanningRuntimeQueuedOperationsSkipRemoteOnCancellation)
	_ = registerSerial("planning-runtime-inspect-serializes-with-run-role", runPlanningRuntimeInspectSerializesWithRunRole)

	// planning_session_test.go

	_ = register("planning-session-service-create-and-query", runPlanningSessionServiceCreateAndQuery)
	_ = register("planning-session-service-submit-message", runPlanningSessionServiceSubmitMessage)
	_ = register("planning-session-service-complete-analysis", runPlanningSessionServiceCompleteAnalysis)
	_ = register("planning-session-service-fail-and-retry", runPlanningSessionServiceFailAndRetry)
	_ = register("planning-session-service-confirm-and-abandon", runPlanningSessionServiceConfirmAndAbandon)

	// project_test.go

	_ = register("project-service-create-and-get", runProjectServiceCreateAndGet)
	_ = register("project-service-rejects-duplicate-i-d", runProjectServiceRejectsDuplicateID)
	_ = register("project-service-rejects-invalid-input", runProjectServiceRejectsInvalidInput)
	_ = register("project-service-get-not-found", runProjectServiceGetNotFound)
	_ = register("project-service-update-replaces-all-fields", runProjectServiceUpdateReplacesAllFields)
	_ = register("project-service-update-not-found", runProjectServiceUpdateNotFound)
	_ = register("project-service-list-omits-deleted", runProjectServiceListOmitsDeleted)
	_ = register("task-creation-requires-existing-project", runTaskCreationRequiresExistingProject)
	_ = register("project-service-default-host-optional", runProjectServiceDefaultHostOptional)
	_ = register("project-service-default-host-create-and-read", runProjectServiceDefaultHostCreateAndRead)
	_ = register("project-service-default-host-update", runProjectServiceDefaultHostUpdate)
	_ = register("project-service-default-host-cleared-to-null", runProjectServiceDefaultHostClearedToNull)
	_ = register("project-service-default-host-rejects-unknown-or-deleted", runProjectServiceDefaultHostRejectsUnknownOrDeleted)
	_ = register("project-service-default-host-keeps-deleted-reference", runProjectServiceDefaultHostKeepsDeletedReference)

	// requirement_confirm_test.go

	_ = register("confirm-creates-single-requirement-and-tasks", runConfirmCreatesSingleRequirementAndTasks)
	_ = register("confirm-keeps-selected-old-revision", runConfirmKeepsSelectedOldRevision)
	_ = register("confirm-rejects-other-session-revision", runConfirmRejectsOtherSessionRevision)
	_ = register("confirm-rejects-unexpected-session-state", runConfirmRejectsUnexpectedSessionState)
	_ = register("confirm-rejects-missing-session", runConfirmRejectsMissingSession)
	_ = register("confirm-rejects-invalid-plan-snapshot", runConfirmRejectsInvalidPlanSnapshot)
	_ = register("confirm-rejects-unparseable-revision-snapshot", runConfirmRejectsUnparseableRevisionSnapshot)
	_ = register("confirm-requires-usable-host", runConfirmRequiresUsableHost)
	_ = register("confirm-rejects-deleted-project", runConfirmRejectsDeletedProject)
	_ = register("confirm-rejects-duplicate-confirmation", runConfirmRejectsDuplicateConfirmation)
	_ = register("confirm-concurrent-only-one-succeeds", runConfirmConcurrentOnlyOneSucceeds)
	_ = register("confirm-isolates-projects", runConfirmIsolatesProjects)
	_ = register("confirm-rolls-back-on-write-failure", runConfirmRollsBackOnWriteFailure)
	_ = register("requirement-service-queries", runRequirementServiceQueries)

	// requirement_delete_code_save_test.go

	_ = register("delete-code-save-state-matrix", runDeleteCodeSaveStateMatrix)
	_ = register("delete-keeps-code-save-record-on-rejection", runDeleteKeepsCodeSaveRecordOnRejection)

	// requirement_delete_race_test.go

	_ = register("delete-and-cancel-concurrently-keep-facts", runDeleteAndCancelConcurrentlyKeepFacts)
	_ = register("delete-releases-earlier-grants-when-later-task-is-held", runDeleteReleasesEarlierGrantsWhenLaterTaskIsHeld)
	_ = register("delete-with-cancelled-context-keeps-facts", runDeleteWithCancelledContextKeepsFacts)
	_ = register("delete-and-project-stop-keep-facts", runDeleteAndProjectStopKeepFacts)
	_ = register("release-on-stop-keeps-lease-of-running-task", runReleaseOnStopKeepsLeaseOfRunningTask)

	// requirement_delete_test.go

	_ = register("delete-requires-coordination", runDeleteRequiresCoordination)
	_ = register("delete-soft-deletes-requirement-and-tasks", runDeleteSoftDeletesRequirementAndTasks)
	_ = register("delete-rejects-unsettled-task-statuses", runDeleteRejectsUnsettledTaskStatuses)
	_ = register("delete-rejects-unknown-task-status", runDeleteRejectsUnknownTaskStatus)
	_ = register("delete-rejects-active-workflow-run", runDeleteRejectsActiveWorkflowRun)
	_ = register("delete-rejects-occupied-step", runDeleteRejectsOccupiedStep)
	_ = register("delete-rejects-unresolved-code-save", runDeleteRejectsUnresolvedCodeSave)
	_ = register("delete-rejects-held-execution-grant", runDeleteRejectsHeldExecutionGrant)
	_ = register("delete-releases-grants-after-success", runDeleteReleasesGrantsAfterSuccess)
	_ = register("delete-holds-grants-during-transaction", runDeleteHoldsGrantsDuringTransaction)
	_ = register("concurrent-requirement-delete-only-one-succeeds", runConcurrentRequirementDeleteOnlyOneSucceeds)
	_ = register("delete-keeps-other-requirements-running", runDeleteKeepsOtherRequirementsRunning)
	_ = register("delete-removes-tasks-from-project-facts", runDeleteRemovesTasksFromProjectFacts)
	_ = register("delete-keeps-execution-history", runDeleteKeepsExecutionHistory)

	// role_service_test.go

	_ = register("role-service-update-persists-editable-fields", runRoleServiceUpdatePersistsEditableFields)
	_ = register("role-service-rejects-incomplete-config", runRoleServiceRejectsIncompleteConfig)
	_ = register("role-service-update-tools-policy", runRoleServiceUpdateToolsPolicy)

	// roles_test.go

	_ = register("role-seeds-provide-four-fixed-roles", runRoleSeedsProvideFourFixedRoles)

	// sessions_test.go

	_ = register("start-records-running-session", runStartRecordsRunningSession)
	_ = register("finish-writes-artifacts-and-archive", runFinishWritesArtifactsAndArchive)
	_ = register("finish-keeps-archive-failure-independent", runFinishKeepsArchiveFailureIndependent)
	_ = register("finish-rejects-terminal-session", runFinishRejectsTerminalSession)
	_ = register("fail-converges-running-session", runFailConvergesRunningSession)
	_ = register("session-root-follows-database", runSessionRootFollowsDatabase)

	// step_executor_fixture_test.go

	// step_executor_test.go

	_ = register("execute-step-success", runExecuteStepSuccess)
	_ = register("execute-step-failure-keeps-history", runExecuteStepFailureKeepsHistory)
	_ = register("execute-step-timeout", runExecuteStepTimeout)
	_ = register("execute-step-cancelled", runExecuteStepCancelled)
	_ = register("execute-step-rejects-queued-task", runExecuteStepRejectsQueuedTask)
	_ = register("execute-step-planned-task", runExecuteStepPlannedTask)
	_ = register("execute-step-requires-container", runExecuteStepRequiresContainer)
	_ = register("execute-step-requires-validated-host", runExecuteStepRequiresValidatedHost)
	_ = register("executor-rejects-run-before-start", runExecutorRejectsRunBeforeStart)
	_ = register("executor-rejects-run-after-stop", runExecutorRejectsRunAfterStop)
	_ = register("executor-stop-cancels-in-flight-run", runExecutorStopCancelsInFlightRun)
	_ = register("executor-name", runExecutorName)
	_ = register("execute-step-rejects-non-agent-step", runExecuteStepRejectsNonAgentStep)
	_ = register("execute-step-attempt-creation-failure", runExecuteStepAttemptCreationFailure)
	_ = register("execute-step-finalization-failure-is-reported", runExecuteStepFinalizationFailureIsReported)
	_ = register("execute-step-appends-events", runExecuteStepAppendsEvents)
	_ = register("execute-step-concurrent-requests", runExecuteStepConcurrentRequests)
	_ = register("execute-step-rejects-occupied-step", runExecuteStepRejectsOccupiedStep)
	_ = register("execute-step-late-result-does-not-override-terminal", runExecuteStepLateResultDoesNotOverrideTerminal)

	// step_sequence_test.go

	_ = register("execute-steps-runs-in-plan-order", runExecuteStepsRunsInPlanOrder)
	_ = register("execute-steps-stops-on-failure", runExecuteStepsStopsOnFailure)
	_ = register("execute-steps-stops-on-cancellation", runExecuteStepsStopsOnCancellation)
	_ = register("execute-steps-rejects-unfinished-prerequisite", runExecuteStepsRejectsUnfinishedPrerequisite)
	_ = register("execute-step-allows-completed-prerequisite", runExecuteStepAllowsCompletedPrerequisite)
	_ = register("execute-steps-rejects-concurrent-sequence", runExecuteStepsRejectsConcurrentSequence)
	_ = register("execute-steps-records-git-state", runExecuteStepsRecordsGitState)
	_ = register("execute-step-git-state-failure-is-recorded", runExecuteStepGitStateFailureIsRecorded)
	_ = register("execute-step-without-git-capability", runExecuteStepWithoutGitCapability)
	_ = register("execute-steps-requires-container", runExecuteStepsRequiresContainer)
	_ = register("execute-steps-stops-on-uncertain-step", runExecuteStepsStopsOnUncertainStep)

	// step_workflow_repair_fixture_test.go

	// step_workflow_repair_test.go

	_ = register("repair-loop-retries-until-verification-passes", runRepairLoopRetriesUntilVerificationPasses)
	_ = register("repair-loop-stops-at-attempt-limit", runRepairLoopStopsAtAttemptLimit)
	_ = register("repair-loop-explicit-limit-three", runRepairLoopExplicitLimitThree)
	_ = register("repair-loop-disabled-without-retry-flag", runRepairLoopDisabledWithoutRetryFlag)
	_ = register("repair-loop-stops-on-verification-inconclusive", runRepairLoopStopsOnVerificationInconclusive)
	_ = register("repair-loop-stops-on-infrastructure-error", runRepairLoopStopsOnInfrastructureError)
	_ = register("repair-loop-stops-when-feedback-not-persisted", runRepairLoopStopsWhenFeedbackNotPersisted)
	_ = register("repair-loop-stops-on-developer-failure", runRepairLoopStopsOnDeveloperFailure)
	_ = register("repair-loop-stops-on-developer-timeout", runRepairLoopStopsOnDeveloperTimeout)
	_ = register("repair-loop-stops-on-planning-step", runRepairLoopStopsOnPlanningStep)
	_ = register("repair-loop-stops-before-next-round-when-cancelled", runRepairLoopStopsBeforeNextRoundWhenCancelled)
	_ = register("repair-loop-cancel-between-rounds", runRepairLoopCancelBetweenRounds)
	_ = register("repair-loop-history-and-event-traceability", runRepairLoopHistoryAndEventTraceability)
	_ = register("repair-loop-releases-occupation-after-limit", runRepairLoopReleasesOccupationAfterLimit)

	// step_workflow_test.go

	_ = register("workflow-validates-after-developer-success", runWorkflowValidatesAfterDeveloperSuccess)
	_ = register("workflow-skips-validation-after-developer-failure", runWorkflowSkipsValidationAfterDeveloperFailure)
	_ = register("workflow-skips-validation-after-developer-cancel", runWorkflowSkipsValidationAfterDeveloperCancel)
	_ = register("workflow-required-check-failure-blocks-completion", runWorkflowRequiredCheckFailureBlocksCompletion)
	_ = register("workflow-infrastructure-error-blocks-completion", runWorkflowInfrastructureErrorBlocksCompletion)
	_ = register("workflow-invalid-verification-config-rejected", runWorkflowInvalidVerificationConfigRejected)
	_ = register("workflow-container-unavailable-rejected", runWorkflowContainerUnavailableRejected)
	_ = register("workflow-persists-failure-feedback-for-retry", runWorkflowPersistsFailureFeedbackForRetry)
	_ = register("workflow-concurrent-requests-rejected", runWorkflowConcurrentRequestsRejected)
	_ = register("workflow-developer-finalization-failure-skips-validation", runWorkflowDeveloperFinalizationFailureSkipsValidation)
	_ = register("workflow-stop-cancels-in-flight-run", runWorkflowStopCancelsInFlightRun)

	// task_launch_cancellation_test.go

	_ = register("launch-settles-cancelling-task-without-active-run", runLaunchSettlesCancellingTaskWithoutActiveRun)
	_ = register("launch-keeps-cancelling-task-with-active-run", runLaunchKeepsCancellingTaskWithActiveRun)
	_ = register("launch-settles-cancelling-task-as-recovery-when-cancel-missing", runLaunchSettlesCancellingTaskAsRecoveryWhenCancelMissing)

	// task_launch_test.go

	_ = register("launch-runs-container-then-plan-then-workflow", runLaunchRunsContainerThenPlanThenWorkflow)
	_ = register("launch-skips-container-when-usable-container-exists", runLaunchSkipsContainerWhenUsableContainerExists)
	_ = register("launch-skips-plan-when-steps-persisted", runLaunchSkipsPlanWhenStepsPersisted)
	_ = register("launch-runs-container-then-workspace-then-plan-then-workflow", runLaunchRunsContainerThenWorkspaceThenPlanThenWorkflow)
	_ = register("launch-skips-workspace-when-already-ready", runLaunchSkipsWorkspaceWhenAlreadyReady)
	_ = register("launch-stops-after-workspace-failure", runLaunchStopsAfterWorkspaceFailure)
	_ = register("launch-without-workspace-port-returns-prerequisite", runLaunchWithoutWorkspacePortReturnsPrerequisite)
	_ = register("launch-recovers-workspace-on-start", runLaunchRecoversWorkspaceOnStart)
	_ = register("launch-stops-after-container-failure", runLaunchStopsAfterContainerFailure)
	_ = register("launch-stops-after-plan-failure", runLaunchStopsAfterPlanFailure)
	_ = register("launch-rejects-active-workflow-run", runLaunchRejectsActiveWorkflowRun)
	_ = register("launch-rejects-unknown-task", runLaunchRejectsUnknownTask)
	_ = register("launch-rejects-stopped-service", runLaunchRejectsStoppedService)
	_ = register("launch-rejects-negative-timeout", runLaunchRejectsNegativeTimeout)
	_ = register("recover-step-occupancy-converges-idle-occupancy", runRecoverStepOccupancyConvergesIdleOccupancy)
	_ = register("recover-step-occupancy-skips-active-run", runRecoverStepOccupancySkipsActiveRun)

	// task_management_test.go

	_ = register("create-task-from-api-starts-queued", runCreateTaskFromAPIStartsQueued)
	_ = register("create-task-from-api-validates-input", runCreateTaskFromAPIValidatesInput)
	_ = register("create-task-inherits-project-config", runCreateTaskInheritsProjectConfig)
	_ = register("create-task-overrides-project-config", runCreateTaskOverridesProjectConfig)
	_ = register("create-task-from-api-rejects-unknown-host", runCreateTaskFromAPIRejectsUnknownHost)
	_ = register("create-task-from-api-is-idempotent", runCreateTaskFromAPIIsIdempotent)
	_ = register("list-tasks-for-api-paginates-and-filters", runListTasksForAPIPaginatesAndFilters)
	_ = register("list-tasks-for-api-rejects-oversized-page", runListTasksForAPIRejectsOversizedPage)
	_ = register("list-tasks-for-api-rejects-unknown-status", runListTasksForAPIRejectsUnknownStatus)
	_ = register("get-task-for-api-returns-not-found", runGetTaskForAPIReturnsNotFound)
	_ = register("cancel-task-for-api-cancels-queued-immediately", runCancelTaskForAPICancelsQueuedImmediately)
	_ = register("cancel-task-for-api-marks-running-as-cancelling", runCancelTaskForAPIMarksRunningAsCancelling)
	_ = register("cancel-task-for-api-is-idempotent", runCancelTaskForAPIIsIdempotent)
	_ = register("cancel-task-for-api-rejects-finished-task", runCancelTaskForAPIRejectsFinishedTask)
	_ = register("delete-task-rejects-running-task", runDeleteTaskRejectsRunningTask)
	_ = register("delete-task-rejects-cancelling-task", runDeleteTaskRejectsCancellingTask)
	_ = register("delete-task-accepts-finished-task", runDeleteTaskAcceptsFinishedTask)
	_ = register("delete-task-after-cancel-succeeds", runDeleteTaskAfterCancelSucceeds)
	_ = register("delete-task-rejects-repeated-delete", runDeleteTaskRejectsRepeatedDelete)
	_ = register("concurrent-cancel-only-one-transition-wins", runConcurrentCancelOnlyOneTransitionWins)
	_ = register("concurrent-delete-only-one-succeeds", runConcurrentDeleteOnlyOneSucceeds)

	// task_workflow_repair_test.go

	_ = register("workflow-consumes-repair-stop-class", runWorkflowConsumesRepairStopClass)

	// task_workflow_test.go

	_ = register("workflow-executes-steps-in-order", runWorkflowExecutesStepsInOrder)
	_ = register("workflow-stops-when-verification-fails", runWorkflowStopsWhenVerificationFails)
	_ = register("workflow-stops-when-developer-fails", runWorkflowStopsWhenDeveloperFails)
	_ = register("workflow-requires-recovery-on-infrastructure-error", runWorkflowRequiresRecoveryOnInfrastructureError)
	_ = register("workflow-does-not-report-success-without-code-saver", runWorkflowDoesNotReportSuccessWithoutCodeSaver)
	_ = register("workflow-rejects-missing-plan", runWorkflowRejectsMissingPlan)
	_ = register("workflow-rejects-unready-container", runWorkflowRejectsUnreadyContainer)
	_ = register("workflow-rejects-unvalidated-host", runWorkflowRejectsUnvalidatedHost)
	_ = register("workflow-rejects-concurrent-start", runWorkflowRejectsConcurrentStart)
	_ = register("workflow-progress-is-persisted", runWorkflowProgressIsPersisted)
	_ = register("workflow-progress-write-failure-stops-advance", runWorkflowProgressWriteFailureStopsAdvance)
	_ = register("workflow-cancel-stops-advance", runWorkflowCancelStopsAdvance)
	_ = register("workflow-cancel-is-idempotent", runWorkflowCancelIsIdempotent)
	_ = register("workflow-resume-skips-completed-steps", runWorkflowResumeSkipsCompletedSteps)
	_ = register("workflow-resume-rejects-recovery-required-step", runWorkflowResumeRejectsRecoveryRequiredStep)
	_ = register("workflow-resume-rejects-plan-change", runWorkflowResumeRejectsPlanChange)
	_ = register("workflow-recovery-on-server-restart", runWorkflowRecoveryOnServerRestart)
	_ = register("workflow-recovery-cancels-requested", runWorkflowRecoveryCancelsRequested)
	_ = register("workflow-records-events", runWorkflowRecordsEvents)
	_ = register("workflow-closeout-requires-session-archive", runWorkflowCloseoutRequiresSessionArchive)
	_ = register("workflow-closeout-requires-recovery-on-unknown-commit", runWorkflowCloseoutRequiresRecoveryOnUnknownCommit)
	_ = register("workflow-closeout-requires-recovery-on-unconfirmed-record", runWorkflowCloseoutRequiresRecoveryOnUnconfirmedRecord)

	// task_workspace_test.go

	_ = register("initialize-task-workspace-succeeds", runInitializeTaskWorkspaceSucceeds)
	_ = register("initialize-task-workspace-skips-same-container", runInitializeTaskWorkspaceSkipsSameContainer)
	_ = register("initialize-task-workspace-reinitializes-after-container-change", runInitializeTaskWorkspaceReinitializesAfterContainerChange)
	_ = register("initialize-task-workspace-rejects-uncertain-without-replay", runInitializeTaskWorkspaceRejectsUncertainWithoutReplay)
	_ = register("initialize-task-workspace-requires-running-container", runInitializeTaskWorkspaceRequiresRunningContainer)
	_ = register("initialize-task-workspace-rejects-detached-container", runInitializeTaskWorkspaceRejectsDetachedContainer)
	_ = register("initialize-task-workspace-rejects-removed-container", runInitializeTaskWorkspaceRejectsRemovedContainer)
	_ = register("initialize-task-workspace-requires-container", runInitializeTaskWorkspaceRequiresContainer)
	_ = register("initialize-task-workspace-records-failure-stage", runInitializeTaskWorkspaceRecordsFailureStage)
	_ = register("initialize-task-workspace-maps-timeout", runInitializeTaskWorkspaceMapsTimeout)
	_ = register("initialize-task-workspace-maps-cancel", runInitializeTaskWorkspaceMapsCancel)
	_ = register("initialize-task-workspace-rejects-invalid-input", runInitializeTaskWorkspaceRejectsInvalidInput)
	_ = register("initialize-task-workspace-rejects-unknown-task", runInitializeTaskWorkspaceRejectsUnknownTask)
	_ = register("initialize-task-workspace-keeps-work-dir-under-workspace", runInitializeTaskWorkspaceKeepsWorkDirUnderWorkspace)
	_ = register("initialize-task-workspace-persists-event", runInitializeTaskWorkspacePersistsEvent)
	_ = register("recover-interrupted-converges-to-uncertain", runRecoverInterruptedConvergesToUncertain)
	_ = register("recover-interrupted-keeps-terminal-records", runRecoverInterruptedKeepsTerminalRecords)

	// validation_review_test.go

	_ = register("review-passed-with-deterministic-passed", runReviewPassedWithDeterministicPassed)
	_ = register("review-cannot-override-deterministic-failure", runReviewCannotOverrideDeterministicFailure)
	_ = register("review-failure-tightens-passed", runReviewFailureTightensPassed)
	_ = register("review-needs-human-review-blocks-completion", runReviewNeedsHumanReviewBlocksCompletion)
	_ = register("review-unparseable-output-blocks-completion", runReviewUnparseableOutputBlocksCompletion)
	_ = register("review-call-failure-blocks-completion", runReviewCallFailureBlocksCompletion)
	_ = register("review-timeout-blocks-completion", runReviewTimeoutBlocksCompletion)
	_ = register("review-persistence-failure-is-not-success", runReviewPersistenceFailureIsNotSuccess)
	_ = register("review-not-configured-blocks-completion", runReviewNotConfiguredBlocksCompletion)
	_ = register("review-optional-when-task-has-no-validator-role", runReviewOptionalWhenTaskHasNoValidatorRole)
	_ = register("review-history-is-append-only", runReviewHistoryIsAppendOnly)
	_ = register("review-session-is-not-reused", runReviewSessionIsNotReused)
	_ = register("review-prompt-carries-deterministic-evidence", runReviewPromptCarriesDeterministicEvidence)
	_ = register("review-required-but-unavailable-blocks-completion", runReviewRequiredButUnavailableBlocksCompletion)

	// validation_test.go

	_ = register("validation-all-required-passed", runValidationAllRequiredPassed)
	_ = register("validation-required-check-fails", runValidationRequiredCheckFails)
	_ = register("validation-optional-check-fails", runValidationOptionalCheckFails)
	_ = register("validation-check-timed-out", runValidationCheckTimedOut)
	_ = register("validation-check-start-failed", runValidationCheckStartFailed)
	_ = register("validation-cancelled-by-caller", runValidationCancelledByCaller)
	_ = register("validation-requires-running-container", runValidationRequiresRunningContainer)
	_ = register("validation-requires-container-binding", runValidationRequiresContainerBinding)
	_ = register("validation-rejects-unfinished-attempt", runValidationRejectsUnfinishedAttempt)
	_ = register("validation-rejects-foreign-scope", runValidationRejectsForeignScope)
	_ = register("validation-duplicate-request-conflicts", runValidationDuplicateRequestConflicts)
	_ = register("validation-records-partial-results", runValidationRecordsPartialResults)
	_ = register("validation-rejects-missing-required-check", runValidationRejectsMissingRequiredCheck)
	_ = register("validation-check-override-cannot-weaken-gate", runValidationCheckOverrideCannotWeakenGate)
	_ = register("validation-rejects-unsafe-work-dir", runValidationRejectsUnsafeWorkDir)
	_ = register("validation-stopped-service-rejects-new-runs", runValidationStoppedServiceRejectsNewRuns)
	_ = register("validation-persist-failure-is-not-success", runValidationPersistFailureIsNotSuccess)
	_ = register("validation-overall-timeout", runValidationOverallTimeout)
	_ = register("validation-service-stop-cancels-in-flight", runValidationServiceStopCancelsInFlight)
	_ = register("validation-archives-outputs", runValidationArchivesOutputs)
)
