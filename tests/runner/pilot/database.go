package pilot

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/internal/testrunner"

	_ "modernc.org/sqlite" // 只读核对迁移版本表时使用的纯 Go 驱动
)

// databasePath 返回用例工作空间内的数据库文件路径。
func databasePath(workspace string) string {
	return filepath.Join(workspace, filepath.FromSlash(databaseRelativePath))
}

// assertDatabaseCreated 确认服务进程真的在工作空间内建了库。
//
// 它是"服务以工作空间为工作目录"的直接证据：配置里的数据库路径是相对路径，写错位置就找不到文件。
func assertDatabaseCreated(c *testrunner.Context, path string) {
	info, err := os.Stat(path)
	if err != nil {
		c.Fatalf("服务进程没有在工作空间内建库 %s: %v", path, err)
	}
	if info.Size() == 0 {
		c.Fatalf("数据库文件 %s 是空文件", path)
	}
	c.Logf("服务进程已在工作空间内建库：%s（%d 字节）", path, info.Size())
}

// reportJournalSidecars 报告数据库的 WAL/SHM 状态。
//
// 正常关闭数据库时 SQLite 会收起这两个附属文件；仍然存在说明连接没有正常关闭，作为警告
// 记录，不单独判失败：优雅关闭已经由退出码与服务日志标记确认。
func reportJournalSidecars(c *testrunner.Context, path string) {
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		switch {
		case err == nil:
			c.Warnf("服务退出后仍存在 %s（%d 字节）：数据库可能没有正常关闭", path+suffix, info.Size())
		case errors.Is(err, fs.ErrNotExist):
		default:
			c.Warnf("无法确认 %s 是否存在: %v", path+suffix, err)
		}
	}
}

// reopenDatabase 重新打开同一份数据库，确认服务退出后它仍然可用。
//
// 打开走产品自己的入口：连接参数校验、版本化迁移与仓储初始化都会执行，因此"能打开"同时说明
// 文件锁已释放、Schema 完整。角色是服务启动时补齐的固定数据，用它确认打开的确实是那份库。
func reopenDatabase(c *testrunner.Context, path string) {
	ctx := c.Context()
	store, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		c.Fatalf("服务退出后重新打开数据库失败: %v", err)
	}
	if err := store.Ping(ctx); err != nil {
		_ = store.Close()
		c.Fatalf("重新打开的数据库不可用: %v", err)
	}
	roles, listErr := store.ListRoles(ctx)
	closeErr := store.Close()
	if listErr != nil {
		c.Fatalf("读取重新打开数据库的角色失败: %v", listErr)
	}
	if closeErr != nil {
		c.Fatalf("关闭重新打开的数据库失败: %v", closeErr)
	}
	if len(roles) == 0 {
		c.Fatalf("重新打开的数据库里没有角色记录，无法确认这是服务初始化的那份数据库")
	}
	c.Logf("数据库可以重新打开：角色 %d 条", len(roles))
}

// assertMigrationsApplied 确认服务已经把全部迁移执行完成。
//
// 直接读 Goose 的版本表：只有被应用的版本才算完成，出现未应用的行即判失败。用独立连接读取，
// 不经过产品的连接池，因此结论来自磁盘上的真实状态。
func assertMigrationsApplied(c *testrunner.Context, path string) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		c.Fatalf("打开数据库核对迁移版本失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	var applied, total int
	var latest sql.NullInt64
	const query = `SELECT
		(SELECT count(*) FROM goose_db_version WHERE is_applied = 1),
		(SELECT count(*) FROM goose_db_version),
		(SELECT max(version_id) FROM goose_db_version WHERE is_applied = 1)`
	if err := db.QueryRowContext(c.Context(), query).Scan(&applied, &total, &latest); err != nil {
		c.Fatalf("读取 %s 的迁移版本表失败: %v", path, err)
	}
	if applied != total || applied == 0 {
		c.Fatalf("数据库迁移未完成：已应用 %d 条，版本表共 %d 条", applied, total)
	}
	c.Logf("数据库迁移已完成：版本 %d（已应用 %d 条）", latest.Int64, applied)
}
