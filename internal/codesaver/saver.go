package codesaver

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// Options 约束 Saver 的构造。
type Options struct {
	// Executor 是容器内命令执行器，必填。
	Executor ContainerExecutor
	// Guard 是容器归属核验器，必填。
	Guard ContainerGuard
	// Recorder 是保存记录仓储，必填。
	Recorder Recorder
	// ProbeTimeout 覆盖轻量查询命令的默认超时。
	ProbeTimeout time.Duration
	// CommitTimeout 覆盖提交操作的默认超时。
	CommitTimeout time.Duration
	// PushTimeout 覆盖推送与拉取的默认超时。
	PushTimeout time.Duration
	// MergeTimeout 覆盖合并操作的默认超时。
	MergeTimeout time.Duration
}

// Saver 是代码保存的统一入口。
//
// Saver 不持有连接与容器生命周期：执行器、核验器与记录仓储都由调用方按次提供，
// 因此它不会在失败时启动、停止或删除容器，也不会修改其他任务的状态。
type Saver struct {
	executor      ContainerExecutor
	guard         ContainerGuard
	recorder      Recorder
	probeTimeout  time.Duration
	commitTimeout time.Duration
	pushTimeout   time.Duration
	mergeTimeout  time.Duration
}

// New 构造代码保存组件。
func New(options Options) (*Saver, error) {
	if options.Executor == nil {
		return nil, fmt.Errorf("代码保存初始化失败: 容器执行器不能为空")
	}
	if options.Guard == nil {
		return nil, fmt.Errorf("代码保存初始化失败: 容器核验器不能为空")
	}
	if options.Recorder == nil {
		return nil, fmt.Errorf("代码保存初始化失败: 记录仓储不能为空")
	}
	saver := &Saver{
		executor:      options.Executor,
		guard:         options.Guard,
		recorder:      options.Recorder,
		probeTimeout:  options.ProbeTimeout,
		commitTimeout: options.CommitTimeout,
		pushTimeout:   options.PushTimeout,
		mergeTimeout:  options.MergeTimeout,
	}
	if saver.probeTimeout <= 0 {
		saver.probeTimeout = DefaultProbeTimeout
	}
	if saver.commitTimeout <= 0 {
		saver.commitTimeout = DefaultCommitTimeout
	}
	if saver.pushTimeout <= 0 {
		saver.pushTimeout = DefaultPushTimeout
	}
	if saver.mergeTimeout <= 0 {
		saver.mergeTimeout = DefaultMergeTimeout
	}
	return saver, nil
}

// Run 执行一次完整的代码保存流程：核验工作区 → 提交 → 推送 →（可选）合并。
func (s *Saver) Run(ctx context.Context, request Request) (Result, error) {
	request.Operation = OperationSave
	return s.execute(ctx, request)
}

// Push 只推送已存在的任务分支提交，不隐式执行主分支合并。
func (s *Saver) Push(ctx context.Context, request Request) (Result, error) {
	request.Operation = OperationPush
	return s.execute(ctx, request)
}

// Merge 只对已成功推送的任务分支执行合并。
//
// 合并前置条件（提交结果确定、任务分支推送成功）由上一次保存记录提供：
// 本次记录沿用该结果，因此 Merge 失败不会影响任务分支已经推送成功的事实。
func (s *Saver) Merge(ctx context.Context, request Request) (Result, error) {
	request.Operation = OperationMerge
	return s.execute(ctx, request)
}

// execute 按给定请求准备并执行一次代码保存。
func (s *Saver) execute(ctx context.Context, request Request) (Result, error) {
	session, err := s.Prepare(ctx, request)
	if err != nil {
		return Result{}, err
	}
	return session.Execute(ctx)
}

// Session 是一次已通过前置核验的代码保存操作。
//
// 它持有本次操作使用的执行器、容器记录与保存记录，因此每次调用都有独立的
// 记录与状态推进顺序，不会与其他任务共享可变状态。
type Session struct {
	saver     *Saver
	request   Request
	runner    *gitRunner
	container domain.Container
	record    domain.CodeSaveRecord
	base      BaseBranch
	started   time.Time
}

// Record 返回当前的保存记录副本。
func (x *Session) Record() domain.CodeSaveRecord {
	return x.record
}

// Container 返回本次操作使用的容器登记记录。
func (x *Session) Container() domain.Container {
	return x.container
}

// Prepare 完成参数校验、容器核验与保存记录落盘。
//
// 记录在远程操作之前写入：一旦提交或推送过程中断，仍能从数据库定位最后一个
// 已确认完成的阶段。
func (s *Saver) Prepare(ctx context.Context, request Request) (*Session, error) {
	prepared, err := s.validate(request)
	if err != nil {
		return nil, err
	}
	container, err := s.guard.EnsureExecutable(ctx, prepared.ContainerID)
	if err != nil {
		return nil, classifyGuardError(err)
	}
	if err := checkContainer(prepared, container); err != nil {
		return nil, err
	}
	runner := &gitRunner{
		executor:     s.executor,
		dockerID:     container.DockerID,
		workDir:      prepared.WorkDir,
		probeTimeout: s.probeTimeout,
	}
	if err := ensureGitAvailable(ctx, runner); err != nil {
		return nil, err
	}
	session := &Session{
		saver:     s,
		request:   prepared,
		runner:    runner,
		container: container,
		started:   time.Now().UTC(),
	}
	if err := session.createRecord(ctx); err != nil {
		return nil, err
	}
	if prepared.Operation == OperationMerge {
		if err := session.inheritSavedState(ctx); err != nil {
			return nil, err
		}
	}
	if base := strings.TrimSpace(prepared.BaseBranch); base != "" {
		session.base = BaseBranch{Name: base, Remote: prepared.RemoteName + "/" + base}
	}
	return session, nil
}

// validate 校验调用参数并归一化默认值。
func (s *Saver) validate(request Request) (Request, error) {
	if request.Operation == "" {
		request.Operation = OperationSave
	}
	if err := request.Operation.Validate(); err != nil {
		return Request{}, err
	}
	taskBranch, err := domain.TaskBranchName(request.TaskID)
	if err != nil {
		return Request{}, newError(
			CodeInvalidArgument,
			"prepare",
			"任务标识不合法: "+err.Error(),
			err,
		)
	}
	if branch := strings.TrimSpace(request.TaskBranch); branch != "" {
		if err := domain.ValidateTaskBranchForTask(request.TaskID, branch); err != nil {
			return Request{}, newError(
				CodeTaskBranchMismatch,
				"prepare",
				err.Error(),
				err,
			)
		}
	}
	if strings.TrimSpace(request.ContainerID) == "" {
		return Request{}, newError(CodeInvalidArgument, "prepare", "容器标识不能为空", nil)
	}
	workDir := strings.TrimSpace(request.WorkDir)
	if workDir == "" {
		workDir = domain.DefaultContainerWorkDir
	}
	if err := domain.ValidateContainerWorkDir(workDir); err != nil {
		return Request{}, newError(CodeInvalidArgument, "prepare", err.Error(), err)
	}
	remoteName := strings.TrimSpace(request.RemoteName)
	if remoteName == "" {
		remoteName = defaultRemoteName
	}
	if err := domain.ValidateGitBranchName(remoteName); err != nil {
		return Request{}, newError(
			CodeInvalidArgument,
			"prepare",
			"Git 远程名称不合法: "+err.Error(),
			err,
		)
	}
	baseBranch := ""
	if raw := strings.TrimSpace(request.BaseBranch); raw != "" {
		normalized, normalizeErr := domain.NormalizeGitBranch(raw)
		if normalizeErr != nil {
			return Request{}, newError(
				CodeInvalidArgument,
				"prepare",
				"基础分支不合法: "+normalizeErr.Error(),
				normalizeErr,
			)
		}
		if normalized == taskBranch {
			return Request{}, newError(
				CodeInvalidArgument,
				"prepare",
				"基础分支不能与任务分支相同",
				nil,
			)
		}
		baseBranch = normalized
	}
	if !request.Author.IsZero() {
		if err := request.Author.Validate(); err != nil {
			return Request{}, newError(CodeInvalidArgument, "prepare", err.Error(), err)
		}
	}
	message, err := commitMessage(request.TaskID, request.CommitMessage)
	if err != nil {
		return Request{}, err
	}
	prepared := request
	prepared.TaskID = strings.TrimSpace(request.TaskID)
	prepared.TaskBranch = taskBranch
	prepared.WorkDir = workDir
	prepared.RemoteName = remoteName
	prepared.BaseBranch = baseBranch
	prepared.CommitMessage = message
	return prepared, nil
}

// commitMessage 返回本次提交使用的提交信息。
//
// 调用方未给出时由代码构造可追踪的默认信息；提交信息只作为参数传递，不拼接
// 进 shell 命令，因此其中的特殊字符不会被解释。
func commitMessage(taskID, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "AgentFlow: save task " + strings.TrimSpace(taskID), nil
	}
	if strings.ContainsRune(trimmed, 0) {
		return "", newError(CodeInvalidArgument, "prepare", "提交信息不能包含 NUL 字符", nil)
	}
	const limit = 4000
	if len(trimmed) > limit {
		return "", newError(CodeInvalidArgument, "prepare", "提交信息过长", nil)
	}
	return trimmed, nil
}

// classifyGuardError 把容器核验错误映射为代码保存错误类别。
func classifyGuardError(err error) error {
	if err == nil {
		return nil
	}
	switch docker.ErrorCodeOf(err) {
	case docker.CodeContainerNotFound:
		return newError(CodeContainerNotFound, "prepare", err.Error(), err)
	case docker.CodeDetached:
		return newError(CodeContainerNotManaged, "prepare", err.Error(), err)
	case docker.CodeOwnershipMismatch:
		return newError(CodeContainerNotFound, "prepare", err.Error(), err)
	case docker.CodeStateUncertain, docker.CodeSSHUnavailable:
		return newError(CodeStateUncertain, "prepare", err.Error(), err)
	default:
		return newError(CodeContainerNotRunning, "prepare", err.Error(), err)
	}
}

// checkContainer 核验容器归属与登记信息一致。
//
// 容器已停止、已删除或已脱离管理时直接报错：代码保存不会为了继续执行而启动
// 或重新接管容器。
func checkContainer(request Request, container domain.Container) error {
	if container.Detached || container.State == domain.ContainerStateDetached {
		return newError(
			CodeContainerNotManaged,
			"prepare",
			"容器已脱离 AgentFlow 管理，拒绝自动操作: "+container.ID.String(),
			nil,
		)
	}
	if container.State == domain.ContainerStateRemoved {
		return newError(
			CodeContainerNotFound,
			"prepare",
			"容器已被删除: "+container.ID.String(),
			nil,
		)
	}
	if strings.TrimSpace(container.DockerID) == "" {
		return newError(
			CodeContainerNotFound,
			"prepare",
			"容器记录缺少 Docker 容器 ID: "+container.ID.String(),
			nil,
		)
	}
	if expected := strings.TrimSpace(request.DockerID); expected != "" &&
		expected != container.DockerID {
		return newError(
			CodeContainerNotFound,
			"prepare",
			"登记的 Docker 容器 ID 与实际不一致: 期望 "+expected+
				"，登记 "+container.DockerID,
			nil,
		)
	}
	if machine := strings.TrimSpace(request.MachineID); machine != "" &&
		machine != container.HostID.String() {
		return newError(
			CodeContainerNotFound,
			"prepare",
			"容器不属于指定执行节点: 期望 "+machine+"，实际 "+container.HostID.String(),
			nil,
		)
	}
	return nil
}

// ensureGitAvailable 检查容器内是否存在可用的 Git。
func ensureGitAvailable(ctx context.Context, runner *gitRunner) error {
	result, err := runner.executor.ExecInContainer(
		ctx,
		runner.dockerID,
		[]string{gitExecutable, "--version"},
		runner.probeTimeout,
	)
	if err != nil {
		return classifyExecError("prepare", err, result)
	}
	if !result.Succeeded() || strings.TrimSpace(result.Stdout) == "" {
		return newError(
			CodeGitUnavailable,
			"prepare",
			"容器内没有可用的 Git: "+summarizeOutput(result),
			nil,
		)
	}
	return nil
}

// createRecord 写入初始保存记录。
func (x *Session) createRecord(ctx context.Context) error {
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		x.request.TaskID,
		x.container.ID.String(),
		x.container.DockerID,
		x.request.RemoteName,
		"",
		x.request.TaskBranch,
		x.request.BaseBranch,
		x.request.MergeAfterPush,
		x.started,
	)
	if err != nil {
		return newError(CodeInvalidArgument, "prepare", err.Error(), err)
	}
	record.ProjectID = x.request.ProjectID
	record.AttemptID = x.request.AttemptID
	record.Start("prepare", x.started)
	if err := x.saver.recorder.Create(ctx, record); err != nil {
		return recordError("prepare", err)
	}
	x.record = record
	return nil
}

// persist 把当前记录写回数据库。
//
// 每个阶段推进后都必须单独落盘：远程 Git 操作与数据库无法共享事务，
// 只有及时写入才能在中断后定位最后一个已确认完成的阶段。
func (x *Session) persist(ctx context.Context) error {
	if err := x.record.Validate(); err != nil {
		return recordError(x.record.Stage, err)
	}
	if err := x.saver.recorder.Update(ctx, x.record); err != nil {
		return recordError(x.record.Stage, err)
	}
	return nil
}

// fail 记录失败信息并落盘，返回带类别的错误。
func (x *Session) fail(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	code := ErrorCodeOf(err)
	stage := x.record.Stage
	var structured *Error
	if ok := asError(err, &structured); ok {
		stage = structured.Stage
	}
	x.record.MarkError(stage, statusForErrorCode(code), errorDetail(err), time.Now())
	x.record.Finish(time.Now())
	if persistErr := x.persist(ctx); persistErr != nil {
		return persistErr
	}
	return err
}

// asError 判断错误链中是否包含代码保存错误。
func asError(err error, target **Error) bool {
	return errors.As(err, target)
}

// errorDetail 提取错误的可读摘要。
func errorDetail(err error) string {
	if err == nil {
		return ""
	}
	var structured *Error
	if asError(err, &structured) && structured.Detail != "" {
		return structured.Detail
	}
	return err.Error()
}

// Execute 执行本次调用请求的操作组合。
//
// 顺序固定为：工作区核验 →（可选）提交 →（可选）推送 →（可选）合并。
// 提交与推送的结果分别落盘，推送失败不会影响已成功的提交记录。
func (x *Session) Execute(ctx context.Context) (Result, error) {
	result := Result{}
	state, err := x.inspect(ctx)
	if err != nil {
		return result, x.fail(ctx, err)
	}
	result.Worktree = state
	if err := x.verifyBranch(ctx, state); err != nil {
		return result, x.fail(ctx, err)
	}
	if err := x.recordRepository(ctx); err != nil {
		return result, x.fail(ctx, err)
	}
	base, err := x.resolveBase(ctx)
	if err != nil {
		if x.mergeRequested() {
			return result, x.skipMerge(ctx, err)
		}
		// 未请求合并时基础分支识别失败不阻塞提交与推送。
		base = BaseBranch{}
	}
	x.base = base
	result.Base = base
	if err := x.commit(ctx, state); err != nil {
		if abandonErr := x.abandonPush(ctx); abandonErr != nil {
			return result, abandonErr
		}
		result.Record = x.record
		return result, x.fail(ctx, err)
	}
	result.NoChanges = x.record.CommitStatus == domain.CommitStatusNoChanges
	result.Record = x.record
	if err := x.push(ctx); err != nil {
		result.Record = x.record
		return result, x.fail(ctx, err)
	}
	result.Record = x.record
	if err := x.merge(ctx); err != nil {
		result.Record = x.record
		return result, x.fail(ctx, err)
	}
	x.record.Finish(time.Now())
	if err := x.persist(ctx); err != nil {
		return result, err
	}
	result.Record = x.record
	result.Duration = time.Since(x.started)
	return result, nil
}

// recordRepository 记录脱敏后的仓库地址。
func (x *Session) recordRepository(ctx context.Context) error {
	raw, err := x.runner.output(
		ctx,
		"remote",
		CodeRemoteInvalid,
		"config", "--get", "remote."+x.request.RemoteName+".url",
	)
	if err != nil {
		if ErrorCodeOf(err) == CodeRemoteInvalid {
			return newError(
				CodeRemoteInvalid,
				"remote",
				"Git 远程不存在: "+x.request.RemoteName,
				err,
			)
		}
		return err
	}
	repository, err := domain.ParseGitRepositoryURL(raw)
	if err != nil {
		// 仓库地址可能包含内嵌凭据或本地路径：解析失败时不记录原始内容。
		x.record.Repository = ""
		return newError(
			CodeRemoteInvalid,
			"remote",
			"Git 远程地址不符合仓库访问策略（只允许 ssh://、https:// 或 host:path 且不含内嵌凭据）",
			err,
		)
	}
	x.record.Repository = repository.Display()
	return x.persist(ctx)
}

// inspect 读取工作区结构状态。
func (x *Session) inspect(ctx context.Context) (WorktreeState, error) {
	top, err := x.runner.output(
		ctx,
		"verify",
		CodeWorktreeInvalid,
		"rev-parse", "--show-toplevel",
	)
	if err != nil {
		return WorktreeState{}, newError(
			CodeWorktreeInvalid,
			"verify",
			"目标目录不是有效的 Git 工作区: "+x.request.WorkDir,
			err,
		)
	}
	if cleaned := path.Clean(strings.TrimSpace(top)); cleaned != x.request.WorkDir {
		return WorktreeState{}, newError(
			CodeWorktreeInvalid,
			"verify",
			"目标目录不是 Git 仓库根目录: "+x.request.WorkDir+"（仓库根目录为 "+cleaned+"）",
			nil,
		)
	}
	raw, err := x.runner.output(
		ctx,
		"verify",
		CodeWorktreeInvalid,
		"status", "--porcelain=v1", "-z", "--branch",
	)
	if err != nil {
		return WorktreeState{}, err
	}
	state, err := ParseStatus(raw)
	if err != nil {
		return WorktreeState{}, err
	}
	head, err := x.runner.output(ctx, "verify", CodeWorktreeInvalid, "rev-parse", "HEAD")
	if err != nil {
		return WorktreeState{}, err
	}
	state.Head = strings.TrimSpace(head)
	if !validCommit(state.Head) {
		return WorktreeState{}, newError(
			CodeWorktreeInvalid,
			"verify",
			"rev-parse HEAD 没有返回有效的 commit SHA",
			nil,
		)
	}
	return state, nil
}

// verifyBranch 校验当前分支就是约定的任务分支。
//
// 发现其他分支时拒绝继续：自动切换分支可能隐藏上一步执行目录或分支已经出错的
// 事实。
func (x *Session) verifyBranch(ctx context.Context, state WorktreeState) error {
	branch, err := x.runner.output(
		ctx,
		"verify",
		CodeWorktreeInvalid,
		"rev-parse", "--abbrev-ref", "HEAD",
	)
	if err != nil {
		return err
	}
	current := strings.TrimSpace(branch)
	if current == "HEAD" {
		return newError(
			CodeTaskBranchMismatch,
			"verify",
			"当前处于分离头指针状态，与约定的任务分支 "+x.request.TaskBranch+" 不一致",
			nil,
		)
	}
	if current != x.request.TaskBranch {
		return newError(
			CodeTaskBranchMismatch,
			"verify",
			"当前分支 "+current+" 与约定的任务分支 "+x.request.TaskBranch+" 不一致",
			nil,
		)
	}
	return nil
}

// abandonPush 在推送阶段未执行时把推送状态标记为未尝试。
//
// 只有仍然处于 pending 的记录会被改写：已经得到确定结果的推送状态必须保留，
// 不能被后续失败覆盖。
func (x *Session) abandonPush(ctx context.Context) error {
	if x.record.PushStatus != domain.PushStatusPending {
		return nil
	}
	x.record.Stage = "push"
	if err := x.record.MarkPush(domain.PushStatusNotAttempted, "", time.Now()); err != nil {
		return newError(CodePushFailed, "push", err.Error(), err)
	}
	return x.persist(ctx)
}

// Status 读取指定任务最近一次保存记录。
func (s *Saver) Status(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error) {
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return nil, newError(CodeInvalidArgument, "status", "任务标识不能为空", nil)
	}
	records, err := s.recorder.ListByTask(ctx, trimmed)
	if err != nil {
		return nil, recordError("status", err)
	}
	return records, nil
}

// inheritSavedState 在只执行合并时沿用上一次保存记录的提交与推送结果。
//
// 合并必须建立在"任务分支已经推送成功"的事实之上，因此这里读取该任务的最近一次
// 保存记录，而不是把本次调用的提交与推送状态当作未执行。记录不存在、容器不同或
// 分支不同时明确拒绝，避免对来源不明的分支执行合并。
func (x *Session) inheritSavedState(ctx context.Context) error {
	records, err := x.saver.recorder.ListByTask(ctx, x.request.TaskID)
	if err != nil {
		return recordError("merge", err)
	}
	for _, candidate := range records {
		if candidate.ID == x.record.ID {
			continue
		}
		if candidate.PushStatus != domain.PushStatusSucceeded {
			continue
		}
		if candidate.TaskBranch != x.request.TaskBranch {
			return newError(
				CodeMergeNotAllowed,
				"merge",
				"最近一次已推送记录的任务分支 "+candidate.TaskBranch+
					" 与本次任务分支 "+x.request.TaskBranch+" 不一致",
				nil,
			)
		}
		if candidate.ContainerID != x.record.ContainerID {
			return newError(
				CodeMergeNotAllowed,
				"merge",
				"任务分支已在容器 "+candidate.ContainerID+" 中推送，请在该容器内执行合并",
				nil,
			)
		}
		if candidate.CommitStatus != domain.CommitStatusSucceeded {
			return newError(
				CodeMergeNotAllowed,
				"merge",
				"最近一次保存记录没有成功的提交，拒绝合并",
				nil,
			)
		}
		x.record.Stage = "merge"
		if markErr := x.record.MarkCommit(
			domain.CommitStatusSucceeded,
			candidate.CommitSHA,
			candidate.CommitMessage,
			time.Now(),
		); markErr != nil {
			return newError(CodeMergeNotAllowed, "merge", markErr.Error(), markErr)
		}
		if markErr := x.record.MarkPush(
			domain.PushStatusSucceeded,
			candidate.RemoteCommitSHA,
			time.Now(),
		); markErr != nil {
			return newError(CodeMergeNotAllowed, "merge", markErr.Error(), markErr)
		}
		if x.record.BaseBranch == "" {
			x.record.BaseBranch = candidate.BaseBranch
		}
		return x.persist(ctx)
	}
	return newError(
		CodeMergeNotAllowed,
		"merge",
		"没有找到该任务已成功推送的任务分支记录，拒绝合并",
		nil,
	)
}
