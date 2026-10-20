package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// executionStepFixture 构造一对用于执行准入校验的任务与步骤。
func executionStepFixture(
	t *testing.T,
	taskStatus domain.TaskStatus,
	stepStatus domain.StepStatus,
) (domain.Task, domain.Step) {
	t.Helper()
	now := time.Now().UTC()
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	task, err := domain.NewTask("requirement-1", "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"",
		role,
		domain.VerificationSpec{},
		domain.RetryConfig{MaxAttempts: 1},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	task.Status = taskStatus
	step.Status = stepStatus
	return task, step
}

// TestCheckStepExecutionStartRequiresRunnableTaskStatus 验证任务状态准入。
func TestCheckStepExecutionStartRequiresRunnableTaskStatus(t *testing.T) {
	t.Parallel()
	allowed := map[domain.TaskStatus]bool{
		domain.TaskStatusQueued:           false,
		domain.TaskStatusPreparing:        true,
		domain.TaskStatusPlanning:         false,
		domain.TaskStatusPlanned:          true,
		domain.TaskStatusDeveloping:       true,
		domain.TaskStatusVerifying:        false,
		domain.TaskStatusSaving:           false,
		domain.TaskStatusCancelling:       false,
		domain.TaskStatusRetrying:         true,
		domain.TaskStatusPaused:           true,
		domain.TaskStatusCompleted:        false,
		domain.TaskStatusFailed:           false,
		domain.TaskStatusCancelled:        false,
		domain.TaskStatusRecoveryRequired: true,
	}
	for status, expected := range allowed {
		task, step := executionStepFixture(t, status, domain.StepStatusPending)
		err := domain.CheckStepExecutionStart(task, step)
		if expected && err != nil {
			t.Errorf("任务状态 %s 应允许执行，实际拒绝: %v", status, err)
			continue
		}
		if !expected && err == nil {
			t.Errorf("任务状态 %s 不应允许执行", status)
		}
		if !expected && !errors.Is(err, domain.ErrStepExecutionBlocked) {
			t.Errorf("任务状态 %s 的拒绝必须可识别: %v", status, err)
		}
	}
}

// TestCheckStepExecutionStartRequiresRunnableStepStatus 验证步骤状态准入。
func TestCheckStepExecutionStartRequiresRunnableStepStatus(t *testing.T) {
	t.Parallel()
	allowed := map[domain.StepStatus]bool{
		domain.StepStatusPending:          true,
		domain.StepStatusRetryWait:        true,
		domain.StepStatusFailed:           true,
		domain.StepStatusRecoveryRequired: true,
		domain.StepStatusRunning:          false,
		domain.StepStatusDeveloping:       false,
		domain.StepStatusVerifying:        false,
		domain.StepStatusCompleted:        false,
		domain.StepStatusSkipped:          false,
		domain.StepStatusCancelled:        false,
	}
	for status, expected := range allowed {
		task, step := executionStepFixture(t, domain.TaskStatusDeveloping, status)
		err := domain.CheckStepExecutionStart(task, step)
		if expected && err != nil {
			t.Errorf("步骤状态 %s 应允许执行，实际拒绝: %v", status, err)
			continue
		}
		if !expected && err == nil {
			t.Errorf("步骤状态 %s 不应允许执行", status)
		}
	}
}

// TestCheckStepExecutionStartRejectsForeignStep 验证步骤归属校验。
func TestCheckStepExecutionStartRejectsForeignStep(t *testing.T) {
	t.Parallel()
	task, step := executionStepFixture(t, domain.TaskStatusPlanned, domain.StepStatusPending)
	step.TaskID = "another-task"
	if err := domain.CheckStepExecutionStart(task, step); err == nil {
		t.Fatal("不属于该任务的步骤必须被拒绝")
	}
}

// TestCheckStepExecutionStartRejectsOccupiedStep 验证被占用的步骤不能再次启动。
func TestCheckStepExecutionStartRejectsOccupiedStep(t *testing.T) {
	t.Parallel()
	task, step := executionStepFixture(t, domain.TaskStatusPlanned, domain.StepStatusPending)
	attemptID := domain.NewID()
	step.CurrentAttemptID = &attemptID
	if err := domain.CheckStepExecutionStart(task, step); err == nil {
		t.Fatal("已被占用的步骤必须被拒绝")
	}
}

// TestCheckStepExecutionStartRejectsNonAgentStep 验证固定阶段不能通过该入口执行。
func TestCheckStepExecutionStartRejectsNonAgentStep(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	task, err := domain.NewTask("requirement-1", "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.Status = domain.TaskStatusPlanned
	step, err := domain.NewWorkflowStep(
		task.ID,
		1,
		domain.StepTypeVerify,
		"执行验证",
		"运行必需检查",
		domain.VerificationSpec{},
		1,
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := domain.CheckStepExecutionStart(task, step); err == nil {
		t.Fatal("确定性验证阶段不应通过 Agent 执行接口启动")
	}
}

// TestCheckStepExecutionStartRejectsCancelledTask 验证取消请求阻止新的执行。
func TestCheckStepExecutionStartRejectsCancelledTask(t *testing.T) {
	t.Parallel()
	task, step := executionStepFixture(t, domain.TaskStatusDeveloping, domain.StepStatusPending)
	if err := task.RequestCancel(time.Now().UTC()); err != nil {
		t.Fatalf("记录取消请求失败: %v", err)
	}
	if err := domain.CheckStepExecutionStart(task, step); err == nil {
		t.Fatal("已请求取消的任务不应开始新的执行")
	}
}

// TestStepExecutionOutcomeAttemptStatus 验证执行结论到尝试终态的映射。
func TestStepExecutionOutcomeAttemptStatus(t *testing.T) {
	t.Parallel()
	cases := map[domain.StepExecutionOutcome]domain.AttemptStatus{
		domain.StepExecutionCompleted:   domain.AttemptStatusCompleted,
		domain.StepExecutionFailed:      domain.AttemptStatusFailed,
		domain.StepExecutionTimedOut:    domain.AttemptStatusTimedOut,
		domain.StepExecutionCancelled:   domain.AttemptStatusCancelled,
		domain.StepExecutionInterrupted: domain.AttemptStatusInterrupted,
	}
	for outcome, expected := range cases {
		if actual := outcome.AttemptStatus(); actual != expected {
			t.Errorf("%s 的尝试终态应为 %s，实际 %s", outcome, expected, actual)
		}
		if err := outcome.Validate(); err != nil {
			t.Errorf("%s 应通过校验: %v", outcome, err)
		}
	}
	if err := domain.StepExecutionOutcome("unknown").Validate(); err == nil {
		t.Fatal("未知执行结论必须被拒绝")
	}
}
