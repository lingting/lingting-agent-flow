package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// expectedTables 是第一版迁移应当创建的业务表。
var expectedTables = []string{
	"attempts",
	"events",
	"requirements",
	"roles",
	"sessions",
	"steps",
	"tasks",
	"verification_results",
}

func TestMigrateCreatesAllTables(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	tables, err := listTables(ctx, db)
	if err != nil {
		t.Fatalf("读取表清单失败: %v", err)
	}
	for _, expected := range expectedTables {
		if !containsString(tables, expected) {
			t.Fatalf("缺少表 %s，实际表清单: %v", expected, tables)
		}
	}
	if !containsString(tables, migrationTable) {
		t.Fatalf("缺少迁移记录表 %s，实际表清单: %v", migrationTable, tables)
	}
}

func TestMigrateRecordsVersions(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema 版本不符: 期望 1，实际 %d", version)
	}
	statuses, err := db.MigrationStatuses(ctx)
	if err != nil {
		t.Fatalf("读取迁移状态失败: %v", err)
	}
	if len(statuses) != 1 || !statuses[0].Applied {
		t.Fatalf("迁移状态不符: %+v", statuses)
	}
	if statuses[0].AppliedAt <= 0 {
		t.Fatalf("迁移应用时间应当为正数: %d", statuses[0].AppliedAt)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentflow.db")
	first := openTestDB(t, path)
	ctx := testContext(t)
	role := mustRole(t, first, domain.RoleTypePlanner, "规划师")
	if err := first.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	second := openTestDB(t, path)
	version, err := second.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != 1 {
		t.Fatalf("重复迁移后版本不符: %d", version)
	}
	roles, err := second.ListRoles(ctx)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if len(roles) != 1 || roles[0].ID != role.ID {
		t.Fatalf("重复迁移破坏了已有数据: %+v", roles)
	}
}

func TestMigrateFailureDoesNotRecordVersion(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	broken := migration{
		version: 99,
		name:    "broken",
		script:  "CREATE TABLE broken_table (id TEXT); INVALID SQL STATEMENT;",
	}
	if err := db.applyMigration(ctx, broken); err == nil {
		t.Fatal("非法迁移应当返回错误")
	}
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != 1 {
		t.Fatalf("失败的迁移不应记录版本，实际版本: %d", version)
	}
	tables, err := listTables(ctx, db)
	if err != nil {
		t.Fatalf("读取表清单失败: %v", err)
	}
	if containsString(tables, "broken_table") {
		t.Fatalf("失败的迁移不应留下表: %v", tables)
	}
}

func TestMigrateRejectsUnsupportedSchemaVersion(t *testing.T) {
	db := newTestDB(t)
	ctx := testContext(t)
	if _, err := db.sql.ExecContext(
		ctx,
		"INSERT INTO "+migrationTable+" (version, name, applied_at) VALUES (?, ?, ?)",
		99,
		"future",
		nowMillis(),
	); err != nil {
		t.Fatalf("写入未来版本失败: %v", err)
	}
	err := db.migrate(ctx)
	if !errors.Is(err, storage.ErrUnsupportedSchema) {
		t.Fatalf("应当返回不支持的版本错误，实际: %v", err)
	}
}

func TestLoadMigrationsParsesEmbeddedFiles(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("读取迁移文件失败: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("迁移文件不应为空")
	}
	versions := make([]int, 0, len(migrations))
	for _, item := range migrations {
		if item.script == "" {
			t.Fatalf("迁移 %s 内容为空", item.name)
		}
		versions = append(versions, item.version)
	}
	if !sort.IntsAreSorted(versions) {
		t.Fatalf("迁移未按版本升序排列: %v", versions)
	}
}

func TestParseMigrationVersionRejectsInvalidNames(t *testing.T) {
	cases := []string{"initial.sql", "abc_init.sql", "0000_init.sql"}
	for _, name := range cases {
		if _, err := parseMigrationVersion(name); err == nil {
			t.Fatalf("非法迁移文件名应当被拒绝: %s", name)
		}
	}
	if version, err := parseMigrationVersion("0007_add_index.sql"); err != nil || version != 7 {
		t.Fatalf("迁移版本解析失败: %d, %v", version, err)
	}
}

// listTables 返回当前数据库中的用户表名。
func listTables(ctx context.Context, db *DB) ([]string, error) {
	rows, err := db.sql.QueryContext(
		ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	tables := make([]string, 0, 16)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

// containsString 判断字符串是否存在于切片中。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
