package pi_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestConcurrentRunsUseIndependentSessions 验证并发执行互不覆盖 Session 与输出。
func TestConcurrentRunsUseIndependentSessions(t *testing.T) {
	t.Parallel()
	const total = 4
	root := filepath.Join(testkit.Dir(t), "sessions")
	harnesses := make([]*harness, 0, total)
	for index := 0; index < total; index++ {
		h := newHarness(t, "normal", pi.Options{})
		h.request.SessionRoot = root
		h.request.SessionID = "session-concurrent-" + string(rune('a'+index))
		harnesses = append(harnesses, h)
	}

	results := make([]agent.RunResult, total)
	errs := make([]error, total)
	var group sync.WaitGroup
	for index := 0; index < total; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errs[index] = harnesses[index].run(context.Background(), t)
		}(index)
	}
	group.Wait()

	paths := make(map[string]struct{}, total)
	for index := 0; index < total; index++ {
		if errs[index] != nil {
			t.Fatalf("并发执行 %d 失败: %v", index, errs[index])
		}
		if results[index].FinalText != "READY" {
			t.Fatalf("并发执行 %d 输出不符: %q", index, results[index].FinalText)
		}
		if results[index].Session.Completeness != agent.SessionComplete {
			t.Fatalf("并发执行 %d Session 不完整: %s", index, results[index].Session.Completeness)
		}
		path := results[index].Session.JSONLPath
		if _, duplicated := paths[path]; duplicated {
			t.Fatalf("并发执行复用了同一个 Session 文件: %s", path)
		}
		paths[path] = struct{}{}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("并发执行 %d 的 Session 文件不存在: %v", index, statErr)
		}
	}
	if len(paths) != total {
		t.Fatalf("Session 文件数量不符: %d", len(paths))
	}
}

// TestCancellingOneRunKeepsOthersRunning 验证取消一个调用不会影响其他并发调用。
func TestCancellingOneRunKeepsOthersRunning(t *testing.T) {
	t.Parallel()
	cancelled := newHarness(t, "hang", pi.Options{})
	healthy := newHarness(t, "normal", pi.Options{})

	cancelCtx, cancel := context.WithCancel(context.Background())
	var group sync.WaitGroup
	group.Add(2)
	var cancelledResult agent.RunResult
	var cancelledErr error
	var healthyResult agent.RunResult
	var healthyErr error
	go func() {
		defer group.Done()
		cancelledResult, cancelledErr = cancelled.run(cancelCtx, t)
	}()
	go func() {
		defer group.Done()
		healthyResult, healthyErr = healthy.run(context.Background(), t)
	}()
	// 等待被取消的调用真正启动后再取消，避免取消发生在启动之前。
	waitForEvent(t, cancelled.events, agent.EventRunStarted)
	cancel()
	group.Wait()

	if cancelledErr == nil {
		t.Fatal("被取消的调用应当返回错误")
	}
	if cancelledResult.Status != agent.RunCancelled {
		t.Fatalf("被取消的调用状态不符: %s", cancelledResult.Status)
	}
	if healthyErr != nil {
		t.Fatalf("另一个调用不应受影响: %v", healthyErr)
	}
	if healthyResult.Status != agent.RunCompleted {
		t.Fatalf("另一个调用应当正常完成: %s", healthyResult.Status)
	}
	if healthyResult.FinalText != "READY" {
		t.Fatalf("另一个调用输出不符: %q", healthyResult.FinalText)
	}
}

// waitForEvent 在有上限的时间内等待指定类型的事件出现。
func waitForEvent(t *testing.T, recorder *eventRecorder, eventType agent.EventType) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if recorder.count(eventType) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待事件 %s 超时", eventType)
}
