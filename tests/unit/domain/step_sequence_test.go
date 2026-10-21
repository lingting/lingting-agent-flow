package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// prerequisiteSteps 构造一组按序号排列的开发步骤。
//
// 每个步骤都带完整角色与验证规格：前置条件校验只关心序号与状态，但步骤本身必须
// 是合法实体，否则测试会因为无关的字段校验失败而失去意义。
func prerequisiteSteps(t *testing.T, count int) []domain.Step {
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
	steps := make([]domain.Step, 0, count)
	for index := 0; index < count; index++ {
		step, err := domain.NewAgentStep(
			task.ID,
			index+1,
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
		steps = append(steps, step)
	}
	return steps
}

// TestCheckStepPrerequisitesRequiresPreviousCompleted 验证前序步骤必须已完成。
func TestCheckStepPrerequisitesRequiresPreviousCompleted(t *testing.T) {
	t.Parallel()
	steps := prerequisiteSteps(t, 3)
	// 第 3 步的前序步骤尚未完成时必须拒绝。
	err := domain.CheckStepPrerequisites(steps, steps[2])
	if err == nil {
		t.Fatal("前序步骤未完成时不应允许执行后续步骤")
	}
	if !errors.Is(err, domain.ErrStepExecutionBlocked) {
		t.Fatalf("拒绝必须可识别为状态阻断: %v", err)
	}
	var prerequisite domain.StepPrerequisiteError
	if !errors.As(err, &prerequisite) {
		t.Fatalf("拒绝必须携带未满足的前序步骤: %v", err)
	}
	if prerequisite.Sequence != 1 || prerequisite.StepID != steps[0].ID {
		t.Fatalf("应报告序号最小的未完成前序步骤，实际 %+v", prerequisite)
	}
}

// TestCheckStepPrerequisitesAllowsCompletedAndSkipped 验证已完成或被跳过的前序步骤放行。
func TestCheckStepPrerequisitesAllowsCompletedAndSkipped(t *testing.T) {
	t.Parallel()
	steps := prerequisiteSteps(t, 3)
	steps[0].Status = domain.StepStatusCompleted
	steps[1].Status = domain.StepStatusSkipped
	if err := domain.CheckStepPrerequisites(steps, steps[2]); err != nil {
		t.Fatalf("前序步骤已完成或被跳过时应允许执行: %v", err)
	}
}

// TestCheckStepPrerequisitesRejectsFailedPrevious 验证失败的前序步骤阻止后续执行。
func TestCheckStepPrerequisitesRejectsFailedPrevious(t *testing.T) {
	t.Parallel()
	steps := prerequisiteSteps(t, 2)
	steps[0].Status = domain.StepStatusFailed
	err := domain.CheckStepPrerequisites(steps, steps[1])
	if err == nil {
		t.Fatal("失败的前序步骤不应允许执行后续步骤")
	}
	if !errors.Is(err, domain.ErrStepExecutionBlocked) {
		t.Fatalf("拒绝必须可识别为状态阻断: %v", err)
	}
}

// TestNextStepToExecuteSkipsNonDevelopSteps 验证顺序执行只选择开发步骤。
func TestNextStepToExecuteSkipsNonDevelopSteps(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	task, err := domain.NewTask("requirement-1", "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	prepare, err := domain.NewWorkflowStep(
		task.ID,
		1,
		domain.StepTypePrepare,
		"准备环境",
		"创建任务容器",
		domain.VerificationSpec{},
		1,
		now,
	)
	if err != nil {
		t.Fatalf("构造准备步骤失败: %v", err)
	}
	prepare.Status = domain.StepStatusCompleted
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
	develop, err := domain.NewAgentStep(
		task.ID,
		2,
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
		t.Fatalf("构造开发步骤失败: %v", err)
	}
	all := []domain.Step{prepare, develop}
	step, ok, nextErr := domain.NextStepToExecute(all)
	if nextErr != nil {
		t.Fatalf("不应返回错误: %v", nextErr)
	}
	if !ok {
		t.Fatal("应当选出待执行的开发步骤")
	}
	if step.ID != develop.ID {
		t.Fatalf("选出的步骤不符: %s", step.ID)
	}
}

// TestNextStepToExecuteStopsOnRecoveryRequired 验证结果不确定的步骤阻止自动推进。
func TestNextStepToExecuteStopsOnRecoveryRequired(t *testing.T) {
	t.Parallel()
	steps := prerequisiteSteps(t, 2)
	steps[1].Status = domain.StepStatusRecoveryRequired
	_, ok, err := domain.NextStepToExecute(steps)
	if err == nil {
		t.Fatal("存在需要恢复核验的步骤时必须停止")
	}
	if ok {
		t.Fatal("存在需要恢复核验的步骤时不应选出可执行步骤")
	}
	if !errors.Is(err, domain.ErrStepExecutionBlocked) {
		t.Fatalf("停止原因必须可识别: %v", err)
	}
}

// TestNextStepToExecuteRejectsRunningStep 验证正在执行的步骤阻止重复推进。
func TestNextStepToExecuteRejectsRunningStep(t *testing.T) {
	t.Parallel()
	steps := prerequisiteSteps(t, 1)
	steps[0].Status = domain.StepStatusRunning
	_, ok, err := domain.NextStepToExecute(steps)
	if err == nil {
		t.Fatal("步骤已被占用时必须停止")
	}
	if ok {
		t.Fatal("步骤已被占用时不应选出可执行步骤")
	}
}

// TestNextStepToExecuteReturnsDoneWhenAllCompleted 验证全部完成后没有可执行步骤。
func TestNextStepToExecuteReturnsDoneWhenAllCompleted(t *testing.T) {
	t.Parallel()
	steps := prerequisiteSteps(t, 2)
	for index := range steps {
		steps[index].Status = domain.StepStatusCompleted
	}
	_, ok, err := domain.NextStepToExecute(steps)
	if err != nil {
		t.Fatalf("全部完成时不应返回错误: %v", err)
	}
	if ok {
		t.Fatal("全部完成时不应再有可执行步骤")
	}
}

// TestGitWorktreeStateValidate 验证 Git 状态的自洽性校验。
func TestGitWorktreeStateValidate(t *testing.T) {
	t.Parallel()
	if err := (domain.GitWorktreeState{StatusRead: true, Head: ""}).Validate(); err != nil {
		t.Fatalf("采集成功且 HEAD 为空应允许（空仓库场景）: %v", err)
	}
	if err := (domain.GitWorktreeState{StatusRead: false, StatusError: "失败"}).Validate(); err != nil {
		t.Fatalf("采集失败只带错误摘要应允许: %v", err)
	}
	if err := (domain.GitWorktreeState{StatusRead: false, Head: "abc"}).Validate(); err == nil {
		t.Fatal("采集失败时不允许携带 HEAD")
	}
	if err := (domain.GitWorktreeState{StatusRead: true, Modified: -1}).Validate(); err == nil {
		t.Fatal("变更计数不能为负数")
	}
}

// TestGitWorktreeStateSummary 验证摘要能区分干净、脏与读取失败。
func TestGitWorktreeStateSummary(t *testing.T) {
	t.Parallel()
	clean := domain.GitWorktreeState{StatusRead: true}
	if summary := clean.Summary(); summary == "" {
		t.Fatal("干净工作区必须有摘要")
	}
	dirty := domain.GitWorktreeState{StatusRead: true, Dirty: true, Modified: 2, Added: 1}
	if summary := dirty.Summary(); summary == "" {
		t.Fatal("脏工作区必须有摘要")
	}
	failed := domain.GitWorktreeState{StatusRead: false, StatusError: "命令失败"}
	if summary := failed.Summary(); summary == "" {
		t.Fatal("读取失败必须有摘要")
	}
	if failed.Summary() == clean.Summary() {
		t.Fatal("读取失败与干净工作区的摘要不能相同")
	}
}

// TestAttemptSetGitState 验证执行前后的 Git 状态被写入尝试。
func TestAttemptSetGitState(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	attempt, err := domain.NewAttempt("task-1", "step-1", 1, now)
	if err != nil {
		t.Fatalf("构造尝试失败: %v", err)
	}
	before := domain.GitWorktreeState{
		StatusRead: true,
		Branch:     "agent/task-1",
		Head:       "1111111111111111111111111111111111111111",
	}
	if err := attempt.SetGitBefore(before); err != nil {
		t.Fatalf("记录执行前状态失败: %v", err)
	}
	if attempt.GitBranch != before.Branch {
		t.Fatalf("分支应同步写入尝试: %s", attempt.GitBranch)
	}
	after := domain.GitWorktreeState{
		StatusRead: true,
		Branch:     "agent/task-1",
		Head:       "2222222222222222222222222222222222222222",
		Dirty:      true,
		Modified:   3,
	}
	if err := attempt.SetGitAfter(after); err != nil {
		t.Fatalf("记录执行后状态失败: %v", err)
	}
	if attempt.GitAfter == nil || !attempt.GitAfter.Dirty {
		t.Fatalf("执行后状态未写入: %+v", attempt.GitAfter)
	}
	if err := attempt.Validate(); err != nil {
		t.Fatalf("带 Git 状态的尝试必须通过校验: %v", err)
	}
}

// TestAttemptRejectsInvalidGitBranch 验证非法分支名不会被写入尝试。
func TestAttemptRejectsInvalidGitBranch(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	attempt, err := domain.NewAttempt("task-1", "step-1", 1, now)
	if err != nil {
		t.Fatalf("构造尝试失败: %v", err)
	}
	state := domain.GitWorktreeState{StatusRead: true, Branch: "-bad branch"}
	if err := attempt.SetGitBefore(state); err == nil {
		t.Fatal("非法分支名不应被接受")
	}
}
