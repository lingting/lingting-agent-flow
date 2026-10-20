package pi

import (
	"strings"
	"time"
)

// 执行相关的默认参数。
const (
	// DefaultTimeout 是一次 Agent 调用的默认超时时间。
	DefaultTimeout = 30 * time.Minute
	// DefaultExportTimeout 是 HTML 导出的默认超时时间。
	DefaultExportTimeout = 60 * time.Second
	// DefaultGraceTimeout 是关闭与取消时等待进程退出的宽限时间。
	DefaultGraceTimeout = 5 * time.Second
	// DefaultOutputLimit 是保留的 stdout 摘要字节上限。
	DefaultOutputLimit = 256 << 10
	// DefaultStderrLimit 是保留的 stderr 诊断输出字节上限。
	DefaultStderrLimit = 64 << 10
	// DefaultLogLimit 是单个输出日志文件（stdout 或 stderr）的默认字节上限。
	//
	// 超过上限后停止写入并在文件末尾留下明确标记，避免长任务把日志写满磁盘。
	DefaultLogLimit int64 = 8 << 20
	// MaxOutputEventLength 是单条流式输出事件的文本上限。
	MaxOutputEventLength = 4 << 10
	// maxFailureDetail 是失败原因中保留的 stderr 字节上限。
	maxFailureDetail = 2 << 10
	// readerDrainTimeout 是等待输出读取协程退出的上限。
	//
	// 正常结束时读取协程随管道关闭退出；只有极端情况下（进程无法回收）才会超时，
	// 此时宁可丢失剩余输出，也不能让调用永久阻塞。
	readerDrainTimeout = 10 * time.Second
)

// Options 是 Pi Runner 的可选配置。
type Options struct {
	// Executable 是 Pi 可执行文件路径或 PATH 中的名称，为空时使用 pi。
	Executable string
	// PrefixArgs 是附加在 Pi 参数之前的额外参数。
	//
	// 用于通过包装程序启动 Pi（例如 node dist/cli.js），测试替身也使用该字段。
	PrefixArgs []string
	// Env 是子进程环境变量，nil 表示继承当前进程环境。
	Env []string
	// Timeout 是单次调用的默认超时时间，零值使用 DefaultTimeout。
	//
	// RunRequest.Timeout 非零时优先使用请求中的值。
	Timeout time.Duration
	// ExportTimeout 是 HTML 导出的超时时间，零值使用 DefaultExportTimeout。
	ExportTimeout time.Duration
	// GraceTimeout 是关闭与取消时的宽限时间，零值使用 DefaultGraceTimeout。
	GraceTimeout time.Duration
	// OutputLimit 是保留的 stdout 摘要字节上限，零值使用 DefaultOutputLimit。
	OutputLimit int
	// StderrLimit 是保留的 stderr 诊断输出字节上限，零值使用 DefaultStderrLimit。
	StderrLimit int
	// LogLimit 是 stdout 与 stderr 日志文件各自的字节上限，零值使用 DefaultLogLimit。
	LogLimit int64
	// DisableExport 为 true 时不再尝试导出 HTML。
	DisableExport bool
}

// Runner 是 Pi 的 Agent Runner 实现。
type Runner struct {
	options Options
}

// NewRunner 构造 Pi Runner。
func NewRunner(options Options) *Runner {
	return &Runner{options: options}
}

// resolveOptions 补齐默认配置。
func (r *Runner) resolveOptions() Options {
	options := r.options
	options.Executable = strings.TrimSpace(options.Executable)
	if options.Timeout <= 0 {
		options.Timeout = DefaultTimeout
	}
	if options.ExportTimeout <= 0 {
		options.ExportTimeout = DefaultExportTimeout
	}
	if options.GraceTimeout <= 0 {
		options.GraceTimeout = DefaultGraceTimeout
	}
	if options.OutputLimit <= 0 {
		options.OutputLimit = DefaultOutputLimit
	}
	if options.StderrLimit <= 0 {
		options.StderrLimit = DefaultStderrLimit
	}
	if options.LogLimit <= 0 {
		options.LogLimit = DefaultLogLimit
	}
	return options
}
