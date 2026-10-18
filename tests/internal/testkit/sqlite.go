package testkit

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite" // 注册纯 Go 的 SQLite 驱动
)

// OpenSQLite 打开一条指向指定数据库文件的独立连接。
//
// 它用于绕过仓储层直接验证数据库约束（CHECK、触发器、表结构），与产品使用的
// 驱动和 PRAGMA 保持一致；连接在测试结束时关闭，数据库文件保留。
//
// 业务 Schema 不声明外键，产品连接显式关闭外键检查，测试连接保持同一设置，
// 避免测试与运行时行为不一致。
func OpenSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开测试数据库连接失败: %s: %v", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("连接测试数据库失败: %s: %v", path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库连接失败: %v", closeErr)
		}
	})
	return db
}

// SQLitePath 返回工作空间内的数据库路径，并确保父目录存在。
func (w *Workspace) SQLitePath() string {
	w.t.Helper()
	w.Dir()
	return w.DatabasePath()
}

// MustSQLite 在工作空间中打开一个全新的数据库连接，并在测试结束时关闭。
func (w *Workspace) MustSQLite(t *testing.T) *sql.DB {
	t.Helper()
	return OpenSQLite(t, w.DatabasePath())
}

// FormatSQLite 返回便于诊断的数据库描述。
func FormatSQLite(path string) string {
	return fmt.Sprintf("SQLITE=%s", path)
}
