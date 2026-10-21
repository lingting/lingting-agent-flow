package app

import (
	"context"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
)

// Git 工作区状态采集的默认参数。
const (
	// defaultGitStateTimeout 是单次 Git 状态采集的超时。
	//
	// 采集只读取分支、HEAD 与状态摘要，不执行网络操作，因此比 Clone 与推送的
	// 超时短得多。
	defaultGitStateTimeout = 60 * time.Second
)

// StepExecutionGitPort 是步骤执行对 Git 工作区状态采集的需求。
//
// 采集只读取信息：不提交、不推送、不切换分支，也不修改工作区。实现必须只读，
// 否则"记录代码状态"本身就会改变被记录的事实。
//
// 采集失败时必须返回带 StatusRead=false 的状态与错误：调用方在前置校验阶段据此
// 拒绝执行，在执行后快照阶段据此记录"读取失败"，两者都不会被当作"工作区干净"。
type StepExecutionGitPort interface {
	// InspectWorktree 读取容器内任务工作区当前的分支、HEAD 与变更摘要。
	InspectWorktree(
		ctx context.Context,
		request GitWorktreeRequest,
	) (domain.GitWorktreeState, error)
}

// GitWorktreeRequest 是一次 Git 工作区状态采集的输入。
//
// 容器与工作目录都来自已核验的数据库记录：调用方不能指定任意容器或路径。
type GitWorktreeRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// DockerID 是 Docker 容器 ID，给出时必须与登记记录一致。
	DockerID string
	// WorkDir 是容器内 Git 工作区路径，空值使用默认项目目录。
	WorkDir string
	// TaskID 是任务标识，用于校验任务分支命名约定。
	TaskID string
	// Timeout 是本次采集的超时时间，零值使用默认值。
	Timeout time.Duration
}

// failedGitState 构造采集失败的状态，只保留可诊断摘要。
func failedGitState(cause error) domain.GitWorktreeState {
	state := domain.GitWorktreeState{StatusRead: false}
	if cause != nil {
		state.StatusError = summarizeGitStateError(cause)
	}
	return state
}

// summarizeGitStateError 把采集错误裁剪为可写入数据库的摘要。
func summarizeGitStateError(err error) string {
	return agent.TruncateText(strings.TrimSpace(err.Error()), 512)
}
