// 本文件登记 tests/unit/container 的 29 条用例，顺序与源文件一致，便于按包核对迁移数量。
//
// 29 条源用例都带 t.Parallel()，因此全部按可并行条目登记；每个源文件对应一个
// cases_*.go，注释里保留源文件名。
package container

var (
	// container_test.go

	_ = register("new-container-validates-fields", runNewContainerValidatesFields)
	_ = register("container-lifecycle-transitions", runContainerLifecycleTransitions)
	_ = register("detached-container-rejects-automatic-operations", runDetachedContainerRejectsAutomaticOperations)
	_ = register("container-state-validation", runContainerStateValidation)
	_ = register("parse-docker-container-state", runParseDockerContainerState)
	_ = register("decide-container-lifecycle", runDecideContainerLifecycle)
	_ = register("plan-container-restart-requires-new-container", runPlanContainerRestartRequiresNewContainer)
	_ = register("cache-mount-validation", runCacheMountValidation)
	_ = register("select-caches-requires-explicit-allow-list", runSelectCachesRequiresExplicitAllowList)
	_ = register("project-config-rejects-base-environment", runProjectConfigRejectsBaseEnvironment)
	_ = register("container-errors-are-distinguishable", runContainerErrorsAreDistinguishable)

	// docker_args_test.go

	_ = register("cache-volume-name-is-scoped", runCacheVolumeNameIsScoped)
	_ = register("container-name-is-safe-and-unique", runContainerNameIsSafeAndUnique)
	_ = register("verify-ownership-requires-matching-labels", runVerifyOwnershipRequiresMatchingLabels)
	_ = register("layout-paths", runLayoutPaths)
	_ = register("parse-df-available-kilobytes", runParseDfAvailableKilobytes)
	_ = register("docker-version-parsing", runDockerVersionParsing)
	_ = register("classify-docker-message", runClassifyDockerMessage)
	_ = register("container-create-args-enforce-safety-boundary", runContainerCreateArgsEnforceSafetyBoundary)
	_ = register("container-create-args-reject-invalid-input", runContainerCreateArgsRejectInvalidInput)
	_ = register("build-log-command-quotes-paths", runBuildLogCommandQuotesPaths)
	_ = register("parse-image-inspect", runParseImageInspect)
	_ = register("parse-container-inspect", runParseContainerInspect)
	_ = register("container-work-dir-must-be-inside-workspace", runContainerWorkDirMustBeInsideWorkspace)
	_ = register("planning-container-create-args", runPlanningContainerCreateArgs)
	_ = register("planning-container-create-args-reject-invalid-input", runPlanningContainerCreateArgsRejectInvalidInput)
	_ = register("planning-container-create-args-required-cli-commands", runPlanningContainerCreateArgsRequiredCLICommands)

	// docker_status_test.go

	_ = register("parse-container-inspect-status-matrix", runParseContainerInspectStatusMatrix)
	_ = register("classify-docker-container-condition-is-total", runClassifyDockerContainerConditionIsTotal)
)
