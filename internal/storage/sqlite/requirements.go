package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// requirementColumns 是需求表的完整列顺序。
//
// 确认生成相关列追加在末尾：历史手工需求的这些列全部为 NULL，读取层不得把空值补成
// 默认项目、默认快照或默认确认时间。
const requirementColumns = `id, title, description, priority, status, source, source_id,
	created_at, updated_at, project_id, planning_session_id,
	final_analysis_json, final_plan_json, confirmed_at, deleted_at`

// requirementInsertCount 是写入需求时的参数个数。
const requirementInsertCount = 15

// requirementStore 实现 storage.RequirementStore。
type requirementStore struct {
	q queryer
}

// CreateRequirement 写入需求。
func (s requirementStore) CreateRequirement(ctx context.Context, requirement domain.Requirement) error {
	if err := requirement.Validate(); err != nil {
		return err
	}
	statement := `INSERT INTO requirements (` + requirementColumns + `)
		VALUES (` + placeholders(requirementInsertCount) + `)`
	_, err := s.q.ExecContext(ctx, statement, requirementInsertArgs(requirement)...)
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
	source_id = ?, updated_at = ?, project_id = ?, planning_session_id = ?,
	final_analysis_json = ?, final_plan_json = ?, confirmed_at = ?, deleted_at = ?
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
		sqlNullString(requirement.ProjectID),
		toIDPtrParam(requirement.PlanningSessionID),
		sqlNullString(string(requirement.FinalAnalysisJSON)),
		sqlNullString(string(requirement.FinalPlanJSON)),
		toMillisPtr(requirement.ConfirmedAt),
		toMillisPtr(requirement.DeletedAt),
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
//
// 已逻辑删除的需求默认不返回；排序固定为优先级、创建时间与写入顺序，保证同一批数据
// 每次查询得到相同顺序。
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
	builder.WriteString(` FROM requirements WHERE deleted_at IS NULL`)
	args := make([]any, 0, 6)
	if projectID := strings.ToLower(strings.TrimSpace(filter.ProjectID)); projectID != "" {
		builder.WriteString(` AND project_id = ?`)
		args = append(args, projectID)
	}
	if len(filter.Statuses) > 0 {
		builder.WriteString(` AND status IN (`)
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

// requirementInsertArgs 构造写入需求的参数列表。
func requirementInsertArgs(requirement domain.Requirement) []any {
	return []any{
		toIDParam(requirement.ID),
		requirement.Title,
		requirement.Description,
		requirement.Priority.Rank(),
		string(requirement.Status),
		string(requirement.Source),
		sqlNullString(requirement.SourceID),
		toMillis(requirement.CreatedAt),
		toMillis(requirement.UpdatedAt),
		sqlNullString(requirement.ProjectID),
		toIDPtrParam(requirement.PlanningSessionID),
		sqlNullString(string(requirement.FinalAnalysisJSON)),
		sqlNullString(string(requirement.FinalPlanJSON)),
		toMillisPtr(requirement.ConfirmedAt),
		toMillisPtr(requirement.DeletedAt),
	}
}

// scanRequirement 从一行结果读取需求。
func scanRequirement(row scanner) (*domain.Requirement, error) {
	var (
		requirement       domain.Requirement
		rawID             string
		rank              int
		status            string
		source            string
		sourceID          sql.NullString
		createdAt         int64
		updatedAt         int64
		projectID         sql.NullString
		planningSessionID sql.NullString
		analysisJSON      sql.NullString
		planJSON          sql.NullString
		confirmedAt       sql.NullInt64
		deletedAt         sql.NullInt64
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
		&projectID,
		&planningSessionID,
		&analysisJSON,
		&planJSON,
		&confirmedAt,
		&deletedAt,
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
	requirement.ProjectID = projectID.String
	requirement.PlanningSessionID = fromIDPtr(planningSessionID)
	requirement.FinalAnalysisJSON = rawJSONOrNil(analysisJSON)
	requirement.FinalPlanJSON = rawJSONOrNil(planJSON)
	requirement.ConfirmedAt = fromMillisPtr(confirmedAt)
	requirement.DeletedAt = fromMillisPtr(deletedAt)
	return &requirement, nil
}

// rawJSONOrNil 把可空的文本列还原为原始 JSON 快照。
//
// 空值与空字符串都保持为空：NULL 表示"这一行没有该快照"，与"快照内容为空"不是同一件
// 事实，读取层不得为缺失的快照补造内容。
func rawJSONOrNil(value sql.NullString) json.RawMessage {
	if !value.Valid {
		return nil
	}
	trimmed := strings.TrimSpace(value.String)
	if trimmed == "" {
		return nil
	}
	return json.RawMessage(trimmed)
}
