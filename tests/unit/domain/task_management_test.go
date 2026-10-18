package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// newQueuedTask 构造一个等待执行的任务。
func newQueuedTask(t *testing.T) domain.Task {
	t.Helper()
	task, err := domain.NewTask(domain.NewID(), "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	return task
}

// TestCancelImmediatelyOnlyFromQueued 校验只有等待执行的任务可以直接取消。
func TestCancelImmediatelyOnlyFromQueued(t *testing.T) {
	t.Parallel()
	task := newQueuedTask(t)
	now := time.Now().UTC()
	if err := task.CancelImmediately(now); err != nil {
		t.Fatalf("取消等待中的任务失败: %v", err)
	}
	if task.Status != domain.TaskStatusCancelled {
		t.Fatalf("状态应为 cancelled，实际 %s", task.Status)
	}
	if task.FinalResult != domain.TaskResultCancelled {
		t.Fatalf("结论应为 cancelled，实际 %q", task.FinalResult)
	}
	if task.FinishedAt == nil {
		t.Fatal("取消后应记录结束时间")
	}
	if !task.CancelRequested || task.CancelRequestedAt == nil {
		t.Fatal("取消请求应被记录")
	}
}

// TestCancelImmediatelyRejectsRunningTask 校验执行中的任务不能被直接标记为已取消。
func TestCancelImmediatelyRejectsRunningTask(t *testing.T) {
	t.Parallel()
	task := newQueuedTask(t)
	now := time.Now().UTC()
	if err := task.Transition(domain.TaskStatusPreparing, now); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	err := task.CancelImmediately(now)
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("执行中的任务应拒绝直接取消，实际: %v", err)
	}
	if task.Status != domain.TaskStatusPreparing {
		t.Fatalf("拒绝时不应改变状态: %s", task.Status)
	}
	if task.FinalResult != domain.TaskResultNone {
		t.Fatalf("拒绝时不应写入结论: %q", task.FinalResult)
	}
}

// TestRequestCancelIsIdempotent 校验重复取消请求不重复改写时间戳。
func TestRequestCancelIsIdempotent(t *testing.T) {
	t.Parallel()
	task := newQueuedTask(t)
	first := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := task.RequestCancel(first); err != nil {
		t.Fatalf("首次取消请求失败: %v", err)
	}
	recorded := *task.CancelRequestedAt
	second := first.Add(time.Hour)
	if err := task.RequestCancel(second); err != nil {
		t.Fatalf("重复取消请求应当成功: %v", err)
	}
	if !task.CancelRequestedAt.Equal(recorded) {
		t.Fatalf("重复请求不应改写时间戳: %v -> %v", recorded, *task.CancelRequestedAt)
	}
}

// TestRequestCancelRejectsTerminalTask 校验终态任务不能再请求取消。
func TestRequestCancelRejectsTerminalTask(t *testing.T) {
	t.Parallel()
	task := newQueuedTask(t)
	now := time.Now().UTC()
	if err := task.CancelImmediately(now); err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}
	err := task.RequestCancel(now.Add(time.Minute))
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("终态任务应拒绝取消请求，实际: %v", err)
	}
}

// TestMarkDeletedRequiresTerminalStatus 校验只有终态任务可以删除。
func TestMarkDeletedRequiresTerminalStatus(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cases := []struct {
		name string
		path []domain.TaskStatus
	}{
		{name: "等待执行"},
		{name: "准备环境", path: []domain.TaskStatus{domain.TaskStatusPreparing}},
		{name: "正在开发", path: []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusDeveloping,
		}},
		{name: "取消中", path: []domain.TaskStatus{domain.TaskStatusCancelling}},
	}
	for _, testCase := range cases {
		task := newQueuedTask(t)
		for _, status := range testCase.path {
			if err := task.Transition(status, now); err != nil {
				t.Fatalf("%s: 状态迁移失败: %v", testCase.name, err)
			}
		}
		err := task.MarkDeleted(now)
		if !errors.Is(err, domain.ErrInvalidState) {
			t.Fatalf("%s: 非终态任务应拒绝删除，实际: %v", testCase.name, err)
		}
		if task.DeletedAt != nil {
			t.Fatalf("%s: 拒绝时不应写入删除时间", testCase.name)
		}
	}
}

// TestMarkDeletedAcceptsTerminalStatus 校验终态任务可以删除且不改变状态。
func TestMarkDeletedAcceptsTerminalStatus(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusCompleted,
		domain.TaskStatusFailed,
		domain.TaskStatusCancelled,
	} {
		task := newQueuedTask(t)
		task.Status = status
		if err := task.MarkDeleted(now); err != nil {
			t.Fatalf("%s: 终态任务应允许删除: %v", status, err)
		}
		if task.Status != status {
			t.Fatalf("%s: 删除不应改变状态: %s", status, task.Status)
		}
		if task.DeletedAt == nil {
			t.Fatalf("%s: 删除时间应当被记录", status)
		}
		if !task.DeletedAt.Equal(now.UTC().Truncate(time.Millisecond)) {
			t.Fatalf("%s: 删除时间不符: %v", status, task.DeletedAt)
		}
		if !task.UpdatedAt.Equal(*task.DeletedAt) {
			t.Fatalf("%s: 删除应刷新更新时间", status)
		}
	}
}

// TestMarkDeletedRejectsRepeatedDelete 校验重复删除被拒绝。
func TestMarkDeletedRejectsRepeatedDelete(t *testing.T) {
	t.Parallel()
	task := newQueuedTask(t)
	now := time.Now().UTC()
	task.Status = domain.TaskStatusFailed
	if err := task.MarkDeleted(now); err != nil {
		t.Fatalf("首次删除失败: %v", err)
	}
	err := task.MarkDeleted(now.Add(time.Minute))
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("重复删除应被拒绝，实际: %v", err)
	}
}

// TestDeletedTaskRejectsCancel 校验已删除任务不能继续取消。
func TestDeletedTaskRejectsCancel(t *testing.T) {
	t.Parallel()
	task := newQueuedTask(t)
	now := time.Now().UTC()
	task.Status = domain.TaskStatusFailed
	if err := task.MarkDeleted(now); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	if err := task.RequestCancel(now); !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("已删除任务应拒绝取消请求，实际: %v", err)
	}
	if err := task.CancelImmediately(now); !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("已删除任务应拒绝直接取消，实际: %v", err)
	}
}

// TestTaskStatusIsTerminalCoversManagementSemantics 校验终态判定与管理接口一致。
func TestTaskStatusIsTerminalCoversManagementSemantics(t *testing.T) {
	t.Parallel()
	terminal := map[domain.TaskStatus]bool{
		domain.TaskStatusQueued:           false,
		domain.TaskStatusPreparing:        false,
		domain.TaskStatusPlanning:         false,
		domain.TaskStatusPlanned:          false,
		domain.TaskStatusDeveloping:       false,
		domain.TaskStatusVerifying:        false,
		domain.TaskStatusSaving:           false,
		domain.TaskStatusCancelling:       false,
		domain.TaskStatusRetrying:         false,
		domain.TaskStatusPaused:           false,
		domain.TaskStatusRecoveryRequired: false,
		domain.TaskStatusCompleted:        true,
		// failed 不是终态：允许用户显式重试回到执行流程。
		domain.TaskStatusFailed:    false,
		domain.TaskStatusCancelled: true,
	}
	for status, want := range terminal {
		if got := status.IsTerminal(); got != want {
			t.Fatalf("%s 的终态判定应为 %v，实际 %v", status, want, got)
		}
	}
	// 已结束（可删除）比终态更宽：失败任务的执行已经停止，可以删除。
	finished := map[domain.TaskStatus]bool{
		domain.TaskStatusQueued:     false,
		domain.TaskStatusPreparing:  false,
		domain.TaskStatusDeveloping: false,
		domain.TaskStatusCancelling: false,
		domain.TaskStatusPaused:     false,
		domain.TaskStatusCompleted:  true,
		domain.TaskStatusFailed:     true,
		domain.TaskStatusCancelled:  true,
	}
	for status, want := range finished {
		if got := status.IsFinished(); got != want {
			t.Fatalf("%s 的已结束判定应为 %v，实际 %v", status, want, got)
		}
	}
}

// TestValidateProjectID 校验可选项目标识的合法性。
func TestValidateProjectID(t *testing.T) {
	t.Parallel()
	valid := []string{"", "  ", "demo", "my-project-2", "abc123"}
	for _, id := range valid {
		if err := domain.ValidateProjectID(id); err != nil {
			t.Fatalf("合法项目标识 %q 被拒绝: %v", id, err)
		}
	}
	invalid := []string{"Demo", "my_project", "-demo", "demo project", string(make([]byte, 40))}
	for _, id := range invalid {
		if err := domain.ValidateProjectID(id); err == nil {
			t.Fatalf("非法项目标识 %q 应当被拒绝", id)
		}
	}
}
