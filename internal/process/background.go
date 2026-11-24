package process

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// BackgroundOptions 描述一次脱离父进程的后台进程启动。
type BackgroundOptions struct {
	// Command 是启动参数，Path 必须是可执行文件路径或 PATH 中的名称。
	Command Command
	// OutputPath 是子进程 stdout 与 stderr 追加写入的文件路径，必填。
	//
	// 后台进程不持有任何管道：父进程退出后它仍然要能继续写输出，因此输出只能落到
	// 文件，而不是由父进程转发完就关闭的管道。
	OutputPath string
}

// Background 是一个已脱离父进程运行的后台进程。
//
// 父进程退出后子进程继续运行；本类型只保留进程句柄与退出通知，不持有任何管道，
// 因此父进程退出不会让后台进程后续写输出失败。
type Background struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	waitErr error
}

// StartBackground 启动后台进程。
//
// 子进程进入新的会话（Unix）或独立进程组（Windows），stdin 指向空设备，stdout 与 stderr
// 追加写入 OutputPath。父进程不创建管道，只在内存中保留进程句柄用于失败时的终止。
func StartBackground(options BackgroundOptions) (*Background, error) {
	if err := options.Command.Validate(); err != nil {
		return nil, err
	}
	outputPath := strings.TrimSpace(options.OutputPath)
	if outputPath == "" {
		return nil, fmt.Errorf("%w: 后台进程输出文件不能为空", ErrInvalidCommand)
	}
	path, err := exec.LookPath(strings.TrimSpace(options.Command.Path))
	if err != nil {
		return nil, fmt.Errorf("process: 找不到可执行文件 %q: %w", options.Command.Path, err)
	}
	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("process: 打开后台进程输出文件失败: %s: %w", outputPath, err)
	}
	// stdin 指向空设备：后台进程不读任何输入，也不会继承终端或调用方的输入句柄。
	input, err := os.Open(os.DevNull)
	if err != nil {
		_ = output.Close()
		return nil, fmt.Errorf("process: 打开后台进程输入失败: %w", err)
	}
	cmd := exec.Command(path, options.Command.Args...)
	cmd.Dir = options.Command.Dir
	cmd.Env = options.Command.Env
	cmd.Stdin = input
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.SysProcAttr = detachedAttributes()
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, fmt.Errorf("process: 启动后台进程失败: %w", err)
	}
	// 子进程已继承自己的句柄：父进程这里的副本必须关闭，否则文件会跟随父进程生命周期。
	_ = input.Close()
	_ = output.Close()
	background := &Background{cmd: cmd, exited: make(chan struct{})}
	go background.reap()
	return background, nil
}

// PID 返回后台进程 ID。
func (b *Background) PID() int {
	if b == nil || b.cmd.Process == nil {
		return 0
	}
	return b.cmd.Process.Pid
}

// Exited 返回后台进程退出后关闭的通道。
func (b *Background) Exited() <-chan struct{} {
	if b == nil {
		return nil
	}
	return b.exited
}

// ExitErr 返回后台进程的退出结果，尚未退出时返回 nil。
func (b *Background) ExitErr() error {
	if b == nil {
		return nil
	}
	select {
	case <-b.exited:
		return b.waitErr
	default:
		return nil
	}
}

// Terminate 请求后台进程退出，宽限时间结束后仍未退出则强制终止。
//
// 无论走哪条路径都会回收子进程；强制终止后仍无法回收时返回 ErrShutdownTimeout。
func (b *Background) Terminate(grace time.Duration) error {
	if b == nil {
		return nil
	}
	if grace <= 0 {
		grace = DefaultGraceTimeout
	}
	select {
	case <-b.exited:
		return nil
	default:
	}
	_ = terminateSignal(b.cmd.Process)
	select {
	case <-b.exited:
		return nil
	case <-time.After(grace):
	}
	killErr := b.cmd.Process.Kill()
	select {
	case <-b.exited:
		return nil
	case <-time.After(grace):
	}
	if killErr != nil && !isProcessGone(killErr) {
		return fmt.Errorf("process: 终止后台进程失败: %w", killErr)
	}
	return fmt.Errorf("%w: 后台进程 %d 未能回收", ErrShutdownTimeout, b.PID())
}

// reap 回收后台进程并记录退出结果。
//
// 父进程退出时本 goroutine 会随进程一起结束：后台进程由操作系统重新挂到 init 或
// 终止，不需要父进程回收。
func (b *Background) reap() {
	waitErr := b.cmd.Wait()
	b.waitErr = waitErr
	close(b.exited)
}
