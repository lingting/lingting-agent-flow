package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// wrapError 把底层错误转换为带操作上下文的存储错误。
//
// 返回的错误保留 storage 包定义的哨兵错误，调用方通过 errors.Is 判断类别；
// 同时用 %w 保留原始驱动错误，便于排查具体约束名。
func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	sentinel := classify(err)
	if sentinel == nil {
		return fmt.Errorf("sqlite: %s失败: %w", operation, err)
	}
	return fmt.Errorf("sqlite: %s失败: %w: %w", operation, sentinel, err)
}

// classify 判断错误属于哪一类存储错误。
func classify(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	if errors.Is(err, storage.ErrNotFound) ||
		errors.Is(err, storage.ErrConflict) ||
		errors.Is(err, storage.ErrForeignKey) ||
		errors.Is(err, storage.ErrInvalidData) ||
		errors.Is(err, storage.ErrBusy) {
		return nil
	}
	message := strings.ToLower(err.Error())
	// 外键失败可能由显式约束或隐式触发器触发，结果码并不统一，因此先按消息判断。
	switch {
	case strings.Contains(message, "foreign key constraint failed"):
		return storage.ErrForeignKey
	case strings.Contains(message, "database is locked"),
		strings.Contains(message, "database table is locked"):
		return storage.ErrBusy
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return classifyCode(sqliteErr.Code())
	}
	if strings.Contains(message, "constraint failed") {
		return storage.ErrInvalidData
	}
	return nil
}

// classifyCode 把 SQLite 结果码映射为存储错误。
func classifyCode(code int) error {
	switch code {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return storage.ErrBusy
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY, sqlite3.SQLITE_CONSTRAINT_TRIGGER:
		return storage.ErrForeignKey
	case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY,
		sqlite3.SQLITE_CONSTRAINT_UNIQUE:
		return storage.ErrConflict
	case sqlite3.SQLITE_CONSTRAINT,
		sqlite3.SQLITE_CONSTRAINT_CHECK,
		sqlite3.SQLITE_CONSTRAINT_NOTNULL:
		return storage.ErrInvalidData
	default:
		return nil
	}
}

// notFoundError 构造带实体信息的记录不存在错误。
func notFoundError(entity, id string) error {
	return fmt.Errorf("sqlite: %s %s: %w", entity, id, storage.ErrNotFound)
}

// ensureStatusAffected 确认带状态前置条件的更新命中记录。
//
// 影响行数为 0 只说明条件不成立：记录可能已被删除，也可能状态已被其他操作改变。
// 两种情况都按 storage.ErrConflict 返回，调用方必须重新读取当前状态，不能假定写入成功。
func ensureStatusAffected(result sql.Result, entity string, id domain.ID) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: 统计 %s 影响行数失败: %w", entity, err)
	}
	if affected == 0 {
		return fmt.Errorf(
			"sqlite: %s %s 的状态已变更，拒绝过期更新: %w",
			entity,
			id.String(),
			storage.ErrConflict,
		)
	}
	return nil
}
