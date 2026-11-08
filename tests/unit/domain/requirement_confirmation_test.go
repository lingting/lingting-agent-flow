// Package domain_test 提供方案确认相关领域规则的单元测试。
//
// 方案确认只做三件事：把已通过结构化校验的实施方案转换成带连续顺序的正式任务草案、
// 由需求分析快照推导确定性的需求标题与描述，并校验确认生成需求的字段组合一致性。
// 三者都是纯函数，因此这里直接构造结构体覆盖合法与非法情形。
package domain_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// planTask 构造一个合法的任务草案。
func planTask(id string, dependsOn []string) domain.ImplementationPlanTask {
	return domain.ImplementationPlanTask{
		ID:                 id,
		Title:              "任务 " + id,
		Description:        "实现 " + id,
		DependsOn:          dependsOn,
		AcceptanceCriteria: []string{"可以独立验收"},
	}
}

// planOf 用给定任务构造实施方案。
func planOf(tasks ...domain.ImplementationPlanTask) domain.ImplementationPlan {
	return domain.ImplementationPlan{Summary: "总体实施思路", Tasks: tasks}
}

// TestConfirmableTasksAcceptsSingleTask 验证只有 1 个任务的方案可以确认，顺序为 1。
func TestConfirmableTasksAcceptsSingleTask(t *testing.T) {
	t.Parallel()
	planned, err := planOf(planTask("only", nil)).ConfirmableTasks()
	if err != nil {
		t.Fatalf("单任务方案应可确认: %v", err)
	}
	if len(planned) != 1 || planned[0].Sequence != 1 {
		t.Fatalf("单任务方案顺序错误: %+v", planned)
	}
}

// TestConfirmableTasksDerivesSequentialOrder 验证顺序由数组位置推导且连续唯一。
func TestConfirmableTasksDerivesSequentialOrder(t *testing.T) {
	t.Parallel()
	plan := planOf(
		planTask("a", nil),
		planTask("b", []string{"a"}),
		planTask("c", []string{"a", "b"}),
	)
	planned, err := plan.ConfirmableTasks()
	if err != nil {
		t.Fatalf("合法拓扑顺序应可确认: %v", err)
	}
	for index, task := range planned {
		if task.Sequence != index+1 {
			t.Fatalf("第 %d 个任务顺序应为 %d，实际 %d", index+1, index+1, task.Sequence)
		}
	}
	if planned[2].Draft.ID != "c" {
		t.Fatalf("任务草案顺序与数组顺序不一致: %+v", planned)
	}
}

// TestConfirmableTasksRejectsInvalidPlan 验证各类非法方案都不能生成正式任务。
func TestConfirmableTasksRejectsInvalidPlan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		plan domain.ImplementationPlan
	}{
		{name: "空任务列表", plan: planOf()},
		{name: "摘要为空", plan: domain.ImplementationPlan{Tasks: []domain.ImplementationPlanTask{planTask("a", nil)}}},
		{
			name: "任务标识重复",
			plan: planOf(planTask("dup", nil), planTask("dup", nil)),
		},
		{
			name: "依赖不存在",
			plan: planOf(planTask("a", []string{"missing"})),
		},
		{
			name: "依赖自引用",
			plan: planOf(planTask("a", []string{"a"})),
		},
		{
			name: "依赖成环",
			plan: planOf(planTask("a", []string{"b"}), planTask("b", []string{"a"})),
		},
		{
			name: "顺序不满足拓扑约定",
			plan: planOf(planTask("later", nil), planTask("first", []string{"later", "later"})),
		},
		{
			name: "标题为空",
			plan: planOf(domain.ImplementationPlanTask{
				ID:                 "a",
				Title:              "   ",
				Description:        "描述",
				AcceptanceCriteria: []string{"可以验收"},
			}),
		},
		{
			name: "描述为空",
			plan: planOf(domain.ImplementationPlanTask{
				ID:                 "a",
				Title:              "标题",
				Description:        "",
				AcceptanceCriteria: []string{"可以验收"},
			}),
		},
		{
			name: "验收条件为空",
			plan: planOf(domain.ImplementationPlanTask{
				ID:                 "a",
				Title:              "标题",
				Description:        "描述",
				AcceptanceCriteria: []string{},
			}),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			planned, err := testCase.plan.ConfirmableTasks()
			if err == nil {
				t.Fatalf("非法方案不应生成正式任务: %+v", planned)
			}
			if !errors.Is(err, domain.ErrInvalidValue) {
				t.Fatalf("非法方案应返回领域校验错误，实际 %v", err)
			}
		})
	}
}

// TestRequirementTitleAndDescriptionFromAnalysis 验证需求标题与描述的推导规则。
func TestRequirementTitleAndDescriptionFromAnalysis(t *testing.T) {
	t.Parallel()
	analysis := domain.RequirementAnalysis{
		Summary:            "补齐方案确认",
		Goals:              []string{"生成正式需求"},
		CurrentState:       []string{"已有规划会话"},
		Scope:              []string{"internal/app"},
		OutOfScope:         []string{"不做调度"},
		Constraints:        []string{"不新增外键"},
		Risks:              []string{"事务边界容易写错"},
		AcceptanceCriteria: []string{"失败不留半成品"},
	}
	title, err := domain.RequirementTitleFromAnalysis(analysis)
	if err != nil {
		t.Fatalf("推导需求标题失败: %v", err)
	}
	if title != "补齐方案确认" {
		t.Fatalf("需求标题应取自分析摘要，实际 %q", title)
	}
	description, err := domain.RequirementDescriptionFromAnalysis(analysis)
	if err != nil {
		t.Fatalf("推导需求描述失败: %v", err)
	}
	for _, expected := range []string{
		"补齐方案确认",
		"目标：",
		"- 生成正式需求",
		"范围：",
		"- internal/app",
		"约束：",
		"- 不新增外键",
		"验收标准：",
		"- 失败不留半成品",
	} {
		if !strings.Contains(description, expected) {
			t.Fatalf("需求描述缺少 %q: %s", expected, description)
		}
	}
	// 同一份分析快照必须得到完全相同的描述：它是确定性摘要，不含时间或随机内容。
	repeat, err := domain.RequirementDescriptionFromAnalysis(analysis)
	if err != nil {
		t.Fatalf("重复推导需求描述失败: %v", err)
	}
	if repeat != description {
		t.Fatalf("需求描述不是确定性摘要:\n%s\n%s", description, repeat)
	}
}

// TestRequirementTitleRejectsEmptySummary 验证空摘要不能作为需求标题。
func TestRequirementTitleRejectsEmptySummary(t *testing.T) {
	t.Parallel()
	analysis := domain.RequirementAnalysis{
		Summary:            "   ",
		Goals:              []string{"目标"},
		CurrentState:       []string{"现状"},
		Scope:              []string{"范围"},
		OutOfScope:         []string{"非目标"},
		Constraints:        []string{"约束"},
		Risks:              []string{"风险"},
		AcceptanceCriteria: []string{"验收"},
	}
	if _, err := domain.RequirementTitleFromAnalysis(analysis); err == nil {
		t.Fatalf("空摘要不应被接受为标题")
	}
}

// confirmedRequirementInput 返回一份完整的确认生成需求输入。
func confirmedRequirementInput(at time.Time) domain.ConfirmedRequirementInput {
	return domain.ConfirmedRequirementInput{
		ProjectID:         "demo-project",
		PlanningSessionID: domain.NewID(),
		Title:             "补齐方案确认",
		Description:       "目标：\n- 生成正式需求",
		Priority:          domain.PriorityNormal,
		AnalysisJSON:      json.RawMessage(`{"summary":"补齐方案确认"}`),
		PlanJSON:          json.RawMessage(`{"summary":"按阶段推进","tasks":[]}`),
		At:                at,
	}
}

// TestNewConfirmedRequirementRequiresCompleteConfirmation 验证确认生成的需求必须完整。
func TestNewConfirmedRequirementRequiresCompleteConfirmation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	requirement, err := domain.NewConfirmedRequirement(confirmedRequirementInput(at))
	if err != nil {
		t.Fatalf("完整输入应可生成需求: %v", err)
	}
	if requirement.Status != domain.RequirementStatusQueued {
		t.Fatalf("初始状态应沿用需求领域既有约定 queued，实际 %s", requirement.Status)
	}
	if requirement.ProjectID != "demo-project" {
		t.Fatalf("项目标识未写入: %+v", requirement)
	}
	if requirement.PlanningSessionID == nil || requirement.ConfirmedAt == nil {
		t.Fatalf("规划来源与确认时间必须写入: %+v", requirement)
	}

	cases := []struct {
		name   string
		mutate func(*domain.ConfirmedRequirementInput)
	}{
		{
			name:   "缺少项目",
			mutate: func(input *domain.ConfirmedRequirementInput) { input.ProjectID = "" },
		},
		{
			name:   "缺少规划会话",
			mutate: func(input *domain.ConfirmedRequirementInput) { input.PlanningSessionID = "" },
		},
		{
			name:   "缺少分析快照",
			mutate: func(input *domain.ConfirmedRequirementInput) { input.AnalysisJSON = nil },
		},
		{
			name:   "缺少方案快照",
			mutate: func(input *domain.ConfirmedRequirementInput) { input.PlanJSON = nil },
		},
		{
			name:   "快照不是合法 JSON",
			mutate: func(input *domain.ConfirmedRequirementInput) { input.PlanJSON = json.RawMessage(`{`) },
		},
		{
			name:   "快照是 null",
			mutate: func(input *domain.ConfirmedRequirementInput) { input.AnalysisJSON = json.RawMessage(`null`) },
		},
		{
			name:   "标题为空",
			mutate: func(input *domain.ConfirmedRequirementInput) { input.Title = "  " },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := confirmedRequirementInput(at)
			testCase.mutate(&input)
			if _, err := domain.NewConfirmedRequirement(input); err == nil {
				t.Fatalf("不完整的确认输入不应被接受")
			}
		})
	}
}

// TestNewConfirmedRequirementNormalizesProjectID 验证项目标识被规范化为小写形式。
func TestNewConfirmedRequirementNormalizesProjectID(t *testing.T) {
	t.Parallel()
	input := confirmedRequirementInput(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC))
	input.ProjectID = "  Demo-Project  "
	requirement, err := domain.NewConfirmedRequirement(input)
	if err != nil {
		t.Fatalf("项目标识应被规范化而不是拒绝: %v", err)
	}
	if requirement.ProjectID != "demo-project" {
		t.Fatalf("项目标识未规范化为小写: %q", requirement.ProjectID)
	}
}

// TestRequirementValidateRejectsIncompleteConfirmation 验证确认字段组合不一致时被拒绝。
func TestRequirementValidateRejectsIncompleteConfirmation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	sessionID := domain.NewID()
	base := domain.Requirement{
		ID:                domain.NewID(),
		Title:             "标题",
		Description:       "描述",
		Priority:          domain.PriorityNormal,
		Status:            domain.RequirementStatusQueued,
		Source:            domain.RequirementSourceManual,
		ProjectID:         "demo-project",
		PlanningSessionID: &sessionID,
		FinalAnalysisJSON: json.RawMessage(`{"summary":"标题"}`),
		FinalPlanJSON:     json.RawMessage(`{"summary":"方案","tasks":[]}`),
		ConfirmedAt:       &at,
		CreatedAt:         at,
		UpdatedAt:         at,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("完整的确认字段应通过校验: %v", err)
	}
	cases := map[string]func(requirement *domain.Requirement){
		"缺少项目":   func(requirement *domain.Requirement) { requirement.ProjectID = "" },
		"缺少确认时间": func(requirement *domain.Requirement) { requirement.ConfirmedAt = nil },
		"缺少分析快照": func(requirement *domain.Requirement) { requirement.FinalAnalysisJSON = nil },
		"缺少方案快照": func(requirement *domain.Requirement) { requirement.FinalPlanJSON = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requirement := base
			mutate(&requirement)
			if err := requirement.Validate(); err == nil {
				t.Fatalf("不完整的确认字段应被拒绝: %+v", requirement)
			}
		})
	}
}

// TestRequirementRejectsConfirmationFieldsWithoutPlanningSource 验证手工需求不得带确认字段。
func TestRequirementRejectsConfirmationFieldsWithoutPlanningSource(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	requirement, err := domain.NewRequirement("手工需求", "手工需求描述", domain.PriorityNormal, at)
	if err != nil {
		t.Fatalf("构造手工需求失败: %v", err)
	}
	requirement.ProjectID = "demo-project"
	if err := requirement.Validate(); err == nil {
		t.Fatalf("没有规划来源却带项目关联的需求应被拒绝")
	}
}

// TestTaskRequirementSequence 验证任务的需求内顺序必须为正数，并且写入是幂等的。
func TestTaskRequirementSequence(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	requirementID := domain.NewID()
	task, err := domain.NewTask(requirementID, "ssh://git.example.com/team/repo", "main", "", at)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if task.RequirementSequence != nil {
		t.Fatalf("新建任务的顺序应为空，实际 %+v", task.RequirementSequence)
	}
	if err := task.SetRequirementSequence(0); err == nil {
		t.Fatalf("顺序 0 应被拒绝")
	}
	if err := task.SetRequirementSequence(2); err != nil {
		t.Fatalf("设置需求内顺序失败: %v", err)
	}
	if task.RequirementSequence == nil || *task.RequirementSequence != 2 {
		t.Fatalf("需求内顺序未写入: %+v", task.RequirementSequence)
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("带顺序的任务应通过校验: %v", err)
	}
	invalid := -1
	task.RequirementSequence = &invalid
	if err := task.Validate(); err == nil {
		t.Fatalf("负数顺序应被拒绝")
	}
}
