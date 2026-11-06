package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationsDir 是版本化迁移脚本在 migrationFS 中的目录。
const migrationsDir = "migrations"

// migrate 执行所有待执行的迁移脚本。
//
// 迁移脚本是数据库结构的唯一事实来源：已执行的版本由 Goose 记录在 goose_db_version 表中，
// 重复打开同一数据库不会重复执行；脚本顺序、事务与失败回滚都由 Goose 负责，这里只负责把
// 错误包装成带数据库路径的初始化错误。
//
// Provider 不调用 Close：数据库连接由 DB 自己持有，不能在这里提前关闭。
func (db *DB) migrate(ctx context.Context) error {
	if err := db.ensureOpen(); err != nil {
		return err
	}
	provider, err := newMigrationProvider(db.sql)
	if err != nil {
		return fmt.Errorf("sqlite: 初始化迁移组件失败: %s: %w", db.path, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return wrapError(fmt.Sprintf("执行数据库迁移: %s", db.path), err)
	}
	return nil
}

// newMigrationProvider 构造只处理 SQL 迁移的 Goose Provider。
//
// 关闭全局注册表，避免依赖全局可变状态；禁止乱序迁移，保证脚本严格按版本递增执行。
func newMigrationProvider(sqlDB *sql.DB) (*goose.Provider, error) {
	migrations, err := fs.Sub(migrationFS, migrationsDir)
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(
		goose.DialectSQLite3,
		sqlDB,
		migrations,
		goose.WithDisableGlobalRegistry(true),
		goose.WithAllowOutofOrder(false),
	)
}
