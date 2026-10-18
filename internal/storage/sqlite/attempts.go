package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lingting/agentflow/internal/domain"
)

// attemptColumns 是尝试表的完整列顺序。
const attemptColumns = `id, step_id, attempt_number, status, container_id, docker_id,
	input_digest, exit_code, timed_out, cancelled, retry_reason, error_class,
	failure_stage, failure_summary, verification_verdict, verification_summary,
	verification_id, session_ids_json, code_save_id, started_at, finished_at, created_at`

// attemptInsertCount 是写入尝试时的参数个数。
const attemptInsertCount = 22

// attemptStore 实现 storage.AttemptStore。
type attemptStore struct {
	q queryer
}

// attemptRecord 是写入尝试前的 JSON 编码结果。
type attemptRecord struct {
	attempt     domain.Attempt
	sessionJSON string
}

// CreateAttempt 写入一次执行尝试。
//
// 同一步骤内 attempt_number 重复时返回 storage.ErrConflict，旧记录不会被覆盖。
func (s attemptStore) CreateAttempt(ctx context.Context, attempt domain.Attempt) error {
	record, err := encodeAttempt(attempt)
	if err != nil {
		return err
	}
	statement := `INSERT INTO attempts (` + attemptColumns + `)
		VALUES (` + placeholders(attemptInsertCount) + `)`
	_, err = s.q.ExecContext(ctx, statement, attemptArgs(record)...)
	if err != nil {
		return wrapError("写入执行尝试", err)
	}
	return nil
}

// UpdateAttempt 按主键更新执行尝试。
//
// 结束状态一经写入不会回到 running：状态机拒绝该迁移，数据库约束同样会拒绝。
func (s attemptStore) UpdateAttempt(ctx context.Context, attempt domain.Attempt) error {
	record, err := encodeAttempt(attempt)
	if err != nil {
		return err
	}
	const statement = `
UPDATE attempts SET
	status = ?, container_id = ?, docker_id = ?, input_digest = ?, exit_code = ?,
	timed_out = ?, cancelled = ?, retry_reason = ?, error_class = ?, failure_stage = ?,
	failure_summary = ?, verification_verdict = ?, verification_summary = ?,
	verification_id = ?, session_ids_json = ?, code_save_id = ?, finished_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		string(record.attempt.Status),
		nullIfBlank(record.attempt.ContainerID),
		nullIfBlank(record.attempt.DockerID),
		nullIfBlank(record.attempt.InputDigest),
		record.attempt.ExitCode,
		toBoolInt(record.attempt.TimedOut),
		toBoolInt(record.attempt.Cancelled),
		nullIfBlank(record.attempt.RetryReason),
		nullIfBlank(record.attempt.ErrorClass),
		nullIfBlank(record.attempt.FailureStage),
		nullIfBlank(record.attempt.FailureSummary),
		nullIfBlank(string(record.attempt.VerificationVerdict)),
		nullIfBlank(record.attempt.VerificationSummary),
		toIDPtrParam(record.attempt.VerificationID),
		record.sessionJSON,
		nullIfBlank(record.attempt.CodeSaveID),
		toMillisPtr(record.attempt.FinishedAt),
		toIDParam(record.attempt.ID),
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

// encodeAttempt 校验尝试并编码其 JSON 字段。
func encodeAttempt(attempt domain.Attempt) (attemptRecord, error) {
	if err := attempt.Validate(); err != nil {
		return attemptRecord{}, err
	}
	sessions := attempt.SessionIDs
	if sessions == nil {
		sessions = []domain.ID{}
	}
	encoded, err := marshalSlice("Attempt.session_ids", sessions)
	if err != nil {
		return attemptRecord{}, err
	}
	return attemptRecord{attempt: attempt, sessionJSON: encoded}, nil
}

// attemptArgs 构造写入尝试的参数列表。
func attemptArgs(record attemptRecord) []any {
	attempt := record.attempt
	return []any{
		toIDParam(attempt.ID),
		toIDParam(attempt.StepID),
		attempt.AttemptNumber,
		string(attempt.Status),
		nullIfBlank(attempt.ContainerID),
		nullIfBlank(attempt.DockerID),
		nullIfBlank(attempt.InputDigest),
		attempt.ExitCode,
		toBoolInt(attempt.TimedOut),
		toBoolInt(attempt.Cancelled),
		nullIfBlank(attempt.RetryReason),
		nullIfBlank(attempt.ErrorClass),
		nullIfBlank(attempt.FailureStage),
		nullIfBlank(attempt.FailureSummary),
		nullIfBlank(string(attempt.VerificationVerdict)),
		nullIfBlank(attempt.VerificationSummary),
		toIDPtrParam(attempt.VerificationID),
		record.sessionJSON,
		nullIfBlank(attempt.CodeSaveID),
		toMillis(attempt.StartedAt),
		toMillisPtr(attempt.FinishedAt),
		toMillis(attempt.CreatedAt),
	}
}

// scanAttempt 从一行结果读取执行尝试。
func scanAttempt(row scanner) (*domain.Attempt, error) {
	var (
		attempt             domain.Attempt
		rawID               string
		stepID              string
		status              string
		containerID         sql.NullString
		dockerID            sql.NullString
		inputDigest         sql.NullString
		timedOut            int
		cancelled           int
		retryReason         sql.NullString
		errorClass          sql.NullString
		failureStage        sql.NullString
		failureSummary      sql.NullString
		verdict             sql.NullString
		verificationSummary sql.NullString
		verificationID      sql.NullString
		sessionJSON         string
		codeSaveID          sql.NullString
		startedAt           int64
		finishedAt          sql.NullInt64
		createdAt           int64
	)
	err := row.Scan(
		&rawID,
		&stepID,
		&attempt.AttemptNumber,
		&status,
		&containerID,
		&dockerID,
		&inputDigest,
		&attempt.ExitCode,
		&timedOut,
		&cancelled,
		&retryReason,
		&errorClass,
		&failureStage,
		&failureSummary,
		&verdict,
		&verificationSummary,
		&verificationID,
		&sessionJSON,
		&codeSaveID,
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
	attempt.ContainerID = containerID.String
	attempt.DockerID = dockerID.String
	attempt.InputDigest = inputDigest.String
	attempt.TimedOut = fromBoolInt(timedOut)
	attempt.Cancelled = fromBoolInt(cancelled)
	attempt.RetryReason = retryReason.String
	attempt.ErrorClass = errorClass.String
	attempt.FailureStage = failureStage.String
	attempt.FailureSummary = failureSummary.String
	attempt.VerificationVerdict = domain.VerificationVerdict(verdict.String)
	attempt.VerificationSummary = verificationSummary.String
	attempt.VerificationID = fromIDPtr(verificationID)
	attempt.CodeSaveID = codeSaveID.String
	attempt.StartedAt = fromMillis(startedAt)
	attempt.FinishedAt = fromMillisPtr(finishedAt)
	attempt.CreatedAt = fromMillis(createdAt)
	if err := unmarshalJSON("Attempt.session_ids", sessionJSON, &attempt.SessionIDs); err != nil {
		return nil, err
	}
	return &attempt, nil
}
