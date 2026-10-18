package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// NewInvocationID 生成一次 Agent 调用的唯一标识。
//
// 每次调用都必须使用新的调用标识：它既用于生成容器内的临时 Session 路径，
// 也用于本机 Session 目录分层，保证同一次会话中的多次调用不会互相覆盖。
func NewInvocationID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Errorf("agent: 生成调用标识失败: %w", err))
	}
	return hex.EncodeToString(buffer)
}

// ContainerGuard 核验容器是否属于当前 AgentFlow 且处于可执行状态。
//
// 归属核验不能只依据容器名称或可预测的 Docker 标签：实现必须同时核对本地登记
// 记录与 Docker 标签，并在容器已 detached 时拒绝自动操作。
type ContainerGuard interface {
	// EnsureExecutable 核验容器存在、受管理且正在运行，返回容器登记记录。
	EnsureExecutable(ctx context.Context, containerID string) (domain.Container, error)
}

// ContainerExecRequest 是一次容器内命令执行请求。
type ContainerExecRequest struct {
	// DockerID 是 Docker 侧容器 ID。
	DockerID string
	// Command 是容器内命令及其参数，必须以参数切片形式给出。
	Command []string
	// Stdin 非 nil 时写入容器内进程的标准输入，并在写入后关闭。
	Stdin []byte
	// Timeout 是本次执行的超时时间，零值表示只受 ctx 限制。
	Timeout time.Duration
	// MaxOutputBytes 是标准输出与标准错误各自保留的字节上限。
	MaxOutputBytes int
}

// ContainerExecResult 是一次容器内命令的执行结果。
//
// 结果必须区分"命令以非零码正常结束"与"超时、取消或结果无法确认"：
// 只凭退出码无法判断远程进程是否仍在运行。
type ContainerExecResult struct {
	// ExitCode 是容器内进程的退出码，未能取得时为 -1。
	ExitCode int
	// Stdout 是标准输出，超过上限时只保留前缀。
	Stdout string
	// Stderr 是标准错误，超过上限时只保留前缀。
	Stderr string
	// StdoutTruncated 表示标准输出是否被截断。
	StdoutTruncated bool
	// StderrTruncated 表示标准错误是否被截断。
	StderrTruncated bool
	// StdinStatus 是标准输入写入状态：none、written 或 failed。
	StdinStatus string
	// StdinBytes 是成功写入标准输入的字节数。
	StdinBytes int64
	// StdinError 是标准输入写入失败的说明。
	StdinError string
	// Duration 是执行耗时。
	Duration time.Duration
	// TimedOut 表示命令因超时被中断。
	TimedOut bool
	// Cancelled 表示命令因调用方取消被中断。
	Cancelled bool
	// Certain 表示可以确认容器内命令已经结束。
	//
	// 为 false 时容器内进程可能仍在运行，调用方必须先核验实际状态，
	// 不得直接重试。
	Certain bool
}

// Succeeded 判断命令是否以退出码 0 正常结束。
func (r ContainerExecResult) Succeeded() bool {
	return r.Certain && !r.TimedOut && !r.Cancelled && r.ExitCode == 0
}

// ContainerExecutor 在任务容器内执行命令，并在容器、远程宿主机与本机之间传输文件。
//
// 实现由 app 层提供：它把容器服务、docker.Runner 与 SFTP 通道适配成本接口。
// Runner 只依赖本接口，因此容器内执行路径可以在没有真实 Docker 与 SSH 的
// 情况下被完整测试。
type ContainerExecutor interface {
	// Exec 在指定容器内执行一条命令。
	Exec(ctx context.Context, request ContainerExecRequest) (ContainerExecResult, error)
	// CopyFromContainer 把容器内文件复制到远程宿主机临时目录。
	//
	// 使用受控的临时导出路径而不是把文件内容经标准输出回传：Session 可能很大，
	// 不能把它整体缓存在内存中。
	CopyFromContainer(ctx context.Context, dockerID, containerPath, hostPath string) error
	// DownloadToLocal 把远程宿主机文件下载到本机路径。
	//
	// 实现必须先写入本机临时文件、校验字节数后再原子提交到目标路径。
	DownloadToLocal(ctx context.Context, hostPath, localPath string, maxBytes int64) (int64, error)
	// RemoveHostFile 删除远程宿主机临时文件，目标不存在时视为成功。
	RemoveHostFile(ctx context.Context, hostPath string) error
}
