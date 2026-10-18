package codesaver

import (
	"context"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 提交阶段使用的固定参数。
const (
	// authorNameKey 是单条命令生效的作者姓名配置键。
	authorNameKey = "user.name"
	// authorEmailKey 是单条命令生效的作者邮箱配置键。
	authorEmailKey = "user.email"
)

// commit 检查并提交任务产生的变更。
//
// 行为约定：
//   - 工作区存在未解决的冲突时直接拒绝，不自动选择任何一方内容；
//   - 暂存区已有内容时不重置它，而是把全部非忽略变更一并提交，并在记录中标注；
//   - 没有变更时记录 no_changes，不创建空提交；
//   - 提交后重新读取 HEAD：命令异常但 HEAD 已变化时按成功处理，不重复提交。
func (x *Session) commit(ctx context.Context, state WorktreeState) error {
	if x.request.Operation != OperationSave {
		return x.markCommitNotAttempted(ctx)
	}
	if hasConflicts(state) {
		return newError(
			CodeUnresolvedConflict,
			"commit",
			"工作区存在未解决的合并冲突，拒绝自动提交或推送",
			nil,
		)
	}
	refreshed, err := x.refreshStatus(ctx)
	if err != nil {
		return err
	}
	// 重新读取的条目用于判断变更，HEAD 仍以核验阶段的结果为基准。
	refreshed.Head = state.Head
	state = refreshed
	if state.Clean {
		x.record.Stage = "commit"
		if markErr := x.record.MarkCommit(
			domain.CommitStatusNoChanges,
			"",
			x.request.CommitMessage,
			time.Now(),
		); markErr != nil {
			return newError(CodeCommitFailed, "commit", markErr.Error(), markErr)
		}
		return x.persist(ctx)
	}
	author, err := x.resolveAuthor(ctx)
	if err != nil {
		return err
	}
	if err := x.stageAll(ctx); err != nil {
		return err
	}
	return x.runCommit(ctx, state, author)
}

// markCommitNotAttempted 记录本次没有尝试提交。
func (x *Session) markCommitNotAttempted(ctx context.Context) error {
	if x.record.CommitStatus != domain.CommitStatusPending {
		return nil
	}
	x.record.Stage = "commit"
	if err := x.record.MarkCommit(
		domain.CommitStatusNotAttempted,
		"",
		"",
		time.Now(),
	); err != nil {
		return newError(CodeCommitFailed, "commit", err.Error(), err)
	}
	return x.persist(ctx)
}

// refreshStatus 重新读取工作区状态。
//
// 提交前的判定必须基于最新状态：前一步读取与提交之间可能有其他进程改动工作区。
func (x *Session) refreshStatus(ctx context.Context) (WorktreeState, error) {
	raw, err := x.runner.output(
		ctx,
		"commit",
		CodeWorktreeInvalid,
		"status", "--porcelain=v1", "-z",
	)
	if err != nil {
		return WorktreeState{}, err
	}
	return parseStatus(raw)
}

// stageAll 把全部非忽略变更加入暂存区。
//
// 使用 `git add --all`：它不删除未知文件、不重置暂存区，也不会改写已有提交。
func (x *Session) stageAll(ctx context.Context) error {
	result, err := x.runner.run(ctx, x.saver.commitTimeout, "add", "--all")
	if err != nil {
		return classifyExecError("commit", err, result)
	}
	if !result.Succeeded() {
		return newError(
			CodeCommitFailed,
			"commit",
			"暂存变更失败: "+summarizeOutput(result),
			nil,
		)
	}
	return nil
}

// resolveAuthor 确定本次提交使用的作者身份。
//
// 优先使用仓库中已配置且完整的身份；配置缺失时才使用调用方显式给出的身份，
// 并通过一次性 -c 参数生效，不修改仓库或全局 Git 配置。两者都不可用时明确报错，
// 绝不虚构姓名或邮箱。
func (x *Session) resolveAuthor(ctx context.Context) (domain.GitAuthor, error) {
	if x.request.Author.IsZero() {
		if _, err := x.repoConfig(ctx, authorNameKey); err != nil {
			return domain.GitAuthor{}, newError(
				CodeAuthorMissing,
				"commit",
				"仓库没有配置提交身份（user.name / user.email），请在任务或项目配置中提供作者信息",
				err,
			)
		}
		if _, err := x.repoConfig(ctx, authorEmailKey); err != nil {
			return domain.GitAuthor{}, newError(
				CodeAuthorMissing,
				"commit",
				"仓库没有配置提交身份（user.name / user.email），请在任务或项目配置中提供作者信息",
				err,
			)
		}
		return domain.GitAuthor{}, nil
	}
	return x.request.Author, nil
}

// repoConfig 读取仓库内的 Git 配置项。
func (x *Session) repoConfig(ctx context.Context, key string) (string, error) {
	value, err := x.runner.output(ctx, "commit", CodeAuthorMissing, "config", "--get", key)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", newError(CodeAuthorMissing, "commit", "Git 配置项为空: "+key, nil)
	}
	return trimmed, nil
}

// runCommit 执行提交并核验结果。
func (x *Session) runCommit(
	ctx context.Context,
	before WorktreeState,
	author domain.GitAuthor,
) error {
	args := []string{}
	if !author.IsZero() {
		args = append(
			args,
			"-c", authorNameKey+"="+author.Name,
			"-c", authorEmailKey+"="+author.Email,
		)
	}
	args = append(args, "commit", "--no-verify", "-m", x.request.CommitMessage)
	result, err := x.runner.run(ctx, x.saver.commitTimeout, args...)
	after, headErr := x.headCommit(ctx)
	if headErr != nil {
		// HEAD 无法读取：提交结果无法确认，必须先核验仓库状态。
		x.record.Stage = "commit"
		if markErr := x.record.MarkCommit(
			domain.CommitStatusUnknown,
			"",
			x.request.CommitMessage,
			time.Now(),
		); markErr != nil {
			return newError(CodeCommitFailed, "commit", markErr.Error(), markErr)
		}
		if persistErr := x.persist(ctx); persistErr != nil {
			return persistErr
		}
		return newError(
			CodeStateUncertain,
			"commit",
			"提交后无法读取 HEAD，请先核验容器内仓库状态",
			headErr,
		)
	}
	if after != before.Head {
		// HEAD 已变化：以实际仓库状态为准，不再重复创建提交。
		return x.markCommitSucceeded(ctx, after)
	}
	if err != nil {
		return classifyExecError("commit", err, result)
	}
	if !result.Succeeded() {
		x.record.Stage = "commit"
		if markErr := x.record.MarkCommit(
			domain.CommitStatusFailed,
			"",
			x.request.CommitMessage,
			time.Now(),
		); markErr != nil {
			return newError(CodeCommitFailed, "commit", markErr.Error(), markErr)
		}
		if persistErr := x.persist(ctx); persistErr != nil {
			return persistErr
		}
		return newError(
			CodeCommitFailed,
			"commit",
			"提交失败且 HEAD 未变化: "+summarizeOutput(result),
			nil,
		)
	}
	return newError(
		CodeCommitFailed,
		"commit",
		"提交命令成功但 HEAD 未变化，请核验容器内仓库状态",
		nil,
	)
}

// headCommit 读取当前 HEAD 指向的 commit SHA。
func (x *Session) headCommit(ctx context.Context) (string, error) {
	out, err := x.runner.output(ctx, "commit", CodeWorktreeInvalid, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(out)
	if !validCommit(trimmed) {
		return "", newError(
			CodeWorktreeInvalid,
			"commit",
			"rev-parse HEAD 没有返回有效的 commit SHA",
			nil,
		)
	}
	return trimmed, nil
}

// markCommitSucceeded 记录提交成功并落盘。
func (x *Session) markCommitSucceeded(ctx context.Context, sha string) error {
	x.record.Stage = "commit"
	if err := x.record.MarkCommit(
		domain.CommitStatusSucceeded,
		sha,
		x.request.CommitMessage,
		time.Now(),
	); err != nil {
		return newError(CodeCommitFailed, "commit", err.Error(), err)
	}
	return x.persist(ctx)
}
