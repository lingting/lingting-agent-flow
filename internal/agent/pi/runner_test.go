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
)

// TestMain 让测试二进制同时充当假 Pi 子进程。
func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakePiEnv); scenario != "" {
		os.Exit(runFakePi())
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
	report := filepath.Join(t.TempDir(), "report.json")
	options.Executable = executable
	options.Env = append(os.Environ(),
		fakePiEnv+"="+scenario,
		fakePiReportEnv+"="+report,
	)
	if options.GraceTimeout <= 0 {
		options.GraceTimeout = 2 * time.Second
	}
	if options.ResponseTimeout <= 0 {
		options.ResponseTimeout = 10 * time.Second
	}
	return &harness{
		runner: pi.NewRunner(options),
		request: agent.RunRequest{
			SessionID:  "session-under-test",
			Model:      "fake/model-1",
			Prompt:     "只回答 READY",
			WorkingDir: t.TempDir(),
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
func (h *harness) readReport(t *testing.T) fakeReport {
	t.Helper()
	data, err := os.ReadFile(h.report)
	if err != nil {
		t.Fatalf("读取启动报告失败: %v", err)
	}
	var report fakeReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("解析启动报告失败: %v", err)
	}
	return report
}

// TestRunCompletesNormally 验证正常执行链路与事件映射。
func TestRunCompletesNormally(t *testing.T) {
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
	if !strings.HasPrefix(result.ExternalSessionID, "pi-session-") {
		t.Fatalf("外部会话标识不符: %s", result.ExternalSessionID)
	}
	if result.FinalText != "READY" {
		t.Fatalf("最终文本不符: %q", result.FinalText)
	}
	if result.ExitCode != 0 {
		t.Fatalf("期望退出码 0，实际 %d", result.ExitCode)
	}
	if result.StartedAt.IsZero() || result.FinishedAt.IsZero() {
		t.Fatalf("开始与结束时间不能为空")
	}
	if h.events.count(agent.EventRunStarted) != 1 {
		t.Fatalf("期望 1 次 run.started，实际 %d", h.events.count(agent.EventRunStarted))
	}
	if h.events.count(agent.EventMessageDelta) != 1 {
		t.Fatalf("期望 1 次 message.delta，实际 %d", h.events.count(agent.EventMessageDelta))
	}
	if h.events.count(agent.EventMessageCompleted) != 1 {
		t.Fatalf("期望 1 次 message.completed，实际 %d", h.events.count(agent.EventMessageCompleted))
	}
	if h.events.count(agent.EventRunSettled) != 1 {
		t.Fatalf("期望 1 次 run.settled，实际 %d", h.events.count(agent.EventRunSettled))
	}
	if h.events.count(agent.EventRunFailed) != 0 {
		t.Fatalf("正常执行不应出现 run.failed")
	}
}

// TestRunUsesModelAndWorkingDir 验证启动参数与工作目录来自请求。
func TestRunUsesModelAndWorkingDir(t *testing.T) {
	h := newHarness(t, "normal", pi.Options{})
	if _, err := h.run(context.Background(), t); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	report := h.readReport(t)
	joined := strings.Join(report.Args, " ")
	for _, expected := range []string{"--mode rpc", "--no-session", "--model fake/model-1"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("启动参数缺少 %q: %s", expected, joined)
		}
	}
	if !sameDir(report.Cwd, h.request.WorkingDir) {
		t.Fatalf("工作目录不符: %s != %s", report.Cwd, h.request.WorkingDir)
	}
}

// TestEachRunUsesIndependentProcess 验证每次执行创建独立进程。
func TestEachRunUsesIndependentProcess(t *testing.T) {
	h := newHarness(t, "normal", pi.Options{})
	first, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("首次执行失败: %v", err)
	}
	second, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("二次执行失败: %v", err)
	}
	if first.ExternalSessionID == second.ExternalSessionID {
		t.Fatalf("两次执行不应复用同一个 Pi 进程: %s", first.ExternalSessionID)
	}
}

// TestEventsBelongToTheirSession 验证事件归属正确的会话。
func TestEventsBelongToTheirSession(t *testing.T) {
	first := newHarness(t, "normal", pi.Options{})
	if _, err := first.run(context.Background(), t); err != nil {
		t.Fatalf("首次执行失败: %v", err)
	}
	second := newHarness(t, "normal", pi.Options{})
	second.request.SessionID = "session-second"
	if _, err := second.run(context.Background(), t); err != nil {
		t.Fatalf("二次执行失败: %v", err)
	}
	ids := second.events.sessionIDs()
	if len(ids) != 1 {
		t.Fatalf("事件归属出现多个会话: %v", ids)
	}
	if _, ok := ids["session-second"]; !ok {
		t.Fatalf("事件未归属到本次会话: %v", ids)
	}
}

// TestInstantCompletionIsNotMissed 验证响应与完成事件同时到达时不丢失事件。
func TestInstantCompletionIsNotMissed(t *testing.T) {
	h := newHarness(t, "instant", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if h.events.count(agent.EventRunSettled) != 1 {
		t.Fatalf("期望收到 run.settled")
	}
}

// TestAgentEndDoesNotFinishRun 验证只有 agent_settled 才结束本轮执行。
func TestAgentEndDoesNotFinishRun(t *testing.T) {
	h := newHarness(t, "agent_end", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if result.FinalText != "READY" {
		t.Fatalf("agent_end 之后的消息不应覆盖最终文本: %q", result.FinalText)
	}
	if h.events.count(agent.EventMessageDelta) != 2 {
		t.Fatalf("agent_end 之后的事件仍应被消费，实际 %d 条增量", h.events.count(agent.EventMessageDelta))
	}
}

// TestAbortedSettledMarksCancelled 验证 agent_settled 的中止标志决定状态。
func TestAbortedSettledMarksCancelled(t *testing.T) {
	h := newHarness(t, "aborted", pi.Options{})
	result, err := h.run(context.Background(), t)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望取消错误，实际 %v", err)
	}
	if result.Status != agent.RunCancelled {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCancelled, result.Status)
	}
	if h.events.count(agent.EventRunCancelled) != 1 {
		t.Fatalf("期望 1 次 run.cancelled，实际 %d", h.events.count(agent.EventRunCancelled))
	}
}

// TestProviderErrorWithZeroExitIsFailure 验证退出码与事件流不一致时以事件为准。
func TestProviderErrorWithZeroExitIsFailure(t *testing.T) {
	h := newHarness(t, "provider_error", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err == nil {
		t.Fatalf("期望执行失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.ExitCode != 0 {
		t.Fatalf("期望退出码 0，实际 %d", result.ExitCode)
	}
	if h.events.count(agent.EventRunFailed) != 1 {
		t.Fatalf("期望 1 次 run.failed，实际 %d", h.events.count(agent.EventRunFailed))
	}
}

// TestNonZeroExitIsFailure 验证进程异常退出被判定为失败。
func TestNonZeroExitIsFailure(t *testing.T) {
	h := newHarness(t, "exit_nonzero", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err == nil {
		t.Fatalf("期望执行失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.ExitCode != 3 {
		t.Fatalf("期望退出码 3，实际 %d", result.ExitCode)
	}
}

// TestStartupFailureIsFailure 验证启动阶段失败被正确报告。
func TestStartupFailureIsFailure(t *testing.T) {
	h := newHarness(t, "startup_failure", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err == nil {
		t.Fatalf("期望执行失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.ExitCode != 7 {
		t.Fatalf("期望退出码 7，实际 %d", result.ExitCode)
	}
}

// TestInvalidJSONIsProtocolError 验证非法 JSON 被识别为协议错误。
func TestInvalidJSONIsProtocolError(t *testing.T) {
	h := newHarness(t, "invalid_json", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err == nil {
		t.Fatalf("期望执行失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
}

// TestHandledPromptDoesNotWaitForSettled 验证被消费的提示词不会等待 agent_settled。
func TestHandledPromptDoesNotWaitForSettled(t *testing.T) {
	h := newHarness(t, "handled", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if h.events.count(agent.EventRunSettled) != 0 {
		t.Fatalf("被消费的提示词不应收到 run.settled")
	}
}

// TestUnknownEventsAreIgnored 验证未识别的事件不会中断执行。
func TestUnknownEventsAreIgnored(t *testing.T) {
	h := newHarness(t, "unknown", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
}

// TestFragmentedCRLFOutputIsParsed 验证 CRLF、分片与末尾无换行的记录。
func TestFragmentedCRLFOutputIsParsed(t *testing.T) {
	h := newHarness(t, "fragmented", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if result.FinalText != "READY" {
		t.Fatalf("最终文本不符: %q", result.FinalText)
	}
}

// TestLongRecordIsAccepted 验证较长的 JSONL 记录不会被截断。
func TestLongRecordIsAccepted(t *testing.T) {
	h := newHarness(t, "long", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if result.FinalText != "READY" {
		t.Fatalf("最终文本不符: %q", result.FinalText)
	}
	if h.events.count(agent.EventMessageDelta) != 1 {
		t.Fatalf("长记录应产生 1 条增量事件")
	}
}

// TestContextCancellationIsHandled 验证执行期间取消能够终止进程。
func TestContextCancellationIsHandled(t *testing.T) {
	h := newHarness(t, "hang", pi.Options{GraceTimeout: 500 * time.Millisecond})
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
}

// TestCancelledContextIsNotStarted 验证启动前取消不会启动进程。
func TestCancelledContextIsNotStarted(t *testing.T) {
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
		t.Fatalf("启动前取消不应创建子进程")
	}
}

// TestPreviousCancellationDoesNotAffectNextRun 验证取消不会污染后续执行。
func TestPreviousCancellationDoesNotAffectNextRun(t *testing.T) {
	h := newHarness(t, "hang", pi.Options{GraceTimeout: 300 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	if _, err := h.run(ctx, t); err == nil {
		t.Fatalf("期望首次执行被取消")
	}

	next := newHarness(t, "normal", pi.Options{})
	next.request.SessionID = "session-after-cancel"
	result, err := next.run(context.Background(), t)
	if err != nil {
		t.Fatalf("取消后的执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
}

// TestGraceTimeoutTerminatesProcess 验证宽限时间耗尽后强制终止并回收进程。
func TestGraceTimeoutTerminatesProcess(t *testing.T) {
	h := newHarness(t, "hang", pi.Options{GraceTimeout: 200 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := h.run(ctx, t)
	if err == nil {
		t.Fatalf("期望执行被取消")
	}
	if result.Status != agent.RunCancelled {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCancelled, result.Status)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("取消耗时过长: %s", elapsed)
	}
}

// TestMissingExecutableFails 验证 Pi 不可用时返回明确错误。
func TestMissingExecutableFails(t *testing.T) {
	runner := pi.NewRunner(pi.Options{Executable: "agentflow-not-installed-pi"})
	result, err := runner.Run(context.Background(), agent.RunRequest{
		SessionID: "session-missing",
		Model:     "fake/model-1",
		Prompt:    "只回答 READY",
	}, nil)
	if err == nil {
		t.Fatalf("期望执行失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
}

// TestInvalidRequestIsRejected 验证缺少必需字段的请求被拒绝。
func TestInvalidRequestIsRejected(t *testing.T) {
	runner := pi.NewRunner(pi.Options{})
	result, err := runner.Run(context.Background(), agent.RunRequest{SessionID: "session"}, nil)
	if err == nil {
		t.Fatalf("期望参数校验失败")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
}

// TestNilEmitIsAllowed 验证回调为空时执行仍然可用。
func TestNilEmitIsAllowed(t *testing.T) {
	h := newHarness(t, "normal", pi.Options{})
	result, err := h.runner.Run(context.Background(), h.request, nil)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
}

// TestCheckReportsCapabilities 验证检查命令能识别 RPC 模式与握手。
func TestCheckReportsCapabilities(t *testing.T) {
	h := newHarness(t, "normal", pi.Options{})
	report, err := pi.Check(context.Background(), pi.Options{Executable: h.executable, Env: h.env})
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if report.Executable == "" || report.Version == "" {
		t.Fatalf("检查结果缺少可执行程序或版本: %+v", report)
	}
	if !report.RPCModeSupported || !report.StateHandshake {
		t.Fatalf("检查结果不符合预期: %+v", report)
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
