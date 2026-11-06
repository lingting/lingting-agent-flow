package sqlite_test

import (
	"context"
	"testing"

	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestMigrationsBootstrap 确认全新数据库由版本化迁移脚本初始化，重复打开不会重复执行。
func TestMigrationsBootstrap(t *testing.T) {
	ctx := testContext(t)
	path := testkit.New(t).SQLitePath()

	first := openMigratedDB(t, ctx, path)
	assertMigratedSchema(t, ctx, first)
	applied := countMigrationRecords(t, ctx, first)
	if err := first.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	second := openMigratedDB(t, ctx, path)
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("关闭数据库失败: %v", err)
		}
	})
	assertMigratedSchema(t, ctx, second)
	if after := countMigrationRecords(t, ctx, second); after != applied {
		t.Fatalf("再次打开重复执行了迁移: 迁移记录数 %d → %d", applied, after)
	}
}

// openMigratedDB 打开数据库，迁移由 sqlite.Open 自动执行。
func openMigratedDB(t *testing.T, ctx context.Context, path string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %s: %v", path, err)
	}
	return db
}

// assertMigratedSchema 确认核心表已经由迁移创建并且可以正常读取。
func assertMigratedSchema(t *testing.T, ctx context.Context, db *sqlite.DB) {
	t.Helper()
	var tasks int
	if err := rawDB(t, db).QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks").Scan(&tasks); err != nil {
		t.Fatalf("读取核心表 tasks 失败: %v", err)
	}
}

// countMigrationRecords 返回迁移版本表的记录数，重复打开时不应增加。
func countMigrationRecords(t *testing.T, ctx context.Context, db *sqlite.DB) int {
	t.Helper()
	var records int
	if err := rawDB(t, db).QueryRowContext(ctx, "SELECT COUNT(*) FROM goose_db_version").Scan(&records); err != nil {
		t.Fatalf("读取迁移版本表失败: %v", err)
	}
	if records == 0 {
		t.Fatalf("迁移版本表没有记录")
	}
	return records
}
