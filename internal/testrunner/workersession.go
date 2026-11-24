package testrunner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// workerStartupTimeout 是常驻 worker 从启动到回报就绪的上限。
const workerStartupTimeout = 30 * time.Second

// persistentWorker 是编排进程持有的一条常驻 worker 进程（V4 R3 §3）。
//
// 一个 worker 同一时刻只执行一条用例：派发与等待都在派发它的槽位 goroutine 内完成，
// 因此这里不需要额外互斥；进程的回收由独立 goroutine 负责，读等待只发生在通道上。
type persistentWorker struct {
	id      int
	command *exec.Cmd
	stdin   io.WriteCloser
	// events 是 worker 回报的事件；读取端遇到 EOF 时关闭它。
	events chan workerEvent
	// exited 在进程回收后关闭；waitErr 在此之前写入，因此关闭之后读取才有意义。
	exited  chan struct{}
	waitErr error
	// spawnAt 是进程启动时刻；复用 worker 时它早于用例被派发的时刻。
	spawnAt time.Time
	// executed 是该进程已经执行过的用例数：大于 0 时后续执行都算复用。
	executed int
}

// startPersistentWorker 启动一条常驻 worker 并等待它就绪。
//
// stderrPath 是该 worker 自己的标准错误日志：用例的标准流由 worker 自己分流到用例产物，
// 这里的日志只承载进程级输出（例如 Go 运行时 panic 栈）。
//
// home 是该槽位的数据主目录，以 AGENTFLOW_HOME 注入进程环境：worker 内所有用例共用它，
// 用例主体据此与服务进程落在同一份数据目录上。
func startPersistentWorker(executable string, workerID int, home string, stderrPath string) (*persistentWorker, error) {
	commandIn, stdin, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("创建 worker 指令管道失败: %w", err)
	}
	stdout, commandOut, err := os.Pipe()
	if err != nil {
		closeQuietly(commandIn, stdin)
		return nil, fmt.Errorf("创建 worker 回报管道失败: %w", err)
	}
	stderr, err := os.Create(stderrPath)
	if err != nil {
		closeQuietly(commandIn, stdin, stdout, commandOut)
		return nil, fmt.Errorf("创建 worker stderr 日志失败: %w", err)
	}
	command := exec.Command(executable, "--exec-worker", "--worker-id", strconv.Itoa(workerID))
	command.Stdin = commandIn
	command.Stdout = commandOut
	command.Stderr = stderr
	command.SysProcAttr = processGroupAttr()
	if home != "" {
		command.Env = append(os.Environ(), WorkerHomeEnv+"="+home)
	}
	spawnAt := time.Now()
	startErr := command.Start()
	// 父进程只保留自己那一端：子进程已经继承，父端不关闭就等不到 EOF。
	closeQuietly(commandIn, commandOut, stderr)
	if startErr != nil {
		closeQuietly(stdin, stdout)
		return nil, fmt.Errorf("启动 worker 进程失败: %w", startErr)
	}
	worker := &persistentWorker{
		id:      workerID,
		command: command,
		stdin:   stdin,
		events:  make(chan workerEvent, 8),
		exited:  make(chan struct{}),
		spawnAt: spawnAt,
	}
	go worker.collectEvents(stdout)
	go func() {
		worker.waitErr = command.Wait()
		close(worker.exited)
	}()
	if err := worker.awaitReady(); err != nil {
		_ = worker.stop(DefaultWorkerGrace)
		return nil, err
	}
	return worker, nil
}

// closeQuietly 关闭管道与文件句柄；关闭失败没有可报告的后续动作，因此只忽略错误。
func closeQuietly(files ...io.Closer) {
	for _, file := range files {
		_ = file.Close()
	}
}

// collectEvents 把 worker 的标准输出解析成事件。
//
// 通道被非协议输出污染时不再猜测语义：上报一条 invalid 事件并停止读取，worker 按已失效处理。
func (w *persistentWorker) collectEvents(reader io.ReadCloser) {
	defer close(w.events)
	defer func() { _ = reader.Close() }()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 4096), controlChannelMaxLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event workerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			w.events <- workerEvent{Kind: workerEventInvalid, Message: line}
			return
		}
		w.events <- event
	}
	if err := scanner.Err(); err != nil {
		w.events <- workerEvent{Kind: workerEventInvalid, Message: err.Error()}
	}
}

// awaitReady 等待 worker 回报就绪；启动失败或提前退出都如实回报。
func (w *persistentWorker) awaitReady() error {
	timer := time.NewTimer(workerStartupTimeout)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-w.events:
			if !ok {
				return fmt.Errorf("worker %d 未回报就绪即退出（%s）", w.id, w.exitSummary())
			}
			switch event.Kind {
			case workerEventReady:
				return nil
			case workerEventInvalid:
				return fmt.Errorf("worker %d 控制通道出现无法解析的输出: %s", w.id, event.Message)
			default:
				return fmt.Errorf("worker %d 在就绪前回报了 %q", w.id, event.Kind)
			}
		case <-timer.C:
			return fmt.Errorf("worker %d 在 %s 内没有回报就绪", w.id, workerStartupTimeout)
		}
	}
}

// exchange 把一条用例派发给常驻 worker，并等待它写出结果或被迫终止。
func (w *persistentWorker) exchange(ctx context.Context, inv workerInvocation) (workerObservation, error) {
	observation := workerObservation{WorkerID: w.id, SpawnAt: w.spawnAt, Reused: w.executed > 0}
	if err := removeStaleResult(inv.ResultPath); err != nil {
		return observation, err
	}
	command := workerCommand{
		Kind:            workerCommandRun,
		CaseID:          inv.Case.ID,
		ResultPath:      inv.ResultPath,
		ArtifactDir:     inv.ArtifactDir,
		HandlePath:      inv.HandlePath,
		WorkspaceDir:    inv.WorkspaceDir,
		StdoutPath:      inv.LogPaths.Stdout,
		StderrPath:      inv.LogPaths.Stderr,
		Timeout:         inv.Timeout,
		RealTests:       inv.RealTests,
		RealTestsReason: inv.RealTestsReason,
	}
	if err := w.send(command); err != nil {
		// 进程在派发之前就结束了：这条用例没有进入 worker，按"未执行"如实返回（W5）。
		observation.Restarted = true
		observation.FinishedAt = time.Now()
		observation.ExitCode = w.exitCode()
		return observation, nil
	}
	timer := time.NewTimer(inv.Timeout + inv.Grace)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-w.events:
			if !ok {
				return w.finishOnExit(observation, inv), nil
			}
			switch event.Kind {
			case workerEventStarted:
				if inv.OnStart != nil {
					inv.OnStart()
				}
			case workerEventDone:
				if event.CaseID != inv.Case.ID {
					continue
				}
				w.executed++
				return finishFromResult(observation, inv, event.Message), nil
			case workerEventInvalid:
				observation.Restarted = true
				observation.FinishedAt = time.Now()
				observation.ExitCode = ExitFailure
				return observation, fmt.Errorf(
					"worker %d 控制通道出现无法解析的输出: %s",
					w.id,
					event.Message,
				)
			default:
				// ready 与 shutdown 不属于用例执行阶段，忽略。
			}
		case <-timer.C:
			// 用例连同 worker 一起超时：先终止进程树，该 worker 不再复用。
			observation.TimedOut = true
			if err := w.terminate(inv.Grace); err != nil {
				return observation, err
			}
			observation.FinishedAt = time.Now()
			observation.ExitCode = w.exitCode()
			return observation, nil
		case <-ctx.Done():
			// 取消不伪造结论：这条用例由调用方记 error，worker 不再复用。
			observation.Cancelled = true
			if err := w.cancel(inv.Grace); err != nil {
				return observation, err
			}
			observation.FinishedAt = time.Now()
			observation.ExitCode = w.exitCode()
			return observation, nil
		}
	}
}

// finishOnExit 处理"worker 在用例结束前退出"：结论只能来自它自己写出的结果文件（W5）。
func (w *persistentWorker) finishOnExit(observation workerObservation, inv workerInvocation) workerObservation {
	observation.Restarted = true
	observation.FinishedAt = time.Now()
	observation.ExitCode = w.exitCodeAfterReap()
	result, hasResult, readErr := readWorkerResult(inv.ResultPath)
	observation.Result = result
	observation.HasResult = hasResult
	observation.ReadError = readErr
	if hasResult {
		// 用例在退出之前写出了结果文件：结论以它为准，退出码沿用用例自己的映射。
		observation.ExitCode = result.ExitCode
	}
	return observation
}

// finishFromResult 在 worker 回报结束之后读取它写出的结果文件。
func finishFromResult(observation workerObservation, inv workerInvocation, message string) workerObservation {
	observation.FinishedAt = time.Now()
	result, hasResult, readErr := readWorkerResult(inv.ResultPath)
	observation.Result = result
	observation.HasResult = hasResult
	observation.ReadError = readErr
	if hasResult {
		// 复用 worker 没有"本次用例的进程退出码"：退出码由用例结论决定，与结果文件一致。
		observation.ExitCode = result.ExitCode
		return observation
	}
	if readErr == nil {
		observation.ReadError = fmt.Errorf(
			"worker 回报用例结束但没有写出结果文件（%s）",
			defaultIfBlank(message, "没有说明"),
		)
	}
	observation.ExitCode = ExitFailure
	return observation
}

// send 把一条指令写到 worker 的标准输入。
func (w *persistentWorker) send(command workerCommand) error {
	payload, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("编码派发指令失败: %w", err)
	}
	if _, err := w.stdin.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("向 worker %d 派发指令失败: %w", w.id, err)
	}
	return nil
}

// alive 报告进程是否还活着：已被回收的进程不能复用。
func (w *persistentWorker) alive() bool {
	select {
	case <-w.exited:
		return false
	default:
		return true
	}
}

// exitCode 返回已回收进程的退出码；进程仍在运行时返回 -1。
func (w *persistentWorker) exitCode() int {
	select {
	case <-w.exited:
		return w.exitCodeAfterReap()
	default:
		return -1
	}
}

// exitCodeAfterReap 等待进程回收并返回退出码。
func (w *persistentWorker) exitCodeAfterReap() int {
	<-w.exited
	if w.command.ProcessState == nil {
		return -1
	}
	return w.command.ProcessState.ExitCode()
}

// exitSummary 把进程回收结论压成一句话，供启动失败的错误信息使用。
func (w *persistentWorker) exitSummary() string {
	code := w.exitCodeAfterReap()
	if code >= 0 {
		return fmt.Sprintf("退出码 %d", code)
	}
	if w.waitErr != nil {
		return w.waitErr.Error()
	}
	return "已退出"
}

// terminate 终止 worker 进程树并等待回收；回收失败如实返回错误，不当作已终止。
func (w *persistentWorker) terminate(grace time.Duration) error {
	_ = w.stdin.Close()
	if !w.alive() {
		// 进程已经结束：没有可终止的目标，回收由 Wait 完成。
		<-w.exited
		return nil
	}
	if err := killProcessTree(w.command.Process.Pid); err != nil {
		return err
	}
	select {
	case <-w.exited:
		return nil
	case <-time.After(grace):
		return fmt.Errorf("终止 worker %d 的进程树后 %s 内仍未回收", w.id, grace)
	}
}

// cancel 在运行被取消后先给宽限期，到期再终止进程树。
//
// 编排进程与 worker 之间没有反向取消通道：worker 只在用例之间读指令，因此"先投递取消"
// 在这里就是停止等待并给出宽限，与一次性 worker 的口径一致。
func (w *persistentWorker) cancel(grace time.Duration) error {
	select {
	case <-w.exited:
		_ = w.stdin.Close()
		return nil
	case <-time.After(grace):
	}
	return w.terminate(grace)
}

// stop 让 worker 自行退出：先投递 shutdown，宽限期后仍不退出就终止进程树。
func (w *persistentWorker) stop(grace time.Duration) error {
	sendErr := w.send(workerCommand{Kind: workerCommandShutdown})
	_ = w.stdin.Close()
	if sendErr == nil {
		select {
		case <-w.exited:
			return nil
		case <-time.After(grace):
		}
	}
	return w.terminate(grace)
}
