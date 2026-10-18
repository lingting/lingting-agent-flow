package workflow

import (
	"context"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
)

// Ports 是工作流引擎依赖的全部下层能力。
//
// 每个接口只表达工作流真正需要的最小操作，因此实现方可以由 app 层薄适配器提供，
// 也可以在测试中由替身提供。工作流引擎不感知 SSH、Docker 或 Pi 的具体协议。
type Ports struct {
	// Containers 负责任务容器的创建、核验、处置与清理。
	Containers ContainerPort
	// Source 在任务容器内部获取仓库代码并初始化任务分支。
	Source SourcePort
	// Agents 执行单次 Agent 调用并持久化独立 Session。
	Agents AgentPort
	// Commands 在任务容器内执行非交互命令，用于确定性验证。
	Commands CommandPort
	// CodeSaver 执行提交、推送与可选的合并，并分别持久化结果。
	CodeSaver CodeSavePort
}

// ContainerSpec 是创建任务容器的输入。
type ContainerSpec struct {
	// HostID 是目标执行节点标识。
	HostID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// VersionLabel 是锁定的环境版本标签，空值表示使用当前可用版本。
	VersionLabel string
	// ProjectID 是可选的项目标识。
	ProjectID string
	// Caches 是允许挂载的缓存类型。
	Caches []string
	// WorkDir 是容器内项目工作目录。
	WorkDir string
	// TaskID 是任务关联。
	TaskID domain.ID
	// AttemptID 是尝试关联。
	AttemptID *domain.ID
}

// ContainerInfo 是容器登记记录的最小投影。
type ContainerInfo struct {
	// ID 是容器登记记录 ID。
	ID string
	// DockerID 是 Docker 侧容器 ID。
	DockerID string
	// HostID 是所属执行节点。
	HostID domain.ID
	// Detached 表示容器已脱离 AgentFlow 自动管理。
	Detached bool
	// Removed 表示容器已删除。
	Removed bool
	// Running 表示容器当前可执行命令。
	Running bool
}

// ContainerPort 是工作流引擎对容器生命周期的需求。
//
// 实现不得自行判断任务是否成功，也不得在失败时静默忽略错误。
type ContainerPort interface {
	// Create 创建并启动一个全新的任务容器。
	Create(ctx context.Context, spec ContainerSpec) (ContainerInfo, error)
	// Inspect 核验容器实际状态，容器已不存在时返回 Removed 为 true 的记录。
	Inspect(ctx context.Context, containerID string) (ContainerInfo, error)
	// Dispose 按处置方式处理容器：delete 删除，detach 保留现场并脱离管理。
	Dispose(ctx context.Context, containerID string, disposition domain.RestartDisposition) error
}

// SourceSpec 是在容器内获取代码的输入。
type SourceSpec struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// TaskID 是任务标识，任务分支固定为 agent/<task-id>。
	TaskID string
	// RepositoryURL 是仓库地址。
	RepositoryURL string
	// WorkDir 是容器内目标目录。
	WorkDir string
	// BaseBranch 是显式指定的基础分支，空值表示自动识别。
	BaseBranch string
}

// SourceInfo 是代码获取的结果。
type SourceInfo struct {
	Repository string
	BaseBranch string
	BaseCommit string
	TaskBranch string
	Commit     string
	WorkDir    string
}

// SourcePort 在任务容器内部获取代码。
//
// 实现不负责 Push、Merge 或容器生命周期，也不得把代码复制到宿主机或本机目录。
type SourcePort interface {
	Fetch(ctx context.Context, spec SourceSpec) (SourceInfo, error)
}

// AgentInvocation 是一次 Agent 调用的输入。
//
// 每次调用都必须使用独立的 Scope（Task/Step/Attempt）与独立的 Prompt；
// 工作流引擎为每次调用生成新的 Session 记录，不复用旧 Session。
type AgentInvocation struct {
	// ContainerID 是执行本次调用的容器登记记录 ID。
	ContainerID string
	// Scope 是会话的任务、步骤与尝试归属。
	Scope domain.SessionScope
	// Role 是执行本次调用的角色。
	Role domain.Role
	// Prompt 是完整提示词。
	Prompt string
	// Timeout 是本次调用的超时时间，零值使用执行器默认值。
	Timeout time.Duration
}

// AgentOutcome 是一次 Agent 调用的结果。
type AgentOutcome struct {
	// Result 是执行器返回的运行结果。
	Result agent.RunResult
	// Session 是已持久化的会话记录。
	Session domain.Session
}

// AgentPort 执行单次 Agent 调用并持久化独立 Session。
//
// 实现负责把 Session 文件保存到本机持久化目录并写入元数据；无论调用成功、失败、
// 超时还是取消，都必须写入一条真实状态的会话记录。实现不得自行重试整次调用，
// 也不得在容器内进程状态未知时把结果报告为成功。
type AgentPort interface {
	Run(ctx context.Context, invocation AgentInvocation) (AgentOutcome, error)
}

// CommandRun 是一条待执行的确定性验证命令。
type CommandRun struct {
	// Command 是命令定义。
	Command domain.VerificationCommand
	// WorkDir 是容器内执行目录。
	WorkDir string
	// Timeout 是执行超时。
	Timeout time.Duration
	// MaxOutputBytes 是保留的输出字节上限。
	MaxOutputBytes int
}

// CommandPort 在任务容器内执行非交互命令。
//
// 命令以参数切片传递，不经过 shell 解释；实现必须区分"命令以非零码正常结束"与
// "超时、取消或结果无法确认"。
type CommandPort interface {
	Exec(ctx context.Context, containerID string, run CommandRun) (domain.CommandResult, error)
}

// CodeSaveSpec 是一次代码保存的输入。
type CodeSaveSpec struct {
	ContainerID string
	ProjectID   string
	TaskID      string
	AttemptID   string
	WorkDir     string
	BaseBranch  string
	// MergeAfterPush 表示推送成功后尝试合并基础分支。
	MergeAfterPush bool
	CommitMessage  string
}

// CodeSaveOutcome 是一次代码保存的结果。
type CodeSaveOutcome struct {
	Record domain.CodeSaveRecord
}

// CodeSavePort 执行提交、推送与可选的合并。
//
// 实现必须分别持久化 Commit、Push 与 Merge 状态，并在结果不确定时先核验远程状态；
// 实现不决定任务是否最终成功。
type CodeSavePort interface {
	Save(ctx context.Context, spec CodeSaveSpec) (CodeSaveOutcome, error)
}
