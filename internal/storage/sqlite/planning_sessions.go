package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

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
//
// 运行时绑定（host_id / container_id / source_commit）虽然也在其中，但规划运行时不得
// 使用整行更新：它走 UpdatePlanningSessionRuntimeBinding 的条件更新，只写运行时列，
// 避免用旧对象覆盖并发推进的业务状态、采纳的方案版本、失败摘要与角色会话标识。
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

// UpdatePlanningSessionRuntimeBinding 以读取时的运行时绑定为条件写入新的运行时绑定。
//
// 只更新 host_id、container_id、source_commit 与 updated_at：status、finished_at、
// adopted_revision_id、error_summary、analyst_session_id、planner_session_id 与
// project_id 保持数据库中的值，因此规划运行时的写入不可能把已确认的会话改回进行中，
// 也不会清空已保存的方案版本、角色会话标识或失败摘要。
//
// WHERE 条件对三个绑定列做 NULL 安全比较（COALESCE 把 NULL 与空值归一为空串）：未绑定
// 状态必须能与调用方读到的空值正确匹配，否则"写入首个绑定"永远匹配不上，或者退化成
// 一条不带条件的覆盖写入。影响行数为 0 说明会话不存在或绑定已被其他操作改写，前者
// 返回 storage.ErrNotFound，后者返回 storage.ErrConflict。
//
// at 是本次更新时间，调用方必须传入真实时间：一次运行时绑定写入不允许把 updated_at
// 退回到零值或旧时间。
func (s planningSessionStore) UpdatePlanningSessionRuntimeBinding(
	ctx context.Context,
	id domain.ID,
	expected storage.PlanningSessionRuntimeBinding,
	next storage.PlanningSessionRuntimeBinding,
	at time.Time,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if next.HostID != nil {
		if err := next.HostID.Validate(); err != nil {
			return err
		}
	}
	containerID := strings.TrimSpace(next.ContainerID)
	sourceCommit := strings.TrimSpace(next.SourceCommit)
	if sourceCommit != "" {
		if err := domain.ValidateGitCommitSHA("source_commit", sourceCommit); err != nil {
			return err
		}
	}
	// 空值在 SQL 里是 NULL，不能参与等值比较：归一为 '' 后再与 COALESCE 的结果比对。
	expectedHostID := ""
	if expected.HostID != nil {
		expectedHostID = expected.HostID.String()
	}
	const statement = `
UPDATE planning_sessions SET
	host_id = ?, container_id = ?, source_commit = ?, updated_at = ?
WHERE id = ?
	AND COALESCE(host_id, '') = ?
	AND COALESCE(container_id, '') = ?
	AND COALESCE(source_commit, '') = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		toIDPtrParam(next.HostID),
		nullIfBlank(containerID),
		nullIfBlank(sourceCommit),
		toMillis(at),
		toIDParam(id),
		expectedHostID,
		strings.TrimSpace(expected.ContainerID),
		strings.TrimSpace(expected.SourceCommit),
	)
	if err != nil {
		return wrapError("更新规划会话运行时绑定", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("更新规划会话运行时绑定", err)
	}
	if affected > 0 {
		return nil
	}
	// 影响行数为 0 有两种可能：会话不存在，或绑定已被其他操作改写。两者都必须让调用方
	// 重新读取会话，不能当作写入成功。
	current, readErr := s.GetPlanningSession(ctx, id)
	if readErr != nil {
		if errors.Is(readErr, storage.ErrNotFound) {
			return readErr
		}
		return wrapError("更新规划会话运行时绑定", readErr)
	}
	currentHostID := ""
	if current.HostID != nil {
		currentHostID = current.HostID.String()
	}
	return wrapError(
		"更新规划会话运行时绑定",
		errors.Join(
			storage.ErrConflict,
			errors.New(
				"运行时绑定已被其他操作改写，当前执行节点 "+currentHostID+
					"、容器 "+current.ContainerID+"、固定 commit "+current.SourceCommit,
			),
		),
	)
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
