package pi

import (
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/process"
)

// outputCapture 持续消费子进程输出，并在内存中只保留有上限的摘要。
//
// 长任务可能输出大量内容，因此不能把全部输出缓存到内存：
//   - stdout 只保留末尾若干字节用于最终文本摘要；
//   - stderr 只保留末尾若干字节用于诊断；
//   - 完整内容通过事件流交给调用方写日志。
type outputCapture struct {
	sessionID string
	emit      func(agent.Event)

	mu     sync.Mutex
	stdout *tailBuffer
	stderr *tailBuffer
}

// newOutputCapture 构造输出采集器。
func newOutputCapture(limit int, sessionID string, emit func(agent.Event)) *outputCapture {
	if limit <= 0 {
		limit = DefaultOutputLimit
	}
	return &outputCapture{
		sessionID: sessionID,
		emit:      emit,
		stdout:    newTailBuffer(limit),
		stderr:    newTailBuffer(DefaultStderrLimit),
	}
}

// consumeStdout 消费标准输出直到通道关闭。
func (c *outputCapture) consumeStdout(lines <-chan process.Line) {
	for line := range lines {
		if line.Err != nil {
			c.emitOutput(agent.StreamStderr, "读取标准输出失败: "+line.Err.Error())
			return
		}
		c.mu.Lock()
		c.stdout.WriteString(line.Text + "\n")
		c.mu.Unlock()
		c.emitOutput(agent.StreamStdout, line.Text)
	}
}

// consumeStderr 消费标准错误直到通道关闭。
func (c *outputCapture) consumeStderr(lines <-chan process.Line) {
	for line := range lines {
		if line.Err != nil {
			return
		}
		c.mu.Lock()
		c.stderr.WriteString(line.Text + "\n")
		c.mu.Unlock()
		c.emitOutput(agent.StreamStderr, line.Text)
	}
}

// stdoutTail 返回标准输出的末尾摘要。
func (c *outputCapture) stdoutTail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.stdout.String())
}

// stderrTail 返回标准错误的末尾摘要。
func (c *outputCapture) stderrTail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.stderr.String())
}

// emitOutput 发出流式输出事件，单条事件文本有独立上限。
func (c *outputCapture) emitOutput(stream, text string) {
	if c.emit == nil {
		return
	}
	trimmed := strings.TrimRight(text, "\r\n")
	if trimmed == "" {
		return
	}
	event, err := agent.NewEvent(c.sessionID, agent.EventRunOutput, agent.RunOutputData{
		Stream: stream,
		Text:   agent.TruncateText(trimmed, MaxOutputEventLength),
	}, time.Now())
	if err != nil {
		return
	}
	c.emit(event)
}

// tailBuffer 是只保留最近若干字节的并发安全缓冲区。
//
// 用于保留输出末尾：超出上限时丢弃最旧的部分，避免长任务耗尽内存。
type tailBuffer struct {
	limit int
	data  []byte
}

// newTailBuffer 构造带长度上限的缓冲区。
func newTailBuffer(limit int) *tailBuffer {
	if limit <= 0 {
		limit = DefaultOutputLimit
	}
	return &tailBuffer{limit: limit}
}

// WriteString 追加内容并裁剪到上限之内。
func (b *tailBuffer) WriteString(text string) {
	b.data = append(b.data, text...)
	if len(b.data) <= b.limit {
		return
	}
	overflow := len(b.data) - b.limit
	b.data = append(b.data[:0], b.data[overflow:]...)
}

// String 返回当前保留的内容。
func (b *tailBuffer) String() string {
	return string(b.data)
}
