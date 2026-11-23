// 本文件登记 tests/unit/execenv 的 20 条用例，顺序与源文件 execenv_test.go 一致，
// 便于按包核对迁移数量。
//
// 用例主体在 cases_execenv.go 里；本包用例只依赖纯类型与受控替身，不占用编排进程资源，
// 因此没有资源声明。
package execenv

var (
	_ = register("environment-kind-must-be-supported", runEnvironmentKindMustBeSupported)
	_ = register("workspace-rejects-source-in-session-dir", runWorkspaceRejectsSourceInSessionDir)
	_ = register("management-workspace-does-not-contain-source", runManagementWorkspaceDoesNotContainSource)
	_ = register("ensure-work-dir-rejects-escaping-paths", runEnsureWorkDirRejectsEscapingPaths)
	_ = register("exec-spec-resolve-defaults", runExecSpecResolveDefaults)
	_ = register("exec-spec-rejects-invalid-input", runExecSpecRejectsInvalidInput)
	_ = register("exec-result-distinguishes-outcomes", runExecResultDistinguishesOutcomes)
	_ = register("decide-retention-follows-lifecycle-policy", runDecideRetentionFollowsLifecyclePolicy)
	_ = register("prepared-requires-complete-environment", runPreparedRequiresCompleteEnvironment)
	_ = register("provider-rejects-unsupported-kind", runProviderRejectsUnsupportedKind)
	_ = register("prepare-spec-validation", runPrepareSpecValidation)
	_ = register("provider-contract-is-implementable", runProviderContractIsImplementable)
	_ = register("environment-label-and-accessors", runEnvironmentLabelAndAccessors)
	_ = register("container-ref-validation", runContainerRefValidation)
	_ = register("prepared-failed-step", runPreparedFailedStep)
	_ = register("read-only-workspace-sets-read-only-project-access", runReadOnlyWorkspaceSetsReadOnlyProjectAccess)
	_ = register("read-only-workspace-requires-writable-session-dir", runReadOnlyWorkspaceRequiresWritableSessionDir)
	_ = register("read-only-workspace-rejects-overlapping-project-dir", runReadOnlyWorkspaceRejectsOverlappingProjectDir)
	_ = register("read-only-workspace-rejects-caches", runReadOnlyWorkspaceRejectsCaches)
	_ = register("read-only-workspace-keeps-workspace-writable", runReadOnlyWorkspaceKeepsWorkspaceWritable)
)
