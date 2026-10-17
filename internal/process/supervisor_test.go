package process_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/process"
)

// TestMain 让测试二进制同时充当受控的子进程。
//
// 只有设置了行为环境变量时才走替身分支，其余情况执行正常测试。
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeModeEnv); mode != "" {
		os.Exit(runFakeProcess(mode))
	}
	os.Exit(m.Run())
}

// fakeCommand 构造以测试二进制为可执行文件的启动描述。
func fakeCommand(t *testing.T, mode string, env ...string) process.Command {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("获取测试可执行文件失败: %v", err)
	}
	return process.Command{
		Path: executable,
		Args: []string{"-test.run=^$"},
		Env:  append(os.Environ(), append([]string{fakeModeEnv + "=" + mode}, env...)...),
	}
}

// writeTempFile 在临时目录写入文件并返回路径。
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入临时文件失败: %v", err)
	}
	return path
}

// collectLines 读取全部输出行。
func collectLines(supervisor *process.Supervisor) []string {
	var lines []string
	for line := range supervisor.Lines() {
		if line.Err == nil {
			lines = append(lines, line.Text)
		}
	}
	return lines
}

// TestStartRejectsMissingExecutable 验证可执行程序缺失时启动失败。
func TestStartRejectsMissingExecutable(t *testing.T) {
	_, err := process.Start(process.Command{Path: "agentflow-not-exists"}, process.Options{})
	if err == nil {
		t.Fatalf("期望启动失败，实际成功")
	}
}

// TestStartRejectsEmptyExecutable 验证空的可执行程序被拒绝。
func TestStartRejectsEmptyExecutable(t *testing.T) {
	_, err := process.Start(process.Command{Path: "  "}, process.Options{})
	if !errors.Is(err, process.ErrInvalidCommand) {
		t.Fatalf("期望 ErrInvalidCommand，实际 %v", err)
	}
}

// TestLinesSplitOnLFOnly 验证只按 LF 分界并去掉可选的 CR。
func TestLinesSplitOnLFOnly(t *testing.T) {
	content := "first\r\nsecond\n\u2028third\u2029\n\nfourth-without-newline"
	path := writeTempFile(t, "stdout.txt", content)
	supervisor, err := process.Start(fakeCommand(t, "emit", fakeStdoutEnv+"="+path), process.Options{})
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	lines := collectLines(supervisor)
	if _, err := supervisor.Shutdown(time.Second); err != nil {
		t.Fatalf("关闭子进程失败: %v", err)
	}
	expected := []string{"first", "second", "\u2028third\u2029", "fourth-without-newline"}
	if strings.Join(lines, "|") != strings.Join(expected, "|") {
		t.Fatalf("输出行不符合预期: %#v", lines)
	}
}

// TestStderrIsSeparateFromStdout 验证 stderr 不进入协议输出。
func TestStderrIsSeparateFromStdout(t *testing.T) {
	stdout := writeTempFile(t, "stdout.txt", "only-stdout\n")
	stderr := writeTempFile(t, "stderr.txt", "only-stderr\n")
	supervisor, err := process.Start(
		fakeCommand(t, "emit", fakeStdoutEnv+"="+stdout, fakeStderrEnv+"="+stderr),
		process.Options{},
	)
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	lines := collectLines(supervisor)
	exit, err := supervisor.Shutdown(time.Second)
	if err != nil {
		t.Fatalf("关闭子进程失败: %v", err)
	}
	if exit.Code != 0 {
		t.Fatalf("期望退出码 0，实际 %d", exit.Code)
	}
	if strings.Join(lines, "|") != "only-stdout" {
		t.Fatalf("stdout 不符合预期: %#v", lines)
	}
	if !strings.Contains(supervisor.Stderr(), "only-stderr") {
		t.Fatalf("stderr 未被捕获: %q", supervisor.Stderr())
	}
}

// TestExitCodeIsReported 验证非零退出码被正确回收。
func TestExitCodeIsReported(t *testing.T) {
	supervisor, err := process.Start(fakeCommand(t, "exit3"), process.Options{})
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	collectLines(supervisor)
	exit := supervisor.Wait()
	if exit.Code != 3 {
		t.Fatalf("期望退出码 3，实际 %d", exit.Code)
	}
	if exit.Err == nil {
		t.Fatalf("期望非零退出返回错误")
	}
}

// TestShutdownTerminatesHungProcess 验证宽限时间耗尽后强制终止。
func TestShutdownTerminatesHungProcess(t *testing.T) {
	supervisor, err := process.Start(fakeCommand(t, "hang"), process.Options{GraceTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	start := time.Now()
	exit, err := supervisor.Shutdown(150 * time.Millisecond)
	if err != nil {
		t.Fatalf("关闭子进程失败: %v", err)
	}
	if exit.Code == 0 {
		t.Fatalf("被强制终止的进程不应返回退出码 0")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("关闭耗时过长: %s", elapsed)
	}
}

// TestWriteToStdin 验证可以向子进程 stdin 写入数据。
func TestWriteToStdin(t *testing.T) {
	supervisor, err := process.Start(fakeCommand(t, "stdin_echo"), process.Options{})
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	if err := supervisor.Write([]byte("hello\n")); err != nil {
		t.Fatalf("写入 stdin 失败: %v", err)
	}
	if err := supervisor.CloseStdin(); err != nil {
		t.Fatalf("关闭 stdin 失败: %v", err)
	}
	lines := collectLines(supervisor)
	if _, err := supervisor.Shutdown(time.Second); err != nil {
		t.Fatalf("关闭子进程失败: %v", err)
	}
	if strings.Join(lines, "|") != "hello" {
		t.Fatalf("stdin 回显不符合预期: %#v", lines)
	}
}

// TestGracefulShutdownOnStdinClose 验证关闭 stdin 即可让进程正常退出。
func TestGracefulShutdownOnStdinClose(t *testing.T) {
	supervisor, err := process.Start(fakeCommand(t, "exit_on_stdin_close"), process.Options{})
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	exit, err := supervisor.Shutdown(5 * time.Second)
	if err != nil {
		t.Fatalf("关闭子进程失败: %v", err)
	}
	if exit.Code != 0 {
		t.Fatalf("期望退出码 0，实际 %d", exit.Code)
	}
}

// TestConcurrentOutputDoesNotDeadlock 验证 stdout 与 stderr 同时大量输出时不死锁。
func TestConcurrentOutputDoesNotDeadlock(t *testing.T) {
	supervisor, err := process.Start(
		fakeCommand(t, "flood"),
		process.Options{StderrLimit: 1024, GraceTimeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	count := 0
	for line := range supervisor.Lines() {
		if line.Err != nil {
			t.Fatalf("读取输出失败: %v", line.Err)
		}
		count++
	}
	exit, err := supervisor.Shutdown(5 * time.Second)
	if err != nil {
		t.Fatalf("关闭子进程失败: %v", err)
	}
	if exit.Code != 0 {
		t.Fatalf("期望退出码 0，实际 %d", exit.Code)
	}
	if count != 256 {
		t.Fatalf("期望读取 256 行，实际 %d", count)
	}
	if len(supervisor.Stderr()) > 1024 {
		t.Fatalf("stderr 保留内容超过上限: %d", len(supervisor.Stderr()))
	}
}

// TestOverlongLineIsRejected 验证超长行返回明确错误。
func TestOverlongLineIsRejected(t *testing.T) {
	path := writeTempFile(t, "stdout.txt", strings.Repeat("x", 512)+"\n")
	supervisor, err := process.Start(
		fakeCommand(t, "emit", fakeStdoutEnv+"="+path),
		process.Options{MaxLineLength: 64},
	)
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	var failure error
	for line := range supervisor.Lines() {
		if line.Err != nil {
			failure = line.Err
		}
	}
	_, _ = supervisor.Shutdown(time.Second)
	if failure == nil {
		t.Fatalf("期望超长行返回错误")
	}
	if !strings.Contains(failure.Error(), "上限") {
		t.Fatalf("错误信息缺少长度说明: %v", failure)
	}
}

// TestRepeatedShutdownIsSafe 验证重复与并发关闭不会重复回收进程。
func TestRepeatedShutdownIsSafe(t *testing.T) {
	supervisor, err := process.Start(fakeCommand(t, "exit_on_stdin_close"), process.Options{})
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	results := make(chan error, 4)
	for index := 0; index < 4; index++ {
		go func() {
			_, shutdownErr := supervisor.Shutdown(5 * time.Second)
			results <- shutdownErr
		}()
	}
	for index := 0; index < 4; index++ {
		if err := <-results; err != nil {
			t.Fatalf("并发关闭失败: %v", err)
		}
	}
	if err := supervisor.CloseStdin(); err != nil {
		t.Fatalf("重复关闭 stdin 失败: %v", err)
	}
	if err := supervisor.Terminate(); err != nil {
		t.Fatalf("重复终止失败: %v", err)
	}
}

// TestPIDIsReported 验证子进程 ID 被正确记录。
func TestPIDIsReported(t *testing.T) {
	supervisor, err := process.Start(fakeCommand(t, "exit_on_stdin_close"), process.Options{})
	if err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	if supervisor.PID() <= 0 {
		t.Fatalf("期望有效的进程 ID，实际 %d", supervisor.PID())
	}
	if _, err := supervisor.Shutdown(5 * time.Second); err != nil {
		t.Fatalf("关闭子进程失败: %v", err)
	}
}
