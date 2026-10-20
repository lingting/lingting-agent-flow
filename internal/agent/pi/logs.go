package pi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// 输出日志文件的固定名称与截断标记。
const (
	// stdoutFileName 是标准输出日志文件名。
	stdoutFileName = "stdout.log"
	// stderrFileName 是标准错误日志文件名。
	stderrFileName = "stderr.log"
	// truncationMarker 是日志被截断时写入的明确标记。
	//
	// 日志写入有上限，超限后必须留下标记，不能把截断后的文件伪装成完整日志。
	truncationMarker = "\n[agentflow] 日志超过 %d 字节上限，后续内容已丢弃（原始输出共 %d 字节）\n"
)

// streamLog 是一个有大小上限的日志文件写入器。
//
// 设计取舍：
//   - stdout 与 stderr 分别写入不同文件，不混流，避免丢失错误上下文；
//   - 写入过程只保留固定大小的缓冲区，长任务不会无限占用内存；
//   - 超过上限后继续计数但不再写入，并在文件末尾留下明确标记；
//   - 关闭文件后写入被忽略，避免进程回收后仍有迟到输出导致 panic。
type streamLog struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	limit    int64
	written  int64
	dropped  int64
	closed   bool
	markDone bool
}

// newStreamLog 创建日志文件，父目录必须已存在。
func newStreamLog(path string, limit int64) (*streamLog, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("pi: 日志文件路径不能为空")
	}
	if limit <= 0 {
		limit = DefaultLogLimit
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("pi: 创建日志目录失败: %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("pi: 创建日志文件失败: %s: %w", path, err)
	}
	return &streamLog{path: path, file: file, limit: limit}, nil
}

// Write 追加一行日志，超过上限后丢弃并计数。
//
// 返回的字节数是调用方写入的字节数，与是否真正落盘无关：日志截断不是写入错误，
// 不应中断正在执行的进程。
func (l *streamLog) Write(line string) int {
	if l == nil {
		return 0
	}
	payload := line + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.file == nil {
		return len(payload)
	}
	remaining := l.limit - l.written
	if remaining <= 0 {
		l.dropped += int64(len(payload))
		return len(payload)
	}
	chunk := payload
	if int64(len(chunk)) > remaining {
		chunk = chunk[:remaining]
		l.dropped += int64(len(payload) - len(chunk))
	}
	written, err := l.file.WriteString(chunk)
	l.written += int64(written)
	if err != nil {
		l.dropped += int64(len(chunk) - written)
	}
	return len(payload)
}

// Path 返回日志文件路径。
func (l *streamLog) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Truncated 判断日志是否因超过上限被截断。
func (l *streamLog) Truncated() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped > 0
}

// Close 写入截断标记并关闭文件，可重复调用。
func (l *streamLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.file == nil {
		return nil
	}
	if l.dropped > 0 && !l.markDone {
		marker := fmt.Sprintf(truncationMarker, l.limit, l.written+l.dropped)
		_, _ = l.file.WriteString(marker)
		l.markDone = true
	}
	err := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("pi: 关闭日志文件失败: %s: %w", l.path, err)
	}
	return nil
}

// outputLogs 是一次调用的 stdout 与 stderr 日志文件。
type outputLogs struct {
	stdout *streamLog
	stderr *streamLog
	// error 记录创建或关闭日志文件时的失败说明。
	err error
}

// newOutputLogs 在给定目录下创建本次调用的两个日志文件。
//
// 创建失败不是致命错误：日志缺失只影响诊断便利性，执行结果与 Session 归档
// 仍然有效，因此这里只记录说明，让调用方把原因写进结果。
func newOutputLogs(dir string, limit int64) *outputLogs {
	logs := &outputLogs{}
	stdout, err := newStreamLog(filepath.Join(dir, stdoutFileName), limit)
	if err != nil {
		logs.err = err
	} else {
		logs.stdout = stdout
	}
	stderr, err := newStreamLog(filepath.Join(dir, stderrFileName), limit)
	if err != nil {
		logs.err = joinErrors(logs.err, err)
	} else {
		logs.stderr = stderr
	}
	return logs
}

// writeStdout 写入一行标准输出。
func (o *outputLogs) writeStdout(line string) {
	if o == nil {
		return
	}
	o.stdout.Write(line)
}

// writeStderr 写入一行标准错误。
func (o *outputLogs) writeStderr(line string) {
	if o == nil {
		return
	}
	o.stderr.Write(line)
}

// close 关闭两个日志文件并汇总错误。
func (o *outputLogs) close() error {
	if o == nil {
		return nil
	}
	return joinErrors(o.stdout.Close(), o.stderr.Close())
}

// stdoutPath 返回标准输出日志路径，未创建时为空。
func (o *outputLogs) stdoutPath() string {
	if o == nil {
		return ""
	}
	return o.stdout.Path()
}

// stderrPath 返回标准错误日志路径，未创建时为空。
func (o *outputLogs) stderrPath() string {
	if o == nil {
		return ""
	}
	return o.stderr.Path()
}

// stdoutTruncated 判断标准输出日志是否被截断。
func (o *outputLogs) stdoutTruncated() bool {
	if o == nil {
		return false
	}
	return o.stdout.Truncated()
}

// stderrTruncated 判断标准错误日志是否被截断。
func (o *outputLogs) stderrTruncated() bool {
	if o == nil {
		return false
	}
	return o.stderr.Truncated()
}

// joinErrors 合并多个错误，忽略空值。
func joinErrors(errs ...error) error {
	var messages []string
	for _, err := range errs {
		if err != nil {
			messages = append(messages, err.Error())
		}
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(messages, "；"))
}

// joinFailure 合并两段失败说明，忽略空值。
func joinFailure(existing, addition string) string {
	switch {
	case strings.TrimSpace(existing) == "":
		return strings.TrimSpace(addition)
	case strings.TrimSpace(addition) == "":
		return strings.TrimSpace(existing)
	default:
		return strings.TrimSpace(existing) + "；" + strings.TrimSpace(addition)
	}
}
