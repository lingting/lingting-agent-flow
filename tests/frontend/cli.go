package frontend

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
)

// cliLogTailLines 是失败信息里回显的日志行数上限。
const cliLogTailLines = 40

// cliRequest 是一次前端 CLI 调用的输入。
type cliRequest struct {
	// uiDir 是 CLI 的工作目录，固定为前端项目目录。
	uiDir string
	// args 是 pnpm 的参数（不含 pnpm 本身）。
	args []string
	// env 是追加到继承环境之上的变量。
	env []string
	// logPath 是 stdout/stderr 的合并输出文件。
	logPath string
	// session 是启动后登记进程号的会话句柄，供释放后自证没有残留。
	session testrunner.Handle
}

// cliRun 是一次前端 CLI 调用的结果。
type cliRun struct {
	exitCode int
	// output 是日志尾部，只在失败信息与日志摘要里使用。
	output string
}

// runFrontendCLI 执行一次前端 CLI 调用并等待结束。
//
// CLI 被放在独立进程组里：用例超时或取消时按进程树回收，避免 pnpm → node → 浏览器残留。
// 进程号在启动后立刻登记到会话句柄，终检据此核验没有遗留进程。
func runFrontendCLI(c *testrunner.Context, req cliRequest) (cliRun, error) {
	logFile, err := os.Create(req.logPath)
	if err != nil {
		return cliRun{}, fmt.Errorf("创建前端 CLI 输出文件失败: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	command := exec.Command("pnpm", req.args...)
	command.Dir = req.uiDir
	command.Env = append(os.Environ(), req.env...)
	command.SysProcAttr = testrunner.CommandProcessGroup()
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		return cliRun{}, fmt.Errorf("启动前端 CLI 失败: %w", err)
	}
	if err := testrunner.RecordFrontendProcess(req.session, command.Process.Pid); err != nil {
		killErr := testrunner.KillProcessTree(command.Process.Pid)
		_ = command.Wait()
		if killErr != nil {
			return cliRun{}, fmt.Errorf("%w；回收进程树也失败: %v", err, killErr)
		}
		return cliRun{}, err
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case <-wait:
		run := cliRun{output: tailOfFile(req.logPath, cliLogTailLines)}
		if command.ProcessState != nil {
			run.exitCode = command.ProcessState.ExitCode()
		}
		return run, nil
	case <-c.Context().Done():
		killErr := testrunner.KillProcessTree(command.Process.Pid)
		<-wait
		if killErr != nil {
			return cliRun{}, fmt.Errorf("用例结束后终止前端 CLI 失败: %w", killErr)
		}
		return cliRun{}, fmt.Errorf("前端 CLI 在用例结束前没有退出")
	}
}

// tailOfFile 读取文件末尾的若干行，读取失败时返回空字符串。
func tailOfFile(path string, lines int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	tail := make([]string, 0, lines)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		tail = append(tail, scanner.Text())
		if len(tail) > lines {
			tail = tail[1:]
		}
	}
	return strings.TrimSpace(strings.Join(tail, "\n"))
}

// cliEnv 组装前端 CLI 的环境变量：基础目录、产物目录与可选的额外变量。
func cliEnv(artifactDir string, extra map[string]string) []string {
	env := []string{"AGENTFLOW_UI_ARTIFACT_DIR=" + artifactDir}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+extra[key])
	}
	return env
}
