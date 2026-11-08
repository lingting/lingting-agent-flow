package sqlite_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// mustConfirmedRequirement 写入一条确认生成的正式需求。
func mustConfirmedRequirement(
	t *testing.T,
	db *sqlite.DB,
	projectID string,
	title string,
	at time.Time,
) domain.Requirement {
	t.Helper()
	requirement, err := domain.NewConfirmedRequirement(domain.ConfirmedRequirementInput{
		ProjectID:         projectID,
		PlanningSessionID: domain.NewID(),
		Title:             title,
		Description:       "确认生成的需求描述",
		Priority:          domain.PriorityNormal,
		AnalysisJSON:      json.RawMessage(`{"summary":"` + title + `"}`),
		PlanJSON:          json.RawMessage(`{"summary":"按阶段推进","tasks":[]}`),
		At:                at,
	})
	if err != nil {
		t.Fatalf("构造确认生成的需求失败: %v", err)
	}
	if err := db.CreateRequirement(testContext(t), requirement); err != nil {
		t.Fatalf("写入确认生成的需求失败: %v", err)
	}
	return requirement
}

// TestConfirmedRequirementRoundTrip 验证确认生成需求的规划字段可以独立读回。
func TestConfirmedRequirementRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	at := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)
	requirement := mustConfirmedRequirement(t, db, "demo-project", "补齐方案确认", at)

	loaded, err := db.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if loaded.ProjectID != "demo-project" {
		t.Fatalf("项目关联未保存: %q", loaded.ProjectID)
	}
	if loaded.PlanningSessionID == nil || *loaded.PlanningSessionID != *requirement.PlanningSessionID {
		t.Fatalf("规划会话关联未保存: %+v", loaded.PlanningSessionID)
	}
	if string(loaded.FinalAnalysisJSON) != string(requirement.FinalAnalysisJSON) {
		t.Fatalf("需求分析快照未原样保存: %s", loaded.FinalAnalysisJSON)
	}
	if string(loaded.FinalPlanJSON) != string(requirement.FinalPlanJSON) {
		t.Fatalf("实施方案快照未原样保存: %s", loaded.FinalPlanJSON)
	}
	if loaded.ConfirmedAt == nil || !loaded.ConfirmedAt.Equal(at) {
		t.Fatalf("确认时间未保存: %+v", loaded.ConfirmedAt)
	}
	if loaded.DeletedAt != nil {
		t.Fatalf("新建需求不应带删除时间: %+v", loaded.DeletedAt)
	}
	// 需求状态没有取值白名单：未知取值必须能写入并原样读回。
	if loaded.Status != domain.RequirementStatusQueued {
		t.Fatalf("初始状态应沿用领域约定 queued，实际 %s", loaded.Status)
	}
}

// TestManualRequirementKeepsEmptyConfirmationFields 验证历史手工需求没有规划来源。
func TestManualRequirementKeepsEmptyConfirmationFields(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)

	loaded, err := db.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if loaded.ProjectID != "" || loaded.PlanningSessionID != nil {
		t.Fatalf("手工需求不应带项目或规划来源: %+v", loaded)
	}
	if len(loaded.FinalAnalysisJSON) != 0 || len(loaded.FinalPlanJSON) != 0 {
		t.Fatalf("手工需求不应带规划快照: %+v", loaded)
	}
	if loaded.ConfirmedAt != nil {
		t.Fatalf("手工需求不应带确认时间: %+v", loaded.ConfirmedAt)
	}
}

// mustSequencedTask 写入一个带需求内顺序的任务。
func mustSequencedTask(
	t *testing.T,
	db *sqlite.DB,
	requirementID domain.ID,
	sequence int,
) domain.Task {
	t.Helper()
	task, err := domain.NewTask(requirementID, "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := task.SetRequirementSequence(sequence); err != nil {
		t.Fatalf("设置需求内顺序失败: %v", err)
	}
	if err := db.CreateTask(testContext(t), task); err != nil {
		t.Fatalf("写入带顺序的任务失败: %v", err)
	}
	return task
}

// TestRequirementSequenceUniquePerRequirement 验证同一需求内的顺序唯一。
func TestRequirementSequenceUniquePerRequirement(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	first := mustSequencedTask(t, db, requirement.ID, 1)
	if first.RequirementSequence == nil || *first.RequirementSequence != 1 {
		t.Fatalf("任务顺序未保存: %+v", first.RequirementSequence)
	}

	duplicate, err := domain.NewTask(requirement.ID, "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := duplicate.SetRequirementSequence(1); err != nil {
		t.Fatalf("设置需求内顺序失败: %v", err)
	}
	if err := db.CreateTask(ctx, duplicate); err == nil {
		t.Fatalf("同一需求内的重复顺序应被唯一索引拒绝")
	} else if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复顺序应返回冲突错误，实际 %v", err)
	}

	// 不同需求可以有相同顺序。
	other := mustRequirement(t, db)
	mustSequencedTask(t, db, other.ID, 1)

	// 历史任务没有需求内顺序：多个 NULL 不参与唯一索引。
	legacyA := mustTask(t, db, requirement.ID)
	legacyB := mustTask(t, db, requirement.ID)
	if legacyA.RequirementSequence != nil || legacyB.RequirementSequence != nil {
		t.Fatalf("历史任务不应带需求内顺序")
	}
}

// TestTaskUpdatesKeepRequirementSequence 验证任务更新不会丢失需求内顺序。
func TestTaskUpdatesKeepRequirementSequence(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustSequencedTask(t, db, requirement.ID, 3)

	task.FailureSummary = "阶段性说明"
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	loaded, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.RequirementSequence == nil || *loaded.RequirementSequence != 3 {
		t.Fatalf("整行更新丢失了需求内顺序: %+v", loaded.RequirementSequence)
	}

	if err := loaded.Transition(domain.TaskStatusPreparing, time.Now().UTC()); err != nil {
		t.Fatalf("推进任务状态失败: %v", err)
	}
	if err := db.UpdateTaskIfStatus(
		ctx,
		*loaded,
		[]domain.TaskStatus{domain.TaskStatusQueued},
	); err != nil {
		t.Fatalf("条件更新任务失败: %v", err)
	}
	updated, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if updated.RequirementSequence == nil || *updated.RequirementSequence != 3 {
		t.Fatalf("条件更新丢失了需求内顺序: %+v", updated.RequirementSequence)
	}
}

// TestListRequirementsByProject 验证项目需求列表的筛选与排序是确定性的。
func TestListRequirementsByProject(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	early := mustConfirmedRequirement(t, db, "project-a", "早确认的需求", base)
	late := mustConfirmedRequirement(t, db, "project-a", "晚确认的需求", base.Add(time.Hour))
	other := mustConfirmedRequirement(t, db, "project-b", "其他项目的需求", base)
	legacy := mustRequirement(t, db)

	listByProject := func(projectID string) []domain.Requirement {
		t.Helper()
		requirements, err := db.ListRequirements(ctx, storage.RequirementFilter{ProjectID: projectID})
		if err != nil {
			t.Fatalf("查询项目需求失败: %v", err)
		}
		return requirements
	}

	first := listByProject("project-a")
	if len(first) != 2 {
		t.Fatalf("项目 a 的需求数量不符: %+v", first)
	}
	// 排序规则是优先级、创建时间与写入顺序：同一优先级下先创建的排在前面。
	if first[0].ID != early.ID || first[1].ID != late.ID {
		t.Fatalf("项目需求顺序不稳定: %s, %s", first[0].ID, first[1].ID)
	}
	second := listByProject("project-a")
	if second[0].ID != first[0].ID || second[1].ID != first[1].ID {
		t.Fatalf("相同查询返回了不同顺序")
	}
	if listByProject("project-b")[0].ID != other.ID {
		t.Fatalf("项目 b 的需求查询结果不符")
	}
	for _, requirement := range listByProject("project-a") {
		if requirement.ID == legacy.ID {
			t.Fatalf("没有项目关联的历史需求不应出现在项目需求列表中")
		}
	}

	// 不指定项目时返回全部未删除需求，历史需求仍然可见。
	all, err := db.ListRequirements(ctx, storage.RequirementFilter{})
	if err != nil {
		t.Fatalf("查询全部需求失败: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("全部需求数量不符: %d", len(all))
	}
}

// TestTaskListOrderedBySequenceIndependentOfCreation 验证顺序与创建时间无关。
func TestTaskListOrderedBySequenceIndependentOfCreation(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	first := mustSequencedTask(t, db, requirement.ID, 1)
	second := mustSequencedTask(t, db, requirement.ID, 2)

	tasks, err := db.ListTasks(ctx, storage.TaskFilter{RequirementID: requirement.ID})
	if err != nil {
		t.Fatalf("查询需求任务失败: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("需求任务数量不符: %d", len(tasks))
	}
	found := map[domain.ID]int{}
	for _, task := range tasks {
		if task.RequirementSequence == nil {
			t.Fatalf("确认生成的任务必须有顺序: %+v", task)
		}
		found[task.ID] = *task.RequirementSequence
	}
	if found[first.ID] != 1 || found[second.ID] != 2 {
		t.Fatalf("需求内顺序与写入顺序不一致: %+v", found)
	}
}
