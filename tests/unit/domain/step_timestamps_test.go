package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// TestStepStartsWithoutTimestamps 校验新建待执行步骤没有起止时间。
//
// 计划生成只创建步骤，不做任何执行：此时两个时间字段必须为空，否则界面会把
// "计划已就绪"误读为"步骤已开始"。
func TestStepStartsWithoutTimestamps(t *testing.T) {
	t.Parallel()
	step := newStepForTest(t)
	if step.Status != domain.StepStatusPending {
		t.Fatalf("新建步骤应为 pending，实际 %s", step.Status)
	}
	if step.StartedAt != nil || step.FinishedAt != nil {
		t.Fatalf("新建待执行步骤不应有起止时间: %v / %v", step.StartedAt, step.FinishedAt)
	}
}

// TestStepStartTimeIsRecordedOnlyOnRealExecution 校验只有真正进入执行才记录开始时间。
//
// running 与 developing 可以直接从 pending 到达；verifying 只能从执行占用状态
// 到达，因此它验证的是"进入验证阶段不会刷新已经记录的开始时间"。
func TestStepStartTimeIsRecordedOnlyOnRealExecution(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, next := range []domain.StepStatus{
		domain.StepStatusRunning,
		domain.StepStatusDeveloping,
	} {
		step := newStepForTest(t)
		if err := step.Transition(next, now); err != nil {
			t.Fatalf("迁移到 %s 失败: %v", next, err)
		}
		if step.StartedAt == nil {
			t.Fatalf("迁移到 %s 时必须记录开始时间", next)
		}
		if !step.StartedAt.Equal(now) {
			t.Fatalf("开始时间不符: 期望 %s，实际 %s", now, step.StartedAt)
		}
		if step.FinishedAt != nil {
			t.Fatalf("迁移到 %s 时不应记录结束时间", next)
		}
	}

	// 进入 verifying：开始时间保持在首次真正执行时写入的值。
	step := newStepForTest(t)
	if err := step.Transition(domain.StepStatusRunning, now); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	later := now.Add(3 * time.Second)
	if err := step.Transition(domain.StepStatusVerifying, later); err != nil {
		t.Fatalf("迁移到 verifying 失败: %v", err)
	}
	if step.StartedAt == nil || !step.StartedAt.Equal(now) {
		t.Fatalf("进入验证阶段不应刷新开始时间: %v", step.StartedAt)
	}
	if step.FinishedAt != nil {
		t.Fatalf("进入验证阶段不应记录结束时间: %v", step.FinishedAt)
	}
}

// TestStepSkippedAndCancelledFromPendingHaveNoStartTime 校验未开始的步骤被跳过或
// 直接取消时不产生开始时间。
//
// skipped 与 cancelled 都可以从 pending 直接到达：它们表达"这一步没有执行"，
// 因此写入 started_at 会让界面误报"曾经开始执行"。
func TestStepSkippedAndCancelledFromPendingHaveNoStartTime(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for _, next := range []domain.StepStatus{
		domain.StepStatusSkipped,
		domain.StepStatusCancelled,
	} {
		step := newStepForTest(t)
		if err := step.Transition(next, now); err != nil {
			t.Fatalf("迁移到 %s 失败: %v", next, err)
		}
		if step.StartedAt != nil {
			t.Fatalf("未执行的步骤迁移到 %s 时不应记录开始时间: %v", next, step.StartedAt)
		}
		if step.FinishedAt == nil {
			t.Fatalf("迁移到 %s 时必须记录结束时间", next)
		}
	}
}

// TestStepFinishedAtRecordedWhenCycleEnds 校验执行周期结束时记录结束时间。
func TestStepFinishedAtRecordedWhenCycleEnds(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, status := range []domain.StepStatus{
		domain.StepStatusCompleted,
		domain.StepStatusFailed,
		domain.StepStatusRetryWait,
		domain.StepStatusRecoveryRequired,
		domain.StepStatusCancelled,
	} {
		step := newStepForTest(t)
		if err := step.Transition(domain.StepStatusRunning, now); err != nil {
			t.Fatalf("启动步骤失败: %v", err)
		}
		at := now.Add(2 * time.Second)
		if err := step.Transition(status, at); err != nil {
			t.Fatalf("迁移到 %s 失败: %v", status, err)
		}
		if step.FinishedAt == nil {
			t.Fatalf("迁移到 %s 时必须记录结束时间", status)
		}
		if !step.FinishedAt.Equal(at) {
			t.Fatalf("结束时间不符: 期望 %s，实际 %s", at, step.FinishedAt)
		}
		if step.StartedAt == nil || !step.StartedAt.Equal(now) {
			t.Fatalf("首次开始时间不应被覆盖: %v", step.StartedAt)
		}
	}
}

// TestStepReExecutionKeepsFirstStartAndClearsFinish 校验重新执行开启新执行周期：
// 保留首次开始时间，清空上一轮的结束时间。
//
// 这样 started_at 表达"这一步第一次真正开始"，finished_at 表达"当前执行周期何时
// 结束"。若保留旧的 finished_at，页面会在仍在执行时显示一个虚构的完成时间；
// 若覆盖 started_at，则首次进入执行的历史事实会丢失。
func TestStepReExecutionKeepsFirstStartAndClearsFinish(t *testing.T) {
	t.Parallel()
	first := time.Now().UTC().Truncate(time.Millisecond)
	step := newStepForTest(t)
	if err := step.Transition(domain.StepStatusRunning, first); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusFailed, first.Add(time.Second)); err != nil {
		t.Fatalf("标记步骤失败: %v", err)
	}
	if step.FinishedAt == nil {
		t.Fatal("失败时必须记录结束时间")
	}
	// 显式重新执行：进入第二个执行周期。
	second := first.Add(time.Minute)
	if err := step.Transition(domain.StepStatusRunning, second); err != nil {
		t.Fatalf("重新启动步骤失败: %v", err)
	}
	if step.StartedAt == nil || !step.StartedAt.Equal(first) {
		t.Fatalf("重新执行不应覆盖首次开始时间: %v", step.StartedAt)
	}
	if step.FinishedAt != nil {
		t.Fatalf("重新执行后不应保留上一周期的结束时间: %v", step.FinishedAt)
	}
}

// TestStepSameStatusTransitionKeepsTimestamps 校验重复写入同一状态不会被接受，
// 因此也不会伪造或清空时间。
//
// 步骤状态机不接受 running → running 这类原地迁移：重复迁移必须先被视为非法，
// 才能保证 started_at / finished_at 不会被一次重复调用刷新。
func TestStepSameStatusTransitionKeepsTimestamps(t *testing.T) {
	t.Parallel()
	start := time.Now().UTC().Truncate(time.Millisecond)
	step := newStepForTest(t)
	if err := step.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusRunning, start.Add(time.Minute)); !errors.Is(
		err,
		domain.ErrInvalidTransition,
	) {
		t.Fatalf("同一状态的重复迁移应被拒绝，实际: %v", err)
	}
	if !step.StartedAt.Equal(start) {
		t.Fatalf("被拒绝的重复迁移不应刷新开始时间: %v", step.StartedAt)
	}
	if err := step.Transition(domain.StepStatusCompleted, start.Add(2*time.Minute)); err != nil {
		t.Fatalf("完成步骤失败: %v", err)
	}
	finished := *step.FinishedAt
	if err := step.Transition(domain.StepStatusCompleted, start.Add(time.Hour)); !errors.Is(
		err,
		domain.ErrInvalidTransition,
	) {
		t.Fatalf("终态的重复迁移应被拒绝，实际: %v", err)
	}
	if !step.FinishedAt.Equal(finished) {
		t.Fatalf("被拒绝的重复迁移不应改写结束时间: %v", step.FinishedAt)
	}
}

// TestStepStatusTimeSemantics 校验状态与时间语义的映射本身。
func TestStepStatusTimeSemantics(t *testing.T) {
	t.Parallel()
	for _, status := range []domain.StepStatus{
		domain.StepStatusRunning,
		domain.StepStatusDeveloping,
		domain.StepStatusVerifying,
	} {
		if !status.BeginsExecution() {
			t.Fatalf("%s 应表示开始执行", status)
		}
		if status.EndsExecution() {
			t.Fatalf("%s 不应同时表示执行周期结束", status)
		}
	}
	for _, status := range []domain.StepStatus{
		domain.StepStatusCompleted,
		domain.StepStatusSkipped,
		domain.StepStatusCancelled,
		domain.StepStatusFailed,
		domain.StepStatusRetryWait,
		domain.StepStatusRecoveryRequired,
	} {
		if !status.EndsExecution() {
			t.Fatalf("%s 应表示执行周期结束", status)
		}
		if status.BeginsExecution() {
			t.Fatalf("%s 不应同时表示开始执行", status)
		}
	}
	for _, status := range []domain.StepStatus{domain.StepStatusPending, ""} {
		if status.BeginsExecution() || status.EndsExecution() {
			t.Fatalf("%q 不代表任何执行边界", status)
		}
	}
}
