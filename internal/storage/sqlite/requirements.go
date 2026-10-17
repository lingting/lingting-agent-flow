package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// requirementColumns 是需求表的完整列顺序。
const requirementColumns = `id, title, description, priority, status, source, source_id,
	created_at, updated_at`

// requirementStore 实现 storage.RequirementStore。
type requirementStore struct {
	q queryer
}

// CreateRequirement 写入需求。
func (s requirementStore) CreateRequirement(ctx context.Context, requirement domain.Requirement) error {
	if err := requirement.Validate(); err != nil {
		return err
	}
	const statement = `INSERT INTO requirements (` + requirementColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(requirement.ID),
		requirement.Title,
		requirement.Description,
		requirement.Priority.Rank(),
		string(requirement.Status),
		string(requirement.Source),
		sqlNullString(requirement.SourceID),
		toMillis(requirement.CreatedAt),
		toMillis(requirement.UpdatedAt),
	)
	if err != nil {
		return wrapError("写入需求", err)
	}
	return nil
}

// UpdateRequirement 按主键更新需求。
func (s requirementStore) UpdateRequirement(ctx context.Context, requirement domain.Requirement) error {
	if err := requirement.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE requirements SET
	title = ?, description = ?, priority = ?, status = ?, source = ?,
	source_id = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		requirement.Title,
		requirement.Description,
		requirement.Priority.Rank(),
		string(requirement.Status),
		string(requirement.Source),
		sqlNullString(requirement.SourceID),
		toMillis(requirement.UpdatedAt),
		toIDParam(requirement.ID),
	)
	if err != nil {
		return wrapError("更新需求", err)
	}
	return ensureAffected(result, "需求", requirement.ID)
}

// GetRequirement 读取单个需求。
func (s requirementStore) GetRequirement(
	ctx context.Context,
	id domain.ID,
) (*domain.Requirement, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + requirementColumns + ` FROM requirements WHERE id = ?`
	requirement, err := scanRequirement(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("需求", id.String())
		}
		return nil, wrapError("读取需求", err)
	}
	return requirement, nil
}

// ListRequirements 按队列顺序返回需求：优先级升序、创建时间升序。
func (s requirementStore) ListRequirements(
	ctx context.Context,
	filter storage.RequirementFilter,
) ([]domain.Requirement, error) {
	statement, args := buildRequirementQuery(filter)
	rows, err := s.q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, wrapError("查询需求", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	requirements := make([]domain.Requirement, 0, 8)
	for rows.Next() {
		requirement, err := scanRequirement(rows)
		if err != nil {
			return nil, wrapError("读取需求", err)
		}
		requirements = append(requirements, *requirement)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询需求", err)
	}
	return requirements, nil
}

// buildRequirementQuery 构造需求查询语句与参数。
//
// 条件全部通过占位符绑定，不拼接用户输入。
func buildRequirementQuery(filter storage.RequirementFilter) (string, []any) {
	var builder strings.Builder
	builder.WriteString(`SELECT `)
	builder.WriteString(requirementColumns)
	builder.WriteString(` FROM requirements`)
	args := make([]any, 0, 4)
	if len(filter.Statuses) > 0 {
		builder.WriteString(` WHERE status IN (`)
		builder.WriteString(placeholders(len(filter.Statuses)))
		builder.WriteString(`)`)
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	builder.WriteString(` ORDER BY priority, created_at, rowid`)
	if filter.Limit > 0 {
		builder.WriteString(` LIMIT ?`)
		args = append(args, filter.Limit)
	}
	return builder.String(), args
}

// scanRequirement 从一行结果读取需求。
func scanRequirement(row scanner) (*domain.Requirement, error) {
	var (
		requirement domain.Requirement
		rawID       string
		rank        int
		status      string
		source      string
		sourceID    sql.NullString
		createdAt   int64
		updatedAt   int64
	)
	err := row.Scan(
		&rawID,
		&requirement.Title,
		&requirement.Description,
		&rank,
		&status,
		&source,
		&sourceID,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	priority, err := domain.ParsePriorityRank(rank)
	if err != nil {
		return nil, err
	}
	requirement.ID = domain.ID(rawID)
	requirement.Priority = priority
	requirement.Status = domain.RequirementStatus(status)
	requirement.Source = domain.RequirementSource(source)
	requirement.SourceID = sourceID.String
	requirement.CreatedAt = fromMillis(createdAt)
	requirement.UpdatedAt = fromMillis(updatedAt)
	return &requirement, nil
}
