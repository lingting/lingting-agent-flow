package testrunner

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite" // 与产品使用同一个纯 Go 驱动，避免测试与运行时行为不一致
)

// sqliteHeader 是 SQLite 数据库文件的前 16 个字节，用于自证文件是可用数据库。
const sqliteHeader = "SQLite format 3\x00"

// SQLiteProvider 在本次运行的工作根目录下创建用例独占的 SQLite 数据库文件。
//
// 提供者只负责"给出一个可用的库文件"：用例自己打开连接、建表与断言；产品 Schema 的
// 迁移仍由 internal/storage/sqlite 承担，这里不参与，也不写入业务数据。
type SQLiteProvider struct {
	root  string
	mu    sync.Mutex
	files map[string]Handle
}

// NewSQLiteProvider 创建一个以 root 为工作根目录的数据库提供者。
func NewSQLiteProvider(root string) *SQLiteProvider {
	return &SQLiteProvider{root: root, files: make(map[string]Handle)}
}

// Acquire 创建并校验一个空数据库文件，句柄的 payload 只暴露 path。
func (p *SQLiteProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	token, err := randomToken(4)
	if err != nil {
		return Handle{}, err
	}
	dir := resourceShardDir(p.root, ResourceSQLiteDB, decl.Name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Handle{}, fmt.Errorf("创建资源 %q 的目录失败: %w", decl.Name, err)
	}
	path := filepath.Join(dir, safeName(decl.Name)+"-"+token+".db")
	if err := createSQLiteFile(ctx, path); err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的数据库失败: %w", decl.Name, err)
	}
	handle, err := buildHandle(decl, map[string]string{"path": path}, "")
	if err != nil {
		removeSQLiteFiles(path)
		return Handle{}, err
	}
	p.mu.Lock()
	p.files[path] = handle
	p.mu.Unlock()
	return handle, nil
}

// Release 删除数据库文件及其 WAL 附属文件，可重复调用。
func (p *SQLiteProvider) Release(ctx context.Context, handle Handle) error {
	path := handle.PayloadValue("path")
	p.mu.Lock()
	_, tracked := p.files[path]
	delete(p.files, path)
	p.mu.Unlock()
	if !tracked {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("释放资源 %q 时上下文已结束: %w", handle.Name, err)
	}
	return removeSQLiteFiles(path)
}

// Verify 自证数据库文件在释放后不再存在。
func (p *SQLiteProvider) Verify(ctx context.Context, handle Handle) error {
	path := handle.PayloadValue("path")
	p.mu.Lock()
	_, tracked := p.files[path]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("数据库文件 %s 仍被提供者持有，释放没有生效", path)
	}
	return verifyFileAbsent(path)
}

// Snapshot 返回仍被占用的数据库文件。
func (p *SQLiteProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.files))
	for path, handle := range p.files {
		entries = append(entries, LiveResource{Handle: handle, Detail: "现存的数据库文件 " + path})
	}
	return entries, nil
}

// createSQLiteFile 创建数据库文件并确认它是可用的 SQLite 数据库。
//
// PRAGMA user_version 会在文件里写入数据库头，因此不会留下 0 字节的"假数据库"。
func createSQLiteFile(ctx context.Context, path string) error {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("打开数据库文件失败: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA user_version = 0"); err != nil {
		return fmt.Errorf("初始化数据库失败: %w", err)
	}
	return verifySQLiteFile(path)
}

// verifySQLiteFile 校验文件存在且带有 SQLite 文件头。
func verifySQLiteFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("数据库文件不可用: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("数据库路径 %s 是目录", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("读取数据库文件失败: %w", err)
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, len(sqliteHeader))
	if _, err := file.Read(header); err != nil {
		return fmt.Errorf("读取数据库文件头失败: %w", err)
	}
	if string(header) != sqliteHeader {
		return fmt.Errorf("文件 %s 不是 SQLite 数据库", path)
	}
	return nil
}

// removeSQLiteFiles 删除数据库文件与 WAL 附属文件，目标不存在视为成功。
func removeSQLiteFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除 %s 失败: %w", candidate, err)
		}
	}
	return nil
}

// verifyFileAbsent 自证路径已不存在。
func verifyFileAbsent(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("路径 %s 仍然存在，资源没有真正释放", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("核验路径 %s 失败: %w", path, err)
	}
	return nil
}

// safeName 把资源声明名转换成可用的文件与容器标识片段。
func safeName(name string) string {
	cleaned := make([]rune, 0, len(name))
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= '0' && char <= '9':
		case char == '-' || char == '_':
		default:
			char = '-'
		}
		cleaned = append(cleaned, char)
	}
	if len(cleaned) == 0 {
		return "resource"
	}
	return string(cleaned)
}
