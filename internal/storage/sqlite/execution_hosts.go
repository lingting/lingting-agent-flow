package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// executionHostColumns 是执行节点表的完整列顺序。
const executionHostColumns = `id, name, host, port, username, workspace_root,
	is_default, enabled, auth_method, private_key_path, host_key_algorithm,
	host_key_fingerprint, host_key_trusted_at, last_connect_status,
	last_connect_error, last_connected_at, created_at, updated_at`

// executionHostInsertCount 是写入执行节点时的参数个数。
const executionHostInsertCount = 18

// executionHostStore 实现 storage.ExecutionHostStore。
type executionHostStore struct {
	q queryer
}

// CreateExecutionHost 写入执行节点。
//
// 默认节点的唯一性由部分唯一索引保证：已有默认节点时再写入 is_default = 1
// 会返回 storage.ErrConflict，而不是产生两个默认节点。
func (s executionHostStore) CreateExecutionHost(
	ctx context.Context,
	host domain.ExecutionHost,
) error {
	if err := host.Validate(); err != nil {
		return err
	}
	statement := `INSERT INTO execution_hosts (` + executionHostColumns + `)
		VALUES (` + placeholders(executionHostInsertCount) + `)`
	_, err := s.q.ExecContext(ctx, statement, executionHostArgs(host)...)
	if err != nil {
		return wrapError("写入执行节点", err)
	}
	return nil
}

// UpdateExecutionHost 按主键更新执行节点。
//
// 节点 ID 与创建时间不可修改；记录不存在时返回 storage.ErrNotFound。
func (s executionHostStore) UpdateExecutionHost(
	ctx context.Context,
	host domain.ExecutionHost,
) error {
	if err := host.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE execution_hosts SET
	name = ?, host = ?, port = ?, username = ?, workspace_root = ?,
	is_default = ?, enabled = ?, auth_method = ?, private_key_path = ?,
	host_key_algorithm = ?, host_key_fingerprint = ?, host_key_trusted_at = ?,
	last_connect_status = ?, last_connect_error = ?, last_connected_at = ?,
	updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		host.Name,
		host.Host,
		host.Port,
		host.Username,
		host.WorkspaceRoot,
		toBoolInt(host.IsDefault),
		toBoolInt(host.Enabled),
		host.AuthMethod.String(),
		nullIfBlank(host.PrivateKeyPath),
		nullIfBlank(host.HostKeyAlgorithm),
		nullIfBlank(host.HostKeyFingerprint),
		toMillisPtr(host.HostKeyTrustedAt),
		nullIfBlank(host.LastConnectStatus.String()),
		nullIfBlank(host.LastConnectError),
		toMillisPtr(host.LastConnectedAt),
		toMillis(host.UpdatedAt),
		toIDParam(host.ID),
	)
	if err != nil {
		return wrapError("更新执行节点", err)
	}
	return ensureAffected(result, "执行节点", host.ID)
}

// executionHostArgs 构造写入执行节点时的参数列表。
//
// 顺序必须与 executionHostColumns 完全一致。
func executionHostArgs(host domain.ExecutionHost) []any {
	return []any{
		toIDParam(host.ID),
		host.Name,
		host.Host,
		host.Port,
		host.Username,
		host.WorkspaceRoot,
		toBoolInt(host.IsDefault),
		toBoolInt(host.Enabled),
		host.AuthMethod.String(),
		nullIfBlank(host.PrivateKeyPath),
		nullIfBlank(host.HostKeyAlgorithm),
		nullIfBlank(host.HostKeyFingerprint),
		toMillisPtr(host.HostKeyTrustedAt),
		nullIfBlank(host.LastConnectStatus.String()),
		nullIfBlank(host.LastConnectError),
		toMillisPtr(host.LastConnectedAt),
		toMillis(host.CreatedAt),
		toMillis(host.UpdatedAt),
	}
}

// DeleteExecutionHost 删除执行节点配置。
//
// 删除只影响本机数据库记录，不会删除远程工作空间或远程 Docker 资源。
func (s executionHostStore) DeleteExecutionHost(ctx context.Context, id domain.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	result, err := s.q.ExecContext(ctx, `DELETE FROM execution_hosts WHERE id = ?`, toIDParam(id))
	if err != nil {
		return wrapError("删除执行节点", err)
	}
	return ensureAffected(result, "执行节点", id)
}

// GetExecutionHost 读取单个执行节点。
func (s executionHostStore) GetExecutionHost(
	ctx context.Context,
	id domain.ID,
) (*domain.ExecutionHost, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + executionHostColumns + ` FROM execution_hosts WHERE id = ?`
	host, err := scanExecutionHost(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("执行节点", id.String())
		}
		return nil, wrapError("读取执行节点", err)
	}
	return host, nil
}

// ListExecutionHosts 按创建时间返回全部执行节点。
func (s executionHostStore) ListExecutionHosts(ctx context.Context) ([]domain.ExecutionHost, error) {
	const statement = `SELECT ` + executionHostColumns + ` FROM execution_hosts ORDER BY created_at, rowid`
	rows, err := s.q.QueryContext(ctx, statement)
	if err != nil {
		return nil, wrapError("查询执行节点", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	hosts := make([]domain.ExecutionHost, 0, 8)
	for rows.Next() {
		host, err := scanExecutionHost(rows)
		if err != nil {
			return nil, wrapError("读取执行节点", err)
		}
		hosts = append(hosts, *host)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询执行节点", err)
	}
	return hosts, nil
}

// SetDefaultExecutionHost 在单个事务内切换默认节点。
//
// 先清除原默认节点再设置新节点，两步必须同时成功或同时回滚；数据库的部分唯一索引
// 保证即使出现并发调用也不会留下两个默认节点。节点不存在时返回 storage.ErrNotFound。
func (s executionHostStore) SetDefaultExecutionHost(ctx context.Context, id domain.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	withTx, ok := s.q.(transactioner)
	if !ok {
		// 已在事务内调用时直接复用当前事务，此时由调用方负责整体提交。
		return s.setDefaultInTx(ctx, id)
	}
	return withTx.runInTx(ctx, func(tx queryer) error {
		return executionHostStore{q: tx}.setDefaultInTx(ctx, id)
	})
}

// setDefaultInTx 在给定连接或事务内完成默认节点切换。
func (s executionHostStore) setDefaultInTx(ctx context.Context, id domain.ID) error {
	host, err := s.GetExecutionHost(ctx, id)
	if err != nil {
		return err
	}
	if host.IsDefault {
		return nil
	}
	if _, err := s.q.ExecContext(
		ctx,
		`UPDATE execution_hosts SET is_default = 0 WHERE is_default = 1`,
	); err != nil {
		return wrapError("清除默认执行节点", err)
	}
	result, err := s.q.ExecContext(
		ctx,
		`UPDATE execution_hosts SET is_default = 1, updated_at = ? WHERE id = ?`,
		nowMillis(),
		toIDParam(id),
	)
	if err != nil {
		return wrapError("设置默认执行节点", err)
	}
	return ensureAffected(result, "执行节点", id)
}

// GetDefaultExecutionHost 返回当前默认执行节点。
func (s executionHostStore) GetDefaultExecutionHost(
	ctx context.Context,
) (*domain.ExecutionHost, error) {
	const statement = `SELECT ` + executionHostColumns + ` FROM execution_hosts WHERE is_default = 1`
	host, err := scanExecutionHost(s.q.QueryRowContext(ctx, statement))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("默认执行节点", "")
		}
		return nil, wrapError("读取默认执行节点", err)
	}
	return host, nil
}

// scanExecutionHost 从一行结果读取执行节点。
func scanExecutionHost(row scanner) (*domain.ExecutionHost, error) {
	var (
		host             domain.ExecutionHost
		rawID            string
		isDefault        int
		enabled          int
		authMethod       string
		privateKeyPath   sql.NullString
		hostKeyAlgorithm sql.NullString
		hostKeyPrint     sql.NullString
		hostKeyTrustedAt sql.NullInt64
		connectStatus    sql.NullString
		connectError     sql.NullString
		connectedAt      sql.NullInt64
		createdAt        int64
		updatedAt        int64
	)
	err := row.Scan(
		&rawID,
		&host.Name,
		&host.Host,
		&host.Port,
		&host.Username,
		&host.WorkspaceRoot,
		&isDefault,
		&enabled,
		&authMethod,
		&privateKeyPath,
		&hostKeyAlgorithm,
		&hostKeyPrint,
		&hostKeyTrustedAt,
		&connectStatus,
		&connectError,
		&connectedAt,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	host.ID = domain.ID(rawID)
	host.IsDefault = fromBoolInt(isDefault)
	host.Enabled = fromBoolInt(enabled)
	host.AuthMethod = domain.AuthMethod(authMethod)
	host.PrivateKeyPath = privateKeyPath.String
	host.HostKeyAlgorithm = hostKeyAlgorithm.String
	host.HostKeyFingerprint = hostKeyPrint.String
	host.HostKeyTrustedAt = fromMillisPtr(hostKeyTrustedAt)
	host.LastConnectStatus = domain.ConnectStatus(connectStatus.String)
	host.LastConnectError = connectError.String
	host.LastConnectedAt = fromMillisPtr(connectedAt)
	host.CreatedAt = fromMillis(createdAt)
	host.UpdatedAt = fromMillis(updatedAt)
	return &host, nil
}

// 编译期确认实现满足接口约束。
var _ storage.ExecutionHostStore = executionHostStore{}
var _ storage.ExecutionHostStore = executionHostStore{}
