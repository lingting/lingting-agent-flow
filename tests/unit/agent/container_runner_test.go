// Package agent_unit 提供容器内 Pi Runner 的单元测试。
//
// 这里使用可替换的容器执行器与容器核验器替身：命令构造、Session 路径唯一性、
// 标准输入写入、退出状态分类、Session 提取与 HTML 导出失败处理都经过真实代码
// 路径，但不需要真实容器、SSH 连接或模型调用。
package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 测试使用的固定值。
const (
	testContainerID = "container-1"
	testDockerID    = "docker-container-1"
	testModel       = "fake/model-1"
	// testSessionJSONL 是一份结构完整的最小 Session 内容。
	testSessionJSONL = `{"type":"session","version":3,"id":"fake-session","cwd":"/workspace/project"}
{"type":"message","id":"a1","parentId":null,"timestamp":"2025-01-02T03:04:06.000Z","message":{"role":"user","content":"只回答 READY","timestamp":1735787046000}}
{"type":"message","id":"b2","parentId":"a1","timestamp":"2025-01-02T03:04:07.000Z","message":{"role":"assistant","content":[{"type":"text","text":"READY"}],"provider":"fake","model":"model-1","stopReason":"stop","timestamp":1735787047000}}
`
	// partialSessionJSONL 是内容残缺的 Session 文件：头部合法但后续行不是合法 JSON。
	partialSessionJSONL = `{"type":"session","version":3,"id":"fake-session","cwd":"/workspace/project"}
not-json
`
)

// fakeExecutor 记录容器内命令并按预设规则返回结果。
//
// 它同时模拟容器与远程宿主机文件系统：容器内 Session 文件在写入 containerFiles
// 后由 CopyFromContainer 复制到宿主机临时路径，再由 DownloadToLocal 写入本机，
// 因此三段式传输流程经过真实代码路径。
type fakeExecutor struct {
	mu sync.Mutex
	// commands 记录收到的执行请求。
	commands []agent.ContainerExecRequest
	// responder 按请求返回结果；返回 zero 结果时使用默认成功响应。
	responder func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error)
	// containerFiles 是"容器内"文件内容。
	containerFiles map[string]string
	// hostFiles 是"远程宿主机"临时文件内容。
	hostFiles map[string]string
	// removedHostFiles 记录被清理的宿主机临时文件。
	removedHostFiles []string
	// downloadFailures 让接下来的若干次下载失败。
	downloadFailures int
	// copyFailures 让接下来的若干次容器导出失败。
	copyFailures int
	// operations 按发生顺序记录执行与文件传输，用于验证恢复先于主调用。
	operations []string
	// uploads 记录本机文件到远程宿主机临时文件的传输。
	uploads []fileTransfer
	// containerCopies 记录远程宿主机临时文件到容器内路径的传输。
	containerCopies []fileTransfer
	// terminations 记录收到的容器终止请求。
	terminations []agent.ContainerTerminationRequest
	// terminationStates 记录每次终止时清理上下文的状态。
	terminationStates []terminationContextState
	// terminateResponder 返回终止结果；为空时报告已确认停止。
	terminateResponder func(
		request agent.ContainerTerminationRequest,
	) (agent.ContainerTerminationResult, error)
}

// terminationContextState 是终止调用发生时清理上下文的状态。
type terminationContextState struct {
	// err 是调用发生时上下文自身的错误状态。
	err error
	// deadline 是调用发生时上下文的时间上限。
	deadline time.Time
	// hasDeadline 表示上下文是否带时间上限。
	hasDeadline bool
}

// fileTransfer 是一次文件传输的起止路径。
type fileTransfer struct {
	source string
	target string
}

// readerOnlyExecutor 只实现 agent.ContainerExecutor，不提供容器文件写入能力。
//
// 它用于验证"执行器不支持把本机文件写入容器"时 Runner 明确失败，而不是静默丢弃
// 历史上下文后创建新的空 Session。
type readerOnlyExecutor struct {
	agent.ContainerExecutor
}

// Exec 记录请求并返回预设结果。
func (e *fakeExecutor) Exec(
	ctx context.Context,
	request agent.ContainerExecRequest,
) (agent.ContainerExecResult, error) {
	e.mu.Lock()
	e.commands = append(e.commands, request)
	e.operations = append(e.operations, "exec:"+strings.Join(request.Command, " "))
	responder := e.responder
	e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return agent.ContainerExecResult{
			ExitCode:  -1,
			TimedOut:  errors.Is(err, context.DeadlineExceeded),
			Cancelled: errors.Is(err, context.Canceled),
		}, err
	}
	if responder != nil {
		result, err := responder(e, request)
		if err != nil || result != (agent.ContainerExecResult{}) {
			return result, err
		}
	}
	return succeeded("READY\n"), nil
}

// CopyFromContainer 把"容器内"文件复制到宿主机临时路径。
func (e *fakeExecutor) CopyFromContainer(
	ctx context.Context,
	dockerID, containerPath, hostPath string,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.copyFailures > 0 {
		e.copyFailures--
		return errors.New("模拟容器导出失败")
	}
	content, ok := e.containerFiles[containerPath]
	if !ok {
		return errors.New("容器内文件不存在: " + containerPath)
	}
	e.hostFiles[hostPath] = content
	return nil
}

// DownloadToLocal 把宿主机临时文件写入本机路径。
func (e *fakeExecutor) DownloadToLocal(
	ctx context.Context,
	hostPath, localPath string,
	maxBytes int64,
) (int64, error) {
	e.mu.Lock()
	failures := e.downloadFailures
	if failures > 0 {
		e.downloadFailures = failures - 1
	}
	content, ok := e.hostFiles[hostPath]
	e.mu.Unlock()
	if failures > 0 {
		return 0, errors.New("模拟下载失败")
	}
	if !ok {
		return 0, errors.New("宿主机临时文件不存在: " + hostPath)
	}
	if maxBytes > 0 && int64(len(content)) > maxBytes {
		return 0, errors.New("文件超过大小上限")
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(localPath, []byte(content), 0o600); err != nil {
		return 0, err
	}
	return int64(len(content)), nil
}

// UploadFromLocal 把本机文件上传到"远程宿主机"临时路径。
func (e *fakeExecutor) UploadFromLocal(ctx context.Context, localPath, hostPath string) error {
	content, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.operations = append(e.operations, "upload:"+hostPath)
	e.uploads = append(e.uploads, fileTransfer{source: localPath, target: hostPath})
	e.hostFiles[hostPath] = string(content)
	return nil
}

// TerminateContainer 记录终止请求并返回预设结果。
//
// 它实现 agent.ContainerTerminator：Runner 只在调用方明确启用规划容器终止策略、
// 且 Pi 主命令已经启动但结果无法确认时才会调用它。
func (e *fakeExecutor) TerminateContainer(
	ctx context.Context,
	request agent.ContainerTerminationRequest,
) (agent.ContainerTerminationResult, error) {
	e.mu.Lock()
	e.terminations = append(e.terminations, request)
	deadline, hasDeadline := ctx.Deadline()
	e.terminationStates = append(e.terminationStates, terminationContextState{
		err:         ctx.Err(),
		deadline:    deadline,
		hasDeadline: hasDeadline,
	})
	e.operations = append(e.operations, "terminate:"+request.ContainerID)
	responder := e.terminateResponder
	e.mu.Unlock()
	if responder != nil {
		return responder(request)
	}
	return agent.ContainerTerminationResult{Stopped: true}, nil
}

// terminationCount 返回收到的终止请求数量。
func (e *fakeExecutor) terminationCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.terminations)
}

// terminationAt 返回第 index 次终止请求。
func (e *fakeExecutor) terminationAt(t *testing.T, index int) agent.ContainerTerminationRequest {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if index >= len(e.terminations) {
		t.Fatalf("第 %d 次终止请求不存在，实际记录 %d 次", index+1, len(e.terminations))
	}
	return e.terminations[index]
}

// terminationStateAt 返回第 index 次终止发生时清理上下文的状态。
//
// 状态在调用发生时记录：终止返回后 Runner 会立即释放清理上下文，事后再检查
// 上下文本身得不到调用时的真实状态。
func (e *fakeExecutor) terminationStateAt(t *testing.T, index int) terminationContextState {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if index >= len(e.terminationStates) {
		t.Fatalf("第 %d 次终止上下文不存在，实际记录 %d 次", index+1, len(e.terminationStates))
	}
	return e.terminationStates[index]
}

// CopyToContainer 把"远程宿主机"文件复制到"容器内"路径。
func (e *fakeExecutor) CopyToContainer(
	ctx context.Context,
	dockerID, hostPath, containerPath string,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	content, ok := e.hostFiles[hostPath]
	if !ok {
		return errors.New("宿主机临时文件不存在: " + hostPath)
	}
	e.operations = append(e.operations, "copy-to-container:"+containerPath)
	e.containerCopies = append(e.containerCopies, fileTransfer{source: hostPath, target: containerPath})
	e.containerFiles[containerPath] = content
	return nil
}

// operationsSince 返回从指定下标开始的操作记录副本。
func (e *fakeExecutor) operationsSince(index int) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if index < 0 || index > len(e.operations) {
		return nil
	}
	return append([]string(nil), e.operations[index:]...)
}

// operationCount 返回已记录的操作数量。
func (e *fakeExecutor) operationCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.operations)
}

// RemoveHostFile 删除宿主机临时文件。
func (e *fakeExecutor) RemoveHostFile(ctx context.Context, hostPath string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removedHostFiles = append(e.removedHostFiles, hostPath)
	delete(e.hostFiles, hostPath)
	return nil
}

// requests 返回收到的全部执行请求副本。
func (e *fakeExecutor) requests() []agent.ContainerExecRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]agent.ContainerExecRequest(nil), e.commands...)
}

// remainingHostFiles 返回尚未清理的宿主机临时文件数量。
func (e *fakeExecutor) remainingHostFiles() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.hostFiles)
}

// writeContainerFile 写入"容器内"文件。
func (e *fakeExecutor) writeContainerFile(path, content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.containerFiles[path] = content
}

// fakeGuard 返回预设的容器记录或错误。
type fakeGuard struct {
	container domain.Container
	err       error
}

// EnsureExecutable 返回预设容器记录。
func (g *fakeGuard) EnsureExecutable(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	if g.err != nil {
		return domain.Container{}, g.err
	}
	return g.container, nil
}

// succeeded 构造成功的执行结果。
func succeeded(stdout string) agent.ContainerExecResult {
	return agent.ContainerExecResult{
		ExitCode: 0,
		Stdout:   stdout,
		Certain:  true,
	}
}

// newContainer 构造运行中的容器记录。
func newContainer() domain.Container {
	return domain.Container{
		ID:          domain.ID(testContainerID),
		HostID:      domain.ID("host-1"),
		DockerID:    testDockerID,
		Name:        "agentflow-container-1",
		Environment: domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
		State:       domain.ContainerStateRunning,
		DockerState: domain.DockerStateRunning,
	}
}

// harness 是一次容器内 Runner 调用的测试装置。
type harness struct {
	runner      *pi.ContainerRunner
	executor    *fakeExecutor
	request     agent.RunRequest
	sessionRoot string
	workDir     string
}

// newHarness 构造容器内 Runner 测试装置。
func newHarness(
	t *testing.T,
	responder func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error),
) *harness {
	t.Helper()
	return newHarnessWithOptions(t, responder, pi.Options{
		Timeout:       30 * time.Second,
		ExportTimeout: 5 * time.Second,
		GraceTimeout:  2 * time.Second,
	})
}

// newHarnessWithOptions 使用指定 Runner 选项构造测试装置。
func newHarnessWithOptions(
	t *testing.T,
	responder func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error),
	options pi.Options,
) *harness {
	t.Helper()
	workspace := testkit.New(t)
	workspace.Report()
	sessionRoot := workspace.SessionRoot()
	// 容器内工作目录是容器内部路径，与本机测试工作空间无关。
	workDir := "/workspace/project"
	executor := &fakeExecutor{
		responder:      responder,
		containerFiles: make(map[string]string),
		hostFiles:      make(map[string]string),
	}
	runner, err := pi.NewContainerRunner(options, executor, &fakeGuard{container: newContainer()})
	if err != nil {
		t.Fatalf("构造容器内 Runner 失败: %v", err)
	}
	request := agent.RunRequest{
		SessionID:   "session-1",
		TaskID:      "task-1",
		StepID:      "step-1",
		AttemptID:   "attempt-1",
		Model:       testModel,
		Prompt:      "只回答 READY",
		SessionRoot: sessionRoot,
		Timeout:     30 * time.Second,
		Container: &agent.ContainerTarget{
			ContainerID: testContainerID,
			DockerID:    testDockerID,
			WorkDir:     workDir,
		},
	}
	return &harness{
		runner:      runner,
		executor:    executor,
		request:     request,
		sessionRoot: sessionRoot,
		workDir:     workDir,
	}
}

// successResponder 模拟完整成功流程：写入容器内 Session 并完成导出。
func successResponder(
	executor *fakeExecutor,
	request agent.ContainerExecRequest,
) (agent.ContainerExecResult, error) {
	if isVersionProbe(request.Command) {
		return succeeded("9.9.9\n"), nil
	}
	if isExportCall(request.Command) {
		html := exportTargetOf(request.Command)
		if html != "" {
			executor.writeContainerFile(html, "<html>session</html>")
		}
		return succeeded("Exported\n"), nil
	}
	executor.writeContainerFile(sessionPathOf(request.Command), testSessionJSONL)
	return succeeded("READY\n"), nil
}

// isVersionProbe 判断是否是版本探测调用。
func isVersionProbe(command []string) bool {
	return containsArg(command, "--version")
}

// isExportCall 判断是否是 HTML 导出调用。
func isExportCall(command []string) bool {
	return containsArg(command, "--export")
}

// exportTargetOf 返回导出目标路径。
func exportTargetOf(command []string) string {
	for index, arg := range command {
		if arg == "--export" && index+2 < len(command) {
			return command[index+2]
		}
	}
	return ""
}

// sessionPathOf 从命令中提取 --session 的取值。
func sessionPathOf(command []string) string {
	for index, arg := range command {
		if arg == "--session" && index+1 < len(command) {
			return command[index+1]
		}
	}
	return ""
}

// containsArg 判断命令是否包含指定参数。
func containsArg(command []string, want string) bool {
	for _, arg := range command {
		if arg == want {
			return true
		}
	}
	return false
}

// mainCallOf 返回 --print 主调用请求。
func mainCallOf(requests []agent.ContainerExecRequest) *agent.ContainerExecRequest {
	for index := range requests {
		if containsArg(requests[index].Command, "--print") {
			return &requests[index]
		}
	}
	return nil
}

// TestCommandConstruction 验证容器内 Pi 参数构造与标准输入。
func TestCommandConstruction(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("期望退出码 0，实际 %d", result.ExitCode)
	}
	main := mainCallOf(h.executor.requests())
	if main == nil {
		t.Fatalf("没有收到 --print 调用")
	}
	for _, required := range []string{"--print", "--session", "--model"} {
		if !containsArg(main.Command, required) {
			t.Fatalf("缺少参数 %s: %v", required, main.Command)
		}
	}
	if containsArg(main.Command, "--no-session") {
		t.Fatalf("不允许使用 --no-session: %v", main.Command)
	}
	if !containsArg(main.Command, testModel) {
		t.Fatalf("缺少模型标识: %v", main.Command)
	}
	if string(main.Stdin) != "只回答 READY\n" {
		t.Fatalf("标准输入内容不符: %q", main.Stdin)
	}
	if main.DockerID != testDockerID {
		t.Fatalf("执行容器不符: %s", main.DockerID)
	}
	// 没有可用工具时必须显式关闭全部工具，不能省略参数让 Pi 回退到默认工具列表。
	for _, required := range []string{"--no-tools", "--no-mcp", "--no-extensions"} {
		if !containsArg(main.Command, required) {
			t.Fatalf("缺少参数 %s: %v", required, main.Command)
		}
	}
	if toolListIndex(main.Command) >= 0 {
		t.Fatalf("没有可用工具时不允许使用 --tools: %v", main.Command)
	}

	// 有可用工具时用显式列表替换默认选择，并保持关闭 MCP 与扩展。
	request := h.request
	request.Tools = []string{"read", "grep"}
	if _, err := h.runner.Run(context.Background(), request, nil); err != nil {
		t.Fatalf("带工具策略的执行失败: %v", err)
	}
	requests := h.executor.requests()
	withTools := mainCallOf(requests[len(requests)-3:])
	if withTools == nil {
		t.Fatalf("没有收到带工具策略的 --print 调用")
	}
	index := toolListIndex(withTools.Command)
	if index < 0 || withTools.Command[index] != "read,grep" {
		t.Fatalf("工具列表参数不符: %v", withTools.Command)
	}
	if containsArg(withTools.Command, "--no-tools") {
		t.Fatalf("带工具列表时不允许同时使用 --no-tools: %v", withTools.Command)
	}
	for _, required := range []string{"--no-mcp", "--no-extensions"} {
		if !containsArg(withTools.Command, required) {
			t.Fatalf("带工具列表时仍缺少参数 %s: %v", required, withTools.Command)
		}
	}
}

// toolListIndex 返回 --tools 在命令中的位置，缺失时返回 -1。
func toolListIndex(command []string) int {
	for index, arg := range command {
		if arg == "--tools" {
			return index + 1
		}
	}
	return -1
}

// TestSessionPathUniquePerInvocation 验证每次调用使用独立的容器内与本机 Session 路径。
func TestSessionPathUniquePerInvocation(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	paths := make(map[string]struct{}, 3)
	locals := make(map[string]struct{}, 3)
	for index := 0; index < 3; index++ {
		request := h.request
		request.InvocationID = ""
		result, err := h.runner.Run(context.Background(), request, nil)
		if err != nil {
			t.Fatalf("第 %d 次执行失败: %v", index+1, err)
		}
		if result.InvocationID == "" {
			t.Fatalf("结果缺少调用标识")
		}
		if _, ok := paths[result.Session.JSONLPath]; ok {
			t.Fatalf("多次调用复用了本机 Session 路径: %s", result.Session.JSONLPath)
		}
		paths[result.Session.JSONLPath] = struct{}{}
		if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
			t.Fatalf("本机 Session 文件不存在: %v", statErr)
		}
		locals[result.InvocationID] = struct{}{}
	}
	if len(locals) != 3 {
		t.Fatalf("期望 3 个不同调用标识，实际 %d", len(locals))
	}
	// 容器内 Session 路径同样必须唯一。
	containerPaths := make(map[string]struct{}, 3)
	for _, request := range h.executor.requests() {
		path := sessionPathOf(request.Command)
		if path == "" {
			continue
		}
		containerPaths[path] = struct{}{}
	}
	if len(containerPaths) != 3 {
		t.Fatalf("期望 3 个不同容器内 Session 路径，实际 %d: %v", len(containerPaths), containerPaths)
	}
}

// TestSessionSavedLocally 验证原始 Session 保存到本机并记录完整性。
func TestSessionSavedLocally(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Session.Completeness != agent.SessionComplete {
		t.Fatalf("期望完整性 %s，实际 %s（%s）",
			agent.SessionComplete, result.Session.Completeness, result.Session.Error)
	}
	if result.Session.ExportStatus != agent.ExportSucceeded {
		t.Fatalf("期望导出成功，实际 %s", result.Session.ExportStatus)
	}
	for _, path := range []string{result.Session.JSONLPath, result.Session.HTMLPath} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("本机文件不存在: %s: %v", path, statErr)
		}
	}
	// Session 文件内容与容器内一致。
	content, readErr := os.ReadFile(result.Session.JSONLPath)
	if readErr != nil {
		t.Fatalf("读取本机 Session 失败: %v", readErr)
	}
	if string(content) != testSessionJSONL {
		t.Fatalf("本机 Session 内容与容器内不一致")
	}
	// 远程临时副本在成功后应被清理。
	if remaining := h.executor.remainingHostFiles(); remaining != 0 {
		t.Fatalf("远程临时副本未清理，剩余 %d 个", remaining)
	}
}

// TestExportFailureKeepsJSONL 验证 HTML 导出失败时原始 JSONL 仍然保留。
func TestExportFailureKeepsJSONL(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		if isExportCall(request.Command) {
			return agent.ContainerExecResult{
				ExitCode: 5,
				Stderr:   "导出失败",
				Certain:  true,
			}, nil
		}
		executor.writeContainerFile(sessionPathOf(request.Command), testSessionJSONL)
		return succeeded("READY\n"), nil
	})
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err != nil {
		t.Fatalf("导出失败不应让执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if result.Session.ExportStatus != agent.ExportFailed {
		t.Fatalf("期望导出状态 failed，实际 %s", result.Session.ExportStatus)
	}
	if result.Session.Completeness != agent.SessionExportFailed {
		t.Fatalf("期望完整性 %s，实际 %s",
			agent.SessionExportFailed, result.Session.Completeness)
	}
	if result.Session.JSONLPath == "" {
		t.Fatalf("原始 Session 路径不应为空")
	}
	if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
		t.Fatalf("原始 Session 文件必须保留: %v", statErr)
	}
	if result.Session.HTMLPath != "" {
		t.Fatalf("导出失败时不应记录 HTML 路径: %s", result.Session.HTMLPath)
	}
}

// TestMissingSessionMarksFailure 验证容器内没有 Session 时不能报告成功。
func TestMissingSessionMarksFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		if isExportCall(request.Command) {
			return succeeded(""), nil
		}
		return succeeded("READY\n"), nil
	})
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err == nil {
		t.Fatalf("缺少 Session 时不应返回成功")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.Session.Completeness != agent.SessionMissing {
		t.Fatalf("期望完整性 %s，实际 %s",
			agent.SessionMissing, result.Session.Completeness)
	}
}

// TestPartialSessionRecorded 验证残缺 Session 被记录为 partial 并保留文件。
func TestPartialSessionRecorded(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		if isExportCall(request.Command) {
			return succeeded(""), nil
		}
		executor.writeContainerFile(sessionPathOf(request.Command), partialSessionJSONL)
		return succeeded("READY\n"), nil
	})
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Session.Completeness != agent.SessionPartial {
		t.Fatalf("期望完整性 %s，实际 %s",
			agent.SessionPartial, result.Session.Completeness)
	}
	if result.Session.JSONLPath == "" {
		t.Fatalf("残缺 Session 仍应保留文件路径")
	}
	if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
		t.Fatalf("残缺 Session 文件必须保留: %v", statErr)
	}
}

// TestDownloadFailureKeepsRemoteCopy 验证下载失败时不清理远程副本并记录状态。
func TestDownloadFailureKeepsRemoteCopy(t *testing.T) {
	t.Parallel()
	// 关闭 HTML 导出，使唯一的下载动作就是 Session 提取。
	h := newHarnessWithOptions(t, successResponder, pi.Options{
		Timeout:       30 * time.Second,
		GraceTimeout:  2 * time.Second,
		DisableExport: true,
	})
	h.executor.downloadFailures = 1
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err == nil {
		t.Fatalf("下载失败时应返回错误")
	}
	if result.Session.Completeness == agent.SessionComplete {
		t.Fatalf("提取失败不能被标记为 complete")
	}
	if result.Session.Error == "" {
		t.Fatalf("提取失败应记录可诊断说明")
	}
	if h.executor.remainingHostFiles() == 0 {
		t.Fatalf("下载失败时应保留远程临时副本以便排查")
	}
}

// TestNonZeroExitClassified 验证非零退出码被归类为失败。
func TestNonZeroExitClassified(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		if isExportCall(request.Command) {
			return succeeded(""), nil
		}
		executor.writeContainerFile(sessionPathOf(request.Command), partialSessionJSONL)
		return agent.ContainerExecResult{
			ExitCode: 3,
			Stderr:   "模型不可用",
			Certain:  true,
		}, nil
	})
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err == nil {
		t.Fatalf("非零退出码应返回错误")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.ExitCode != 3 {
		t.Fatalf("期望退出码 3，实际 %d", result.ExitCode)
	}
	if !strings.Contains(result.Failure, "模型不可用") {
		t.Fatalf("失败说明应包含标准错误摘要: %q", result.Failure)
	}
	if result.Session.Completeness != agent.SessionPartial {
		t.Fatalf("异常退出时应记录 partial，实际 %s", result.Session.Completeness)
	}
}

// TestTimeoutAndCancelClassified 验证超时与取消状态。
func TestTimeoutAndCancelClassified(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result agent.ContainerExecResult
		err    error
		want   agent.RunStatus
	}{
		{
			name: "超时",
			result: agent.ContainerExecResult{
				ExitCode: -1,
				TimedOut: true,
			},
			err:  context.DeadlineExceeded,
			want: agent.RunTimedOut,
		},
		{
			name: "取消",
			result: agent.ContainerExecResult{
				ExitCode:  -1,
				Cancelled: true,
			},
			err:  context.Canceled,
			want: agent.RunCancelled,
		},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error) {
				if isVersionProbe(request.Command) {
					return succeeded("9.9.9\n"), nil
				}
				if isExportCall(request.Command) {
					return succeeded(""), nil
				}
				executor.writeContainerFile(sessionPathOf(request.Command), partialSessionJSONL)
				return item.result, item.err
			})
			result, err := h.runner.Run(context.Background(), h.request, nil)
			if err == nil {
				t.Fatalf("期望返回错误")
			}
			if result.Status != item.want {
				t.Fatalf("期望状态 %s，实际 %s", item.want, result.Status)
			}
		})
	}
}

// TestUncertainProcessReported 验证无法确认进程结束时返回可诊断状态。
func TestUncertainProcessReported(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		if isExportCall(request.Command) {
			return succeeded(""), nil
		}
		executor.writeContainerFile(sessionPathOf(request.Command), partialSessionJSONL)
		return agent.ContainerExecResult{ExitCode: -1, Certain: false}, nil
	})
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err == nil {
		t.Fatalf("进程结束状态不确定时应返回错误")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if !strings.Contains(result.Failure, "无法确认") {
		t.Fatalf("失败说明应提示需要核验: %q", result.Failure)
	}
}

// TestPiUnavailableInContainer 验证容器内缺少 Pi 时失败且不执行主调用。
func TestPiUnavailableInContainer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return agent.ContainerExecResult{
				ExitCode: 127,
				Stderr:   "pi: command not found",
				Certain:  true,
			}, nil
		}
		return succeeded("READY\n"), nil
	})
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err == nil {
		t.Fatalf("容器内缺少 Pi 时应返回错误")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if mainCallOf(h.executor.requests()) != nil {
		t.Fatalf("Pi 不可用时不应启动主调用")
	}
}

// TestDetachedContainerRejected 验证已脱离管理的容器不会被执行。
func TestDetachedContainerRejected(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	container := newContainer()
	container.Detached = true
	container.State = domain.ContainerStateDetached
	executor := &fakeExecutor{
		containerFiles: make(map[string]string),
		hostFiles:      make(map[string]string),
	}
	runner, err := pi.NewContainerRunner(pi.Options{}, executor, &fakeGuard{container: container})
	if err != nil {
		t.Fatalf("构造容器内 Runner 失败: %v", err)
	}
	result, runErr := runner.Run(context.Background(), agent.RunRequest{
		SessionID:   "session-1",
		Model:       testModel,
		Prompt:      "只回答 READY",
		SessionRoot: workspace.SessionRoot(),
		Container: &agent.ContainerTarget{
			ContainerID: testContainerID,
			DockerID:    testDockerID,
		},
	}, nil)
	if runErr == nil {
		t.Fatalf("已脱离管理的容器不应执行")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if len(executor.requests()) != 0 {
		t.Fatalf("已脱离管理的容器不应执行任何命令")
	}
}

// TestDockerIDMismatchRejected 验证容器记录与请求的 Docker ID 不一致时拒绝执行。
func TestDockerIDMismatchRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	request := h.request
	request.Container = &agent.ContainerTarget{
		ContainerID: testContainerID,
		DockerID:    "other-docker-id",
	}
	_, err := h.runner.Run(context.Background(), request, nil)
	if err == nil {
		t.Fatalf("Docker ID 不一致时应返回错误")
	}
	if len(h.executor.requests()) != 0 {
		t.Fatalf("Docker ID 不一致时不应执行任何命令")
	}
}

// 稳定 Session 用例使用的固定标识。
const (
	// testPlanningSessionID 是稳定 Session 用例的规划会话标识。
	testPlanningSessionID = "planning-session-1"
	// testAnalystSessionID 是需求分析师角色的会话标识。
	testAnalystSessionID = "analyst-session-1"
)

// stableContainerPath 返回容器内固定的 Session 文件路径。
//
// 结构与规划运行时生成的路径一致：<规划会话标识>/<角色会话标识>/session.jsonl，
// 位于容器 Session 根目录之内。
func stableContainerPath() string {
	return pi.DefaultContainerSessionDir + "/" + testPlanningSessionID + "/" +
		testAnalystSessionID + "/session.jsonl"
}

// stableTarget 返回一次稳定 Session 调用的两个路径。
func (h *harness) stableTarget() *agent.StableSessionTarget {
	return &agent.StableSessionTarget{
		ContainerPath: stableContainerPath(),
		ArchiveDir: filepath.Join(
			h.sessionRoot,
			"planning",
			testPlanningSessionID,
			testAnalystSessionID,
		),
	}
}

// stableRequest 构造一次使用稳定 Session 的调用请求。
func (h *harness) stableRequest(invocationID string) agent.RunRequest {
	request := h.request
	request.InvocationID = invocationID
	request.StableSession = h.stableTarget()
	return request
}

// archivePath 返回稳定 Session 的本机归档路径。
func (h *harness) archivePath() string {
	return filepath.Join(h.stableTarget().ArchiveDir, "session.jsonl")
}

// writeArchive 写入本机稳定归档内容。
func (h *harness) writeArchive(t *testing.T, content string) {
	t.Helper()
	dir := h.stableTarget().ArchiveDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("创建本机归档目录失败: %v", err)
	}
	if err := os.WriteFile(h.archivePath(), []byte(content), 0o600); err != nil {
		t.Fatalf("写入本机归档失败: %v", err)
	}
}

// readArchive 读取本机稳定归档内容。
func (h *harness) readArchive(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(h.archivePath())
	if err != nil {
		t.Fatalf("读取本机归档失败: %v", err)
	}
	return string(content)
}

// operationIndex 返回第一条匹配前缀的操作下标，缺失时返回 -1。
func operationIndex(operations []string, prefix string) int {
	for index, operation := range operations {
		if strings.HasPrefix(operation, prefix) {
			return index
		}
	}
	return -1
}

// mainCallIndex 返回第一次 --print 主调用的操作下标，缺失时返回 -1。
func mainCallIndex(operations []string) int {
	for index, operation := range operations {
		if strings.HasPrefix(operation, "exec:") && strings.Contains(operation, "--print") {
			return index
		}
	}
	return -1
}

// TestStableSessionFirstRunCreatesArchive 校验首轮调用建立本机稳定归档。
func TestStableSessionFirstRunCreatesArchive(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	if _, err := os.Stat(h.archivePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("首轮调用前不应存在本机归档，实际 %v", err)
	}
	result, err := h.runner.Run(context.Background(), h.stableRequest("invoke-1"), nil)
	if err != nil {
		t.Fatalf("首轮执行失败: %v", err)
	}
	// 没有历史归档时不进行恢复：既不上传本机文件，也不写入容器内路径。
	operations := h.executor.operationsSince(0)
	if index := operationIndex(operations, "upload:"); index >= 0 {
		t.Fatalf("首轮调用不应上传历史归档: %v", operations)
	}
	if index := operationIndex(operations, "copy-to-container:"); index >= 0 {
		t.Fatalf("首轮调用不应恢复历史归档: %v", operations)
	}
	main := mainCallOf(h.executor.requests())
	if main == nil {
		t.Fatal("没有收到 --print 主调用")
	}
	if path := sessionPathOf(main.Command); path != stableContainerPath() {
		t.Fatalf("Pi 必须使用服务端提供的固定容器 Session 路径，实际 %s", path)
	}
	if result.StableSession.ContainerPath != stableContainerPath() {
		t.Fatalf("结果中的容器路径不符: %s", result.StableSession.ContainerPath)
	}
	if result.StableSession.Restored {
		t.Fatal("首轮调用不应报告已恢复历史 Session")
	}
	if !result.StableSession.Archived {
		t.Fatalf("首轮调用应建立稳定归档: %+v", result.StableSession)
	}
	if result.StableSession.ArchivePath != h.archivePath() {
		t.Fatalf("归档路径不符: %s", result.StableSession.ArchivePath)
	}
	if result.StableSession.Bytes != int64(len(testSessionJSONL)) {
		t.Fatalf("归档字节数不符: %d", result.StableSession.Bytes)
	}
	if content := h.readArchive(t); content != testSessionJSONL {
		t.Fatalf("归档内容与完整 Session 不一致: %q", content)
	}
	if result.Session.Completeness != agent.SessionComplete {
		t.Fatalf("单轮 Session 应完整，实际 %s", result.Session.Completeness)
	}
}

// TestStableSessionSecondRunRestoresBeforePi 校验第二轮先恢复历史再启动 Pi。
func TestStableSessionSecondRunRestoresBeforePi(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	first := h.stableRequest("invoke-1")
	firstResult, err := h.runner.Run(context.Background(), first, nil)
	if err != nil {
		t.Fatalf("首轮执行失败: %v", err)
	}
	if content := h.readArchive(t); content != testSessionJSONL {
		t.Fatalf("首轮结束后应存在完整稳定归档: %q", content)
	}

	// 第二轮使用新的调用标识，但必须复用同一个容器 Session 路径与本机归档。
	baseline := h.executor.operationCount()
	second := h.stableRequest("invoke-2")
	result, err := h.runner.Run(context.Background(), second, nil)
	if err != nil {
		t.Fatalf("第二轮执行失败: %v", err)
	}
	if result.InvocationID == firstResult.InvocationID {
		t.Fatalf("每轮的调用标识必须不同: %s", result.InvocationID)
	}
	if !result.StableSession.Restored {
		t.Fatalf("第二轮应恢复历史 Session: %+v", result.StableSession)
	}
	if !result.StableSession.Archived {
		t.Fatalf("第二轮应更新稳定归档: %+v", result.StableSession)
	}
	operations := h.executor.operationsSince(baseline)
	uploadIndex := operationIndex(operations, "upload:")
	mkdirIndex := -1
	for index, operation := range operations {
		if strings.HasPrefix(operation, "exec:") && strings.Contains(operation, "mkdir") {
			mkdirIndex = index
			break
		}
	}
	copyIndex := operationIndex(operations, "copy-to-container:")
	promptIndex := mainCallIndex(operations)
	if uploadIndex < 0 || mkdirIndex < 0 || copyIndex < 0 || promptIndex < 0 {
		t.Fatalf("缺少恢复或执行操作: %v", operations)
	}
	// 顺序固定为：本机文件 → 远程宿主机临时文件 → 容器固定 Session 路径 → 启动 Pi。
	if !(uploadIndex < mkdirIndex && mkdirIndex < copyIndex && copyIndex < promptIndex) {
		t.Fatalf("恢复必须先于主 Pi 调用完成: %v", operations)
	}
	if len(h.executor.uploads) == 0 || len(h.executor.containerCopies) == 0 {
		t.Fatal("应记录上传与写入容器的实际传输")
	}
	upload := h.executor.uploads[len(h.executor.uploads)-1]
	if upload.source != h.archivePath() {
		t.Fatalf("上传来源应是本机归档，实际 %s", upload.source)
	}
	if !strings.Contains(upload.target, "/restore/") {
		t.Fatalf("上传目标应位于远程宿主机受控临时目录，实际 %s", upload.target)
	}
	copied := h.executor.containerCopies[len(h.executor.containerCopies)-1]
	if copied.target != stableContainerPath() {
		t.Fatalf("写入容器的目标应是固定 Session 路径，实际 %s", copied.target)
	}
	if copied.source != upload.target {
		t.Fatalf("写入容器应使用刚上传的远程临时文件: %s", copied.source)
	}
	// 第二轮仍然使用同一个固定容器 Session 路径。
	requests := h.executor.requests()
	main := mainCallOf(requests[len(requests)-4:])
	if main == nil {
		t.Fatal("没有收到第二轮的 --print 主调用")
	}
	if path := sessionPathOf(main.Command); path != stableContainerPath() {
		t.Fatalf("第二轮必须复用同一个容器 Session 路径，实际 %s", path)
	}
	if content := h.readArchive(t); content != testSessionJSONL {
		t.Fatalf("第二轮结束后归档应是最新完整快照: %q", content)
	}
}

// TestStableSessionFailedRunKeepsLastKnownGoodArchive 校验失败轮不覆盖最后一份完整归档。
func TestStableSessionFailedRunKeepsLastKnownGoodArchive(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(executor *fakeExecutor, request agent.ContainerExecRequest) (agent.ContainerExecResult, error) {
		if isVersionProbe(request.Command) {
			return succeeded("9.9.9\n"), nil
		}
		if isExportCall(request.Command) {
			return succeeded(""), nil
		}
		// 恢复历史 Session 时 Runner 会先在容器内创建目录，该命令不属于主调用。
		if sessionPathOf(request.Command) == "" {
			return succeeded(""), nil
		}
		executor.writeContainerFile(sessionPathOf(request.Command), partialSessionJSONL)
		return agent.ContainerExecResult{ExitCode: 3, Stderr: "模型不可用", Certain: true}, nil
	})
	h.writeArchive(t, testSessionJSONL)
	result, err := h.runner.Run(context.Background(), h.stableRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("非零退出码应返回错误")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.StableSession.Archived {
		t.Fatalf("失败的轮次不得报告已归档: %+v", result.StableSession)
	}
	if result.StableSession.Error == "" {
		t.Fatal("应说明本轮未更新稳定归档")
	}
	if content := h.readArchive(t); content != testSessionJSONL {
		t.Fatalf("失败轮次不得覆盖最后一次完整归档: %q", content)
	}
	// 单轮 Session 仍按既有规则保存，供诊断使用。
	if result.Session.JSONLPath == "" {
		t.Fatal("失败轮次仍应保留单轮 Session 文件")
	}
	content, readErr := os.ReadFile(result.Session.JSONLPath)
	if readErr != nil {
		t.Fatalf("读取单轮 Session 失败: %v", readErr)
	}
	if string(content) != partialSessionJSONL {
		t.Fatalf("单轮 Session 内容不符: %q", string(content))
	}
}

// TestStableSessionCorruptArchiveRejectsBeforePi 校验损坏归档在恢复阶段被拒绝。
func TestStableSessionCorruptArchiveRejectsBeforePi(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	h.writeArchive(t, partialSessionJSONL)
	result, err := h.runner.Run(context.Background(), h.stableRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("损坏的本机归档应让本轮失败")
	}
	if mainCallOf(h.executor.requests()) != nil {
		t.Fatal("恢复失败时不得启动主 Pi 调用")
	}
	if result.StableSession.Restored {
		t.Fatal("恢复失败不应报告已恢复")
	}
	if result.StableSession.Error == "" {
		t.Fatal("恢复失败应给出可诊断说明")
	}
	if !strings.Contains(result.StableSession.Error, "归档") {
		t.Fatalf("错误说明应指明 Session 归档不可用: %q", result.StableSession.Error)
	}
	if content := h.readArchive(t); content != partialSessionJSONL {
		t.Fatalf("恢复失败不得删除或改写原归档: %q", content)
	}
}

// TestStableSessionArchiveFailureIsReported 校验归档失败与执行结论分别上报。
func TestStableSessionArchiveFailureIsReported(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	h.writeArchive(t, testSessionJSONL)
	// 受控文件系统故障：归档临时路径被一个非空目录占据，原子替换无法完成。
	tempPath := filepath.Join(h.stableTarget().ArchiveDir, ".invoke-1.tmp")
	if err := os.MkdirAll(tempPath, 0o700); err != nil {
		t.Fatalf("创建故障目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempPath, "blocker"), []byte("x"), 0o600); err != nil {
		t.Fatalf("写入故障文件失败: %v", err)
	}
	result, err := h.runner.Run(context.Background(), h.stableRequest("invoke-1"), nil)
	if err == nil {
		t.Fatal("归档失败必须作为错误上报")
	}
	if !errors.Is(err, agent.ErrSessionArchive) {
		t.Fatalf("归档失败应返回 agent.ErrSessionArchive，实际 %v", err)
	}
	// Pi 本身执行正常，执行结论不得被归档失败改写。
	if result.Status != agent.RunCompleted {
		t.Fatalf("归档失败不应把执行状态改为 %s", result.Status)
	}
	if result.StableSession.Archived {
		t.Fatalf("归档失败不得报告已归档: %+v", result.StableSession)
	}
	if result.StableSession.Error == "" {
		t.Fatal("StableSession.Error 应说明归档失败")
	}
	if content := h.readArchive(t); content != testSessionJSONL {
		t.Fatalf("归档失败不得破坏已有完整归档: %q", content)
	}
}

// TestStableSessionRestoreRejectsUnsupportedWriter 校验缺少容器写入能力时明确失败。
func TestStableSessionRestoreRejectsUnsupportedWriter(t *testing.T) {
	t.Parallel()
	h := newHarness(t, successResponder)
	h.writeArchive(t, testSessionJSONL)
	// 只暴露 agent.ContainerExecutor：执行器不实现 agent.ContainerFileWriter。
	runner, err := pi.NewContainerRunner(
		pi.Options{Timeout: 30 * time.Second, GraceTimeout: 2 * time.Second},
		readerOnlyExecutor{ContainerExecutor: h.executor},
		&fakeGuard{container: newContainer()},
	)
	if err != nil {
		t.Fatalf("构造容器内 Runner 失败: %v", err)
	}
	result, runErr := runner.Run(context.Background(), h.stableRequest("invoke-1"), nil)
	if runErr == nil {
		t.Fatal("缺少容器写入能力时应明确失败，不能静默忽略历史上下文")
	}
	if mainCallOf(h.executor.requests()) != nil {
		t.Fatal("无法恢复历史上下文时不得启动主 Pi 调用")
	}
	if result.StableSession.Restored {
		t.Fatal("未恢复历史上下文时不应报告已恢复")
	}
	if !strings.Contains(result.StableSession.Error, "不支持") {
		t.Fatalf("错误说明应指明执行器不支持写入容器: %q", result.StableSession.Error)
	}
	if content := h.readArchive(t); content != testSessionJSONL {
		t.Fatalf("执行器能力不足时不得删除历史归档: %q", content)
	}
}
