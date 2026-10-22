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
}

// Exec 记录请求并返回预设结果。
func (e *fakeExecutor) Exec(
	ctx context.Context,
	request agent.ContainerExecRequest,
) (agent.ContainerExecResult, error) {
	e.mu.Lock()
	e.commands = append(e.commands, request)
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
