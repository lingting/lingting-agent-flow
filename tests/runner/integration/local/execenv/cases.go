// 本文件登记 tests/integration/local/execenv 的 28 条用例，顺序与源文件一致，
// 便于与 R0 冻结清单逐条对账。
//
// 用例主体在 cases_execenv.go 与 cases_docker.go 里；本包的容器、镜像与 SSH 全部是
// 受控替身，不占用编排进程资源，因此没有资源声明。
package execenv

var (
	// execenv_test.go（22）
	_ = register("prepare-succeeds-and-records-stages", runPrepareSucceedsAndRecordsStages)
	_ = register("prepare-fails-when-image-preparation-fails", runPrepareFailsWhenImagePreparationFails)
	_ = register("prepare-fails-when-container-creation-fails", runPrepareFailsWhenContainerCreationFails)
	_ = register("prepare-fails-when-container-not-running", runPrepareFailsWhenContainerNotRunning)
	_ = register("status-distinguishes-released-and-detached", runStatusDistinguishesReleasedAndDetached)
	_ = register("status-reports-unknown-on-inspect-failure", runStatusReportsUnknownOnInspectFailure)
	_ = register("exec-passes-work-dir-args-and-env", runExecPassesWorkDirArgsAndEnv)
	_ = register("exec-rejects-work-dir-outside-workspace", runExecRejectsWorkDirOutsideWorkspace)
	_ = register("exec-propagates-timeout-and-cancellation", runExecPropagatesTimeoutAndCancellation)
	_ = register("exec-reports-execution-layer-error", runExecReportsExecutionLayerError)
	_ = register("release-delete-removes-container", runReleaseDeleteRemovesContainer)
	_ = register("release-keeps-container-on-failure", runReleaseKeepsContainerOnFailure)
	_ = register("release-detach-on-interrupted-keeps-control", runReleaseDetachOnInterruptedKeepsControl)
	_ = register("release-delete-is-idempotent-when-container-missing", runReleaseDeleteIsIdempotentWhenContainerMissing)
	_ = register("release-rejects-ownership-mismatch-as-missing", runReleaseRejectsOwnershipMismatchAsMissing)
	_ = register("prepare-for-task-creates-fresh-container-per-attempt", runPrepareForTaskCreatesFreshContainerPerAttempt)
	_ = register("prepare-for-task-rejects-unknown-attempt", runPrepareForTaskRejectsUnknownAttempt)
	_ = register("prepare-rejects-unvalidated-host", runPrepareRejectsUnvalidatedHost)
	_ = register("workspace-isolation-from-management-area", runWorkspaceIsolationFromManagementArea)
	_ = register("session-files-survive-container-release", runSessionFilesSurviveContainerRelease)
	_ = register("check-environment-runs-in-given-workspace", runCheckEnvironmentRunsInGivenWorkspace)
	_ = register("prepare-for-task-reports-conflict-when-task-advanced", runPrepareForTaskReportsConflictWhenTaskAdvanced)

	// docker_test.go（6）
	_ = register("exec-command-in-passes-env-and-work-dir", runExecCommandInPassesEnvAndWorkDir)
	_ = register("exec-command-in-rejects-invalid-env", runExecCommandInRejectsInvalidEnv)
	_ = register("exec-command-in-with-input-enables-interactive", runExecCommandInWithInputEnablesInteractive)
	_ = register("required-cli-commands-covers-exec-env", runRequiredCLICommandsCoversExecEnv)
	_ = register("compatibility-baseline-is-unchanged", runCompatibilityBaselineIsUnchanged)
	_ = register("container-create-args-keep-safety-boundaries", runContainerCreateArgsKeepSafetyBoundaries)
)
