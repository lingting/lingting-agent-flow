package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestStepTimestampsStartEmpty 校验新建步骤在数据库中的起止时间为空。
//
// 计划生成只写 pending 步骤：此时 started_at / finished_at 必须是 NULL，不能被
// created_at 之类的字段填满，否则界面会把"计划已就绪"显示成"已开始执行"。
func TestStepTimestampsStartEmpty(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	loaded, err := db.GetStep(testContext(t), built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.StartedAt != nil || loaded.FinishedAt != nil {
		t.Fatalf("新建步骤不应有起止时间: %v / %v", loaded.StartedAt, loaded.FinishedAt)
	}
}

// TestStepStartedAtPersists 校验首次进入执行时 started_at 被真实持久化。
func TestStepStartedAtPersists(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	start := time.Now().UTC().Truncate(time.Millisecond)
	step := built.step
	if err := step.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := db.UpdateStepIfStatus(ctx, step, []domain.StepStatus{domain.StepStatusPending}); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.StartedAt == nil || !loaded.StartedAt.Equal(start) {
		t.Fatalf("开始时间未按毫秒精度持久化: %v（期望 %v）", loaded.StartedAt, start)
	}
	if loaded.FinishedAt != nil {
		t.Fatalf("刚开始执行的步骤不应有结束时间: %v", loaded.FinishedAt)
	}
}

// TestStepFinishedAtPersists 校验步骤结束时 finished_at 被真实持久化，且保留开始时间。
func TestStepFinishedAtPersists(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	start := time.Now().UTC().Truncate(time.Millisecond)
	finish := start.Add(90 * time.Second)
	step := built.step
	if err := step.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := db.UpdateStepIfStatus(ctx, step, []domain.StepStatus{domain.StepStatusPending}); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusCompleted, finish); err != nil {
		t.Fatalf("完成步骤失败: %v", err)
	}
	if err := db.UpdateStepIfStatus(ctx, step, []domain.StepStatus{domain.StepStatusRunning}); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.StartedAt == nil || !loaded.StartedAt.Equal(start) {
		t.Fatalf("开始时间应当保留: %v", loaded.StartedAt)
	}
	if loaded.FinishedAt == nil || !loaded.FinishedAt.Equal(finish) {
		t.Fatalf("结束时间未按毫秒精度持久化: %v（期望 %v）", loaded.FinishedAt, finish)
	}
}

// TestStepFailureAndRecoveryTimestampsPersist 校验失败与待核验状态的结束时间同样落库。
//
// 这两种状态允许重新执行，因此必须记录结束时间，使页面能如实显示"这一轮何时停止"，
// 而不是把一个仍在进行中的步骤显示成成功。
func TestStepFailureAndRecoveryTimestampsPersist(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	start := time.Now().UTC().Truncate(time.Millisecond)
	finish := start.Add(time.Minute)
	step := built.step
	if err := step.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := db.UpdateStepIfStatus(ctx, step, []domain.StepStatus{domain.StepStatusPending}); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	for _, next := range []domain.StepStatus{
		domain.StepStatusFailed,
		domain.StepStatusRecoveryRequired,
	} {
		running, err := db.GetStep(ctx, built.step.ID)
		if err != nil {
			t.Fatalf("读取步骤失败: %v", err)
		}
		if running.Status != domain.StepStatusRunning {
			previous := running.Status
			if err := running.Transition(domain.StepStatusRunning, start); err != nil {
				t.Fatalf("恢复执行状态失败: %v", err)
			}
			if err := db.UpdateStepIfStatus(
				ctx,
				*running,
				[]domain.StepStatus{previous},
			); err != nil {
				t.Fatalf("更新步骤失败: %v", err)
			}
			running, err = db.GetStep(ctx, built.step.ID)
			if err != nil {
				t.Fatalf("读取步骤失败: %v", err)
			}
		}
		previous := running.Status
		if err := running.Transition(next, finish); err != nil {
			t.Fatalf("迁移到 %s 失败: %v", next, err)
		}
		if err := db.UpdateStepIfStatus(ctx, *running, []domain.StepStatus{previous}); err != nil {
			t.Fatalf("写入步骤 %s 失败: %v", next, err)
		}
		loaded, err := db.GetStep(ctx, built.step.ID)
		if err != nil {
			t.Fatalf("读取步骤失败: %v", err)
		}
		if loaded.Status != next {
			t.Fatalf("步骤状态应为 %s，实际 %s", next, loaded.Status)
		}
		if loaded.StartedAt == nil || !loaded.StartedAt.Equal(start) {
			t.Fatalf("%s 状态下开始时间应保留: %v", next, loaded.StartedAt)
		}
		if loaded.FinishedAt == nil || !loaded.FinishedAt.Equal(finish) {
			t.Fatalf("%s 状态下结束时间应落库: %v", next, loaded.FinishedAt)
		}
	}
}

// TestStepReExecutionClearsFinishedAtInDatabase 校验重新执行时清空上一周期结束时间。
//
// 重新执行必须开启新的执行周期：started_at 保留首次真正执行的时间，finished_at
// 回到空值，避免仍在执行的步骤在数据库中带着上一轮的结束时间。
func TestStepReExecutionClearsFinishedAtInDatabase(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	first := time.Now().UTC().Truncate(time.Millisecond)
	step := built.step
	if err := step.Transition(domain.StepStatusRunning, first); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := db.UpdateStepIfStatus(ctx, step, []domain.StepStatus{domain.StepStatusPending}); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusFailed, first.Add(time.Second)); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	if err := db.UpdateStepIfStatus(ctx, step, []domain.StepStatus{domain.StepStatusRunning}); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	// 显式重新执行。
	if err := step.Transition(domain.StepStatusRunning, first.Add(time.Minute)); err != nil {
		t.Fatalf("重新启动步骤失败: %v", err)
	}
	if err := db.UpdateStepIfStatus(ctx, step, []domain.StepStatus{domain.StepStatusFailed}); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.StartedAt == nil || !loaded.StartedAt.Equal(first) {
		t.Fatalf("重新执行后首次开始时间应保留: %v", loaded.StartedAt)
	}
	if loaded.FinishedAt != nil {
		t.Fatalf("重新执行后结束时间应为空: %v", loaded.FinishedAt)
	}
}

// TestStepUpdateWithoutTimestampsKeepsThem 校验只更新其他字段时不会清空已有时间。
//
// 步骤的写入路径都会整行写回：时间字段必须随实体一起写回，否则「补写失败摘要」
// 这类只改说明的更新会把已确认的起止时间抹掉。
func TestStepUpdateWithoutTimestampsKeepsThem(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	start := time.Now().UTC().Truncate(time.Millisecond)
	step := built.step
	if err := step.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := db.UpdateStepIfStatus(ctx, step, []domain.StepStatus{domain.StepStatusPending}); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	// 只改说明字段，不改状态与时间。
	loaded.Result = "补充结果说明"
	if err := db.UpdateStep(ctx, *loaded); err != nil {
		t.Fatalf("更新步骤失败: %v", err)
	}
	again, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if again.StartedAt == nil || !again.StartedAt.Equal(start) {
		t.Fatalf("只改说明字段不应清空开始时间: %v", again.StartedAt)
	}
	if again.Result != "补充结果说明" {
		t.Fatalf("说明字段未写入: %q", again.Result)
	}
}

// TestStepTimestampsSurviveUnknownStatus 校验历史记录中的未知状态不会被时间写入改写。
func TestStepTimestampsSurviveUnknownStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	built := newFixture(t, db)
	// 模拟历史数据：状态为未知取值，起止时间确实缺失。
	testkit.MustExec(
		t,
		rawDB(t, db),
		`UPDATE steps SET status = 'awaiting_review' WHERE id = ?`,
		built.step.ID.String(),
	)
	loaded, err := db.GetStep(testContext(t), built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.Status != domain.StepStatus("awaiting_review") {
		t.Fatalf("未知状态应原样保留: %s", loaded.Status)
	}
	// 读取侧绝不伪造时间：历史缺失值保持为空。
	if loaded.StartedAt != nil || loaded.FinishedAt != nil {
		t.Fatalf("历史缺失的时间不应被推断出来: %v / %v", loaded.StartedAt, loaded.FinishedAt)
	}
}

// TestBeginStepExecutionWritesStartedAt 校验并发受保护的占用路径同时写入开始时间。
//
// 占用是"步骤真正开始执行"的唯一跨进程落点：开始时间必须与状态、尝试、任务推进
// 在同一个事务内写入，不能依赖调用方事后补写。
func TestBeginStepExecutionWritesStartedAt(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	// BeginStepExecution 要求任务处于可执行状态。
	task, err := db.GetTask(ctx, built.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	advanceTaskToDeveloping(t, ctx, db, task)
	at := time.Now().UTC().Truncate(time.Millisecond)
	started, err := db.BeginStepExecution(ctx, storage.StepExecutionStartRequest{
		TaskID:             task.ID,
		StepID:             built.step.ID,
		ExpectedTaskStatus: task.Status,
		ExpectedStepStatus: domain.StepStatusPending,
		At:                 at,
	})
	if err != nil {
		t.Fatalf("占用步骤失败: %v", err)
	}
	if started.Step.StartedAt == nil || !started.Step.StartedAt.Equal(at) {
		t.Fatalf("占用返回的步骤应带开始时间: %v", started.Step.StartedAt)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.Status != domain.StepStatusRunning {
		t.Fatalf("步骤应为 running，实际 %s", loaded.Status)
	}
	if loaded.StartedAt == nil || !loaded.StartedAt.Equal(at) {
		t.Fatalf("占用必须把开始时间真实落库: %v", loaded.StartedAt)
	}
	if loaded.FinishedAt != nil {
		t.Fatalf("刚开始执行时不应有结束时间: %v", loaded.FinishedAt)
	}
	if loaded.CurrentAttemptID == nil || *loaded.CurrentAttemptID != started.Attempt.ID {
		t.Fatalf("占用必须记录当前尝试: %v", loaded.CurrentAttemptID)
	}
}

// TestBeginStepExecutionRejectsSecondOccupancy 校验重复占用仍被拒绝。
//
// 时间写入不能削弱既有的执行权约束：第二次占用必须返回冲突，且不改变第一次写入的
// 开始时间。
func TestBeginStepExecutionRejectsSecondOccupancy(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	task, err := db.GetTask(ctx, built.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	advanceTaskToDeveloping(t, ctx, db, task)
	at := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := db.BeginStepExecution(ctx, storage.StepExecutionStartRequest{
		TaskID:             task.ID,
		StepID:             built.step.ID,
		ExpectedTaskStatus: task.Status,
		ExpectedStepStatus: domain.StepStatusPending,
		At:                 at,
	}); err != nil {
		t.Fatalf("占用步骤失败: %v", err)
	}
	_, err = db.BeginStepExecution(ctx, storage.StepExecutionStartRequest{
		TaskID:             task.ID,
		StepID:             built.step.ID,
		ExpectedTaskStatus: domain.TaskStatusDeveloping,
		ExpectedStepStatus: domain.StepStatusRunning,
		At:                 at.Add(time.Minute),
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复占用应返回冲突，实际: %v", err)
	}
	loaded, err := db.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if loaded.StartedAt == nil || !loaded.StartedAt.Equal(at) {
		t.Fatalf("被拒绝的重复占用不应改写开始时间: %v", loaded.StartedAt)
	}
}

// advanceTaskToDeveloping 把测试任务沿真实状态机推进到 developing。
//
// queued 不能直接迁移到 developing：环境准备与计划两步是必要环节，这里按真实
// 顺序逐级推进，避免测试构造出生产上不可能出现的状态。
func advanceTaskToDeveloping(
	t *testing.T,
	ctx context.Context,
	db *sqlite.DB,
	task *domain.Task,
) {
	t.Helper()
	for _, next := range []domain.TaskStatus{
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
		domain.TaskStatusDeveloping,
	} {
		previous := task.Status
		if err := task.Transition(next, time.Now().UTC()); err != nil {
			t.Fatalf("推进任务状态到 %s 失败: %v", next, err)
		}
		if err := db.UpdateTaskIfStatus(ctx, *task, []domain.TaskStatus{previous}); err != nil {
			t.Fatalf("写入任务状态 %s 失败: %v", next, err)
		}
	}
}

// TestStepTimestampsSurviveOldDatabaseUpgrade 校验旧库升级后历史步骤仍可读取，
// 且缺失的时间不会被升级过程伪造。
//
// 模拟方式是写入历史记录后关闭连接、用同一路径重新打开（与进程重启一致）：
// 升级路径必须保留既有数据，并且不得为缺失值为 NULL 的记录补写任意时刻。
func TestStepTimestampsSurviveOldDatabaseUpgrade(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	path := workspace.SQLitePath()
	db := openTestDB(t, path)
	ctx := testContext(t)
	built := newFixture(t, db)

	// 先写入一个已执行过的步骤，模拟旧库中的历史记录。
	start := time.Now().UTC().Truncate(time.Millisecond)
	finish := start.Add(time.Minute)
	step := built.step
	if err := step.Transition(domain.StepStatusRunning, start); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusCompleted, finish); err != nil {
		t.Fatalf("完成步骤失败: %v", err)
	}
	if err := db.UpdateStepIfStatus(ctx, step, []domain.StepStatus{domain.StepStatusPending}); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	// 再写入一个从未执行的步骤，模拟历史中缺少时间的记录。
	pending := mustStep(t, db, built.task, built.developer, 2)
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	// 重新打开同一路径：迁移在事务内完成，历史数据与时间必须原样保留。
	upgraded := openTestDB(t, path)
	executed, err := upgraded.GetStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("升级后读取步骤失败: %v", err)
	}
	if executed.StartedAt == nil || !executed.StartedAt.Equal(start) {
		t.Fatalf("升级后开始时间丢失或被改写: %v（期望 %v）", executed.StartedAt, start)
	}
	if executed.FinishedAt == nil || !executed.FinishedAt.Equal(finish) {
		t.Fatalf("升级后结束时间丢失或被改写: %v（期望 %v）", executed.FinishedAt, finish)
	}
	never, err := upgraded.GetStep(ctx, pending.ID)
	if err != nil {
		t.Fatalf("升级后读取步骤失败: %v", err)
	}
	if never.StartedAt != nil || never.FinishedAt != nil {
		t.Fatalf(
			"升级不得为未执行的步骤伪造时间: %v / %v",
			never.StartedAt,
			never.FinishedAt,
		)
	}
}
