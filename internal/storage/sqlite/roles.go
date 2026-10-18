package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// roleColumns 是角色表的完整列顺序。
const roleColumns = `id, name, type, runner, model, system_prompt, tools_policy_json,
	enabled, created_at, updated_at`

// roleStore 实现 storage.RoleStore。
type roleStore struct {
	q queryer
}

// CreateRole 写入角色。
func (s roleStore) CreateRole(ctx context.Context, role domain.Role) error {
	if err := role.Validate(); err != nil {
		return err
	}
	policy, err := marshalJSON("Role.tools_policy", role.ToolsPolicy)
	if err != nil {
		return err
	}
	const statement = `INSERT INTO roles (` + roleColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.q.ExecContext(
		ctx,
		statement,
		toIDParam(role.ID),
		role.Name,
		role.Type.String(),
		role.Runner,
		role.Model,
		role.SystemPrompt,
		policy,
		toBoolInt(role.Enabled),
		toMillis(role.CreatedAt),
		toMillis(role.UpdatedAt),
	)
	if err != nil {
		return wrapError("写入角色", err)
	}
	return nil
}

// UpdateRole 按主键更新角色。
//
// 角色 ID 不可修改；记录不存在时返回 storage.ErrNotFound。
func (s roleStore) UpdateRole(ctx context.Context, role domain.Role) error {
	if err := role.Validate(); err != nil {
		return err
	}
	policy, err := marshalJSON("Role.tools_policy", role.ToolsPolicy)
	if err != nil {
		return err
	}
	const statement = `
UPDATE roles SET
	name = ?, type = ?, runner = ?, model = ?, system_prompt = ?,
	tools_policy_json = ?, enabled = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		role.Name,
		role.Type.String(),
		role.Runner,
		role.Model,
		role.SystemPrompt,
		policy,
		toBoolInt(role.Enabled),
		toMillis(role.UpdatedAt),
		toIDParam(role.ID),
	)
	if err != nil {
		return wrapError("更新角色", err)
	}
	if err := ensureAffected(result, "角色", role.ID); err != nil {
		return err
	}
	return nil
}

// DeleteRole 逻辑删除角色。
//
// 数据库不声明外键，删除角色不会影响历史步骤与会话记录。这里只把 deleted_at
// 置为当前时间并禁用角色，历史引用仍能读到该角色。
func (s roleStore) DeleteRole(ctx context.Context, id domain.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	now := nowMillis()
	result, err := s.q.ExecContext(
		ctx,
		`UPDATE roles SET deleted_at = ?, enabled = 0, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now,
		now,
		toIDParam(id),
	)
	if err != nil {
		return wrapError("删除角色", err)
	}
	return ensureAffected(result, "角色", id)
}

// GetRole 读取单个角色。
func (s roleStore) GetRole(ctx context.Context, id domain.ID) (*domain.Role, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + roleColumns + ` FROM roles WHERE id = ? AND deleted_at IS NULL`
	role, err := scanRole(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("角色", id.String())
		}
		return nil, wrapError("读取角色", err)
	}
	return role, nil
}

// ListRoles 按创建时间返回全部未删除的角色。
func (s roleStore) ListRoles(ctx context.Context) ([]domain.Role, error) {
	const statement = `SELECT ` + roleColumns + ` FROM roles WHERE deleted_at IS NULL
		ORDER BY created_at, rowid`
	rows, err := s.q.QueryContext(ctx, statement)
	if err != nil {
		return nil, wrapError("查询角色", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	roles := make([]domain.Role, 0, 8)
	for rows.Next() {
		role, err := scanRole(rows)
		if err != nil {
			return nil, wrapError("读取角色", err)
		}
		roles = append(roles, *role)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询角色", err)
	}
	return roles, nil
}

// SeedRoles 在角色表为空时导入初始角色，已有记录时不做任何修改。
//
// 返回实际写入的角色数量。该操作是显式初始化入口，不会被普通启动流程隐式调用。
func (s roleStore) SeedRoles(ctx context.Context, seeds []storage.RoleSeed) (int, error) {
	roles, err := buildSeedRoles(seeds)
	if err != nil {
		return 0, err
	}
	if len(roles) == 0 {
		return 0, nil
	}
	// 检查与写入在同一个事务内完成，避免并发初始化时重复导入。
	withTx, ok := s.q.(transactioner)
	if !ok {
		return 0, fmt.Errorf("sqlite: 角色初始化需要事务支持")
	}
	created := 0
	err = withTx.runInTx(ctx, func(tx queryer) error {
		inner := roleStore{q: tx}
		existing, err := inner.countRoles(ctx)
		if err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		for _, role := range roles {
			if err := inner.CreateRole(ctx, role); err != nil {
				return err
			}
			created++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

// countRoles 返回未删除角色的数量。
//
// 逻辑删除的角色不参与"是否已有角色"的判断，但保留物理行以维持历史引用。
func (s roleStore) countRoles(ctx context.Context) (int, error) {
	var total int
	if err := s.q.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM roles WHERE deleted_at IS NULL`,
	).Scan(&total); err != nil {
		return 0, wrapError("统计角色", err)
	}
	return total, nil
}

// buildSeedRoles 把初始化定义批量转换为可持久化的角色。
//
// 时间戳由调用时刻生成；角色 ID 沿用配置中的标识，保证配置与数据库可对照。
// 全部定义先完成校验，避免中途失败留下部分角色。
func buildSeedRoles(seeds []storage.RoleSeed) ([]domain.Role, error) {
	if len(seeds) == 0 {
		return nil, nil
	}
	roles := make([]domain.Role, 0, len(seeds))
	createdAt := fromMillis(nowMillis())
	for _, seed := range seeds {
		role, err := buildSeedRole(seed, createdAt)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// buildSeedRole 把单条初始化定义转换为可持久化的角色。
func buildSeedRole(seed storage.RoleSeed, createdAt time.Time) (domain.Role, error) {
	if err := seed.ID.Validate(); err != nil {
		return domain.Role{}, err
	}
	runner := strings.TrimSpace(seed.Runner)
	if runner == "" {
		runner = domain.RoleRunnerPi
	}
	role := domain.Role{
		ID:           seed.ID,
		Name:         strings.TrimSpace(seed.Name),
		Type:         seed.Type,
		Runner:       runner,
		Model:        strings.TrimSpace(seed.Model),
		SystemPrompt: strings.TrimSpace(seed.SystemPrompt),
		ToolsPolicy:  seed.ToolsPolicy,
		Enabled:      true,
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}
	if err := role.Validate(); err != nil {
		return domain.Role{}, err
	}
	return role, nil
}

// scanRole 从一行结果读取角色。
func scanRole(row scanner) (*domain.Role, error) {
	var (
		role      domain.Role
		rawID     string
		rawType   string
		policyRaw string
		enabled   int
		createdAt int64
		updatedAt int64
	)
	err := row.Scan(
		&rawID,
		&role.Name,
		&rawType,
		&role.Runner,
		&role.Model,
		&role.SystemPrompt,
		&policyRaw,
		&enabled,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	role.ID = domain.ID(rawID)
	role.Type = domain.RoleType(rawType)
	role.Enabled = fromBoolInt(enabled)
	role.CreatedAt = fromMillis(createdAt)
	role.UpdatedAt = fromMillis(updatedAt)
	if err := unmarshalJSON("Role.tools_policy", policyRaw, &role.ToolsPolicy); err != nil {
		return nil, err
	}
	return &role, nil
}

// scanner 抽象 *sql.Row 与 *sql.Rows 的扫描能力。
type scanner interface {
	Scan(dest ...any) error
}

// ensureAffected 确认更新或删除命中了记录。
func ensureAffected(result sql.Result, entity string, id domain.ID) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: 统计 %s 影响行数失败: %w", entity, err)
	}
	if affected == 0 {
		return notFoundError(entity, id.String())
	}
	return nil
}
