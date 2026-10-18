package codesaver

import (
	"context"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// ContainerExecutor 是代码保存需要的容器内命令执行能力。
//
// 接口只暴露"在受管理容器内执行一条命令"这一件事：命令必须以参数切片给出，
// 由实现负责转义，调用方不得自行拼接 shell 字符串。
type ContainerExecutor interface {
	// ExecInContainer 在指定容器内执行一条非交互命令。
	ExecInContainer(
		ctx context.Context,
		dockerID string,
		command []string,
		timeout time.Duration,
	) (docker.CommandResult, error)
}

// ContainerGuard 核验容器是否属于当前 AgentFlow 且处于可执行状态。
//
// 实现必须同时核对本地登记记录与 Docker 侧事实，并在容器已 detached 时拒绝
// 自动操作；代码保存不会为了继续执行而启动或重新接管容器。
type ContainerGuard interface {
	EnsureExecutable(ctx context.Context, containerID string) (domain.Container, error)
}

// GitCredentialProvider 为私有仓库提供一次性认证材料。
//
// 凭据只用于本次 Git 操作：实现必须保证材料限定在当前执行与目标仓库范围内，
// 不写入长期保留的 Git 配置、仓库 URL、日志或数据库普通字段。第一版没有经过
// 安全设计的凭据存储，因此默认不提供实现，需要认证的仓库会明确报错。
type GitCredentialProvider interface {
	// Credentials 返回目标仓库的认证材料；没有可用凭据时返回错误。
	Credentials(ctx context.Context, repositoryURL string) (GitCredentials, error)
}

// GitCredentials 是仓库认证材料。
//
// 所有字段都是内存中的临时值，禁止写入日志、数据库、Git 配置或命令行参数。
type GitCredentials struct {
	// Username 是 HTTPS 认证用户名。
	Username string
	// Password 是 HTTPS 认证口令或令牌。
	Password string
	// SSHAgentSocket 是容器内可见的 SSH Agent 套接字路径。
	SSHAgentSocket string
}

// IsEmpty 判断认证材料是否为空。
func (c GitCredentials) IsEmpty() bool {
	return strings.TrimSpace(c.Username) == "" &&
		strings.TrimSpace(c.Password) == "" &&
		strings.TrimSpace(c.SSHAgentSocket) == ""
}

// Recorder 持久化代码保存记录。
//
// 每个阶段推进后都必须单独写入：远程 Git 操作与本机数据库无法共享事务，
// 只有及时落盘才能在中断后定位最后一个已确认完成的阶段。
type Recorder interface {
	Create(ctx context.Context, record domain.CodeSaveRecord) error
	Update(ctx context.Context, record domain.CodeSaveRecord) error
	Get(ctx context.Context, id domain.ID) (*domain.CodeSaveRecord, error)
	ListByTask(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error)
}

// 默认超时。
const (
	// DefaultProbeTimeout 是轻量 Git 查询命令的默认超时。
	DefaultProbeTimeout = 60 * time.Second
	// DefaultCommitTimeout 是提交操作的默认超时。
	DefaultCommitTimeout = 2 * time.Minute
	// DefaultPushTimeout 是推送与拉取的默认超时。
	//
	// 推送需要传输对象，网络较慢时耗时明显长于查询，因此单独设置。
	DefaultPushTimeout = 10 * time.Minute
	// DefaultMergeTimeout 是合并与合并后推送的默认超时。
	DefaultMergeTimeout = 10 * time.Minute
)

// Operation 是一次代码保存请求要执行的操作组合。
type Operation string

// 操作组合取值。
const (
	// OperationSave 执行提交与推送，并按配置尝试合并基础分支。
	OperationSave Operation = "save"
	// OperationPush 只推送已经存在的任务分支提交，不隐式执行合并。
	OperationPush Operation = "push"
	// OperationMerge 只对已成功推送的任务分支执行合并。
	OperationMerge Operation = "merge"
)

// String 返回操作的字符串形式。
func (o Operation) String() string {
	return string(o)
}

// Validate 校验操作是否受支持。
func (o Operation) Validate() error {
	switch o {
	case OperationSave, OperationPush, OperationMerge:
		return nil
	default:
		return newError(CodeInvalidArgument, "prepare", "未知的代码保存操作 "+string(o), nil)
	}
}

// Request 是一次代码保存的输入。
type Request struct {
	// MachineID 是执行节点标识，用于核验容器归属。
	MachineID string
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// DockerID 是实际 Docker 容器 ID；给出时必须与登记记录一致。
	DockerID string
	// ProjectID 是可选的项目标识。
	ProjectID string
	// AttemptID 是发起本次保存的执行尝试标识，用于从尝试反查保存结果。
	AttemptID string
	// TaskID 是任务标识，任务分支固定为 agent/<task-id>。
	TaskID string
	// WorkDir 是容器内 Git 工作区路径，空值使用 domain.DefaultContainerWorkDir。
	WorkDir string
	// TaskBranch 是调用方认定的任务分支，给出时必须与 agent/<task-id> 一致。
	TaskBranch string
	// BaseBranch 是显式指定的基础分支，空值表示按约定优先级自动识别。
	BaseBranch string
	// MergeAfterPush 表示在推送成功后尝试合并基础分支。
	//
	// 零值即 false：不允许因为开启了代码保存就推断用户同意合并主分支。
	MergeAfterPush bool
	// CommitMessage 是提交信息，空值使用代码构造的默认信息。
	CommitMessage string
	// Author 是可选提交身份，空值表示使用仓库已有的有效身份。
	Author domain.GitAuthor
	// RemoteName 是 Git 远程名称，空值使用 origin。
	RemoteName string
	// Credentials 是可选的一次性仓库凭据。
	Credentials GitCredentials
	// Operation 是本次要执行的操作组合，零值等价于 OperationSave。
	Operation Operation
	// ProbeTimeout 覆盖轻量查询命令的超时。
	ProbeTimeout time.Duration
	// CommitTimeout 覆盖提交操作的超时。
	CommitTimeout time.Duration
	// PushTimeout 覆盖推送与拉取的超时。
	PushTimeout time.Duration
	// MergeTimeout 覆盖合并操作的超时。
	MergeTimeout time.Duration
}

// Result 是一次代码保存的结果。
type Result struct {
	// Record 是最终持久化的保存记录。
	Record domain.CodeSaveRecord
	// Worktree 是工作区核验结果。
	Worktree WorktreeState
	// Base 是基础分支识别结果。
	Base BaseBranch
	// NoChanges 表示工作区没有可提交的变更。
	NoChanges bool
	// Duration 是本次操作的耗时。
	Duration time.Duration
}

// Succeeded 判断本次代码保存是否完成了被请求的全部操作。
func (r Result) Succeeded() bool {
	record := r.Record
	if record.CommitStatus != domain.CommitStatusSucceeded &&
		record.CommitStatus != domain.CommitStatusNoChanges &&
		record.CommitStatus != domain.CommitStatusNotAttempted {
		return false
	}
	if record.PushStatus != domain.PushStatusSucceeded &&
		record.PushStatus != domain.PushStatusNotAttempted {
		return false
	}
	if record.MergeEnabled {
		return record.MergeStatus == domain.MergeStatusSucceeded
	}
	return true
}

// WorktreeState 是一次工作区核验的结构化结果。
type WorktreeState struct {
	// Head 是核验时 HEAD 指向的 commit SHA。
	Head string
	// Branch 是当前分支名。
	Branch string
	// Detached 表示当前处于分离头指针状态。
	Detached bool
	// Modified 是已修改的已跟踪文件数。
	Modified int
	// Added 是新增的未跟踪文件数。
	Added int
	// Deleted 是已删除的已跟踪文件数。
	Deleted int
	// Renamed 是重命名的文件数。
	Renamed int
	// Staged 是暂存区中的变更条目数。
	Staged int
	// Conflicts 是未解决的冲突条目数。
	Conflicts int
	// Clean 表示工作区与暂存区都没有变更。
	Clean bool
}

// BaseBranch 是基础分支识别结果。
type BaseBranch struct {
	// Name 是基础分支的逻辑名称，例如 main。
	Name string
	// Local 表示本地基础分支是否存在。
	Local bool
	// Remote 是远程跟踪引用名，例如 origin/main。
	Remote string
	// RemoteCommit 是远程基础分支当前指向的 commit。
	RemoteCommit string
	// Selection 记录基础分支的确定方式。
	Selection domain.GitBranchSelection
}
