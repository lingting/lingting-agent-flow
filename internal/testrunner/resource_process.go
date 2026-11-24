package testrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// caseHomeEnv 返回注入给资源子进程的数据主目录变量；上下文没有下发时返回空。
//
// 自证与清理都不需要它：只有启动被测进程时才用它把数据目录固定在用例的临时目录里。
func caseHomeEnv(ctx context.Context) []string {
	home := caseHomeOf(ctx)
	if home == "" {
		return nil
	}
	return []string{WorkerHomeEnv + "=" + home}
}

// processStartupWindow 是新起子进程的存活观察窗口：窗口内退出视为启动失败。
const processStartupWindow = 300 * time.Millisecond

// processStopTimeout 是终止资源子进程后的回收等待上限。
const processStopTimeout = 10 * time.Second

// processGonePollInterval 是等待进程消失的轮询间隔：进程一消失立即返回，不做固定休眠。
const processGonePollInterval = 50 * time.Millisecond

// processInfo 是一条进程观测信息，用于定位进程树与端口的残留占用者。
type processInfo struct {
	PID  int
	PPID int
	PGID int
	Stat string
	Args string
}

// String 返回便于写入失败原因的一行进程描述。
func (info processInfo) String() string {
	return fmt.Sprintf(
		"pid=%d ppid=%d pgid=%d 状态=%s 命令行=%s",
		info.PID,
		info.PPID,
		info.PGID,
		info.Stat,
		info.Args,
	)
}

// describeProcesses 把进程观测信息拼成一行，供失败原因与日志使用。
func describeProcesses(infos []processInfo) string {
	if len(infos) == 0 {
		return "无"
	}
	parts := make([]string, 0, len(infos))
	for _, info := range infos {
		parts = append(parts, info.String())
	}
	return strings.Join(parts, "；")
}

// waitProcessesGone 在限时内等待这些进程全部从进程表中消失。
//
// 采信"已退出"必须有观测依据：轮询到 PID 不再存在才算回收完成，固定休眠或只看启动器
// 的 Wait 返回值都会把仍在运行的子孙进程当成已回收。
func waitProcessesGone(ctx context.Context, members []processInfo, timeout time.Duration) error {
	if len(members) == 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		surviving := make([]processInfo, 0, len(members))
		for _, member := range members {
			alive, err := processAlive(member.PID)
			if err != nil {
				return fmt.Errorf("查询进程 %d 失败: %w", member.PID, err)
			}
			if alive {
				surviving = append(surviving, member)
			}
		}
		if len(surviving) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s 内仍未退出：%s", timeout, describeProcesses(surviving))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"等待进程退出时上下文已结束（未退出：%s）: %w",
				describeProcesses(surviving),
				ctx.Err(),
			)
		case <-time.After(processGonePollInterval):
		}
	}
}

// HoldFlag 是常驻子进程的内部参数，只供 child-process 的默认子进程使用。
const HoldFlag = "--hold"

// managedProcess 是一条由资源管理器托管的子进程。
//
// 进程以独立进程组启动，终止时按进程树回收；Wait 结果通过 done 通道发布，避免在
// 多个资源之间共享状态。logPath 是子进程 stdout/stderr 的输出文件，空表示输出被丢弃。
type managedProcess struct {
	cmd     *exec.Cmd
	name    string
	logPath string
	done    chan struct{}
	waitErr error
}

// processLaunch 是托管子进程的一次启动参数。
type processLaunch struct {
	// name 是资源声明名，只用于错误信息。
	name string
	// executable 是已解析的可执行文件绝对路径。
	executable string
	// args 是命令行参数。
	args []string
	// dir 是子进程的工作目录，空表示沿用编排进程的工作目录。
	dir string
	// logPath 是子进程 stdout/stderr 的输出文件，空表示丢弃输出。
	logPath string
	// env 是追加到继承环境之上的变量，形如 KEY=VALUE；空表示完全继承。
	env []string
}

// startManagedProcess 启动子进程并确认它没有在启动窗口内立即退出。
func startManagedProcess(launch processLaunch) (*managedProcess, error) {
	var logFile *os.File
	if launch.logPath != "" {
		if err := os.MkdirAll(filepath.Dir(launch.logPath), 0o750); err != nil {
			return nil, fmt.Errorf("创建资源 %q 的输出目录失败: %w", launch.name, err)
		}
		file, err := os.Create(launch.logPath)
		if err != nil {
			return nil, fmt.Errorf("创建资源 %q 的输出文件失败: %w", launch.name, err)
		}
		logFile = file
	}
	cmd := exec.Command(launch.executable, launch.args...)
	cmd.SysProcAttr = managedProcessGroupAttr()
	cmd.Dir = launch.dir
	if len(launch.env) > 0 {
		cmd.Env = append(os.Environ(), launch.env...)
	}
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, fmt.Errorf("启动资源 %q 的子进程失败: %w", launch.name, err)
	}
	process := &managedProcess{
		cmd:     cmd,
		name:    launch.name,
		logPath: launch.logPath,
		done:    make(chan struct{}),
	}
	go func() {
		process.waitErr = cmd.Wait()
		if logFile != nil {
			_ = logFile.Close()
		}
		close(process.done)
	}()
	select {
	case <-process.done:
		err := process.waitErr
		if err == nil {
			err = fmt.Errorf("进程在启动后立即以退出码 0 结束")
		}
		if launch.logPath != "" {
			return nil, fmt.Errorf(
				"资源 %q 的子进程未能常驻: %w（输出见 %s）",
				launch.name,
				err,
				launch.logPath,
			)
		}
		return nil, fmt.Errorf("资源 %q 的子进程未能常驻: %w", launch.name, err)
	case <-time.After(processStartupWindow):
	}
	return process, nil
}

// pid 返回子进程的进程号。
func (p *managedProcess) pid() int {
	return p.cmd.Process.Pid
}

// stop 终止整棵进程树并等待回收，可重复调用。
//
// 启动器退出不代表它派生的进程退出：后代可能在新进程组里继续监听端口。因此不因启动器
// 已经结束就跳过回收，终止前采集到的进程必须全部消失才算完成。
func (p *managedProcess) stop(ctx context.Context) error {
	members, err := terminateProcessTree(p.pid())
	if err != nil {
		return err
	}
	select {
	case <-p.done:
	case <-time.After(processStopTimeout):
		return fmt.Errorf("终止资源 %q 的进程树后 %s 内仍未回收", p.name, processStopTimeout)
	case <-ctx.Done():
		return fmt.Errorf("等待资源 %q 的进程退出时上下文已结束: %w", p.name, ctx.Err())
	}
	if err := waitProcessesGone(ctx, members, processStopTimeout); err != nil {
		return fmt.Errorf("资源 %q 的进程树没有完整回收: %w", p.name, err)
	}
	return nil
}

// stopProcessTrees 终止这些进程号各自的进程树，并等待采集到的进程全部退出。
//
// 终止失败与等待超时合并返回：投递信号失败不代表进程已经退出，等待超时也不代表它一定
// 还在运行；两者都保留，避免后一个结论掩盖前一个。
func stopProcessTrees(ctx context.Context, pids []int) error {
	members, killErr := terminateProcessTrees(pids)
	return errors.Join(killErr, waitProcessesGone(ctx, members, processStopTimeout))
}

// ProcessAlive 报告指定进程号是否仍然存在。
//
// 用例侧用它确认被管理进程在停止后真的消失，而不是只相信 Wait 的返回值；查询失败返回错误，
// 调用方必须按"无法确认"处理，不得当作已退出。
func ProcessAlive(pid int) (bool, error) {
	return processAlive(pid)
}

// KillProcessTree 终止指定进程的整棵进程树，可重复调用。
//
// 用例主体启动的前端 CLI（pnpm → node → 浏览器）同样按进程树回收：只杀直接子进程会留下
// 浏览器与 Vitest fork，终检会把它们当成残留；类 Unix 上还会先采集父子链与进程组，
// 把自行更换进程组的后代一并列入终止目标。
func KillProcessTree(pid int) error {
	return killProcessTree(pid)
}

// CommandProcessGroup 返回让外部 CLI 成为独立进程组的启动属性。
//
// 独立进程组是 KillProcessTree 的依据之一：Windows 用 taskkill /T，类 Unix 系统按进程树与 -pid 发信号。
func CommandProcessGroup() *syscall.SysProcAttr {
	return managedProcessGroupAttr()
}

// ChildProcessProvider 启动并托管一个随用例生命周期存活的子进程。
//
// 声明不给出 command 时使用编排进程自身以 “--hold“ 启动的常驻子进程：自检组因此不需要
// 依赖任何外部可执行文件，也不会把真实 Pi / SSH / Docker 调用混进本机用例。
type ChildProcessProvider struct {
	selfExecutable string
	mu             sync.Mutex
	procs          map[int]*childProcessEntry
}

// childProcessEntry 是一条已启动的子进程记录。
type childProcessEntry struct {
	handle  Handle
	process *managedProcess
	logPath string
}

// NewChildProcessProvider 创建一个子进程提供者。
func NewChildProcessProvider(selfExecutable string) *ChildProcessProvider {
	return &ChildProcessProvider{
		selfExecutable: strings.TrimSpace(selfExecutable),
		procs:          make(map[int]*childProcessEntry),
	}
}

// Acquire 启动子进程，句柄的 payload 给出 pid 与实际可执行文件。
func (p *ChildProcessProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	command := specValue(decl, "command")
	args := specArgs(decl, "args")
	if command == "" {
		if p.selfExecutable == "" {
			return Handle{}, fmt.Errorf(
				"资源 %q 没有给出 Spec 键 command，且编排进程没有可用的自身可执行文件",
				decl.Name,
			)
		}
		command = p.selfExecutable
		args = append([]string{HoldFlag}, args...)
	}
	executable, err := resolveExecutable(command)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	logPath, err := processLogPath(ctx, decl)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	process, err := startManagedProcess(processLaunch{
		name:       decl.Name,
		executable: executable,
		args:       args,
		dir:        caseWorkspaceOf(ctx),
		logPath:    logPath,
		env:        caseHomeEnv(ctx),
	})
	if err != nil {
		return Handle{}, err
	}
	handle, err := buildHandle(decl, map[string]string{
		"pid":        strconv.Itoa(process.pid()),
		"executable": executable,
	}, "")
	if err != nil {
		_ = process.stop(context.Background())
		return Handle{}, err
	}
	p.mu.Lock()
	p.procs[process.pid()] = &childProcessEntry{handle: handle, process: process, logPath: logPath}
	p.mu.Unlock()
	return handle, nil
}

// Release 终止子进程的整棵进程树，可重复调用。
func (p *ChildProcessProvider) Release(ctx context.Context, handle Handle) error {
	entry := p.take(handle.PayloadValue("pid"))
	if entry == nil {
		return nil
	}
	return entry.process.stop(ctx)
}

// Verify 自证子进程在释放后不再存活。
func (p *ChildProcessProvider) Verify(ctx context.Context, handle Handle) error {
	raw := handle.PayloadValue("pid")
	if p.holds(raw) {
		return fmt.Errorf("子进程 %s 仍被提供者持有，释放没有生效", raw)
	}
	pid, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("句柄的 pid %q 不是合法进程号: %w", raw, err)
	}
	alive, err := processAlive(pid)
	if err != nil {
		return fmt.Errorf("核验子进程 %d 是否存活失败: %w", pid, err)
	}
	if alive {
		return fmt.Errorf("子进程 %d 在释放后仍然存活", pid)
	}
	return nil
}

// ProcessLogPath 返回该子进程的输出文件路径；记录已释放或缺失时返回空字符串。
func (p *ChildProcessProvider) ProcessLogPath(handle Handle) string {
	pid, err := strconv.Atoi(strings.TrimSpace(handle.PayloadValue("pid")))
	if err != nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.procs[pid]
	if entry == nil {
		return ""
	}
	return entry.logPath
}

// Snapshot 返回仍在运行的子进程。
func (p *ChildProcessProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.procs))
	for pid, entry := range p.procs {
		entries = append(entries, LiveResource{
			Handle: entry.handle,
			Detail: fmt.Sprintf("仍在运行的子进程 %d", pid),
		})
	}
	return entries, nil
}

func (p *ChildProcessProvider) take(rawPID string) *childProcessEntry {
	pid, err := strconv.Atoi(strings.TrimSpace(rawPID))
	if err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.procs[pid]
	delete(p.procs, pid)
	return entry
}

func (p *ChildProcessProvider) holds(rawPID string) bool {
	pid, err := strconv.Atoi(strings.TrimSpace(rawPID))
	if err != nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.procs[pid]
	return ok
}

// ServiceProcessProvider 启动一个被测服务进程并等待其 HTTP 端点就绪。
// 就绪判定只支持 HTTP：提供者先分配一个空闲端口，用 {port} 占位替换后启动进程，
// 再轮询 ready_path 是否返回 200。非 HTTP 服务的就绪判定在 R4 试点中补充，不允许用
// "进程还活着"冒充"服务可用"。
type ServiceProcessProvider struct {
	selfExecutable string
	mu             sync.Mutex
	services       map[int]*serviceProcessEntry
}

// serviceProcessEntry 是一条已启动的服务进程记录。
type serviceProcessEntry struct {
	handle  Handle
	process *managedProcess
	logPath string
}

// NewServiceProcessProvider 创建一个服务进程提供者。
//
// 声明不给 binary 时使用编排进程自身以 --serve 启动的自检服务：它监听提供者分配的空闲端口，
// 因此自检组不需要额外构建被测服务。
func NewServiceProcessProvider(selfExecutable string) *ServiceProcessProvider {
	return &ServiceProcessProvider{
		selfExecutable: strings.TrimSpace(selfExecutable),
		services:       make(map[int]*serviceProcessEntry),
	}
}

// Acquire 启动服务进程并等待 HTTP 端点返回 200，句柄给出 pid、base_url 与 binary。
func (p *ServiceProcessProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	binary, args, err := p.resolveLaunch(decl)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	port, err := freeTCPPort()
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的空闲端口失败: %w", decl.Name, err)
	}
	args = substitutePort(args, port)
	fingerprint, err := fileSHA256(binary)
	if err != nil {
		return Handle{}, err
	}
	readyURL := fmt.Sprintf("http://127.0.0.1:%d%s", port, readyPath(decl))
	readyTimeout := specDuration(decl, "ready_timeout_ms", 30*time.Second)
	logPath, err := processLogPath(ctx, decl)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	process, err := startManagedProcess(processLaunch{
		name:       decl.Name,
		executable: binary,
		args:       args,
		dir:        caseWorkspaceOf(ctx),
		logPath:    logPath,
		env:        caseHomeEnv(ctx),
	})
	if err != nil {
		return Handle{}, err
	}
	if err := waitHTTPReady(ctx, readyURL, readyTimeout); err != nil {
		stopErr := process.stop(context.Background())
		if stopErr != nil {
			return Handle{}, fmt.Errorf("服务 %q 没有就绪且回收失败: %w", decl.Name, err)
		}
		return Handle{}, fmt.Errorf("分配资源 %q 时服务没有就绪: %w", decl.Name, err)
	}
	handle, err := buildHandle(decl, map[string]string{
		"pid":      strconv.Itoa(process.pid()),
		"base_url": readyURL,
		"binary":   binary,
	}, fingerprint)
	if err != nil {
		_ = process.stop(context.Background())
		return Handle{}, err
	}
	p.mu.Lock()
	p.services[process.pid()] = &serviceProcessEntry{handle: handle, process: process, logPath: logPath}
	p.mu.Unlock()
	return handle, nil
}

// resolveLaunch 解析要启动的可执行文件与参数：声明为空时使用自身 --serve 模式。
func (p *ServiceProcessProvider) resolveLaunch(decl ResourceDecl) (string, []string, error) {
	configured := specValue(decl, "binary")
	args := specArgs(decl, "args")
	if configured == "" {
		if p.selfExecutable == "" {
			return "", nil, fmt.Errorf("没有给出 Spec 键 binary，且编排进程没有可用的自身可执行文件")
		}
		executable, err := resolveExecutable(p.selfExecutable)
		if err != nil {
			return "", nil, err
		}
		defaultArgs := []string{"--serve", "127.0.0.1:{port}"}
		return executable, append(defaultArgs, args...), nil
	}
	executable, err := resolveExecutable(configured)
	if err != nil {
		return "", nil, err
	}
	return executable, args, nil
}

// freeTCPPort 分配一个当前空闲的本地端口。
//
// 端口在真正被服务监听前存在竞争窗口；就绪判定会在超时内重试连接，因此窗口期失败表现为
// "服务没有就绪"而不是静默通过。
func freeTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return 0, fmt.Errorf("监听地址 %s 不是 TCP 地址", listener.Addr())
	}
	port := tcpAddr.Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

// substitutePort 把参数中的 {port} 占位替换为实际分配的端口。
func substitutePort(args []string, port int) []string {
	substituted := make([]string, 0, len(args))
	for _, arg := range args {
		substituted = append(substituted, strings.ReplaceAll(arg, "{port}", strconv.Itoa(port)))
	}
	return substituted
}

// readyPath 返回就绪探测路径，声明未给出时使用根路径。
func readyPath(decl ResourceDecl) string {
	path := specValue(decl, "ready_path")
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

// Release 终止服务进程的整棵进程树，可重复调用。
func (p *ServiceProcessProvider) Release(ctx context.Context, handle Handle) error {
	entry := p.take(handle.PayloadValue("pid"))
	if entry == nil {
		return nil
	}
	return entry.process.stop(ctx)
}

// Verify 自证服务进程已退出且端点不再响应。
func (p *ServiceProcessProvider) Verify(ctx context.Context, handle Handle) error {
	raw := handle.PayloadValue("pid")
	if p.holds(raw) {
		return fmt.Errorf("服务进程 %s 仍被提供者持有，释放没有生效", raw)
	}
	pid, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("句柄的 pid %q 不是合法进程号: %w", raw, err)
	}
	alive, err := processAlive(pid)
	if err != nil {
		return fmt.Errorf("核验服务进程 %d 是否存活失败: %w", pid, err)
	}
	if alive {
		return fmt.Errorf("服务进程 %d 在释放后仍然存活", pid)
	}
	return verifyHTTPGone(ctx, handle.PayloadValue("base_url"))
}

// Snapshot 返回仍在运行的服务进程。
func (p *ServiceProcessProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.services))
	for pid, entry := range p.services {
		entries = append(entries, LiveResource{
			Handle: entry.handle,
			Detail: fmt.Sprintf("仍在运行的服务进程 %d（%s）", pid, entry.handle.PayloadValue("base_url")),
		})
	}
	return entries, nil
}

func (p *ServiceProcessProvider) take(rawPID string) *serviceProcessEntry {
	pid, err := strconv.Atoi(strings.TrimSpace(rawPID))
	if err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.services[pid]
	delete(p.services, pid)
	return entry
}

func (p *ServiceProcessProvider) holds(rawPID string) bool {
	pid, err := strconv.Atoi(strings.TrimSpace(rawPID))
	if err != nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.services[pid]
	return ok
}

// ProcessLogPath 返回该服务进程的输出文件路径；记录已释放或缺失时返回空字符串。
func (p *ServiceProcessProvider) ProcessLogPath(handle Handle) string {
	pid, err := strconv.Atoi(strings.TrimSpace(handle.PayloadValue("pid")))
	if err != nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.services[pid]
	if entry == nil {
		return ""
	}
	return entry.logPath
}

// processLogPath 返回托管子进程的输出文件路径：写在用例工作空间的 process 目录下。
//
// 没有用例工作空间时返回空字符串（输出丢弃）：不回退到仓库目录或临时目录根，避免把不受
// 管理的文件留在产物之外。
func processLogPath(ctx context.Context, decl ResourceDecl) (string, error) {
	workspace := caseWorkspaceOf(ctx)
	if workspace == "" {
		return "", nil
	}
	token, err := randomToken(4)
	if err != nil {
		return "", err
	}
	name := safeName(decl.Type) + "-" + safeName(decl.Name) + "-" + token + ".log"
	return filepath.Join(workspace, "process", name), nil
}

// resolveExecutable 解析可执行文件路径：含路径分隔符时必须是已存在的文件，否则查 PATH。
func resolveExecutable(command string) (string, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "", fmt.Errorf("缺少可执行文件路径")
	}
	if strings.ContainsAny(trimmed, `/\\`) {
		absolute, err := filepath.Abs(trimmed)
		if err != nil {
			return "", fmt.Errorf("解析可执行文件路径 %q 失败: %w", trimmed, err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return "", fmt.Errorf("可执行文件 %s 不可用: %w", absolute, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("可执行文件 %s 是目录", absolute)
		}
		return absolute, nil
	}
	path, err := exec.LookPath(trimmed)
	if err != nil {
		return "", fmt.Errorf("在 PATH 中找不到可执行文件 %q: %w", trimmed, err)
	}
	return filepath.Abs(path)
}

// fileSHA256 返回文件内容的 SHA256，用作按内容寻址的指纹。
func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 计算指纹失败: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("计算 %s 的指纹失败: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// specDuration 读取以毫秒为单位的 Spec 时长键。
func specDuration(decl ResourceDecl, key string, fallback time.Duration) time.Duration {
	raw := specValue(decl, key)
	if raw == "" {
		return fallback
	}
	milliseconds, err := strconv.Atoi(raw)
	if err != nil || milliseconds <= 0 {
		return fallback
	}
	return time.Duration(milliseconds) * time.Millisecond
}
