package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// 代码获取的默认超时。
const (
	// DefaultCloneTimeout 是单次 Git Clone 的默认超时时间。
	//
	// 第一版不做浅克隆：完整历史可能较大，因此超时比普通查询命令宽松。
	DefaultCloneTimeout = 20 * time.Minute
	// DefaultProbeTimeout 是容器内轻量探测命令（git --version、ls、rev-parse）的超时时间。
	DefaultProbeTimeout = 60 * time.Second
)

// Request 是一次代码获取的输入。
type Request struct {
	// TaskID 是本次代码获取所属任务，用于生成任务分支 agent/<task-id>。
	TaskID string
	// ContainerID 是容器登记记录 ID，用于核验归属与管理状态。
	ContainerID string
	// RepositoryURL 是仓库地址，必须是 ssh://、https:// 或 host:path 形式且不含内嵌凭据。
	RepositoryURL string
	// WorkDir 是容器内目标工作目录，空值使用 domain.DefaultContainerWorkDir。
	WorkDir string
	// BaseBranch 是显式指定的基础分支，空值表示按约定优先级自动识别。
	BaseBranch string
	// Timeout 是本次代码获取的超时时间，零值使用 DefaultCloneTimeout。
	Timeout time.Duration
}

// Result 是一次代码获取的结果。
type Result struct {
	// Repository 是脱敏后的仓库地址，可以安全写入日志与数据库。
	Repository string
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// DockerID 是 Docker 侧容器 ID。
	DockerID string
	// WorkDir 是容器内项目目录。
	WorkDir string
	// BaseBranch 是识别出的基础分支。
	BaseBranch string
	// BaseSelection 记录基础分支的确定方式。
	BaseSelection domain.GitBranchSelection
	// BaseCommit 是基础分支指向的 commit SHA。
	BaseCommit string
	// TaskBranch 是任务分支名，固定为 agent/<task-id>。
	TaskBranch string
	// TaskBranchSource 描述任务分支的来源：new 表示基于基础分支新建，
	// remote 表示检出已存在的远程任务分支，local 表示检出已存在的本地分支。
	TaskBranchSource string
	// Commit 是最终检出的 commit SHA。
	Commit string
	// Duration 是本次代码获取的耗时。
	Duration time.Duration
}

// 任务分支来源取值。
const (
	// TaskBranchSourceNew 表示基于基础分支新建任务分支。
	TaskBranchSourceNew = "new"
	// TaskBranchSourceRemote 表示检出远程已存在的任务分支。
	TaskBranchSourceRemote = "remote"
	// TaskBranchSourceLocal 表示检出本地已存在的任务分支。
	TaskBranchSourceLocal = "local"
)

// Provider 是把 Git 仓库代码获取到任务容器内部的代码来源实现。
//
// Provider 不持有连接：执行器与归属核验器由调用方按次提供，因此它不承担容器
// 或连接的生命周期，也不会在失败时删除容器或工作区。
type Provider struct {
	// executor 在容器内执行命令。
	executor ContainerExecutor
	// guard 核验容器归属与管理状态。
	guard ContainerGuard
	// credentials 是可选的仓库凭据提供器，第一版通常为 nil。
	credentials GitCredentialProvider
	// cloneTimeout 是 Clone 的默认超时。
	cloneTimeout time.Duration
	// probeTimeout 是探测命令的默认超时。
	probeTimeout time.Duration
}

// Options 约束 Provider 的构造。
type Options struct {
	// Executor 是容器内命令执行器，必填。
	Executor ContainerExecutor
	// Guard 是容器归属核验器，必填。
	Guard ContainerGuard
	// Credentials 是可选的仓库凭据提供器。
	Credentials GitCredentialProvider
	// CloneTimeout 覆盖 Clone 默认超时。
	CloneTimeout time.Duration
	// ProbeTimeout 覆盖探测命令默认超时。
	ProbeTimeout time.Duration
}

// NewProvider 构造 Git 代码来源。
func NewProvider(options Options) (*Provider, error) {
	if options.Executor == nil {
		return nil, fmt.Errorf("代码获取初始化失败: 容器执行器不能为空")
	}
	if options.Guard == nil {
		return nil, fmt.Errorf("代码获取初始化失败: 容器核验器不能为空")
	}
	provider := &Provider{
		executor:     options.Executor,
		guard:        options.Guard,
		credentials:  options.Credentials,
		cloneTimeout: options.CloneTimeout,
		probeTimeout: options.ProbeTimeout,
	}
	if provider.cloneTimeout <= 0 {
		provider.cloneTimeout = DefaultCloneTimeout
	}
	if provider.probeTimeout <= 0 {
		provider.probeTimeout = DefaultProbeTimeout
	}
	return provider, nil
}

// Fetch 把仓库代码获取到容器内目标目录并初始化任务分支。
//
// 执行顺序固定为：校验参数 → 核验容器 → 校验目标目录 → 校验 Git → Clone →
// 识别基础分支 → 初始化任务分支 → 校验工作区。任一步失败都返回带类别的错误，
// 并且不会删除已经存在的目录内容。
func (p *Provider) Fetch(ctx context.Context, request Request) (Result, error) {
	started := time.Now()
	prepared, err := p.prepare(ctx, request)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Repository:  prepared.repository.Display(),
		ContainerID: prepared.container.ID.String(),
		DockerID:    prepared.container.DockerID,
		WorkDir:     prepared.workDir,
		TaskBranch:  prepared.taskBranch,
	}
	if err := p.clone(ctx, prepared); err != nil {
		return result, err
	}
	base, err := p.resolveBaseBranch(ctx, prepared)
	if err != nil {
		return result, err
	}
	result.BaseBranch = base.name
	result.BaseSelection = base.selection
	result.BaseCommit = base.commit
	branchSource, err := p.initTaskBranch(ctx, prepared, base)
	if err != nil {
		result.Duration = time.Since(started)
		return result, err
	}
	result.TaskBranchSource = branchSource
	commit, err := p.verifyWorktree(ctx, prepared)
	if err != nil {
		result.Duration = time.Since(started)
		return result, err
	}
	result.Commit = commit
	result.Duration = time.Since(started)
	return result, nil
}

// prepared 是通过全部前置校验后的执行上下文。
type prepared struct {
	repository domain.GitRepositoryURL
	container  domain.Container
	workDir    string
	taskBranch string
	// taskID 是原始任务标识。
	taskID string
	// explicitBase 是调用方显式指定的基础分支，空值表示自动识别。
	explicitBase string
	timeout      time.Duration
}

// prepare 执行参数校验与容器核验。
func (p *Provider) prepare(ctx context.Context, request Request) (prepared, error) {
	taskBranch, err := domain.TaskBranchName(request.TaskID)
	if err != nil {
		return prepared{}, newError(
			CodeInvalidArgument,
			"prepare",
			"任务标识不合法: "+err.Error(),
			err,
		)
	}
	repository, err := domain.ParseGitRepositoryURL(request.RepositoryURL)
	if err != nil {
		return prepared{}, newError(
			CodeInvalidRepositoryURL,
			"prepare",
			err.Error(),
			err,
		)
	}
	workDir := strings.TrimSpace(request.WorkDir)
	if workDir == "" {
		workDir = domain.DefaultContainerWorkDir
	}
	if err := domain.ValidateContainerWorkDir(workDir); err != nil {
		return prepared{}, newError(CodeInvalidArgument, "prepare", err.Error(), err)
	}
	if strings.TrimSpace(request.ContainerID) == "" {
		return prepared{}, newError(CodeInvalidArgument, "prepare", "容器标识不能为空", nil)
	}
	explicitBase := ""
	if base := strings.TrimSpace(request.BaseBranch); base != "" {
		normalized, err := domain.NormalizeGitBranch(base)
		if err != nil {
			return prepared{}, newError(
				CodeInvalidArgument,
				"prepare",
				"基础分支不合法: "+err.Error(),
				err,
			)
		}
		explicitBase = normalized
	}
	container, err := p.guard.EnsureExecutable(ctx, request.ContainerID)
	if err != nil {
		return prepared{}, classifyGuardError(err)
	}
	if container.Detached {
		return prepared{}, newError(
			CodeContainerNotManaged,
			"prepare",
			"容器已脱离 AgentFlow 管理: "+container.ID.String(),
			nil,
		)
	}
	if strings.TrimSpace(container.DockerID) == "" {
		return prepared{}, newError(
			CodeContainerNotFound,
			"prepare",
			"容器记录缺少 Docker 容器 ID: "+container.ID.String(),
			nil,
		)
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = p.cloneTimeout
	}
	return prepared{
		repository:   repository,
		container:    container,
		workDir:      workDir,
		taskBranch:   taskBranch,
		taskID:       strings.TrimSpace(request.TaskID),
		explicitBase: explicitBase,
		timeout:      timeout,
	}, nil
}

// classifyGuardError 把容器核验错误映射为代码获取错误类别。
func classifyGuardError(err error) error {
	if err == nil {
		return nil
	}
	code := docker.ErrorCodeOf(err)
	switch code {
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

// baseBranch 是识别出的基础分支信息。
type baseBranch struct {
	name      string
	selection domain.GitBranchSelection
	commit    string
}

// run 在容器内执行一条命令，并把不确定结果统一转换为 CodeStateUncertain。
func (p *Provider) run(
	ctx context.Context,
	item prepared,
	op string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	if timeout <= 0 {
		timeout = p.probeTimeout
	}
	result, err := p.executor.ExecInContainer(ctx, item.container.DockerID, command, timeout)
	if interrupted := interruptCode(ctx, result); interrupted != CodeOK {
		return result, newError(interrupted, op, "容器内命令被中断", err)
	}
	if err != nil {
		if result.Uncertain() {
			return result, newError(
				CodeStateUncertain,
				op,
				"容器内命令未能确认结束，请先核验容器内实际状态",
				err,
			)
		}
		return result, err
	}
	if result.Uncertain() {
		return result, newError(
			CodeStateUncertain,
			op,
			"容器内命令未能确认结束，请先核验容器内实际状态",
			nil,
		)
	}
	return result, nil
}

// interruptCode 判断命令是否因取消或超时被中断，并返回对应错误类别。
//
// 本地上下文取消与远程执行状态都要检查：远程超时不一定伴随本地上下文结束，
// 而本地取消也不一定能在远程结果状态中体现。
func interruptCode(ctx context.Context, result docker.CommandResult) ErrorCode {
	if result.TimedOut() || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CodeCloneTimeout
	}
	if result.Cancelled() || errors.Is(ctx.Err(), context.Canceled) {
		return CodeCloneInterrupted
	}
	return CodeOK
}

// output 返回命令的输出摘要，用于诊断信息。
func output(result docker.CommandResult) string {
	combined := strings.TrimSpace(result.Stderr + "\n" + result.Stdout)
	if combined == "" {
		return ""
	}
	lines := strings.Split(combined, "\n")
	const keepLines = 6
	if len(lines) > keepLines {
		lines = lines[len(lines)-keepLines:]
	}
	summary := strings.Join(lines, " | ")
	const limit = 600
	if len(summary) > limit {
		return summary[:limit] + "..."
	}
	return summary
}
