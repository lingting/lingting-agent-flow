package pi_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/tests/internal/fakepi"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestMain 让测试二进制同时充当假 Pi 子进程。
func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakepi.EnvScenario); scenario != "" {
		os.Exit(fakepi.Run())
	}
	os.Exit(m.Run())
}

// harness 是一次 Runner 调用的测试装置。
type harness struct {
	runner     *pi.Runner
	request    agent.RunRequest
	report     string
	executable string
	env        []string
	events     *eventRecorder
}

// eventRecorder 并发安全地收集内部事件。
type eventRecorder struct {
	mu     sync.Mutex
	events []agent.Event
}

// emit 记录一条内部事件。
func (r *eventRecorder) emit(event agent.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

// count 返回指定事件类型出现的次数。
func (r *eventRecorder) count(eventType agent.EventType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, event := range r.events {
		if event.Type == eventType {
			total++
		}
	}
	return total
}

// sessionIDs 返回事件中出现的会话标识集合。
func (r *eventRecorder) sessionIDs() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make(map[string]int)
	for _, event := range r.events {
		ids[event.SessionID]++
	}
	return ids
}

// newHarness 构造以测试二进制为假 Pi 的执行装置。
func newHarness(t *testing.T, scenario string, options pi.Options) *harness {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("获取测试可执行文件失败: %v", err)
	}
	report := filepath.Join(testkit.Dir(t), "report.json")
	options.Executable = executable
	options.Env = append(os.Environ(),
		fakepi.EnvScenario+"="+scenario,
		fakepi.EnvReport+"="+report,
	)
	if options.GraceTimeout <= 0 {
		options.GraceTimeout = 2 * time.Second
	}
	if options.ExportTimeout <= 0 {
		options.ExportTimeout = 10 * time.Second
	}
	return &harness{
		runner: pi.NewRunner(options),
		request: agent.RunRequest{
			SessionID:    "session-under-test",
			RoleType:     "developer",
			Model:        "fake/model-1",
			SystemPrompt: "你是测试助手。",
			Prompt:       "只回答 READY",
			WorkingDir:   testkit.Dir(t),
			SessionRoot:  filepath.Join(testkit.Dir(t), "sessions"),
			Timeout:      30 * time.Second,
		},
		report:     report,
		executable: executable,
		env:        options.Env,
		events:     &eventRecorder{},
	}
}

// run 执行一次 Runner 调用。
func (h *harness) run(ctx context.Context, t *testing.T) (agent.RunResult, error) {
	t.Helper()
	return h.runner.Run(ctx, h.request, h.events.emit)
}

// readReport 读取子进程写入的启动报告。
func (h *harness) readReport(t *testing.T) fakepi.Report {
	t.Helper()
	data, err := os.ReadFile(h.report)
	if err != nil {
		t.Fatalf("读取启动报告失败: %v", err)
	}
	var report fakepi.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("解析启动报告失败: %v", err)
	}
	return report
}

// TestRunCompletesNormally 验证正常执行链路、事件映射与 Session 持久化。
func TestRunCompletesNormally(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "normal", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if result.SessionID != "session-under-test" {
		t.Fatalf("内部会话标识不符: %s", result.SessionID)
	}
	if result.FinalText != "READY" {
		t.Fatalf("最终文本不符: %q", result.FinalText)
	}
	if result.ExitCode != 0 {
		t.Fatalf("期望退出码 0，实际 %d", result.ExitCode)
	}
	if result.StartedAt.IsZero() || result.FinishedAt.IsZero() {
		t.Fatal("开始与结束时间不能为空")
	}
	if result.Session.Completeness != agent.SessionComplete {
		t.Fatalf("期望 Session 完整，实际 %s", result.Session.Completeness)
	}
	if result.Session.ExportStatus != agent.ExportSucceeded {
		t.Fatalf("期望导出成功，实际 %s", result.Session.ExportStatus)
	}
	if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
		t.Fatalf("Session 文件不存在: %v", statErr)
	}
	if _, statErr := os.Stat(result.Session.HTMLPath); statErr != nil {
		t.Fatalf("HTML 文件不存在: %v", statErr)
	}
	if h.events.count(agent.EventRunStarted) != 1 {
		t.Fatalf("期望 1 次 run.started，实际 %d", h.events.count(agent.EventRunStarted))
	}
	if h.events.count(agent.EventSessionSaved) != 1 {
		t.Fatalf("期望 1 次 session.saved，实际 %d", h.events.count(agent.EventSessionSaved))
	}
	if h.events.count(agent.EventSessionExported) != 1 {
		t.Fatalf("期望 1 次 session.exported，实际 %d", h.events.count(agent.EventSessionExported))
	}
	if h.events.count(agent.EventRunSettled) != 1 {
		t.Fatalf("期望 1 次 run.settled，实际 %d", h.events.count(agent.EventRunSettled))
	}
	if h.events.count(agent.EventRunFailed) != 0 {
		t.Fatal("正常执行不应出现 run.failed")
	}
}

// TestRunUsesNonInteractiveArguments 验证启动参数不使用 RPC，也不使用 --no-session。
func TestRunUsesNonInteractiveArguments(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "normal", pi.Options{})
	if _, err := h.run(context.Background(), t); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	report := h.readReport(t)
	joined := strings.Join(report.Args, " ")
	for _, expected := range []string{"--print", "--session", "--model fake/model-1"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("启动参数缺少 %q: %s", expected, joined)
		}
	}
	for _, forbidden := range []string{"--no-session", "--mode rpc"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("启动参数不应包含 %q: %s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "--append-system-prompt") {
		t.Fatalf("启动参数缺少系统提示词: %s", joined)
	}
	if !sameDir(report.Cwd, h.request.WorkingDir) {
		t.Fatalf("工作目录不符: %s != %s", report.Cwd, h.request.WorkingDir)
	}
}

// TestSessionPathFollowsLayout 验证 Session 路径按任务、步骤与调用标识分层。
func TestSessionPathFollowsLayout(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "normal", pi.Options{})
	h.request.TaskID = "task-1"
	h.request.StepID = "step-2"
	h.request.AttemptID = "attempt-3"
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	expected := filepath.Join(
		h.request.SessionRoot,
		"task-1",
		"step-2",
		"attempt-3",
		"session-under-test",
		"session.jsonl",
	)
	if result.Session.JSONLPath != expected {
		t.Fatalf("Session 路径不符:\n期望 %s\n实际 %s", expected, result.Session.JSONLPath)
	}
}

// TestDifferentRunsUseDifferentSessionFiles 验证不同调用的 Session 路径不冲突。
func TestDifferentRunsUseDifferentSessionFiles(t *testing.T) {
	t.Parallel()
	root := filepath.Join(testkit.Dir(t), "sessions")
	first := newHarness(t, "normal", pi.Options{})
	first.request.SessionRoot = root
	firstResult, err := first.run(context.Background(), t)
	if err != nil {
		t.Fatalf("首次执行失败: %v", err)
	}

	second := newHarness(t, "normal", pi.Options{})
	second.request.SessionRoot = root
	second.request.SessionID = "session-second"
	secondResult, err := second.run(context.Background(), t)
	if err != nil {
		t.Fatalf("二次执行失败: %v", err)
	}
	if firstResult.Session.JSONLPath == secondResult.Session.JSONLPath {
		t.Fatalf("不同调用不应共用 Session 文件: %s", firstResult.Session.JSONLPath)
	}
	if _, statErr := os.Stat(firstResult.Session.JSONLPath); statErr != nil {
		t.Fatalf("首次 Session 文件被覆盖: %v", statErr)
	}
}

// TestNonZeroExitIsFailure 验证非零退出码被判定为失败，但 Session 仍然保留。
func TestNonZeroExitIsFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "exit_nonzero", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err == nil {
		t.Fatal("期望执行失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.ExitCode != 3 {
		t.Fatalf("期望退出码 3，实际 %d", result.ExitCode)
	}
	if !strings.Contains(result.StderrSummary, "模型不可用") {
		t.Fatalf("stderr 摘要缺少诊断信息: %q", result.StderrSummary)
	}
	if h.events.count(agent.EventRunFailed) != 1 {
		t.Fatalf("期望 1 次 run.failed，实际 %d", h.events.count(agent.EventRunFailed))
	}
}

// TestStderrIsCapturedSeparately 验证标准错误与标准输出分开记录。
func TestStderrIsCapturedSeparately(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "stderr_output", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.FinalText != "READY" {
		t.Fatalf("标准输出不应混入诊断信息: %q", result.FinalText)
	}
	if !strings.Contains(result.StderrSummary, "诊断信息") {
		t.Fatalf("stderr 摘要缺少内容: %q", result.StderrSummary)
	}
}

// TestHugeOutputIsBounded 验证超长输出不会无限占用内存。
func TestHugeOutputIsBounded(t *testing.T) {
	t.Parallel()
	limit := 8 << 10
	h := newHarness(t, "huge_output", pi.Options{OutputLimit: limit})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(result.FinalText) > agent.MaxFinalTextLength {
		t.Fatalf("最终文本超过摘要上限: %d", len(result.FinalText))
	}
	if result.FinalText == "" {
		t.Fatal("超长输出应当保留末尾内容")
	}
}

// TestTimeoutIsReportedSeparately 验证超时与取消是不同的结果。
func TestTimeoutIsReportedSeparately(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hang", pi.Options{GraceTimeout: 200 * time.Millisecond})
	h.request.Timeout = 300 * time.Millisecond
	start := time.Now()
	result, err := h.run(context.Background(), t)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望超时错误，实际 %v", err)
	}
	if result.Status != agent.RunTimedOut {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunTimedOut, result.Status)
	}
	if h.events.count(agent.EventRunTimedOut) != 1 {
		t.Fatalf("期望 1 次 run.timed_out，实际 %d", h.events.count(agent.EventRunTimedOut))
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("超时处理耗时过长: %s", elapsed)
	}
}

// TestCancellationIsReportedSeparately 验证调用方取消能够终止进程。
func TestCancellationIsReportedSeparately(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hang", pi.Options{GraceTimeout: 300 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	result, err := h.run(ctx, t)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望取消错误，实际 %v", err)
	}
	if result.Status != agent.RunCancelled {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCancelled, result.Status)
	}
	if h.events.count(agent.EventRunCancelled) != 1 {
		t.Fatalf("期望 1 次 run.cancelled，实际 %d", h.events.count(agent.EventRunCancelled))
	}
	if h.events.count(agent.EventRunTimedOut) != 0 {
		t.Fatal("取消不应产生 run.timed_out")
	}
}

// TestCancelledContextIsNotStarted 验证启动前取消不会创建子进程。
func TestCancelledContextIsNotStarted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "normal", pi.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := h.run(ctx, t)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望取消错误，实际 %v", err)
	}
	if result.Status != agent.RunCancelled {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCancelled, result.Status)
	}
	if _, statErr := os.Stat(h.report); statErr == nil {
		t.Fatal("启动前取消不应创建子进程")
	}
}

// TestMissingSessionIsFailure 验证退出码为 0 但没有 Session 文件时判定为失败。
func TestMissingSessionIsFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "missing_session", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err == nil {
		t.Fatal("缺少 Session 文件时不应报告成功")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.Session.Completeness != agent.SessionMissing {
		t.Fatalf("期望完整性 %s，实际 %s", agent.SessionMissing, result.Session.Completeness)
	}
	if result.Session.JSONLPath != "" {
		t.Fatalf("缺少文件时不应给出路径: %s", result.Session.JSONLPath)
	}
}

// TestInvalidSessionIsPartial 验证内容损坏的 Session 仍然保留并标记为部分可用。
func TestInvalidSessionIsPartial(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "invalid_session", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if result.Session.Completeness != agent.SessionPartial {
		t.Fatalf("期望完整性 %s，实际 %s", agent.SessionPartial, result.Session.Completeness)
	}
	if result.Session.JSONLPath == "" {
		t.Fatal("内容损坏的 Session 仍应保留路径")
	}
	if result.Session.Error == "" {
		t.Fatal("应当记录 Session 校验失败的原因")
	}
	if result.Session.ExportStatus != agent.ExportNotAttempted {
		t.Fatalf("内容损坏时不应尝试导出: %s", result.Session.ExportStatus)
	}
}

// TestPartialSessionAfterAbnormalExit 验证异常退出时保留已有记录。
func TestPartialSessionAfterAbnormalExit(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "partial_session", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err == nil {
		t.Fatal("期望执行失败")
	}
	if result.Session.Completeness != agent.SessionPartial {
		t.Fatalf("期望完整性 %s，实际 %s", agent.SessionPartial, result.Session.Completeness)
	}
	if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
		t.Fatalf("异常退出后仍应保留 Session 文件: %v", statErr)
	}
}

// TestExportFailureKeepsJSONL 验证导出失败不会丢失原始 Session。
func TestExportFailureKeepsJSONL(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "export_failure", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("导出失败不应让执行整体失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if result.Session.Completeness != agent.SessionExportFailed {
		t.Fatalf("期望完整性 %s，实际 %s", agent.SessionExportFailed, result.Session.Completeness)
	}
	if result.Session.ExportStatus != agent.ExportFailed {
		t.Fatalf("期望导出状态 %s，实际 %s", agent.ExportFailed, result.Session.ExportStatus)
	}
	if result.Session.HTMLPath != "" {
		t.Fatalf("导出失败时不应给出 HTML 路径: %s", result.Session.HTMLPath)
	}
	if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
		t.Fatalf("原始 Session 必须保留: %v", statErr)
	}
	if h.events.count(agent.EventSessionExported) != 1 {
		t.Fatalf("期望 1 次 session.exported，实际 %d", h.events.count(agent.EventSessionExported))
	}
}

// TestExportDisabledSkipsHTML 验证关闭导出时不产生 HTML。
func TestExportDisabledSkipsHTML(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "normal", pi.Options{DisableExport: true})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Session.Completeness != agent.SessionComplete {
		t.Fatalf("期望完整性 %s，实际 %s", agent.SessionComplete, result.Session.Completeness)
	}
	if result.Session.ExportStatus != agent.ExportNotAttempted {
		t.Fatalf("期望导出状态 %s，实际 %s", agent.ExportNotAttempted, result.Session.ExportStatus)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(result.Session.JSONLPath), "session.html")); statErr == nil {
		t.Fatal("关闭导出时不应生成 HTML")
	}
}

// TestStartupFailureIsReported 验证启动阶段失败被正确报告。
func TestStartupFailureIsReported(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "startup_failure", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err == nil {
		t.Fatal("期望执行失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.ExitCode != 7 {
		t.Fatalf("期望退出码 7，实际 %d", result.ExitCode)
	}
	if !strings.Contains(result.StderrSummary, "启动失败") {
		t.Fatalf("stderr 摘要缺少诊断信息: %q", result.StderrSummary)
	}
}

// TestMissingExecutableFails 验证 Pi 不可用时返回明确错误。
func TestMissingExecutableFails(t *testing.T) {
	t.Parallel()
	runner := pi.NewRunner(pi.Options{Executable: "agentflow-not-installed-pi"})
	result, err := runner.Run(context.Background(), agent.RunRequest{
		SessionID:   "session-missing",
		Model:       "fake/model-1",
		Prompt:      "只回答 READY",
		SessionRoot: testkit.Dir(t),
	}, nil)
	if err == nil {
		t.Fatal("期望执行失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
}

// TestInvalidRequestIsRejected 验证缺少必需字段的请求被拒绝。
func TestInvalidRequestIsRejected(t *testing.T) {
	t.Parallel()
	runner := pi.NewRunner(pi.Options{})
	result, err := runner.Run(context.Background(), agent.RunRequest{SessionID: "session"}, nil)
	if err == nil {
		t.Fatal("期望参数校验失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
}

// TestNilEmitIsAllowed 验证回调为空时执行仍然可用。
func TestNilEmitIsAllowed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "normal", pi.Options{})
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
}

// TestEventsBelongToTheirSession 验证事件归属正确的会话。
func TestEventsBelongToTheirSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "normal", pi.Options{})
	if _, err := h.run(context.Background(), t); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	ids := h.events.sessionIDs()
	if len(ids) != 1 {
		t.Fatalf("事件归属出现多个会话: %v", ids)
	}
	if _, ok := ids["session-under-test"]; !ok {
		t.Fatalf("事件未归属到本次会话: %v", ids)
	}
}

// TestStaleSessionFileIsReplaced 验证旧的同名 Session 文件不会污染本次结果。
func TestStaleSessionFileIsReplaced(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "normal", pi.Options{})
	paths, err := pi.SessionLayout{SessionID: h.request.SessionID}.Paths(h.request.SessionRoot)
	if err != nil {
		t.Fatalf("解析 Session 路径失败: %v", err)
	}
	if err := paths.EnsureDir(); err != nil {
		t.Fatalf("创建 Session 目录失败: %v", err)
	}
	if err := os.WriteFile(paths.JSONL, []byte(`{"type":"session","stale":true}`+"\n"), 0o600); err != nil {
		t.Fatalf("写入旧 Session 文件失败: %v", err)
	}

	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	data, readErr := os.ReadFile(result.Session.JSONLPath)
	if readErr != nil {
		t.Fatalf("读取 Session 文件失败: %v", readErr)
	}
	if strings.Contains(string(data), "stale") {
		t.Fatal("旧的同名 Session 文件应当被清理")
	}
}

// sameDir 比较两个目录是否等价。
func sameDir(left, right string) bool {
	if left == right {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return os.SameFile(leftInfo, rightInfo)
}
