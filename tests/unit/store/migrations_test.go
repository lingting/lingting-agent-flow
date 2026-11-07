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
	assertGooseVersionRecords(t, ctx, first)
	assertPlanningSchema(t, ctx, first)
	assertProjectDefaultHostColumn(t, ctx, first)
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
	assertPlanningSchema(t, ctx, second)
	assertProjectDefaultHostColumn(t, ctx, second)
	if after := countMigrationRecords(t, ctx, second); after != applied {
		t.Fatalf("再次打开重复执行了迁移: 迁移记录数 %d → %d", applied, after)
	}
	// 重复打开后版本记录仍然是同一条已执行的迁移。
	assertGooseVersionRecords(t, ctx, second)
}

// assertGooseVersionRecords 核验 Goose 版本表记录了唯一的初始迁移。
func assertGooseVersionRecords(t *testing.T, ctx context.Context, db *sqlite.DB) {
	t.Helper()
	rows, err := rawDB(t, db).QueryContext(
		ctx,
		"SELECT version_id, is_applied FROM goose_db_version ORDER BY id",
	)
	if err != nil {
		t.Fatalf("读取迁移版本表失败: %v", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			t.Errorf("关闭迁移版本表结果失败: %v", closeErr)
		}
	}()
	type record struct {
		version int64
		applied int64
	}
	records := make([]record, 0, 4)
	for rows.Next() {
		var item record
		if err := rows.Scan(&item.version, &item.applied); err != nil {
			t.Fatalf("读取迁移版本记录失败: %v", err)
		}
		records = append(records, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历迁移版本表失败: %v", err)
	}
	// Goose 在初始化时先写入 version=0 的基线记录，随后写入每个已执行脚本的版本。
	if len(records) < 2 {
		t.Fatalf("迁移版本记录不足: %+v", records)
	}
	applied := make(map[int64]int64, len(records))
	for _, item := range records {
		applied[item.version] = item.applied
	}
	if applied[0] != 1 {
		t.Fatalf("缺少 Goose 基线版本记录: %+v", records)
	}
	if applied[1] != 1 {
		t.Fatalf("初始迁移脚本没有被记录为已执行: %+v", records)
	}
	for version, flag := range applied {
		if flag != 1 {
			t.Fatalf("迁移版本 %d 未被记录为已执行: %+v", version, records)
		}
	}
}

// assertPlanningSchema 核验 M2 规划相关表、关键字段与索引由迁移创建。
func assertPlanningSchema(t *testing.T, ctx context.Context, db *sqlite.DB) {
	t.Helper()
	requiredColumns := map[string][]string{
		"planning_sessions": {
			"id", "project_id", "status", "host_id", "container_id", "source_commit",
			"analyst_session_id", "planner_session_id", "adopted_revision_id",
			"error_summary", "created_at", "updated_at", "finished_at",
		},
		"planning_messages": {
			"id", "planning_session_id", "sequence", "sender_type", "content", "created_at",
		},
		"planning_revisions": {
			"id", "planning_session_id", "revision_number",
			"requirement_analysis_json", "implementation_plan_json",
			"validation_result_json", "created_at",
		},
	}
	for table, columns := range requiredColumns {
		for _, column := range columns {
			if !hasColumn(t, ctx, db, table, column) {
				t.Fatalf("迁移后表 %s 缺少字段 %s", table, column)
			}
		}
	}
	requiredIndexes := map[string]string{
		"idx_planning_sessions_project":  "planning_sessions",
		"idx_planning_sessions_status":   "planning_sessions",
		"idx_planning_messages_sequence": "planning_messages",
		"idx_planning_revisions_number":  "planning_revisions",
	}
	for index, table := range requiredIndexes {
		if !hasIndex(t, ctx, db, table, index) {
			t.Fatalf("迁移后表 %s 缺少索引 %s", table, index)
		}
	}
}

// assertProjectDefaultHostColumn 核验项目默认执行节点字段与索引。
func assertProjectDefaultHostColumn(t *testing.T, ctx context.Context, db *sqlite.DB) {
	t.Helper()
	if !hasColumn(t, ctx, db, "projects", "default_host_id") {
		t.Fatalf("迁移后 projects 表缺少 default_host_id 字段")
	}
	if !hasIndex(t, ctx, db, "projects", "idx_projects_default_host") {
		t.Fatalf("迁移后 projects 表缺少 idx_projects_default_host 索引")
	}
}

// hasColumn 判断表是否包含指定字段。
func hasColumn(t *testing.T, ctx context.Context, db *sqlite.DB, table, column string) bool {
	t.Helper()
	rows, err := rawDB(t, db).QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatalf("读取表 %s 的字段失败: %v", table, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			t.Errorf("关闭表字段结果失败: %v", closeErr)
		}
	}()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("读取表 %s 字段名失败: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表 %s 字段失败: %v", table, err)
	}
	return false
}

// hasIndex 判断表是否包含指定索引。
func hasIndex(t *testing.T, ctx context.Context, db *sqlite.DB, table, index string) bool {
	t.Helper()
	rows, err := rawDB(t, db).QueryContext(
		ctx,
		"SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ?",
		table,
	)
	if err != nil {
		t.Fatalf("读取表 %s 的索引失败: %v", table, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			t.Errorf("关闭索引结果失败: %v", closeErr)
		}
	}()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("读取表 %s 索引名失败: %v", table, err)
		}
		if name == index {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表 %s 索引失败: %v", table, err)
	}
	return false
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
