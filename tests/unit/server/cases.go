// 本文件登记 tests/unit/server 的 13 条用例，顺序与源文件一致，便于按包核对迁移数量。
//
// 12 条源用例带 t.Parallel()，按可并行条目登记；server-lock-path-matches-relative-path
// 源用例会切换进程工作目录且没有 t.Parallel()，按串行条目登记。
package server

var (
	// process_lock_test.go

	_ = register("process-lock-rejects-second-instance", runProcessLockRejectsSecondInstance)
	_ = register("server-lock-path-is-stable-for-same-database", runServerLockPathIsStableForSameDatabase)
	_ = registerSerial("server-lock-path-matches-relative-path", runServerLockPathMatchesRelativePath)
	_ = register("process-lock-unifies-database-symlink-alias", runProcessLockUnifiesDatabaseSymlinkAlias)
	_ = register(
		"process-lock-unifies-parent-directory-symlink-alias",
		runProcessLockUnifiesParentDirectorySymlinkAlias,
	)
	_ = register("process-lock-rejects-dangling-database-symlink", runProcessLockRejectsDanglingDatabaseSymlink)
	_ = register("process-lock-rejects-vanished-alias-target", runProcessLockRejectsVanishedAliasTarget)
	_ = register("server-run-rejects-symlink-alias-conflict", runServerRunRejectsSymlinkAliasConflict)
	_ = register("server-run-uses-normalized-database-path", runServerRunUsesNormalizedDatabasePath)
	_ = register("server-run-does-not-start-when-lock-held", runServerRunDoesNotStartWhenLockHeld)
	_ = register("server-run-rejects-dangling-database-symlink", runServerRunRejectsDanglingDatabaseSymlink)
	_ = register("server-run-releases-lock-when-startup-fails", runServerRunReleasesLockWhenStartupFails)

	// planning_recovery_test.go

	_ = register(
		"server-run-recovers-interrupted-planning-analysis",
		runServerRunRecoversInterruptedPlanningAnalysis,
	)
)
