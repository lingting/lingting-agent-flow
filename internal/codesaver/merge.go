package codesaver

import (
	"context"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// merge 按配置把任务分支合并到基础分支。
//
// 前置条件全部满足时才执行：提交结果确定、任务分支推送成功且远程状态已核验、
// 基础分支可识别、工作区没有未解决冲突、合并已被显式授权。任何条件不满足时都
// 不隐式执行合并，而是记录跳过或拒绝的原因。
func (x *Session) merge(ctx context.Context) error {
	if !x.mergeRequested() {
		if x.record.MergeStatus == domain.MergeStatusPending {
			x.record.Stage = "merge"
			if err := x.record.MarkMerge(
				domain.MergeStatusNotRequested,
				"",
				time.Now(),
			); err != nil {
				return newError(CodeMergeNotAllowed, "merge", err.Error(), err)
			}
			return x.persist(ctx)
		}
		return nil
	}
	if x.record.MergeStatus == domain.MergeStatusNotRequested {
		// 记录创建时未声明合并，但调用方在本次请求中要求合并。
		x.record.MergeEnabled = true
		x.record.MergeStatus = domain.MergeStatusPending
	}
	if err := x.checkMergePreconditions(ctx); err != nil {
		return x.skipMerge(ctx, err)
	}
	return x.runMerge(ctx)
}

// mergeRequested 判断本次调用是否请求了基础分支合并。
//
// 默认不合并：只有显式执行 merge 操作，或在保存流程中显式开启 merge_after_push
// 时才请求合并。开启代码保存本身不代表用户同意合并主分支。
func (x *Session) mergeRequested() bool {
	switch x.request.Operation {
	case OperationMerge:
		return true
	case OperationSave:
		return x.request.MergeAfterPush
	default:
		return false
	}
}

// skipMerge 记录合并没有执行的原因。
//
// 合并没有真正执行时推送阶段也不会继续，因此同时把仍然 pending 的推送状态标记为
// 未尝试，避免记录中出现无法解释的中间状态。
func (x *Session) skipMerge(ctx context.Context, err error) error {
	if abandonErr := x.abandonPush(ctx); abandonErr != nil {
		return abandonErr
	}
	code := ErrorCodeOf(err)
	x.record.Stage = "merge"
	status := mergeStatusForError(code)
	if status == domain.MergeStatusSkipped {
		if markErr := x.record.MarkMerge(status, "", time.Now()); markErr != nil {
			return newError(CodeMergeNotAllowed, "merge", markErr.Error(), markErr)
		}
	}
	x.record.MarkError("merge", statusForErrorCode(code), errorDetail(err), time.Now())
	if persistErr := x.persist(ctx); persistErr != nil {
		return persistErr
	}
	return err
}

// checkMergePreconditions 校验合并的前置条件。
func (x *Session) checkMergePreconditions(ctx context.Context) error {
	switch x.record.CommitStatus {
	case domain.CommitStatusSucceeded, domain.CommitStatusNoChanges:
		// 提交结果已确定：成功或确认没有新变更，都可以继续合并。
	default:
		return newError(
			CodeMergeNotAllowed,
			"merge",
			"提交结果未确定，禁止合并基础分支",
			nil,
		)
	}
	if x.record.PushStatus != domain.PushStatusSucceeded {
		return newError(
			CodeMergeNotAllowed,
			"merge",
			"任务分支尚未确认推送成功，禁止合并基础分支",
			nil,
		)
	}
	if strings.TrimSpace(x.base.Name) == "" {
		return newError(
			CodeBaseBranchMissing,
			"merge",
			"无法可靠识别基础分支，请显式指定",
			nil,
		)
	}
	state, err := x.refreshStatus(ctx)
	if err != nil {
		return err
	}
	if hasConflicts(state) {
		return newError(
			CodeUnresolvedConflict,
			"merge",
			"工作区存在未解决的冲突，禁止自动合并",
			nil,
		)
	}
	return nil
}

// runMerge 执行一次可预测、可审计的合并流程。
//
// 步骤：刷新远程引用 → 核实基础分支状态 → 准备基础分支 → 合并任务分支 →
// 推送基础分支 → 核验远程基础分支 commit。
func (x *Session) runMerge(ctx context.Context) error {
	if err := x.fetchRemote(ctx); err != nil {
		return x.failMerge(ctx, err)
	}
	remoteSHA, err := x.remoteBranchCommit(ctx, x.base.Name)
	if err != nil {
		return x.failMerge(ctx, newError(
			CodeStateUncertain,
			"merge",
			"无法核实远程基础分支状态",
			err,
		))
	}
	if remoteSHA == "" {
		return x.failMerge(ctx, newError(
			CodeBaseBranchMissing,
			"merge",
			"远程基础分支不存在: "+x.request.RemoteName+"/"+x.base.Name,
			nil,
		))
	}
	x.base.RemoteCommit = remoteSHA
	if err := x.prepareBaseBranch(ctx, remoteSHA); err != nil {
		return x.failMerge(ctx, err)
	}
	if err := x.mergeTaskBranch(ctx); err != nil {
		return x.failMerge(ctx, err)
	}
	mergeSHA, err := x.headCommit(ctx)
	if err != nil {
		return x.markMergeUnknown(ctx, "", "合并后无法读取 HEAD", err)
	}
	return x.pushBaseBranch(ctx, mergeSHA)
}

// fetchRemote 刷新远程引用。
func (x *Session) fetchRemote(ctx context.Context) error {
	result, err := x.runner.run(
		ctx,
		x.saver.mergeTimeout,
		"fetch", "--prune", "--no-tags", x.request.RemoteName,
	)
	if err != nil {
		return classifyExecError("merge", err, result)
	}
	if !result.Succeeded() {
		return newError(
			CodeMergeFailed,
			"merge",
			"刷新远程引用失败: "+summarizeOutput(result),
			nil,
		)
	}
	return nil
}

// prepareBaseBranch 在本地准备基础分支，使其与远程基础分支状态一致。
//
// 本地分支存在时直接检出，不强制重置：如果本地基础分支落后，后续合并会以实际
// 刷新后的状态为准。本地分支不存在时从远程跟踪引用创建。
func (x *Session) prepareBaseBranch(ctx context.Context, remoteSHA string) error {
	exists := x.base.Local
	if !exists {
		_, err := x.runner.output(
			ctx,
			"merge",
			CodeBaseBranchMissing,
			"rev-parse", "--verify", "--quiet", "refs/heads/"+x.base.Name+"^{commit}",
		)
		exists = err == nil
	}
	var (
		args = []string{"checkout", "--no-force"}
		err  error
	)
	if exists {
		args = append(args, x.base.Name)
	} else {
		args = append(
			args,
			"-b", x.base.Name,
			"--track", x.request.RemoteName+"/"+x.base.Name,
		)
	}
	result, err := x.runner.run(ctx, x.saver.mergeTimeout, args...)
	if err != nil {
		return classifyExecError("merge", err, result)
	}
	if !result.Succeeded() {
		return newError(
			CodeMergeFailed,
			"merge",
			"准备基础分支失败: "+summarizeOutput(result),
			nil,
		)
	}
	// 让本地基础分支快进到远程状态：只允许快进，不使用强制重置。
	if err := x.fastForwardBase(ctx, remoteSHA); err != nil {
		return err
	}
	return nil
}

// fastForwardBase 在本地基础分支落后时快进到远程基础分支。
func (x *Session) fastForwardBase(ctx context.Context, remoteSHA string) error {
	current, err := x.headCommit(ctx)
	if err != nil {
		return err
	}
	if current == remoteSHA {
		return nil
	}
	result, mergeErr := x.runner.run(
		ctx,
		x.saver.mergeTimeout,
		"merge", "--ff-only", "--no-edit", remoteSHA,
	)
	if mergeErr != nil {
		return classifyExecError("merge", mergeErr, result)
	}
	if !result.Succeeded() {
		return newError(
			CodeMergeFailed,
			"merge",
			"本地基础分支无法快进到远程状态: "+summarizeOutput(result),
			nil,
		)
	}
	return nil
}

// mergeTaskBranch 把任务分支合并到当前基础分支。
//
// 使用 --no-ff 创建可审计的合并提交；出现冲突时中止合并并保留现场，不自动选择
// 任何一方内容，也不伪造成功合并。
func (x *Session) mergeTaskBranch(ctx context.Context) error {
	result, err := x.runner.run(
		ctx,
		x.saver.mergeTimeout,
		"merge", "--no-ff", "--no-edit", "-m",
		"AgentFlow: merge "+x.request.TaskBranch+" into "+x.base.Name,
		x.request.TaskBranch,
	)
	if err != nil {
		if code := ErrorCodeOf(err); code != CodeOK {
			if code == CodeTimeout || code == CodeInterrupted || code == CodeStateUncertain {
				return newError(code, "merge", "合并命令未能确认结束", err)
			}
		}
		return classifyExecError("merge", err, result)
	}
	if result.Succeeded() {
		return nil
	}
	if isMergeConflict(result) {
		// 中止合并，使工作区回到可检查的干净状态；不重置整个仓库。
		abort, abortErr := x.runner.run(ctx, x.saver.mergeTimeout, "merge", "--abort")
		detail := "合并基础分支出现冲突，已中止自动合并并保留任务分支与已推送的提交"
		if abortErr != nil || !abort.Succeeded() {
			detail += "；中止合并失败，请人工检查容器内工作区"
		}
		return newError(CodeMergeConflict, "merge", detail, nil)
	}
	return newError(
		CodeMergeFailed,
		"merge",
		"合并失败: "+summarizeOutput(result),
		nil,
	)
}

// isMergeConflict 判断合并失败是否由冲突导致。
func isMergeConflict(result docker.CommandResult) bool {
	combined := strings.ToLower(summarizeOutput(result))
	for _, candidate := range []string{"conflict", "automatic merge failed", "fix conflicts"} {
		if strings.Contains(combined, candidate) {
			return true
		}
	}
	return false
}

// pushBaseBranch 推送更新后的基础分支并核验远程 commit。
func (x *Session) pushBaseBranch(ctx context.Context, mergeSHA string) error {
	x.record.Stage = "merge"
	result, err := x.runner.run(
		ctx,
		x.saver.mergeTimeout,
		"push", "--porcelain", x.request.RemoteName,
		x.base.Name+":refs/heads/"+x.base.Name,
	)
	if err != nil {
		return x.resolveMergePushFailure(ctx, mergeSHA, err, result)
	}
	if !result.Succeeded() {
		return x.resolveMergePushFailure(ctx, mergeSHA, nil, result)
	}
	remoteSHA, verifyErr := x.remoteBranchCommit(ctx, x.base.Name)
	if verifyErr != nil {
		return x.markMergeUnknown(ctx, mergeSHA, "合并后无法核验远程基础分支状态", verifyErr)
	}
	if remoteSHA != mergeSHA {
		return x.markMergeUnknown(
			ctx,
			mergeSHA,
			"远程基础分支指向 "+remoteSHA+"，与本地合并结果 "+mergeSHA+" 不一致",
			nil,
		)
	}
	return x.markMergeSucceeded(ctx, mergeSHA)
}

// resolveMergePushFailure 处理基础分支推送失败或结果不确定的情形。
func (x *Session) resolveMergePushFailure(
	ctx context.Context,
	mergeSHA string,
	cause error,
	result docker.CommandResult,
) error {
	code := classifyPushFailure(cause, result)
	if code == CodeTimeout || code == CodeInterrupted || code == CodeStateUncertain {
		remoteSHA, verifyErr := x.remoteBranchCommit(ctx, x.base.Name)
		if verifyErr == nil && remoteSHA == mergeSHA {
			return x.markMergeSucceeded(ctx, mergeSHA)
		}
		return x.markMergeUnknown(ctx, mergeSHA, "基础分支推送结果无法确认，请先核验远程状态", cause)
	}
	detail := "基础分支推送失败"
	if cause != nil {
		detail += ": " + cause.Error()
	} else {
		detail += ": " + summarizeOutput(result)
	}
	return x.markMergeFailed(ctx, mergeSHA, detail)
}

// markMergeSucceeded 记录合并成功并落盘。
func (x *Session) markMergeSucceeded(ctx context.Context, mergeSHA string) error {
	x.record.Stage = "merge"
	if err := x.record.MarkMerge(domain.MergeStatusSucceeded, mergeSHA, time.Now()); err != nil {
		return newError(CodeMergeFailed, "merge", err.Error(), err)
	}
	return x.persist(ctx)
}

// markMergeFailed 记录合并失败并落盘。
func (x *Session) markMergeFailed(ctx context.Context, mergeSHA, detail string) error {
	x.record.Stage = "merge"
	if err := x.record.MarkMerge(domain.MergeStatusFailed, mergeSHA, time.Now()); err != nil {
		return newError(CodeMergeFailed, "merge", err.Error(), err)
	}
	x.record.MarkError("merge", statusForErrorCode(CodeMergeFailed), detail, time.Now())
	if persistErr := x.persist(ctx); persistErr != nil {
		return persistErr
	}
	return newError(CodeMergeFailed, "merge", detail, nil)
}

// markMergeUnknown 记录合并结果不确定并落盘。
func (x *Session) markMergeUnknown(
	ctx context.Context,
	mergeSHA string,
	detail string,
	cause error,
) error {
	x.record.Stage = "merge"
	if err := x.record.MarkMerge(domain.MergeStatusUnknown, mergeSHA, time.Now()); err != nil {
		return newError(CodeStateUncertain, "merge", err.Error(), err)
	}
	x.record.MarkError("merge", statusForErrorCode(CodeStateUncertain), detail, time.Now())
	if persistErr := x.persist(ctx); persistErr != nil {
		return persistErr
	}
	return newError(CodeStateUncertain, "merge", detail, cause)
}

// failMerge 记录合并失败或冲突并落盘。
func (x *Session) failMerge(ctx context.Context, err error) error {
	code := ErrorCodeOf(err)
	x.record.Stage = "merge"
	status := mergeStatusForError(code)
	detail := errorDetail(err)
	if markErr := x.record.MarkMerge(status, "", time.Now()); markErr != nil {
		// 状态迁移不被允许时仍然记录错误摘要，避免丢失诊断信息。
		x.record.MarkError("merge", statusForErrorCode(code), detail, time.Now())
		if persistErr := x.persist(ctx); persistErr != nil {
			return persistErr
		}
		return err
	}
	x.record.MarkError("merge", statusForErrorCode(code), detail, time.Now())
	if persistErr := x.persist(ctx); persistErr != nil {
		return persistErr
	}
	return err
}
