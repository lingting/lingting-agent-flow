// Package domain_test 提供任务级工作流执行记录的单元测试。
//
// 用例只覆盖纯函数与状态机：不访问数据库、不启动进程，因此可以在任意环境完整执行。
package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// workflowRunAt 是工作流测试使用的固定时间。
var workflowRunAt = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// workflowStep 构造一个用于计划摘要与顺序校验的开发步骤。
func workflowStep(t *testing.T, taskID domain.ID, sequence int, title string) domain.Step {
	t.Helper()
	role := domain.Role{
		ID:           domain.NewID(),
		Name:         "开发角色",
		Type:         domain.RoleTypeDeveloper,
		Runner:       "pi",
		Model:        "group/free",
		SystemPrompt: "你是开发者",
		Enabled:      true,
		CreatedAt:    workflowRunAt,
		UpdatedAt:    workflowRunAt,
	}
	step, err := domain.NewAgentStep(
		taskID,
		sequence,
		domain.StepTypeDevelop,
		title,
		"完成 "+title,
		"",
		role,
		domain.VerificationSpec{
			Commands: []domain.VerificationCommand{{Name: "build", Command: "go", Args: []string{"build", "./..."}}},
		},
		domain.RetryConfig{MaxAttempts: 1},
		workflowRunAt,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	return step
}

// TestWorkflowRunStatusTransition 覆盖工作流执行状态机的关键约束。
func TestWorkflowRunStatusTransition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		from    domain.WorkflowRunStatus
		to      domain.WorkflowRunStatus
		allowed bool
	}{
		{"pending 可以开始", domain.WorkflowRunPending, domain.WorkflowRunRunning, true},
		{"running 可以完成", domain.WorkflowRunRunning, domain.WorkflowRunCompleted, true},
		{"running 可以阻塞", domain.WorkflowRunRunning, domain.WorkflowRunBlocked, true},
		{"running 可以进入恢复核验", domain.WorkflowRunRunning, domain.WorkflowRunRecoveryRequired, true},
		{"blocked 可以恢复", domain.WorkflowRunBlocked, domain.WorkflowRunRunning, true},
		{"recovery_required 可以恢复", domain.WorkflowRunRecoveryRequired, domain.WorkflowRunRunning, true},
		{"cancel_requested 可以收敛为已取消", domain.WorkflowRunCancelRequested, domain.WorkflowRunCancelled, true},
		{"cancel_requested 不能回到 running", domain.WorkflowRunCancelRequested, domain.WorkflowRunRunning, false},
		{"completed 不能被重新打开", domain.WorkflowRunCompleted, domain.WorkflowRunRunning, false},
		{"cancelled 不能被重新打开", domain.WorkflowRunCancelled, domain.WorkflowRunRunning, false},
		{"failed 不能被重新打开", domain.WorkflowRunFailed, domain.WorkflowRunRunning, false},
		{"blocked 不能直接完成", domain.WorkflowRunBlocked, domain.WorkflowRunCompleted, false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			run := domain.WorkflowRun{Status: testCase.from}
			err := run.Transition(testCase.to, workflowRunAt)
			if testCase.allowed && err != nil {
				t.Fatalf("期望允许迁移 %s → %s，实际失败: %v", testCase.from, testCase.to, err)
			}
			if !testCase.allowed {
				if err == nil {
					t.Fatalf("期望拒绝迁移 %s → %s，实际成功", testCase.from, testCase.to)
				}
				if !strings.Contains(err.Error(), "状态迁移") {
					t.Fatalf("错误信息未说明状态迁移被拒绝: %v", err)
				}
				if run.Status != testCase.from {
					t.Fatalf("拒绝迁移时状态不应改变: %s", run.Status)
				}
			}
		})
	}
}

// TestWorkflowRunTerminalRequiresFinishedAt 校验终态必须记录结束时间。
func TestWorkflowRunTerminalRequiresFinishedAt(t *testing.T) {
	t.Parallel()
	taskID := domain.NewID()
	step := workflowStep(t, taskID, 1, "第一步")
	run, err := domain.NewWorkflowRun(taskID, []domain.Step{step}, workflowRunAt)
	if err != nil {
		t.Fatalf("创建工作流记录失败: %v", err)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("待执行记录应当合法: %v", err)
	}
	run.Status = domain.WorkflowRunCompleted
	if err := run.Validate(); err == nil {
		t.Fatal("终态缺少结束时间时应当校验失败")
	}
}

// TestWorkflowPlanDigestIsStable 校验计划摘要只依赖计划内容。
func TestWorkflowPlanDigestIsStable(t *testing.T) {
	t.Parallel()
	taskID := domain.NewID()
	first := workflowStep(t, taskID, 1, "第一步")
	second := workflowStep(t, taskID, 2, "第二步")
	digest, err := domain.WorkflowPlanDigest([]domain.Step{first, second})
	if err != nil {
		t.Fatalf("计算计划摘要失败: %v", err)
	}
	// 步骤状态变化不改变计划内容摘要：恢复时必须仍能匹配同一份计划。
	completed := first
	completed.Status = domain.StepStatusCompleted
	repeated, err := domain.WorkflowPlanDigest([]domain.Step{completed, second})
	if err != nil {
		t.Fatalf("计算计划摘要失败: %v", err)
	}
	if digest != repeated {
		t.Fatalf("计划内容未变但摘要不同: %s != %s", digest, repeated)
	}
	// 计划被重新生成时摘要必须不同，否则会沿用错误的游标。
	renamed := workflowStep(t, taskID, 2, "第二步（改名）")
	changed, err := domain.WorkflowPlanDigest([]domain.Step{first, renamed})
	if err != nil {
		t.Fatalf("计算计划摘要失败: %v", err)
	}
	if digest == changed {
		t.Fatal("计划内容变化后摘要必须不同")
	}
}

// TestCheckWorkflowStartRejectsInvalidPlans 覆盖启动前的计划与任务校验。
func TestCheckWorkflowStartRejectsInvalidPlans(t *testing.T) {
	t.Parallel()
	requirementID := domain.NewID()
	newTask := func(t *testing.T) domain.Task {
		t.Helper()
		task, err := domain.NewTask(requirementID, "ssh://git.example.com/team/repo", "main", "", workflowRunAt)
		if err != nil {
			t.Fatalf("构造任务失败: %v", err)
		}
		task.ContainerID = "container-1"
		task.Config = domain.TaskConfig{
			Goal:            "完成需求",
			Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
			DeveloperRoleID: domain.NewID(),
			PlannerRoleID:   domain.NewID(),
		}
		return task
	}
	toPlanned := func(t *testing.T, task domain.Task) domain.Task {
		t.Helper()
		for _, status := range []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanning,
			domain.TaskStatusPlanned,
		} {
			if err := task.Transition(status, workflowRunAt); err != nil {
				t.Fatalf("任务迁移到 %s 失败: %v", status, err)
			}
		}
		return task
	}

	t.Run("合法计划通过校验", func(t *testing.T) {
		t.Parallel()
		task := toPlanned(t, newTask(t))
		steps := []domain.Step{
			workflowStep(t, task.ID, 1, "第一步"),
			workflowStep(t, task.ID, 2, "第二步"),
		}
		if err := domain.CheckWorkflowStart(task, steps); err != nil {
			t.Fatalf("合法计划不应被拒绝: %v", err)
		}
	})

	t.Run("计划缺失时拒绝启动", func(t *testing.T) {
		t.Parallel()
		task := toPlanned(t, newTask(t))
		if err := domain.CheckWorkflowStart(task, nil); err == nil {
			t.Fatal("没有计划步骤时应当拒绝启动")
		}
	})

	t.Run("任务状态不允许时拒绝启动", func(t *testing.T) {
		t.Parallel()
		task := newTask(t)
		steps := []domain.Step{workflowStep(t, task.ID, 1, "第一步")}
		err := domain.CheckWorkflowStart(task, steps)
		if err == nil {
			t.Fatal("queued 任务未完成环境准备时应当拒绝启动")
		}
		if !strings.Contains(err.Error(), "不允许启动工作流执行") {
			t.Fatalf("错误信息未说明状态不允许: %v", err)
		}
	})

	t.Run("缺少容器时拒绝启动", func(t *testing.T) {
		t.Parallel()
		task := toPlanned(t, newTask(t))
		task.ContainerID = ""
		steps := []domain.Step{workflowStep(t, task.ID, 1, "第一步")}
		err := domain.CheckWorkflowStart(task, steps)
		if err == nil || !strings.Contains(err.Error(), "任务容器") {
			t.Fatalf("缺少容器时应当拒绝启动: %v", err)
		}
	})

	t.Run("重复步骤序号时拒绝启动", func(t *testing.T) {
		t.Parallel()
		task := toPlanned(t, newTask(t))
		steps := []domain.Step{
			workflowStep(t, task.ID, 1, "第一步"),
			workflowStep(t, task.ID, 1, "重复序号"),
		}
		err := domain.CheckWorkflowStart(task, steps)
		if err == nil || !strings.Contains(err.Error(), "重复的步骤序号") {
			t.Fatalf("重复序号时应当拒绝启动: %v", err)
		}
	})

	t.Run("缺少必需检查时拒绝启动", func(t *testing.T) {
		t.Parallel()
		task := toPlanned(t, newTask(t))
		step := workflowStep(t, task.ID, 1, "第一步")
		step.VerificationSpec = domain.VerificationSpec{}
		err := domain.CheckWorkflowStart(task, []domain.Step{step})
		if err == nil || !strings.Contains(err.Error(), "必需检查") {
			t.Fatalf("缺少必需检查时应当拒绝启动: %v", err)
		}
	})

	t.Run("非开发步骤时拒绝启动", func(t *testing.T) {
		t.Parallel()
		task := toPlanned(t, newTask(t))
		step, err := domain.NewWorkflowStep(
			task.ID,
			1,
			domain.StepTypeVerify,
			"验证",
			"执行验证",
			domain.VerificationSpec{},
			1,
			workflowRunAt,
		)
		if err != nil {
			t.Fatalf("构造步骤失败: %v", err)
		}
		startErr := domain.CheckWorkflowStart(task, []domain.Step{step})
		if startErr == nil || !strings.Contains(startErr.Error(), "不是开发步骤") {
			t.Fatalf("非开发步骤时应当拒绝启动: %v", startErr)
		}
	})

	t.Run("全部步骤已完成时拒绝启动", func(t *testing.T) {
		t.Parallel()
		task := toPlanned(t, newTask(t))
		step := workflowStep(t, task.ID, 1, "第一步")
		step.Status = domain.StepStatusCompleted
		err := domain.CheckWorkflowStart(task, []domain.Step{step})
		if err == nil || !strings.Contains(err.Error(), "无需启动工作流") {
			t.Fatalf("全部步骤已完成时应当拒绝启动: %v", err)
		}
	})
}

// TestDecideWorkflowCloseout 覆盖任务级收尾决策的全部组合。
func TestDecideWorkflowCloseout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		input      domain.WorkflowCloseoutInput
		wantStatus domain.WorkflowRunStatus
	}{
		{
			name:       "步骤未完成时不进入收尾",
			input:      domain.WorkflowCloseoutInput{StepsCompleted: false},
			wantStatus: domain.WorkflowRunBlocked,
		},
		{
			name: "代码保存结果不确定时需要人工核验",
			input: domain.WorkflowCloseoutInput{
				StepsCompleted:        true,
				CodeSaveRequested:     true,
				CodeSaveIndeterminate: true,
				SessionPersisted:      true,
			},
			wantStatus: domain.WorkflowRunRecoveryRequired,
		},
		{
			name: "代码保存未成功时阻塞",
			input: domain.WorkflowCloseoutInput{
				StepsCompleted:    true,
				CodeSaveRequested: true,
				SessionPersisted:  true,
			},
			wantStatus: domain.WorkflowRunBlocked,
		},
		{
			name: "会话未归档时需要人工核验",
			input: domain.WorkflowCloseoutInput{
				StepsCompleted:    true,
				CodeSaveRequested: true,
				CodeSaveSucceeded: true,
				SessionPersisted:  false,
			},
			wantStatus: domain.WorkflowRunRecoveryRequired,
		},
		{
			name: "全部条件满足时完成",
			input: domain.WorkflowCloseoutInput{
				StepsCompleted:    true,
				CodeSaveRequested: true,
				CodeSaveSucceeded: true,
				SessionPersisted:  true,
			},
			wantStatus: domain.WorkflowRunCompleted,
		},
		{
			name: "未请求代码保存时只看会话归档",
			input: domain.WorkflowCloseoutInput{
				StepsCompleted:   true,
				SessionPersisted: true,
			},
			wantStatus: domain.WorkflowRunCompleted,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := domain.DecideWorkflowCloseout(testCase.input)
			if decision.Status != testCase.wantStatus {
				t.Fatalf("期望结论 %s，实际 %s", testCase.wantStatus, decision.Status)
			}
			if decision.Summary == "" {
				t.Fatal("结论必须给出说明")
			}
			if decision.Completed() != (testCase.wantStatus == domain.WorkflowRunCompleted) {
				t.Fatalf("Completed 与结论不一致: %s", decision.Status)
			}
		})
	}
}

// TestWorkflowRunRecordsProgress 校验游标只向前推进。
func TestWorkflowRunRecordsProgress(t *testing.T) {
	t.Parallel()
	taskID := domain.NewID()
	first := workflowStep(t, taskID, 1, "第一步")
	second := workflowStep(t, taskID, 2, "第二步")
	run, err := domain.NewWorkflowRun(taskID, []domain.Step{first, second}, workflowRunAt)
	if err != nil {
		t.Fatalf("创建工作流记录失败: %v", err)
	}
	if run.TotalSteps != 2 {
		t.Fatalf("开发步骤总数应为 2，实际 %d", run.TotalSteps)
	}
	if err := run.RecordStepCompleted(first, workflowRunAt); err != nil {
		t.Fatalf("记录步骤完成失败: %v", err)
	}
	if run.CompletedSteps != 1 || run.LastCompletedSequence != 1 {
		t.Fatalf("进度记录不正确: %+v", run)
	}
	// 重复记录同一步骤不应让进度虚增。
	if err := run.RecordStepCompleted(first, workflowRunAt); err != nil {
		t.Fatalf("重复记录步骤完成失败: %v", err)
	}
	if run.CompletedSteps != 1 {
		t.Fatalf("重复记录不应增加已完成数量: %d", run.CompletedSteps)
	}
	if err := run.RecordStepCompleted(second, workflowRunAt); err != nil {
		t.Fatalf("记录步骤完成失败: %v", err)
	}
	if run.CompletedSteps != 2 || run.LastCompletedSequence != 2 {
		t.Fatalf("进度记录不正确: %+v", run)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("记录进度后记录应当合法: %v", err)
	}
}

// TestWorkflowRunRequestCancelIsIdempotent 校验取消请求幂等且不伪造已停止。
func TestWorkflowRunRequestCancelIsIdempotent(t *testing.T) {
	t.Parallel()
	taskID := domain.NewID()
	step := workflowStep(t, taskID, 1, "第一步")
	run, err := domain.NewWorkflowRun(taskID, []domain.Step{step}, workflowRunAt)
	if err != nil {
		t.Fatalf("创建工作流记录失败: %v", err)
	}
	if err := run.Start("worker-1", time.Minute, workflowRunAt); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	if err := run.RequestCancel(workflowRunAt); err != nil {
		t.Fatalf("请求取消失败: %v", err)
	}
	firstAt := *run.CancelRequestedAt
	later := workflowRunAt.Add(time.Hour)
	if err := run.RequestCancel(later); err != nil {
		t.Fatalf("重复请求取消应当幂等: %v", err)
	}
	if !run.CancelRequestedAt.Equal(firstAt) {
		t.Fatal("重复取消不应改写取消请求时间")
	}
	if run.Status != domain.WorkflowRunRunning {
		t.Fatalf("请求取消不应直接改写状态，实际 %s", run.Status)
	}
	// 取消必须经过 cancel_requested 中间态：running 不能直接跳到 cancelled。
	if err := run.Transition(domain.WorkflowRunCancelRequested, later); err != nil {
		t.Fatalf("推进取消请求状态失败: %v", err)
	}
	if err := run.Cancel("已取消", later); err != nil {
		t.Fatalf("收敛为已取消失败: %v", err)
	}
	if run.Status != domain.WorkflowRunCancelled || run.FinishedAt == nil {
		t.Fatalf("取消终态不正确: %+v", run)
	}
	if err := run.RequestCancel(later); err == nil {
		t.Fatal("已结束的执行不应接受取消请求")
	}
}

// TestWorkflowRunLeaseExpiry 校验执行权过期判断。
func TestWorkflowRunLeaseExpiry(t *testing.T) {
	t.Parallel()
	taskID := domain.NewID()
	step := workflowStep(t, taskID, 1, "第一步")
	run, err := domain.NewWorkflowRun(taskID, []domain.Step{step}, workflowRunAt)
	if err != nil {
		t.Fatalf("创建工作流记录失败: %v", err)
	}
	if run.LeaseActive(workflowRunAt) {
		t.Fatal("未启动的记录不应持有执行权")
	}
	if err := run.Start("worker-1", 5*time.Minute, workflowRunAt); err != nil {
		t.Fatalf("启动工作流失败: %v", err)
	}
	if !run.LeaseActive(workflowRunAt.Add(time.Minute)) {
		t.Fatal("租约有效期内应视为持有执行权")
	}
	if run.LeaseActive(workflowRunAt.Add(10 * time.Minute)) {
		t.Fatal("租约过期后不应视为持有执行权")
	}
}
