package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 需求聚合删除的领域判定用例。
//
// 这些判定是需求级删除的唯一准入来源：单任务删除接口的状态集合不因此放宽，
// 未知状态必须失败关闭。

// newDeletedCandidate 构造一个已确认生成的需求。
func newDeletedCandidate(t *testing.T) domain.Requirement {
	t.Helper()
	sessionID := domain.NewID()
	requirement, err := domain.NewConfirmedRequirement(domain.ConfirmedRequirementInput{
		ProjectID:         "delete-demo",
		PlanningSessionID: sessionID,
		Title:             "删除需求",
		Description:       "验证需求聚合删除的领域规则",
		Priority:          domain.PriorityNormal,
		AnalysisJSON:      domainAnalysisJSON(),
		PlanJSON:          domainPlanJSON(),
		At:                time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("构造确认需求失败: %v", err)
	}
	return requirement
}

// domainAnalysisJSON 返回一份通过领域校验的需求分析快照。
func domainAnalysisJSON() []byte {
	return []byte(`{"summary":"删除需求","goals":["验证删除"],` +
		`"current_state":["已有需求"],"scope":["domain"],"out_of_scope":["不做调度"],` +
		`"constraints":["不新增外键"],"risks":["误删现场"],"acceptance_criteria":["失败关闭"]}`)
}

// domainPlanJSON 返回一份包含单个任务的实施方案快照。
func domainPlanJSON() []byte {
	return []byte(`{"summary":"单任务","tasks":[{"id":"task-1","title":"任务",` +
		`"description":"实现任务","depends_on":[],"acceptance_criteria":["可独立验收"]}]}`)
}

// TestRequirementMarkDeletedOnlyOnce 校验首次删除写入时间并保留业务事实，重复删除被拒绝。
func TestRequirementMarkDeletedOnlyOnce(t *testing.T) {
	t.Parallel()
	requirement := newDeletedCandidate(t)
	originalStatus := requirement.Status
	originalPriority := requirement.Priority
	originalAnalysis := string(requirement.FinalAnalysisJSON)
	originalPlan := string(requirement.FinalPlanJSON)
	originalConfirmedAt := *requirement.ConfirmedAt
	originalSession := *requirement.PlanningSessionID

	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := requirement.MarkDeleted(at); err != nil {
		t.Fatalf("首次删除失败: %v", err)
	}
	if requirement.DeletedAt == nil || !requirement.DeletedAt.Equal(at) {
		t.Fatalf("删除时间不符: %+v", requirement.DeletedAt)
	}
	if !requirement.UpdatedAt.Equal(at) {
		t.Fatalf("更新时间应同步为删除时间: %+v", requirement.UpdatedAt)
	}
	if !requirement.Deleted() {
		t.Fatal("删除后 Deleted 应为 true")
	}
	if requirement.Status != originalStatus {
		t.Fatalf("删除不得改变业务状态: %s -> %s", originalStatus, requirement.Status)
	}
	if requirement.Priority != originalPriority {
		t.Fatalf("删除不得改变优先级: %s", requirement.Priority)
	}
	if string(requirement.FinalAnalysisJSON) != originalAnalysis ||
		string(requirement.FinalPlanJSON) != originalPlan {
		t.Fatal("删除不得清空确认快照")
	}
	if !requirement.ConfirmedAt.Equal(originalConfirmedAt) {
		t.Fatal("删除不得清空确认时间")
	}
	if *requirement.PlanningSessionID != originalSession {
		t.Fatal("删除不得清空规划来源")
	}

	second := time.Date(2026, 2, 4, 0, 0, 0, 0, time.UTC)
	err := requirement.MarkDeleted(second)
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("重复删除应返回状态冲突，实际: %v", err)
	}
	if !requirement.DeletedAt.Equal(at) {
		t.Fatalf("重复删除不得改写删除时间: %+v", requirement.DeletedAt)
	}
}

// TestRequirementMarkDeletedRejectsEmptyEntity 校验空实体不会被当成可删除需求。
func TestRequirementMarkDeletedRejectsEmptyEntity(t *testing.T) {
	t.Parallel()
	var requirement *domain.Requirement
	if err := requirement.MarkDeleted(time.Now().UTC()); err == nil {
		t.Fatal("空实体应返回错误")
	}
}

// TestRequirementDeletionAllowsOnlySafeStatuses 校验需求聚合删除只允许安全状态。
func TestRequirementDeletionAllowsOnlySafeStatuses(t *testing.T) {
	t.Parallel()
	allowed := map[domain.TaskStatus]bool{
		domain.TaskStatusQueued:    true,
		domain.TaskStatusCompleted: true,
		domain.TaskStatusFailed:    true,
		domain.TaskStatusCancelled: true,
	}
	for _, status := range domain.RequirementDeletionTaskStatuses() {
		if !allowed[status] {
			t.Fatalf("删除状态集合出现非安全状态: %s", status)
		}
		delete(allowed, status)
	}
	if len(allowed) != 0 {
		t.Fatalf("删除状态集合缺少安全状态: %v", allowed)
	}
}

// TestTaskAllowsRequirementDeletion 校验需求级删除按白名单判定，未知状态失败关闭。
func TestTaskAllowsRequirementDeletion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status domain.TaskStatus
		want   bool
	}{
		{domain.TaskStatusQueued, true},
		{domain.TaskStatusCompleted, true},
		{domain.TaskStatusFailed, true},
		{domain.TaskStatusCancelled, true},
		{domain.TaskStatusPreparing, false},
		{domain.TaskStatusPlanning, false},
		{domain.TaskStatusPlanned, false},
		{domain.TaskStatusDeveloping, false},
		{domain.TaskStatusVerifying, false},
		{domain.TaskStatusSaving, false},
		{domain.TaskStatusRetrying, false},
		{domain.TaskStatusCancelling, false},
		{domain.TaskStatusPaused, false},
		{domain.TaskStatusRecoveryRequired, false},
		{domain.TaskStatus("mystery"), false},
	}
	for _, item := range cases {
		task := newQueuedTask(t)
		task.Status = item.status
		if got := task.AllowsRequirementDeletion(); got != item.want {
			t.Fatalf("状态 %s 的判定不符: 期望 %v，实际 %v", item.status, item.want, got)
		}
	}
}

// TestTaskAllowsRequirementDeletionRejectsDeletedTask 校验已删除任务不会被重复删除。
func TestTaskAllowsRequirementDeletionRejectsDeletedTask(t *testing.T) {
	t.Parallel()
	task := newQueuedTask(t)
	now := time.Now().UTC()
	if err := task.Transition(domain.TaskStatusFailed, now); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := task.MarkDeleted(now); err != nil {
		t.Fatalf("标记删除失败: %v", err)
	}
	if task.AllowsRequirementDeletion() {
		t.Fatal("已删除任务不应再次允许需求级删除")
	}
}

// TestTaskMarkDeletedStillRejectsQueued 校验单任务删除规则没有因需求级删除而放宽。
func TestTaskMarkDeletedStillRejectsQueued(t *testing.T) {
	t.Parallel()
	task := newQueuedTask(t)
	if !task.AllowsRequirementDeletion() {
		t.Fatal("queued 应允许需求聚合删除")
	}
	err := task.MarkDeleted(time.Now().UTC())
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("单任务删除仍应拒绝 queued，实际: %v", err)
	}
	if task.DeletedAt != nil {
		t.Fatal("被拒绝时不得写入删除时间")
	}
}

// TestTaskAllowsRequirementDeletionKeepsFields 校验需求级判定不改写任务字段。
func TestTaskAllowsRequirementDeletionKeepsFields(t *testing.T) {
	t.Parallel()
	task := newQueuedTask(t)
	now := time.Now().UTC()
	if err := task.Transition(domain.TaskStatusFailed, now); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := task.Finish(domain.TaskResultFailed, "构建失败", now); err != nil {
		t.Fatalf("写入最终结论失败: %v", err)
	}
	status := task.Status
	result := task.FinalResult
	summary := task.FailureSummary
	updatedAt := task.UpdatedAt

	if !task.AllowsRequirementDeletion() {
		t.Fatal("failed 应允许需求聚合删除")
	}
	if task.Status != status || task.FinalResult != result ||
		task.FailureSummary != summary || !task.UpdatedAt.Equal(updatedAt) {
		t.Fatal("需求级判定不得改变任务业务字段")
	}
	if task.DeletedAt != nil {
		t.Fatal("需求级判定不得写入删除时间")
	}
}

// TestStepStatusRequiresManualVerification 校验步骤现场的失败关闭判定。
func TestStepStatusRequiresManualVerification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status domain.StepStatus
		want   bool
	}{
		{domain.StepStatusPending, false},
		{domain.StepStatusRunning, true},
		{domain.StepStatusDeveloping, true},
		{domain.StepStatusVerifying, true},
		{domain.StepStatusRecoveryRequired, true},
		{domain.StepStatusCompleted, false},
		{domain.StepStatusFailed, false},
		{domain.StepStatusCancelled, false},
		{domain.StepStatusSkipped, false},
		{domain.StepStatusRetryWait, false},
		{domain.StepStatus("mystery"), false},
	}
	for _, item := range cases {
		if got := item.status.RequiresManualVerification(); got != item.want {
			t.Fatalf("步骤状态 %s 的判定不符: 期望 %v，实际 %v", item.status, item.want, got)
		}
	}
}

// TestRequirementDeletedEventIsKnown 校验需求删除事件类型已被事件模型接受。
func TestRequirementDeletedEventIsKnown(t *testing.T) {
	t.Parallel()
	if err := domain.EventTypeRequirementDeleted.Validate(); err != nil {
		t.Fatalf("需求删除事件类型应合法: %v", err)
	}
	event, err := domain.NewEvent(
		domain.NewID(),
		nil,
		nil,
		nil,
		domain.EventTypeRequirementDeleted,
		[]byte(`{"requirement_id":"demo"}`),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造需求删除事件失败: %v", err)
	}
	if event.Type != domain.EventTypeRequirementDeleted {
		t.Fatalf("事件类型不符: %s", event.Type)
	}
}
