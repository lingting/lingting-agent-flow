package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// sessionColumns 是会话表的完整列顺序。
const sessionColumns = `id, task_id, step_id, attempt_id, role_id, role_snapshot_json,
	provider, model, external_session_id, process_id, status,
	session_jsonl_path, session_html_path, run_status, completeness, export_status,
	archive_status, archive_path, stdout_path, stderr_path,
	exit_code, failure_summary, invocation_id, container_id, docker_id,
	started_at, finished_at, created_at`

// sessionStore 实现 storage.SessionStore。
type sessionStore struct {
	q queryer
}

// CreateSession 写入会话。
// 写入前在同一连接上校验归属一致性：若会话关联了 Attempt，则该 Attempt 所属的
// 步骤必须与会话声明的步骤一致，否则返回 storage.ErrInvalidData。
//
// 数据库不声明外键，因此这里只校验能够读到父记录时的归属关系；父记录已不存在
// （历史孤立 ID）时不阻止写入。
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
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
		sqlNullString(string(session.ArchiveStatus)),
		sqlNullString(session.ArchivePath),
		sqlNullString(session.StdoutPath),
		sqlNullString(session.StderrPath),
		toIntPtrParam(session.ExitCode),
		sqlNullString(session.FailureSummary),
		sqlNullString(session.InvocationID),
		sqlNullString(session.ContainerID),
		sqlNullString(session.DockerID),
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
	archive_status = ?, archive_path = ?, stdout_path = ?, stderr_path = ?,
	exit_code = ?, failure_summary = ?,
	invocation_id = ?, container_id = ?, docker_id = ?, finished_at = ?
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
		sqlNullString(string(session.ArchiveStatus)),
		sqlNullString(session.ArchivePath),
		sqlNullString(session.StdoutPath),
		sqlNullString(session.StderrPath),
		toIntPtrParam(session.ExitCode),
		sqlNullString(session.FailureSummary),
		sqlNullString(session.InvocationID),
		sqlNullString(session.ContainerID),
		sqlNullString(session.DockerID),
		toMillisPtr(session.FinishedAt),
		toIDParam(session.ID),
	)
	if err != nil {
		return wrapError("更新会话", err)
	}
	return ensureAffected(result, "会话", session.ID)
}

// UpdateSessionArchive 只更新会话的归档状态与归档位置。
//
// 归档与执行结果、HTML 导出相互独立，因此这里不改写 status、run_status 与
// export_status：归档失败不会把成功的调用改写为失败，反之亦然。
//
// 归档结果一经写入不再改写：WHERE 条件要求当前归档状态为空或未归档，影响行数为 0
// 时返回 storage.ErrConflict，避免迟到的归档任务覆盖已经确认的事实。
func (s sessionStore) UpdateSessionArchive(ctx context.Context, session domain.Session) error {
	if err := session.ArchiveStatus.Validate(); err != nil {
		return err
	}
	if session.ArchiveStatus == domain.SessionArchiveNotAttempted {
		return wrapError(
			"更新会话归档",
			errors.Join(
				storage.ErrInvalidData,
				errors.New("归档结果不能写回未归档"),
			),
		)
	}
	const statement = `
UPDATE sessions SET archive_status = ?, archive_path = ?
WHERE id = ? AND (archive_status IS NULL OR archive_status = ?)`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		string(session.ArchiveStatus),
		nullIfBlank(session.ArchivePath),
		toIDParam(session.ID),
		string(domain.SessionArchiveNotAttempted),
	)
	if err != nil {
		return wrapError("更新会话归档", err)
	}
	return ensureStatusAffected(result, "会话归档", session.ID)
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

// ListSessionsByStatus 返回全部处于指定执行状态的会话，按创建时间升序。
//
// 查询只读，结果按 (created_at, rowid) 稳定排序，使重复恢复得到一致顺序。
func (s sessionStore) ListSessionsByStatus(
	ctx context.Context,
	statuses []domain.SessionStatus,
) ([]domain.Session, error) {
	if len(statuses) == 0 {
		return []domain.Session{}, nil
	}
	placeholders := make([]string, 0, len(statuses))
	args := make([]any, 0, len(statuses))
	for _, status := range statuses {
		if err := status.Validate(); err != nil {
			return nil, err
		}
		placeholders = append(placeholders, "?")
		args = append(args, string(status))
	}
	statement := `SELECT ` + sessionColumns + ` FROM sessions
		WHERE status IN (` + strings.Join(placeholders, ", ") + `)
		ORDER BY created_at, rowid`
	return s.querySessions(ctx, statement, args...)
}

// ListSessionPage 按创建时间稳定排序分页返回会话。
//
// 排序键是 (created_at, rowid)：同一毫秒内创建的多个会话仍有确定顺序，分页不会
// 因行顺序抖动而漏读或重复。
func (s sessionStore) ListSessionPage(
	ctx context.Context,
	filter storage.SessionFilter,
) (storage.SessionPage, error) {
	if err := filter.TaskID.Validate(); err != nil {
		return storage.SessionPage{}, err
	}
	conditions := []string{"task_id = ?"}
	args := []any{toIDParam(filter.TaskID)}
	if filter.StepID != nil {
		conditions = append(conditions, "step_id = ?")
		args = append(args, toIDParam(*filter.StepID))
	}
	if filter.AttemptID != nil {
		conditions = append(conditions, "attempt_id = ?")
		args = append(args, toIDParam(*filter.AttemptID))
	}
	if filter.RoleID != nil {
		conditions = append(conditions, "role_id = ?")
		args = append(args, toIDParam(*filter.RoleID))
	}
	if len(filter.Statuses) > 0 {
		statusPlaceholders := make([]string, 0, len(filter.Statuses))
		for _, status := range filter.Statuses {
			statusPlaceholders = append(statusPlaceholders, "?")
			args = append(args, string(status))
		}
		conditions = append(conditions, "status IN ("+strings.Join(statusPlaceholders, ", ")+")")
	}
	var countBuilder strings.Builder
	countBuilder.WriteString(`SELECT COUNT(*) FROM sessions`)
	appendConditions(&countBuilder, conditions)
	page := storage.SessionPage{Sessions: []domain.Session{}}
	if err := s.q.QueryRowContext(ctx, countBuilder.String(), args...).Scan(&page.Total); err != nil {
		return storage.SessionPage{}, wrapError("统计会话数量", err)
	}
	if page.Total == 0 {
		return page, nil
	}
	var builder strings.Builder
	builder.WriteString(`SELECT `)
	builder.WriteString(sessionColumns)
	builder.WriteString(` FROM sessions`)
	appendConditions(&builder, conditions)
	builder.WriteString(` ORDER BY created_at, rowid`)
	queryArgs := append([]any{}, args...)
	queryArgs = appendPaging(&builder, queryArgs, filter.Limit, filter.Offset)
	sessions, err := s.querySessions(ctx, builder.String(), queryArgs...)
	if err != nil {
		return storage.SessionPage{}, err
	}
	page.Sessions = sessions
	return page, nil
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
	attemptStepID, found, err := s.attemptStepID(ctx, *session.AttemptID)
	if err != nil {
		return err
	}
	if !found {
		// 尝试记录已不存在：数据库不再强制引用完整性，允许保存孤立的历史标识。
		return nil
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

// attemptStepID 返回某次尝试所属的步骤标识；尝试不存在时 found 为 false。
func (s sessionStore) attemptStepID(
	ctx context.Context,
	attemptID domain.ID,
) (string, bool, error) {
	const statement = `SELECT step_id FROM attempts WHERE id = ?`
	var stepID string
	err := s.q.QueryRowContext(ctx, statement, toIDParam(attemptID)).Scan(&stepID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, wrapError("读取尝试归属", err)
	}
	return stepID, true, nil
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
		archiveStatus sql.NullString
		archivePath   sql.NullString
		stdoutPath    sql.NullString
		stderrPath    sql.NullString
		exitCode      sql.NullInt64
		failure       sql.NullString
		invocationID  sql.NullString
		containerID   sql.NullString
		dockerID      sql.NullString
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
		&archiveStatus,
		&archivePath,
		&stdoutPath,
		&stderrPath,
		&exitCode,
		&failure,
		&invocationID,
		&containerID,
		&dockerID,
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
	session.ArchiveStatus = domain.SessionArchiveStatus(archiveStatus.String)
	session.ArchivePath = archivePath.String
	session.StdoutPath = stdoutPath.String
	session.StderrPath = stderrPath.String
	session.ExitCode = fromIntPtr(exitCode)
	session.FailureSummary = failure.String
	session.InvocationID = invocationID.String
	session.ContainerID = containerID.String
	session.DockerID = dockerID.String
	session.StartedAt = fromMillis(startedAt)
	session.FinishedAt = fromMillisPtr(finishedAt)
	session.CreatedAt = fromMillis(createdAtUnix)
	if err := unmarshalJSON("Session.role_snapshot", snapshotJSON, &session.RoleSnapshot); err != nil {
		return nil, err
	}
	return &session, nil
}
