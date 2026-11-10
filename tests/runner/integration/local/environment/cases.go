// 本文件登记 tests/integration/local/environment 的 7 条用例，顺序与源文件一致，
// 便于与 R0 冻结清单逐条对账。
//
// 用例主体在 cases_environment.go 与 cases_host_gate.go 里；只有真正打开数据库的用例
// 声明 sqlite-db，纯文件替身与纯判定入口的用例不声明资源。
package environment

var (
	// environment_test.go（3）
	_ = register("version-files-are-not-overwritten", runVersionFilesAreNotOverwritten)
	_ = register("manifest-round-trip-on-disk", runManifestRoundTripOnDisk)
	_ = register("context-upload-and-cleanup", runContextUploadAndCleanup)

	// host_gate_test.go（4）
	_ = register("remote-operations-reject-unvalidated-host", runRemoteOperationsRejectUnvalidatedHost, databaseDecl()...)
	_ = register("disabled-host-is-rejected", runDisabledHostIsRejected, databaseDecl()...)
	_ = register("read-only-preflight-bypasses-gate", runReadOnlyPreflightBypassesGate, databaseDecl()...)
	_ = register("ensure-host-usable-for-remote-task-is-shared", runEnsureHostUsableForRemoteTaskIsShared)
)
