// Package sshclient 提供经过主机密钥验证与身份认证的 SSH 连接能力。
//
// 本包包含三个层次：
//   - 连接：主机密钥校验、身份认证与连接生命周期管理；
//   - 执行：在已认证连接上运行远程命令并收集退出码与输出；
//   - 传输：通过 SFTP 上传、下载与管理远程文件。
//
// 本包只提供通用的远程执行与文件传输能力，不包含 Docker、容器生命周期、
// 任务调度或工作流逻辑。
//
// 凭据安全约定：
//   - 密码与私钥口令只在内存中传递，禁止写入数据库、配置文件或日志；
//   - 私钥只保存本机文件路径，本包只读取该文件，不复制也不修改其内容；
//   - 错误信息中不包含凭据内容。
package sshclient

import (
	"context"
	"io"
	"time"
)

// Endpoint 描述 SSH 连接目标。
type Endpoint struct {
	// Host 是 IP 地址或主机名，不含端口。
	Host string
	// Port 是 SSH 端口。
	Port int
	// Username 是 SSH 登录用户名。
	Username string
}

// Client 表示一条已经完成主机密钥验证与身份认证的 SSH 连接。
//
// 调用方必须在使用结束后调用 Close 释放连接。Client 允许并发使用：每次 Run
// 使用独立的远程会话，取消或超时只关闭该次会话，不影响同一连接上的其他调用；
// Close 会关闭整条连接，因此不应与仍在进行的操作并发调用。
type Client interface {
	// Endpoint 返回本次连接的目标。
	Endpoint() Endpoint
	// Run 执行远程命令并返回退出码与输出。
	//
	// 非零退出码通过 CommandResult.ExitCode 与 CommandStatusExitError 表达，
	// 不视为传输层错误；error 只在无法执行命令、连接断开、超时或取消时返回。
	Run(ctx context.Context, command string, options RunOptions) (CommandResult, error)
	// RunWithInput 执行远程命令并把 input 写入其标准输入。
	//
	// input 为 nil 时不提供标准输入（等价于 Run）；非 nil 时在命令启动后写入内容并
	// 立即关闭写入端，使需要 EOF 才能继续的远程程序能够正常结束。写入失败不会中断
	// 命令本身，错误通过 CommandResult.StdinError 报告。
	RunWithInput(
		ctx context.Context,
		command string,
		input io.Reader,
		options RunOptions,
	) (CommandResult, error)
	// SFTP 打开一个新的 SFTP 子系统通道。
	//
	// 返回的 FileTransfer 由调用方负责关闭；关闭它只释放该通道，
	// 不影响底层 SSH 连接上的其他操作。
	SFTP() (FileTransfer, error)
	// Close 关闭连接并释放关联资源，重复调用返回 nil。
	Close() error
}

// CommandStatus 是远程命令的结果状态。
//
// 只凭退出码无法区分"命令以非零码正常结束"与"命令被取消、超时或连接中断"，
// 因此结果状态独立于退出码表达。
type CommandStatus string

// 远程命令结果状态取值。
const (
	// CommandStatusSucceeded 表示命令以退出码 0 正常结束。
	CommandStatusSucceeded CommandStatus = "succeeded"
	// CommandStatusExitError 表示命令正常结束但退出码非零。
	CommandStatusExitError CommandStatus = "exit_error"
	// CommandStatusTimeout 表示命令因超时被中断。
	CommandStatusTimeout CommandStatus = "timeout"
	// CommandStatusCancelled 表示命令因调用方取消被中断。
	CommandStatusCancelled CommandStatus = "cancelled"
	// CommandStatusConnectionError 表示会话创建失败、连接中断或未取得退出码。
	CommandStatusConnectionError CommandStatus = "connection_error"
)

// String 返回结果状态的字符串形式。
func (s CommandStatus) String() string {
	return string(s)
}

// CommandResult 是远程命令的执行结果。
type CommandResult struct {
	// Command 是实际执行的命令字符串。
	Command string
	// Status 是结果状态，能区分成功、非零退出、超时、取消与连接中断。
	Status CommandStatus
	// ExitCode 是远程进程的退出码；未能取得退出码时为 -1。
	ExitCode int
	// Stdout 是标准输出内容，超过上限时只保留前缀。
	Stdout string
	// Stderr 是标准错误内容，超过上限时只保留前缀。
	Stderr string
	// StdoutTruncated 表示标准输出是否因超过上限被截断。
	StdoutTruncated bool
	// StderrTruncated 表示标准错误是否因超过上限被截断。
	StderrTruncated bool
	// Duration 是命令执行耗时。
	Duration time.Duration
	// Stdin 描述标准输入的写入结果，未提供输入时 Status 为 StdinStatusNone。
	Stdin StdinResult
	// TerminationConfirmed 表示是否确认远程命令已经结束。
	//
	// 正常结束时为 true；超时或取消时本端已关闭会话通道，但 SSH 协议不保证
	// 远程进程一定被终止，因此该字段为 false，调用方不得据此认为远程进程已退出。
	TerminationConfirmed bool
	// Started 表示远程会话已建立并提交了命令。
	//
	// 会话创建失败、上下文已取消或写入标准输入失败时保持 false：此时退出码与
	// 输出都没有意义，调用方不得据此判定"命令执行失败"，只能判定"没有拿到证据"。
	Started bool
}

// Succeeded 判断命令是否以退出码 0 正常结束。
//
// 超时、取消与连接中断一律不算成功，即使已经收到部分标准输出。
func (r CommandResult) Succeeded() bool {
	switch r.Status {
	case CommandStatusTimeout, CommandStatusCancelled, CommandStatusConnectionError:
		return false
	}
	return r.ExitCode == 0
}

// TimedOut 判断命令是否因超时被中断。
func (r CommandResult) TimedOut() bool {
	return r.Status == CommandStatusTimeout
}

// Cancelled 判断命令是否因调用方取消被中断。
func (r CommandResult) Cancelled() bool {
	return r.Status == CommandStatusCancelled
}

// RunOptions 约束一次远程命令执行。
type RunOptions struct {
	// Timeout 是本次执行的超时时间，零值表示只受 ctx 限制。
	//
	// 超时会中断远程命令并关闭对应会话，不影响同一连接上的其他操作。
	Timeout time.Duration
	// MaxOutputBytes 是标准输出与标准错误各自保留的字节上限。
	//
	// 零值使用 DefaultMaxOutputBytes；超过上限的内容被丢弃并标记为截断，
	// 但读取会继续进行，避免远程进程因管道写满而阻塞。
	MaxOutputBytes int
}

// DefaultMaxOutputBytes 是远程命令输出保留的默认字节上限。
const DefaultMaxOutputBytes = 1 << 20

// 标准输入写入结果状态。
const (
	// StdinStatusNone 表示本次执行没有提供标准输入。
	StdinStatusNone = "none"
	// StdinStatusWritten 表示内容已完整写入并关闭标准输入。
	StdinStatusWritten = "written"
	// StdinStatusFailed 表示写入或关闭标准输入时出错。
	StdinStatusFailed = "failed"
)

// StdinResult 描述标准输入的写入结果。
//
// 写入失败不代表远程命令失败：命令可能已经读到部分内容并自行结束。
// 调用方必须结合退出码与 StdinError 判断实际状态。
type StdinResult struct {
	// Status 是写入结果状态。
	Status string
	// Bytes 是成功写入的字节数。
	Bytes int64
	// Error 是写入或关闭失败的说明，成功时为空。
	Error string
}
