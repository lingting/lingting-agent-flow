// 本文件登记 tests/unit/cigate 的 9 条用例，顺序与源文件 cases_policy.go 一致，便于按包
// 核对迁移数量。
//
// 9 条源用例都带 t.Parallel()，因此全部按可并行条目登记；子测试仍是 Case 内的顺序子测试，
// 不单独成 Case。
package cigate

var (
	_ = register("parse-mode", runParseMode)
	_ = register("parse-event", runParseEvent)
	_ = register("decide-push", runDecidePush)
	_ = register("decide-push-collaborator-uses-permission-check", runDecidePushCollaboratorUsesPermissionCheck)
	_ = register("decide-pull-request", runDecidePullRequest)
	_ = register("decide-pull-request-ignores-actor", runDecidePullRequestIgnoresActor)
	_ = register("decide-identity-missing", runDecideIdentityMissing)
	_ = register("decide-unsupported-events", runDecideUnsupportedEvents)
	_ = register("decision-reason-is-single-line", runDecisionReasonIsSingleLine)
)
