package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// Fetcher 是代码获取方式的统一抽象。
//
// 第一版只有 Git Clone 一种实现（GitFetcher）。把"获取代码"收敛到一个接口，
// 是为了让后续的 HTTP 下载、wget、curl 与 SCP 等方式只需要新增实现，而不必改动
// 服务层、持久化模型与调用方。
//
// 实现约束：
//   - 所有命令通过 ContainerExecutor 在目标容器内执行，不得在宿主机执行；
//   - 目标工作区已存在且非空时必须拒绝，不得自动清空或覆盖；
//   - 不得把凭据写入命令行参数、URL、日志或错误摘要。
type Fetcher interface {
	// Kind 返回代码获取方式，用于持久化与对外展示。
	Kind() domain.CodeSourceType
	// Fetch 在容器内获取代码并初始化任务分支。
	Fetch(ctx context.Context, request FetchRequest) (FetchResult, error)
}

// FetchRequest 是一次代码获取的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方不能指定任意容器、任意路径或
// 任意命令行参数。
type FetchRequest struct {
	// ProjectID 是可选的项目标识。
	ProjectID string
	// TaskID 是任务标识，用于生成任务分支 agent/<task-id>。
	TaskID string
	// AttemptID 是本次代码获取所属的执行尝试。
	AttemptID string
	// SourceType 是期望的代码获取方式，零值等价于 git。
	//
	// 显式给出时用于拒绝与实现不匹配的请求，避免把未知方式静默当成 git 执行。
	SourceType domain.CodeSourceType
	// Repository 是仓库地址，必须是 ssh://、https:// 或 host:path 形式。
	Repository string
	// WorkDir 是容器内目标工作目录，空值使用 domain.DefaultContainerWorkDir。
	WorkDir string
	// BaseBranch 是显式指定的基础分支，空值表示按约定优先级自动识别。
	BaseBranch string
	// ContainerID 是容器登记记录 ID，用于核验归属与管理状态。
	ContainerID string
	// Timeout 是本次代码获取的超时时间，零值使用实现默认值。
	Timeout time.Duration
}

// FetchResult 是一次代码获取的结果。
//
// 失败时同样返回结果：调用方据此把"失败阶段 + 脱敏原因"写入执行记录，
// 而不是只拿到一个没有上下文的 error。
type FetchResult struct {
	// ProjectID 是本次获取所属项目。
	ProjectID string
	// TaskID 是本次获取所属任务。
	TaskID string
	// AttemptID 是本次获取所属尝试。
	AttemptID string
	// SourceType 是实际使用的代码获取方式。
	SourceType domain.CodeSourceType
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
	// TaskBranchSource 描述任务分支的来源。
	TaskBranchSource domain.TaskBranchSource
	// Commit 是最终检出的 commit SHA。
	Commit string
	// Stage 是最后进入的阶段，失败时即为失败阶段。
	Stage domain.CodeFetchStage
	// ErrorCode 是可诊断的错误类别，成功时为空。
	ErrorCode string
	// ErrorSummary 是脱敏并裁剪后的失败摘要。
	ErrorSummary string
	// StartedAt 是开始时间。
	StartedAt time.Time
	// FinishedAt 是结束时间。
	FinishedAt time.Time
}

// Duration 返回本次获取的耗时。
func (r FetchResult) Duration() time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	return r.FinishedAt.Sub(r.StartedAt)
}

// Succeeded 判断本次获取是否成功。
func (r FetchResult) Succeeded() bool {
	return r.ErrorCode == "" && strings.TrimSpace(r.Commit) != ""
}

// GitFetcher 是把 Git 仓库获取到任务容器内部的代码获取实现。
//
// 它只是 Provider 的接口适配层：真正的执行顺序、错误分类与分支策略仍然集中在
// Provider 中，避免同一套规则出现两份实现。
type GitFetcher struct {
	provider *Provider
}

// NewGitFetcher 构造 Git 代码获取实现。
func NewGitFetcher(options Options) (*GitFetcher, error) {
	provider, err := NewProvider(options)
	if err != nil {
		return nil, err
	}
	return &GitFetcher{provider: provider}, nil
}

// Kind 返回代码获取方式。
func (f *GitFetcher) Kind() domain.CodeSourceType {
	return domain.CodeSourceGit
}

// Fetch 在容器内克隆仓库并初始化任务分支。
func (f *GitFetcher) Fetch(ctx context.Context, request FetchRequest) (FetchResult, error) {
	if f == nil || f.provider == nil {
		return FetchResult{}, newError(CodeInvalidArgument, "prepare", "代码获取实现未初始化", nil)
	}
	sourceType := request.SourceType
	if sourceType == "" {
		sourceType = domain.CodeSourceGit
	}
	if err := sourceType.Validate(); err != nil {
		return FetchResult{}, newError(CodeInvalidArgument, "prepare", err.Error(), err)
	}
	if sourceType != domain.CodeSourceGit {
		return FetchResult{}, newError(
			CodeInvalidArgument,
			"prepare",
			fmt.Sprintf("当前实现只支持 git 代码获取，不支持 %s", sourceType),
			nil,
		)
	}
	started := time.Now().UTC()
	result := FetchResult{
		ProjectID:   strings.TrimSpace(request.ProjectID),
		TaskID:      strings.TrimSpace(request.TaskID),
		AttemptID:   strings.TrimSpace(request.AttemptID),
		SourceType:  sourceType,
		ContainerID: strings.TrimSpace(request.ContainerID),
		StartedAt:   started,
	}
	fetched, err := f.provider.Fetch(ctx, Request{
		TaskID:        request.TaskID,
		ContainerID:   request.ContainerID,
		RepositoryURL: request.Repository,
		WorkDir:       request.WorkDir,
		BaseBranch:    request.BaseBranch,
		Timeout:       request.Timeout,
	})
	result.FinishedAt = time.Now().UTC()
	result.Repository = fetched.Repository
	result.DockerID = fetched.DockerID
	result.WorkDir = fetched.WorkDir
	result.BaseBranch = fetched.BaseBranch
	result.BaseSelection = fetched.BaseSelection
	result.BaseCommit = fetched.BaseCommit
	result.TaskBranch = fetched.TaskBranch
	result.TaskBranchSource = domain.TaskBranchSource(fetched.TaskBranchSource)
	result.Commit = fetched.Commit
	if err != nil {
		result.Stage = stageOf(err)
		result.ErrorCode = ErrorCodeOf(err).String()
		result.ErrorSummary = summaryOf(err)
		return result, err
	}
	result.Stage = domain.CodeFetchStageVerify
	return result, nil
}

// stageOf 把代码获取错误映射为初始化阶段。
//
// 阶段必须能区分"仓库不可访问""目标目录被占用"与"分支创建失败"：
// 只记录一个失败状态无法定位问题。
func stageOf(err error) domain.CodeFetchStage {
	code := ErrorCodeOf(err)
	switch code {
	case CodeInvalidArgument, CodeInvalidRepositoryURL,
		CodeContainerNotFound, CodeContainerNotManaged, CodeContainerNotRunning:
		return domain.CodeFetchStageValidate
	case CodeGitUnavailable, CodeTargetDirNotEmpty:
		return domain.CodeFetchStageProbe
	case CodeRepositoryAccessFailed, CodeRepositoryAuthRequired,
		CodeCloneTimeout, CodeCloneInterrupted:
		return domain.CodeFetchStageClone
	case CodeBaseBranchMissing:
		return domain.CodeFetchStageBaseBranch
	case CodeTaskBranchConflict:
		return domain.CodeFetchStageTaskBranch
	case CodeWorktreeInvalid:
		return domain.CodeFetchStageVerify
	case CodeStateUncertain:
		return domain.CodeFetchStageClone
	default:
		return domain.CodeFetchStageProbe
	}
}

// summaryOf 返回脱敏后的错误摘要。
//
// 结构化错误的 Detail 已经排除了凭据内容（仓库地址在进入该结构前就已脱敏），
// 这里只负责裁剪长度，避免把整段命令输出写入数据库。
func summaryOf(err error) string {
	if err == nil {
		return ""
	}
	var structured *Error
	if errorsAs(err, &structured) {
		return truncateSummary(structured.Detail)
	}
	return truncateSummary(err.Error())
}

// truncateSummary 裁剪摘要长度。
func truncateSummary(value string) string {
	trimmed := strings.TrimSpace(value)
	const limit = 600
	if len(trimmed) > limit {
		return trimmed[:limit] + "..."
	}
	return trimmed
}

// 编译期确认 GitFetcher 满足统一抽象。
var _ Fetcher = (*GitFetcher)(nil)

// errorsAs 是 errors.As 的薄包装，避免在本文件重复导入 errors。
func errorsAs(err error, target **Error) bool {
	if !errors.As(err, target) {
		return false
	}
	return true
}
