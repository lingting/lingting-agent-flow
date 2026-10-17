package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// TestOpenCreatesParentDirectory 验证数据库父目录会被自动创建。
func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "agentflow.db")
	db := openTestDB(t, path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("数据库文件未创建: %v", err)
	}
	if db.Path() != path {
		t.Fatalf("数据库路径不符: 期望 %s，实际 %s", path, db.Path())
	}
}

func TestOpenEnablesForeignKeysAndWAL(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	var foreignKeys int
	if err := db.sql.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("读取 foreign_keys 失败: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("外键检查未启用: %d", foreignKeys)
	}
	var journalMode string
	if err := db.sql.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("读取 journal_mode 失败: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode 不是 WAL: %s", journalMode)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(testContext(t), Options{Path: "  "}); err == nil {
		t.Fatal("空路径应当返回错误")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	db, err := Open(testContext(t), Options{Path: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("首次关闭失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("重复关闭应当返回 nil: %v", err)
	}
	if _, err := db.SchemaVersion(testContext(t)); err == nil {
		t.Fatal("关闭后读取版本应当失败")
	}
}

func TestReopenKeepsCommittedData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentflow.db")
	ctx := testContext(t)

	db, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	role := mustRole(t, db, domain.RoleTypePlanner, "规划师")
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	reopened := openTestDB(t, path)
	loadedRole, err := reopened.GetRole(ctx, role.ID)
	if err != nil {
		t.Fatalf("重新打开后读取角色失败: %v", err)
	}
	if loadedRole.Name != role.Name {
		t.Fatalf("角色名称不符: %s", loadedRole.Name)
	}
	loadedTask, err := reopened.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("重新打开后读取任务失败: %v", err)
	}
	if loadedTask.RequirementID != requirement.ID {
		t.Fatalf("任务归属不符: %s", loadedTask.RequirementID)
	}
}

func TestWithTxCommitsAndRollsBack(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)

	committed := mustTaskInTx(t, db, requirement.ID, true)
	if _, err := db.GetTask(ctx, committed.ID); err != nil {
		t.Fatalf("已提交任务应当可读: %v", err)
	}

	rolledBack := mustTaskInTx(t, db, requirement.ID, false)
	if _, err := db.GetTask(ctx, rolledBack.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("回滚任务不应存在，实际错误: %v", err)
	}
}

// mustTaskInTx 在事务中写入任务，并按 commit 决定提交或回滚。
func mustTaskInTx(t *testing.T, db *DB, requirementID domain.ID, commit bool) domain.Task {
	t.Helper()
	task, err := domain.NewTask(requirementID, "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	err = db.WithTx(testContext(t), func(ctx context.Context, tx storage.Tx) error {
		if err := tx.CreateTask(ctx, task); err != nil {
			return err
		}
		if commit {
			return nil
		}
		return errors.New("主动回滚")
	})
	if commit && err != nil {
		t.Fatalf("提交事务失败: %v", err)
	}
	if !commit && err == nil {
		t.Fatal("回滚事务应当返回错误")
	}
	return task
}

func TestWithTxRecoversPanicAndRollsBack(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	requirement := mustRequirement(t, db)
	task, err := domain.NewTask(requirement.ID, "/repo", "main", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic 应当继续向上传播")
			}
		}()
		_ = db.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
			if err := tx.CreateTask(ctx, task); err != nil {
				t.Fatalf("写入任务失败: %v", err)
			}
			panic("测试 panic")
		})
	}()
	if _, err := db.GetTask(ctx, task.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("panic 后事务应当回滚，实际错误: %v", err)
	}
}

func TestConcurrentWritesRemainConsistent(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	const total = 8
	var group sync.WaitGroup
	errs := make(chan error, total)
	for index := 0; index < total; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			requirement, err := domain.NewRequirement(
				"并发需求",
				"并发写入",
				domain.PriorityNormal,
				time.Now().UTC(),
			)
			if err != nil {
				errs <- err
				return
			}
			errs <- db.CreateRequirement(ctx, requirement)
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发写入失败: %v", err)
		}
	}
	requirements, err := db.ListRequirements(ctx, storage.RequirementFilter{})
	if err != nil {
		t.Fatalf("查询需求失败: %v", err)
	}
	if len(requirements) != total {
		t.Fatalf("并发写入条数不符: 期望 %d，实际 %d", total, len(requirements))
	}
}
