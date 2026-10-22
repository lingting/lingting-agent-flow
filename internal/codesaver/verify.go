package codesaver

import (
	"context"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// maxFirstParentCommits 限制推算合并提交时读取的历史长度。
//
// 合并提交必然位于基线与远程基础分支之间：区间过长说明远程基础分支已被大幅改写，
// 此时不再尝试归因，直接交给人工核验。
const maxFirstParentCommits = 200

// Observe 只读核验一次代码保存记录对应的真实仓库状态。
//
// 它只执行只读的 Git 查询（rev-parse / ls-remote / rev-list），不提交、不推送、
// 不切换分支、不写入 code_saves 记录：核验本身绝不能改变被核验的事实。
//
// 核验失败不会返回错误，而是返回带 ReadFailed 的观察结果：无法确认时必须保留
// 不确定性，由 domain.DecideCodeSaveRecovery 判为需要人工核验，而不是伪造成
// "操作没有发生"。
func (s *Saver) Observe(
	ctx context.Context,
	request Request,
	record domain.CodeSaveRecord,
) domain.CodeSaveObservation {
	observation, err := s.observe(ctx, request, record)
	if err != nil {
		return domain.CodeSaveObservation{
			ReadFailed: true,
			ReadError:  errorDetail(err),
		}
	}
	return observation
}

// observe 执行实际的只读查询。
func (s *Saver) observe(
	ctx context.Context,
	request Request,
	record domain.CodeSaveRecord,
) (domain.CodeSaveObservation, error) {
	prepared, err := s.validate(request)
	if err != nil {
		return domain.CodeSaveObservation{}, err
	}
	container, err := s.guard.EnsureExecutable(ctx, prepared.ContainerID)
	if err != nil {
		return domain.CodeSaveObservation{}, classifyGuardError(err)
	}
	if err := checkContainer(prepared, container); err != nil {
		return domain.CodeSaveObservation{}, err
	}
	runner := &gitRunner{
		executor:     s.executor,
		dockerID:     container.DockerID,
		workDir:      prepared.WorkDir,
		probeTimeout: s.probeTimeout,
	}
	if err := ensureGitAvailable(ctx, runner); err != nil {
		return domain.CodeSaveObservation{}, err
	}
	session := &Session{
		saver:     s,
		request:   prepared,
		runner:    runner,
		container: container,
	}
	return session.observeRecord(ctx, record)
}

// observeRecord 读取判定所需的全部只读事实。
func (x *Session) observeRecord(
	ctx context.Context,
	record domain.CodeSaveRecord,
) (domain.CodeSaveObservation, error) {
	observation := domain.CodeSaveObservation{}
	if record.CommitStatus == domain.CommitStatusPending {
		head, err := x.headCommit(ctx)
		if err != nil {
			return observation, err
		}
		observation.HeadCommit = head
	}
	if record.PushStatus == domain.PushStatusPending {
		remote, err := x.remoteBranchCommit(ctx, x.request.TaskBranch)
		if err != nil {
			return observation, err
		}
		observation.RemoteTaskCommit = remote
	}
	if record.MergeStatus != domain.MergeStatusPending {
		return observation, nil
	}
	baseBranch := strings.TrimSpace(record.BaseBranch)
	if baseBranch == "" {
		// 没有基础分支名时无法核验：交给判定函数转人工，不猜测分支。
		return observation, nil
	}
	base, err := x.loadBase(ctx, baseBranch)
	if err != nil {
		return observation, err
	}
	observation.RemoteBaseCommit = base.RemoteCommit
	if base.RemoteCommit == "" {
		return observation, nil
	}
	taskCommit, err := x.localBranchCommit(ctx, x.request.TaskBranch)
	if err != nil {
		return observation, err
	}
	expected, resolvable, err := x.expectedMergeCommit(
		ctx,
		record.BaselineBaseCommitSHA,
		base.RemoteCommit,
		taskCommit,
	)
	if err != nil {
		return observation, err
	}
	observation.ExpectedMergeCommit = expected
	observation.MergeCommitResolvable = resolvable
	return observation, nil
}

// localBranchCommit 读取本地分支指向的 commit。
func (x *Session) localBranchCommit(ctx context.Context, branch string) (string, error) {
	out, err := x.runner.output(
		ctx,
		"recover",
		CodeStateUncertain,
		"rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}",
	)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(out)
	if !validCommit(trimmed) {
		return "", newError(
			CodeStateUncertain,
			"recover",
			"本地分支 "+branch+" 的 commit SHA 非法或无法读取",
			nil,
		)
	}
	return trimmed, nil
}

// expectedMergeCommit 依据基线、远程基础分支与任务分支推算唯一的合并提交。
//
// 判定条件必须同时成立：候选是合并提交、第一父提交等于基线、第二父提交等于任务
// 分支提交。只依据"基础分支发生了变化"无法证明合并由本次操作产生。
//
// 无法唯一确定时返回 resolvable=false：调用方据此判为不确定，而不是判失败。
func (x *Session) expectedMergeCommit(
	ctx context.Context,
	baseline, remoteBase, taskCommit string,
) (string, bool, error) {
	trimmedBaseline := strings.TrimSpace(baseline)
	if !validCommit(trimmedBaseline) || trimmedBaseline == remoteBase {
		return "", false, nil
	}
	out, err := x.runner.output(
		ctx,
		"recover",
		CodeStateUncertain,
		"rev-list", "--first-parent", "--parents", trimmedBaseline+".."+remoteBase,
	)
	if err != nil {
		return "", false, err
	}
	commits, err := parseFirstParentCommits(out)
	if err != nil {
		return "", false, err
	}
	if len(commits) == 0 || len(commits) > maxFirstParentCommits {
		return "", false, nil
	}
	// --first-parent 由新到旧排列，最后一项的第一父提交就是基线。
	oldest := commits[len(commits)-1]
	if oldest.parent != trimmedBaseline || oldest.secondParent != strings.TrimSpace(taskCommit) {
		return "", false, nil
	}
	return oldest.sha, true, nil
}

// firstParentCommit 是 rev-list --first-parent --parents 输出的一条记录。
type firstParentCommit struct {
	// sha 是这条记录指向的提交。
	sha string
	// parent 是第一父提交。
	parent string
	// secondParent 是第二父提交，非合并提交时为空。
	secondParent string
}

// parseFirstParentCommits 解析 rev-list 的输出。
//
// 只接受"sha 父提交 [第二父提交]"这一种形态：形态不符说明输出被截断或命令并非
// 预期命令，此时按无法推算处理，不猜测缺失的父子关系。
func parseFirstParentCommits(out string) ([]firstParentCommit, error) {
	commits := make([]firstParentCommit, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 || len(fields) > 3 {
			return nil, newError(
				CodeStateUncertain,
				"recover",
				"无法解析 rev-list 输出，提交关系不明确",
				nil,
			)
		}
		if !validCommit(fields[0]) || !validCommit(fields[1]) {
			return nil, newError(
				CodeStateUncertain,
				"recover",
				"rev-list 返回的 commit SHA 非法",
				nil,
			)
		}
		commit := firstParentCommit{sha: fields[0], parent: fields[1]}
		if len(fields) == 3 && validCommit(fields[2]) {
			commit.secondParent = fields[2]
		}
		commits = append(commits, commit)
	}
	return commits, nil
}
