// 本文件登记 tests/unit/agent 的 35 条用例，顺序与源文件一致，便于按包核对迁移数量。
//
// 35 条源用例都带 t.Parallel()，因此全部按可并行条目登记；每个源文件对应一个
// cases_*.go，注释里保留源文件名。
package agent

var (
	// agent_test.go

	_ = register("new-event-encodes-payload", runNewEventEncodesPayload)
	_ = register("new-event-without-payload", runNewEventWithoutPayload)
	_ = register("new-event-rejects-unknown-type", runNewEventRejectsUnknownType)
	_ = register("event-type-validate", runEventTypeValidate)
	_ = register("run-status-validate", runRunStatusValidate)
	_ = register("run-request-validate", runRunRequestValidate)
	_ = register("truncate-text", runTruncateText)

	// container_runner_test.go

	_ = register("command-construction", runCommandConstruction)
	_ = register("session-path-unique-per-invocation", runSessionPathUniquePerInvocation)
	_ = register("session-saved-locally", runSessionSavedLocally)
	_ = register("export-failure-keeps-jsonl", runExportFailureKeepsJSONL)
	_ = register("missing-session-marks-failure", runMissingSessionMarksFailure)
	_ = register("partial-session-recorded", runPartialSessionRecorded)
	_ = register("download-failure-keeps-remote-copy", runDownloadFailureKeepsRemoteCopy)
	_ = register("non-zero-exit-classified", runNonZeroExitClassified)
	_ = register("timeout-and-cancel-classified", runTimeoutAndCancelClassified)
	_ = register("uncertain-process-reported", runUncertainProcessReported)
	_ = register("pi-unavailable-in-container", runPiUnavailableInContainer)
	_ = register("detached-container-rejected", runDetachedContainerRejected)
	_ = register("docker-id-mismatch-rejected", runDockerIDMismatchRejected)
	_ = register("stable-session-first-run-creates-archive", runStableSessionFirstRunCreatesArchive)
	_ = register("stable-session-second-run-restores-before-pi", runStableSessionSecondRunRestoresBeforePi)
	_ = register("stable-session-failed-run-keeps-last-known-good-archive", runStableSessionFailedRunKeepsLastKnownGoodArchive)
	_ = register("stable-session-corrupt-archive-rejects-before-pi", runStableSessionCorruptArchiveRejectsBeforePi)
	_ = register("stable-session-archive-failure-is-reported", runStableSessionArchiveFailureIsReported)
	_ = register("stable-session-restore-rejects-unsupported-writer", runStableSessionRestoreRejectsUnsupportedWriter)

	// container_termination_test.go

	_ = register("container-runner-success-does-not-terminate-container", runContainerRunnerSuccessDoesNotTerminateContainer)
	_ = register("container-runner-task-uncertain-does-not-terminate-container", runContainerRunnerTaskUncertainDoesNotTerminateContainer)
	_ = register("container-runner-terminates-on-planning-timeout", runContainerRunnerTerminatesOnPlanningTimeout)
	_ = register("container-runner-terminates-once-when-result-uncertain", runContainerRunnerTerminatesOnceWhenResultUncertain)
	_ = register("container-runner-termination-uses-cleanup-context", runContainerRunnerTerminationUsesCleanupContext)
	_ = register("container-runner-skips-termination-when-command-never-started", runContainerRunnerSkipsTerminationWhenCommandNeverStarted)
	_ = register("container-runner-skips-termination-on-explicit-exit", runContainerRunnerSkipsTerminationOnExplicitExit)
	_ = register("container-runner-termination-uncertain-reported", runContainerRunnerTerminationUncertainReported)
	_ = register("container-runner-termination-keeps-last-archive", runContainerRunnerTerminationKeepsLastArchive)
)
