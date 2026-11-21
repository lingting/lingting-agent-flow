package testrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 前端三类资源（frontend-dev-server / browser / node-worker）的提供者实现（V3 R6）。
//
// 三者的分工与 R6 §4.3 一致：dev server 由编排进程启动并在整次运行内共享；browser 与
// node-worker 不自行驱动浏览器或 Vitest，只负责会话目录、实际启动进程的登记与释放后的
// 残留自证——真正的前端 CLI 进程由用例主体按 D2（一文件一次调用）启动。
const (
	// frontendDevServerReadyTimeout 是 Vite dev server 的就绪等待上限：首次运行还要做
	// 依赖预构建，因此比普通服务进程宽松。
	frontendDevServerReadyTimeout = 180 * time.Second
	// frontendProbeTimeout 是版本探测命令的执行上限。
	frontendProbeTimeout = 60 * time.Second
	// frontendSessionPIDFile 是会话目录里记录实际启动进程号的固定文件名。
	frontendSessionPIDFile = "process.pid"
	// frontendPackageManager 是前端项目使用的包管理器可执行文件。
	frontendPackageManager = "pnpm"
	// frontendListenerPayload 是句柄里记录 dev server 监听进程号的 payload 键。
	frontendListenerPayload = "listener_pids"
)

// defaultFrontendRoot 从当前工作目录向上查找前端项目目录，找不到时返回空字符串。
//
// 找不到时提供者在分配阶段如实失败，不回退到其它目录启动 dev server，也不假装资源可用。
func defaultFrontendRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, "lingting-agent-flow-ui")
		if info, statErr := os.Stat(filepath.Join(candidate, "package.json")); statErr == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// frontendRootOf 返回前端项目的绝对路径：优先取显式配置，其次按当前目录向上查找。
func frontendRootOf(configured string) (string, error) {
	root := strings.TrimSpace(configured)
	if root == "" {
		root = defaultFrontendRoot()
	}
	if root == "" {
		return "", fmt.Errorf("找不到前端项目目录（lingting-agent-flow-ui），无法分配前端资源")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("解析前端项目目录 %q 失败: %w", root, err)
	}
	if info, statErr := os.Stat(filepath.Join(absolute, "package.json")); statErr != nil || info.IsDir() {
		return "", fmt.Errorf("前端项目目录 %s 缺少 package.json", absolute)
	}
	return absolute, nil
}

// runFrontendCommand 在前端项目目录执行一条探测命令并返回去掉首尾空白的输出。
func runFrontendCommand(ctx context.Context, frontendRoot string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, frontendProbeTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, frontendPackageManager, args...)
	command.Dir = frontendRoot
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := errors.As(err, &exitErr); ok {
			return "", fmt.Errorf(
				"执行 %s %s 失败: %w（输出 %s）",
				frontendPackageManager,
				strings.Join(args, " "),
				err,
				strings.TrimSpace(string(exitErr.Stderr)),
			)
		}
		return "", fmt.Errorf("执行 %s %s 失败: %w", frontendPackageManager, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

// FrontendDevServerProvider 启动 Vite dev server 并按 http://127.0.0.1:<port> 就绪。
//
// 端口由提供者分配并通过 AGENTFLOW_UI_PORT 注入，配置侧不再使用固定端口：两个并发运行
// 不会抢同一个端口，端口冲突因此表现为分配失败，而不是静默复用别人的 dev server。
type FrontendDevServerProvider struct {
	root         string
	frontendRoot string
	mu           sync.Mutex
	servers      map[int]*frontendDevServerEntry
}

// frontendDevServerEntry 是一条已启动的 dev server 记录。
type frontendDevServerEntry struct {
	handle  Handle
	process *managedProcess
	baseURL string
	logPath string
}

// NewFrontendDevServerProvider 创建一个 dev server 提供者。
func NewFrontendDevServerProvider(root, frontendRoot string) *FrontendDevServerProvider {
	return &FrontendDevServerProvider{
		root:         root,
		frontendRoot: frontendRoot,
		servers:      make(map[int]*frontendDevServerEntry),
	}
}

// Acquire 启动 dev server 并等待 HTTP 就绪，句柄给出 base_url、pid 与监听进程号。
//
// 监听进程号在就绪后立即记录：它是资源回收的关键句柄，端口关闭只由 Verify 的探测断言，
// 不能用"启动器已退出"替代。
func (p *FrontendDevServerProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	frontendRoot, err := frontendRootOf(p.frontendRoot)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	port, err := freeTCPPort()
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的空闲端口失败: %w", decl.Name, err)
	}
	executable, err := resolveExecutable(frontendPackageManager)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	fingerprint, err := fileSHA256(filepath.Join(frontendRoot, "package.json"))
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	logPath := filepath.Join(
		resourceShardDir(p.root, ResourceFrontendDevServer, decl.Name),
		"dev-server.log",
	)
	process, err := startManagedProcess(processLaunch{
		name:       decl.Name,
		executable: executable,
		args:       []string{"exec", "vp", "dev"},
		dir:        frontendRoot,
		logPath:    logPath,
		env: []string{
			"AGENTFLOW_UI_HOST=127.0.0.1",
			fmt.Sprintf("AGENTFLOW_UI_PORT=%d", port),
		},
	})
	if err != nil {
		return Handle{}, err
	}
	if err := waitHTTPReady(ctx, baseURL+"/", frontendDevServerReadyTimeout); err != nil {
		stopErr := stopFrontendDevServer(context.Background(), process, nil)
		if stopErr != nil {
			return Handle{}, fmt.Errorf("dev server %q 没有就绪且回收失败: %w", decl.Name, err)
		}
		return Handle{}, fmt.Errorf("分配资源 %q 时 dev server 没有就绪: %w", decl.Name, err)
	}
	listeners, err := waitPortListeners(ctx, port)
	if err != nil {
		stopErr := stopFrontendDevServer(context.Background(), process, listeners.PIDs)
		if stopErr != nil {
			return Handle{}, fmt.Errorf("dev server %q 的监听进程查询失败且回收失败: %w", decl.Name, err)
		}
		return Handle{}, fmt.Errorf("分配资源 %q 时无法定位 dev server 的监听进程: %w", decl.Name, err)
	}
	if len(listeners.PIDs) == 0 {
		cause := fmt.Errorf(
			"分配资源 %q 时监听发现与就绪探测矛盾: %s 已就绪，但%s",
			decl.Name,
			baseURL,
			describeListenerQuery(listeners),
		)
		if stopErr := stopFrontendDevServer(context.Background(), process, nil); stopErr != nil {
			return Handle{}, fmt.Errorf("%w（回收已启动的进程失败: %v）", cause, stopErr)
		}
		return Handle{}, cause
	}
	handle, err := buildHandle(decl, map[string]string{
		"base_url":              baseURL,
		"pid":                   strconv.Itoa(process.pid()),
		frontendListenerPayload: encodeListenerPIDs(listeners.PIDs),
	}, fingerprint)
	if err != nil {
		_ = stopFrontendDevServer(context.Background(), process, listeners.PIDs)
		return Handle{}, err
	}
	p.mu.Lock()
	p.servers[process.pid()] = &frontendDevServerEntry{
		handle:  handle,
		process: process,
		baseURL: baseURL,
		logPath: logPath,
	}
	p.mu.Unlock()
	return handle, nil
}

// Release 回收 dev server 的启动器进程树与监听进程，并等待端口关闭，可重复调用。
//
// 只回收启动器不够：pnpm / vp dev 可能在就绪后先退出，实际监听由已经脱离父子链的进程持有。
// 监听进程号在 Acquire 时记录在句柄里，这里必须一起回收，并观测到端口真正关闭。
func (p *FrontendDevServerProvider) Release(ctx context.Context, handle Handle) error {
	entry := p.take(handle.PayloadValue("pid"))
	if entry == nil {
		return nil
	}
	listeners, decodeErr := decodeListenerPIDs(handle.PayloadValue(frontendListenerPayload))
	stopErr := stopFrontendDevServer(ctx, entry.process, listeners)
	if decodeErr != nil {
		return errors.Join(decodeErr, stopErr)
	}
	port, portErr := listenerPortOf(handle.PayloadValue("base_url"))
	if portErr != nil {
		return errors.Join(portErr, stopErr)
	}
	if closeErr := waitPortClosed(ctx, port, processStopTimeout); closeErr != nil {
		return errors.Join(stopErr, describePortOccupied(ctx, closeErr, fmt.Sprintf("127.0.0.1:%d", port)))
	}
	return stopErr
}

// stopFrontendDevServer 回收 dev server 的启动器进程树与记录的监听进程。
//
// 两个错误都保留：启动器回收失败与监听进程回收失败是两件独立的事，合并返回后由命令执行
// 入口统一输出一次。
func stopFrontendDevServer(ctx context.Context, process *managedProcess, listenerPIDs []int) error {
	processErr := process.stop(ctx)
	return errors.Join(processErr, stopProcessTrees(ctx, listenerPIDs))
}

// Verify 自证 dev server 已停止：启动器与监听进程消失、端口关闭、HTTP 不再可用。
//
// 启动器退出不能代替端口关闭：端口是资源的最终事实，两者必须分别核验。
func (p *FrontendDevServerProvider) Verify(ctx context.Context, handle Handle) error {
	raw := handle.PayloadValue("pid")
	if p.holds(raw) {
		return fmt.Errorf("dev server 进程 %s 仍被提供者持有，释放没有生效", raw)
	}
	pid, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("句柄的 pid %q 不是合法进程号: %w", raw, err)
	}
	alive, err := processAlive(pid)
	if err != nil {
		return fmt.Errorf("核验 dev server 进程 %d 是否存活失败: %w", pid, err)
	}
	if alive {
		return fmt.Errorf("dev server 进程 %d 在释放后仍然存活", pid)
	}
	listeners, err := decodeListenerPIDs(handle.PayloadValue(frontendListenerPayload))
	if err != nil {
		return fmt.Errorf("核验 dev server 的监听进程失败: %w", err)
	}
	for _, listener := range listeners {
		alive, aliveErr := processAlive(listener)
		if aliveErr != nil {
			return fmt.Errorf("核验 dev server 监听进程 %d 是否存活失败: %w", listener, aliveErr)
		}
		if alive {
			return fmt.Errorf("dev server 监听进程 %d 在释放后仍然存活", listener)
		}
	}
	baseURL := handle.PayloadValue("base_url")
	if baseURL == "" {
		return fmt.Errorf("句柄缺少 base_url，无法核验 dev server 端口")
	}
	address := strings.TrimPrefix(strings.TrimPrefix(baseURL, "http://"), "https://")
	if err := verifyPortClosed(ctx, address); err != nil {
		return describePortOccupied(ctx, err, address)
	}
	if err := verifyHTTPGone(ctx, baseURL); err != nil {
		return err
	}
	return nil
}

// ProcessLogPath 返回 dev server 的输出文件路径。
func (p *FrontendDevServerProvider) ProcessLogPath(handle Handle) string {
	entry := p.peek(handle.PayloadValue("pid"))
	if entry == nil {
		return ""
	}
	return entry.logPath
}

// Snapshot 返回仍在运行的 dev server。
func (p *FrontendDevServerProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.servers))
	for pid, entry := range p.servers {
		entries = append(entries, LiveResource{
			Handle: entry.handle,
			Detail: fmt.Sprintf("仍在运行的 dev server 进程 %d（%s）", pid, entry.baseURL),
		})
	}
	return entries, nil
}

func (p *FrontendDevServerProvider) take(rawPID string) *frontendDevServerEntry {
	pid, err := strconv.Atoi(strings.TrimSpace(rawPID))
	if err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.servers[pid]
	delete(p.servers, pid)
	return entry
}

func (p *FrontendDevServerProvider) holds(rawPID string) bool {
	return p.peek(rawPID) != nil
}

func (p *FrontendDevServerProvider) peek(rawPID string) *frontendDevServerEntry {
	pid, err := strconv.Atoi(strings.TrimSpace(rawPID))
	if err != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.servers[pid]
}

// FrontendSessionProvider 管理前端 CLI 的会话目录与进程登记。
//
// 它不启动浏览器或 Vitest：真实进程由用例主体启动后立即用 RecordFrontendProcess 登记，
// 提供者负责在释放后核验登记的进程已经消失，并在会话目录里保留该用例的产物。
type FrontendSessionProvider struct {
	resourceType string
	root         string
	frontendRoot string

	mu       sync.Mutex
	sessions map[string]*frontendSession

	versionOnce sync.Once
	version     string
	versionErr  error
}

// frontendSession 是一条已分配的会话记录。
type frontendSession struct {
	handle Handle
	dir    string
}

// NewBrowserProvider 创建 Playwright 浏览器会话提供者。
func NewBrowserProvider(root, frontendRoot string) *FrontendSessionProvider {
	return &FrontendSessionProvider{
		resourceType: ResourceBrowser,
		root:         root,
		frontendRoot: frontendRoot,
		sessions:     make(map[string]*frontendSession),
	}
}

// NewNodeWorkerProvider 创建 Vitest 的 node worker 会话提供者。
func NewNodeWorkerProvider(root, frontendRoot string) *FrontendSessionProvider {
	return &FrontendSessionProvider{
		resourceType: ResourceNodeWorker,
		root:         root,
		frontendRoot: frontendRoot,
		sessions:     make(map[string]*frontendSession),
	}
}

// Acquire 创建一个会话目录并写下运行期标识，返回带会话目录的句柄。
func (p *FrontendSessionProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	token, err := randomToken(4)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	dir := filepath.Join(resourceShardDir(p.root, p.resourceType, decl.Name), token)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Handle{}, fmt.Errorf("创建资源 %q 的会话目录失败: %w", decl.Name, err)
	}
	version, err := p.runtimeVersion(ctx)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	payload := map[string]string{"session_dir": dir, "version": version}
	switch p.resourceType {
	case ResourceBrowser:
		payload["browser"] = "chromium"
	case ResourceNodeWorker:
		payload["runtime"] = "node"
	}
	handle, err := buildHandle(decl, payload, version)
	if err != nil {
		return Handle{}, err
	}
	p.mu.Lock()
	p.sessions[dir] = &frontendSession{handle: handle, dir: dir}
	p.mu.Unlock()
	return handle, nil
}

// runtimeVersion 探测并缓存运行时版本；探测失败时不下发空版本。
func (p *FrontendSessionProvider) runtimeVersion(ctx context.Context) (string, error) {
	p.versionOnce.Do(func() {
		frontendRoot, err := frontendRootOf(p.frontendRoot)
		if err != nil {
			p.versionErr = err
			return
		}
		switch p.resourceType {
		case ResourceBrowser:
			output, err := runFrontendCommand(ctx, frontendRoot, "exec", "playwright", "--version")
			if err != nil {
				p.versionErr = err
				return
			}
			p.version = strings.TrimSpace(strings.TrimPrefix(output, "Version"))
		default:
			output, err := runFrontendCommand(ctx, frontendRoot, "exec", "node", "--version")
			if err != nil {
				p.versionErr = err
				return
			}
			p.version = output
		}
	})
	if p.versionErr != nil {
		return "", p.versionErr
	}
	if strings.TrimSpace(p.version) == "" {
		return "", fmt.Errorf("资源类型 %q 的运行时版本为空", p.resourceType)
	}
	return p.version, nil
}

// Release 结束会话登记，并在会话目录里保留已生成的产物。
//
// 登记的进程如果仍在运行，先按进程树终止：它意味着用例没有正常收尾，不能留给终检发现。
func (p *FrontendSessionProvider) Release(ctx context.Context, handle Handle) error {
	dir := handle.PayloadValue("session_dir")
	p.mu.Lock()
	_, held := p.sessions[dir]
	delete(p.sessions, dir)
	p.mu.Unlock()
	if !held {
		return nil
	}
	pid, err := recordedFrontendPID(dir)
	if err != nil {
		return err
	}
	if pid <= 0 {
		return nil
	}
	alive, err := processAlive(pid)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	return killProcessTree(pid)
}

// Verify 自证登记的进程在释放后已经消失。
//
// 没有登记进程说明该用例没有启动前端 CLI：这属于如实记录的空会话，不当作残留；登记了
// 进程却仍存活则判为残留，由运行级终检收敛。
func (p *FrontendSessionProvider) Verify(ctx context.Context, handle Handle) error {
	dir := handle.PayloadValue("session_dir")
	if dir == "" {
		return fmt.Errorf("句柄缺少 session_dir，无法核验前端会话")
	}
	p.mu.Lock()
	_, held := p.sessions[dir]
	p.mu.Unlock()
	if held {
		return fmt.Errorf("前端会话 %s 仍被提供者持有，释放没有生效", dir)
	}
	pid, err := recordedFrontendPID(dir)
	if err != nil {
		return err
	}
	if pid <= 0 {
		return nil
	}
	alive, err := processAlive(pid)
	if err != nil {
		return fmt.Errorf("核验会话进程 %d 是否存活失败: %w", pid, err)
	}
	if alive {
		return fmt.Errorf("会话目录 %s 登记的进程 %d 在释放后仍然存活", dir, pid)
	}
	return nil
}

// Snapshot 返回仍被持有、或登记进程仍在运行的前端会话。
func (p *FrontendSessionProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	held := make([]*frontendSession, 0, len(p.sessions))
	for _, session := range p.sessions {
		held = append(held, session)
	}
	p.mu.Unlock()
	entries := make([]LiveResource, 0, len(held))
	for _, session := range held {
		pid, err := recordedFrontendPID(session.dir)
		if err != nil {
			return nil, err
		}
		detail := fmt.Sprintf("仍在持有的前端会话 %s", session.dir)
		if pid > 0 {
			alive, aliveErr := processAlive(pid)
			if aliveErr != nil {
				return nil, aliveErr
			}
			if alive {
				detail = fmt.Sprintf("前端会话 %s 的进程 %d 仍在运行", session.dir, pid)
			}
		}
		entries = append(entries, LiveResource{Handle: session.handle, Detail: detail})
	}
	return entries, nil
}

// RecordFrontendProcess 把用例实际启动的前端 CLI 进程登记到会话目录。
//
// 用例必须在启动进程后立即登记：提供者与运行级终检据此确认释放后没有残留进程。
func RecordFrontendProcess(handle Handle, pid int) error {
	dir := handle.PayloadValue("session_dir")
	if dir == "" {
		return fmt.Errorf("句柄 %q 缺少 session_dir，无法登记前端进程", handle.Name)
	}
	if pid <= 0 {
		return fmt.Errorf("登记前端进程失败: 进程号 %d 不合法", pid)
	}
	path := filepath.Join(dir, frontendSessionPIDFile)
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return fmt.Errorf("登记前端进程 %d 失败: %w", pid, err)
	}
	return nil
}

// recordedFrontendPID 读取会话目录里登记的进程号，未登记时返回 0。
func recordedFrontendPID(dir string) (int, error) {
	content, err := os.ReadFile(filepath.Join(dir, frontendSessionPIDFile))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("读取会话目录 %s 的进程登记失败: %w", dir, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		return 0, fmt.Errorf("会话目录 %s 的进程登记不是合法进程号: %w", dir, err)
	}
	return pid, nil
}
