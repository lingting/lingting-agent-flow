// 本文件登记 tests/unit/domain 的 249 条用例，顺序与源文件一致，便于按包核对迁移数量。
//
// 249 条源用例都带 t.Parallel()，因此全部按可并行条目登记；每个源文件对应一个
// cases_*.go，注释里保留源文件名。
package domain

var (
	// agent_assessment_test.go

	_ = register("parse-agent-assessment-accepts-structured-output", runParseAgentAssessmentAcceptsStructuredOutput)
	_ = register("parse-agent-assessment-rejects-unusable-output", runParseAgentAssessmentRejectsUnusableOutput)
	_ = register("decide-validation-review-cannot-override-required-failure", runDecideValidationReviewCannotOverrideRequiredFailure)
	_ = register("decide-validation-requires-usable-review", runDecideValidationRequiresUsableReview)
	_ = register("decide-validation-passed-requires-assessment", runDecideValidationPassedRequiresAssessment)
	_ = register("decide-validation-review-tightens", runDecideValidationReviewTightens)
	_ = register("decide-validation-review-needs-human-review", runDecideValidationReviewNeedsHumanReview)
	_ = register("decide-validation-review-passed-with-assessment", runDecideValidationReviewPassedWithAssessment)
	_ = register("decide-validation-unfinished-run-ignores-review", runDecideValidationUnfinishedRunIgnoresReview)
	_ = register("review-status-validate", runReviewStatusValidate)

	// code_fetch_test.go

	_ = register("new-code-fetch-record-defaults", runNewCodeFetchRecordDefaults)
	_ = register("code-fetch-rejects-invalid-task-id", runCodeFetchRejectsInvalidTaskID)
	_ = register("code-fetch-rejects-invalid-work-dir", runCodeFetchRejectsInvalidWorkDir)
	_ = register("code-fetch-rejects-unknown-source-type", runCodeFetchRejectsUnknownSourceType)
	_ = register("code-fetch-succeeded-requires-commit", runCodeFetchSucceededRequiresCommit)
	_ = register("code-fetch-succeeded-requires-branch-source", runCodeFetchSucceededRequiresBranchSource)
	_ = register("code-fetch-terminal-cannot-be-reopened", runCodeFetchTerminalCannotBeReopened)
	_ = register("code-fetch-skipped-counts-as-ready", runCodeFetchSkippedCountsAsReady)
	_ = register("code-fetch-failed-requires-summary", runCodeFetchFailedRequiresSummary)
	_ = register("code-fetch-failure-summary-truncated", runCodeFetchFailureSummaryTruncated)
	_ = register("code-fetch-rejects-base-branch-equal-to-task-branch", runCodeFetchRejectsBaseBranchEqualToTaskBranch)
	_ = register("task-branch-source-validation", runTaskBranchSourceValidation)
	_ = register("code-fetch-status-terminal", runCodeFetchStatusTerminal)

	// code_save_test.go

	_ = register("requires-manual-recovery", runRequiresManualRecovery)

	// container_disposal_test.go

	_ = register("decide-container-disposal-allows-managed-container", runDecideContainerDisposalAllowsManagedContainer)
	_ = register("decide-container-disposal-allows-history-generation", runDecideContainerDisposalAllowsHistoryGeneration)
	_ = register("decide-container-disposal-rejects-cross-task", runDecideContainerDisposalRejectsCrossTask)
	_ = register("decide-container-disposal-rejects-unassigned-container", runDecideContainerDisposalRejectsUnassignedContainer)
	_ = register("decide-container-disposal-rejects-host-mismatch", runDecideContainerDisposalRejectsHostMismatch)
	_ = register("decide-container-disposal-rejects-detached-container", runDecideContainerDisposalRejectsDetachedContainer)
	_ = register("decide-container-disposal-rejects-removed-detach", runDecideContainerDisposalRejectsRemovedDetach)
	_ = register("decide-container-disposal-rejects-contradictory-detached-record", runDecideContainerDisposalRejectsContradictoryDetachedRecord)
	_ = register("decide-container-disposal-rejects-invalid-action", runDecideContainerDisposalRejectsInvalidAction)

	// container_ownership_test.go

	_ = register("decide-container-ownership-accepts-owned-container", runDecideContainerOwnershipAcceptsOwnedContainer)
	_ = register("decide-container-ownership-rejects-foreign-task", runDecideContainerOwnershipRejectsForeignTask)
	_ = register("decide-container-ownership-rejects-unassigned-container", runDecideContainerOwnershipRejectsUnassignedContainer)
	_ = register("decide-container-ownership-rejects-mismatched-record", runDecideContainerOwnershipRejectsMismatchedRecord)
	_ = register("decide-container-ownership-rejects-missing-reference", runDecideContainerOwnershipRejectsMissingReference)
	_ = register("decide-container-ownership-rejects-host-mismatch", runDecideContainerOwnershipRejectsHostMismatch)
	_ = register("decide-container-ownership-rejects-detached-and-removed", runDecideContainerOwnershipRejectsDetachedAndRemoved)
	_ = register("decide-container-ownership-rejects-missing-docker-id", runDecideContainerOwnershipRejectsMissingDockerID)

	// execution_host_auth_test.go

	_ = register("new-execution-host-requires-private-key", runNewExecutionHostRequiresPrivateKey)
	_ = register("new-execution-host-with-private-key-auth", runNewExecutionHostWithPrivateKeyAuth)
	_ = register("new-execution-host-rejects-private-key-without-path", runNewExecutionHostRejectsPrivateKeyWithoutPath)
	_ = register("auth-method-validate", runAuthMethodValidate)
	_ = register("parse-auth-method", runParseAuthMethod)
	_ = register("execution-host-rejects-non-sha256-fingerprint", runExecutionHostRejectsNonSHA256Fingerprint)
	_ = register("record-connect-result", runRecordConnectResult)
	_ = register("record-connect-result-rejects-free-text", runRecordConnectResultRejectsFreeText)
	_ = register("trust-host-key", runTrustHostKey)

	// execution_state_test.go

	_ = register("attempt-legal-transitions", runAttemptLegalTransitions)
	_ = register("attempt-terminal-cannot-be-reopened", runAttemptTerminalCannotBeReopened)
	_ = register("attempt-failed-requires-summary", runAttemptFailedRequiresSummary)
	_ = register("attempt-cancelled-flag-is-set", runAttemptCancelledFlagIsSet)
	_ = register("attempt-unknown-can-be-resolved", runAttemptUnknownCanBeResolved)
	_ = register("step-legal-transitions", runStepLegalTransitions)
	_ = register("step-pending-can-be-skipped-or-cancelled", runStepPendingCanBeSkippedOrCancelled)
	_ = register("step-terminal-cannot-return-to-running", runStepTerminalCannotReturnToRunning)
	_ = register("step-unknown-status-is-rejected-on-transition", runStepUnknownStatusIsRejectedOnTransition)
	_ = register("session-execution-and-archive-are-independent", runSessionExecutionAndArchiveAreIndependent)
	_ = register("session-failed-call-still-records-archive", runSessionFailedCallStillRecordsArchive)
	_ = register("session-archive-cannot-be-overwritten", runSessionArchiveCannotBeOverwritten)
	_ = register("session-archive-succeeded-requires-path", runSessionArchiveSucceededRequiresPath)
	_ = register("session-archive-rejects-unknown-status", runSessionArchiveRejectsUnknownStatus)
	_ = register("session-export-status-is-independent", runSessionExportStatusIsIndependent)
	_ = register("unexecuted-records-are-not-marked-successful", runUnexecutedRecordsAreNotMarkedSuccessful)

	// plan_generation_test.go

	_ = register("build-plan-accepts-valid-draft", runBuildPlanAcceptsValidDraft)
	_ = register("build-plan-rejects-invalid-drafts", runBuildPlanRejectsInvalidDrafts)
	_ = register("build-plan-rejects-wrong-role-type", runBuildPlanRejectsWrongRoleType)
	_ = register("check-plan-generation-start-accepts-allowed-statuses", runCheckPlanGenerationStartAcceptsAllowedStatuses)
	_ = register("check-plan-generation-start-rejects", runCheckPlanGenerationStartRejects)
	_ = register("decide-plan-generation-failure", runDecidePlanGenerationFailure)
	_ = register("decide-plan-generation-recovery", runDecidePlanGenerationRecovery)
	_ = register("decide-cancellation-recovery", runDecideCancellationRecovery)

	// planning_analysis_test.go

	_ = register("parse-requirement-analysis-accepts-structured-output", runParseRequirementAnalysisAcceptsStructuredOutput)
	_ = register("parse-requirement-analysis-rejects-unusable-output", runParseRequirementAnalysisRejectsUnusableOutput)
	_ = register("parse-implementation-plan-accepts-structured-output", runParseImplementationPlanAcceptsStructuredOutput)
	_ = register("parse-implementation-plan-rejects-unusable-output", runParseImplementationPlanRejectsUnusableOutput)
	_ = register("planning-snapshots-round-trip", runPlanningSnapshotsRoundTrip)
	_ = register("planning-revision-carries-snapshot-version", runPlanningRevisionCarriesSnapshotVersion)
	_ = register("planning-revision-rejects-unknown-snapshot-version", runPlanningRevisionRejectsUnknownSnapshotVersion)
	_ = register("validate-rejects-missing-snapshot-version", runValidateRejectsMissingSnapshotVersion)

	// planning_session_test.go

	_ = register("planning-session-analysis-round-trip", runPlanningSessionAnalysisRoundTrip)
	_ = register("planning-session-failed-needs-explicit-retry", runPlanningSessionFailedNeedsExplicitRetry)
	_ = register("planning-session-confirm-binds-revision", runPlanningSessionConfirmBindsRevision)
	_ = register("planning-session-adopted-revision-requires-confirmed", runPlanningSessionAdoptedRevisionRequiresConfirmed)
	_ = register("planning-session-abandon-follows-state-machine", runPlanningSessionAbandonFollowsStateMachine)
	_ = register("planning-session-terminal-states-stay-closed", runPlanningSessionTerminalStatesStayClosed)
	_ = register("planning-session-role-sessions-stay-distinct", runPlanningSessionRoleSessionsStayDistinct)

	// project_test.go

	_ = register("new-project-normalizes-input", runNewProjectNormalizesInput)
	_ = register("new-project-rejects-invalid-input", runNewProjectRejectsInvalidInput)
	_ = register("new-project-requires-language-environment", runNewProjectRequiresLanguageEnvironment)
	_ = register("project-allows-empty-environment-version", runProjectAllowsEmptyEnvironmentVersion)
	_ = register("project-mark-deleted-is-one-way", runProjectMarkDeletedIsOneWay)
	_ = register("require-project-id", runRequireProjectID)
	_ = register("project-id-pattern-limits", runProjectIDPatternLimits)
	_ = register("project-default-host-normalization", runProjectDefaultHostNormalization)

	// repair_cycle_test.go

	_ = register("decide-repair-budget-counts-only-current-cycle", runDecideRepairBudgetCountsOnlyCurrentCycle)
	_ = register("repair-attempt-consumes-budget", runRepairAttemptConsumesBudget)
	_ = register("repair-attempt-unresolved", runRepairAttemptUnresolved)
	_ = register("decide-repair-start-continues-within-budget", runDecideRepairStartContinuesWithinBudget)
	_ = register("decide-repair-start-stops-at-exhausted", runDecideRepairStartStopsAtExhausted)
	_ = register("decide-repair-start-stop-classes", runDecideRepairStartStopClasses)
	_ = register("decide-repair-start-opens-new-cycle-when-exhausted", runDecideRepairStartOpensNewCycleWhenExhausted)
	_ = register("decide-repair-start-rejects-new-cycle-with-budget-left", runDecideRepairStartRejectsNewCycleWithBudgetLeft)
	_ = register("decide-repair-start-allows-new-cycle-when-repair-disabled", runDecideRepairStartAllowsNewCycleWhenRepairDisabled)
	_ = register("decide-repair-start-rejects-new-cycle-without-history", runDecideRepairStartRejectsNewCycleWithoutHistory)
	_ = register("decide-repair-start-uses-last-cycle-budget", runDecideRepairStartUsesLastCycleBudget)
	_ = register("repair-cycle-validate-rejects-invalid", runRepairCycleValidateRejectsInvalid)
	_ = register("repair-budget-scope-validate-rejects-unknown", runRepairBudgetScopeValidateRejectsUnknown)
	_ = register("attempt-rejects-half-budget-scope", runAttemptRejectsHalfBudgetScope)

	// repair_loop_test.go

	_ = register("decide-repair-loop-defaults-to-retryable", runDecideRepairLoopDefaultsToRetryable)
	_ = register("decide-repair-loop-attempt-limit-boundary", runDecideRepairLoopAttemptLimitBoundary)
	_ = register("decide-repair-loop-zero-max-attempts-uses-default", runDecideRepairLoopZeroMaxAttemptsUsesDefault)
	_ = register("decide-repair-loop-stop-classes", runDecideRepairLoopStopClasses)
	_ = register("decide-repair-loop-completed-only-when-verification-passed", runDecideRepairLoopCompletedOnlyWhenVerificationPassed)
	_ = register("decide-repair-loop-completed-precedes-unpersisted", runDecideRepairLoopCompletedPrecedesUnpersisted)
	_ = register("decide-repair-loop-cancelled-precedes-everything", runDecideRepairLoopCancelledPrecedesEverything)
	_ = register("repair-failure-class-validate-rejects-unknown", runRepairFailureClassValidateRejectsUnknown)
	_ = register("decide-repair-closeout-mapping", runDecideRepairCloseoutMapping)
	_ = register("decide-repair-closeout-exhausted-mentions-limit", runDecideRepairCloseoutExhaustedMentionsLimit)
	_ = register("repair-closeout-attempts-limit-or", runRepairCloseoutAttemptsLimitOr)
	_ = register("retry-config-allows-verification-failure", runRetryConfigAllowsVerificationFailure)

	// requirement_confirmation_test.go

	_ = register("confirmable-tasks-accepts-single-task", runConfirmableTasksAcceptsSingleTask)
	_ = register("confirmable-tasks-derives-sequential-order", runConfirmableTasksDerivesSequentialOrder)
	_ = register("confirmable-tasks-rejects-invalid-plan", runConfirmableTasksRejectsInvalidPlan)
	_ = register("requirement-title-and-description-from-analysis", runRequirementTitleAndDescriptionFromAnalysis)
	_ = register("requirement-title-rejects-empty-summary", runRequirementTitleRejectsEmptySummary)
	_ = register("new-confirmed-requirement-requires-complete-confirmation", runNewConfirmedRequirementRequiresCompleteConfirmation)
	_ = register("new-confirmed-requirement-normalizes-project-id", runNewConfirmedRequirementNormalizesProjectID)
	_ = register("requirement-validate-rejects-incomplete-confirmation", runRequirementValidateRejectsIncompleteConfirmation)
	_ = register("requirement-rejects-confirmation-fields-without-planning-source", runRequirementRejectsConfirmationFieldsWithoutPlanningSource)
	_ = register("task-requirement-sequence", runTaskRequirementSequence)

	// requirement_deletion_test.go

	_ = register("requirement-mark-deleted-only-once", runRequirementMarkDeletedOnlyOnce)
	_ = register("requirement-mark-deleted-rejects-empty-entity", runRequirementMarkDeletedRejectsEmptyEntity)
	_ = register("requirement-deletion-allows-only-safe-statuses", runRequirementDeletionAllowsOnlySafeStatuses)
	_ = register("task-allows-requirement-deletion", runTaskAllowsRequirementDeletion)
	_ = register("task-allows-requirement-deletion-rejects-deleted-task", runTaskAllowsRequirementDeletionRejectsDeletedTask)
	_ = register("task-mark-deleted-still-rejects-queued", runTaskMarkDeletedStillRejectsQueued)
	_ = register("task-allows-requirement-deletion-keeps-fields", runTaskAllowsRequirementDeletionKeepsFields)
	_ = register("step-status-requires-manual-verification", runStepStatusRequiresManualVerification)
	_ = register("requirement-deleted-event-is-known", runRequirementDeletedEventIsKnown)

	// session_result_test.go

	_ = register("apply-artifacts-completes-session", runApplyArtifactsCompletesSession)
	_ = register("apply-artifacts-maps-run-status", runApplyArtifactsMapsRunStatus)
	_ = register("apply-artifacts-rejects-incomplete-metadata", runApplyArtifactsRejectsIncompleteMetadata)
	_ = register("session-completeness-has-jsonl", runSessionCompletenessHasJSONL)
	_ = register("apply-artifacts-records-output-paths", runApplyArtifactsRecordsOutputPaths)

	// step_execution_test.go

	_ = register("check-step-execution-start-requires-runnable-task-status", runCheckStepExecutionStartRequiresRunnableTaskStatus)
	_ = register("check-step-execution-start-requires-runnable-step-status", runCheckStepExecutionStartRequiresRunnableStepStatus)
	_ = register("check-step-execution-start-rejects-foreign-step", runCheckStepExecutionStartRejectsForeignStep)
	_ = register("check-step-execution-start-rejects-occupied-step", runCheckStepExecutionStartRejectsOccupiedStep)
	_ = register("check-step-execution-start-rejects-non-agent-step", runCheckStepExecutionStartRejectsNonAgentStep)
	_ = register("check-step-execution-start-rejects-cancelled-task", runCheckStepExecutionStartRejectsCancelledTask)
	_ = register("step-execution-outcome-attempt-status", runStepExecutionOutcomeAttemptStatus)

	// step_sequence_test.go

	_ = register("check-step-prerequisites-requires-previous-completed", runCheckStepPrerequisitesRequiresPreviousCompleted)
	_ = register("check-step-prerequisites-allows-completed-and-skipped", runCheckStepPrerequisitesAllowsCompletedAndSkipped)
	_ = register("check-step-prerequisites-rejects-failed-previous", runCheckStepPrerequisitesRejectsFailedPrevious)
	_ = register("next-step-to-execute-skips-non-develop-steps", runNextStepToExecuteSkipsNonDevelopSteps)
	_ = register("next-step-to-execute-stops-on-recovery-required", runNextStepToExecuteStopsOnRecoveryRequired)
	_ = register("next-step-to-execute-rejects-running-step", runNextStepToExecuteRejectsRunningStep)
	_ = register("next-step-to-execute-returns-done-when-all-completed", runNextStepToExecuteReturnsDoneWhenAllCompleted)
	_ = register("git-worktree-state-validate", runGitWorktreeStateValidate)
	_ = register("git-worktree-state-summary", runGitWorktreeStateSummary)
	_ = register("attempt-set-git-state", runAttemptSetGitState)
	_ = register("attempt-rejects-invalid-git-branch", runAttemptRejectsInvalidGitBranch)

	// step_verification_test.go

	_ = register("decide-step-closeout", runDecideStepCloseout)
	_ = register("derive-step-verification-state", runDeriveStepVerificationState)
	_ = register("derive-step-verification-state-from-matches-record", runDeriveStepVerificationStateFromMatchesRecord)
	_ = register("redact-sensitive-text", runRedactSensitiveText)
	_ = register("clip-feedback-text", runClipFeedbackText)
	_ = register("new-validation-failure-feedback", runNewValidationFailureFeedback)

	// task_closeout_test.go

	_ = register("decide-task-closeout-view-combinations", runDecideTaskCloseoutViewCombinations)
	_ = register("task-closeout-inconsistent-needs-attention", runTaskCloseoutInconsistentNeedsAttention)
	_ = register("decide-task-closeout-recovery-stalled-in-saving", runDecideTaskCloseoutRecoveryStalledInSaving)
	_ = register("decide-task-closeout-recovery-ignores-other-statuses", runDecideTaskCloseoutRecoveryIgnoresOtherStatuses)
	_ = register("task-closeout-inconsistent-summary-mentions-evidence", runTaskCloseoutInconsistentSummaryMentionsEvidence)

	// task_launch_test.go

	_ = register("decide-task-launch-next-picks-earliest-unfinished-stage", runDecideTaskLaunchNextPicksEarliestUnfinishedStage)
	_ = register("check-task-launch-start-rejects-invalid-scenes", runCheckTaskLaunchStartRejectsInvalidScenes)
	_ = register("decide-step-occupancy-recovery-only-converges-idle-occupancy", runDecideStepOccupancyRecoveryOnlyConvergesIdleOccupancy)
	_ = register("task-launch-stage-validate-rejects-unknown-stage", runTaskLaunchStageValidateRejectsUnknownStage)

	// task_management_test.go

	_ = register("cancel-immediately-only-from-queued", runCancelImmediatelyOnlyFromQueued)
	_ = register("cancel-immediately-rejects-running-task", runCancelImmediatelyRejectsRunningTask)
	_ = register("request-cancel-is-idempotent", runRequestCancelIsIdempotent)
	_ = register("request-cancel-rejects-terminal-task", runRequestCancelRejectsTerminalTask)
	_ = register("mark-deleted-requires-terminal-status", runMarkDeletedRequiresTerminalStatus)
	_ = register("mark-deleted-accepts-terminal-status", runMarkDeletedAcceptsTerminalStatus)
	_ = register("mark-deleted-rejects-repeated-delete", runMarkDeletedRejectsRepeatedDelete)
	_ = register("deleted-task-rejects-cancel", runDeletedTaskRejectsCancel)
	_ = register("task-status-is-terminal-covers-management-semantics", runTaskStatusIsTerminalCoversManagementSemantics)
	_ = register("validate-project-id", runValidateProjectID)

	// task_queue_test.go

	_ = register("task-status-allows-automatic-scheduling", runTaskStatusAllowsAutomaticScheduling)
	_ = register("task-status-allows-automatic-scheduling-is-stricter-than-can-start", runTaskStatusAllowsAutomaticSchedulingIsStricterThanCanStart)
	_ = register("decide-task-queue-admission-accepts-queued-task", runDecideTaskQueueAdmissionAcceptsQueuedTask)
	_ = register("decide-task-queue-admission-rejects-non-queued-status", runDecideTaskQueueAdmissionRejectsNonQueuedStatus)
	_ = register("decide-task-queue-admission-rejects-deleted-and-cancelled", runDecideTaskQueueAdmissionRejectsDeletedAndCancelled)
	_ = register("decide-task-queue-admission-rejects-active-run-and-residue", runDecideTaskQueueAdmissionRejectsActiveRunAndResidue)
	_ = register("decide-task-queue-admission-rejects-pending-side-effect", runDecideTaskQueueAdmissionRejectsPendingSideEffect)
	_ = register("decide-task-queue-admission-rejects-missing-prerequisite", runDecideTaskQueueAdmissionRejectsMissingPrerequisite)
	_ = register("decide-task-queue-admission-rejects-unknown-priority", runDecideTaskQueueAdmissionRejectsUnknownPriority)
	_ = register("decide-task-queue-admission-collects-all-reasons", runDecideTaskQueueAdmissionCollectsAllReasons)
	_ = register("compare-task-queue-order-by-priority", runCompareTaskQueueOrderByPriority)
	_ = register("compare-task-queue-order-is-fifo-within-priority", runCompareTaskQueueOrderIsFIFOWithinPriority)
	_ = register("compare-task-queue-order-is-stable-on-tied-time", runCompareTaskQueueOrderIsStableOnTiedTime)
	_ = register("sort-task-queue-order-keys-is-deterministic", runSortTaskQueueOrderKeysIsDeterministic)
	_ = register("new-task-queue-order-key-rejects-unknown-priority", runNewTaskQueueOrderKeyRejectsUnknownPriority)
	_ = register("decide-task-execution-handover", runDecideTaskExecutionHandover)
	_ = register("task-slot-state-occupancy", runTaskSlotStateOccupancy)
	_ = register("task-slot-state-only-idle-allows-automatic-claim", runTaskSlotStateOnlyIdleAllowsAutomaticClaim)
	_ = register("decide-task-slot-state-does-not-treat-indeterminate-as-failure", runDecideTaskSlotStateDoesNotTreatIndeterminateAsFailure)
	_ = register("decide-task-slot-state-blocked-run-awaits-human", runDecideTaskSlotStateBlockedRunAwaitsHuman)

	// task_retry_test.go

	_ = register("allows-explicit-retry-only-for-failed", runAllowsExplicitRetryOnlyForFailed)
	_ = register("decide-task-retry-eligibility-allows-clean-failure", runDecideTaskRetryEligibilityAllowsCleanFailure)
	_ = register("decide-task-retry-eligibility-rejects-blocking-facts", runDecideTaskRetryEligibilityRejectsBlockingFacts)
	_ = register("decide-task-retry-eligibility-collects-all-reasons", runDecideTaskRetryEligibilityCollectsAllReasons)
	_ = register("reopen-for-retry-clears-previous-cycle", runReopenForRetryClearsPreviousCycle)
	_ = register("reopen-for-retry-rejects-unsafe-states", runReopenForRetryRejectsUnsafeStates)

	// task_workspace_test.go

	_ = register("task-workspace-init-record-defaults", runTaskWorkspaceInitRecordDefaults)
	_ = register("task-workspace-init-status-machine", runTaskWorkspaceInitStatusMachine)
	_ = register("task-workspace-succeeded-requires-commit", runTaskWorkspaceSucceededRequiresCommit)
	_ = register("task-workspace-failed-requires-summary", runTaskWorkspaceFailedRequiresSummary)
	_ = register("task-workspace-rejects-invalid-work-dir", runTaskWorkspaceRejectsInvalidWorkDir)
	_ = register("decide-task-workspace-next", runDecideTaskWorkspaceNext)
	_ = register("decide-task-workspace-next-uncertain-message", runDecideTaskWorkspaceNextUncertainMessage)
	_ = register("decide-task-workspace-recovery", runDecideTaskWorkspaceRecovery)
	_ = register("task-workspace-interrupted-summary-is-readable", runTaskWorkspaceInterruptedSummaryIsReadable)

	// validation_decision_test.go

	_ = register("decide-validation-all-required-passed", runDecideValidationAllRequiredPassed)
	_ = register("decide-validation-required-exit-code-non-zero", runDecideValidationRequiredExitCodeNonZero)
	_ = register("decide-validation-optional-failure-keeps-passed", runDecideValidationOptionalFailureKeepsPassed)
	_ = register("decide-validation-required-timed-out", runDecideValidationRequiredTimedOut)
	_ = register("decide-validation-required-cancelled", runDecideValidationRequiredCancelled)
	_ = register("decide-validation-start-failed", runDecideValidationStartFailed)
	_ = register("decide-validation-all-skipped", runDecideValidationAllSkipped)
	_ = register("decide-validation-no-required-check", runDecideValidationNoRequiredCheck)
	_ = register("decide-validation-agent-cannot-override-required-failure", runDecideValidationAgentCannotOverrideRequiredFailure)
	_ = register("decide-validation-agent-failure-tightens", runDecideValidationAgentFailureTightens)
	_ = register("decide-validation-agent-needs-human-review", runDecideValidationAgentNeedsHumanReview)
	_ = register("decide-validation-incomplete-results", runDecideValidationIncompleteResults)
	_ = register("decide-validation-unfinished-run", runDecideValidationUnfinishedRun)
	_ = register("count-check-results-ignores-unfinished", runCountCheckResultsIgnoresUnfinished)
	_ = register("validation-spec-ordered", runValidationSpecOrdered)
	_ = register("validation-check-definition-rejects-shell-tokens", runValidationCheckDefinitionRejectsShellTokens)

	// workflow_run_test.go

	_ = register("workflow-run-status-transition", runWorkflowRunStatusTransition)
	_ = register("workflow-run-terminal-requires-finished-at", runWorkflowRunTerminalRequiresFinishedAt)
	_ = register("workflow-plan-digest-is-stable", runWorkflowPlanDigestIsStable)
	_ = register("check-workflow-start-rejects-invalid-plans", runCheckWorkflowStartRejectsInvalidPlans)
	_ = register("decide-workflow-closeout", runDecideWorkflowCloseout)
	_ = register("workflow-run-records-progress", runWorkflowRunRecordsProgress)
	_ = register("workflow-run-request-cancel-is-idempotent", runWorkflowRunRequestCancelIsIdempotent)
	_ = register("workflow-run-start-records-running-state", runWorkflowRunStartRecordsRunningState)
)
