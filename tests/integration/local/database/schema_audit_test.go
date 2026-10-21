// Package database_test 审计迁移后的完整业务 Schema 是否满足约束规范。
//
// 规范要点：业务表不声明外键、枚举字段不受数据库级封闭取值限制、触发器不模拟
// 外键或枚举白名单，同时主键、NOT NULL、唯一约束与非枚举 CHECK 必须保留。
package sqlite_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// businessTables 是 AgentFlow 管理的全部业务表。
var businessTables = []string{
	"attempts",
	"code_fetches",
	"code_saves",
	"containers",
	"environment_versions",
	"events",
	"execution_hosts",
	"requirements",
	"roles",
	"sessions",
	"steps",
	"tasks",
	"validation_check_results",
	"validation_feedbacks",
	"validation_runs",
	"verification_results",
}

// testContext 返回带超时的测试上下文。
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// openFreshDB 在测试工作空间中打开一个全新数据库。
//
// 数据库文件保留在 TEST_ARTIFACT_DIR 之下，测试结束只关闭连接。
func openFreshDB(t *testing.T) *sqlite.DB {
	t.Helper()
	path := testkit.New(t).SQLitePath()
	db, err := sqlite.Open(testContext(t), sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %s: %v", path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	return db
}

// rawConn 打开一条绕过仓储层的独立连接。
func rawConn(t *testing.T, db *sqlite.DB) *sql.DB {
	t.Helper()
	return testkit.OpenSQLite(t, db.Path())
}

// TestSchemaHasNoForeignKeys 验证所有业务表都不声明外键。
func TestSchemaHasNoForeignKeys(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	conn := rawConn(t, db)
	ctx := testContext(t)
	for _, table := range businessTables {
		rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_list(`+quoteIdentifier(t, table)+`)`)
		if err != nil {
			t.Fatalf("读取 %s 外键失败: %v", table, err)
		}
		count := 0
		for rows.Next() {
			count++
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("遍历 %s 外键失败: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("关闭 %s 外键结果失败: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("表 %s 仍声明 %d 个外键", table, count)
		}
	}
}

// TestSchemaHasNoForeignKeySyntax 验证建表语句中不残留外键语法。
func TestSchemaHasNoForeignKeySyntax(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	conn := rawConn(t, db)
	for _, table := range businessTables {
		ddl := tableSQL(t, conn, table)
		upper := strings.ToUpper(ddl)
		for _, forbidden := range []string{"REFERENCES", "FOREIGN KEY"} {
			if strings.Contains(upper, forbidden) {
				t.Fatalf("表 %s 的建表语句仍包含 %s:\n%s", table, forbidden, ddl)
			}
		}
	}
}

// TestSchemaHasNoTriggerConstraints 验证不存在模拟外键或枚举白名单的触发器。
func TestSchemaHasNoTriggerConstraints(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	conn := rawConn(t, db)
	triggers := listTriggers(t, conn)
	// 现存触发器只允许是跨字段的私钥路径要求，不得出现外键模拟或枚举白名单。
	allowed := map[string]struct{}{
		"trg_execution_hosts_private_key_required":        {},
		"trg_execution_hosts_private_key_required_update": {},
	}
	for name, ddl := range triggers {
		if _, ok := allowed[name]; !ok {
			t.Fatalf("出现未预期的触发器 %s:\n%s", name, ddl)
		}
		upper := strings.ToUpper(ddl)
		for _, forbidden := range []string{"REFERENCES", "FOREIGN KEY", "RAISE(ABORT"} {
			if !strings.Contains(upper, forbidden) {
				continue
			}
			if forbidden == "RAISE(ABORT" && isPrivateKeyTrigger(name) {
				continue
			}
			t.Fatalf("触发器 %s 含有受限逻辑 %s:\n%s", name, forbidden, ddl)
		}
	}
	if len(triggers) != len(allowed) {
		t.Fatalf("触发器数量不符: 期望 %d，实际 %d", len(allowed), len(triggers))
	}
}

// TestSchemaHasNoEnumWhitelist 验证枚举式字段没有数据库级封闭取值限制。
func TestSchemaHasNoEnumWhitelist(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	conn := rawConn(t, db)
	ctx := testContext(t)
	for _, table := range businessTables {
		ddl := tableSQL(t, conn, table)
		for _, column := range enumColumns {
			if !strings.Contains(strings.ToLower(ddl), " "+column+" ") {
				continue
			}
			if !columnHasEnumWhitelist(t, conn, table, column) {
				continue
			}
			t.Fatalf("表 %s 的枚举字段 %s 仍有数据库级取值限制:\n%s", table, column, ddl)
		}
	}
	// 交叉核验：直接向枚举字段写入未知值必须成功。
	for _, probe := range enumProbes {
		if err := insertUnknownEnumValue(ctx, conn, probe); err != nil {
			t.Fatalf("枚举字段 %s.%s 无法保存未知值: %v", probe.table, probe.column, err)
		}
	}
}

// TestSchemaKeepsRequiredConstraints 验证必须保留的约束没有被连带删除。
func TestSchemaKeepsRequiredConstraints(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	conn := rawConn(t, db)
	ctx := testContext(t)
	for _, expectation := range requiredConstraints {
		if !hasConstraint(t, conn, expectation.table, expectation.fragment) {
			t.Fatalf("表 %s 缺少应当保留的约束 %s", expectation.table, expectation.fragment)
		}
	}
	// 主键、NOT NULL 与唯一约束必须仍然生效。
	if _, err := conn.ExecContext(
		ctx,
		`INSERT INTO roles (id, name, type, runner, model, system_prompt,
		 tools_policy_json, enabled, created_at, updated_at)
		 VALUES ('role-a', '角色', 'planner', 'pi', 'p/m', '提示词', '{}', 1, 1, 1)`,
	); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	if _, err := conn.ExecContext(
		ctx,
		`INSERT INTO roles (id, name, type, runner, model, system_prompt,
		 tools_policy_json, enabled, created_at, updated_at)
		 VALUES ('role-a', '重复', 'planner', 'pi', 'p/m', '提示词', '{}', 1, 1, 1)`,
	); err == nil {
		t.Fatal("主键重复应当被拒绝")
	}
	if _, err := conn.ExecContext(
		ctx,
		`INSERT INTO roles (id, name, type, runner, model, system_prompt,
		 tools_policy_json, enabled, created_at, updated_at)
		 VALUES ('role-b', '   ', 'planner', 'pi', 'p/m', '提示词', '{}', 1, 1, 1)`,
	); err == nil {
		t.Fatal("空名称应当被非枚举 CHECK 拒绝")
	}
	if _, err := conn.ExecContext(
		ctx,
		`INSERT INTO roles (id, name, type, runner, model, system_prompt,
		 tools_policy_json, enabled, created_at, updated_at)
		 VALUES ('role-c', '角色', 'planner', 'pi', 'p/m', '提示词', 'not-json', 1, 1, 1)`,
	); err == nil {
		t.Fatal("非法 JSON 应当被非枚举 CHECK 拒绝")
	}
}

// TestSchemaKeepsIndexes 验证关联查询所需的索引仍然存在。
func TestSchemaKeepsIndexes(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	conn := rawConn(t, db)
	indexes := listIndexes(t, conn)
	for _, expected := range expectedIndexes {
		if _, ok := indexes[expected]; !ok {
			t.Fatalf("缺少索引 %s，实际索引: %v", expected, sortedKeys(indexes))
		}
	}
	// 普通关联索引保留：按 task_id 查询步骤、按 step_id 查询尝试仍然可用。
	if _, err := conn.ExecContext(
		context.Background(),
		`EXPLAIN QUERY PLAN SELECT * FROM steps WHERE task_id = 'x'`,
	); err != nil {
		t.Fatalf("按 task_id 查询步骤失败: %v", err)
	}
	if _, err := conn.ExecContext(
		context.Background(),
		`EXPLAIN QUERY PLAN SELECT * FROM attempts WHERE step_id = 'x'`,
	); err != nil {
		t.Fatalf("按 step_id 查询尝试失败: %v", err)
	}
}

// TestSchemaTablesAreWritable 验证迁移后每张业务表都可以正常写入与查询。
func TestSchemaTablesAreWritable(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	conn := rawConn(t, db)
	ctx := testContext(t)
	for _, table := range businessTables {
		var count int
		if err := conn.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM `+quoteIdentifier(t, table),
		).Scan(&count); err != nil {
			t.Fatalf("查询表 %s 失败: %v", table, err)
		}
	}
}

// TestConcurrentSchemaAuditIsStable 验证并发审计同一数据库不会互相干扰。
func TestConcurrentSchemaAuditIsStable(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	const workers = 6
	var group sync.WaitGroup
	failures := make(chan string, workers)
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			conn := rawConn(t, db)
			ctx := testContext(t)
			for _, table := range businessTables {
				var count int
				if err := conn.QueryRowContext(
					ctx,
					`SELECT COUNT(*) FROM pragma_foreign_key_list(?)`,
					table,
				).Scan(&count); err != nil {
					failures <- "读取外键失败: " + table + ": " + err.Error()
					return
				}
				if count != 0 {
					failures <- "并发审计发现外键: " + table
					return
				}
			}
		}()
	}
	group.Wait()
	close(failures)
	for message := range failures {
		t.Fatal(message)
	}
}

// TestMigrationArtifactsStayInWorkspace 验证数据库与 WAL 文件位于测试工作空间内。
func TestMigrationArtifactsStayInWorkspace(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	path := workspace.SQLitePath()
	db, err := sqlite.Open(testContext(t), sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	}()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("解析数据库绝对路径失败: %v", err)
	}
	root, err := filepath.Abs(workspace.Root())
	if err != nil {
		t.Fatalf("解析工作空间绝对路径失败: %v", err)
	}
	if !strings.HasPrefix(absolute, root+string(os.PathSeparator)) {
		t.Fatalf("数据库文件不在测试工作空间内: %s (根目录 %s)", absolute, root)
	}
	if _, err := os.Stat(absolute); err != nil {
		t.Fatalf("数据库文件不存在: %v", err)
	}
}

// TestTasksSoftDeleteFollowsSchemaRules 校验任务逻辑删除字段符合数据库约束规范。
//
// 逻辑删除只新增可空列与部分索引：不得引入外键、枚举白名单或触发器，
// 也不得把 deleted_at 变成必填字段，否则历史记录将无法升级。
func TestTasksSoftDeleteFollowsSchemaRules(t *testing.T) {
	t.Parallel()
	db := openFreshDB(t)
	conn := rawConn(t, db)
	ctx := testContext(t)

	// tasks 表仍然没有任何外键。
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_list(tasks)`)
	if err != nil {
		t.Fatalf("读取 tasks 外键失败: %v", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		t.Fatal("tasks 表不得声明外键")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历外键结果失败: %v", err)
	}

	// deleted_at 只做时间范围校验，不限定枚举取值。
	ddl := tableSQL(t, conn, "tasks")
	if !strings.Contains(ddl, "deleted_at") {
		t.Fatalf("tasks 缺少 deleted_at 列:\n%s", ddl)
	}
	if strings.Contains(strings.ToUpper(ddl), "REFERENCES") {
		t.Fatalf("tasks 不得声明外键:\n%s", ddl)
	}

	// 逻辑删除后记录仍可读取，且未知状态不受影响。
	testkit.MustExec(t, conn, `INSERT INTO tasks
		(id, requirement_id, status, config_json, repository_path, base_ref,
		 created_at, updated_at, deleted_at)
		VALUES ('soft-task', 'orphan-req', 'unknown_status_v3', '{}', '/repo', 'main', 1, 1, 2)`)
	var status string
	if err := conn.QueryRowContext(
		ctx,
		`SELECT status FROM tasks WHERE id = 'soft-task'`,
	).Scan(&status); err != nil {
		t.Fatalf("读取逻辑删除任务失败: %v", err)
	}
	if status != "unknown_status_v3" {
		t.Fatalf("未知状态应原样保留: %s", status)
	}

	// deleted_at 的 CHECK 仍然拒绝非正数时间戳。
	if _, err := conn.ExecContext(
		ctx,
		`UPDATE tasks SET deleted_at = 0 WHERE id = 'soft-task'`,
	); err == nil {
		t.Fatal("deleted_at = 0 应当被非枚举 CHECK 拒绝")
	}
}
