package sqlite

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/lingting/agentflow/internal/storage"
)

// schemaScript 是完整的最终态 Schema，通过 embed 随程序发布，不依赖工作目录下的文件。
//
//go:embed schema.sql
var schemaScript string

// currentSchemaVersion 是当前程序支持的 Schema 标记，必须与 schema.sql 写入的
// PRAGMA user_version 保持一致。
//
// 标记只用于识别"数据库是否已经是当前 Schema"，不是增量迁移的版本记录：不存在更早的
// 版本，也不会据此执行任何升级脚本。
const currentSchemaVersion = 1

// initializeSchema 确认数据库使用当前 Schema，新数据库在此完成一次性初始化。
//
// 三种情况：
//   - 标记与当前 Schema 一致：按当前数据库正常打开，不执行建表语句；
//   - 空数据库：在一个事务内执行 schema.sql 并写入标记；
//   - 其他数据库（旧 Schema、只建了部分表、标记不匹配）：拒绝打开，不升级也不重建。
func (db *DB) initializeSchema(ctx context.Context) error {
	if err := db.ensureOpen(); err != nil {
		return err
	}
	version, tables, err := readSchemaState(ctx, db.sql)
	if err != nil {
		return err
	}
	if version == currentSchemaVersion {
		return nil
	}
	if version != 0 || tables > 0 {
		return unsupportedSchemaError(db.path, version)
	}
	return db.createSchema(ctx)
}

// createSchema 在单个事务内执行完整的 schema.sql。
//
// 事务使用 immediate 语义（DSN 的 _txlock），开启时即持有写锁，因此并发首次打开同一
// 数据库的多个进程被串行化。持有写锁后重新读取数据库状态：其他进程可能已完成初始化，
// 此时直接结束事务，避免重复建表。
//
// SQL 与 PRAGMA user_version 在同一事务内提交：任一步失败整体回滚，不会留下半套结构，
// 也不会留下与结构不符的标记。
func (db *DB) createSchema(ctx context.Context) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return wrapError("开启 Schema 初始化事务", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	version, tables, err := readSchemaState(ctx, tx)
	if err != nil {
		return err
	}
	if version == currentSchemaVersion {
		// 其他进程已完成初始化：回滚空事务后按当前 Schema 打开。
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return wrapError("回滚 Schema 初始化事务", rollbackErr)
		}
		committed = true
		return nil
	}
	if version != 0 || tables > 0 {
		return unsupportedSchemaError(db.path, version)
	}
	if _, err := tx.ExecContext(ctx, schemaScript); err != nil {
		return fmt.Errorf("sqlite: 执行 Schema 初始化脚本失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return wrapError("提交 Schema 初始化事务", err)
	}
	committed = true
	return nil
}

// readSchemaState 读取当前 Schema 标记与已有的业务表数量。
//
// 业务表数量用于区分"空数据库"与"没有标记的非空数据库"：后者只可能是旧 Schema 或
// 建到一半的数据库，程序不提供升级。
func readSchemaState(ctx context.Context, query queryer) (int, int, error) {
	var version int
	if err := query.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, 0, wrapError("读取 Schema 标记", err)
	}
	var tables int
	if err := query.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
	).Scan(&tables); err != nil {
		return 0, 0, wrapError("读取数据库表", err)
	}
	return version, tables, nil
}

// unsupportedSchemaError 构造拒绝打开不兼容数据库的错误。
//
// 错误信息必须让用户知道程序不会自动升级、也不会替用户删除数据库，需要自行备份后手动
// 重建。
func unsupportedSchemaError(path string, version int) error {
	return fmt.Errorf(
		"sqlite: 数据库不是全新库，也不是当前 Schema（数据库标记 %d，当前标记 %d）: %s: "+
			"本程序只支持全新初始化或当前 Schema，不提供旧库升级；"+
			"请先自行备份数据，再手动重建数据库文件: %w",
		version,
		currentSchemaVersion,
		path,
		storage.ErrUnsupportedSchema,
	)
}
