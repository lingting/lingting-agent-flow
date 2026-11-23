// 本文件登记 tests/unit/source 的 21 条用例，顺序与源文件 cases_provider.go 一致，便于按包
// 核对迁移数量。
//
// 21 条源用例都带 t.Parallel()，因此全部按可并行条目登记；子测试仍是 Case 内的顺序子测试，
// 不单独成 Case。源文件 status_test.go 没有用例，只有命令结果状态的替身构造，迁移为
// helpers.go。
package source

var (
	_ = register("repository-url-escaping", runRepositoryURLEscaping)
	_ = register("invalid-repository-url", runInvalidRepositoryURL)
	_ = register("invalid-task-id", runInvalidTaskID)
	_ = register("container-guard-errors", runContainerGuardErrors)
	_ = register("detached-container-rejected", runDetachedContainerRejected)
	_ = register("git-unavailable", runGitUnavailable)
	_ = register("target-dir-not-empty", runTargetDirNotEmpty)
	_ = register("base-branch-selection-priority", runBaseBranchSelectionPriority)
	_ = register("base-branch-unrecognized", runBaseBranchUnrecognized)
	_ = register("explicit-base-branch", runExplicitBaseBranch)
	_ = register("explicit-base-branch-missing", runExplicitBaseBranchMissing)
	_ = register("create-task-branch", runCreateTaskBranch)
	_ = register("remote-task-branch-exists", runRemoteTaskBranchExists)
	_ = register("clone-failure-classification", runCloneFailureClassification)
	_ = register("clone-cancelled-and-timeout", runCloneCancelledAndTimeout)
	_ = register("uncertain-result-classification", runUncertainResultClassification)
	_ = register("branch-init-failure", runBranchInitFailure)
	_ = register("worktree-verification-failure", runWorktreeVerificationFailure)
	_ = register("branch-mismatch-detected", runBranchMismatchDetected)
	_ = register("concurrent-fetches-use-distinct-work-dirs", runConcurrentFetchesUseDistinctWorkDirs)
	_ = register("cancelled-context-rejected", runCancelledContextRejected)
)
