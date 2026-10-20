package sqlite_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// TestConcurrentTransactionsDoNotDeadlock 验证并发事务能全部完成且不占用连接。
func TestConcurrentTransactionsDoNotDeadlock(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	const total = 6
	var group sync.WaitGroup
	errs := make(chan error, total)
	for index := 0; index < total; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
				if _, err := tx.GetTask(ctx, built.task.ID); err != nil {
					return err
				}
				event, err := domain.NewEvent(
					built.task.ID,
					nil,
					nil,
					nil,
					domain.EventTypeTaskStarted,
					nil,
					time.Now().UTC(),
				)
				if err != nil {
					return err
				}
				_, err = tx.AppendEvent(ctx, event)
				return err
			})
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发事务失败: %v", err)
		}
	}
	events, err := db.ListEventsByTask(ctx, built.task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("查询事件失败: %v", err)
	}
	if len(events) != total {
		t.Fatalf("并发事务事件数量不符: %d", len(events))
	}
	// 事务结束后连接必须归还，后续查询不应超时。
	if _, err := db.GetTask(ctx, built.task.ID); err != nil {
		t.Fatalf("事务结束后读取失败: %v", err)
	}
}

// TestNestedStepStoreReusesOuterTransaction 验证事务内的批量写入复用外层事务。
func TestNestedStepStoreReusesOuterTransaction(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	role := mustRole(t, db, domain.RoleTypeDeveloper, "开发工程师")
	task := mustTask(t, db, requirement.ID)
	step, err := domain.NewStep(
		task.ID, 1, "步骤", "目标", "要求", role, domain.VerificationSpec{}, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}

	rollback := errors.New("回滚")
	err = db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if err := tx.CreateSteps(ctx, []domain.Step{step}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("事务应当返回业务错误，实际: %v", err)
	}
	steps, err := db.ListStepsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("查询步骤失败: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("外层事务回滚后步骤不应存在: %d", len(steps))
	}
}

// TestConcurrentAttemptCreationKeepsSingleWinner 验证同一步骤的并发尝试序号写入
// 只有一个成功，其余返回唯一约束冲突。
func TestConcurrentAttemptCreationKeepsSingleWinner(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	built := newFixture(t, db)
	const total = 4
	var group sync.WaitGroup
	results := make(chan error, total)
	for index := 0; index < total; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			attempt, err := domain.NewAttempt(built.task.ID, built.step.ID, 2, time.Now().UTC())
			if err != nil {
				results <- err
				return
			}
			results <- db.CreateAttempt(ctx, attempt)
		}()
	}
	group.Wait()
	close(results)
	succeeded := 0
	conflicted := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, storage.ErrConflict):
			conflicted++
		default:
			t.Fatalf("并发写入出现意外错误: %v", err)
		}
	}
	if succeeded != 1 || conflicted != total-1 {
		t.Fatalf("并发写入结果不符: 成功 %d，冲突 %d", succeeded, conflicted)
	}
	attempts, err := db.ListAttemptsByStep(ctx, built.step.ID)
	if err != nil {
		t.Fatalf("查询执行尝试失败: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("尝试数量不符: %d", len(attempts))
	}
}

// TestStoreSatisfiesInterfaces 确认组合结果满足存储接口约束。
func TestStoreSatisfiesInterfaces(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	var store storage.Store = db
	if store == nil {
		t.Fatal("DB 应当实现 storage.Store")
	}
	var seeder storage.RoleSeeder = db
	if seeder == nil {
		t.Fatal("DB 应当实现 storage.RoleSeeder")
	}
}
