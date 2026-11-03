package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// planningSessionColumns 是规划会话表的完整列顺序。
const planningSessionColumns = `id, project_id, status, host_id, container_id, source_commit,
	analyst_session_id, planner_session_id, adopted_revision_id, error_summary,
	created_at, updated_at, finished_at`

// planningSessionInsertCount 是写入规划会话时的参数个数。
const planningSessionInsertCount = 13

// planningSessionUpdateSet 是更新规划会话时被改写的列。
//
// 所属项目不在其中：会话一旦建立就绑定项目，改绑会让历史消息与分析指向另一份代码，
// 因此 project_id 只在创建时写入。
const planningSessionUpdateSet = `status = ?, host_id = ?, container_id = ?, source_commit = ?,
	analyst_session_id = ?, planner_session_id = ?, adopted_revision_id = ?, error_summary = ?,
	updated_at = ?, finished_at = ?`

// planningSessionStore 实现 storage.PlanningSessionStore 的会话部分。
type planningSessionStore struct {
	q queryer
}

// CreatePlanningSession 写入规划会话。
func (s planningSessionStore) CreatePlanningSession(
	ctx context.Context,
	session domain.PlanningSession,
) error {
	if err := session.Validate(); err != nil {
		return err
	}
	statement := `INSERT INTO planning_sessions (` + planningSessionColumns + `)
		VALUES (` + placeholders(planningSessionInsertCount) + `)`
	_, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(session.ID),
		strings.TrimSpace(session.ProjectID),
		string(session.Status),
		toIDPtrParam(session.HostID),
		nullIfBlank(session.ContainerID),
		nullIfBlank(session.SourceCommit),
		nullIfBlank(session.AnalystSessionID),
		nullIfBlank(session.PlannerSessionID),
		toIDPtrParam(session.AdoptedRevisionID),
		nullIfBlank(session.ErrorSummary),
		toMillis(session.CreatedAt),
		toMillis(session.UpdatedAt),
		toMillisPtr(session.FinishedAt),
	)
	if err != nil {
		return wrapError("写入规划会话", err)
	}
	return nil
}

// UpdatePlanningSession 按主键更新规划会话。
func (s planningSessionStore) UpdatePlanningSession(
	ctx context.Context,
	session domain.PlanningSession,
) error {
	if err := session.Validate(); err != nil {
		return err
	}
	statement := `UPDATE planning_sessions SET ` + planningSessionUpdateSet + ` WHERE id = ?`
	args := appendPlanningSessionUpdateArgs(session)
	result, err := s.q.ExecContext(
		ctx,
		statement,
		append(args, toIDParam(session.ID))...,
	)
	if err != nil {
		return wrapError("更新规划会话", err)
	}
	return ensureAffected(result, "规划会话", session.ID)
}

// UpdatePlanningSessionIfStatus 在数据库中的状态仍属于 expected 时写入规划会话。
//
// 这是"迟到的规划结果不得覆盖已确认、已放弃或已失败的会话结论"的落点：影响行数为 0
// 说明状态已被其他执行者推进，按 storage.ErrConflict 返回，调用方必须重新读取真实
// 记录，而不是用本地对象覆盖新事实。
func (s planningSessionStore) UpdatePlanningSessionIfStatus(
	ctx context.Context,
	session domain.PlanningSession,
	expected []domain.PlanningSessionStatus,
) error {
	if err := session.Validate(); err != nil {
		return err
	}
	statement := `UPDATE planning_sessions SET ` + planningSessionUpdateSet + ` WHERE id = ?`
	args := appendPlanningSessionUpdateArgs(session)
	args = append(args, toIDParam(session.ID))
	if len(expected) > 0 {
		candidates := make([]string, 0, len(expected))
		for _, status := range expected {
			candidates = append(candidates, "?")
			args = append(args, string(status))
		}
		statement += ` AND status IN (` + strings.Join(candidates, ", ") + `)`
	}
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return wrapError("更新规划会话", err)
	}
	if len(expected) == 0 {
		return ensureAffected(result, "规划会话", session.ID)
	}
	return ensureStatusAffected(result, "规划会话", session.ID)
}

// GetPlanningSession 读取单个规划会话。
func (s planningSessionStore) GetPlanningSession(
	ctx context.Context,
	id domain.ID,
) (*domain.PlanningSession, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + planningSessionColumns + ` FROM planning_sessions WHERE id = ?`
	session, err := scanPlanningSession(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("规划会话", id.String())
		}
		return nil, wrapError("读取规划会话", err)
	}
	return &session, nil
}

// ListPlanningSessionsByProject 按项目列出规划会话，最近的在前。
//
// 同一毫秒内以标识兜底，保证重复执行得到一致顺序。
func (s planningSessionStore) ListPlanningSessionsByProject(
	ctx context.Context,
	projectID string,
) ([]domain.PlanningSession, error) {
	trimmed := strings.TrimSpace(projectID)
	if trimmed == "" {
		return nil, wrapError(
			"查询规划会话列表",
			errors.Join(storage.ErrInvalidData, errors.New("项目标识不能为空")),
		)
	}
	if err := domain.ValidateProjectID(trimmed); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + planningSessionColumns + ` FROM planning_sessions
		WHERE project_id = ?
		ORDER BY created_at DESC, id DESC`
	rows, err := s.q.QueryContext(ctx, statement, trimmed)
	if err != nil {
		return nil, wrapError("查询规划会话列表", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	sessions := make([]domain.PlanningSession, 0, 8)
	for rows.Next() {
		session, scanErr := scanPlanningSession(rows)
		if scanErr != nil {
			return nil, wrapError("读取规划会话", scanErr)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询规划会话列表", err)
	}
	return sessions, nil
}

// ensureSessionExists 确认规划会话存在。
//
// 数据库不声明外键，因此消息与方案版本的写入路径必须自己确认目标会话存在：把记录
// 追加到不存在的会话会让这段历史无法被任何会话读取。
func (s planningSessionStore) ensureSessionExists(ctx context.Context, id domain.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	const statement = `SELECT 1 FROM planning_sessions WHERE id = ?`
	var exists int
	err := s.q.QueryRowContext(ctx, statement, toIDParam(id)).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFoundError("规划会话", id.String())
		}
		return wrapError("读取规划会话", err)
	}
	return nil
}

// appendPlanningSessionUpdateArgs 构造更新规划会话的参数，顺序与列一一对应。
func appendPlanningSessionUpdateArgs(session domain.PlanningSession) []any {
	return []any{
		string(session.Status),
		toIDPtrParam(session.HostID),
		nullIfBlank(session.ContainerID),
		nullIfBlank(session.SourceCommit),
		nullIfBlank(session.AnalystSessionID),
		nullIfBlank(session.PlannerSessionID),
		toIDPtrParam(session.AdoptedRevisionID),
		nullIfBlank(session.ErrorSummary),
		toMillis(session.UpdatedAt),
		toMillisPtr(session.FinishedAt),
	}
}

// planningSessionScanner 抽象单行与多行查询结果。
type planningSessionScanner interface {
	Scan(dest ...any) error
}

// scanPlanningSession 从一行结果还原规划会话。
//
// 状态原样读回，不在读取路径做枚举校验：数据库不限定取值集合，未知状态必须保留，
// 不得被映射为默认状态或当作损坏数据丢弃。
func scanPlanningSession(row planningSessionScanner) (domain.PlanningSession, error) {
	var (
		id                string
		projectID         string
		status            string
		hostID            sql.NullString
		containerID       sql.NullString
		sourceCommit      sql.NullString
		analystSessionID  sql.NullString
		plannerSessionID  sql.NullString
		adoptedRevisionID sql.NullString
		errorSummary      sql.NullString
		createdAt         int64
		updatedAt         int64
		finishedAt        sql.NullInt64
	)
	if err := row.Scan(
		&id,
		&projectID,
		&status,
		&hostID,
		&containerID,
		&sourceCommit,
		&analystSessionID,
		&plannerSessionID,
		&adoptedRevisionID,
		&errorSummary,
		&createdAt,
		&updatedAt,
		&finishedAt,
	); err != nil {
		return domain.PlanningSession{}, err
	}
	return domain.PlanningSession{
		ID:                domain.ID(id),
		ProjectID:         projectID,
		Status:            domain.PlanningSessionStatus(status),
		HostID:            fromIDPtr(hostID),
		ContainerID:       containerID.String,
		SourceCommit:      sourceCommit.String,
		AnalystSessionID:  analystSessionID.String,
		PlannerSessionID:  plannerSessionID.String,
		AdoptedRevisionID: fromIDPtr(adoptedRevisionID),
		ErrorSummary:      errorSummary.String,
		CreatedAt:         fromMillis(createdAt),
		UpdatedAt:         fromMillis(updatedAt),
		FinishedAt:        fromMillisPtr(finishedAt),
	}, nil
}
