package testkit

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// LegacySchemaVersion 是"移除外键与枚举白名单"迁移之前的最后一个 schema 版本。
//
// 该版本由 migrations/0001…0008 生成：业务表带列级/表级外键、级联删除、
// 枚举白名单 CHECK 与布尔 0/1 限制，roles 与 execution_hosts 没有 deleted_at。
const LegacySchemaVersion = 8

// migrationsDirFromRepo 是迁移脚本相对仓库根目录的位置。
const migrationsDirFromRepo = "internal/storage/sqlite/migrations"

// CreateLegacySchema 在给定数据库上构造 v8 版本的完整业务 Schema。
//
// 它直接执行仓库中真实的 0001…0008 迁移脚本，因此旧版本样本与实际发布过的
// Schema 完全一致，不需要在测试里维护第二份 DDL。迁移记录表会被写入 1..8 版本，
// 模拟一个已被旧版本程序迁移过的真实数据库。
//
// 样本只落在调用方提供的测试工作空间中，不读取也不修改开发者本机的真实数据库。
func CreateLegacySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	scripts := readMigrationScripts(t, LegacySchemaVersion)
	MustExec(t, db, `CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY NOT NULL,
    name       TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
)`)
	for _, script := range scripts {
		if _, err := db.Exec(script.sql); err != nil {
			t.Fatalf("执行旧版本迁移 %s 失败: %v", script.name, err)
		}
		MustExec(
			t,
			db,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			script.version,
			script.label,
			1700000000000+int64(script.version),
		)
	}
}

// migrationScript 是一个待执行的迁移脚本。
type migrationScript struct {
	version int
	name    string
	label   string
	sql     string
}

// readMigrationScripts 读取仓库中版本号不超过 maxVersion 的迁移脚本。
func readMigrationScripts(t *testing.T, maxVersion int) []migrationScript {
	t.Helper()
	dir := migrationsDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取迁移目录失败: %s: %v", dir, err)
	}
	scripts := make([]migrationScript, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, label, ok := parseMigrationName(t, entry.Name())
		if !ok || version > maxVersion {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("读取迁移脚本失败: %s: %v", entry.Name(), err)
		}
		scripts = append(scripts, migrationScript{
			version: version,
			name:    entry.Name(),
			label:   label,
			sql:     strings.TrimSpace(string(content)),
		})
	}
	sort.Slice(scripts, func(i, j int) bool { return scripts[i].version < scripts[j].version })
	if len(scripts) == 0 {
		t.Fatalf("迁移目录中没有找到脚本: %s", dir)
	}
	return scripts
}

// migrationsDir 返回仓库中迁移脚本目录的绝对路径。
//
// 路径由本文件的编译期位置推导，因此与执行 go test 时的工作目录无关。
func migrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试辅助代码路径")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	dir := filepath.Clean(filepath.Join(root, migrationsDirFromRepo))
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("迁移目录不存在: %s: %v", dir, err)
	}
	return dir
}

// parseMigrationName 解析 "0001_initial.sql" 形式的迁移文件名。
func parseMigrationName(t *testing.T, name string) (int, string, bool) {
	t.Helper()
	base := strings.TrimSuffix(name, ".sql")
	index := strings.Index(base, "_")
	if index <= 0 {
		return 0, "", false
	}
	var version int
	if _, err := fmt.Sscanf(base[:index], "%d", &version); err != nil {
		t.Fatalf("迁移文件名版本号非法: %s: %v", name, err)
	}
	return version, base[index+1:], true
}
