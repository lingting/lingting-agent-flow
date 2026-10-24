package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// latestSchemaVersion 是当前程序内置迁移的最新版本号。
const latestSchemaVersion = 24

// migrationTableName 是记录已应用迁移版本的元数据表名。
const migrationTableName = "schema_migrations"

// expectedTables 是迁移应当创建的业务表。
var expectedTables = []string{
	"attempts",
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
	// 0020 新增的任务级工作区初始化记录表。
	"task_workspace_initializations",
}

func TestMigrateCreatesAllTables(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	tables, err := listTables(t, db)
	if err != nil {
		t.Fatalf("读取表清单失败: %v", err)
	}
	for _, expected := range expectedTables {
		if !containsString(tables, expected) {
			t.Fatalf("缺少表 %s，实际表清单: %v", expected, tables)
		}
	}
	if !containsString(tables, migrationTableName) {
		t.Fatalf("缺少迁移记录表 %s，实际表清单: %v", migrationTableName, tables)
	}
}

func TestMigrateRecordsVersions(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != latestSchemaVersion {
		t.Fatalf("schema 版本不符: 期望 %d，实际 %d", latestSchemaVersion, version)
	}
	statuses, err := db.MigrationStatuses(ctx)
	if err != nil {
		t.Fatalf("读取迁移状态失败: %v", err)
	}
	if len(statuses) != latestSchemaVersion {
		t.Fatalf("迁移数量不符: %d", len(statuses))
	}
	for _, status := range statuses {
		if !status.Applied {
			t.Fatalf("迁移 %04d 应当已应用: %+v", status.Version, status)
		}
		if status.AppliedAt <= 0 {
			t.Fatalf("迁移应用时间应当为正数: %d", status.AppliedAt)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(testkit.Dir(t), "agentflow.db")
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
	if version != latestSchemaVersion {
		t.Fatalf("重复迁移后版本不符: %d", version)
	}
	roles, err := second.ListRoles(ctx)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if len(roles) != 1 || roles[0].ID != role.ID {
		t.Fatalf("重复迁移破坏了已有数据: %+v", roles)
	}
	// 迁移后应当可以在同一个数据库上继续读写执行节点。
	host, err := domain.NewExecutionHost(
		"", "迁移节点", "10.0.0.1", 0, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := second.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("迁移后写入执行节点失败: %v", err)
	}
	if _, err := second.GetExecutionHost(ctx, host.ID); err != nil {
		t.Fatalf("迁移后读取执行节点失败: %v", err)
	}
}

func TestMigrateUpgradesExistingDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(testkit.Dir(t), "agentflow.db")
	db := openTestDB(t, path)
	ctx := testContext(t)
	role := mustRole(t, db, domain.RoleTypeDeveloper, "开发工程师")
	requirement := mustRequirement(t, db)
	task := mustTask(t, db, requirement.ID)
	// 模拟旧版本数据库：删除新增的表与对应迁移记录，重新打开后应自动补建。
	testkit.MustExec(t, rawDB(t, db), `DROP TABLE code_saves`)
	testkit.MustExec(t, rawDB(t, db), `DROP TABLE containers`)
	testkit.MustExec(t, rawDB(t, db), `DROP TABLE environment_versions`)
	testkit.MustExec(t, rawDB(t, db), `DROP TABLE execution_hosts`)
	// 会话产出字段同样需要回退，才能重新执行对应的迁移。
	for _, statement := range []string{
		`DROP INDEX idx_sessions_run_status`,
		`DROP INDEX idx_sessions_completeness`,
		`ALTER TABLE sessions DROP COLUMN session_jsonl_path`,
		`ALTER TABLE sessions DROP COLUMN session_html_path`,
		`ALTER TABLE sessions DROP COLUMN run_status`,
		`ALTER TABLE sessions DROP COLUMN completeness`,
		`ALTER TABLE sessions DROP COLUMN export_status`,
		`ALTER TABLE sessions DROP COLUMN archive_status`,
		`ALTER TABLE sessions DROP COLUMN archive_path`,
		`DROP INDEX idx_sessions_task_role`,
		`ALTER TABLE sessions DROP COLUMN exit_code`,
		`ALTER TABLE sessions DROP COLUMN failure_summary`,
		`DROP INDEX idx_sessions_invocation`,
		`DROP INDEX idx_sessions_container`,
		`ALTER TABLE sessions DROP COLUMN invocation_id`,
		`ALTER TABLE sessions DROP COLUMN container_id`,
		`ALTER TABLE sessions DROP COLUMN docker_id`,
		// 执行记录模型同样需要回退，才能重新执行 0012。
		// 索引必须在删除列之前移除：SQLite 不允许删除被索引引用的列。
		`DROP INDEX idx_attempts_git_branch`,
		`DROP INDEX idx_attempts_task`,
		`ALTER TABLE attempts DROP COLUMN git_branch`,
		`ALTER TABLE attempts DROP COLUMN git_before_json`,
		`ALTER TABLE attempts DROP COLUMN git_after_json`,
		`ALTER TABLE attempts DROP COLUMN task_id`,
		`ALTER TABLE steps DROP COLUMN failure_summary`,
		// 输出日志位置同样需要回退，才能重新执行 0013。
		`ALTER TABLE sessions DROP COLUMN stdout_path`,
		`ALTER TABLE sessions DROP COLUMN stderr_path`,
		// 验证引擎的两张表同样需要回退，才能重新执行 0014。
		`DROP TABLE validation_check_results`,
		`DROP TABLE validation_runs`,
		// 代码初始化记录表同样需要回退，才能重新执行 0015。
		// 0023 的活动唯一索引随表一起删除：重建表后迁移会重新创建它。
		`DROP TABLE code_fetches`,
		// 验证失败反馈表同样需要回退，才能重新执行 0017。
		`DROP TABLE validation_feedbacks`,
		// 工作流执行记录表同样需要回退，才能重新执行 0018。
		`DROP TABLE workflow_runs`,
		// 任务级工作区初始化记录表同样需要回退，才能重新执行 0020。
		`DROP TABLE task_workspace_initializations`,
		// 执行周期表与尝试的预算作用域列同样需要回退，才能重新执行 0022。
		`DROP INDEX idx_attempts_budget_scope`,
		`ALTER TABLE attempts DROP COLUMN budget_scope`,
		`ALTER TABLE attempts DROP COLUMN budget_scope_id`,
		`DROP TABLE repair_cycles`,
		// 代码保存表在上方已整体删除（第 136 行），因此 0019 新增的恢复字段
		// 会随 CREATE TABLE 一起重建：不需要单独回退，也不应该在已删除的表上
		// 执行 ALTER。此处只需确保建表语句包含全部列。
	} {
		testkit.MustExec(t, rawDB(t, db), statement)
	}
	testkit.MustExec(
		t,
		rawDB(t, db),
		`DELETE FROM `+migrationTableName+` WHERE version IN (2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24)`,
	)
	if version, err := db.SchemaVersion(ctx); err != nil || version != 1 {
		t.Fatalf("模拟旧库版本失败: %d, %v", version, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	upgraded := openTestDB(t, path)
	version, err := upgraded.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != latestSchemaVersion {
		t.Fatalf("升级后版本不符: %d", version)
	}
	host, err := domain.NewExecutionHost(
		"", "迁移节点", "10.0.0.1", 0, "deploy", "", time.Now(),
		domain.WithPrivateKey(testPrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := upgraded.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("升级后写入执行节点失败: %v", err)
	}
	if _, err := upgraded.GetRole(ctx, role.ID); err != nil {
		t.Fatalf("升级破坏了已有角色: %v", err)
	}
	loaded, err := upgraded.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("升级破坏了已有任务: %v", err)
	}
	if loaded.ID != task.ID {
		t.Fatalf("升级后任务不符: %+v", loaded)
	}
}

func TestMigrateRejectsInvalidMigrationRecord(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	// 写入一条程序不认识的迁移版本，重新打开数据库时必须被拒绝。
	testkit.MustExec(
		t,
		rawDB(t, db),
		"INSERT INTO "+migrationTableName+" (version, name, applied_at) VALUES (?, ?, ?)",
		99,
		"future",
		time.Now().UnixMilli(),
	)
	if _, err := sqlite.Open(ctx, sqlite.Options{Path: db.Path()}); !errors.Is(err, storage.ErrUnsupportedSchema) {
		t.Fatalf("应当返回不支持的版本错误，实际: %v", err)
	}
}

// listTables 返回当前数据库中的用户表名。
func listTables(t *testing.T, db *sqlite.DB) ([]string, error) {
	t.Helper()
	rows, err := rawDB(t, db).QueryContext(
		context.Background(),
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
