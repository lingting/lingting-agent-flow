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
// 调用方必须在使用结束后调用 Close 释放连接；Client 不保证并发安全，
// 同一连接上的命令执行与文件传输应当串行或由调用方自行加锁。
type Client interface {
	// Endpoint 返回本次连接的目标。
	Endpoint() Endpoint
	// Run 执行远程命令并返回退出码与输出。
	//
	// 非零退出码通过 CommandResult.ExitCode 表达，不视为传输层错误；
	// error 只在无法执行命令、连接断开、超时或取消时返回。
	Run(ctx context.Context, command string, options RunOptions) (CommandResult, error)
	// SFTP 打开一个新的 SFTP 子系统通道。
	//
	// 返回的 FileTransfer 由调用方负责关闭；关闭它只释放该通道，
	// 不影响底层 SSH 连接上的其他操作。
	SFTP() (FileTransfer, error)
	// Close 关闭连接并释放关联资源，重复调用返回 nil。
	Close() error
}

// CommandResult 是远程命令的执行结果。
type CommandResult struct {
	// Command 是实际执行的命令字符串。
	Command string
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
}

// Succeeded 判断命令是否以退出码 0 结束。
func (r CommandResult) Succeeded() bool {
	return r.ExitCode == 0
}

// RunOptions 约束一次远程命令执行。
type RunOptions struct {
	// Timeout 是本次执行的超时时间，零值表示只受 ctx 限制。
	Timeout time.Duration
	// MaxOutputBytes 是标准输出与标准错误各自保留的字节上限。
	//
	// 零值使用 DefaultMaxOutputBytes；超过上限的内容被丢弃并标记为截断，
	// 但读取会继续进行，避免远程进程因管道写满而阻塞。
	MaxOutputBytes int
}

// DefaultMaxOutputBytes 是远程命令输出保留的默认字节上限。
const DefaultMaxOutputBytes = 1 << 20
