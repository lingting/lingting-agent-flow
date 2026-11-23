// 本文件登记 tests/unit/httpapi 的 241 条用例，顺序与源文件一致，便于按包核对迁移数量。
//
// 221 条源用例带 t.Parallel()，按可并行条目登记；规划分析相关的 20 条源用例
// 没有 t.Parallel()，按串行条目登记。每个源文件对应一个 cases_*.go，注释里保留源文件名；
// cases_fixture.go 由 container_fixture_test.go 迁来，只有辅助函数，没有可登记用例。
package httpapi

var (
	// code_initialize_test.go

	_ = register("initialize-code-returns-record", runInitializeCodeReturnsRecord)
	_ = register("initialize-code-rejects-get", runInitializeCodeRejectsGet)
	_ = register("initialize-code-returns-not-found-for-unknown-task", runInitializeCodeReturnsNotFoundForUnknownTask)
	_ = register("initialize-code-returns-conflict-when-step-mismatched", runInitializeCodeReturnsConflictWhenStepMismatched)
	_ = register("initialize-code-returns-precondition-when-container-missing", runInitializeCodeReturnsPreconditionWhenContainerMissing)
	_ = register("initialize-code-returns-execution-failed", runInitializeCodeReturnsExecutionFailed)
	_ = register("initialize-code-rejects-invalid-timeout", runInitializeCodeRejectsInvalidTimeout)
	_ = register("initialize-code-rejects-invalid-work-dir", runInitializeCodeRejectsInvalidWorkDir)
	_ = register("initialize-code-requires-service", runInitializeCodeRequiresService)
	_ = register("initialize-code-unknown-path-is-not-found", runInitializeCodeUnknownPathIsNotFound)
	_ = register("initialize-code-does-not-leak-secrets", runInitializeCodeDoesNotLeakSecrets)

	// environments_test.go

	_ = register("environment-list-returns-real-records", runEnvironmentListReturnsRealRecords)
	_ = register("environment-list-omits-internal-fields", runEnvironmentListOmitsInternalFields)
	_ = register("environment-version-returns-real-build-status", runEnvironmentVersionReturnsRealBuildStatus)
	_ = register("environment-list-rejects-missing-host-id", runEnvironmentListRejectsMissingHostID)
	_ = register("environment-list-rejects-unknown-host", runEnvironmentListRejectsUnknownHost)
	_ = register("environment-list-empty-host-returns-empty-list", runEnvironmentListEmptyHostReturnsEmptyList)
	_ = register("environment-version-rejects-unknown-version", runEnvironmentVersionRejectsUnknownVersion)
	_ = register("environment-version-rejects-invalid-parameters", runEnvironmentVersionRejectsInvalidParameters)
	_ = register("environment-version-rejects-missing-host-id", runEnvironmentVersionRejectsMissingHostID)
	_ = register("environment-unknown-sub-paths-are-not-found", runEnvironmentUnknownSubPathsAreNotFound)
	_ = register("environment-rejects-unsupported-methods", runEnvironmentRejectsUnsupportedMethods)
	_ = register("environment-without-service-returns-unavailable", runEnvironmentWithoutServiceReturnsUnavailable)
	_ = register("environment-storage-failure-is-not-an-empty-list", runEnvironmentStorageFailureIsNotAnEmptyList)
	_ = register("environment-query-service-is-read-only", runEnvironmentQueryServiceIsReadOnly)
	_ = register("environment-version-not-found-is-distinguishable", runEnvironmentVersionNotFoundIsDistinguishable)

	// environments_write_test.go

	_ = register("create-version-rejects-invalid-requests", runCreateVersionRejectsInvalidRequests)
	_ = register("create-version-maps-service-errors", runCreateVersionMapsServiceErrors)
	_ = register("create-version-converts-request", runCreateVersionConvertsRequest)
	_ = register("build-submit-reports-accepted-only", runBuildSubmitReportsAcceptedOnly)
	_ = register("build-submit-validation", runBuildSubmitValidation)
	_ = register("build-status-reports-real-outcome", runBuildStatusReportsRealOutcome)
	_ = register("build-log-never-accepts-paths", runBuildLogNeverAcceptsPaths)
	_ = register("dockerfile-endpoint-reads-version", runDockerfileEndpointReadsVersion)
	_ = register("environment-read-endpoints-regression", runEnvironmentReadEndpointsRegression)
	_ = register("environment-write-endpoints-require-assembly", runEnvironmentWriteEndpointsRequireAssembly)
	_ = register("init-endpoint-reports-preflight", runInitEndpointReportsPreflight)

	// history_test.go

	_ = register("list-events-returns-json-contract", runListEventsReturnsJSONContract)
	_ = register("list-events-rejects-unknown-task", runListEventsRejectsUnknownTask)
	_ = register("list-events-rejects-invalid-paging", runListEventsRejectsInvalidPaging)
	_ = register("list-events-supports-paging-and-filters", runListEventsSupportsPagingAndFilters)
	_ = register("list-events-shows-unknown-type-safely", runListEventsShowsUnknownTypeSafely)
	_ = register("list-events-rejects-non-get-method", runListEventsRejectsNonGetMethod)
	_ = register("task-event-stream-rejects-non-get-method", runTaskEventStreamRejectsNonGetMethod)
	_ = register("task-event-stream-rejects-invalid-cursor", runTaskEventStreamRejectsInvalidCursor)
	_ = register("task-event-stream-rejects-unknown-task", runTaskEventStreamRejectsUnknownTask)
	_ = register("task-event-stream-pushes-persisted-events", runTaskEventStreamPushesPersistedEvents)
	_ = register("task-event-stream-sends-heartbeat-comments", runTaskEventStreamSendsHeartbeatComments)
	_ = register("task-event-stream-delivers-events-across-batches", runTaskEventStreamDeliversEventsAcrossBatches)
	_ = register("task-event-stream-stops-on-write-failure", runTaskEventStreamStopsOnWriteFailure)
	_ = register("task-event-stream-stops-on-request-cancel", runTaskEventStreamStopsOnRequestCancel)
	_ = register("task-event-stream-slow-client-does-not-block-other-tasks", runTaskEventStreamSlowClientDoesNotBlockOtherTasks)
	_ = register("list-repair-cycles-returns-history", runListRepairCyclesReturnsHistory)
	_ = register("list-repair-cycles-returns-empty-list", runListRepairCyclesReturnsEmptyList)
	_ = register("list-repair-cycles-rejects-foreign-step", runListRepairCyclesRejectsForeignStep)
	_ = register("list-repair-cycles-rejects-unknown-targets", runListRepairCyclesRejectsUnknownTargets)
	_ = register("history-endpoints-unavailable-without-service", runHistoryEndpointsUnavailableWithoutService)
	_ = register("history-endpoints-are-read-only", runHistoryEndpointsAreReadOnly)

	// hosts_test.go

	_ = register("create-host-returns-checking-status", runCreateHostReturnsCheckingStatus)
	_ = register("create-host-ignores-client-validation-fields", runCreateHostIgnoresClientValidationFields)
	_ = register("create-host-rejects-invalid-auth-method", runCreateHostRejectsInvalidAuthMethod)
	_ = register("get-host-returns-validation-details", runGetHostReturnsValidationDetails)
	_ = register("validate-endpoint-returns-checking", runValidateEndpointReturnsChecking)
	_ = register("validate-endpoint-rejects-get", runValidateEndpointRejectsGet)
	_ = register("update-host-invalidates-validation", runUpdateHostInvalidatesValidation)
	_ = register("unknown-host-returns-json-not-found", runUnknownHostReturnsJSONNotFound)
	_ = register("unknown-host-sub-path-returns-not-found", runUnknownHostSubPathReturnsNotFound)
	_ = register("host-service-unavailable-returns503", runHostServiceUnavailableReturns503)
	_ = register("list-hosts-returns-all-records", runListHostsReturnsAllRecords)
	_ = register("delete-host-returns-no-content", runDeleteHostReturnsNoContent)
	_ = register("host-response-does-not-leak-secrets", runHostResponseDoesNotLeakSecrets)

	// plan_test.go

	_ = register("plan-api-persists-plan", runPlanAPIPersistsPlan)
	_ = register("plan-api-rejects-invalid-output", runPlanAPIRejectsInvalidOutput)
	_ = register("plan-api-method-not-allowed", runPlanAPIMethodNotAllowed)
	_ = register("plan-api-negative-timeout", runPlanAPINegativeTimeout)
	_ = register("plan-path-not-swallowed-by-task-handler", runPlanPathNotSwallowedByTaskHandler)

	// planning_analysis_reliability_test.go

	_ = registerSerial("planning-api-analysis-persistence-failure-is-reported", runPlanningAPIAnalysisPersistenceFailureIsReported)
	_ = registerSerial("planning-api-analysis-retry-after-restart-recovery", runPlanningAPIAnalysisRetryAfterRestartRecovery)
	_ = registerSerial("planning-api-analysis-retry-rejects-running-analysis", runPlanningAPIAnalysisRetryRejectsRunningAnalysis)
	_ = registerSerial("planning-api-analysis-rejects-request-overrides", runPlanningAPIAnalysisRejectsRequestOverrides)

	// planning_analysis_test.go

	_ = registerSerial("planning-api-create-and-list-sessions", runPlanningAPICreateAndListSessions)
	_ = registerSerial("planning-api-analysis-full-flow", runPlanningAPIAnalysisFullFlow)
	_ = registerSerial("planning-api-analysis-requires-analyzing-session", runPlanningAPIAnalysisRequiresAnalyzingSession)
	_ = registerSerial("planning-api-analysis-rejects-concurrent-request", runPlanningAPIAnalysisRejectsConcurrentRequest)
	_ = registerSerial("planning-api-analysis-failure-is-reported", runPlanningAPIAnalysisFailureIsReported)
	_ = registerSerial("planning-api-analysis-without-service", runPlanningAPIAnalysisWithoutService)
	_ = registerSerial("planning-api-analysis-rejects-unknown-field", runPlanningAPIAnalysisRejectsUnknownField)
	_ = registerSerial("planning-api-message-and-abandon-flow", runPlanningAPIMessageAndAbandonFlow)

	// planning_test.go

	_ = registerSerial("planning-api-session-detail", runPlanningAPISessionDetail)
	_ = registerSerial("planning-api-session-detail-not-found", runPlanningAPISessionDetailNotFound)
	_ = registerSerial("planning-api-runtime-inspection-without-binding", runPlanningAPIRuntimeInspectionWithoutBinding)
	_ = registerSerial("planning-api-prepare-requires-bound-host", runPlanningAPIPrepareRequiresBoundHost)
	_ = registerSerial("planning-api-prepare-without-container-service", runPlanningAPIPrepareWithoutContainerService)
	_ = registerSerial("planning-api-release-on-unbound-session", runPlanningAPIReleaseOnUnboundSession)
	_ = registerSerial("planning-api-without-services", runPlanningAPIWithoutServices)
	_ = registerSerial("planning-api-method-not-allowed", runPlanningAPIMethodNotAllowed)

	// projects_test.go

	_ = register("project-api-lifecycle", runProjectAPILifecycle)
	_ = register("project-api-create-rejects-invalid-input", runProjectAPICreateRejectsInvalidInput)
	_ = register("project-api-create-rejects-unknown-field", runProjectAPICreateRejectsUnknownField)
	_ = register("project-api-create-rejects-duplicate-id", runProjectAPICreateRejectsDuplicateID)
	_ = register("project-api-unknown-project-returns-not-found", runProjectAPIUnknownProjectReturnsNotFound)
	_ = register("project-api-rejects-unsupported-methods", runProjectAPIRejectsUnsupportedMethods)
	_ = register("project-api-path-shape-rejects-nested-segments", runProjectAPIPathShapeRejectsNestedSegments)
	_ = register("project-api-without-service-reports-unavailable", runProjectAPIWithoutServiceReportsUnavailable)
	_ = register("project-api-task-creation-requires-project", runProjectAPITaskCreationRequiresProject)
	_ = register("project-api-default-host-null-when-unset", runProjectAPIDefaultHostNullWhenUnset)
	_ = register("project-api-default-host-lifecycle", runProjectAPIDefaultHostLifecycle)
	_ = register("project-api-default-host-rejects-unknown-host", runProjectAPIDefaultHostRejectsUnknownHost)

	// requirement_confirm_test.go

	_ = register("requirement-api-confirm-and-query", runRequirementAPIConfirmAndQuery)
	_ = register("requirement-api-confirm-rejects-invalid-request", runRequirementAPIConfirmRejectsInvalidRequest)
	_ = register("requirement-api-confirm-reports-not-found", runRequirementAPIConfirmReportsNotFound)
	_ = register("requirement-api-confirm-reports-state-conflict", runRequirementAPIConfirmReportsStateConflict)
	_ = register("requirement-api-confirm-reports-prerequisite", runRequirementAPIConfirmReportsPrerequisite)
	_ = register("requirement-api-confirm-reports-persistence-failure", runRequirementAPIConfirmReportsPersistenceFailure)
	_ = register("requirement-api-confirm-concurrent", runRequirementAPIConfirmConcurrent)
	_ = register("requirement-api-unavailable", runRequirementAPIUnavailable)

	// requirement_delete_test.go

	_ = register("requirement-api-delete-returns-real-result", runRequirementAPIDeleteReturnsRealResult)
	_ = register("requirement-api-delete-missing-and-repeated", runRequirementAPIDeleteMissingAndRepeated)
	_ = register("requirement-api-delete-rejects-invalid-id", runRequirementAPIDeleteRejectsInvalidID)
	_ = register("requirement-api-delete-rejects-unsettled-tasks", runRequirementAPIDeleteRejectsUnsettledTasks)
	_ = register("requirement-api-delete-unknown-status-conflicts", runRequirementAPIDeleteUnknownStatusConflicts)
	_ = register("requirement-api-delete-unavailable-without-coordination", runRequirementAPIDeleteUnavailableWithoutCoordination)
	_ = register("requirement-api-delete-bare-handler", runRequirementAPIDeleteBareHandler)
	_ = register("requirement-api-delete-method-not-allowed", runRequirementAPIDeleteMethodNotAllowed)
	_ = register("requirement-api-delete-reports-persistence-failure", runRequirementAPIDeleteReportsPersistenceFailure)
	_ = register("requirement-api-delete-reports-event-failure", runRequirementAPIDeleteReportsEventFailure)

	// roles_test.go

	_ = register("role-api-lifecycle", runRoleAPILifecycle)
	_ = register("role-api-rejects-forbidden-fields", runRoleAPIRejectsForbiddenFields)
	_ = register("role-api-updates-tools-policy-within-ceiling", runRoleAPIUpdatesToolsPolicyWithinCeiling)
	_ = register("role-api-keeps-task-creation-roles", runRoleAPIKeepsTaskCreationRoles)

	// router_test.go

	_ = register("health-reports-degraded-without-store", runHealthReportsDegradedWithoutStore)
	_ = register("health-sets-json-content-type", runHealthSetsJSONContentType)
	_ = register("system-info-returns-build-fields", runSystemInfoReturnsBuildFields)
	_ = register("unknown-api-path-returns-json-not-found", runUnknownAPIPathReturnsJSONNotFound)
	_ = register("unsupported-method-returns-json-error", runUnsupportedMethodReturnsJSONError)
	_ = register("api-path-boundary", runAPIPathBoundary)
	_ = register("api-root-returns-json-error", runAPIRootReturnsJSONError)
	_ = register("health-without-service-reports-unavailable", runHealthWithoutServiceReportsUnavailable)
	_ = register("trailing-slash-is-not-found", runTrailingSlashIsNotFound)

	// step_execution_test.go

	_ = register("execute-step-api", runExecuteStepAPI)
	_ = register("execute-step-api-failure-returns500", runExecuteStepAPIFailureReturns500)
	_ = register("execute-step-api-queued-conflict", runExecuteStepAPIQueuedConflict)
	_ = register("execute-step-api-not-found", runExecuteStepAPINotFound)
	_ = register("execute-step-api-method-not-allowed", runExecuteStepAPIMethodNotAllowed)
	_ = register("execute-step-api-invalid-timeout", runExecuteStepAPIInvalidTimeout)
	_ = register("execute-step-api-service-unavailable", runExecuteStepAPIServiceUnavailable)
	_ = register("execute-step-api-not-handled-by-spa", runExecuteStepAPINotHandledBySPA)
	_ = register("execute-step-api-storage-conflict", runExecuteStepAPIStorageConflict)
	_ = register("execute-steps-api", runExecuteStepsAPI)
	_ = register("execute-steps-api-stops-on-failure", runExecuteStepsAPIStopsOnFailure)
	_ = register("execute-steps-api-rejects-queued-task", runExecuteStepsAPIRejectsQueuedTask)
	_ = register("execute-steps-api-rejects-invalid-timeout", runExecuteStepsAPIRejectsInvalidTimeout)
	_ = register("execute-steps-api-rejects-get", runExecuteStepsAPIRejectsGet)
	_ = register("execute-steps-api-not-found", runExecuteStepsAPINotFound)

	// step_workflow_test.go

	_ = register("execute-and-validate-api", runExecuteAndValidateAPI)
	_ = register("execute-and-validate-api-verification-failed-returns500", runExecuteAndValidateAPIVerificationFailedReturns500)
	_ = register("execute-and-validate-api-developer-failure-returns500", runExecuteAndValidateAPIDeveloperFailureReturns500)
	_ = register("execute-and-validate-api-method-not-allowed", runExecuteAndValidateAPIMethodNotAllowed)
	_ = register("execute-and-validate-api-not-found", runExecuteAndValidateAPINotFound)
	_ = register("execute-and-validate-api-conflict", runExecuteAndValidateAPIConflict)
	_ = register("execute-and-validate-api-invalid-config-returns400", runExecuteAndValidateAPIInvalidConfigReturns400)
	_ = register("list-validations-api", runListValidationsAPI)
	_ = register("list-validations-api-empty", runListValidationsAPIEmpty)
	_ = register("repair-cycle-eligibility-api-never-executed", runRepairCycleEligibilityAPINeverExecuted)
	_ = register("repair-cycle-eligibility-api-budget-exhausted-allows", runRepairCycleEligibilityAPIBudgetExhaustedAllows)
	_ = register("repair-cycle-eligibility-api-method-not-allowed", runRepairCycleEligibilityAPIMethodNotAllowed)
	_ = register("repair-cycle-eligibility-api-not-found", runRepairCycleEligibilityAPINotFound)

	// task_containers_test.go

	_ = register("task-containers-list-returns-real-records", runTaskContainersListReturnsRealRecords)
	_ = register("task-containers-list-rejects-wrong-method", runTaskContainersListRejectsWrongMethod)
	_ = register("task-containers-list-unknown-task", runTaskContainersListUnknownTask)
	_ = register("task-containers-missing-service-reports-unavailable", runTaskContainersMissingServiceReportsUnavailable)
	_ = register("task-container-delete-succeeds", runTaskContainerDeleteSucceeds)
	_ = register("task-container-detach-succeeds", runTaskContainerDetachSucceeds)
	_ = register("task-container-disposal-rejects-cross-task", runTaskContainerDisposalRejectsCrossTask)
	_ = register("task-container-disposal-rejects-unknown-records", runTaskContainerDisposalRejectsUnknownRecords)
	_ = register("task-container-detach-rejects-removed-container", runTaskContainerDetachRejectsRemovedContainer)
	_ = register("task-container-delete-is-idempotent-on-removed-record", runTaskContainerDeleteIsIdempotentOnRemovedRecord)
	_ = register("task-container-disposal-failures", runTaskContainerDisposalFailures)
	_ = register("task-container-disposal-uncertain-outcome", runTaskContainerDisposalUncertainOutcome)
	_ = register("task-container-disposal-method-not-allowed", runTaskContainerDisposalMethodNotAllowed)
	_ = register("task-container-response-hides-internal-details", runTaskContainerResponseHidesInternalDetails)

	// task_launch_test.go

	_ = register("start-api-reaches-workflow-stage", runStartAPIReachesWorkflowStage)
	_ = register("start-api-reports-workspace-stage", runStartAPIReportsWorkspaceStage)
	_ = register("start-api-maps-workspace-uncertain-to-conflict", runStartAPIMapsWorkspaceUncertainToConflict)
	_ = register("start-api-maps-workspace-prerequisite-to-precondition", runStartAPIMapsWorkspacePrerequisiteToPrecondition)
	_ = register("start-api-maps-workspace-not-persisted", runStartAPIMapsWorkspaceNotPersisted)
	_ = register("start-api-method-not-allowed", runStartAPIMethodNotAllowed)
	_ = register("start-api-rejects-unknown-task", runStartAPIRejectsUnknownTask)
	_ = register("start-api-rejects-active-run", runStartAPIRejectsActiveRun)
	_ = register("start-api-rejects-negative-timeout", runStartAPIRejectsNegativeTimeout)
	_ = register("start-api-without-service-returns503", runStartAPIWithoutServiceReturns503)
	_ = register("start-api-maps-prerequisite-error", runStartAPIMapsPrerequisiteError)

	// task_retry_test.go

	_ = register("retry-api-accepts-failed-task", runRetryAPIAcceptsFailedTask)
	_ = register("retry-api-rejects-wrong-method", runRetryAPIRejectsWrongMethod)
	_ = register("retry-api-rejects-non-failed-task", runRetryAPIRejectsNonFailedTask)
	_ = register("retry-api-not-found", runRetryAPINotFound)
	_ = register("retry-api-unavailable-without-service", runRetryAPIUnavailableWithoutService)
	_ = register("retry-eligibility-api-is-read-only", runRetryEligibilityAPIIsReadOnly)

	// tasks_test.go

	_ = register("create-task-returns-created-task", runCreateTaskReturnsCreatedTask)
	_ = register("create-task-rejects-invalid-input", runCreateTaskRejectsInvalidInput)
	_ = register("create-task-rejects-malformed-body", runCreateTaskRejectsMalformedBody)
	_ = register("list-tasks-paginates", runListTasksPaginates)
	_ = register("list-tasks-defaults-and-filters", runListTasksDefaultsAndFilters)
	_ = register("list-tasks-rejects-invalid-paging", runListTasksRejectsInvalidPaging)
	_ = register("get-task-detail", runGetTaskDetail)
	_ = register("get-task-detail-not-found", runGetTaskDetailNotFound)
	_ = register("cancel-queued-task-succeeds", runCancelQueuedTaskSucceeds)
	_ = register("cancel-finished-task-returns-conflict", runCancelFinishedTaskReturnsConflict)
	_ = register("cancel-running-task-marks-cancelling", runCancelRunningTaskMarksCancelling)
	_ = register("delete-finished-task-returns-no-content", runDeleteFinishedTaskReturnsNoContent)
	_ = register("failed-task-is-finished-but-not-terminal", runFailedTaskIsFinishedButNotTerminal)
	_ = register("delete-running-task-returns-conflict", runDeleteRunningTaskReturnsConflict)
	_ = register("delete-unknown-task-returns-not-found", runDeleteUnknownTaskReturnsNotFound)
	_ = register("task-paths-reject-unsupported-methods", runTaskPathsRejectUnsupportedMethods)
	_ = register("unknown-task-sub-path-returns-json-not-found", runUnknownTaskSubPathReturnsJSONNotFound)
	_ = register("tasks-without-service-reports-unavailable", runTasksWithoutServiceReportsUnavailable)
	_ = register("task-response-does-not-expose-internal-fields", runTaskResponseDoesNotExposeInternalFields)

	// validation_test.go

	_ = register("validate-attempt-api", runValidateAttemptAPI)
	_ = register("validate-attempt-api-required-check-fails", runValidateAttemptAPIRequiredCheckFails)
	_ = register("validate-attempt-api-container-not-running", runValidateAttemptAPIContainerNotRunning)
	_ = register("validate-attempt-api-not-found", runValidateAttemptAPINotFound)
	_ = register("validate-attempt-api-invalid-check", runValidateAttemptAPIInvalidCheck)
	_ = register("validate-attempt-api-rejects-unsafe-work-dir", runValidateAttemptAPIRejectsUnsafeWorkDir)
	_ = register("validate-attempt-api-method-not-allowed", runValidateAttemptAPIMethodNotAllowed)
	_ = register("validate-attempt-api-unavailable-service", runValidateAttemptAPIUnavailableService)
	_ = register("validate-attempt-api-unknown-path-is-not-found", runValidateAttemptAPIUnknownPathIsNotFound)

	// workflow_test.go

	_ = register("workflow-api-lifecycle", runWorkflowAPILifecycle)
	_ = register("workflow-api-status-without-run", runWorkflowAPIStatusWithoutRun)
	_ = register("workflow-api-rejects-unknown-path", runWorkflowAPIRejectsUnknownPath)
	_ = register("workflow-api-rejects-wrong-method", runWorkflowAPIRejectsWrongMethod)
	_ = register("workflow-api-rejects-duplicate-start", runWorkflowAPIRejectsDuplicateStart)
	_ = register("workflow-api-rejects-invalid-timeout", runWorkflowAPIRejectsInvalidTimeout)
	_ = register("workflow-api-rejects-unknown-task", runWorkflowAPIRejectsUnknownTask)
	_ = register("workflow-api-cancel-requires-run", runWorkflowAPICancelRequiresRun)
	_ = register("workflow-api-resume-requires-run-id", runWorkflowAPIResumeRequiresRunID)

	// workflow_timestamps_test.go

	_ = register("workflow-api-status-omits-missing-step-timestamps", runWorkflowAPIStatusOmitsMissingStepTimestamps)
	_ = register("workflow-api-status-returns-persisted-step-timestamps", runWorkflowAPIStatusReturnsPersistedStepTimestamps)
	_ = register("workflow-api-status-returns-finished-at-on-terminal-steps", runWorkflowAPIStatusReturnsFinishedAtOnTerminalSteps)
	_ = register("workflow-api-status-step-times-are-not-task-times", runWorkflowAPIStatusStepTimesAreNotTaskTimes)
	_ = register("workflow-api-status-keeps-failure-conclusion", runWorkflowAPIStatusKeepsFailureConclusion)
	_ = register("steps-api-returns-same-step-timestamps", runStepsAPIReturnsSameStepTimestamps)
)
