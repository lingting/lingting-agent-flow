package testrunner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// 本文件是 V4 R3 的 worker 侧：常驻 worker 按顺序领取并执行用例，每条用例前后重置
// 进程级状态（W3），用例结论仍然由它自己写出的结果文件表达（W5）。
//
// 重置范围是 W3 冻结的清单：工作目录、环境变量、标准流（日志文件）与资源句柄文件。
// 包级变量不在其中——它由迁移时逐包核对并登记（R5–R8），与 go test 同包共用进程的情况一致。

// PersistentWorkerOptions 是常驻 worker 模式的启动输入。
type PersistentWorkerOptions struct {
	// WorkerID 是编排进程分配的序号，写进每条用例的执行上下文，只用于观测与排错。
	WorkerID int
}

// RunPersistentWorker 在 worker 子进程中循环领取并执行用例，直到收到 shutdown 或父进程关闭 stdin。
//
// 返回的 error 只表示控制通道本身失效（无法回报、无法解析指令）；用例结论不进返回值。
func RunPersistentWorker(opts PersistentWorkerOptions) error {
	baseline, err := captureWorkerBaseline()
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(baseline.control)
	if err := encoder.Encode(workerEvent{Kind: workerEventReady}); err != nil {
		return fmt.Errorf("回报 worker %d 就绪失败: %w", opts.WorkerID, err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4096), controlChannelMaxLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var command workerCommand
		if err := json.Unmarshal([]byte(line), &command); err != nil {
			return fmt.Errorf("worker %d 解析派发指令失败: %w", opts.WorkerID, err)
		}
		stop, err := handleWorkerCommand(encoder, baseline, opts, command)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("worker %d 读取派发指令失败: %w", opts.WorkerID, err)
	}
	return nil
}

// handleWorkerCommand 处理一条指令；stop 表示 worker 应当退出。
func handleWorkerCommand(
	encoder *json.Encoder,
	baseline workerBaseline,
	opts PersistentWorkerOptions,
	command workerCommand,
) (bool, error) {
	switch command.Kind {
	case workerCommandShutdown:
		if err := encoder.Encode(workerEvent{Kind: workerEventShutdown}); err != nil {
			return true, fmt.Errorf("回报 shutdown 失败: %w", err)
		}
		return true, nil
	case workerCommandRun:
		return false, runCommandedCase(encoder, baseline, opts, command)
	default:
		return true, fmt.Errorf("未知的派发指令 %q", command.Kind)
	}
}

// runCommandedCase 执行一条被派发的用例：先回报开始，再执行并回报结束。
//
// 先回报开始是有意为之：编排进程的取消注入与"是否真的跑起来"的观测以它为准，而不是以
// 派发时刻为准。
func runCommandedCase(
	encoder *json.Encoder,
	baseline workerBaseline,
	opts PersistentWorkerOptions,
	command workerCommand,
) error {
	if err := encoder.Encode(workerEvent{Kind: workerEventStarted, CaseID: command.CaseID}); err != nil {
		return fmt.Errorf("回报用例 %q 开始失败: %w", command.CaseID, err)
	}
	message := executeCommandedCase(baseline, opts, command)
	event := workerEvent{Kind: workerEventDone, CaseID: command.CaseID, Message: message}
	if err := encoder.Encode(event); err != nil {
		return fmt.Errorf("回报用例 %q 结束失败: %w", command.CaseID, err)
	}
	return nil
}

// executeCommandedCase 在重置后的进程状态与该用例的日志文件下执行用例主体。
//
// 返回的消息只描述"为什么没有可信结果"（例如结果文件写不出来）；用例结论本身以结果文件为准。
func executeCommandedCase(
	baseline workerBaseline,
	opts PersistentWorkerOptions,
	command workerCommand,
) (message string) {
	if err := baseline.reset(); err != nil {
		return err.Error()
	}
	stdout, err := os.Create(command.StdoutPath)
	if err != nil {
		return fmt.Sprintf("创建用例 stdout 日志失败: %v", err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.Create(command.StderrPath)
	if err != nil {
		return fmt.Sprintf("创建用例 stderr 日志失败: %v", err)
	}
	defer func() { _ = stderr.Close() }()
	// 用例的日志分流到自己的产物目录；控制通道持有的是启动时的原始标准输出。
	os.Stdout = stdout
	os.Stderr = stderr
	defer func() {
		// 用例结束后回到空转输出：迟到的日志不会写进控制通道，也不会串到下一条用例。
		os.Stdout = baseline.idle
		os.Stderr = baseline.idle
	}()
	if _, err := ExecuteWorker(WorkerRequest{
		CaseID:          command.CaseID,
		ResultPath:      command.ResultPath,
		ArtifactDir:     command.ArtifactDir,
		HandlePath:      command.HandlePath,
		WorkspaceDir:    command.WorkspaceDir,
		Timeout:         command.Timeout,
		RealTests:       command.RealTests,
		RealTestsReason: command.RealTestsReason,
		WorkerID:        opts.WorkerID,
	}); err != nil {
		return err.Error()
	}
	return ""
}

// workerBaseline 是 worker 进程启动时的进程级状态快照：每条用例之前恢复，避免用例之间串味。
type workerBaseline struct {
	// control 是控制通道用的原始标准输出：用例输出与进程输出都不得写到它上面。
	control *os.File
	// idle 是用例之间的标准流目标：用例结束后迟到的输出落在这里，不会污染控制通道。
	idle *os.File
	// dir 是 worker 启动时的工作目录。
	dir string
	// env 是 worker 启动时的环境变量快照，形如 "KEY=VALUE"。
	env []string
}

// captureWorkerBaseline 记录 worker 启动瞬间的标准流、工作目录与环境变量。
//
// 启动之后进程的标准输出与标准错误立刻指向空设备：控制通道只通过 encoder 持有原始标准
// 输出，用例的任何输出（含用例结束后迟到的日志）都不会写进控制通道。
func captureWorkerBaseline() (workerBaseline, error) {
	dir, err := os.Getwd()
	if err != nil {
		return workerBaseline{}, fmt.Errorf("读取 worker 工作目录失败: %w", err)
	}
	idle, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return workerBaseline{}, fmt.Errorf("打开 worker 空转输出失败: %w", err)
	}
	baseline := workerBaseline{control: os.Stdout, idle: idle, dir: dir, env: os.Environ()}
	os.Stdout = idle
	os.Stderr = idle
	return baseline, nil
}

// reset 恢复工作目录与环境变量：用例改动过的进程级状态不得带到下一条用例。
//
// 先删除快照之外的变量、再恢复快照值：Windows 的环境变量名不区分大小写，用例把 PATH 写成
// Path 时先恢复再删除会把 PATH 一起删掉。
func (b workerBaseline) reset() error {
	if err := os.Chdir(b.dir); err != nil {
		return fmt.Errorf("恢复 worker 工作目录失败: %w", err)
	}
	known := make(map[string]struct{}, len(b.env))
	for _, entry := range b.env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		known[key] = struct{}{}
	}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := known[key]; exists {
			continue
		}
		if err := os.Unsetenv(key); err != nil {
			return fmt.Errorf("清除用例新增的环境变量 %q 失败: %w", key, err)
		}
	}
	for _, entry := range b.env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("恢复环境变量 %q 失败: %w", key, err)
		}
	}
	return nil
}
