package codesaver

import (
	"context"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// resolveBase 识别基础分支并读取其远程 commit。
//
// 优先使用显式指定的分支；未指定时按 origin/HEAD、main、master 的顺序识别。
// 只使用本地已有的引用与一次远程查询，不进行不必要的网络访问。无法可靠确定时
// 返回错误，绝不把任意当前分支静默当作基础分支。
func (x *Session) resolveBase(ctx context.Context) (BaseBranch, error) {
	if explicit := strings.TrimSpace(x.request.BaseBranch); explicit != "" {
		base, err := x.loadBase(ctx, explicit)
		if err != nil {
			return BaseBranch{}, newError(
				CodeBaseBranchMissing,
				"base",
				"指定的基础分支不可用: "+explicit,
				err,
			)
		}
		base.Selection = domain.GitBranchSelectionExplicit
		return base, nil
	}
	originHead, headErr := x.runner.output(
		ctx,
		"base",
		CodeBaseBranchMissing,
		"symbolic-ref", "--short", domain.OriginHeadRef,
	)
	candidates, candidatesErr := x.remoteBranches(ctx)
	if candidatesErr != nil {
		return BaseBranch{}, candidatesErr
	}
	originHeadValue := ""
	if headErr == nil {
		originHeadValue = strings.TrimSpace(originHead)
	}
	name, selection, err := domain.SelectBaseBranch(originHeadValue, candidates)
	if err != nil {
		return BaseBranch{}, newError(
			CodeBaseBranchMissing,
			"base",
			err.Error(),
			err,
		)
	}
	base, err := x.loadBase(ctx, name)
	if err != nil {
		return BaseBranch{}, newError(
			CodeBaseBranchMissing,
			"base",
			"无法读取基础分支状态: "+name,
			err,
		)
	}
	base.Selection = selection
	return base, nil
}

// remoteBranches 列出本地已知的远程分支名。
func (x *Session) remoteBranches(ctx context.Context) ([]string, error) {
	out, err := x.runner.output(
		ctx,
		"base",
		CodeBaseBranchMissing,
		"for-each-ref",
		"--format=%(refname:short)",
		"refs/remotes/"+x.request.RemoteName+"/",
	)
	if err != nil {
		return nil, err
	}
	branches := make([]string, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasSuffix(trimmed, "/HEAD") {
			continue
		}
		branches = append(branches, trimRefs(trimmed, x.request.RemoteName))
	}
	return branches, nil
}

// loadBase 读取基础分支的本地存在性与远程 commit。
func (x *Session) loadBase(ctx context.Context, name string) (BaseBranch, error) {
	if err := domain.ValidateGitBranchName(name); err != nil {
		return BaseBranch{}, err
	}
	base := BaseBranch{
		Name:   name,
		Remote: x.request.RemoteName + "/" + name,
	}
	if _, err := x.runner.output(
		ctx,
		"base",
		CodeBaseBranchMissing,
		"rev-parse", "--verify", "--quiet", "refs/heads/"+name+"^{commit}",
	); err == nil {
		base.Local = true
	}
	remoteSHA, err := x.remoteBranchCommit(ctx, name)
	if err != nil {
		return BaseBranch{}, err
	}
	if remoteSHA != "" {
		base.RemoteCommit = remoteSHA
	}
	return base, nil
}
