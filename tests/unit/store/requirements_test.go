package sqlite_test

import (
	"errors"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

func TestCreateAndGetRequirement(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)

	loaded, err := db.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if loaded.Title != requirement.Title || loaded.Description != requirement.Description {
		t.Fatalf("需求字段不符: %+v", loaded)
	}
	if loaded.Priority != domain.PriorityNormal {
		t.Fatalf("优先级映射错误: %s", loaded.Priority)
	}
	if loaded.Status != domain.RequirementStatusQueued {
		t.Fatalf("需求初始状态不符: %s", loaded.Status)
	}
	if loaded.Source != domain.RequirementSourceManual {
		t.Fatalf("需求来源不符: %s", loaded.Source)
	}
	if loaded.SourceID != "" {
		t.Fatalf("外部来源 ID 应当为空: %q", loaded.SourceID)
	}
	if !loaded.CreatedAt.Equal(requirement.CreatedAt) ||
		!loaded.UpdatedAt.Equal(requirement.UpdatedAt) {
		t.Fatalf("时间戳未按毫秒精度保存: %+v", loaded)
	}
}

func TestRequirementPriorityRankRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	priorities := []domain.Priority{
		domain.PriorityHigh,
		domain.PriorityNormal,
		domain.PriorityLow,
	}
	created := make([]domain.Requirement, 0, len(priorities))
	for _, priority := range priorities {
		requirement, err := domain.NewRequirement("标题", "描述", priority, time.Now().UTC())
		if err != nil {
			t.Fatalf("构造需求失败: %v", err)
		}
		if err := db.CreateRequirement(ctx, requirement); err != nil {
			t.Fatalf("写入需求失败: %v", err)
		}
		created = append(created, requirement)
	}
	loaded, err := db.ListRequirements(ctx, storage.RequirementFilter{})
	if err != nil {
		t.Fatalf("查询需求失败: %v", err)
	}
	if len(loaded) != len(created) {
		t.Fatalf("需求数量不符: %d", len(loaded))
	}
	// 队列顺序为优先级升序：high、normal、low。
	want := []domain.Priority{
		domain.PriorityHigh,
		domain.PriorityNormal,
		domain.PriorityLow,
	}
	for index, priority := range want {
		if loaded[index].Priority != priority {
			t.Fatalf("第 %d 条优先级不符: 期望 %s，实际 %s", index, priority, loaded[index].Priority)
		}
	}
}

func TestRequirementStatusTransitionPersists(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)

	if err := requirement.Transition(domain.RequirementStatusProcessing, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := db.UpdateRequirement(ctx, requirement); err != nil {
		t.Fatalf("更新需求失败: %v", err)
	}
	loaded, err := db.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if loaded.Status != domain.RequirementStatusProcessing {
		t.Fatalf("状态未持久化: %s", loaded.Status)
	}
	if !loaded.UpdatedAt.Equal(requirement.UpdatedAt) {
		t.Fatalf("更新时间未持久化: %s vs %s", loaded.UpdatedAt, requirement.UpdatedAt)
	}
}

func TestRequirementAcceptsUnknownStatusAtDatabaseLevel(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	requirement := mustRequirement(t, db)
	// 数据库不再限制状态取值集合，未知状态可以保存并由 Go 层负责校验。
	testkit.MustExec(t, rawDB(t, db), `UPDATE requirements SET status = 'awaiting_triage' WHERE id = ?`,
		requirement.ID.String(),
	)
	loaded, err := db.GetRequirement(testContext(t), requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if loaded.Status != domain.RequirementStatus("awaiting_triage") {
		t.Fatalf("未知状态应当原样保留，实际: %s", loaded.Status)
	}
}

func TestListRequirementsFilterByStatusAndLimit(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	first := mustRequirement(t, db)
	second := mustRequirement(t, db)
	if err := first.Transition(domain.RequirementStatusProcessing, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := db.UpdateRequirement(ctx, first); err != nil {
		t.Fatalf("更新需求失败: %v", err)
	}

	queued, err := db.ListRequirements(ctx, storage.RequirementFilter{
		Statuses: []domain.RequirementStatus{domain.RequirementStatusQueued},
	})
	if err != nil {
		t.Fatalf("查询需求失败: %v", err)
	}
	if len(queued) != 1 || queued[0].ID != second.ID {
		t.Fatalf("状态过滤结果不符: %+v", queued)
	}

	limited, err := db.ListRequirements(ctx, storage.RequirementFilter{Limit: 1})
	if err != nil {
		t.Fatalf("查询需求失败: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("限制条数未生效: %d", len(limited))
	}
}

func TestGetRequirementNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	_, err := db.GetRequirement(testContext(t), domain.NewID())
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("应当返回记录不存在，实际: %v", err)
	}
}

func TestRequirementSourceIDIsNullable(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	requirement.SourceID = "issue-42"
	requirement.UpdatedAt = time.Now().UTC()
	if err := db.UpdateRequirement(ctx, requirement); err != nil {
		t.Fatalf("更新需求失败: %v", err)
	}
	loaded, err := db.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if loaded.SourceID != "issue-42" {
		t.Fatalf("外部来源 ID 未保存: %q", loaded.SourceID)
	}
}
