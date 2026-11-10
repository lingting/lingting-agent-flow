package pi

import "time"

// 执行相关的默认参数。
const (
	// DefaultTimeout 是一次 Agent 调用的默认超时时间。
	DefaultTimeout = 30 * time.Minute
	// DefaultExportTimeout 是 HTML 导出的默认超时时间。
	DefaultExportTimeout = 60 * time.Second
	// DefaultOutputLimit 是保留的 stdout 摘要字节上限。
	DefaultOutputLimit = 256 << 10
	// DefaultLogLimit 是单个输出日志文件（stdout 或 stderr）的默认字节上限。
	//
	// 超过上限后停止写入并在文件末尾留下明确标记，避免长任务把日志写满磁盘。
	DefaultLogLimit int64 = 8 << 20
	// MaxOutputEventLength 是单条流式输出事件的文本上限。
	MaxOutputEventLength = 4 << 10
	// maxFailureDetail 是失败原因中保留的 stderr 字节上限。
	maxFailureDetail = 2 << 10
)

// Options 是容器内 Pi Runner 的可选配置。
type Options struct {
	// Timeout 是单次调用的默认超时时间，零值使用 DefaultTimeout。
	//
	// RunRequest.Timeout 非零时优先使用请求中的值。
	Timeout time.Duration
	// ExportTimeout 是 HTML 导出的超时时间，零值使用 DefaultExportTimeout。
	ExportTimeout time.Duration
	// OutputLimit 是保留的 stdout 摘要字节上限，零值使用 DefaultOutputLimit。
	OutputLimit int
	// LogLimit 是 stdout 与 stderr 日志文件各自的字节上限，零值使用 DefaultLogLimit。
	LogLimit int64
	// DisableExport 为 true 时不再尝试导出 HTML。
	DisableExport bool
}
