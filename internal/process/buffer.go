package process

import (
	"bytes"
	"sync"
)

// boundedBuffer 是只保留最近若干字节的并发安全缓冲区。
//
// 用于捕获 stderr 诊断输出：内容超出上限时丢弃最旧的部分，避免异常日志耗尽内存。
type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

// newBoundedBuffer 创建带长度上限的缓冲区。
func newBoundedBuffer(limit int) *boundedBuffer {
	if limit <= 0 {
		limit = DefaultStderrLimit
	}
	return &boundedBuffer{limit: limit}
}

// Write 追加数据并裁剪到上限之内，始终返回写入的字节数。
func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		copy(b.data, b.data[len(b.data)-b.limit:])
		b.data = b.data[:b.limit]
	}
	return len(p), nil
}

// String 返回当前保留的内容。
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.Clone(b.data))
}
