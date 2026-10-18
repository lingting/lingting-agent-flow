package source

import (
	"context"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// ContainerExecutor 是 SourceProvider 需要的容器内命令执行能力。
//
// 接口只暴露"在受管理容器内执行一条命令"这一件事，具体实现由 app 层把
// 容器服务与 docker.Runner 适配进来。SourceProvider 因此可以在没有真实
// SSH 连接与 Docker Engine 的情况下被完整测试。
type ContainerExecutor interface {
	// ExecInContainer 在指定容器内执行一条非交互命令。
	//
	// dockerID 是 Docker 侧容器 ID；command 是容器内命令及其参数，调用方必须以
	// 参数切片形式给出，由实现负责转义，不得自行拼接 shell 字符串。
	ExecInContainer(
		ctx context.Context,
		dockerID string,
		command []string,
		timeout time.Duration,
	) (docker.CommandResult, error)
}

// ContainerGuard 核验容器是否属于当前 AgentFlow 且处于可执行状态。
//
// 归属核验不能只依据容器名称或可预测的 Docker 标签：实现必须同时核对本地登记
// 记录与 Docker 标签，并在容器已 detached 时拒绝自动操作。
type ContainerGuard interface {
	// EnsureExecutable 核验容器存在、受管理且正在运行，返回容器登记记录。
	EnsureExecutable(ctx context.Context, containerID string) (domain.Container, error)
}

// GitCredentialProvider 为私有仓库提供认证信息。
//
// 第一版没有经过安全设计的凭据存储机制，因此不提供任何实现：SourceProvider 在
// 遇到需要认证的仓库时返回 CodeRepositoryAuthRequired，而不是把凭据写进 URL、
// Git 配置或命令行参数。该接口只用于固定未来实现的位置。
type GitCredentialProvider interface {
	// Credentials 返回仓库的认证材料；没有可用凭据时返回错误。
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
