package codesaver

import (
	"context"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// push 把指定任务分支推送到远程仓库。
//
// 行为约定：
//   - 只推送任务分支，不推送其他分支、标签或工作区内容；
//   - 不使用 --force 或 --force-with-lease，远程历史冲突一律作为冲突返回；
//   - 推送结果不确定时先核验远程引用，不盲目重试可能产生副作用的上一条命令。
func (x *Session) push(ctx context.Context) error {
	if x.request.Operation == OperationMerge {
		return x.markPushNotAttempted(ctx)
	}
	switch x.record.CommitStatus {
	case domain.CommitStatusNoChanges, domain.CommitStatusNotAttempted:
		// 没有新提交时仍需确认远程是否已经包含当前任务分支的提交。
		return x.pushExisting(ctx)
	case domain.CommitStatusSucceeded:
		return x.pushCommit(ctx, x.record.CommitSHA)
	default:
		return newError(CodePushFailed, "push", "提交未成功，拒绝推送", nil)
	}
}

// markPushNotAttempted 记录本次没有尝试推送。
func (x *Session) markPushNotAttempted(ctx context.Context) error {
	if x.record.PushStatus != domain.PushStatusPending {
		return nil
	}
	x.record.Stage = "push"
	if err := x.record.MarkPush(domain.PushStatusNotAttempted, "", time.Now()); err != nil {
		return newError(CodePushFailed, "push", err.Error(), err)
	}
	return x.persist(ctx)
}

// pushExisting 在本次没有新提交时核验远程任务分支状态。
func (x *Session) pushExisting(ctx context.Context) error {
	head, err := x.headCommit(ctx)
	if err != nil {
		return err
	}
	remoteSHA, remoteErr := x.remoteBranchCommit(ctx, x.request.TaskBranch)
	if remoteErr == nil && remoteSHA == head {
		return x.markPushSucceeded(ctx, head)
	}
	return x.pushCommit(ctx, head)
}

// pushCommit 推送指定 commit 到远程任务分支并核验结果。
func (x *Session) pushCommit(ctx context.Context, commit string) error {
	x.record.Stage = "push"
	result, err := x.runner.run(
		ctx,
		x.saver.pushTimeout,
		"push", "--porcelain", x.request.RemoteName,
		x.request.TaskBranch+":refs/heads/"+x.request.TaskBranch,
	)
	if err != nil || !result.Succeeded() {
		return x.resolvePushFailure(ctx, commit, err, result)
	}
	// 推送命令成功仍要核验远程引用：只有远程确实指向预期 commit 才算成功。
	remoteSHA, verifyErr := x.remoteBranchCommit(ctx, x.request.TaskBranch)
	if verifyErr != nil {
		return x.markPushUnknown(ctx, commit, "推送后无法核验远程任务分支状态", verifyErr)
	}
	if remoteSHA != commit {
		return x.markPushConflict(
			ctx,
			remoteSHA,
			"远程任务分支指向 "+remoteSHA+"，与本地提交 "+commit+" 不一致",
		)
	}
	return x.markPushSucceeded(ctx, commit)
}

// resolvePushFailure 处理推送命令失败或结果不确定的情形。
func (x *Session) resolvePushFailure(
	ctx context.Context,
	commit string,
	cause error,
	result docker.CommandResult,
) error {
	code := classifyPushFailure(cause, result)
	switch code {
	case CodeTimeout, CodeInterrupted, CodeStateUncertain:
		// 远程可能已经接受推送：先核验远程引用，再决定状态。
		remoteSHA, verifyErr := x.remoteBranchCommit(ctx, x.request.TaskBranch)
		if verifyErr == nil {
			if remoteSHA == commit {
				return x.markPushSucceeded(ctx, commit)
			}
			if remoteSHA != "" {
				return x.markPushConflict(
					ctx,
					remoteSHA,
					"远程任务分支指向 "+remoteSHA+"，与本地提交 "+commit+" 不一致",
				)
			}
		}
		return x.markPushUnknown(ctx, commit, "推送结果无法确认，请先核验远程任务分支", cause)
	case CodePushConflict:
		remoteSHA, _ := x.remoteBranchCommit(ctx, x.request.TaskBranch)
		return x.markPushConflict(
			ctx,
			remoteSHA,
			"远程任务分支历史与本地不一致，需要人工处理（拒绝强制推送）",
		)
	case CodeRepositoryAuthFailed:
		return x.markPushFailed(ctx, commit, CodeRepositoryAuthFailed, "仓库认证失败或缺少可用凭据")
	default:
		return x.markPushFailed(
			ctx,
			commit,
			CodePushFailed,
			"推送失败: "+summarizeOutput(result),
		)
	}
}

// classifyPushFailure 把推送失败归类为可诊断的错误类别。
func classifyPushFailure(cause error, result docker.CommandResult) ErrorCode {
	if code := ErrorCodeOf(cause); code != CodeOK {
		return code
	}
	if result.TimedOut() {
		return CodeTimeout
	}
	if result.Cancelled() {
		return CodeInterrupted
	}
	if result.Uncertain() {
		return CodeStateUncertain
	}
	if isNonFastForward(result) {
		return CodePushConflict
	}
	if isAuthFailure(result) {
		return CodeRepositoryAuthFailed
	}
	return CodePushFailed
}

// isNonFastForward 判断推送失败是否由远程历史冲突导致。
func isNonFastForward(result docker.CommandResult) bool {
	combined := strings.ToLower(result.Stderr + "\n" + result.Stdout)
	for _, candidate := range []string{
		"non-fast-forward",
		"fetch first",
		"rejected",
		"stale info",
		"already exists",
	} {
		if strings.Contains(combined, candidate) {
			return true
		}
	}
	return false
}

// isAuthFailure 判断推送失败是否由认证问题导致。
func isAuthFailure(result docker.CommandResult) bool {
	combined := strings.ToLower(result.Stderr + "\n" + result.Stdout)
	for _, candidate := range []string{
		"authentication failed",
		"could not read username",
		"could not read password",
		"permission denied",
		"terminal prompts disabled",
		"access denied",
		"repository not found",
	} {
		if strings.Contains(combined, candidate) {
			return true
		}
	}
	return false
}

// remoteBranchCommit 读取远程分支当前指向的 commit。
//
// 使用 ls-remote 直接查询远端，而不是依赖可能过期的本地远程跟踪引用。
func (x *Session) remoteBranchCommit(ctx context.Context, branch string) (string, error) {
	out, err := x.runner.output(
		ctx,
		"push",
		CodeStateUncertain,
		"ls-remote", "--heads", x.request.RemoteName, "refs/heads/"+branch,
	)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] != "refs/heads/"+branch {
			continue
		}
		if !validCommit(fields[0]) {
			return "", newError(
				CodeStateUncertain,
				"push",
				"远程返回的 commit SHA 非法",
				nil,
			)
		}
		return fields[0], nil
	}
	return "", nil
}

// markPushSucceeded 记录推送成功并落盘。
func (x *Session) markPushSucceeded(ctx context.Context, sha string) error {
	x.record.Stage = "push"
	if err := x.record.MarkPush(domain.PushStatusSucceeded, sha, time.Now()); err != nil {
		return newError(CodePushFailed, "push", err.Error(), err)
	}
	return x.persist(ctx)
}

// markPushFailed 记录推送失败并落盘。
func (x *Session) markPushFailed(
	ctx context.Context,
	sha string,
	code ErrorCode,
	detail string,
) error {
	x.record.Stage = "push"
	if err := x.record.MarkPush(domain.PushStatusFailed, sha, time.Now()); err != nil {
		return newError(CodePushFailed, "push", err.Error(), err)
	}
	x.record.MarkError("push", statusForErrorCode(code), detail, time.Now())
	if err := x.persist(ctx); err != nil {
		return err
	}
	return newError(code, "push", detail, nil)
}

// markPushConflict 记录推送冲突并落盘。
func (x *Session) markPushConflict(ctx context.Context, sha, detail string) error {
	x.record.Stage = "push"
	if err := x.record.MarkPush(domain.PushStatusConflict, sha, time.Now()); err != nil {
		return newError(CodePushConflict, "push", err.Error(), err)
	}
	x.record.MarkError("push", statusForErrorCode(CodePushConflict), detail, time.Now())
	if err := x.persist(ctx); err != nil {
		return err
	}
	return newError(CodePushConflict, "push", detail, nil)
}

// markPushUnknown 记录推送结果不确定并落盘。
func (x *Session) markPushUnknown(
	ctx context.Context,
	sha string,
	detail string,
	cause error,
) error {
	x.record.Stage = "push"
	if err := x.record.MarkPush(domain.PushStatusUnknown, sha, time.Now()); err != nil {
		return newError(CodeStateUncertain, "push", err.Error(), err)
	}
	x.record.MarkError("push", statusForErrorCode(CodeStateUncertain), detail, time.Now())
	if err := x.persist(ctx); err != nil {
		return err
	}
	return newError(CodeStateUncertain, "push", detail, cause)
}
