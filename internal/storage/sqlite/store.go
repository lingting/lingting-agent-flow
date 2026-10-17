package sqlite

import (
	"context"
	"database/sql"

	"github.com/lingting/agentflow/internal/storage"
)

// dbStore 把连接池上的各实体仓储组合成 storage.Store。
//
// 直接调用其方法等价于每个方法各自使用一条连接，不构成跨实体事务；
// 需要跨实体原子更新时使用 DB.WithTx。
type dbStore struct {
	roleStore
	executionHostStore
	requirementStore
	taskStore
	stepStore
	attemptStore
	sessionStore
	verificationStore
	eventStore
}

// txStore 把事务上的各实体仓储组合成 storage.Tx。
type txStore struct {
	roleStore
	executionHostStore
	requirementStore
	taskStore
	stepStore
	attemptStore
	sessionStore
	verificationStore
	eventStore
}

// newDBStore 构造连接池仓储。
func newDBStore(db *sql.DB) *dbStore {
	pool := sqlPool{db: db}
	return &dbStore{
		roleStore:          roleStore{q: pool},
		executionHostStore: executionHostStore{q: pool},
		requirementStore:   requirementStore{q: pool},
		taskStore:          taskStore{q: pool},
		stepStore:          stepStore{q: pool},
		attemptStore:       attemptStore{q: pool},
		sessionStore:       sessionStore{q: pool},
		verificationStore:  verificationStore{q: pool},
		eventStore:         eventStore{q: pool},
	}
}

// newTxStore 构造事务仓储，全部实体共享同一个事务连接。
func newTxStore(tx *sql.Tx) *txStore {
	return &txStore{
		roleStore:          roleStore{q: tx},
		executionHostStore: executionHostStore{q: tx},
		requirementStore:   requirementStore{q: tx},
		taskStore:          taskStore{q: tx},
		stepStore:          stepStore{q: tx},
		attemptStore:       attemptStore{q: tx},
		sessionStore:       sessionStore{q: tx},
		verificationStore:  verificationStore{q: tx},
		eventStore:         eventStore{q: tx},
	}
}

// 编译期确认组合结果满足接口约束。
var (
	_ storage.Tx    = (*dbStore)(nil)
	_ storage.Tx    = (*txStore)(nil)
	_ storage.Store = (*DB)(nil)
)

// sqlPool 为 *sql.DB 补充按需开启事务的能力。
type sqlPool struct {
	db *sql.DB
}

// ExecContext 执行语句。
func (p sqlPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

// QueryContext 执行查询。
func (p sqlPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}

// QueryRowContext 执行单行查询。
func (p sqlPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.db.QueryRowContext(ctx, query, args...)
}

// runInTx 在独立事务内执行 fn，用于组合仓储的批量写入。
func (p sqlPool) runInTx(ctx context.Context, fn func(tx queryer) error) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapError("开启事务", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapError("提交事务", err)
	}
	committed = true
	return nil
}
