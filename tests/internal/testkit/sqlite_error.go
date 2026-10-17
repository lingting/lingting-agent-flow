package testkit

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/storage"
)

// sqliteErrorClass 是按错误消息推断的 SQLite 约束类别。
//
// 直接连接不经过仓储层的错误包装，因此这里按约束消息还原成 storage 包定义的
// 哨兵错误，使测试可以用与产品一致的 errors.Is 判断错误类别。
type sqliteErrorClass int

// 支持的约束类别。
const (
	classNone sqliteErrorClass = iota
	classForeignKey
	classInvalidData
	classConflict
)

// MustExec 执行一条不期望失败的语句，失败时立即终止测试。
func MustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("执行语句失败: %s: %v", query, err)
	}
}

// ExecConstraint 执行一条预期违反数据库约束的语句，并返回归一化后的存储错误。
//
// 语句必须被数据库拒绝，否则测试立即失败；返回的错误可以直接用
// errors.Is(err, storage.ErrForeignKey) 等方式判断类别。
func ExecConstraint(t *testing.T, db *sql.DB, query string, args ...any) error {
	t.Helper()
	_, err := db.Exec(query, args...)
	if err == nil {
		t.Fatalf("语句应当被数据库拒绝: %s", query)
	}
	return classifySQLiteError(err)
}

// classifySQLiteError 把驱动错误转换为存储包定义的哨兵错误。
func classifySQLiteError(err error) error {
	switch detectClass(err) {
	case classForeignKey:
		return errors.Join(storage.ErrForeignKey, err)
	case classInvalidData:
		return errors.Join(storage.ErrInvalidData, err)
	case classConflict:
		return errors.Join(storage.ErrConflict, err)
	default:
		return err
	}
}

// detectClass 依据驱动错误消息判断约束类别。
func detectClass(err error) sqliteErrorClass {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "foreign key constraint failed"):
		return classForeignKey
	case strings.Contains(message, "unique constraint failed"),
		strings.Contains(message, "primary key constraint failed"):
		return classConflict
	case strings.Contains(message, "constraint failed"):
		return classInvalidData
	default:
		return classNone
	}
}
