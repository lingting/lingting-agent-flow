package planning_remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 真实 Server 子进程验收使用的时间参数。
const (
	// serverStartupTimeout 是子进程从启动到健康检查通过的最长等待时间。
	serverStartupTimeout = 2 * time.Minute
	// serverExitTimeout 是强制终止后等待子进程被回收的最长时间。
	serverExitTimeout = 2 * time.Minute
	// serverLocalRequestTimeout 是只读诊断接口的超时。
	serverLocalRequestTimeout = time.Minute
	// serverRemoteRequestTimeout 是触发远程核验的接口超时。
	serverRemoteRequestTimeout = 15 * time.Minute
)

// serverProcess 是一个真实 Server 子进程。
//
// 它只负责"用正式启动入口起进程、等健康检查、强制终止、观察退出"：进程内的服务装配、
// 数据库初始化与生命周期注册全部由被测程序自己完成，测试侧不构造任何应用服务。
type serverProcess struct {
	f *fixture
	// binaryPath 是通过 `go build` 从 cmd/agentflow 构建的可执行文件。
	binaryPath string
	// configPath 是本次运行专属的配置文件，数据库与 Session 根目录都落在测试工作空间内。
	configPath string
	// address 是只监听回环地址的 127.0.0.1:<随机端口>。
	address string
	// databasePath 是本用例使用的文件型 SQLite 路径。
	databasePath string
	// sessionRoot 是由数据库路径派生的 Session 根目录。
	sessionRoot string

	// cmd 是当前子进程，未运行时为 nil。
	cmd *exec.Cmd
	// exited 在子进程被回收后收到等待结果。
	exited chan error
	// logFile 是子进程标准输出与标准错误的目标文件。
	logFile *os.File
	// logPath 是同一个文件的路径，用于事后复核。
	logPath string
	// runs 记录每次启动与终止的可复核事实。
	runs []serverRun
}

// serverRun 是一次 Server 子进程的启动与终止记录。
type serverRun struct {
	// PID 是子进程标识。
	PID int
	// StartedAt 是启动时间。
	StartedAt time.Time
	// TerminatedAt 是终止时间，未终止时为零值。
	TerminatedAt time.Time
	// Method 是终止方式，未终止时为空。
	Method string
	// ExitStatus 是操作系统报告的退出状态，未终止时为空。
	ExitStatus string
	// LogPath 是该次运行的标准输出与标准错误文件。
	LogPath string
}

// newServerProcess 构建正式 Server 可执行文件并生成测试配置。
func newServerProcess(t *testing.T, f *fixture) *serverProcess {
	t.Helper()
	return &serverProcess{
		f:            f,
		binaryPath:   buildServerBinary(t, f.workspace.Root()),
		configPath:   filepath.Join(f.workspace.Root(), "server-process.yaml"),
		address:      freeLoopbackAddress(t),
		databasePath: f.dbPath,
		sessionRoot:  f.sessionRoot,
	}
}

// start 启动一个新的 Server 子进程并等待健康检查通过。
//
// 启动入口是正式命令 `agentflow server --config <测试配置>`：进程内不注入任何测试
// 替身，因此这里验证的是产品自身的启动装配。
func (p *serverProcess) start(t *testing.T) serverRun {
	t.Helper()
	p.writeConfig(t)
	p.logPath = filepath.Join(
		p.f.workspace.Root(),
		fmt.Sprintf("server-process-%d.log", len(p.runs)+1),
	)
	logFile, err := os.Create(p.logPath)
	if err != nil {
		t.Fatalf("创建 Server 子进程日志失败: %v", err)
	}
	command := exec.Command(
		p.binaryPath,
		"server",
		"--config", p.configPath,
		// 本机可能没有前端构建产物：显式关闭该要求，只验收 API 与后台服务。
		"--require-web-ui=false",
	)
	// 工作目录固定为测试工作空间：任何相对路径产物都不会落进仓库。
	command.Dir = p.f.workspace.Root()
	command.Stdout = logFile
	command.Stderr = logFile
	if startErr := command.Start(); startErr != nil {
		_ = logFile.Close()
		t.Fatalf("启动 Server 子进程失败: %v", startErr)
	}
	p.cmd = command
	p.logFile = logFile
	exited := make(chan error, 1)
	p.exited = exited
	go func() { exited <- command.Wait() }()
	run := serverRun{PID: command.Process.Pid, StartedAt: time.Now().UTC(), LogPath: p.logPath}
	t.Logf("SERVER_PROCESS_STARTED pid=%d address=%s config=%s log=%s",
		run.PID, p.address, p.configPath, p.logPath)
	// 用例中途失败时也必须回收子进程：否则它会继续独占数据库文件锁。
	t.Cleanup(func() { p.killQuietly(t) })
	p.waitHealthy(t)
	p.runs = append(p.runs, run)
	return run
}

// killQuietly 在清理阶段回收仍然存活的子进程，不改变用例结论之外的判定。
func (p *serverProcess) killQuietly(t *testing.T) {
	t.Helper()
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	pid := p.cmd.Process.Pid
	if err := p.cmd.Process.Kill(); err != nil {
		t.Logf("WARN 清理阶段终止子进程 %d 失败: %v", pid, err)
	}
	select {
	case err := <-p.exited:
		t.Logf("SERVER_PROCESS_CLEANUP_KILL pid=%d exit=%s", pid, exitStatusText(err))
	case <-time.After(serverExitTimeout):
		t.Errorf("清理阶段等待子进程 %d 退出超时", pid)
	}
	p.closeLog(t)
	p.cmd = nil
	p.exited = nil
}

// kill 强制终止当前 Server 子进程并等待其退出。
//
// 终止方式由操作系统决定：Unix 上是 SIGKILL，Windows 上是 TerminateProcess。两者都不
// 执行任何关闭钩子，因此不会把"优雅关闭成功"当成崩溃恢复的证据。
func (p *serverProcess) kill(t *testing.T) serverRun {
	t.Helper()
	if p.cmd == nil || p.cmd.Process == nil {
		t.Fatalf("没有正在运行的 Server 子进程可终止")
	}
	pid := p.cmd.Process.Pid
	method := forcedTerminationMethod()
	killErr := p.cmd.Process.Kill()
	waitErr := p.awaitExit(t)
	p.closeLog(t)
	p.cmd = nil
	p.exited = nil
	if killErr != nil {
		t.Fatalf("强制终止 Server 子进程 %d 失败: %v", pid, killErr)
	}
	run := serverRun{
		PID:          pid,
		Method:       method,
		ExitStatus:   exitStatusText(waitErr),
		TerminatedAt: time.Now().UTC(),
		LogPath:      p.logPath,
	}
	if len(p.runs) > 0 {
		index := len(p.runs) - 1
		p.runs[index].TerminatedAt = run.TerminatedAt
		p.runs[index].Method = run.Method
		p.runs[index].ExitStatus = run.ExitStatus
	}
	t.Logf("SERVER_PROCESS_TERMINATED pid=%d method=%s exit=%s",
		run.PID, run.Method, run.ExitStatus)
	return run
}

// awaitExit 等待子进程结束并返回等待错误。
func (p *serverProcess) awaitExit(t *testing.T) error {
	t.Helper()
	select {
	case err := <-p.exited:
		return err
	case <-time.After(serverExitTimeout):
		t.Fatalf("等待子进程 %d 退出超时", p.cmd.Process.Pid)
		return nil
	}
}

// closeLog 关闭子进程日志文件。
func (p *serverProcess) closeLog(t *testing.T) {
	t.Helper()
	if p.logFile == nil {
		return
	}
	if err := p.logFile.Close(); err != nil {
		t.Errorf("关闭 Server 子进程日志失败: %v", err)
	}
	p.logFile = nil
}

// exitStatusText 把等待错误整理成可复核的退出状态文本。
func exitStatusText(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// isExitError 判断等待错误是否只表示"进程已经结束"。
func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

// forcedTerminationMethod 返回本平台的强制终止方式名称。
func forcedTerminationMethod() string {
	if runtime.GOOS == "windows" {
		return "Windows TerminateProcess（等价于强制终止，不执行关闭钩子）"
	}
	return "SIGKILL"
}

// writeConfig 写出本次子进程使用的正式配置文件。
func (p *serverProcess) writeConfig(t *testing.T) {
	t.Helper()
	content := strings.Join([]string{
		"app:",
		"  name: agentflow",
		"  log_level: info",
		"server:",
		"  host: 127.0.0.1",
		fmt.Sprintf("  port: %d", portOfAddress(t, p.address)),
		"  shutdown_timeout_seconds: 30",
		"storage:",
		"  database_path: " + filepath.ToSlash(p.databasePath),
		"workspace:",
		"  worktree_root: " + filepath.ToSlash(filepath.Join(p.f.workspace.Root(), "worktrees")),
		"scheduler:",
		"  max_concurrent_tasks: 1",
		"  max_retries_per_step: 0",
		"ssh:",
		"  known_hosts_path: " + filepath.ToSlash(p.knownHostsPath(t)),
		"  connect_timeout_seconds: 30",
		"",
	}, "\n")
	if err := os.WriteFile(p.configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 Server 子进程配置失败: %v", err)
	}
	t.Logf("SERVER_PROCESS_CONFIG path=%s database=%s session_root=%s",
		p.configPath, p.databasePath, p.sessionRoot)
}

// knownHostsPath 把测试侧 known_hosts 快照合并成一个子进程可读的文件。
//
// 产品只接受一个只读 known_hosts 路径；测试侧来源可能是多个快照文件，这里在测试工作
// 空间内合并，不修改用户真实文件，也不关闭主机密钥校验。
func (p *serverProcess) knownHostsPath(t *testing.T) string {
	t.Helper()
	target := filepath.Join(p.f.workspace.Root(), "known_hosts")
	var merged []byte
	for _, source := range testkit.KnownHostsSources(t) {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("读取 known_hosts 快照失败: %s: %v", source, err)
		}
		merged = append(merged, content...)
		if len(merged) > 0 && merged[len(merged)-1] != '\n' {
			merged = append(merged, '\n')
		}
	}
	if len(merged) == 0 {
		t.Fatalf("没有可用的 known_hosts 来源，无法在开启主机密钥校验的前提下启动服务")
	}
	if err := os.WriteFile(target, merged, 0o600); err != nil {
		t.Fatalf("写入合并后的 known_hosts 失败: %v", err)
	}
	return target
}

// waitHealthy 轮询健康检查直到通过；子进程提前退出或超时即失败。
func (p *serverProcess) waitHealthy(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(serverStartupTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-p.exited:
			p.exited <- err
			t.Fatalf("Server 子进程在健康检查通过之前退出: %v\n输出:\n%s", err, p.readLog())
		default:
		}
		status, body, err := p.requestRaw(
			http.MethodGet,
			httpapi.PathHealth,
			nil,
			serverLocalRequestTimeout,
		)
		if err == nil && status == http.StatusOK {
			if syncErr := p.logFile.Sync(); syncErr != nil {
				t.Logf("WARN 刷新 Server 子进程日志失败: %v", syncErr)
			}
			t.Logf("SERVER_PROCESS_HEALTHY pid=%d address=%s body=%s",
				p.cmd.Process.Pid, p.address, body)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	if syncErr := p.logFile.Sync(); syncErr != nil {
		t.Logf("WARN 刷新 Server 子进程日志失败: %v", syncErr)
	}
	t.Fatalf("Server 子进程在 %s 内未通过健康检查，日志:\n%s", serverStartupTimeout, p.readLog())
}

// readLog 读取子进程标准输出与标准错误。
func (p *serverProcess) readLog() string {
	if p.logPath == "" {
		return ""
	}
	content, err := os.ReadFile(p.logPath)
	if err != nil {
		return "读取日志失败: " + err.Error()
	}
	return string(content)
}

// requestRaw 发起一次 HTTP 请求并返回状态码与响应体。
func (p *serverProcess) requestRaw(
	method, path string,
	body any,
	timeout time.Duration,
) (int, string, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, "", fmt.Errorf("序列化请求体失败: %w", err)
		}
		reader = strings.NewReader(string(payload))
	}
	request, err := http.NewRequest(method, "http://"+p.address+path, reader)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	content, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		return response.StatusCode, "", readErr
	}
	if closeErr != nil {
		return response.StatusCode, string(content), closeErr
	}
	return response.StatusCode, string(content), nil
}

// request 发起一次必须成功的请求并把响应体解码到 out。
func (p *serverProcess) request(
	t *testing.T,
	method, path string,
	body any,
	timeout time.Duration,
	out any,
) {
	t.Helper()
	status, content, err := p.requestRaw(method, path, body, timeout)
	if err != nil {
		t.Fatalf("%s %s 请求失败: %v", method, path, err)
	}
	if status != http.StatusOK {
		t.Fatalf("%s %s 应返回 200，实际 %d，响应体 %s", method, path, status, content)
	}
	if out == nil {
		return
	}
	if decodeErr := json.Unmarshal([]byte(content), out); decodeErr != nil {
		t.Fatalf("解析 %s %s 响应失败: %v，响应体 %s", method, path, decodeErr, content)
	}
}

// failure 发起一次期望失败的请求，返回状态码、错误代码与错误说明。
func (p *serverProcess) failure(
	t *testing.T,
	method, path string,
	body any,
	timeout time.Duration,
) (int, string, string) {
	t.Helper()
	status, content, err := p.requestRaw(method, path, body, timeout)
	if err != nil {
		t.Fatalf("%s %s 请求失败: %v", method, path, err)
	}
	if status == http.StatusOK {
		t.Fatalf("%s %s 期望失败，实际返回 200，响应体 %s", method, path, content)
	}
	var payload httpapi.ErrorResponse
	if decodeErr := json.Unmarshal([]byte(content), &payload); decodeErr != nil {
		t.Fatalf("解析 %s %s 错误响应失败: %v，响应体 %s", method, path, decodeErr, content)
	}
	return status, payload.Error.Code, payload.Error.Message
}

// sessionPath 返回规划会话诊断路径。
func sessionPath(sessionID string) string {
	return httpapi.PathPlanningSessions + "/" + sessionID
}

// sessionDetail 读取规划会话。
func (p *serverProcess) sessionDetail(
	t *testing.T,
	sessionID string,
) httpapi.PlanningSessionDetailResponse {
	t.Helper()
	var payload httpapi.PlanningSessionDetailResponse
	p.request(t, http.MethodGet, sessionPath(sessionID), nil, serverLocalRequestTimeout, &payload)
	return payload
}

// inspectRuntime 核验规划运行时现场。
func (p *serverProcess) inspectRuntime(
	t *testing.T,
	sessionID string,
) httpapi.PlanningRuntimeInspectionResponse {
	t.Helper()
	var payload httpapi.PlanningRuntimeInspectionResponse
	p.request(t, http.MethodGet, sessionPath(sessionID)+"/runtime", nil,
		serverRemoteRequestTimeout, &payload)
	return payload
}

// prepareRuntime 准备或复用规划运行环境。
func (p *serverProcess) prepareRuntime(
	t *testing.T,
	sessionID string,
) httpapi.PlanningRuntimeDetailResponse {
	t.Helper()
	var payload httpapi.PlanningRuntimeDetailResponse
	p.request(t, http.MethodPost, sessionPath(sessionID)+"/runtime/prepare", nil,
		serverRemoteRequestTimeout, &payload)
	return payload
}

// releaseRuntime 释放规划运行环境。
func (p *serverProcess) releaseRuntime(
	t *testing.T,
	sessionID string,
) httpapi.PlanningRuntimeReleaseResponse {
	t.Helper()
	var payload httpapi.PlanningRuntimeReleaseResponse
	p.request(t, http.MethodPost, sessionPath(sessionID)+"/runtime/release", nil,
		serverRemoteRequestTimeout, &payload)
	return payload
}

// runtimeConfig 读取服务启动阶段生成的运行配置快照。
func (p *serverProcess) runtimeConfig(t *testing.T) app.RuntimeConfig {
	t.Helper()
	var payload app.RuntimeConfig
	p.request(t, http.MethodGet, httpapi.PathSettingsConfig, nil, serverLocalRequestTimeout, &payload)
	return payload
}

// buildServerBinary 用正式入口构建 Server 可执行文件。
func buildServerBinary(t *testing.T, workspaceRoot string) string {
	t.Helper()
	name := "agentflow"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(workspaceRoot, "bin", name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("创建 Server 二进制目录失败: %v", err)
	}
	command := exec.Command("go", "build", "-o", target, "./cmd/agentflow")
	command.Dir = repositoryRoot(t)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("构建 Server 二进制失败: %v\n%s", err, string(output))
	}
	t.Logf("SERVER_BINARY=%s", target)
	return target
}

// repositoryRoot 从当前包目录向上定位仓库根目录。
func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("解析包目录失败: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("未能从 %s 向上定位仓库根目录", dir)
		}
		dir = parent
	}
}

// freeLoopbackAddress 申请一个空闲的回环端口并立即释放。
func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请回环端口失败: %v", err)
	}
	address := listener.Addr().String()
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("释放回环端口失败: %v", closeErr)
	}
	return address
}

// portOfAddress 返回监听地址中的端口。
func portOfAddress(t *testing.T, address string) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("解析监听地址 %s 失败: %v", address, err)
	}
	port := 0
	if _, scanErr := fmt.Sscanf(portText, "%d", &port); scanErr != nil || port <= 0 {
		t.Fatalf("监听地址 %s 的端口非法: %q", address, portText)
	}
	return port
}

// fileDigest 返回文件内容的 SHA-256 摘要。
func fileDigest(t *testing.T, target string) string {
	t.Helper()
	content := mustReadFile(t, target)
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// containerExists 通过独立控制连接判断容器是否还存在。
func (f *fixture) containerExists(t *testing.T, dockerID string) bool {
	t.Helper()
	_, err := f.runner.InspectContainer(f.ctx, dockerID)
	if err == nil {
		return true
	}
	if docker.ErrorCodeOf(err) == docker.CodeContainerNotFound {
		return false
	}
	t.Fatalf("无法确认容器 %s 是否存在: %v", dockerID, err)
	return false
}

// stopContainerDirectly 通过独立控制连接停止容器，制造"容器已退出"的现场。
//
// 它不经过生产服务：目的是构造现场，随后由被测 Server 进程按既有策略恢复同一个容器。
func (f *fixture) stopContainerDirectly(t *testing.T, dockerID string) {
	t.Helper()
	run, err := f.runner.Run(f.ctx, "docker stop",
		"docker stop "+shellQuote(dockerID), planningOpTimeout)
	if err != nil {
		t.Fatalf("停止规划容器失败: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("停止规划容器退出码 %d: %s", run.ExitCode, strings.TrimSpace(run.Stderr))
	}
	inspect := f.controlInspectCondition(t, dockerID)
	if inspect.IsActivelyRunning() {
		t.Fatalf("停止后容器仍处于运行状态: %s", inspect.State)
	}
	t.Logf("CONTAINER_STOPPED docker_id=%s state=%s", dockerID, inspect.State)
}

// removeContainerDirectly 通过独立控制连接强制删除容器，制造"容器已不存在"的现场。
func (f *fixture) removeContainerDirectly(t *testing.T, dockerID string) {
	t.Helper()
	run, err := f.runner.Run(f.ctx, "docker rm",
		"docker rm -f "+shellQuote(dockerID), planningOpTimeout)
	if err != nil {
		t.Fatalf("删除规划容器失败: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("删除规划容器退出码 %d: %s", run.ExitCode, strings.TrimSpace(run.Stderr))
	}
	if f.containerExists(t, dockerID) {
		t.Fatalf("删除后容器 %s 仍然存在", dockerID)
	}
	t.Logf("CONTAINER_REMOVED docker_id=%s", dockerID)
}

// removeVolumeDirectly 通过独立控制连接删除快照卷，制造"快照卷已不存在"的现场。
func (f *fixture) removeVolumeDirectly(t *testing.T, volumeName string) {
	t.Helper()
	run, err := f.runner.Run(f.ctx, "docker volume rm",
		"docker volume rm "+shellQuote(volumeName), planningOpTimeout)
	if err != nil {
		t.Fatalf("删除快照卷失败: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("删除快照卷退出码 %d: %s", run.ExitCode, strings.TrimSpace(run.Stderr))
	}
	if err := verifyVolumeAbsent(f.ctx, f.runner, volumeName); err != nil {
		t.Fatalf("删除后快照卷核验失败: %v", err)
	}
	t.Logf("VOLUME_REMOVED volume=%s", volumeName)
}

// countPlanningContainers 返回远端带本规划会话标签的容器数量。
func (f *fixture) countPlanningContainers(t *testing.T, sessionID domain.ID) int {
	t.Helper()
	run, err := f.runner.Run(f.ctx, "docker ps",
		"docker ps -a --filter "+shellQuote("label=agentflow.planning_session_id="+sessionID.String())+
			" --format "+shellQuote("{{.ID}}"),
		planningOpTimeout)
	if err != nil {
		t.Fatalf("列取本会话规划容器失败: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("列取本会话规划容器退出码 %d: %s", run.ExitCode, strings.TrimSpace(run.Stderr))
	}
	count := 0
	for _, line := range strings.Split(run.Stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// assertServerUnreachable 验收被终止的子进程不再提供服务。
//
// 它是"进程确实退出"的操作系统层证据：健康检查必须在有限时间内变成连接失败，而不是
// 仍然由某个存活进程应答。
func assertServerUnreachable(t *testing.T, p *serverProcess) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		status, body, err := p.requestRaw(http.MethodGet, httpapi.PathHealth, nil, 5*time.Second)
		if err != nil {
			t.Logf("SERVER_PROCESS_UNREACHABLE address=%s error=%v", p.address, err)
			return
		}
		t.Logf("WARN 终止后仍收到健康检查响应: status=%d body=%s", status, body)
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("终止后的 Server 进程仍然在 %s 上应答，无法证明进程已经退出", p.address)
}
