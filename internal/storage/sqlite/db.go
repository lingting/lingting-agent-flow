// Package sqlite 基于 database/sql 与 modernc.org/sqlite 实现 storage 接口。
//
// 本包负责数据库连接、迁移与各实体的持久化，业务层只能通过 storage 接口访问数据。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/storage"

	_ "modernc.org/sqlite" // 注册纯 Go 的 SQLite 驱动
)

// 连接与锁等待相关的默认参数。
const (
	// defaultBusyTimeout 是等待写锁释放的最长时间。
	//
	// 本地 SQLite 的写操作通过事务串行协调，等待时间足够长可以避免并发写入
	// 直接返回 SQLITE_BUSY。
	defaultBusyTimeout = 5 * time.Second
	// defaultMaxOpenConns 限制连接池大小。
	//
	// 第一版优先保证正确性：只保留少量连接，避免人为制造额外的写锁竞争。
	defaultMaxOpenConns = 4
	// defaultMaxIdleConns 限制空闲连接数。
	defaultMaxIdleConns = 4
	// busyRetryInitialDelay 是初始化阶段忙重试的起始间隔。
	busyRetryInitialDelay = 5 * time.Millisecond
	// busyRetryMaxDelay 是初始化阶段忙重试的间隔上限。
	busyRetryMaxDelay = 200 * time.Millisecond
)

// Options 是打开数据库的选项。
type Options struct {
	// Path 是数据库文件路径，父目录不存在时自动创建。
	Path string
	// BusyTimeout 是等待写锁释放的最长时间，零值使用 defaultBusyTimeout。
	BusyTimeout time.Duration
	// MaxOpenConns 限制连接池大小，零值使用 defaultMaxOpenConns。
	MaxOpenConns int
}

// DB 实现 storage.Store，持有数据库连接与连接池仓储。
type DB struct {
	// dbStore 提供各实体的非事务仓储方法，直接调用时各自使用独立连接。
	*dbStore
	sql *sql.DB
	// path 是数据库文件路径，用于错误信息。
	path string
}

// Open 打开数据库、执行迁移并返回可用的存储实例。
//
// 打开过程依次完成：创建父目录、建立连接、设置连接参数、执行迁移。
// 任一步骤失败都会关闭已建立的连接，避免泄漏。
//
// 并发打开同一数据库时，初始化阶段的 PRAGMA 读取与迁移可能短暂遇到写锁竞争；
// 这里在可重试的错误上按 busy_timeout 的时间窗口重试，避免把正常的并发启动
// 报成失败。
func Open(ctx context.Context, options Options) (*DB, error) {
	path := strings.TrimSpace(options.Path)
	if path == "" {
		return nil, fmt.Errorf("sqlite: 数据库路径不能为空")
	}
	if err := ensureParentDir(path); err != nil {
		return nil, err
	}
	sqlDB, err := openSQL(path, options)
	if err != nil {
		return nil, err
	}
	db := &DB{dbStore: newDBStore(sqlDB), sql: sqlDB, path: path}
	if err := db.retryBusy(ctx, options, db.applyPragmas); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := db.retryBusy(ctx, options, db.migrate); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// retryBusy 在数据库忙时重试 fn，直到超过 busy_timeout 的时间窗口。
//
// 并发打开同一数据库时，SQLite 的写锁竞争可能让初始化阶段的 PRAGMA 读取或迁移
// 短暂返回 SQLITE_BUSY。这里只在明确属于"忙"的错误上重试，其他错误立即返回，
// 避免把真正的迁移失败误判成可重试问题。
func (db *DB) retryBusy(
	ctx context.Context,
	options Options,
	fn func(ctx context.Context) error,
) error {
	timeout := options.BusyTimeout
	if timeout <= 0 {
		timeout = defaultBusyTimeout
	}
	deadline := time.Now().Add(timeout)
	delay := busyRetryInitialDelay
	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrBusy) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(delay):
		}
		if delay < busyRetryMaxDelay {
			delay *= 2
		}
	}
}

// Close 关闭数据库连接，重复调用返回 nil。
func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	err := db.sql.Close()
	db.sql = nil
	return err
}

// Path 返回数据库文件路径。
func (db *DB) Path() string {
	if db == nil {
		return ""
	}
	return db.path
}

// Ping 用一次只读查询确认数据库仍然可用。
//
// 这里不读取任何业务表：健康检查只需要知道连接能否完成一次真实往返，
// 不能因为业务表为空而误判为不可用。
func (db *DB) Ping(ctx context.Context) error {
	if err := db.ensureOpen(); err != nil {
		return err
	}
	var one int
	if err := db.sql.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return wrapError("检查数据库连接", err)
	}
	return nil
}

// ensureParentDir 创建数据库文件的父目录。
func ensureParentDir(path string) error {
	parent := filepath.Dir(path)
	if parent == "" || parent == "." {
		return nil
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("sqlite: 创建数据库目录失败: %s: %w", parent, err)
	}
	return nil
}

// openSQL 建立数据库连接并设置连接池参数。
//
// 忙等待、外键开关与日志模式通过连接参数在每条新连接上生效，避免依赖连接池中的
// 某一条连接。
func openSQL(path string, options Options) (*sql.DB, error) {
	busyTimeout := options.BusyTimeout
	if busyTimeout <= 0 {
		busyTimeout = defaultBusyTimeout
	}
	maxOpenConns := options.MaxOpenConns
	if maxOpenConns <= 0 {
		maxOpenConns = defaultMaxOpenConns
	}
	dsn := buildDSN(path, busyTimeout)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 打开数据库失败: %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(min(defaultMaxIdleConns, maxOpenConns))
	sqlDB.SetConnMaxLifetime(0)
	return sqlDB, nil
}

// buildDSN 构造带连接参数的 DSN。
func buildDSN(path string, busyTimeout time.Duration) string {
	query := strings.Join([]string{
		"_pragma=busy_timeout(" + formatMillis(busyTimeout) + ")",
		// 业务 Schema 不声明外键，因此显式关闭外键检查：数据库不会因为引用
		// 缺失而拒绝写入，关联完整性由应用层按业务操作自行校验。
		"_pragma=foreign_keys(0)",
		// WAL 模式允许读写并行，写事务之间仍由 SQLite 串行协调。
		"_pragma=journal_mode(WAL)",
		// NORMAL 在 WAL 下仍然保证一致性，同时避免每次提交都强制 fsync。
		"_pragma=synchronous(NORMAL)",
		// 事务一开始就取得写锁，避免"先读后写"升级锁时产生 SQLITE_BUSY。
		// 这是后续原子任务领取所需的事务语义基础。
		"_txlock=immediate",
	}, "&")
	return "file:" + filepath.ToSlash(path) + "?" + query
}

// formatMillis 把时长转换为毫秒字符串。
func formatMillis(duration time.Duration) string {
	return fmt.Sprintf("%d", duration.Milliseconds())
}

// applyPragmas 在已打开的连接上显式确认关键 PRAGMA 生效。
//
// DSN 中的 _pragma 已经作用于每条新连接，这里再校验一次，避免参数拼写错误时静默降级。
func (db *DB) applyPragmas(ctx context.Context) error {
	var foreignKeys int
	if err := db.sql.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return wrapError("读取 foreign_keys 设置", err)
	}
	if foreignKeys != 0 {
		return fmt.Errorf("sqlite: 外键检查未关闭: %s", db.path)
	}
	var journalMode string
	if err := db.sql.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return wrapError("读取 journal_mode 设置", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("sqlite: journal_mode 未设置为 WAL，实际为 %q: %s", journalMode, db.path)
	}
	return nil
}

// WithTx 在单个事务内执行 fn。
//
// fn 返回错误或发生 panic 时回滚，成功后提交；两种情况下事务都不会被遗留占用。
func (db *DB) WithTx(ctx context.Context, fn func(ctx context.Context, tx storage.Tx) error) (err error) {
	if fn == nil {
		return fmt.Errorf("sqlite: 事务回调不能为空")
	}
	if err := db.ensureOpen(); err != nil {
		return err
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return wrapError("开启事务", err)
	}
	// 使用独立上下文，确保 ctx 取消时仍然能够回滚事务。
	committed := false
	defer func() {
		if committed {
			return
		}
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, wrapError("回滚事务", rollbackErr))
		}
	}()
	if err := fn(ctx, newTxStore(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapError("提交事务", err)
	}
	committed = true
	return nil
}

// ensureOpen 确认数据库仍然可用。
func (db *DB) ensureOpen() error {
	if db == nil || db.sql == nil {
		return fmt.Errorf("sqlite: 数据库连接已关闭")
	}
	return nil
}

// queryer 抽象 *sql.DB 与 *sql.Tx 的公共查询能力。
//
// 各实体的仓储实现同时接受两者：事务内使用 *sql.Tx，事务外使用 *sql.DB。
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
