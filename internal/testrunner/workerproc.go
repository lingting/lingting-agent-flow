package testrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// DefaultWorkerGrace 是在用例超时后额外等待 worker 自行收尾的时间，超过后终止进程树。
const DefaultWorkerGrace = 10 * time.Second

// workerInvocation 是一次 worker 子进程的启动参数。
type workerInvocation struct {
	Executable      string
	Case            Case
	Timeout         time.Duration
	Grace           time.Duration
	HandlePath      string
	ResultPath      string
	ArtifactDir     string
	LogPaths        ArtifactPaths
	RealTests       bool
	RealTestsReason string
	// OnSpawn 在 worker 进程真正启动后回调，空值表示不关心。
	OnSpawn func()
}

// workerObservation 是编排进程对 worker 的原始观测，尚不包含资源释放结论。
type workerObservation struct {
	ExitCode int
	TimedOut bool
	// Cancelled 表示 worker 因整次运行被取消而终止。
	Cancelled  bool
	SpawnAt    time.Time
	FinishedAt time.Time
	Result     CaseResult
	HasResult  bool
	ReadError  error
}

// runWorkerProcess 启动 worker、采集其独占输出，并在超时或取消后终止整棵进程树。
func runWorkerProcess(ctx context.Context, inv workerInvocation) (workerObservation, error) {
	observation := workerObservation{SpawnAt: time.Now()}
	if err := removeStaleResult(inv.ResultPath); err != nil {
		return observation, err
	}
	stdout, err := os.Create(inv.LogPaths.Stdout)
	if err != nil {
		return observation, fmt.Errorf("创建 worker stdout 日志失败: %w", err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.Create(inv.LogPaths.Stderr)
	if err != nil {
		return observation, fmt.Errorf("创建 worker stderr 日志失败: %w", err)
	}
	defer func() { _ = stderr.Close() }()

	arguments := []string{
		"--exec-case", inv.Case.ID,
		"--result-file", inv.ResultPath,
		"--artifact-dir", inv.ArtifactDir,
		"--timeout", inv.Timeout.String(),
		"--real-tests-grant", grantArgument(inv.RealTests),
		"--real-tests-reason", singleLine(inv.RealTestsReason),
	}
	if inv.HandlePath != "" {
		arguments = append(arguments, "--handles", inv.HandlePath)
	}
	command := exec.Command(inv.Executable, arguments...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = processGroupAttr()
	if err := command.Start(); err != nil {
		return observation, fmt.Errorf("启动 worker 失败: %w", err)
	}
	if inv.OnSpawn != nil {
		inv.OnSpawn()
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	timer := time.NewTimer(inv.Timeout + inv.Grace)
	defer timer.Stop()
	select {
	case <-wait:
	case <-timer.C:
		observation.TimedOut = true
		if err := terminateWorker(command, wait, inv.Grace); err != nil {
			return observation, err
		}
	case <-ctx.Done():
		// 整次运行被取消：先给宽限期，到期再终止进程树；取消不伪造结论，由调用方记 error。
		observation.Cancelled = true
		if err := awaitWorkerAfterCancel(command, wait, inv.Grace); err != nil {
			return observation, err
		}
	}
	observation.FinishedAt = time.Now()
	if command.ProcessState != nil {
		observation.ExitCode = command.ProcessState.ExitCode()
	}
	result, hasResult, readErr := readWorkerResult(inv.ResultPath)
	observation.Result = result
	observation.HasResult = hasResult
	observation.ReadError = readErr
	return observation, nil
}

// terminateWorker 终止 worker 进程树并等待回收；回收失败如实返回错误，不当作已终止。
func terminateWorker(command *exec.Cmd, wait <-chan error, grace time.Duration) error {
	if err := killProcessTree(command.Process.Pid); err != nil {
		return err
	}
	select {
	case <-wait:
		return nil
	case <-time.After(grace):
		return fmt.Errorf("终止 worker 进程树后 %s 内仍未回收", grace)
	}
}

// removeStaleResult 清理同名结果文件，避免把上一次运行的结论当成本次结果。
func removeStaleResult(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("清理旧用例结果文件失败: %w", err)
	}
	return nil
}

// readWorkerResult 读取 worker 写出的结果文件；文件不存在不视为读取错误。
func readWorkerResult(path string) (CaseResult, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CaseResult{}, false, nil
		}
		return CaseResult{}, false, fmt.Errorf("读取用例结果文件失败: %w", err)
	}
	var result CaseResult
	if err := json.Unmarshal(data, &result); err != nil {
		return CaseResult{}, false, fmt.Errorf("解析用例结果文件失败: %w", err)
	}
	return result, true, nil
}

// grantArgument 把授权结论转换成 worker 参数，worker 不再自行判定。
func grantArgument(allowed bool) string {
	if allowed {
		return "allowed"
	}
	return "denied"
}
