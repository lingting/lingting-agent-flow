// 本文件登记 tests/unit/image 的 10 条用例，顺序与源文件 cases_image.go 一致，便于按包
// 核对迁移数量。
//
// 10 条源用例都带 t.Parallel()，因此全部按可并行条目登记。
package image

var (
	_ = register("context-hash-is-deterministic", runContextHashIsDeterministic)
	_ = register("context-rejects-unsafe-paths", runContextRejectsUnsafePaths)
	_ = register("context-rejects-sensitive-content", runContextRejectsSensitiveContent)
	_ = register("read-context-directory-skips-temporary-files", runReadContextDirectorySkipsTemporaryFiles)
	_ = register("build-config-hash-covers-all-inputs", runBuildConfigHashCoversAllInputs)
	_ = register("image-ref-for-is-traceable", runImageRefForIsTraceable)
	_ = register("validate-dockerfile", runValidateDockerfile)
	_ = register("default-base-dockerfile-has-no-latest-or-secrets", runDefaultBaseDockerfileHasNoLatestOrSecrets)
	_ = register("language-template-lookup", runLanguageTemplateLookup)
	_ = register("default-base-dockerfile-maps-github-to-port443", runDefaultBaseDockerfileMapsGitHubToPort443)
)
