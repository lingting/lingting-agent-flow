package testrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// DefaultWorkerGrace 是在用例超时后额外等待 worker 自行收尾的时间，超过后终止进程树。
const DefaultWorkerGrace = 10 * time.Second

// workerInvocation 是一次 worker 子进程的启动参数。
type workerInvocation struct {
	Executable string
	// WorkerID 是编排进程分配给这条一次性 worker 的序号，写进用例上下文供观测。
	WorkerID        int
	Case            Case
	Timeout         time.Duration
	Grace           time.Duration
	HandlePath      string
	ResultPath      string
	ArtifactDir     string
	WorkspaceDir    string
	LogPaths        ArtifactPaths
	RealTests       bool
	RealTestsReason string
	// Home 是这次用例的数据主目录，以 AGENTFLOW_HOME 注入 worker 进程。
	Home string
	// OnStart 在用例真正开始执行后回调（一次性 worker 是进程启动之后），空值表示不关心。
	OnStart func()
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
	// WorkerID 是执行该用例的 worker 序号；0 表示这次执行没有进入 worker 进程。
	WorkerID int
	// Spawned 表示这次执行新启动了 worker 进程。
	Spawned bool
	// Reused 表示这次执行复用了已经跑过用例的 worker 进程。
	Reused bool
	// Isolated 表示这次执行使用了独占用例的一次性 worker。
	Isolated bool
	// Restarted 表示该 worker 进程没有按协议收尾（崩溃、被污染或派发前已退出）。
	Restarted bool
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
	if inv.WorkerID > 0 {
		arguments = append(arguments, "--worker-id", strconv.Itoa(inv.WorkerID))
	}
	if inv.HandlePath != "" {
		arguments = append(arguments, "--handles", inv.HandlePath)
	}
	if inv.WorkspaceDir != "" {
		arguments = append(arguments, "--workspace", inv.WorkspaceDir)
	}
	command := exec.Command(inv.Executable, arguments...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = processGroupAttr()
	// 数据主目录按槽位指定一次：共享 worker 的所有用例共用它，独占 worker 用自己那份。
	if inv.Home != "" {
		command.Env = append(os.Environ(), WorkerHomeEnv+"="+inv.Home)
	}
	if err := command.Start(); err != nil {
		return observation, fmt.Errorf("启动 worker 失败: %w", err)
	}
	observation.Spawned = true
	if inv.OnStart != nil {
		inv.OnStart()
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
