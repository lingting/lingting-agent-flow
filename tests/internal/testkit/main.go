// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 该文件提供统一的测试入口：把本次运行的完整测试输出（含 t.Logf、WARN [SKIP]
// 与 slog 日志）写入本次运行根目录下的 test.log，使失败现场与跳过原因可以事后
// 复核，而不必重跑测试。
package testkit

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// LogFileName 是本次测试运行的日志文件名。
const LogFileName = "test.log"

// LatestLogFileName 是指向最新一次运行日志的稳定文件名。
//
// 复核时不必翻时间戳：基目录下该文件的内容为最新一次运行 test.log 的绝对路径。
const LatestLogFileName = "latest-run.txt"

// 本次运行的日志状态。
//
// consoleOutput / consoleError 保存进程启动时的真实标准输出与标准错误：
// 重定向之后仍然需要它们把收尾信息写到终端。
var (
	consoleOutput = os.Stdout
	consoleError  = os.Stderr

	runLogOnce sync.Once
	runLogPath string
	runLogFile *os.File
	runLogErr  error
	runLogPipe *os.File
	runLogDone chan struct{}
	runLogSink io.Writer
)

// Main 是测试包统一入口，返回进程退出码。
//
// 它在 m.Run 之前完成三件事：
//
//  1. 创建本次运行的根目录，并在其中打开 test.log；
//  2. 把 os.Stdout 与 os.Stderr 接到一个管道，管道内容同时写到控制台与 test.log，
//     使 t.Logf、WARN [SKIP]、fmt.Println 与 slog 输出一致地落盘；
//  3. 把 slog 默认日志器指向同一目标，使各包不再各自丢弃日志。
//
// 同时默认打开 -test.v：非 verbose 模式下 go test 会缓冲包输出、只在失败时打印，
// 逐用例结论与跳过原因都会丢失；命令行显式给出 -test.v=false 时以命令行为准。
func Main(m *testing.M) int {
	if err := setupRunLog(); err != nil {
		fmt.Fprintf(os.Stderr, "初始化测试日志失败: %v\n", err)
		return m.Run()
	}
	forceVerbose()
	code := m.Run()
	// 最终核验必须在日志刷新之前执行：核验结论（清理结果、docker ps -a 证据）
	// 要与测试结论出现在同一份 test.log 中，并且核验失败必须改变进程退出码。
	finalizeFailed := runRegisteredFinalizers()
	flushRunLog()
	if finalizeFailed && code == 0 {
		code = 1
	}
	return code
}

// RunLogPath 返回本次运行日志文件的绝对路径，尚未初始化时返回空字符串。
func RunLogPath() string {
	return runLogPath
}

// setupRunLog 创建运行根目录与日志文件，并把标准输出与 slog 重定向过去。
func setupRunLog() error {
	runLogOnce.Do(func() {
		root := runRootInternal()
		if rootErr != nil {
			runLogErr = rootErr
			return
		}
		path := filepath.Join(root, LogFileName)
		file, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if openErr != nil {
			runLogErr = fmt.Errorf("创建测试日志失败: %s: %w", path, openErr)
			return
		}
		runLogPath = path
		runLogFile = file
		// 控制台输出与日志文件同时接收，终端上仍然能看到完整输出。
		runLogSink = io.MultiWriter(consoleOutput, file)
		if redirectErr := redirectStdio(runLogSink); redirectErr != nil {
			runLogErr = redirectErr
			return
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(runLogSink, nil)))
		if pointerErr := writeLatestRunPointer(root, path); pointerErr != nil {
			fmt.Fprintf(runLogSink, "WARN 写入最新运行指针失败: %v\n", pointerErr)
		}
		fmt.Fprintf(runLogSink, "TEST_ARTIFACT_DIR=%s\nTEST_LOG=%s\n", root, path)
	})
	return runLogErr
}

// redirectStdio 把标准输出与标准错误接到同时写控制台与日志文件的管道。
//
// testing.T 在 m.Run 时把 os.Stdout 作为自己的输出目标，因此这里必须让它成为
// 真实文件（管道写端），而不是任意 io.Writer；管道读端由后台 goroutine 复制到
// sink，从而同时保留终端输出与落盘日志。
func redirectStdio(sink io.Writer) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("创建测试输出管道失败: %w", err)
	}
	runLogPipe = writer
	runLogDone = make(chan struct{})
	go func() {
		defer close(runLogDone)
		_, _ = io.Copy(sink, reader)
		_ = reader.Close()
	}()
	// 标准错误与标准输出共享同一写端，避免两路输出在日志里交错成半行。
	os.Stdout = writer
	os.Stderr = writer
	return nil
}

// forceVerbose 在命令行未显式关闭时打开 -test.v。
//
// testing 包在初始化阶段已经注册该标志，这里只改它的值；
// 命令行显式给出 -test.v=false 时，随后的 flag.Parse 会以命令行为准。
func forceVerbose() {
	if flag.Parsed() {
		return
	}
	verbose := flag.Lookup("test.v")
	if verbose == nil || verbose.Value.String() == "true" {
		return
	}
	// 标志类型不支持直接设置时不影响测试执行，只是缺少逐用例输出。
	_ = flag.Set("test.v", "true")
}

// writeLatestRunPointer 在基目录写入最新运行日志指针。
func writeLatestRunPointer(root, logPath string) error {
	target := filepath.Join(filepath.Dir(root), LatestLogFileName)
	return os.WriteFile(target, []byte(logPath+"\n"), 0o644)
}

// flushRunLog 关闭管道写端，等待日志复制完成，并同步关闭日志文件。
func flushRunLog() {
	// 先把标准输出换回真实控制台，避免收尾日志落到已关闭的管道。
	os.Stdout = consoleOutput
	os.Stderr = consoleError
	if runLogPipe != nil {
		_ = runLogPipe.Close()
		if runLogDone != nil {
			<-runLogDone
		}
		runLogPipe = nil
	}
	if runLogFile == nil {
		return
	}
	if err := runLogFile.Sync(); err != nil {
		fmt.Fprintf(os.Stderr, "同步测试日志失败: %v\n", err)
	}
	if err := runLogFile.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "关闭测试日志失败: %v\n", err)
	}
	runLogFile = nil
}
