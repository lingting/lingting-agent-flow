package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/lingting/agentflow/internal/storage"
)

// migrationsFS 嵌入迁移脚本，避免运行时依赖工作目录下的外部 SQL 文件。
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsDir 是嵌入文件系统中的迁移目录。
const migrationsDir = "migrations"

// migrationTable 是记录已应用迁移版本的元数据表。
const migrationTable = "schema_migrations"

// migration 是一个已解析的迁移脚本。
type migration struct {
	version int
	name    string
	script  string
}

// MigrationStatus 描述一个迁移的当前状态。
type MigrationStatus struct {
	Version   int
	Name      string
	Applied   bool
	AppliedAt int64
}

// SchemaVersion 返回已应用的最高迁移版本，空数据库返回 0。
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	if err := db.ensureOpen(); err != nil {
		return 0, err
	}
	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return 0, err
	}
	version := 0
	for candidate := range applied {
		if candidate > version {
			version = candidate
		}
	}
	return version, nil
}

// MigrationStatuses 返回全部内置迁移的应用状态，按版本升序排列。
func (db *DB) MigrationStatuses(ctx context.Context) ([]MigrationStatus, error) {
	if err := db.ensureOpen(); err != nil {
		return nil, err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]MigrationStatus, 0, len(migrations))
	for _, item := range migrations {
		status := MigrationStatus{Version: item.version, Name: item.name}
		if at, ok := applied[item.version]; ok {
			status.Applied = true
			status.AppliedAt = at
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// migrate 执行全部尚未应用的迁移。
//
// 语义幂等：已记录的版本不会重复执行；每个迁移在独立事务内执行，失败时回滚，
// 因此不会留下"表已建但版本未记录"或"版本已记录但表未建"的中间状态。
func (db *DB) migrate(ctx context.Context) error {
	if err := db.ensureOpen(); err != nil {
		return err
	}
	if err := db.ensureMigrationTable(ctx); err != nil {
		return err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return err
	}
	if err := checkSupported(applied, migrations); err != nil {
		return err
	}
	for _, item := range migrations {
		if _, done := applied[item.version]; done {
			continue
		}
		if err := db.applyMigration(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

// ensureMigrationTable 创建迁移记录表。
func (db *DB) ensureMigrationTable(ctx context.Context) error {
	const statement = `
CREATE TABLE IF NOT EXISTS ` + migrationTable + ` (
    version    INTEGER PRIMARY KEY NOT NULL,
    name       TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
)`
	if _, err := db.sql.ExecContext(ctx, statement); err != nil {
		return wrapError("创建迁移记录表", err)
	}
	return nil
}

// appliedVersions 读取已应用的迁移版本及其应用时间。
func (db *DB) appliedVersions(ctx context.Context) (map[int]int64, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT version, applied_at FROM "+migrationTable)
	if err != nil {
		return nil, wrapError("读取迁移版本", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	applied := make(map[int]int64)
	for rows.Next() {
		var version int
		var appliedAt int64
		if err := rows.Scan(&version, &appliedAt); err != nil {
			return nil, wrapError("读取迁移版本", err)
		}
		applied[version] = appliedAt
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("读取迁移版本", err)
	}
	return applied, nil
}

// applyMigration 在独立事务内执行一个迁移并记录版本。
func (db *DB) applyMigration(ctx context.Context, item migration) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return wrapError("开启迁移事务", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, item.script); err != nil {
		return fmt.Errorf(
			"sqlite: 执行迁移 %04d_%s 失败: %w",
			item.version,
			item.name,
			wrapError("执行迁移", err),
		)
	}
	if _, err := tx.ExecContext(
		ctx,
		"INSERT INTO "+migrationTable+" (version, name, applied_at) VALUES (?, ?, ?)",
		item.version,
		item.name,
		nowMillis(),
	); err != nil {
		return wrapError("记录迁移版本", err)
	}
	if err := tx.Commit(); err != nil {
		return wrapError("提交迁移", err)
	}
	committed = true
	return nil
}

// checkSupported 拒绝打开版本高于当前程序支持的数据库。
func checkSupported(applied map[int]int64, migrations []migration) error {
	latest := 0
	for _, item := range migrations {
		if item.version > latest {
			latest = item.version
		}
	}
	for version := range applied {
		if version > latest {
			return fmt.Errorf(
				"sqlite: 数据库版本 %d 高于程序支持的最新版本 %d: %w",
				version,
				latest,
				storage.ErrUnsupportedSchema,
			)
		}
	}
	return nil
}

// loadMigrations 读取并解析嵌入的迁移脚本，按版本升序返回。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读取迁移目录失败: %w", err)
	}
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		item, err := loadMigration(entry.Name())
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, item)
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	return migrations, nil
}

// loadMigration 解析单个迁移文件名与内容。
func loadMigration(name string) (migration, error) {
	version, err := parseMigrationVersion(name)
	if err != nil {
		return migration{}, err
	}
	data, err := migrationsFS.ReadFile(migrationsDir + "/" + name)
	if err != nil {
		return migration{}, fmt.Errorf("sqlite: 读取迁移文件失败: %s: %w", name, err)
	}
	script := strings.TrimSpace(string(data))
	if script == "" {
		return migration{}, fmt.Errorf("sqlite: 迁移文件为空: %s", name)
	}
	base := strings.TrimSuffix(name, ".sql")
	label := base
	if index := strings.Index(base, "_"); index >= 0 {
		label = base[index+1:]
	}
	return migration{version: version, name: label, script: script}, nil
}

// parseMigrationVersion 从文件名前缀解析版本号。
func parseMigrationVersion(name string) (int, error) {
	base := strings.TrimSuffix(name, ".sql")
	index := strings.Index(base, "_")
	if index <= 0 {
		return 0, fmt.Errorf("sqlite: 迁移文件名缺少版本前缀: %s", name)
	}
	version, err := strconv.Atoi(base[:index])
	if err != nil {
		return 0, fmt.Errorf("sqlite: 迁移文件名版本号非法: %s: %w", name, err)
	}
	if version <= 0 {
		return 0, fmt.Errorf("sqlite: 迁移版本号必须大于 0: %s", name)
	}
	return version, nil
}

// sqlNullString 把空字符串转换为 NULL，用于可空文本列。
func sqlNullString(value string) sql.NullString {
	if strings.TrimSpace(value) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

// nullIfBlank 把空字符串转换为 NULL，用于可选文本列。
//
// 与 sqlNullString 不同，这里保留调用方传入的原始值，只把空白视为未设置。
func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
