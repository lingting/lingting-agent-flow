package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件覆盖"用户显式重试失败任务"的准入规则。
//
// 这些用例只验证纯领域判定：真正的领取原子性、租约栅栏与状态落库由
// tests/integration/local/scheduler 与 tests/integration/local/workflow 的集成用例用
// 真实 SQLite 证明，不能用这里的纯函数替代。

// retryAt 是重试判定的固定时刻。
var retryAt = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

// failedTask 构造一个已按状态机收敛到 failed 的、配置完整的任务。
func failedTask(t *testing.T) domain.Task {
	t.Helper()
	task := retryReadyTask(t)
	if err := task.Transition(domain.TaskStatusFailed, retryAt); err != nil {
		t.Fatalf("任务迁移到 failed 失败: %v", err)
	}
	if err := task.Finish(domain.TaskResultFailed, "上一次执行失败", retryAt); err != nil {
		t.Fatalf("写入失败结论失败: %v", err)
	}
	return task
}

// retryReadyTask 构造一个配置完整、尚未执行的任务。
func retryReadyTask(t *testing.T) domain.Task {
	t.Helper()
	task, err := domain.NewTask(
		domain.NewID(),
		"ssh://git.example.com/team/repo",
		"main",
		"",
		retryAt,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	hostID := domain.NewID()
	task.HostID = &hostID
	task.Config = domain.TaskConfig{
		Goal:            "完成需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   "planner",
		DeveloperRoleID: "developer",
	}
	return task
}

// retryStep 构造一个指定状态的开发步骤。
func retryStep(t *testing.T, task domain.Task, status domain.StepStatus) domain.Step {
	t.Helper()
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model",
		"角色提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		retryAt,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	step, err := domain.NewAgentStep(
		task.ID,
		1,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现",
		"",
		role,
		domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
		domain.RetryConfig{MaxAttempts: 1},
		retryAt,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	for _, hop := range stepTransitionPath(status) {
		if err := step.Transition(hop, retryAt); err != nil {
			t.Fatalf("步骤迁移到 %s 失败: %v", hop, err)
		}
	}
	return step
}

// stepTransitionPath 返回从 pending 合法到达目标状态的最小迁移路径。
//
// 步骤状态机不提供 pending → failed 的直接边，因此用例必须按真实执行顺序推进：
// 一次失败的步骤实际经历 pending → running → failed。
func stepTransitionPath(target domain.StepStatus) []domain.StepStatus {
	switch target {
	case domain.StepStatusPending:
		return nil
	case domain.StepStatusRunning:
		return []domain.StepStatus{domain.StepStatusRunning}
	case domain.StepStatusDeveloping:
		return []domain.StepStatus{domain.StepStatusDeveloping}
	case domain.StepStatusVerifying:
		return []domain.StepStatus{domain.StepStatusRunning, domain.StepStatusVerifying}
	case domain.StepStatusFailed:
		return []domain.StepStatus{domain.StepStatusRunning, domain.StepStatusFailed}
	case domain.StepStatusCompleted:
		return []domain.StepStatus{domain.StepStatusRunning, domain.StepStatusCompleted}
	default:
		return []domain.StepStatus{domain.StepStatusRunning, target}
	}
}

// TestAllowsExplicitRetryOnlyForFailed 校验只有 failed 允许显式重试。
func TestAllowsExplicitRetryOnlyForFailed(t *testing.T) {
	t.Parallel()
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusQueued,
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
		domain.TaskStatusDeveloping,
		domain.TaskStatusVerifying,
		domain.TaskStatusSaving,
		domain.TaskStatusCancelling,
		domain.TaskStatusRetrying,
		domain.TaskStatusPaused,
		domain.TaskStatusCompleted,
		domain.TaskStatusCancelled,
		domain.TaskStatusRecoveryRequired,
	} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			if status.AllowsExplicitRetry() {
				t.Fatalf("状态 %s 不得允许显式重试", status)
			}
		})
	}
	if !domain.TaskStatusFailed.AllowsExplicitRetry() {
		t.Fatal("failed 必须允许显式重试")
	}
}

// TestDecideTaskRetryEligibilityAllowsCleanFailure 校验干净的失败任务可以重试。
func TestDecideTaskRetryEligibilityAllowsCleanFailure(t *testing.T) {
	t.Parallel()
	task := failedTask(t)
	decision := domain.DecideTaskRetryEligibility(domain.TaskRetryInput{
		Task:  task,
		Steps: []domain.Step{retryStep(t, task, domain.StepStatusFailed)},
		Now:   retryAt,
	})
	if !decision.Allowed {
		t.Fatalf("干净的失败任务应当允许重试，实际拒绝: %v", decision.Rejections)
	}
	if decision.Next != domain.TaskStatusPreparing {
		t.Fatalf("重试应回到 preparing，实际 %s", decision.Next)
	}
	if decision.TaskStatus != domain.TaskStatusFailed {
		t.Fatalf("结论应回显判定时的任务状态，实际 %s", decision.TaskStatus)
	}
	if summary := decision.SummaryText(); summary == "" {
		t.Fatal("允许重试时必须给出可读说明")
	}
}

// TestDecideTaskRetryEligibilityRejectsBlockingFacts 校验各类阻塞事实都被拒绝。
func TestDecideTaskRetryEligibilityRejectsBlockingFacts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(t *testing.T, input *domain.TaskRetryInput)
		want   domain.TaskRetryRejection
	}{
		{
			name: "已删除",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				now := retryAt
				input.Task.DeletedAt = &now
			},
			want: domain.TaskRetryRejectionDeleted,
		},
		{
			name: "已请求取消",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				now := retryAt
				input.Task.CancelRequested = true
				input.Task.CancelRequestedAt = &now
			},
			want: domain.TaskRetryRejectionCancelRequested,
		},
		{
			name: "状态不是 failed",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				input.Task.Status = domain.TaskStatusDeveloping
			},
			want: domain.TaskRetryRejectionStatusNotRetryable,
		},
		{
			name: "租约仍有效",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				expires := retryAt.Add(time.Minute)
				input.Task.LeaseOwner = "worker"
				input.Task.LeaseEpoch = 2
				input.Task.LeaseExpiresAt = &expires
			},
			want: domain.TaskRetryRejectionLeaseHeld,
		},
		{
			name: "仍有未结束执行记录",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				input.ActiveWorkflowRun = &domain.WorkflowRun{
					ID:     domain.NewID(),
					TaskID: input.Task.ID,
					Status: domain.WorkflowRunRunning,
				}
			},
			want: domain.TaskRetryRejectionActiveWorkflowRun,
		},
		{
			name: "仍有被占用步骤",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				input.Steps = []domain.Step{
					retryStep(t, input.Task, domain.StepStatusDeveloping),
				}
			},
			want: domain.TaskRetryRejectionStepOccupied,
		},
		{
			name: "存在结果不确定的步骤",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				input.Steps = []domain.Step{
					retryStep(t, input.Task, domain.StepStatusRecoveryRequired),
				}
			},
			want: domain.TaskRetryRejectionRecoveryResidue,
		},
		{
			name: "仍有未核验的外部副作用",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				input.CodeSaveRecords = []domain.CodeSaveRecord{
					{
						ID:           domain.NewID(),
						TaskID:       input.Task.ID.String(),
						CommitStatus: domain.CommitStatusSucceeded,
						PushStatus:   domain.PushStatusPending,
					},
				}
			},
			want: domain.TaskRetryRejectionSideEffectPending,
		},
		{
			name: "缺少执行节点",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				input.Task.HostID = nil
			},
			want: domain.TaskRetryRejectionPrerequisiteMissing,
		},
		{
			name: "没有可重试的步骤",
			mutate: func(t *testing.T, input *domain.TaskRetryInput) {
				input.Steps = []domain.Step{
					retryStep(t, input.Task, domain.StepStatusCompleted),
				}
			},
			want: domain.TaskRetryRejectionNothingToRetry,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			task := failedTask(t)
			input := domain.TaskRetryInput{
				Task:  task,
				Steps: []domain.Step{retryStep(t, task, domain.StepStatusFailed)},
				Now:   retryAt,
			}
			testCase.mutate(t, &input)
			decision := domain.DecideTaskRetryEligibility(input)
			if decision.Allowed {
				t.Fatalf("%s 时不得允许重试", testCase.name)
			}
			if !decision.Reject(decision.Rejections[0]) {
				t.Fatal("拒绝原因必须可查询")
			}
			found := false
			for _, reason := range decision.Rejections {
				if reason == testCase.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("拒绝原因缺少 %s: %v", testCase.want, decision.Rejections)
			}
			if summary := domain.TaskRetrySummary(decision); summary == "" {
				t.Fatal("拒绝时必须给出可读说明")
			}
		})
	}
}

// TestDecideTaskRetryEligibilityCollectsAllReasons 校验一次给出全部拒绝原因。
func TestDecideTaskRetryEligibilityCollectsAllReasons(t *testing.T) {
	t.Parallel()
	task := failedTask(t)
	task.CancelRequested = true
	now := retryAt
	task.CancelRequestedAt = &now
	expires := retryAt.Add(time.Minute)
	task.LeaseOwner = "worker"
	task.LeaseEpoch = 3
	task.LeaseExpiresAt = &expires
	decision := domain.DecideTaskRetryEligibility(domain.TaskRetryInput{
		Task:  task,
		Steps: []domain.Step{retryStep(t, task, domain.StepStatusFailed)},
		Now:   retryAt,
	})
	for _, reason := range []domain.TaskRetryRejection{
		domain.TaskRetryRejectionCancelRequested,
		domain.TaskRetryRejectionLeaseHeld,
	} {
		if !decision.Reject(reason) {
			t.Fatalf("拒绝原因缺少 %s: %v", reason, decision.Rejections)
		}
	}
}

// TestReopenForRetryClearsPreviousCycle 校验重开任务清空上一周期的结论与占用。
func TestReopenForRetryClearsPreviousCycle(t *testing.T) {
	t.Parallel()
	task := failedTask(t)
	stepID := domain.NewID()
	attemptID := domain.NewID()
	task.CurrentStepID = &stepID
	task.CurrentAttemptID = &attemptID
	if err := task.ReopenForRetry(retryAt); err != nil {
		t.Fatalf("重开任务失败: %v", err)
	}
	if task.Status != domain.TaskStatusPreparing {
		t.Fatalf("重开后应为 preparing，实际 %s", task.Status)
	}
	if task.FinalResult != domain.TaskResultNone {
		t.Fatalf("重开必须清空上一周期的结论，实际 %s", task.FinalResult)
	}
	if task.FailureSummary != "" {
		t.Fatalf("重开必须清空上一周期的失败摘要，实际 %q", task.FailureSummary)
	}
	if task.CurrentStepID != nil || task.CurrentAttemptID != nil {
		t.Fatal("重开必须释放上一周期的执行占用")
	}
	// 任务标识不变：重试是恢复同一个任务，而不是创建新任务。
	if task.ID.IsEmpty() {
		t.Fatal("重试不得改变任务身份")
	}
}

// TestReopenForRetryRejectsUnsafeStates 校验重开只接受 failed 且无取消请求。
func TestReopenForRetryRejectsUnsafeStates(t *testing.T) {
	t.Parallel()
	t.Run("非失败状态", func(t *testing.T) {
		t.Parallel()
		task := retryReadyTask(t)
		if err := task.ReopenForRetry(retryAt); err == nil {
			t.Fatal("非失败状态不得重开")
		} else if !errors.Is(err, domain.ErrInvalidState) {
			t.Fatalf("应返回状态错误，实际 %v", err)
		}
	})
	t.Run("已请求取消", func(t *testing.T) {
		t.Parallel()
		task := failedTask(t)
		task.CancelRequested = true
		now := retryAt
		task.CancelRequestedAt = &now
		if err := task.ReopenForRetry(retryAt); err == nil {
			t.Fatal("已请求取消的任务不得重开")
		}
	})
	t.Run("已删除", func(t *testing.T) {
		t.Parallel()
		task := failedTask(t)
		now := retryAt
		task.DeletedAt = &now
		if err := task.ReopenForRetry(retryAt); err == nil {
			t.Fatal("已删除的任务不得重开")
		}
	})
}
