package domain_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// planFixture 是一套可构造合法计划的领域测试数据。
type planFixture struct {
	task         domain.Task
	planner      domain.Role
	developer    domain.Role
	verification domain.VerificationSpec
	now          time.Time
}

// newPlanFixture 构造计划生成所需的合法任务、规划角色与任务级必需检查。
func newPlanFixture(t *testing.T) planFixture {
	t.Helper()
	now := time.Now().UTC()
	planner, err := domain.NewRole(
		"规划师",
		domain.RoleTypePlanner,
		"provider/planner",
		"你是规划角色",
		domain.DefaultToolsPolicy(domain.RoleTypePlanner),
		now,
	)
	if err != nil {
		t.Fatalf("构造规划角色失败: %v", err)
	}
	developer, err := domain.NewRole(
		"开发者",
		domain.RoleTypeDeveloper,
		"provider/developer",
		"你是开发角色",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		now,
	)
	if err != nil {
		t.Fatalf("构造开发角色失败: %v", err)
	}
	task, err := domain.NewTask("requirement-1", "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.ContainerID = "container-1"
	task.Config = domain.TaskConfig{
		Goal:            "完成本次需求",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		PlannerRoleID:   planner.ID,
		DeveloperRoleID: developer.ID,
		Verification: domain.VerificationSpec{
			AcceptanceCriteria: []string{"任务级验收条件"},
			Commands: []domain.VerificationCommand{
				{Name: "build", Command: "go", Args: []string{"build", "./..."}, Required: true},
			},
		},
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("任务不合法: %v", err)
	}
	return planFixture{
		task:         task,
		planner:      planner,
		developer:    developer,
		verification: task.Config.Verification,
		now:          now,
	}
}

// draft 构造包含 count 个步骤的合法计划草稿。
func (f planFixture) draft(count int) domain.PlanDraft {
	steps := make([]domain.PlanStepDraft, 0, count)
	for index := 0; index < count; index++ {
		steps = append(steps, domain.PlanStepDraft{
			Identifier:         fmt.Sprintf("step-%d", index+1),
			Title:              fmt.Sprintf("实现第 %d 步", index+1),
			Objective:          "按需求实现",
			Instructions:       "只修改当前工作区",
			AcceptanceCriteria: []string{"功能可用"},
		})
	}
	return domain.PlanDraft{Summary: "分步实现需求", Steps: steps}
}

// TestBuildPlanAcceptsValidDraft 校验合法草稿被转换为序号连续的开发步骤。
func TestBuildPlanAcceptsValidDraft(t *testing.T) {
	t.Parallel()
	fixture := newPlanFixture(t)

	plan, err := domain.BuildPlan(
		fixture.task,
		fixture.draft(3),
		fixture.planner,
		fixture.developer,
		fixture.verification,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造计划失败: %v", err)
	}
	// 序号 1 是规划阶段步骤，其后是 3 个开发步骤。
	if len(plan.Steps) != 4 {
		t.Fatalf("步骤数量不符: %d", len(plan.Steps))
	}
	if plan.Steps[0].Type != domain.StepTypePlan {
		t.Fatalf("首个步骤必须是规划阶段: %s", plan.Steps[0].Type)
	}
	if plan.Steps[0].RoleID != fixture.planner.ID {
		t.Fatalf("规划阶段步骤必须绑定规划角色: %s", plan.Steps[0].RoleID)
	}
	for index, step := range plan.Steps {
		if step.Sequence != index+1 {
			t.Fatalf("步骤序号不连续: %d", step.Sequence)
		}
		if index == 0 {
			continue
		}
		if step.Type != domain.StepTypeDevelop {
			t.Fatalf("步骤类型必须是开发步骤: %s", step.Type)
		}
		if step.RoleID != fixture.developer.ID {
			t.Fatalf("开发步骤必须绑定开发角色: %s", step.RoleID)
		}
		if step.Status != domain.StepStatusPending {
			t.Fatalf("步骤初始状态不符: %s", step.Status)
		}
		if !step.VerificationSpec.HasCommands() {
			t.Fatalf("任务级必需命令未合并进步骤 %d", step.Sequence)
		}
		if len(step.VerificationSpec.AcceptanceCriteria) != 2 {
			t.Fatalf(
				"任务级与步骤级验收条件应合并去重，实际 %d 条",
				len(step.VerificationSpec.AcceptanceCriteria),
			)
		}
	}
}

// TestBuildPlanRejectsInvalidDrafts 校验各类非法草稿都被拒绝。
func TestBuildPlanRejectsInvalidDrafts(t *testing.T) {
	t.Parallel()
	fixture := newPlanFixture(t)
	cases := []struct {
		name  string
		draft func() domain.PlanDraft
	}{
		{
			name:  "空计划",
			draft: func() domain.PlanDraft { return domain.PlanDraft{} },
		},
		{
			name: "步骤数超过上限",
			draft: func() domain.PlanDraft {
				return fixture.draft(fixture.task.Config.PlanStepLimit() + 1)
			},
		},
		{
			name: "缺少步骤标识",
			draft: func() domain.PlanDraft {
				draft := fixture.draft(1)
				draft.Steps[0].Identifier = ""
				return draft
			},
		},
		{
			name: "步骤标识重复",
			draft: func() domain.PlanDraft {
				draft := fixture.draft(2)
				draft.Steps[1].Identifier = draft.Steps[0].Identifier
				return draft
			},
		},
		{
			name: "缺少标题",
			draft: func() domain.PlanDraft {
				draft := fixture.draft(1)
				draft.Steps[0].Title = "   "
				return draft
			},
		},
		{
			name: "缺少目标",
			draft: func() domain.PlanDraft {
				draft := fixture.draft(1)
				draft.Steps[0].Objective = ""
				return draft
			},
		},
		{
			name: "缺少验收条件",
			draft: func() domain.PlanDraft {
				draft := fixture.draft(1)
				draft.Steps[0].AcceptanceCriteria = nil
				return draft
			},
		},
		{
			name: "验收条件为空字符串",
			draft: func() domain.PlanDraft {
				draft := fixture.draft(1)
				draft.Steps[0].AcceptanceCriteria = []string{"  "}
				return draft
			},
		},
		{
			name: "非法验证命令",
			draft: func() domain.PlanDraft {
				draft := fixture.draft(1)
				draft.Steps[0].VerificationCommands = []domain.VerificationCommand{
					{Name: "lint", Command: "go build", Required: true},
				}
				return draft
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := domain.BuildPlan(
				fixture.task,
				testCase.draft(),
				fixture.planner,
				fixture.developer,
				fixture.verification,
				fixture.now,
			); err == nil {
				t.Fatalf("非法计划未被拒绝")
			} else if !errors.Is(err, domain.ErrPlanInvalid) {
				t.Fatalf("错误类别不符: %v", err)
			}
		})
	}
}

// TestBuildPlanRejectsWrongRoleType 校验角色类型不符时拒绝生成计划。
func TestBuildPlanRejectsWrongRoleType(t *testing.T) {
	t.Parallel()
	fixture := newPlanFixture(t)
	developer, err := domain.NewRole(
		"开发者",
		domain.RoleTypeDeveloper,
		"provider/developer",
		"你是开发角色",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造开发角色失败: %v", err)
	}
	if _, err := domain.BuildPlan(
		fixture.task,
		fixture.draft(1),
		developer,
		fixture.developer,
		fixture.verification,
		fixture.now,
	); !errors.Is(err, domain.ErrPlanInvalid) {
		t.Fatalf("规划角色类型不符时错误类别不符: %v", err)
	}
	if _, err := domain.BuildPlan(
		fixture.task,
		fixture.draft(1),
		fixture.planner,
		fixture.planner,
		fixture.verification,
		fixture.now,
	); !errors.Is(err, domain.ErrPlanInvalid) {
		t.Fatalf("开发角色类型不符时错误类别不符: %v", err)
	}
}

// TestCheckPlanGenerationStartAcceptsAllowedStatuses 校验允许规划的状态都被接受。
func TestCheckPlanGenerationStartAcceptsAllowedStatuses(t *testing.T) {
	t.Parallel()
	fixture := newPlanFixture(t)
	for _, status := range []domain.TaskStatus{
		domain.TaskStatusQueued,
		domain.TaskStatusPreparing,
		domain.TaskStatusFailed,
		domain.TaskStatusPaused,
		domain.TaskStatusRecoveryRequired,
	} {
		task := fixture.task
		task.Status = status
		if err := domain.CheckPlanGenerationStart(task, nil); err != nil {
			t.Fatalf("状态 %s 应允许规划: %v", status, err)
		}
		if !status.CanStartPlanning() {
			t.Fatalf("状态 %s 的准入判定与校验不一致", status)
		}
	}
}

// TestCheckPlanGenerationStartRejects 校验各拒绝条件返回状态错误。
func TestCheckPlanGenerationStartRejects(t *testing.T) {
	t.Parallel()
	fixture := newPlanFixture(t)
	step, err := domain.NewAgentStep(
		fixture.task.ID,
		1,
		domain.StepTypeDevelop,
		"实现",
		"按需求实现",
		"",
		func() domain.Role {
			role, roleErr := domain.NewRole(
				"开发者",
				domain.RoleTypeDeveloper,
				"provider/developer",
				"你是开发角色",
				domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
				fixture.now,
			)
			if roleErr != nil {
				t.Fatalf("构造开发角色失败: %v", roleErr)
			}
			return role
		}(),
		fixture.verification,
		domain.RetryConfig{MaxAttempts: 1},
		fixture.now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	cases := []struct {
		name  string
		task  func() domain.Task
		steps []domain.Step
	}{
		{
			name: "已删除任务",
			task: func() domain.Task {
				task := fixture.task
				now := fixture.now
				task.DeletedAt = &now
				return task
			},
		},
		{
			name: "已请求取消",
			task: func() domain.Task {
				task := fixture.task
				now := fixture.now
				task.CancelRequested = true
				task.CancelRequestedAt = &now
				return task
			},
		},
		{
			name: "状态不允许规划",
			task: func() domain.Task {
				task := fixture.task
				task.Status = domain.TaskStatusDeveloping
				return task
			},
		},
		{
			name: "缺少容器",
			task: func() domain.Task {
				task := fixture.task
				task.ContainerID = ""
				return task
			},
		},
		{
			name:  "已有计划",
			task:  func() domain.Task { return fixture.task },
			steps: []domain.Step{step},
		},
		{
			name: "未配置规划角色",
			task: func() domain.Task {
				task := fixture.task
				task.Config.PlannerRoleID = ""
				return task
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := domain.CheckPlanGenerationStart(testCase.task(), testCase.steps)
			if err == nil {
				t.Fatalf("非法前置条件未被拒绝")
			}
			if !errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("错误类别不符: %v", err)
			}
		})
	}
}

// TestDecidePlanGenerationFailure 校验调用结论到任务状态的映射。
func TestDecidePlanGenerationFailure(t *testing.T) {
	t.Parallel()
	cases := map[domain.StepExecutionOutcome]domain.TaskStatus{
		domain.StepExecutionCancelled:   domain.TaskStatusCancelling,
		domain.StepExecutionInterrupted: domain.TaskStatusRecoveryRequired,
		domain.StepExecutionFailed:      domain.TaskStatusFailed,
		domain.StepExecutionTimedOut:    domain.TaskStatusFailed,
		domain.StepExecutionCompleted:   domain.TaskStatusFailed,
	}
	for outcome, expected := range cases {
		if actual := domain.DecidePlanGenerationFailure(outcome); actual != expected {
			t.Fatalf("结论 %s 映射为 %s，期望 %s", outcome, actual, expected)
		}
	}
}

// TestDecidePlanGenerationRecovery 校验残留现场的收敛结论。
func TestDecidePlanGenerationRecovery(t *testing.T) {
	t.Parallel()
	if actual := domain.DecidePlanGenerationRecovery(true); actual != domain.TaskStatusPlanned {
		t.Fatalf("已有步骤时应收敛为 planned，实际 %s", actual)
	}
	if actual := domain.DecidePlanGenerationRecovery(false); actual != domain.TaskStatusRecoveryRequired {
		t.Fatalf("没有步骤时应收敛为 recovery_required，实际 %s", actual)
	}
}

// TestDecideCancellationRecovery 校验残留 cancelling 任务的收敛结论。
//
// 三条规则顺序固定：未结束的执行记录优先（不覆盖在途现场），其次是缺少取消请求
// （无法确认流程真的走过），最后才判定取消已生效。
func TestDecideCancellationRecovery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		input    domain.CancellationRecoveryInput
		expected domain.TaskStatus
	}{
		{
			name:     "已记录取消请求且没有未结束执行时收敛为已取消",
			input:    domain.CancellationRecoveryInput{CancelRequested: true},
			expected: domain.TaskStatusCancelled,
		},
		{
			name:     "缺少取消请求时收敛为需要核验",
			input:    domain.CancellationRecoveryInput{},
			expected: domain.TaskStatusRecoveryRequired,
		},
		{
			name: "存在未结束执行记录时收敛为需要核验",
			input: domain.CancellationRecoveryInput{
				CancelRequested: true,
				ActiveRunID:     "run-1",
			},
			expected: domain.TaskStatusRecoveryRequired,
		},
		{
			name: "未结束执行记录优先于取消请求",
			input: domain.CancellationRecoveryInput{
				ActiveRunID: "run-1",
			},
			expected: domain.TaskStatusRecoveryRequired,
		},
	}
	for _, testCase := range cases {
		if actual := domain.DecideCancellationRecovery(testCase.input); actual != testCase.expected {
			t.Fatalf("%s: 期望 %s，实际 %s", testCase.name, testCase.expected, actual)
		}
	}
}
