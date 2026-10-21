package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// enumColumns 是可能带有枚举白名单的列名。
//
// 列名重复出现在多张表中是正常的，审计时按"表 + 列"逐项检查。
var enumColumns = []string{
	"type",
	"status",
	"final_result",
	"step_type",
	"verdict",
	"verification_verdict",
	"run_status",
	"completeness",
	"export_status",
	"auth_method",
	"last_connect_status",
	"env_type",
	"build_status",
	"state",
	"docker_state",
	"end_reason",
	"commit_status",
	"push_status",
	"merge_status",
	"execution_env",
}

// enumProbe 是一次"写入未知枚举值"的探测。
type enumProbe struct {
	// table 是目标表。
	table string
	// column 是被探测的枚举列。
	column string
	// value 是当前版本不认识的新取值。
	value string
	// statement 是插入探测行的完整语句。
	statement string
}

// enumProbes 覆盖全部主要枚举字段，用未知取值直接写入数据库。
var enumProbes = []enumProbe{
	{
		table:  "requirements",
		column: "status",
		value:  "awaiting_triage",
		statement: `INSERT INTO requirements
			(id, title, description, priority, status, source, created_at, updated_at)
			VALUES ('probe-req', '标题', '正文', 1, 'awaiting_triage', 'cli', 1, 1)`,
	},
	{
		table:  "tasks",
		column: "status",
		value:  "recovery_required_v2",
		statement: `INSERT INTO tasks
			(id, requirement_id, status, config_json, repository_path, base_ref,
			 created_at, updated_at)
			VALUES ('probe-task', 'orphan-req', 'recovery_required_v2', '{}',
			 '/repo', 'main', 1, 1)`,
	},
	{
		table:  "steps",
		column: "status",
		value:  "awaiting_review",
		statement: `INSERT INTO steps
			(id, task_id, sequence, step_type, title, objective, instructions_json,
			 verification_spec_json, status, created_at, updated_at)
			VALUES ('probe-step', 'orphan-task', 1, 'audit', '标题', '目标', '{}',
			 '{}', 'awaiting_review', 1, 1)`,
	},
	{
		table:  "attempts",
		column: "status",
		value:  "awaiting_retry",
		statement: `INSERT INTO attempts
			(id, step_id, attempt_number, status, session_ids_json, started_at, created_at)
			VALUES ('probe-attempt', 'orphan-step', 1, 'awaiting_retry', '[]', 1, 1)`,
	},
	{
		table:  "sessions",
		column: "status",
		value:  "paused_v2",
		statement: `INSERT INTO sessions
			(id, task_id, role_id, role_snapshot_json, provider, model, status,
			 started_at, created_at)
			VALUES ('probe-session', 'orphan-task', 'orphan-role', '{}', 'pi', 'p/m',
			 'paused_v2', 1, 1)`,
	},
	{
		table:  "verification_results",
		column: "verdict",
		value:  "pending_review",
		statement: `INSERT INTO verification_results
			(id, task_id, step_id, attempt_id, passed, verdict, checks_json,
			 command_results_json, created_at)
			VALUES ('probe-verification', 'orphan-task', 'orphan-step', 'orphan-attempt',
			 0, 'pending_review', '[]', '[]', 1)`,
	},
	{
		table:  "execution_hosts",
		column: "auth_method",
		value:  "hardware_key",
		statement: `INSERT INTO execution_hosts
			(id, name, host, port, username, workspace_root, auth_method,
			 created_at, updated_at)
			VALUES ('probe-host', '节点', '10.0.0.1', 22, 'deploy', '~', 'hardware_key', 1, 1)`,
	},
	{
		table:  "environment_versions",
		column: "env_type",
		value:  "runtime",
		statement: `INSERT INTO environment_versions
			(host_id, env_type, env_name, version_number, dockerfile_sha256,
			 build_config_sha256, context_sha256, image_ref, build_status, created_at)
			VALUES ('probe-host', 'runtime', 'go', 1,
			 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
			 'agentflow-go:v0001-aaaaaaaaaaaa', 'queued_v2', 1)`,
	},
	{
		table:  "containers",
		column: "state",
		value:  "hibernating",
		statement: `INSERT INTO containers
			(id, host_id, name, env_type, env_name, env_version, build_config_sha256,
			 image_id, state, created_at, updated_at)
			VALUES ('probe-container', 'probe-host', 'agentflow-probe',
			 'language', 'go', 'v0001',
			 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			 'hibernating', 1, 1)`,
	},
	{
		table:  "code_saves",
		column: "commit_status",
		value:  "deferred",
		statement: `INSERT INTO code_saves
			(id, task_id, container_id, remote_name, task_branch, commit_status,
			 push_status, merge_status, created_at, updated_at)
			VALUES ('probe-code-save', 'probe-task', 'probe-container', 'origin',
			 'agent/probe-task', 'deferred', 'deferred', 'deferred', 1, 1)`,
	},
	{
		table:  "validation_runs",
		column: "status",
		value:  "awaiting_rerun",
		statement: `INSERT INTO validation_runs
			(id, task_id, step_id, attempt_id, status, verdict, spec_json,
			 total_checks, passed_checks, failed_checks, skipped_checks,
			 started_at, created_at, updated_at)
			VALUES ('probe-validation-run', 'orphan-task', 'orphan-step',
			 'orphan-attempt', 'awaiting_rerun', 'pending_review', '{}',
			 0, 0, 0, 0, 1, 1, 1)`,
	},
	{
		table:  "validation_check_results",
		column: "status",
		value:  "quarantined",
		statement: `INSERT INTO validation_check_results
			(id, validation_run_id, check_id, check_name, definition_json, status,
			 exit_code, started_at, finished_at, created_at)
			VALUES ('probe-check-result', 'orphan-run', 'probe-check', '探针检查',
			 '{}', 'quarantined', -1, 1, 1, 1)`,
	},
	{
		table:  "roles",
		column: "type",
		value:  "auditor",
		statement: `INSERT INTO roles
			(id, name, type, runner, model, system_prompt, tools_policy_json,
			 created_at, updated_at)
			VALUES ('probe-role', '审计员', 'auditor', 'pi', 'p/m', '提示词', '{}', 1, 1)`,
	},
}

// requiredConstraint 是一条必须保留的非枚举约束。
type requiredConstraint struct {
	table    string
	fragment string
}

// requiredConstraints 是重建后必须保留的非枚举 CHECK 片段。
var requiredConstraints = []requiredConstraint{
	{"requirements", "priority >= 0"},
	{"requirements", "length(trim(title)) > 0"},
	{"steps", "sequence > 0"},
	{"steps", "max_attempts BETWEEN 1 AND 10"},
	{"steps", "json_valid(instructions_json)"},
	{"attempts", "attempt_number > 0"},
	{"attempts", "json_valid(session_ids_json)"},
	{"tasks", "json_valid(config_json)"},
	{"tasks", "cancel_requested = 0 OR cancel_requested_at IS NOT NULL"},
	{"tasks", "current_attempt_id IS NULL OR current_step_id IS NOT NULL"},
	{"sessions", "attempt_id IS NULL OR step_id IS NOT NULL"},
	{"sessions", "process_id IS NULL OR process_id > 0"},
	{"execution_hosts", "port BETWEEN 1 AND 65535"},
	{"environment_versions", "version_number BETWEEN 1 AND 9999"},
	{"containers", "length(build_config_sha256) = 64"},
	{"containers", "attempt_id IS NULL OR task_id IS NOT NULL"},
	{"code_saves", "base_branch IS NULL OR base_branch <> task_branch"},
	{"code_saves", "length(commit_sha) IN (40, 64)"},
	{"verification_results", "json_valid(checks_json)"},
	{"validation_runs", "json_valid(spec_json)"},
	{"validation_runs", "passed_checks + failed_checks + skipped_checks <= total_checks"},
	{"validation_check_results", "json_valid(definition_json)"},
	{"validation_check_results", "sequence > 0"},
}

// expectedIndexes 是必须保留的索引名。
var expectedIndexes = []string{
	"idx_roles_type",
	"idx_requirements_queue",
	"idx_tasks_requirement",
	"idx_tasks_status",
	"idx_tasks_host",
	"idx_tasks_container",
	"idx_tasks_idempotency",
	"idx_tasks_active",
	"idx_steps_task",
	"idx_steps_role",
	"idx_steps_status",
	"idx_attempts_step",
	"idx_attempts_status",
	"idx_sessions_task",
	"idx_sessions_step",
	"idx_sessions_attempt",
	"idx_sessions_run_status",
	"idx_sessions_completeness",
	"idx_sessions_invocation",
	"idx_sessions_container",
	"idx_verification_results_attempt",
	"idx_verification_results_step",
	"idx_verification_results_verdict",
	"idx_events_task",
	"idx_events_step",
	"idx_events_attempt",
	"idx_execution_hosts_default",
	"idx_execution_hosts_name",
	"idx_environment_versions_host",
	"idx_containers_host_state",
	"idx_containers_task",
	"idx_code_saves_task",
	"idx_code_saves_container",
	"idx_code_saves_push_status",
	"idx_validation_runs_attempt",
	"idx_validation_runs_step",
	"idx_validation_runs_task",
	"idx_validation_runs_status",
	"idx_validation_check_results_run",
	"idx_validation_check_results_status",
}

// quoteIdentifier 校验表名并返回可直接拼接的标识符。
//
// 表名只来自本文件的固定清单，这里额外确认只包含安全字符，避免拼接出错。
func quoteIdentifier(t *testing.T, name string) string {
	t.Helper()
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '_':
		default:
			t.Fatalf("表名包含不安全字符: %q", name)
		}
	}
	return name
}

// tableSQL 返回表的建表语句。
func tableSQL(t *testing.T, conn *sql.DB, table string) string {
	t.Helper()
	var ddl sql.NullString
	err := conn.QueryRowContext(
		context.Background(),
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&ddl)
	if err != nil {
		t.Fatalf("读取表 %s 定义失败: %v", table, err)
	}
	if !ddl.Valid {
		t.Fatalf("表 %s 不存在", table)
	}
	return ddl.String
}

// listTriggers 返回全部触发器定义，键为触发器名。
func listTriggers(t *testing.T, conn *sql.DB) map[string]string {
	t.Helper()
	rows, err := conn.QueryContext(
		context.Background(),
		`SELECT name, coalesce(sql, '') FROM sqlite_master WHERE type = 'trigger'`,
	)
	if err != nil {
		t.Fatalf("读取触发器失败: %v", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	triggers := make(map[string]string, 4)
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatalf("读取触发器失败: %v", err)
		}
		triggers[name] = ddl
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历触发器失败: %v", err)
	}
	return triggers
}

// isPrivateKeyTrigger 判断触发器是否是私钥路径的跨字段要求。
func isPrivateKeyTrigger(name string) bool {
	return name == "trg_execution_hosts_private_key_required" ||
		name == "trg_execution_hosts_private_key_required_update"
}

// listIndexes 返回全部显式索引名。
func listIndexes(t *testing.T, conn *sql.DB) map[string]struct{} {
	t.Helper()
	rows, err := conn.QueryContext(
		context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name NOT LIKE 'sqlite_%'`,
	)
	if err != nil {
		t.Fatalf("读取索引失败: %v", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	indexes := make(map[string]struct{}, 32)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("读取索引失败: %v", err)
		}
		indexes[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历索引失败: %v", err)
	}
	return indexes
}

// sortedKeys 返回有序的键列表，便于失败信息阅读。
func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// columnHasEnumWhitelist 判断某一列是否仍被枚举白名单限制。
//
// 判定方式：用一条临时 UPDATE/INSERT 不可行，因此直接检查建表语句中该列附近的
// CHECK 片段是否包含 IN (...) 形式。
func columnHasEnumWhitelist(t *testing.T, conn *sql.DB, table, column string) bool {
	t.Helper()
	ddl := strings.ToLower(tableSQL(t, conn, table))
	index := strings.Index(ddl, column)
	if index < 0 {
		return false
	}
	// 截取该列定义之后到下一个逗号之间的片段，覆盖列级 CHECK。
	rest := ddl[index:]
	if end := strings.Index(rest, ",\n"); end >= 0 {
		rest = rest[:end]
	}
	return strings.Contains(rest, " in (")
}

// insertUnknownEnumValue 用未知枚举值插入一行探测数据。
func insertUnknownEnumValue(ctx context.Context, conn *sql.DB, probe enumProbe) error {
	if _, err := conn.ExecContext(ctx, probe.statement); err != nil {
		return fmt.Errorf("%s.%s = %q: %w", probe.table, probe.column, probe.value, err)
	}
	return nil
}

// hasConstraint 判断表的建表语句是否包含指定约束片段。
func hasConstraint(t *testing.T, conn *sql.DB, table, fragment string) bool {
	t.Helper()
	normalized := normalizeSQL(tableSQL(t, conn, table))
	return strings.Contains(normalized, normalizeSQL(fragment))
}

// normalizeSQL 把空白折叠为单个空格并转小写，便于比较约束片段。
func normalizeSQL(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}
