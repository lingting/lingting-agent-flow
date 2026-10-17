package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lingting/agentflow/internal/domain"
)

// attemptColumns 是尝试表的完整列顺序。
const attemptColumns = `id, step_id, attempt_number, status, failure_summary,
	started_at, finished_at, created_at`

// attemptStore 实现 storage.AttemptStore。
type attemptStore struct {
	q queryer
}

// CreateAttempt 写入一次执行尝试。
//
// 同一步骤内 attempt_number 重复时返回 storage.ErrConflict，旧记录不会被覆盖。
func (s attemptStore) CreateAttempt(ctx context.Context, attempt domain.Attempt) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	const statement = `INSERT INTO attempts (` + attemptColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(attempt.ID),
		toIDParam(attempt.StepID),
		attempt.AttemptNumber,
		string(attempt.Status),
		sqlNullString(attempt.FailureSummary),
		toMillis(attempt.StartedAt),
		toMillisPtr(attempt.FinishedAt),
		toMillis(attempt.CreatedAt),
	)
	if err != nil {
		return wrapError("写入执行尝试", err)
	}
	return nil
}

// UpdateAttempt 按主键更新执行尝试。
func (s attemptStore) UpdateAttempt(ctx context.Context, attempt domain.Attempt) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE attempts SET
	status = ?, failure_summary = ?, finished_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		string(attempt.Status),
		sqlNullString(attempt.FailureSummary),
		toMillisPtr(attempt.FinishedAt),
		toIDParam(attempt.ID),
	)
	if err != nil {
		return wrapError("更新执行尝试", err)
	}
	return ensureAffected(result, "执行尝试", attempt.ID)
}

// GetAttempt 读取单次执行尝试。
func (s attemptStore) GetAttempt(ctx context.Context, id domain.ID) (*domain.Attempt, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + attemptColumns + ` FROM attempts WHERE id = ?`
	attempt, err := scanAttempt(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("执行尝试", id.String())
		}
		return nil, wrapError("读取执行尝试", err)
	}
	return attempt, nil
}

// ListAttemptsByStep 按尝试序号返回某步骤的全部尝试历史。
func (s attemptStore) ListAttemptsByStep(
	ctx context.Context,
	stepID domain.ID,
) ([]domain.Attempt, error) {
	if err := stepID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + attemptColumns + ` FROM attempts
		WHERE step_id = ? ORDER BY attempt_number`
	rows, err := s.q.QueryContext(ctx, statement, toIDParam(stepID))
	if err != nil {
		return nil, wrapError("查询执行尝试", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	attempts := make([]domain.Attempt, 0, 4)
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, wrapError("读取执行尝试", err)
		}
		attempts = append(attempts, *attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询执行尝试", err)
	}
	return attempts, nil
}

// NextAttemptNumber 返回该步骤下一个可用的尝试序号。
func (s attemptStore) NextAttemptNumber(ctx context.Context, stepID domain.ID) (int, error) {
	if err := stepID.Validate(); err != nil {
		return 0, err
	}
	const statement = `SELECT COALESCE(MAX(attempt_number), 0) FROM attempts WHERE step_id = ?`
	var latest int
	if err := s.q.QueryRowContext(ctx, statement, toIDParam(stepID)).Scan(&latest); err != nil {
		return 0, wrapError("查询尝试序号", err)
	}
	return latest + 1, nil
}

// scanAttempt 从一行结果读取执行尝试。
func scanAttempt(row scanner) (*domain.Attempt, error) {
	var (
		attempt        domain.Attempt
		rawID          string
		stepID         string
		status         string
		failureSummary sql.NullString
		startedAt      int64
		finishedAt     sql.NullInt64
		createdAt      int64
	)
	err := row.Scan(
		&rawID,
		&stepID,
		&attempt.AttemptNumber,
		&status,
		&failureSummary,
		&startedAt,
		&finishedAt,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}
	attempt.ID = domain.ID(rawID)
	attempt.StepID = domain.ID(stepID)
	attempt.Status = domain.AttemptStatus(status)
	attempt.FailureSummary = failureSummary.String
	attempt.StartedAt = fromMillis(startedAt)
	attempt.FinishedAt = fromMillisPtr(finishedAt)
	attempt.CreatedAt = fromMillis(createdAt)
	return &attempt, nil
}
