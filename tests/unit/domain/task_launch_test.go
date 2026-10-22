// Package domain_test 验证统一任务启动流程的纯函数判定规则。
//
// 用例只依赖 domain 包：不读数据库、不启动进程、不访问网络，因此可以并发执行。
// 断言的是"下一步该做哪件事"与"被占用的步骤是否需要收敛"这两类决策，而不是
// 应用层的调用顺序。
package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// launchTask 构造一条可用于启动判定的任务记录。
func launchTask(t *testing.T) domain.Task {
	t.Helper()
	task, err := domain.NewTask(
		domain.NewID(),
		"git@example.com:demo/repo.git",
		"main",
		"",
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	environment, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	hostID := domain.NewID()
	task.HostID = &hostID
	task.Config = domain.TaskConfig{
		Goal:            "实现功能",
		Environment:     environment,
		PlannerRoleID:   domain.NewID(),
		DeveloperRoleID: domain.NewID(),
	}
	return task
}

// launchStep 构造一条属于指定任务的步骤记录。
func launchStep(t *testing.T, taskID domain.ID, sequence int, status domain.StepStatus) domain.Step {
	t.Helper()
	step, err := domain.NewAgentStep(
		taskID,
		sequence,
		domain.StepTypeDevelop,
		"实现接口",
		"按需求实现接口",
		"只修改当前工作区",
		domain.Role{
			ID:           domain.NewID(),
			Name:         "开发工程师",
			Type:         domain.RoleTypeDeveloper,
			Runner:       domain.RoleRunnerPi,
			Model:        "group/free",
			SystemPrompt: "开发提示词",
		},
		domain.VerificationSpec{
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}},
			},
		},
		domain.RetryConfig{},
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	step.Status = status
	return step
}

func TestDecideTaskLaunchNextPicksEarliestUnfinishedStage(t *testing.T) {
	t.Parallel()
	task := launchTask(t)

	cases := []struct {
		name        string
		input       domain.TaskLaunchInput
		want        domain.TaskLaunchStage
		wantInvalid bool
	}{
		{
			name:  "没有容器时先准备容器",
			input: domain.TaskLaunchInput{Task: task, ContainerUsable: false},
			want:  domain.TaskLaunchStageContainer,
		},
		{
			name:  "容器可用但没有计划时生成计划",
			input: domain.TaskLaunchInput{Task: task, ContainerUsable: true},
			want:  domain.TaskLaunchStagePlan,
		},
		{
			name: "容器可用且计划已持久化时启动编排",
			input: domain.TaskLaunchInput{
				Task:            task,
				ContainerUsable: true,
				Steps:           []domain.Step{launchStep(t, task.ID, 1, domain.StepStatusPending)},
			},
			want: domain.TaskLaunchStageWorkflow,
		},
		{
			name: "已有未结束执行时拒绝启动",
			input: domain.TaskLaunchInput{
				Task:                 task,
				ContainerUsable:      true,
				HasActiveWorkflowRun: true,
			},
			wantInvalid: true,
		},
		{
			name: "存在需要人工核验的步骤时拒绝启动",
			input: domain.TaskLaunchInput{
				Task:            task,
				ContainerUsable: true,
				Steps: []domain.Step{
					launchStep(t, task.ID, 1, domain.StepStatusRecoveryRequired),
				},
			},
			wantInvalid: true,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			stage, err := domain.DecideTaskLaunchNext(testCase.input)
			if testCase.wantInvalid {
				if !errors.Is(err, domain.ErrInvalidState) {
					t.Fatalf("必须返回状态错误，实际 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("判定下一步失败: %v", err)
			}
			if stage != testCase.want {
				t.Fatalf("阶段应为 %s，实际 %s", testCase.want, stage)
			}
		})
	}
}

func TestCheckTaskLaunchStartRejectsInvalidScenes(t *testing.T) {
	t.Parallel()

	base := launchTask(t)
	deleted := base
	now := time.Now()
	deleted.DeletedAt = &now
	cancelled := base
	cancelled.CancelRequested = true
	cancelled.CancelRequestedAt = &now
	finished := base
	finished.Status = domain.TaskStatusCompleted
	noHost := base
	noHost.HostID = nil
	noPlanner := base
	noPlanner.Config.PlannerRoleID = ""

	cases := []struct {
		name string
		task domain.Task
	}{
		{name: "已删除任务", task: deleted},
		{name: "已请求取消", task: cancelled},
		{name: "已结束任务", task: finished},
		{name: "缺少执行节点", task: noHost},
		{name: "缺少规划角色", task: noPlanner},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := domain.CheckTaskLaunchStart(domain.TaskLaunchInput{Task: testCase.task})
			if !errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("必须返回状态错误，实际 %v", err)
			}
		})
	}
}

func TestDecideStepOccupancyRecoveryOnlyConvergesIdleOccupancy(t *testing.T) {
	t.Parallel()
	task := launchTask(t)

	cases := []struct {
		name      string
		status    domain.StepStatus
		activeRun bool
		wantNeed  bool
		wantNext  domain.StepStatus
	}{
		{
			name:     "未占用的步骤不处理",
			status:   domain.StepStatusPending,
			wantNeed: false,
			wantNext: domain.StepStatusPending,
		},
		{
			name:     "执行中且没有未结束执行时需要收敛",
			status:   domain.StepStatusRunning,
			wantNeed: true,
			wantNext: domain.StepStatusRecoveryRequired,
		},
		{
			name:     "验证中且没有未结束执行时需要收敛",
			status:   domain.StepStatusVerifying,
			wantNeed: true,
			wantNext: domain.StepStatusRecoveryRequired,
		},
		{
			name:      "仍有未结束执行时不抢占",
			status:    domain.StepStatusDeveloping,
			activeRun: true,
			wantNeed:  false,
			wantNext:  domain.StepStatusDeveloping,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			step := launchStep(t, task.ID, 1, testCase.status)
			needed, next := domain.DecideStepOccupancyRecovery(
				domain.TaskLaunchStepOccupancyInput{
					Step:              step,
					ActiveWorkflowRun: testCase.activeRun,
				},
			)
			if needed != testCase.wantNeed {
				t.Fatalf("是否需要收敛应为 %v，实际 %v", testCase.wantNeed, needed)
			}
			if next != testCase.wantNext {
				t.Fatalf("目标状态应为 %s，实际 %s", testCase.wantNext, next)
			}
		})
	}
}

func TestTaskLaunchStageValidateRejectsUnknownStage(t *testing.T) {
	t.Parallel()
	if err := domain.TaskLaunchStageWorkflow.Validate(); err != nil {
		t.Fatalf("已知阶段不应失败: %v", err)
	}
	if err := domain.TaskLaunchStage("unknown").Validate(); !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("未知阶段必须返回字段错误，实际 %v", err)
	}
}
