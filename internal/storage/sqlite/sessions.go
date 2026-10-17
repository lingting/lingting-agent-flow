package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// sessionColumns 是会话表的完整列顺序。
const sessionColumns = `id, task_id, step_id, attempt_id, role_id, role_snapshot_json,
	provider, model, external_session_id, process_id, status,
	session_jsonl_path, session_html_path, run_status, completeness, export_status,
	exit_code, failure_summary, started_at, finished_at, created_at`

// sessionStore 实现 storage.SessionStore。
type sessionStore struct {
	q queryer
}

// CreateSession 写入会话。
//
// 写入前在同一连接上校验归属一致性：若会话关联了 Attempt，则该 Attempt 必须属于
// 会话声明的步骤，且该步骤必须属于会话声明的任务，否则返回 storage.ErrInvalidData。
func (s sessionStore) CreateSession(ctx context.Context, session domain.Session) error {
	if err := session.Validate(); err != nil {
		return err
	}
	snapshot, err := marshalJSON("Session.role_snapshot", session.RoleSnapshot)
	if err != nil {
		return err
	}
	if err := s.ensureScopeConsistent(ctx, session); err != nil {
		return err
	}
	const statement = `INSERT INTO sessions (` + sessionColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.q.ExecContext(
		ctx,
		statement,
		toIDParam(session.ID),
		toIDParam(session.TaskID),
		toIDPtrParam(session.StepID),
		toIDPtrParam(session.AttemptID),
		toIDParam(session.RoleID),
		snapshot,
		session.Provider,
		session.Model,
		sqlNullString(session.ExternalSessionID),
		toIntPtrParam(session.ProcessID),
		string(session.Status),
		sqlNullString(session.JSONLPath),
		sqlNullString(session.HTMLPath),
		sqlNullString(string(session.RunStatus)),
		sqlNullString(string(session.Completeness)),
		sqlNullString(string(session.ExportStatus)),
		toIntPtrParam(session.ExitCode),
		sqlNullString(session.FailureSummary),
		toMillis(session.StartedAt),
		toMillisPtr(session.FinishedAt),
		toMillis(session.CreatedAt),
	)
	if err != nil {
		return wrapError("写入会话", err)
	}
	return nil
}

// UpdateSession 按主键更新会话。
//
// 会话的任务、步骤与尝试归属不可修改，只更新状态与运行期元数据。
func (s sessionStore) UpdateSession(ctx context.Context, session domain.Session) error {
	if err := session.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE sessions SET
	provider = ?, model = ?, external_session_id = ?, process_id = ?,
	status = ?, session_jsonl_path = ?, session_html_path = ?,
	run_status = ?, completeness = ?, export_status = ?,
	exit_code = ?, failure_summary = ?, finished_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		session.Provider,
		session.Model,
		sqlNullString(session.ExternalSessionID),
		toIntPtrParam(session.ProcessID),
		string(session.Status),
		sqlNullString(session.JSONLPath),
		sqlNullString(session.HTMLPath),
		sqlNullString(string(session.RunStatus)),
		sqlNullString(string(session.Completeness)),
		sqlNullString(string(session.ExportStatus)),
		toIntPtrParam(session.ExitCode),
		sqlNullString(session.FailureSummary),
		toMillisPtr(session.FinishedAt),
		toIDParam(session.ID),
	)
	if err != nil {
		return wrapError("更新会话", err)
	}
	return ensureAffected(result, "会话", session.ID)
}

// GetSession 读取单个会话。
func (s sessionStore) GetSession(ctx context.Context, id domain.ID) (*domain.Session, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + sessionColumns + ` FROM sessions WHERE id = ?`
	session, err := scanSession(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("会话", id.String())
		}
		return nil, wrapError("读取会话", err)
	}
	return session, nil
}

// ListSessionsByTask 按创建时间返回任务的全部会话。
func (s sessionStore) ListSessionsByTask(
	ctx context.Context,
	taskID domain.ID,
) ([]domain.Session, error) {
	if err := taskID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + sessionColumns + ` FROM sessions
		WHERE task_id = ? ORDER BY created_at, rowid`
	return s.querySessions(ctx, statement, toIDParam(taskID))
}

// ListSessionsByAttempt 按创建时间返回某次尝试的全部会话。
func (s sessionStore) ListSessionsByAttempt(
	ctx context.Context,
	attemptID domain.ID,
) ([]domain.Session, error) {
	if err := attemptID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + sessionColumns + ` FROM sessions
		WHERE attempt_id = ? ORDER BY created_at, rowid`
	return s.querySessions(ctx, statement, toIDParam(attemptID))
}

// querySessions 执行会话查询并读取结果。
func (s sessionStore) querySessions(
	ctx context.Context,
	statement string,
	args ...any,
) ([]domain.Session, error) {
	rows, err := s.q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, wrapError("查询会话", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	sessions := make([]domain.Session, 0, 4)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, wrapError("读取会话", err)
		}
		sessions = append(sessions, *session)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询会话", err)
	}
	return sessions, nil
}

// ensureScopeConsistent 校验会话的任务、步骤与尝试归属一致。
func (s sessionStore) ensureScopeConsistent(ctx context.Context, session domain.Session) error {
	if session.AttemptID == nil {
		return nil
	}
	attemptStepID, err := s.attemptStepID(ctx, *session.AttemptID)
	if err != nil {
		return err
	}
	declaredStepID := ""
	if session.StepID != nil {
		declaredStepID = session.StepID.String()
	}
	if attemptStepID != declaredStepID {
		return fmt.Errorf(
			"sqlite: 会话 %s 的步骤 %s 与尝试所属步骤 %s 不一致: %w",
			session.ID,
			declaredStepID,
			attemptStepID,
			storage.ErrInvalidData,
		)
	}
	return nil
}

// attemptStepID 返回某次尝试所属的步骤标识。
func (s sessionStore) attemptStepID(ctx context.Context, attemptID domain.ID) (string, error) {
	const statement = `SELECT step_id FROM attempts WHERE id = ?`
	var stepID string
	err := s.q.QueryRowContext(ctx, statement, toIDParam(attemptID)).Scan(&stepID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", notFoundError("执行尝试", attemptID.String())
		}
		return "", wrapError("读取尝试归属", err)
	}
	return stepID, nil
}

// toIntPtrParam 把可空整数转换为可写入 NULL 的参数。
func toIntPtrParam(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// scanSession 从一行结果读取会话。
func scanSession(row scanner) (*domain.Session, error) {
	var (
		session       domain.Session
		rawID         string
		taskID        string
		stepID        sql.NullString
		attemptID     sql.NullString
		roleID        string
		snapshotJSON  string
		externalID    sql.NullString
		processID     sql.NullInt64
		status        string
		jsonlPath     sql.NullString
		htmlPath      sql.NullString
		runStatus     sql.NullString
		completeness  sql.NullString
		exportStatus  sql.NullString
		exitCode      sql.NullInt64
		failure       sql.NullString
		startedAt     int64
		finishedAt    sql.NullInt64
		createdAtUnix int64
	)
	err := row.Scan(
		&rawID,
		&taskID,
		&stepID,
		&attemptID,
		&roleID,
		&snapshotJSON,
		&session.Provider,
		&session.Model,
		&externalID,
		&processID,
		&status,
		&jsonlPath,
		&htmlPath,
		&runStatus,
		&completeness,
		&exportStatus,
		&exitCode,
		&failure,
		&startedAt,
		&finishedAt,
		&createdAtUnix,
	)
	if err != nil {
		return nil, err
	}
	session.ID = domain.ID(rawID)
	session.TaskID = domain.ID(taskID)
	session.StepID = fromIDPtr(stepID)
	session.AttemptID = fromIDPtr(attemptID)
	session.RoleID = domain.ID(roleID)
	session.ExternalSessionID = externalID.String
	session.ProcessID = fromIntPtr(processID)
	session.Status = domain.SessionStatus(status)
	session.JSONLPath = jsonlPath.String
	session.HTMLPath = htmlPath.String
	session.RunStatus = domain.SessionRunStatus(runStatus.String)
	session.Completeness = domain.SessionCompleteness(completeness.String)
	session.ExportStatus = domain.SessionExportStatus(exportStatus.String)
	session.ExitCode = fromIntPtr(exitCode)
	session.FailureSummary = failure.String
	session.StartedAt = fromMillis(startedAt)
	session.FinishedAt = fromMillisPtr(finishedAt)
	session.CreatedAt = fromMillis(createdAtUnix)
	if err := unmarshalJSON("Session.role_snapshot", snapshotJSON, &session.RoleSnapshot); err != nil {
		return nil, err
	}
	return &session, nil
}
