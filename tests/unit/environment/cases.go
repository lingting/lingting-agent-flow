// 本文件登记 tests/unit/environment 的 11 条用例，顺序与源文件一致，便于按包核对迁移数量。
//
// 11 条源用例都带 t.Parallel()，因此全部按可并行条目登记。
package environment

var (
	// environment_test.go

	_ = register("new-environment-ref", runNewEnvironmentRef)
	_ = register("version-label-formatting", runVersionLabelFormatting)
	_ = register("next-version-number-is-monotonic", runNextVersionNumberIsMonotonic)
	_ = register("environment-version-validation", runEnvironmentVersionValidation)
	_ = register("version-build-transitions", runVersionBuildTransitions)
	_ = register("version-failure-keeps-version-unusable", runVersionFailureKeepsVersionUnusable)
	_ = register("validate-image-ref-rejects-latest", runValidateImageRefRejectsLatest)
	_ = register("validate-image-id", runValidateImageID)

	// reusable_version_test.go

	_ = register("find-reusable-version-requires-identical-content", runFindReusableVersionRequiresIdenticalContent)
	_ = register("find-reusable-version-ignores-non-succeeded-records", runFindReusableVersionIgnoresNonSucceededRecords)
	_ = register("mark-uncertain-keeps-version-unusable", runMarkUncertainKeepsVersionUnusable)
)
