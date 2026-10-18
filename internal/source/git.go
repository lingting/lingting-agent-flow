package source

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// Git 可执行文件名与固定参数。
const (
	// gitExecutable 是容器内 Git 命令名。
	gitExecutable = "git"
	// gitDirSuffix 是 Git 元数据目录名。
	gitDirSuffix = ".git"
)

// clone 在容器内执行 Git Clone。
//
// 前置检查：目标目录必须为空或不存在，容器内必须存在可用的 Git。
// 不使用浅克隆：后续分支操作与历史检查可能依赖完整历史。
// 失败时不删除目录：目录中可能已经有用户代码，自动删除会掩盖问题。
func (p *Provider) clone(ctx context.Context, item prepared) error {
	if err := p.ensureGitAvailable(ctx, item); err != nil {
		return err
	}
	if err := p.ensureTargetEmpty(ctx, item); err != nil {
		return err
	}
	if err := p.ensureRepositoryAccess(ctx, item); err != nil {
		return err
	}
	if err := p.ensureParentDir(ctx, item); err != nil {
		return err
	}
	command := []string{
		gitExecutable, "clone",
		"--no-hardlinks",
		"--",
		item.repository.Raw(),
		item.workDir,
	}
	run, err := p.run(ctx, item, "git clone", command, item.timeout)
	if err != nil {
		return p.classifyCloneError(item, run, err)
	}
	if !run.Succeeded() {
		return p.classifyCloneError(item, run, nil)
	}
	return nil
}

// classifyCloneError 把 Clone 失败归类为可诊断的错误类别。
//
// 取消与超时优先于输出判断：远程执行被中断时输出往往不完整，不能据此推断
// 仓库真的不可访问。
func (p *Provider) classifyCloneError(
	item prepared,
	run docker.CommandResult,
	cause error,
) error {
	if cause != nil {
		switch ErrorCodeOf(cause) {
		case CodeCloneInterrupted, CodeCloneTimeout, CodeStateUncertain:
			// 这些状态由执行层给出，不能再被输出内容推断覆盖。
			return cause
		}
	}
	if run.TimedOut() {
		return newError(
			CodeCloneTimeout,
			"git clone",
			"Clone 超时: "+item.repository.Display(),
			cause,
		)
	}
	if run.Cancelled() {
		return newError(
			CodeCloneInterrupted,
			"git clone",
			"Clone 被调用方取消: "+item.repository.Display(),
			cause,
		)
	}
	combined := strings.ToLower(run.Stderr + "\n" + run.Stdout)
	switch {
	case needsAuthentication(combined):
		return newError(
			CodeRepositoryAuthRequired,
			"git clone",
			"仓库需要认证信息，当前没有配置安全的 Git 凭据: "+
				item.repository.Display(),
			cause,
		)
	case isRepositoryAccessFailure(combined):
		return newError(
			CodeRepositoryAccessFailed,
			"git clone",
			"无法访问仓库: "+item.repository.Display()+" | "+output(run),
			cause,
		)
	default:
		return newError(
			CodeRepositoryAccessFailed,
			"git clone",
			"Clone 失败: "+item.repository.Display()+" | "+output(run),
			cause,
		)
	}
}

// needsAuthentication 判断 Git 输出是否表示缺少认证信息。
func needsAuthentication(message string) bool {
	candidates := []string{
		"authentication failed",
		"could not read username",
		"could not read password",
		"permission denied (publickey)",
		"terminal prompts disabled",
		"support for password authentication was removed",
		"invalid username or password",
		"access denied",
		"repository not found",
	}
	for _, candidate := range candidates {
		if strings.Contains(message, candidate) {
			return true
		}
	}
	return false
}

// isRepositoryAccessFailure 判断 Git 输出是否表示网络或仓库不可访问。
func isRepositoryAccessFailure(message string) bool {
	candidates := []string{
		"could not resolve host",
		"connection refused",
		"connection timed out",
		"network is unreachable",
		"does not appear to be a git repository",
		"repository '",
		"not found",
		"unable to access",
	}
	for _, candidate := range candidates {
		if strings.Contains(message, candidate) {
			return true
		}
	}
	return false
}

// ensureGitAvailable 检查容器内是否存在可用的 Git。
func (p *Provider) ensureGitAvailable(ctx context.Context, item prepared) error {
	command := []string{gitExecutable, "--version"}
	run, err := p.run(ctx, item, "git --version", command, p.probeTimeout)
	if err != nil {
		if ErrorCodeOf(err) == CodeStateUncertain ||
			ErrorCodeOf(err) == CodeCloneInterrupted ||
			ErrorCodeOf(err) == CodeCloneTimeout {
			return err
		}
		return newError(
			CodeGitUnavailable,
			"git --version",
			"容器内没有可用的 Git: "+output(run),
			err,
		)
	}
	if !run.Succeeded() || strings.TrimSpace(run.Stdout) == "" {
		return newError(
			CodeGitUnavailable,
			"git --version",
			"容器内没有可用的 Git: "+output(run),
			nil,
		)
	}
	return nil
}

// ensureTargetEmpty 检查目标目录不存在或为空。
//
// 目录中已有内容时直接失败：目录里可能已有用户代码，自动递归删除会掩盖问题，
// 也会让重试覆盖未知内容。
func (p *Provider) ensureTargetEmpty(ctx context.Context, item prepared) error {
	// 先判断目录是否存在，避免把"不存在"当成错误输出处理。
	exists, err := p.pathExists(ctx, item, item.workDir)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	command := []string{"sh", "-c", "ls -A " + shellQuote(item.workDir)}
	run, runErr := p.run(ctx, item, "ls", command, p.probeTimeout)
	if runErr != nil {
		return runErr
	}
	if !run.Succeeded() {
		return newError(
			CodeInvalidArgument,
			"ls",
			"读取目标目录失败: "+item.workDir+" | "+output(run),
			nil,
		)
	}
	entries := strings.Fields(strings.TrimSpace(run.Stdout))
	if len(entries) == 0 {
		return nil
	}
	return newError(
		CodeTargetDirNotEmpty,
		"prepare",
		fmt.Sprintf(
			"目标目录非空，拒绝自动覆盖或删除: %s（已有 %d 项内容）",
			item.workDir,
			len(entries),
		),
		nil,
	)
}

// ensureParentDir 确认目标目录的父目录存在。
//
// Clone 到多级路径时 Git 只会创建最后一级目录，因此父目录必须先存在。
func (p *Provider) ensureParentDir(ctx context.Context, item prepared) error {
	parent := parentDir(item.workDir)
	if parent == "/" || parent == "" {
		return nil
	}
	command := []string{"mkdir", "-p", parent}
	run, err := p.run(ctx, item, "mkdir", command, p.probeTimeout)
	if err != nil {
		return err
	}
	if !run.Succeeded() {
		return newError(
			CodeInvalidArgument,
			"mkdir",
			"创建目标目录的父目录失败: "+parent+" | "+output(run),
			nil,
		)
	}
	return nil
}

// ensureRepositoryAccess 确认仓库认证条件满足。
//
// 第一版没有安全的凭据存储：需要认证的仓库直接返回可识别的错误，
// 而不是把凭据写进 URL 或 Git 配置。
func (p *Provider) ensureRepositoryAccess(ctx context.Context, item prepared) error {
	if p.credentials == nil {
		return nil
	}
	credentials, err := p.credentials.Credentials(ctx, item.repository.Raw())
	if err != nil {
		return newError(
			CodeRepositoryAuthRequired,
			"credentials",
			"仓库凭据不可用: "+item.repository.Display(),
			err,
		)
	}
	if strings.TrimSpace(credentials.Username) == "" &&
		strings.TrimSpace(credentials.Password) == "" &&
		strings.TrimSpace(credentials.SSHAgentSocket) == "" {
		return newError(
			CodeRepositoryAuthRequired,
			"credentials",
			"仓库凭据为空: "+item.repository.Display(),
			nil,
		)
	}
	return nil
}

// pathExists 判断容器内路径是否存在。
func (p *Provider) pathExists(ctx context.Context, item prepared, target string) (bool, error) {
	command := []string{"sh", "-c", "test -e " + shellQuote(target)}
	run, err := p.run(ctx, item, "test", command, p.probeTimeout)
	if err != nil {
		return false, err
	}
	// test 用退出码表达结果：0 存在，1 不存在。其他退出码视为执行异常。
	switch run.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, newError(
			CodeInvalidArgument,
			"test",
			fmt.Sprintf("检查路径存在性失败（退出码 %d）: %s", run.ExitCode, target),
			nil,
		)
	}
}

// resolveBaseBranch 识别基础分支并读取其 commit。
//
// 优先使用本次 Clone 已经获取的本地引用，避免为读取分支信息再次访问网络。
func (p *Provider) resolveBaseBranch(ctx context.Context, item prepared) (baseBranch, error) {
	if explicit := strings.TrimSpace(item.explicitBase); explicit != "" {
		commit, err := p.branchCommit(ctx, item, "refs/remotes/origin/"+explicit)
		if err != nil {
			commit, err = p.branchCommit(ctx, item, "refs/heads/"+explicit)
		}
		if err != nil {
			return baseBranch{}, newError(
				CodeBaseBranchMissing,
				"base branch",
				"指定的基础分支不存在: "+explicit,
				err,
			)
		}
		return baseBranch{
			name:      explicit,
			selection: domain.GitBranchSelectionExplicit,
			commit:    commit,
		}, nil
	}
	originHead, _ := p.gitOutput(ctx, item, "symbolic-ref", "--short", domain.OriginHeadRef)
	candidates, err := p.remoteBranches(ctx, item)
	if err != nil {
		return baseBranch{}, err
	}
	name, selection, err := domain.SelectBaseBranch(originHead, candidates)
	if err != nil {
		return baseBranch{}, newError(CodeBaseBranchMissing, "base branch", err.Error(), err)
	}
	commit, commitErr := p.branchCommit(ctx, item, "refs/remotes/origin/"+name)
	if commitErr != nil {
		commit, commitErr = p.branchCommit(ctx, item, "refs/heads/"+name)
	}
	if commitErr != nil {
		return baseBranch{}, newError(
			CodeBaseBranchMissing,
			"base branch",
			"无法读取基础分支 commit: "+name,
			commitErr,
		)
	}
	return baseBranch{name: name, selection: selection, commit: commit}, nil
}

// remoteBranches 列出本次 Clone 得到的远程分支名。
func (p *Provider) remoteBranches(ctx context.Context, item prepared) ([]string, error) {
	out, err := p.gitOutput(
		ctx,
		item,
		"for-each-ref",
		"--format=%(refname:short)",
		"refs/remotes/origin/",
	)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	branches := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasSuffix(trimmed, "/HEAD") {
			continue
		}
		branches = append(branches, strings.TrimPrefix(trimmed, "origin/"))
	}
	return branches, nil
}

// branchCommit 读取指定引用的 commit SHA。
func (p *Provider) branchCommit(ctx context.Context, item prepared, ref string) (string, error) {
	out, err := p.gitOutput(ctx, item, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(out)
	if commit == "" {
		return "", fmt.Errorf("引用 %s 没有指向任何 commit", ref)
	}
	return commit, nil
}

// gitOutput 在容器内执行一条 Git 查询命令并返回标准输出。
func (p *Provider) gitOutput(
	ctx context.Context,
	item prepared,
	args ...string,
) (string, error) {
	command := append([]string{gitExecutable, "-C", item.workDir}, args...)
	run, err := p.run(ctx, item, "git "+args[0], command, p.probeTimeout)
	if err != nil {
		return "", err
	}
	if !run.Succeeded() {
		return "", newError(
			CodeWorktreeInvalid,
			"git "+args[0],
			output(run),
			nil,
		)
	}
	return run.Stdout, nil
}

// initTaskBranch 创建或检出任务分支。
//
// 分支初始化策略固定：
//   - 远程已存在任务分支时检出该分支并建立跟踪，不覆盖远程内容；
//   - 本地已存在任务分支时直接检出；
//   - 两者都不存在时基于选定的基础分支新建任务分支。
//
// 任何情况下都不强制重置或覆盖已有分支。
func (p *Provider) initTaskBranch(
	ctx context.Context,
	item prepared,
	base baseBranch,
) (string, error) {
	remoteRef := "refs/remotes/origin/" + item.taskBranch
	if _, err := p.branchCommit(ctx, item, remoteRef); err == nil {
		return TaskBranchSourceRemote, p.checkoutRemoteTaskBranch(ctx, item)
	}
	localRef := "refs/heads/" + item.taskBranch
	if _, err := p.branchCommit(ctx, item, localRef); err == nil {
		return TaskBranchSourceLocal, p.checkoutLocalTaskBranch(ctx, item)
	}
	return TaskBranchSourceNew, p.createTaskBranch(ctx, item, base)
}

// checkoutRemoteTaskBranch 检出远程已存在的任务分支。
func (p *Provider) checkoutRemoteTaskBranch(ctx context.Context, item prepared) error {
	command := []string{
		gitExecutable, "-C", item.workDir,
		"checkout", "--no-force", "-b", item.taskBranch,
		"--track", "origin/" + item.taskBranch,
	}
	run, err := p.run(ctx, item, "git checkout", command, p.probeTimeout)
	if err != nil {
		return err
	}
	if !run.Succeeded() {
		return newError(
			CodeTaskBranchConflict,
			"git checkout",
			"检出远程任务分支失败: "+item.taskBranch+" | "+output(run),
			nil,
		)
	}
	return nil
}

// checkoutLocalTaskBranch 检出本地已存在的任务分支。
func (p *Provider) checkoutLocalTaskBranch(ctx context.Context, item prepared) error {
	command := []string{
		gitExecutable, "-C", item.workDir,
		"checkout", "--no-force", item.taskBranch,
	}
	run, err := p.run(ctx, item, "git checkout", command, p.probeTimeout)
	if err != nil {
		return err
	}
	if !run.Succeeded() {
		return newError(
			CodeTaskBranchConflict,
			"git checkout",
			"检出本地任务分支失败: "+item.taskBranch+" | "+output(run),
			nil,
		)
	}
	return nil
}

// createTaskBranch 基于基础分支新建任务分支。
func (p *Provider) createTaskBranch(
	ctx context.Context,
	item prepared,
	base baseBranch,
) error {
	command := []string{
		gitExecutable, "-C", item.workDir,
		"checkout", "--no-force", "-b", item.taskBranch,
		"refs/remotes/origin/" + base.name,
	}
	run, err := p.run(ctx, item, "git checkout", command, p.probeTimeout)
	if err != nil {
		return err
	}
	if !run.Succeeded() {
		return newError(
			CodeTaskBranchConflict,
			"git checkout",
			"基于基础分支 "+base.name+" 创建任务分支失败: "+output(run),
			nil,
		)
	}
	return nil
}

// verifyWorktree 校验 Git 工作区并返回当前 commit SHA。
//
// 校验项：工作区目录存在、.git 元数据有效、rev-parse HEAD 能返回 commit、
// 当前分支是约定的任务分支。Clone 成功但校验失败必须整体失败，
// 不能因为目录存在就认为代码获取成功。
func (p *Provider) verifyWorktree(ctx context.Context, item prepared) (string, error) {
	command := []string{"sh", "-c", "test -d " + shellQuote(item.workDir+"/"+gitDirSuffix)}
	run, err := p.run(ctx, item, "verify", command, p.probeTimeout)
	if err != nil {
		return "", err
	}
	if run.ExitCode != 0 {
		return "", newError(
			CodeWorktreeInvalid,
			"verify",
			"工作区缺少 Git 元数据: "+item.workDir,
			nil,
		)
	}
	commit, err := p.gitOutput(ctx, item, "rev-parse", "HEAD")
	if err != nil {
		return "", newError(CodeWorktreeInvalid, "git rev-parse", err.Error(), err)
	}
	trimmed := strings.TrimSpace(commit)
	if trimmed == "" {
		return "", newError(
			CodeWorktreeInvalid,
			"git rev-parse",
			"rev-parse HEAD 没有返回 commit SHA",
			nil,
		)
	}
	current, err := p.gitOutput(ctx, item, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", newError(CodeWorktreeInvalid, "git rev-parse", err.Error(), err)
	}
	if branch := strings.TrimSpace(current); branch != item.taskBranch {
		return "", newError(
			CodeWorktreeInvalid,
			"verify",
			fmt.Sprintf("当前分支 %q 与约定的任务分支 %q 不一致", branch, item.taskBranch),
			nil,
		)
	}
	return trimmed, nil
}

// parentDir 返回容器内绝对路径的父目录。
func parentDir(target string) string {
	index := strings.LastIndex(target, "/")
	if index <= 0 {
		return "/"
	}
	return target[:index]
}

// shellQuote 把参数转义为可安全嵌入 sh -c 命令的形式。
//
// 只用于本包内部的固定模板（test -e、test -d、ls -A）：模板本身固定，
// 唯一变量是已经通过 domain 校验的容器内绝对路径。
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
