package process

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultMaxLineLength 是单行输出的默认长度上限，避免异常输出耗尽内存。
const DefaultMaxLineLength = 32 << 20

// DefaultStderrLimit 是默认保留的 stderr 诊断输出字节数。
const DefaultStderrLimit = 64 << 10

// Line 是子进程 stdout 的一行原始数据。
//
// 只按 LF 分界并去掉行尾可选的 CR；空行不会出现在通道中。
// Err 非空表示读取失败，该值之后通道关闭。
type Line struct {
	Text string
	Err  error
}

// Options 是启动子进程时的可选项。
type Options struct {
	// GraceTimeout 是请求有序退出后等待进程退出的宽限时间，零值使用 DefaultGraceTimeout。
	GraceTimeout time.Duration
	// MaxLineLength 是单行 stdout 的长度上限，零值使用 DefaultMaxLineLength。
	MaxLineLength int
	// StderrLimit 是保留的 stderr 诊断输出上限，零值使用 DefaultStderrLimit。
	StderrLimit int
	// StderrStream 非 nil 时 stderr 的每一行也会写入该通道。
	//
	// 通道由调用方负责消费；缓冲区满时写入会被丢弃，避免阻塞子进程。
	// stderr 读取结束后由本包关闭该通道，调用方只需消费到关闭为止。
	StderrStream chan Line
}

// Supervisor 管理一个已启动的子进程及其管道。
//
// stdout 由内部 goroutine 持续读取并写入 Lines 返回的通道，调用方必须持续消费
// 该通道直到关闭，否则子进程可能因管道背压而停滞。
// stderr 只作为诊断信息保留最近若干字节，绝不进入协议解析。
type Supervisor struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	lines       chan Line
	stderr      *boundedBuffer
	stderrLines chan Line
	grace       time.Duration

	stdinOnce sync.Once
	stdinErr  error

	killOnce sync.Once
	killErr  error

	stdoutDone chan struct{}
	stderrDone chan struct{}
	done       chan struct{}

	exit Exit
}

// Start 启动子进程并开始读取其输出。
//
// 调用方必须持续消费 Lines 直到关闭；只有在决定终止进程时才可停止消费，
// 此时应先调用 Shutdown 或 Terminate。
func Start(command Command, options Options) (*Supervisor, error) {
	if err := command.Validate(); err != nil {
		return nil, err
	}
	path, err := exec.LookPath(strings.TrimSpace(command.Path))
	if err != nil {
		return nil, fmt.Errorf("process: 找不到可执行文件 %q: %w", command.Path, err)
	}
	grace := options.GraceTimeout
	if grace <= 0 {
		grace = DefaultGraceTimeout
	}
	maxLine := options.MaxLineLength
	if maxLine <= 0 {
		maxLine = DefaultMaxLineLength
	}
	stderrLimit := options.StderrLimit
	if stderrLimit <= 0 {
		stderrLimit = DefaultStderrLimit
	}

	cmd := exec.Command(path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("process: 创建 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("process: 创建 stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("process: 创建 stderr 管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("process: 启动子进程失败: %w", err)
	}

	supervisor := &Supervisor{
		cmd:         cmd,
		stdin:       stdin,
		lines:       make(chan Line, 64),
		stderr:      newBoundedBuffer(stderrLimit),
		stderrLines: options.StderrStream,
		grace:       grace,
		stdoutDone:  make(chan struct{}),
		stderrDone:  make(chan struct{}),
		done:        make(chan struct{}),
	}
	go supervisor.pumpStdout(stdout, maxLine)
	go supervisor.pumpStderr(stderr)
	go supervisor.reap()
	return supervisor, nil
}

// PID 返回子进程 ID。
func (s *Supervisor) PID() int {
	if s == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Lines 返回 stdout 的行通道，通道在子进程输出结束时关闭。
func (s *Supervisor) Lines() <-chan Line {
	return s.lines
}

// Write 向子进程 stdin 写入数据，并立即刷新到管道。
func (s *Supervisor) Write(p []byte) error {
	if s == nil {
		return fmt.Errorf("%w: 进程未启动", ErrInvalidCommand)
	}
	if len(p) == 0 {
		return nil
	}
	if _, err := s.stdin.Write(p); err != nil {
		return fmt.Errorf("process: 写入子进程 stdin 失败: %w", err)
	}
	return nil
}

// CloseStdin 关闭 stdin 以请求子进程有序退出，可重复调用。
func (s *Supervisor) CloseStdin() error {
	if s == nil {
		return nil
	}
	s.stdinOnce.Do(func() {
		s.stdinErr = s.stdin.Close()
	})
	if s.stdinErr != nil && !errors.Is(s.stdinErr, io.ErrClosedPipe) {
		return fmt.Errorf("process: 关闭子进程 stdin 失败: %w", s.stdinErr)
	}
	return nil
}

// Terminate 强制终止子进程，可重复调用。
func (s *Supervisor) Terminate() error {
	if s == nil {
		return nil
	}
	s.killOnce.Do(func() {
		if s.cmd.Process == nil {
			return
		}
		select {
		case <-s.done:
			// 进程已回收，无需再次终止。
			return
		default:
		}
		if err := s.cmd.Process.Kill(); err != nil && !isProcessGone(err) {
			s.killErr = err
		}
	})
	if s.killErr != nil {
		return fmt.Errorf("process: 终止子进程失败: %w", s.killErr)
	}
	return nil
}

// isProcessGone 判断终止错误是否表示进程已经结束。
//
// 进程已被回收时各平台的错误取值不同，统一按「已经退出」处理，避免重复终止报错。
func isProcessGone(err error) bool {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrProcessDone) {
		return true
	}
	return strings.Contains(err.Error(), "already finished") ||
		strings.Contains(err.Error(), "process already finished")
}

// Wait 等待子进程被回收并返回退出结果，可重复与并发调用。
func (s *Supervisor) Wait() Exit {
	if s == nil {
		return Exit{Code: -1}
	}
	<-s.done
	return s.exit
}

// Done 返回在子进程被回收后关闭的通道。
func (s *Supervisor) Done() <-chan struct{} {
	return s.done
}

// Shutdown 请求有序退出，宽限时间内未退出则强制终止，返回退出结果。
//
// 无论走哪条路径都会回收子进程；强制终止后仍无法回收时返回 ErrShutdownTimeout。
func (s *Supervisor) Shutdown(grace time.Duration) (Exit, error) {
	if s == nil {
		return Exit{Code: -1}, nil
	}
	if grace <= 0 {
		grace = s.grace
	}
	if err := s.CloseStdin(); err != nil {
		_ = s.Terminate()
	}
	select {
	case <-s.done:
		return s.exit, nil
	case <-time.After(grace):
	}
	if err := s.Terminate(); err != nil {
		_ = s.CloseStdin()
	}
	select {
	case <-s.done:
		return s.exit, nil
	case <-time.After(grace):
		return Exit{Code: -1}, fmt.Errorf(
			"%w: 已强制终止但未能在 %s 内回收子进程",
			ErrShutdownTimeout,
			grace,
		)
	}
}

// Stderr 返回最近捕获的 stderr 诊断输出。
func (s *Supervisor) Stderr() string {
	if s == nil {
		return ""
	}
	return s.stderr.String()
}

// pumpStdout 持续读取 stdout，只按 LF 分界并去掉行尾可选的 CR。
func (s *Supervisor) pumpStdout(reader io.Reader, maxLine int) {
	defer close(s.stdoutDone)
	defer close(s.lines)

	buffered := bufio.NewReaderSize(reader, 64<<10)
	for {
		line, err := readLine(buffered, maxLine)
		if line != "" {
			s.lines <- Line{Text: line}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.lines <- Line{Err: fmt.Errorf("process: 读取子进程 stdout 失败: %w", err)}
			}
			return
		}
	}
}

// pumpStderr 持续读取 stderr 并保留最近的诊断输出。
//
// 配置了 StderrStream 时，每一行同时写入该通道用于流式日志；
// 通道写入是非阻塞的，消费不及时只会丢弃日志，不会阻塞子进程退出。
func (s *Supervisor) pumpStderr(reader io.Reader) {
	defer close(s.stderrDone)
	if s.stderrLines != nil {
		// 关闭通知消费方不再有新的 stderr 行；关闭后本函数不再写入该通道。
		defer close(s.stderrLines)
	}
	buffered := bufio.NewReaderSize(reader, 16<<10)
	for {
		line, err := readLine(buffered, DefaultMaxLineLength)
		if line != "" {
			_, _ = s.stderr.Write([]byte(line + "\n"))
			s.emitStderrLine(line)
		}
		if err != nil {
			return
		}
	}
}

// emitStderrLine 把一行 stderr 写入流式通道，通道未配置或已满时丢弃。
func (s *Supervisor) emitStderrLine(text string) {
	if s.stderrLines == nil {
		return
	}
	select {
	case s.stderrLines <- Line{Text: text}:
	default:
	}
}

// reap 在输出读取完成后回收子进程。
//
// 必须先等 stdout 读取结束再调用 Wait，否则 Wait 关闭管道会导致输出被截断。
func (s *Supervisor) reap() {
	<-s.stdoutDone
	<-s.stderrDone
	err := s.cmd.Wait()
	exit := Exit{Code: -1, Err: err}
	if s.cmd.ProcessState != nil {
		exit.Code = s.cmd.ProcessState.ExitCode()
	}
	_ = s.CloseStdin()
	s.exit = exit
	close(s.done)
}

// readLine 读取一行数据，超长时返回明确错误。
func readLine(reader *bufio.Reader, maxLine int) (string, error) {
	var builder strings.Builder
	for {
		chunk, err := reader.ReadSlice('\n')
		if builder.Len()+len(chunk) > maxLine {
			return "", fmt.Errorf("单行输出超过 %d 字节上限", maxLine)
		}
		builder.Write(chunk)
		if err == nil {
			return trimLineEnding(builder.String()), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return trimLineEnding(builder.String()), err
		}
	}
}

// trimLineEnding 去掉行尾的 LF 与可选 CR。
func trimLineEnding(line string) string {
	line = strings.TrimSuffix(line, "\n")
	return strings.TrimSuffix(line, "\r")
}
