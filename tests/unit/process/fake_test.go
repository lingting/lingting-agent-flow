package process_test

import (
	"io"
	"os"
	"strings"
)

// 测试替身使用的环境变量。
const (
	// fakeModeEnv 选择测试替身的行为。
	fakeModeEnv = "AGENTFLOW_PROCESS_FAKE"
	// fakeStdoutEnv 指定测试替身原样输出的文件。
	fakeStdoutEnv = "AGENTFLOW_PROCESS_STDOUT"
	// fakeStderrEnv 指定测试替身原样输出的标准错误文件。
	fakeStderrEnv = "AGENTFLOW_PROCESS_STDERR"
)

// runFakeProcess 以指定行为模拟一个外部进程。
//
// 测试通过重新执行测试二进制本身得到可控的子进程，不依赖具体操作系统的 shell。
func runFakeProcess(mode string) int {
	switch mode {
	case "emit":
		writeFile(os.Stdout, os.Getenv(fakeStdoutEnv))
		writeFile(os.Stderr, os.Getenv(fakeStderrEnv))
		return 0
	case "exit3":
		writeFile(os.Stderr, os.Getenv(fakeStderrEnv))
		return 3
	case "hang":
		// 忽略 stdin 关闭，只有强制终止才能结束该进程。
		select {}
	case "stdin_echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		return 0
	case "exit_on_stdin_close":
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	case "flood":
		flood()
		return 0
	default:
		return 2
	}
}

// writeFile 把文件内容写入目标流，文件为空时不做任何事。
func writeFile(target *os.File, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_, _ = target.Write(data)
}

// flood 同时向 stdout 与 stderr 写入大量数据，用于验证不会发生管道死锁。
func flood() {
	block := strings.Repeat("x", 4096) + "\n"
	for index := 0; index < 256; index++ {
		_, _ = io.WriteString(os.Stdout, block)
		_, _ = io.WriteString(os.Stderr, block)
	}
}
