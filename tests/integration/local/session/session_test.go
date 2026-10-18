// Package session_local 提供 Session 提取与持久化边界的本机集成测试。
//
// 这里使用把容器与远程宿主机映射到本机目录的执行替身，因此"容器内文件 →
// 宿主机临时文件 → 本机持久化文件"的三段式传输、字节校验、原子提交与
// 失败保留行为都在真实文件系统上验证，不需要 SSH 或 Docker。
package session_local

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
)

// sessionContent 生成指定行数的合法 Session 内容。
//
// 第一行必须是会话头部，其余行是合法 JSON，因此文件整体可通过校验。
func sessionContent(lines int) string {
	var builder strings.Builder
	builder.WriteString(
		`{"type":"session","version":3,"id":"fake-session","cwd":"/workspace/project"}` + "\n",
	)
	for index := 1; index < lines; index++ {
		builder.WriteString(`{"type":"message","id":"m` +
			padNumber(index) +
			`","parentId":null,"timestamp":"2025-01-02T03:04:06.000Z",` +
			`"message":{"role":"user","content":"第 ` + padNumber(index) + ` 行","timestamp":1735787046000}}` +
			"\n")
	}
	return builder.String()
}

// padNumber 把整数补齐为固定宽度字符串。
func padNumber(value int) string {
	digits := "0123456789"
	result := make([]byte, 0, 6)
	for shift := 100000; shift > 0; shift /= 10 {
		result = append(result, digits[(value/shift)%10])
	}
	return string(result)
}

// fileExecutor 把容器、远程宿主机与本机文件系统映射到测试工作空间。
type fileExecutor struct {
	mu sync.Mutex
	// commands 记录执行请求。
	commands []agent.ContainerExecRequest
	// containerRoot 是"容器内"文件系统根目录。
	containerRoot string
	// hostRoot 是"远程宿主机"临时目录根。
	hostRoot string
	// sessionContent 是容器内 Session 文件内容。
	sessionContent string
	// htmlContent 是导出生成的 HTML 内容。
	htmlContent string
	// exportFails 表示导出命令失败。
	exportFails bool
	// downloadInterrupts 让接下来的若干次下载在写入部分内容后失败。
	downloadInterrupts int
	// removedHostFiles 记录被清理的宿主机临时文件。
	removedHostFiles []string
	// downloadTargets 记录每次下载使用的本机目标路径。
	downloadTargets []string
}

// Exec 模拟容器内 Pi 调用与 HTML 导出。
func (e *fileExecutor) Exec(
	ctx context.Context,
	request agent.ContainerExecRequest,
) (agent.ContainerExecResult, error) {
	e.mu.Lock()
	e.commands = append(e.commands, request)
	e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return agent.ContainerExecResult{
			ExitCode:  -1,
			TimedOut:  errors.Is(err, context.DeadlineExceeded),
			Cancelled: errors.Is(err, context.Canceled),
		}, err
	}
	switch {
	case containsArg(request.Command, "--version"):
		return okResult("9.9.9\n"), nil
	case containsArg(request.Command, "--export"):
		if e.exportFails {
			return agent.ContainerExecResult{
				ExitCode: 5,
				Stderr:   "导出失败",
				Certain:  true,
			}, nil
		}
		target := exportTargetOf(request.Command)
		if target != "" {
			e.writeContainerFile(target, e.htmlContent)
		}
		return okResult("Exported\n"), nil
	default:
		path := sessionPathOf(request.Command)
		if path != "" {
			e.writeContainerFile(path, e.sessionContent)
		}
		return okResult("READY\n"), nil
	}
}

// CopyFromContainer 把容器内文件复制到宿主机临时路径。
func (e *fileExecutor) CopyFromContainer(
	ctx context.Context,
	dockerID, containerPath, hostPath string,
) error {
	source := e.containerPath(containerPath)
	content, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	target := e.hostPath(hostPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, content, 0o600)
}

// DownloadToLocal 把宿主机临时文件写入本机路径。
//
// downloadInterrupts 大于 0 时先写入部分内容再返回错误，用于验证"中断时
// 已写入的部分内容仍可检查"。
func (e *fileExecutor) DownloadToLocal(
	ctx context.Context,
	hostPath, localPath string,
	maxBytes int64,
) (int64, error) {
	e.mu.Lock()
	interrupts := e.downloadInterrupts
	if interrupts > 0 {
		e.downloadInterrupts = interrupts - 1
	}
	e.downloadTargets = append(e.downloadTargets, localPath)
	e.mu.Unlock()
	content, err := os.ReadFile(e.hostPath(hostPath))
	if err != nil {
		return 0, err
	}
	if maxBytes > 0 && int64(len(content)) > maxBytes {
		return 0, errors.New("文件超过大小上限")
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return 0, err
	}
	if interrupts > 0 {
		partial := content[:len(content)/2]
		if writeErr := os.WriteFile(localPath+".partial", partial, 0o600); writeErr != nil {
			return 0, writeErr
		}
		return int64(len(partial)), errors.New("模拟传输中断")
	}
	// 真实实现写入临时文件后原子重命名，这里保持同样语义。
	temp := localPath + ".tmp"
	if err := os.WriteFile(temp, content, 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(temp, localPath); err != nil {
		return 0, err
	}
	return int64(len(content)), nil
}

// RemoveHostFile 删除宿主机临时文件。
func (e *fileExecutor) RemoveHostFile(ctx context.Context, hostPath string) error {
	e.mu.Lock()
	e.removedHostFiles = append(e.removedHostFiles, hostPath)
	e.mu.Unlock()
	target := e.hostPath(hostPath)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// containerPath 把容器内路径映射到测试目录。
func (e *fileExecutor) containerPath(target string) string {
	return filepath.Join(e.containerRoot, filepath.FromSlash(strings.TrimPrefix(target, "/")))
}

// hostPath 把宿主机路径映射到测试目录。
func (e *fileExecutor) hostPath(target string) string {
	return filepath.Join(e.hostRoot, filepath.FromSlash(strings.TrimPrefix(target, "/")))
}

// writeContainerFile 写入容器内文件。
func (e *fileExecutor) writeContainerFile(target, content string) {
	path := e.containerPath(target)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(content), 0o600)
}

// hostFileCount 返回宿主机临时目录中的文件数量。
func (e *fileExecutor) hostFileCount(t *testing.T) int {
	t.Helper()
	total := 0
	walkErr := filepath.Walk(e.hostRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		total++
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历宿主机临时目录失败: %v", walkErr)
	}
	return total
}

// fakeGuard 返回固定容器记录。
type fakeGuard struct {
	container domain.Container
}

// EnsureExecutable 返回固定容器记录。
func (g *fakeGuard) EnsureExecutable(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	return g.container, nil
}

// okResult 构造成功结果。
func okResult(stdout string) agent.ContainerExecResult {
	return agent.ContainerExecResult{ExitCode: 0, Stdout: stdout, Certain: true}
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

// sessionPathOf 返回 --session 的取值。
func sessionPathOf(command []string) string {
	for index, arg := range command {
		if arg == "--session" && index+1 < len(command) {
			return command[index+1]
		}
	}
	return ""
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

// fixture 是一次 Session 提取测试的装置。
type fixture struct {
	runner      *pi.ContainerRunner
	executor    *fileExecutor
	request     agent.RunRequest
	sessionRoot string
}

// newFixture 构造 Session 提取测试装置。
func newFixture(
	t *testing.T,
	lines int,
	configure func(executor *fileExecutor, options *pi.Options),
) *fixture {
	t.Helper()
	workspace := testkit.New(t)
	workspace.Report()
	containerRoot := workspace.Dir("container")
	hostRoot := workspace.Dir("remote-host")
	executor := &fileExecutor{
		containerRoot:  containerRoot,
		hostRoot:       hostRoot,
		sessionContent: sessionContent(lines),
		htmlContent:    "<html>session</html>",
	}
	options := pi.Options{
		Timeout:       60 * time.Second,
		ExportTimeout: 10 * time.Second,
		GraceTimeout:  2 * time.Second,
	}
	if configure != nil {
		configure(executor, &options)
	}
	runner, err := pi.NewContainerRunner(options, executor, &fakeGuard{container: domain.Container{
		ID:          domain.ID(testContainerID),
		HostID:      domain.ID("host-1"),
		DockerID:    testDockerID,
		Name:        "agentflow-container-1",
		Environment: domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
		State:       domain.ContainerStateRunning,
		DockerState: domain.DockerStateRunning,
	}})
	if err != nil {
		t.Fatalf("构造容器内 Runner 失败: %v", err)
	}
	sessionRoot := workspace.SessionRoot()
	request := agent.RunRequest{
		SessionID:   "session-1",
		TaskID:      "task-1",
		StepID:      "step-1",
		AttemptID:   "attempt-1",
		Model:       testModel,
		Prompt:      "只回答 READY",
		SessionRoot: sessionRoot,
		Timeout:     60 * time.Second,
		Container: &agent.ContainerTarget{
			ContainerID: testContainerID,
			DockerID:    testDockerID,
			WorkDir:     "/workspace/project",
		},
	}
	t.Logf("CONTAINER_ROOT=%s", containerRoot)
	t.Logf("REMOTE_HOST_ROOT=%s", hostRoot)
	return &fixture{
		runner:      runner,
		executor:    executor,
		request:     request,
		sessionRoot: sessionRoot,
	}
}

// TestSessionTransferredCompletely 验证完整 Session 被逐字节复制到本机。
func TestSessionTransferredCompletely(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 200, nil)
	result, err := f.runner.Run(context.Background(), f.request, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Session.Completeness != agent.SessionComplete {
		t.Fatalf("期望完整性 %s，实际 %s（%s）",
			agent.SessionComplete, result.Session.Completeness, result.Session.Error)
	}
	local, readErr := os.ReadFile(result.Session.JSONLPath)
	if readErr != nil {
		t.Fatalf("读取本机 Session 失败: %v", readErr)
	}
	if string(local) != f.executor.sessionContent {
		t.Fatalf("本机 Session 与容器内内容不一致: 期望 %d 字节，实际 %d 字节",
			len(f.executor.sessionContent), len(local))
	}
	if int64(len(local)) != result.Session.Bytes {
		t.Fatalf("记录的字节数与文件实际大小不一致: %d != %d",
			result.Session.Bytes, len(local))
	}
	if f.executor.hostFileCount(t) != 0 {
		t.Fatalf("成功后应清理远程临时副本")
	}
	t.Logf("SESSION_JSONL=%s", result.Session.JSONLPath)
}

// TestLargeSessionNotLoadedIntoMemory 验证大文件也能通过分段复制完成传输。
func TestLargeSessionNotLoadedIntoMemory(t *testing.T) {
	t.Parallel()
	// 约 5 MiB 的合法 Session 内容。
	f := newFixture(t, 20000, nil)
	result, err := f.runner.Run(context.Background(), f.request, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Session.Completeness != agent.SessionComplete {
		t.Fatalf("期望完整性 %s，实际 %s（%s）",
			agent.SessionComplete, result.Session.Completeness, result.Session.Error)
	}
	info, statErr := os.Stat(result.Session.JSONLPath)
	if statErr != nil {
		t.Fatalf("读取本机 Session 属性失败: %v", statErr)
	}
	if info.Size() != int64(len(f.executor.sessionContent)) {
		t.Fatalf("本机文件大小不符: 期望 %d，实际 %d",
			len(f.executor.sessionContent), info.Size())
	}
}

// TestInterruptedTransferKeepsPartialFile 验证传输中断时已有部分文件仍可检查。
func TestInterruptedTransferKeepsPartialFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 200, func(executor *fileExecutor, options *pi.Options) {
		// 关闭导出，使唯一的下载动作是 Session 提取。
		options.DisableExport = true
		executor.downloadInterrupts = 1
	})
	result, err := f.runner.Run(context.Background(), f.request, nil)
	if err == nil {
		t.Fatalf("传输中断时应返回错误")
	}
	if result.Session.Completeness == agent.SessionComplete {
		t.Fatalf("中断的提取不能被标记为 complete")
	}
	if result.Session.Error == "" {
		t.Fatalf("中断的提取应记录可诊断说明")
	}
	// 提取失败时结果不记录正式路径，使用替身记录的下载目标路径。
	if len(f.executor.downloadTargets) == 0 {
		t.Fatalf("没有发生任何下载调用")
	}
	partial := f.executor.downloadTargets[0] + ".partial"
	info, statErr := os.Stat(partial)
	if statErr != nil {
		t.Fatalf("部分写入的文件应保留以便检查: %v", statErr)
	}
	if info.Size() == 0 {
		t.Fatalf("部分文件不应为空")
	}
	if f.executor.hostFileCount(t) == 0 {
		t.Fatalf("中断时应保留远程临时副本")
	}
}

// TestExportFailureKeepsJSONL 验证导出失败时原始 JSONL 仍然保留。
func TestExportFailureKeepsJSONL(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20, func(executor *fileExecutor, options *pi.Options) {
		executor.exportFails = true
	})
	result, err := f.runner.Run(context.Background(), f.request, nil)
	if err != nil {
		t.Fatalf("导出失败不应让执行失败: %v", err)
	}
	if result.Session.ExportStatus != agent.ExportFailed {
		t.Fatalf("期望导出状态 failed，实际 %s", result.Session.ExportStatus)
	}
	if result.Session.Completeness != agent.SessionExportFailed {
		t.Fatalf("期望完整性 %s，实际 %s",
			agent.SessionExportFailed, result.Session.Completeness)
	}
	if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
		t.Fatalf("原始 JSONL 必须保留: %v", statErr)
	}
}

// TestConcurrentSessionsDoNotOverwrite 验证并行调用的 Session 互不覆盖。
func TestConcurrentSessionsDoNotOverwrite(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	workspace.Report()
	sessionRoot := workspace.SessionRoot()
	type outcome struct {
		result agent.RunResult
		err    error
	}
	results := make(chan outcome, 3)
	for index := 0; index < 3; index++ {
		index := index
		go func() {
			containerRoot := workspace.Dir("container-" + string(rune('a'+index)))
			hostRoot := workspace.Dir("host-" + string(rune('a'+index)))
			executor := &fileExecutor{
				containerRoot:  containerRoot,
				hostRoot:       hostRoot,
				sessionContent: sessionContent(10 + index),
				htmlContent:    "<html>session</html>",
			}
			runner, err := pi.NewContainerRunner(pi.Options{
				Timeout:       60 * time.Second,
				ExportTimeout: 10 * time.Second,
				GraceTimeout:  2 * time.Second,
			}, executor, &fakeGuard{container: domain.Container{
				ID:          domain.ID(testContainerID),
				HostID:      domain.ID("host-1"),
				DockerID:    testDockerID,
				Name:        "agentflow-container-1",
				Environment: domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
				State:       domain.ContainerStateRunning,
				DockerState: domain.DockerStateRunning,
			}})
			if err != nil {
				results <- outcome{err: err}
				return
			}
			result, runErr := runner.Run(context.Background(), agent.RunRequest{
				SessionID:   "session-" + string(rune('a'+index)),
				TaskID:      "task-1",
				Model:       testModel,
				Prompt:      "只回答 READY",
				SessionRoot: sessionRoot,
				Timeout:     60 * time.Second,
				Container: &agent.ContainerTarget{
					ContainerID: testContainerID,
					DockerID:    testDockerID,
					WorkDir:     "/workspace/project",
				},
			}, nil)
			results <- outcome{result: result, err: runErr}
		}()
	}
	seen := make(map[string]struct{}, 3)
	for index := 0; index < 3; index++ {
		item := <-results
		if item.err != nil {
			t.Fatalf("并发执行失败: %v", item.err)
		}
		if _, ok := seen[item.result.Session.JSONLPath]; ok {
			t.Fatalf("并行调用覆盖了同一个 Session 文件: %s", item.result.Session.JSONLPath)
		}
		seen[item.result.Session.JSONLPath] = struct{}{}
	}
	if len(seen) != 3 {
		t.Fatalf("期望 3 个独立 Session 文件，实际 %d", len(seen))
	}
}

// TestSessionSurvivesContainerRemoval 验证容器目录删除后本机 Session 仍可读。
func TestSessionSurvivesContainerRemoval(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20, nil)
	result, err := f.runner.Run(context.Background(), f.request, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	// 模拟容器删除：只删除容器目录内容，本机 Session 目录不受影响。
	if removeErr := os.RemoveAll(f.executor.containerRoot); removeErr != nil {
		t.Fatalf("删除模拟容器目录失败: %v", removeErr)
	}
	content, readErr := os.ReadFile(result.Session.JSONLPath)
	if readErr != nil {
		t.Fatalf("容器删除后本机 Session 应仍可读: %v", readErr)
	}
	if string(content) != f.executor.sessionContent {
		t.Fatalf("容器删除后本机 Session 内容被破坏")
	}
	// HTML 也必须在本机存在。
	if _, statErr := os.Stat(result.Session.HTMLPath); statErr != nil {
		t.Fatalf("容器删除后本机 HTML 应仍可读: %v", statErr)
	}
}

// TestIncompleteSessionNotMarkedComplete 验证不完整 Session 不会被标记为 complete。
func TestIncompleteSessionNotMarkedComplete(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 5, func(executor *fileExecutor, options *pi.Options) {
		// 写入非法 JSON，模拟进程异常退出后的残缺文件。
		executor.sessionContent = `{"type":"session","version":3,"id":"fake","cwd":"/workspace/project"}
not-json
`
	})
	result, err := f.runner.Run(context.Background(), f.request, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Session.Completeness != agent.SessionPartial {
		t.Fatalf("期望完整性 %s，实际 %s",
			agent.SessionPartial, result.Session.Completeness)
	}
	if result.Session.Error == "" {
		t.Fatalf("残缺 Session 应记录可诊断说明")
	}
}
