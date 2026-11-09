package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// 需求与任务软删除的存储层用例。
//
// 每个断言都重新读取数据库：条件写入的影响行数、事务回滚与默认列表过滤只能由真实
// SQLite 现场证明。

// requirementDeleteAt 是软删除用例使用的固定删除时间。
var requirementDeleteAt = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// mustProjectRequirement 创建一条带项目关联的需求。
//
// 项目视图中任务的可⻅性由需求的项目关联决定，因此这里需要一个真实的项目关联值；
// 业务 Schema 不声明外键，不需要先写入项目行。
func mustProjectRequirement(t *testing.T, db *sqlite.DB, projectID string) domain.Requirement {
	t.Helper()
	requirement, err := domain.NewConfirmedRequirement(domain.ConfirmedRequirementInput{
		ProjectID:         projectID,
		PlanningSessionID: domain.NewID(),
		Title:             "需求标题",
		Description:       "需求正文",
		Priority:          domain.PriorityNormal,
		AnalysisJSON:      []byte(`{"summary":"项目视图"}`),
		PlanJSON:          []byte(`{"summary":"项目视图","tasks":[]}`),
		At:                time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("构造确认需求失败: %v", err)
	}
	if err := db.CreateRequirement(testContext(t), requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	return requirement
}

// TestDeleteRequirementIsConditionalAndHidesRecord 校验条件软删除与默认列表过滤。
func TestDeleteRequirementIsConditionalAndHidesRecord(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)

	if err := db.DeleteRequirement(ctx, requirement.ID, requirementDeleteAt); err != nil {
		t.Fatalf("条件软删除失败: %v", err)
	}
	persisted, err := db.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("读取已删除需求失败: %v", err)
	}
	if persisted.DeletedAt == nil || !persisted.DeletedAt.Equal(requirementDeleteAt) {
		t.Fatalf("删除时间不符: %+v", persisted.DeletedAt)
	}
	if !persisted.UpdatedAt.Equal(requirementDeleteAt) {
		t.Fatalf("更新时间应等于删除时间: %+v", persisted.UpdatedAt)
	}
	if persisted.Status != requirement.Status || persisted.Title != requirement.Title {
		t.Fatal("软删除不得改写需求业务字段")
	}
	visible, err := db.ListRequirements(ctx, storage.RequirementFilter{})
	if err != nil {
		t.Fatalf("查询需求列表失败: %v", err)
	}
	for _, item := range visible {
		if item.ID == requirement.ID {
			t.Fatal("已删除需求不应出现在默认列表")
		}
	}

	// 第二次删除必须失败：并发删除只有一个请求能影响 1 行。
	err = db.DeleteRequirement(ctx, requirement.ID, requirementDeleteAt.Add(time.Hour))
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复删除应返回冲突，实际: %v", err)
	}
	again, err := db.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("重复删除后读取失败: %v", err)
	}
	if !again.DeletedAt.Equal(requirementDeleteAt) {
		t.Fatalf("重复删除不得改写删除时间: %+v", again.DeletedAt)
	}
}

// TestDeleteRequirementRejectsMissingAndEmptyID 校验非法与不存在标识的处理。
func TestDeleteRequirementRejectsMissingAndEmptyID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	if err := db.DeleteRequirement(ctx, "", requirementDeleteAt); err == nil {
		t.Fatal("空标识应返回错误")
	}
	err := db.DeleteRequirement(ctx, domain.NewID(), requirementDeleteAt)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("不存在的需求应返回冲突，实际: %v", err)
	}
}

// TestDeleteTaskAcceptsRequirementDeletionStatuses 校验需求级删除可以收起 queued 任务。
func TestDeleteTaskAcceptsRequirementDeletionStatuses(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)

	allowed := domain.RequirementDeletionTaskStatuses()
	if err := db.DeleteTask(ctx, task.ID, allowed, requirementDeleteAt); err != nil {
		t.Fatalf("按需求级状态集合软删除 queued 任务失败: %v", err)
	}
	persisted, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取已删除任务失败: %v", err)
	}
	if persisted.DeletedAt == nil {
		t.Fatal("任务应写入删除时间")
	}
	if persisted.Status != domain.TaskStatusQueued {
		t.Fatalf("软删除不得改写任务状态: %s", persisted.Status)
	}
	visible, err := db.ListTasks(ctx, storage.TaskFilter{RequirementID: requirement.ID})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("已删除任务不应出现在默认列表: %d", len(visible))
	}
	all, err := db.ListTasks(ctx, storage.TaskFilter{RequirementID: requirement.ID, IncludeDeleted: true})
	if err != nil {
		t.Fatalf("查询含删除任务失败: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("包含已删除任务时应读到 1 条，实际 %d 条", len(all))
	}

	// 重复删除同一任务必须失败。
	err = db.DeleteTask(ctx, task.ID, allowed, requirementDeleteAt)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("重复删除任务应返回冲突，实际: %v", err)
	}
}

// TestDeleteTaskRejectsStatusesOutsideAllowedSet 校验不在集合内的状态被拒绝。
func TestDeleteTaskRejectsStatusesOutsideAllowedSet(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	developing := task
	if err := developing.Transition(domain.TaskStatusPreparing, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := developing.Transition(domain.TaskStatusDeveloping, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := db.UpdateTask(ctx, developing); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	err := db.DeleteTask(ctx, task.ID, domain.RequirementDeletionTaskStatuses(), requirementDeleteAt)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("执行中的任务应拒绝需求级删除，实际: %v", err)
	}
	persisted, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if persisted.DeletedAt != nil {
		t.Fatal("被拒绝时不得写入删除时间")
	}
	if persisted.Status != domain.TaskStatusDeveloping {
		t.Fatalf("被拒绝时不得改变状态: %s", persisted.Status)
	}
}

// TestRequirementDeletionHidesTasksFromProjectView 校验需求删除后项目视图不再包含任务。
func TestRequirementDeletionHidesTasksFromProjectView(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	const projectID = "delete-store-demo"
	requirement := mustProjectRequirement(t, db, projectID)
	task := mustTask(t, db, requirement.ID)

	before, err := db.ListTasks(ctx, storage.TaskFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("查询项目任务失败: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("删除前项目任务数量不符: %d", len(before))
	}
	if err := db.DeleteRequirement(ctx, requirement.ID, requirementDeleteAt); err != nil {
		t.Fatalf("删除需求失败: %v", err)
	}
	after, err := db.ListTasks(ctx, storage.TaskFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("删除后查询项目任务失败: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("需求删除后项目视图不应包含其任务: %d", len(after))
	}
	persisted, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if persisted.DeletedAt != nil {
		t.Fatal("只删除需求时不得改写任务行（任务软删除由应用层在同一事务内完成）")
	}
}

// TestRequirementDeletionRollbackKeepsTasks 校验事务回滚不会留下部分删除。
func TestRequirementDeletionRollbackKeepsTasks(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	first := mustTask(t, db, requirement.ID)
	second := mustTask(t, db, requirement.ID)
	allowed := domain.RequirementDeletionTaskStatuses()

	err := db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if deleteErr := tx.DeleteTask(ctx, first.ID, allowed, requirementDeleteAt); deleteErr != nil {
			return deleteErr
		}
		if deleteErr := tx.DeleteTask(ctx, second.ID, allowed, requirementDeleteAt); deleteErr != nil {
			return deleteErr
		}
		if deleteErr := tx.DeleteRequirement(ctx, requirement.ID, requirementDeleteAt); deleteErr != nil {
			return deleteErr
		}
		// 事件写入失败必须让整批软删除一并回滚。
		event, eventErr := domain.NewEvent(
			first.ID,
			nil,
			nil,
			nil,
			domain.EventType("mystery.event"),
			nil,
			requirementDeleteAt,
		)
		if eventErr == nil {
			_, eventErr = tx.AppendEvent(ctx, event)
		}
		if eventErr == nil {
			t.Fatal("未知事件类型不应写入成功")
		}
		return eventErr
	})
	if err == nil {
		t.Fatal("事件写入失败时事务应返回错误")
	}

	persistedRequirement, err := db.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("回滚后读取需求失败: %v", err)
	}
	if persistedRequirement.DeletedAt != nil {
		t.Fatal("回滚后需求不得保留删除时间")
	}
	visible, err := db.ListTasks(ctx, storage.TaskFilter{RequirementID: requirement.ID})
	if err != nil {
		t.Fatalf("回滚后查询任务失败: %v", err)
	}
	if len(visible) != 2 {
		t.Fatalf("回滚后两条任务都应可见，实际 %d 条", len(visible))
	}

	// 事件确实没有落库。
	events, err := db.ListEventsByTask(ctx, first.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("回滚后不应留下事件，实际 %d 条", len(events))
	}
}

// TestRequirementDeletionRacingRequirementFails 校验需求已被并发删除时任务软删除回滚。
func TestRequirementDeletionRacingRequirementFails(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	allowed := domain.RequirementDeletionTaskStatuses()

	if err := db.DeleteRequirement(ctx, requirement.ID, requirementDeleteAt); err != nil {
		t.Fatalf("预置需求删除失败: %v", err)
	}
	err := db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if deleteErr := tx.DeleteTask(ctx, task.ID, allowed, requirementDeleteAt); deleteErr != nil {
			return deleteErr
		}
		return tx.DeleteRequirement(ctx, requirement.ID, requirementDeleteAt)
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("需求已删除时条件写入应返回冲突，实际: %v", err)
	}
	persisted, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if persisted.DeletedAt != nil {
		t.Fatal("需求写入失败时任务软删除必须回滚")
	}
}

// TestConcurrentRequirementDeletionOnlyOneSucceeds 校验并发需求删除只有一个成功。
func TestConcurrentRequirementDeletionOnlyOneSucceeds(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)

	const goroutines = 6
	var wait sync.WaitGroup
	var mutex sync.Mutex
	success := 0
	conflicts := 0
	others := make([]error, 0, goroutines)
	start := make(chan struct{})
	for index := 0; index < goroutines; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			err := db.DeleteRequirement(ctx, requirement.ID, requirementDeleteAt)
			mutex.Lock()
			defer mutex.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, storage.ErrConflict):
				conflicts++
			default:
				others = append(others, err)
			}
		}()
	}
	close(start)
	wait.Wait()
	if success != 1 {
		t.Fatalf("并发删除应只有一次成功，实际 %d 次", success)
	}
	if conflicts != goroutines-1 {
		t.Fatalf("其余请求应返回冲突，实际 %d（其他错误 %v）", conflicts, others)
	}
}

// TestRequirementDeletionKeepsExecutionHistory 校验软删除不清理任何执行历史。
func TestRequirementDeletionKeepsExecutionHistory(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	role := mustRole(t, db, domain.RoleTypeDeveloper, "删除历史开发")
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	step := mustStep(t, db, task, role, 1)
	attempt := mustAttempt(t, db, step, 1)
	session := mustSession(t, db, domain.SessionScope{
		TaskID:    task.ID,
		StepID:    &step.ID,
		AttemptID: &attempt.ID,
	}, role)
	event, err := domain.NewEvent(task.ID, nil, nil, nil, domain.EventTypeTaskCreated, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造事件失败: %v", err)
	}
	if _, err := db.AppendEvent(ctx, event); err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}

	allowed := domain.RequirementDeletionTaskStatuses()
	if err := db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if deleteErr := tx.DeleteTask(ctx, task.ID, allowed, requirementDeleteAt); deleteErr != nil {
			return deleteErr
		}
		return tx.DeleteRequirement(ctx, requirement.ID, requirementDeleteAt)
	}); err != nil {
		t.Fatalf("需求聚合删除失败: %v", err)
	}

	// 需求与任务不再出现在默认列表，但历史记录仍可按任务读取。
	visible, err := db.ListTasks(ctx, storage.TaskFilter{RequirementID: requirement.ID})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("删除后不应有默认可见任务: %d", len(visible))
	}
	persistedStep, err := db.GetStep(ctx, step.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if persistedStep.Status != step.Status {
		t.Fatalf("步骤状态不得被改写: %s", persistedStep.Status)
	}
	if _, err := db.GetAttempt(ctx, attempt.ID); err != nil {
		t.Fatalf("读取尝试失败: %v", err)
	}
	if _, err := db.GetSession(ctx, session.ID); err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	events, err := db.ListEventsByTask(ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("事件应保留，实际 %d 条", len(events))
	}
	steps, err := db.ListStepsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("按任务查询步骤失败: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("按任务查询步骤应保留 1 条，实际 %d 条", len(steps))
	}
}

// TestUnknownTaskStatusSurvivesDeletionRules 校验未知状态既不会被改写也不会被当作安全状态。
func TestUnknownTaskStatusSurvivesDeletionRules(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	raw := rawDB(t, db)
	if _, err := raw.ExecContext(
		ctx,
		`UPDATE tasks SET status = ? WHERE id = ?`,
		"mystery",
		task.ID.String(),
	); err != nil {
		t.Fatalf("写入未知状态失败: %v", err)
	}
	persisted, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if persisted.Status != domain.TaskStatus("mystery") {
		t.Fatalf("未知状态必须原样读回: %s", persisted.Status)
	}
	if persisted.AllowsRequirementDeletion() {
		t.Fatal("未知状态不得被当作可删除状态")
	}
	err = db.DeleteTask(ctx, task.ID, domain.RequirementDeletionTaskStatuses(), requirementDeleteAt)
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("未知状态应拒绝软删除，实际: %v", err)
	}
	again, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if again.Status != domain.TaskStatus("mystery") {
		t.Fatalf("未知状态不得被映射为默认状态: %s", again.Status)
	}
}

// TestRequirementDeletionIdempotentView 用子测试汇总默认视图的可见性结论。
func TestRequirementDeletionIdempotentView(t *testing.T) {
	t.Parallel()
	for _, status := range domain.RequirementDeletionTaskStatuses() {
		status := status
		t.Run(string(status)+"任务", func(t *testing.T) {
			t.Parallel()
			db := newTestDB(t)
			ctx := testContext(t)
			requirement := mustRequirement(t, db)
			task := mustTask(t, db, requirement.ID)
			current := task
			if status != domain.TaskStatusQueued {
				path := statusPath(status)
				for _, next := range path {
					if err := current.Transition(next, time.Now().UTC()); err != nil {
						t.Fatalf("%s 状态迁移失败: %v", status, err)
					}
				}
				if err := db.UpdateTask(ctx, current); err != nil {
					t.Fatalf("更新任务失败: %v", err)
				}
			}
			if err := db.DeleteTask(ctx, task.ID, domain.RequirementDeletionTaskStatuses(), requirementDeleteAt); err != nil {
				t.Fatalf("%s 任务软删除失败: %v", status, err)
			}
			persisted, err := db.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("读取任务失败: %v", err)
			}
			if persisted.Status != status {
				t.Fatalf("软删除不得改写状态: 期望 %s，实际 %s", status, persisted.Status)
			}
		})
	}
}

// statusPath 返回从 queued 到达目标状态的最小合法迁移序列。
func statusPath(status domain.TaskStatus) []domain.TaskStatus {
	switch status {
	case domain.TaskStatusCompleted:
		return []domain.TaskStatus{
			domain.TaskStatusPreparing,
			domain.TaskStatusDeveloping,
			domain.TaskStatusVerifying,
			domain.TaskStatusCompleted,
		}
	case domain.TaskStatusFailed:
		return []domain.TaskStatus{domain.TaskStatusFailed}
	case domain.TaskStatusCancelled:
		return []domain.TaskStatus{domain.TaskStatusCancelling, domain.TaskStatusCancelled}
	default:
		panic(fmt.Sprintf("未定义的状态迁移路径: %s", status))
	}
}
