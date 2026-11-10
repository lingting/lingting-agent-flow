// 本文件登记 tests/integration/local/session 的 7 条用例，顺序与源文件一致，
// 便于与 R0 冻结清单逐条对账。
//
// 用例主体在 cases_session.go 里；本包不分配编排进程资源，因此没有资源声明。
package session

var (
	_ = register("session-transferred-completely", runSessionTransferredCompletely)
	_ = register("large-session-not-loaded-into-memory", runLargeSessionNotLoadedIntoMemory)
	_ = register("interrupted-transfer-keeps-partial-file", runInterruptedTransferKeepsPartialFile)
	_ = register("export-failure-keeps-jsonl", runExportFailureKeepsJSONL)
	_ = register("concurrent-sessions-do-not-overwrite", runConcurrentSessionsDoNotOverwrite)
	_ = register("session-survives-container-removal", runSessionSurvivesContainerRemoval)
	_ = register("incomplete-session-not-marked-complete", runIncompleteSessionNotMarkedComplete)
)
