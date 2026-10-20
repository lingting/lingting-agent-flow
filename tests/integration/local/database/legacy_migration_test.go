package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// legacyFixture 是一套 v8 数据库中的完整历史数据。
//
// 它覆盖 Task / Step / Attempt / Session / 验证结果 / 事件 / 容器 / 环境版本 /
// 代码保存 / 执行节点 / 角色，用于验证迁移不会丢失任何历史记录。
type legacyFixture struct {
	roleIDs      []string
	requirement  string
	task         string
	steps        []string
	attempts     []string
	session      string
	verification string
	events       int64
	host         string
	container    string
	envVersion   string
	codeSave     string
}

// legacyTimestamps 是历史记录使用的固定时间戳。
const legacyBaseMillis = 1700000000000

// writeLegacyFixture 向 v8 数据库写入完整历史数据。
func writeLegacyFixture(t *testing.T, conn *sql.DB) legacyFixture {
	t.Helper()
	fixture := legacyFixture{
		roleIDs:      []string{"legacy-planner", "legacy-developer", "legacy-validator"},
		requirement:  "legacy-req",
		task:         "legacy-task",
		steps:        []string{"legacy-step-1", "legacy-step-2"},
		attempts:     []string{"legacy-attempt-1", "legacy-attempt-2"},
		session:      "legacy-session",
		verification: "legacy-verification",
		host:         "legacy-host",
		container:    "legacy-container",
		envVersion:   "v0001",
		codeSave:     "legacy-code-save",
	}
	roleTypes := []string{"planner", "developer", "validator"}
	for index, id := range fixture.roleIDs {
		testkit.MustExec(
			t,
			conn,
			`INSERT INTO roles (id, name, type, runner, model, system_prompt,
			 tools_policy_json, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, 'pi', 'provider/model', '提示词', '{}', 1, ?, ?)`,
			id,
			"历史角色"+id,
			roleTypes[index],
			legacyBaseMillis,
			legacyBaseMillis,
		)
	}
	testkit.MustExec(
		t,
		conn,
		`INSERT INTO requirements (id, title, description, priority, status, source,
		 created_at, updated_at)
		 VALUES (?, '历史需求', '历史需求正文', 1, 'completed', 'cli', ?, ?)`,
		fixture.requirement,
		legacyBaseMillis,
		legacyBaseMillis,
	)
	testkit.MustExec(
		t,
		conn,
		`INSERT INTO execution_hosts (id, name, host, port, username, workspace_root,
		 is_default, enabled, auth_method, created_at, updated_at)
		 VALUES (?, '历史节点', '10.0.0.9', 22, 'deploy', '~', 1, 1, 'agent', ?, ?)`,
		fixture.host,
		legacyBaseMillis,
		legacyBaseMillis,
	)
	testkit.MustExec(
		t,
		conn,
		`INSERT INTO tasks (id, requirement_id, status, host_id, config_json,
		 repository_path, base_ref, cancel_requested, created_at, updated_at)
		 VALUES (?, ?, 'completed', ?, '{}', '/repo', 'main', 0, ?, ?)`,
		fixture.task,
		fixture.requirement,
		fixture.host,
		legacyBaseMillis,
		legacyBaseMillis,
	)
	for index, stepID := range fixture.steps {
		testkit.MustExec(
			t,
			conn,
			`INSERT INTO steps (id, task_id, sequence, step_type, title, objective,
			 instructions_json, role_id, role_snapshot_json, verification_spec_json,
			 retry_json, max_attempts, status, created_at, updated_at)
			 VALUES (?, ?, ?, 'develop', '历史步骤', '历史目标', '"要求"', ?, ?, '{}',
			 '{}', 3, 'completed', ?, ?)`,
			stepID,
			fixture.task,
			index+1,
			fixture.roleIDs[1],
			roleSnapshotJSON(t, fixture.roleIDs[1], "developer"),
			legacyBaseMillis,
			legacyBaseMillis,
		)
	}
	for index, attemptID := range fixture.attempts {
		testkit.MustExec(
			t,
			conn,
			`INSERT INTO attempts (id, step_id, attempt_number, status, exit_code,
			 session_ids_json, started_at, finished_at, created_at)
			 VALUES (?, ?, 1, 'completed', 0, '[]', ?, ?, ?)`,
			attemptID,
			fixture.steps[index],
			legacyBaseMillis,
			legacyBaseMillis,
			legacyBaseMillis,
		)
	}
	testkit.MustExec(
		t,
		conn,
		`INSERT INTO sessions (id, task_id, step_id, attempt_id, role_id,
		 role_snapshot_json, provider, model, status, started_at, finished_at,
		 created_at, run_status, completeness, export_status)
		 VALUES (?, ?, ?, ?, ?, '{}', 'pi', 'provider/model', 'completed', ?, ?, ?,
		 'completed', 'complete', 'succeeded')`,
		fixture.session,
		fixture.task,
		fixture.steps[0],
		fixture.attempts[0],
		fixture.roleIDs[1],
		legacyBaseMillis,
		legacyBaseMillis,
		legacyBaseMillis,
	)
	testkit.MustExec(
		t,
		conn,
		`INSERT INTO verification_results (id, task_id, step_id, attempt_id,
		 validator_session_id, passed, verdict, checks_json, command_results_json,
		 created_at)
		 VALUES (?, ?, ?, ?, ?, 1, 'passed', '[]', '[]', ?)`,
		fixture.verification,
		fixture.task,
		fixture.steps[0],
		fixture.attempts[0],
		fixture.session,
		legacyBaseMillis,
	)
	for _, eventType := range []string{"task_created", "task_started"} {
		testkit.MustExec(
			t,
			conn,
			`INSERT INTO events (task_id, step_id, event_type, payload_json, created_at)
			 VALUES (?, NULL, ?, '{}', ?)`,
			fixture.task,
			eventType,
			legacyBaseMillis,
		)
	}
	testkit.MustExec(
		t,
		conn,
		`INSERT INTO environment_versions (host_id, env_type, env_name, version_number,
		 dockerfile_sha256, build_config_sha256, context_sha256, image_ref,
		 build_status, created_at)
		 VALUES (?, 'base', 'base', 1, ?, ?, ?, 'agentflow-base:v0001-aaaaaaaaaaaa',
		 'pending', ?)`,
		fixture.host,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
		strings.Repeat("c", 64),
		legacyBaseMillis,
	)
	testkit.MustExec(
		t,
		conn,
		`INSERT INTO containers (id, host_id, name, env_type, env_name, env_version,
		 build_config_sha256, image_id, task_id, state, docker_state, created_at,
		 finished_at, end_reason, detached, updated_at)
		 VALUES (?, ?, 'agentflow-legacy000001', 'base', 'base', ?,
		 ?, 'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
		 ?, 'removed', 'exited', ?, ?, 'task_succeeded', 0, ?)`,
		fixture.container,
		fixture.host,
		fixture.envVersion,
		strings.Repeat("b", 64),
		fixture.task,
		legacyBaseMillis,
		legacyBaseMillis,
		legacyBaseMillis,
	)
	testkit.MustExec(
		t,
		conn,
		`INSERT INTO code_saves (id, task_id, container_id, attempt_id, remote_name,
		 task_branch, commit_status, commit_sha, push_status, remote_commit_sha,
		 merge_enabled, merge_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'origin', ?, 'succeeded', ?, 'succeeded', ?,
		 0, 'not_requested', ?, ?)`,
		fixture.codeSave,
		fixture.task,
		fixture.container,
		fixture.attempts[0],
		mustTaskBranch(t, fixture.task),
		strings.Repeat("d", 40),
		strings.Repeat("d", 40),
		legacyBaseMillis,
		legacyBaseMillis,
	)
	return fixture
}

// openLegacyDatabase 在工作空间中构造一个 v8 数据库并写入历史数据。
//
// 返回数据库文件路径，样本文件保留在 TEST_ARTIFACT_DIR 内。
func openLegacyDatabase(t *testing.T) (string, legacyFixture) {
	t.Helper()
	path := filepath.Join(testkit.Dir(t), "legacy.db")
	conn := testkit.OpenSQLite(t, path)
	testkit.CreateLegacySchema(t, conn)
	fixture := writeLegacyFixture(t, conn)
	return path, fixture
}

// openMigrated 用产品代码打开并迁移指定数据库。
func openMigrated(t *testing.T, path string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(testContext(t), sqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("迁移数据库失败: %s: %v", path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败: %v", closeErr)
		}
	})
	return db
}

// TestLegacyDatabaseLosesNoRecords 验证旧数据库迁移后历史记录完整保留。
func TestLegacyDatabaseLosesNoRecords(t *testing.T) {
	t.Parallel()
	path, fixture := openLegacyDatabase(t)
	db := openMigrated(t, path)
	ctx := testContext(t)

	version, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != 13 {
		t.Fatalf("迁移后版本应为 13，实际 %d", version)
	}

	roles, err := db.ListRoles(ctx)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if len(roles) != len(fixture.roleIDs) {
		t.Fatalf("角色数量不符: 期望 %d，实际 %d", len(fixture.roleIDs), len(roles))
	}
	for index, id := range fixture.roleIDs {
		if roles[index].ID != domain.ID(id) {
			t.Fatalf("角色顺序或标识不符: %+v", roles[index])
		}
	}

	task, err := db.GetTask(ctx, domain.ID(fixture.task))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status != domain.TaskStatusCompleted {
		t.Fatalf("任务状态未保留: %s", task.Status)
	}
	if task.RequirementID != domain.ID(fixture.requirement) {
		t.Fatalf("任务需求关联未保留: %s", task.RequirementID)
	}
	if task.HostID == nil || *task.HostID != domain.ID(fixture.host) {
		t.Fatalf("任务节点关联未保留: %+v", task.HostID)
	}

	steps, err := db.ListStepsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	if len(steps) != len(fixture.steps) {
		t.Fatalf("步骤数量不符: %d", len(steps))
	}
	for index, step := range steps {
		if step.ID != domain.ID(fixture.steps[index]) {
			t.Fatalf("步骤标识不符: %s", step.ID)
		}
		if step.Type != domain.StepTypeDevelop {
			t.Fatalf("步骤类型未保留: %s", step.Type)
		}
		if step.RoleID != domain.ID(fixture.roleIDs[1]) {
			t.Fatalf("步骤角色关联未保留: %s", step.RoleID)
		}
		attempts, err := db.ListAttemptsByStep(ctx, step.ID)
		if err != nil {
			t.Fatalf("读取尝试失败: %v", err)
		}
		if len(attempts) != 1 {
			t.Fatalf("步骤 %s 的尝试数量不符: %d", step.ID, len(attempts))
		}
		if attempts[0].ID != domain.ID(fixture.attempts[index]) {
			t.Fatalf("尝试标识不符: %s", attempts[0].ID)
		}
		// 0012 迁移必须把既有尝试的任务归属从步骤回填，旧库不能留下空归属。
		if attempts[0].TaskID != task.ID {
			t.Fatalf("尝试任务归属未回填: %s", attempts[0].TaskID)
		}
	}

	sessions, err := db.ListSessionsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != domain.ID(fixture.session) {
		t.Fatalf("会话未保留: %+v", sessions)
	}
	if sessions[0].RunStatus != domain.SessionRunCompleted {
		t.Fatalf("会话运行状态未保留: %s", sessions[0].RunStatus)
	}

	results, err := db.ListVerificationResultsByAttempt(ctx, domain.ID(fixture.attempts[0]))
	if err != nil {
		t.Fatalf("读取验证结果失败: %v", err)
	}
	if len(results) != 1 || !results[0].Passed {
		t.Fatalf("验证结果未保留: %+v", results)
	}

	events, err := db.ListEventsByTask(ctx, task.ID, storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("事件数量不符: %d", len(events))
	}

	containers, err := db.ListContainers(ctx, domain.ContainerFilter{IncludeDetached: true})
	if err != nil {
		t.Fatalf("读取容器失败: %v", err)
	}
	if len(containers) != 1 || containers[0].ID != domain.ID(fixture.container) {
		t.Fatalf("容器记录未保留: %+v", containers)
	}

	versions, err := db.ListEnvironmentVersions(
		ctx,
		storage.EnvironmentVersionFilter{HostID: domain.ID(fixture.host)},
	)
	if err != nil {
		t.Fatalf("读取环境版本失败: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("环境版本未保留: %+v", versions)
	}

	records, err := db.ListCodeSaveRecordsByTask(ctx, fixture.task)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if len(records) != 1 || records[0].ID != domain.ID(fixture.codeSave) {
		t.Fatalf("代码保存记录未保留: %+v", records)
	}
}

// TestLegacyDatabaseLosesNoConstraints 验证迁移后旧约束被移除、新约束可写。
func TestLegacyDatabaseLosesNoConstraints(t *testing.T) {
	t.Parallel()
	path, _ := openLegacyDatabase(t)
	db := openMigrated(t, path)
	conn := testkit.OpenSQLite(t, db.Path())
	ctx := testContext(t)

	// 外键与枚举白名单都必须消失。
	for _, table := range businessTables {
		var count int
		if err := conn.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM pragma_foreign_key_list(?)`,
			table,
		).Scan(&count); err != nil {
			t.Fatalf("读取 %s 外键失败: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("迁移后表 %s 仍有 %d 个外键", table, count)
		}
		if strings.Contains(strings.ToUpper(tableSQLOf(t, conn, table)), "REFERENCES") {
			t.Fatalf("迁移后表 %s 的建表语句仍含 REFERENCES", table)
		}
	}

	// 旧枚举值保留的同时，未知枚举值可以写入。
	testkit.MustExec(
		t,
		conn,
		`UPDATE tasks SET status = 'recovery_required_v2' WHERE id = 'legacy-task'`,
	)
	loaded, err := db.GetTask(ctx, domain.ID("legacy-task"))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.Status != domain.TaskStatus("recovery_required_v2") {
		t.Fatalf("未知任务状态未原样保存: %s", loaded.Status)
	}
}

// TestLegacyDatabaseKeepsIndexesAndAutoincrement 验证索引与自增主键仍然正常。
func TestLegacyDatabaseKeepsIndexesAndAutoincrement(t *testing.T) {
	t.Parallel()
	path, fixture := openLegacyDatabase(t)
	db := openMigrated(t, path)
	conn := testkit.OpenSQLite(t, db.Path())
	ctx := testContext(t)

	indexes := listIndexes(t, conn)
	for _, expected := range expectedIndexes {
		if _, ok := indexes[expected]; !ok {
			t.Fatalf("迁移后缺少索引 %s", expected)
		}
	}

	// events 使用 INTEGER PRIMARY KEY AUTOINCREMENT：迁移后新插入的行必须得到更大的 ID。
	var maxID int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&maxID); err != nil {
		t.Fatalf("读取事件最大标识失败: %v", err)
	}
	testkit.MustExec(
		t,
		conn,
		`INSERT INTO events (task_id, event_type, payload_json, created_at)
		 VALUES (?, 'task_finished', '{}', ?)`,
		fixture.task,
		legacyBaseMillis,
	)
	events, err := db.ListEventsByTask(ctx, domain.ID(fixture.task), storage.EventFilter{})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("事件数量不符: %d", len(events))
	}
	if events[len(events)-1].ID <= maxID {
		t.Fatalf("自增标识未继续增长: 旧最大 %d，新值 %d", maxID, events[len(events)-1].ID)
	}
}

// TestLegacyDatabaseKeepsUniqueAndCheckConstraints 验证唯一约束与非枚举 CHECK 仍生效。
func TestLegacyDatabaseKeepsUniqueAndCheckConstraints(t *testing.T) {
	t.Parallel()
	path, fixture := openLegacyDatabase(t)
	db := openMigrated(t, path)
	conn := testkit.OpenSQLite(t, db.Path())
	ctx := testContext(t)

	// UNIQUE (task_id, sequence) 保留。
	if _, err := conn.ExecContext(
		ctx,
		`INSERT INTO steps (id, task_id, sequence, step_type, title, objective,
		 instructions_json, verification_spec_json, status, created_at, updated_at)
		 VALUES ('dup-step', ?, 1, 'develop', '标题', '目标', '{}', '{}', 'pending', 1, 1)`,
		fixture.task,
	); err == nil {
		t.Fatal("同任务内重复序号应当被唯一约束拒绝")
	}
	// 非枚举 CHECK（JSON 合法性）保留。
	if _, err := conn.ExecContext(
		ctx,
		`UPDATE steps SET instructions_json = 'not-json' WHERE id = ?`,
		fixture.steps[0],
	); err == nil {
		t.Fatal("非法 JSON 应当被非枚举 CHECK 拒绝")
	}
	// 非枚举 CHECK（数值范围）保留。
	if _, err := conn.ExecContext(
		ctx,
		`UPDATE steps SET max_attempts = 99 WHERE id = ?`,
		fixture.steps[0],
	); err == nil {
		t.Fatal("超出范围的尝试次数应当被非枚举 CHECK 拒绝")
	}
}

// TestLegacyDatabaseMigrationIsIdempotent 验证重复打开不会破坏数据或重复迁移。
func TestLegacyDatabaseMigrationIsIdempotent(t *testing.T) {
	t.Parallel()
	path, fixture := openLegacyDatabase(t)
	first := openMigrated(t, path)
	ctx := testContext(t)
	if err := first.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	second := openMigrated(t, path)
	version, err := second.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != 13 {
		t.Fatalf("重复迁移后版本不符: %d", version)
	}
	task, err := second.GetTask(ctx, domain.ID(fixture.task))
	if err != nil {
		t.Fatalf("重复迁移破坏了任务: %v", err)
	}
	if task.ID != domain.ID(fixture.task) {
		t.Fatalf("任务标识不符: %s", task.ID)
	}
	// 迁移记录只应有一条版本 10 的记录。
	var count int
	conn := testkit.OpenSQLite(t, second.Path())
	if err := conn.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 10`,
	).Scan(&count); err != nil {
		t.Fatalf("统计迁移记录失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("版本 10 的迁移记录数量不符: %d", count)
	}
}

// TestLegacyDatabaseFailedMigrationLeavesNoPartialState 验证迁移失败不会留下半成品。
func TestLegacyDatabaseFailedMigrationLeavesNoPartialState(t *testing.T) {
	t.Parallel()
	path, fixture := openLegacyDatabase(t)
	// 制造一个必然失败的迁移前置条件：把 tasks 改名，使迁移中的重建语句找不到源表。
	conn := testkit.OpenSQLite(t, path)
	testkit.MustExec(t, conn, `ALTER TABLE tasks RENAME TO tasks_blocked`)
	if err := conn.Close(); err != nil {
		t.Fatalf("关闭连接失败: %v", err)
	}

	ctx := testContext(t)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err == nil {
		_ = db.Close()
		t.Fatal("缺少源表时迁移必须失败")
	}
	if !strings.Contains(err.Error(), "执行迁移") {
		t.Fatalf("迁移失败信息应指出阶段，实际: %v", err)
	}

	// 失败后版本必须仍是 8，且原有数据未被删除。
	check := testkit.OpenSQLite(t, path)
	defer func() {
		if closeErr := check.Close(); closeErr != nil {
			t.Errorf("关闭校验连接失败: %v", closeErr)
		}
	}()
	var version int
	if err := check.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`,
	).Scan(&version); err != nil {
		t.Fatalf("读取迁移版本失败: %v", err)
	}
	if version != testkit.LegacySchemaVersion {
		t.Fatalf("失败后版本被错误推进: %d", version)
	}
	var tasks int
	if err := check.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM tasks_blocked WHERE id = ?`,
		fixture.task,
	).Scan(&tasks); err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if tasks != 1 {
		t.Fatalf("失败后原始任务记录丢失: %d", tasks)
	}
	var requirements int
	if err := check.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM requirements WHERE id = ?`,
		fixture.requirement,
	).Scan(&requirements); err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if requirements != 1 {
		t.Fatalf("失败后原始需求记录丢失: %d", requirements)
	}
	// 半成品表不得残留。
	var leftovers int
	if err := check.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name LIKE '%_new'`,
	).Scan(&leftovers); err != nil {
		t.Fatalf("统计临时表失败: %v", err)
	}
	if leftovers != 0 {
		t.Fatalf("失败后残留临时表: %d", leftovers)
	}
}

// TestLegacyDatabaseMigrationPreservesOrphanRecords 验证历史孤立引用不会被清理。
func TestLegacyDatabaseMigrationPreservesOrphanRecords(t *testing.T) {
	t.Parallel()
	path, _ := openLegacyDatabase(t)
	// v8 的外键会阻止写入孤立引用，因此先迁移到 v9，再制造孤立记录。
	db := openMigrated(t, path)
	conn := testkit.OpenSQLite(t, db.Path())
	ctx := testContext(t)

	// 删除父记录后子记录必须保留，ID 成为普通孤立标识。
	testkit.MustExec(t, conn, `DELETE FROM requirements WHERE id = 'legacy-req'`)
	task, err := db.GetTask(ctx, domain.ID("legacy-task"))
	if err != nil {
		t.Fatalf("父记录删除后任务不应丢失: %v", err)
	}
	if task.RequirementID != domain.ID("legacy-req") {
		t.Fatalf("任务应保留原需求标识: %s", task.RequirementID)
	}
	// 任务列表查询不得因孤立引用失败。
	tasks, err := db.ListTasks(ctx, storage.TaskFilter{})
	if err != nil {
		t.Fatalf("存在孤立引用时任务列表查询不应失败: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("任务列表数量不符: %d", len(tasks))
	}
}

// TestLegacyDatabaseConcurrentOpenMigratesOnce 验证并发打开同一数据库只迁移一次。
func TestLegacyDatabaseConcurrentOpenMigratesOnce(t *testing.T) {
	t.Parallel()
	path, fixture := openLegacyDatabase(t)
	const workers = 4
	var group sync.WaitGroup
	results := make(chan error, workers)
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
			if err != nil {
				results <- err
				return
			}
			defer func() {
				_ = db.Close()
			}()
			if _, err := db.GetTask(ctx, domain.ID(fixture.task)); err != nil {
				results <- err
			}
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		t.Fatalf("并发打开迁移失败: %v", err)
	}

	conn := testkit.OpenSQLite(t, path)
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Errorf("关闭连接失败: %v", closeErr)
		}
	}()
	var count int
	if err := conn.QueryRowContext(
		testContext(t),
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 10`,
	).Scan(&count); err != nil {
		t.Fatalf("统计迁移记录失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("并发打开后版本 10 的迁移记录数量不符: %d", count)
	}
}

// TestFreshDatabaseMatchesLegacySchemaAfterMigration 验证全新数据库与迁移后旧库结构一致。
func TestFreshDatabaseMatchesLegacySchemaAfterMigration(t *testing.T) {
	t.Parallel()
	legacyPath, _ := openLegacyDatabase(t)
	legacyDB := openMigrated(t, legacyPath)
	legacyConn := testkit.OpenSQLite(t, legacyDB.Path())

	freshDB := openMigrated(t, filepath.Join(testkit.Dir(t), "fresh.db"))
	freshConn := testkit.OpenSQLite(t, freshDB.Path())

	for _, table := range businessTables {
		legacyDDL := normalizeSchemaSQL(tableSQLOf(t, legacyConn, table))
		freshDDL := normalizeSchemaSQL(tableSQLOf(t, freshConn, table))
		if legacyDDL != freshDDL {
			t.Fatalf(
				"表 %s 在旧库迁移与全新迁移后结构不一致:\n旧库: %s\n新库: %s",
				table,
				legacyDDL,
				freshDDL,
			)
		}
	}
}

// tableSQLOf 返回指定表的建表语句。
func tableSQLOf(t *testing.T, conn *sql.DB, table string) string {
	t.Helper()
	var ddl sql.NullString
	if err := conn.QueryRowContext(
		context.Background(),
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&ddl); err != nil {
		t.Fatalf("读取表 %s 定义失败: %v", table, err)
	}
	if !ddl.Valid {
		t.Fatalf("表 %s 不存在", table)
	}
	return ddl.String
}

// normalizeSchemaSQL 把建表语句折叠为便于比较的形式。
func normalizeSchemaSQL(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// TestLegacySampleStaysInWorkspace 验证旧库样本落在测试工作空间内。
func TestLegacySampleStaysInWorkspace(t *testing.T) {
	t.Parallel()
	path, _ := openLegacyDatabase(t)
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("解析样本绝对路径失败: %v", err)
	}
	root := testkit.RunRoot(t)
	if !strings.HasPrefix(absolute, root+string(os.PathSeparator)) {
		t.Fatalf("旧库样本不在测试工作空间内: %s (根目录 %s)", absolute, root)
	}
	if _, err := os.Stat(absolute); err != nil {
		t.Fatalf("旧库样本文件不存在: %v", err)
	}
}

// roleSnapshotJSON 构造与角色标识一致的角色快照 JSON。
func roleSnapshotJSON(t *testing.T, roleID, roleType string) string {
	t.Helper()
	snapshot, err := json.Marshal(domain.RoleSnapshot{
		RoleID:       domain.ID(roleID),
		Name:         "历史角色",
		Type:         domain.RoleType(roleType),
		Runner:       domain.RoleRunnerPi,
		Model:        "provider/model",
		SystemPrompt: "提示词",
		ToolsPolicy:  domain.DefaultToolsPolicy(domain.RoleType(roleType)),
	})
	if err != nil {
		t.Fatalf("编码角色快照失败: %v", err)
	}
	return string(snapshot)
}

// mustTaskBranch 返回任务约定分支名。
func mustTaskBranch(t *testing.T, taskID string) string {
	t.Helper()
	branch, err := domain.TaskBranchName(taskID)
	if err != nil {
		t.Fatalf("构造任务分支名失败: %v", err)
	}
	return branch
}
