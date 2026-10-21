package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// attemptColumns 是尝试表的完整列顺序。
const attemptColumns = `id, task_id, step_id, attempt_number, status, container_id,
	docker_id, input_digest, exit_code, timed_out, cancelled, retry_reason, error_class,
	failure_stage, failure_summary, verification_verdict, verification_summary,
	verification_id, session_ids_json, code_save_id, git_branch, git_before_json,
	git_after_json, started_at, finished_at, created_at`

// attemptInsertCount 是写入尝试时的参数个数。
const attemptInsertCount = 26

// attemptStore 实现 storage.AttemptStore。
type attemptStore struct {
	q queryer
}

// attemptRecord 是写入尝试前的 JSON 编码结果。
type attemptRecord struct {
	attempt domain.Attempt
	// sessionJSON 是会话标识列表的 JSON 编码。
	sessionJSON string
	// gitBeforeJSON 与 gitAfterJSON 是 Git 工作区状态快照的 JSON 编码。
	//
	// 空字符串表示本次没有采集，写入 NULL：数据库里"没有快照"与"快照显示工作区
	// 干净"必须能区分。
	gitBeforeJSON string
	gitAfterJSON  string
}

// CreateAttempt 写入一次执行尝试。
//
// 同一步骤内 attempt_number 重复时返回 storage.ErrConflict，旧记录不会被覆盖。
// 写入前校验任务与步骤归属一致：数据库不声明外键，关系合法性由这里与业务服务共同保证。
func (s attemptStore) CreateAttempt(ctx context.Context, attempt domain.Attempt) error {
	if err := s.ensureAttemptScope(ctx, attempt); err != nil {
		return err
	}
	record, err := encodeAttempt(attempt)
	if err != nil {
		return err
	}
	return s.insertAttempt(ctx, record)
}

// StartAttempt 在一个事务内分配尝试序号、创建 Attempt 并写入。
//
// 序号分配与插入共享同一个事务：事务以 immediate 语义开启（DSN 的 _txlock），
// 因此并发调用会被串行化，不会出现两个调用读到同一个"最大序号"。唯一约束仍然保留，
// 作为写入路径被绕过时的最后防线。
func (s attemptStore) StartAttempt(
	ctx context.Context,
	request storage.StartAttemptRequest,
) (domain.Attempt, error) {
	if err := request.TaskID.Validate(); err != nil {
		return domain.Attempt{}, err
	}
	if err := request.StepID.Validate(); err != nil {
		return domain.Attempt{}, err
	}
	withTx, ok := s.q.(transactioner)
	if !ok {
		// 已在事务内：直接复用当前事务，调用方负责事务边界。
		return s.assignAndInsert(ctx, request)
	}
	var started domain.Attempt
	err := withTx.runInTx(ctx, func(tx queryer) error {
		inner := attemptStore{q: tx}
		attempt, err := inner.assignAndInsert(ctx, request)
		if err != nil {
			return err
		}
		started = attempt
		return nil
	})
	if err != nil {
		return domain.Attempt{}, err
	}
	return started, nil
}

// assignAndInsert 分配序号、构造尝试并写入。
//
// 序号在插入之前读取，两者处于同一事务内，因此不会与并发的分配交叉。
func (s attemptStore) assignAndInsert(
	ctx context.Context,
	request storage.StartAttemptRequest,
) (domain.Attempt, error) {
	number, err := s.NextAttemptNumber(ctx, request.StepID)
	if err != nil {
		return domain.Attempt{}, err
	}
	attempt, err := domain.NewAttempt(
		request.TaskID,
		request.StepID,
		number,
		attemptStartTime(request.At),
	)
	if err != nil {
		return domain.Attempt{}, err
	}
	if err := s.ensureAttemptScope(ctx, attempt); err != nil {
		return domain.Attempt{}, err
	}
	record, err := encodeAttempt(attempt)
	if err != nil {
		return domain.Attempt{}, err
	}
	if err := s.insertAttempt(ctx, record); err != nil {
		return domain.Attempt{}, err
	}
	return record.attempt, nil
}

// attemptStartTime 返回尝试的开始时间，零值使用当前时间。
func attemptStartTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at
}

// insertAttempt 写入编码后的尝试记录。
func (s attemptStore) insertAttempt(ctx context.Context, record attemptRecord) error {
	statement := `INSERT INTO attempts (` + attemptColumns + `)
		VALUES (` + placeholders(attemptInsertCount) + `)`
	if _, err := s.q.ExecContext(ctx, statement, attemptArgs(record)...); err != nil {
		return wrapError("写入执行尝试", err)
	}
	return nil
}

// ensureAttemptScope 校验尝试的任务归属与步骤归属一致。
//
// 步骤记录已不存在时放行：数据库不再强制引用完整性，允许保留指向已删除步骤的
// 历史标识；但只要步骤存在，任务归属就必须一致。
func (s attemptStore) ensureAttemptScope(ctx context.Context, attempt domain.Attempt) error {
	const statement = `SELECT task_id FROM steps WHERE id = ?`
	var taskID string
	err := s.q.QueryRowContext(ctx, statement, toIDParam(attempt.StepID)).Scan(&taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return wrapError("读取步骤归属", err)
	}
	if taskID != attempt.TaskID.String() {
		return fmt.Errorf(
			"sqlite: 尝试 %s 的任务 %s 与步骤所属任务 %s 不一致: %w",
			attempt.ID,
			attempt.TaskID,
			taskID,
			storage.ErrInvalidData,
		)
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
	return s.updateAttempt(ctx, record, nil)
}

// UpdateAttemptIfStatus 在数据库中的状态仍属于 expected 时写入尝试。
func (s attemptStore) UpdateAttemptIfStatus(
	ctx context.Context,
	attempt domain.Attempt,
	expected []domain.AttemptStatus,
) error {
	if len(expected) == 0 {
		return wrapError(
			"条件更新执行尝试",
			errors.Join(storage.ErrInvalidData, errors.New("期望状态集合不能为空")),
		)
	}
	record, err := encodeAttempt(attempt)
	if err != nil {
		return err
	}
	return s.updateAttempt(ctx, record, expected)
}

// updateAttempt 执行尝试更新；expected 为空表示无条件更新，否则附加状态前置条件。
func (s attemptStore) updateAttempt(
	ctx context.Context,
	record attemptRecord,
	expected []domain.AttemptStatus,
) error {
	statement := `
UPDATE attempts SET
	status = ?, container_id = ?, docker_id = ?, input_digest = ?, exit_code = ?,
	timed_out = ?, cancelled = ?, retry_reason = ?, error_class = ?, failure_stage = ?,
	failure_summary = ?, verification_verdict = ?, verification_summary = ?,
	verification_id = ?, session_ids_json = ?, code_save_id = ?, git_branch = ?,
	git_before_json = ?, git_after_json = ?, finished_at = ?
WHERE id = ?`
	args := []any{
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
		nullIfBlank(record.attempt.GitBranch),
		nullIfBlank(record.gitBeforeJSON),
		nullIfBlank(record.gitAfterJSON),
		toMillisPtr(record.attempt.FinishedAt),
		toIDParam(record.attempt.ID),
	}
	if len(expected) > 0 {
		conditions := make([]string, 0, len(expected))
		for _, status := range expected {
			conditions = append(conditions, "?")
			args = append(args, string(status))
		}
		statement += ` AND status IN (` + strings.Join(conditions, ", ") + `)`
	}
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return wrapError("更新执行尝试", err)
	}
	if len(expected) == 0 {
		return ensureAffected(result, "执行尝试", record.attempt.ID)
	}
	return ensureStatusAffected(result, "执行尝试", record.attempt.ID)
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

// ListAttemptPage 按尝试序号稳定排序分页返回步骤的尝试。
//
// 排序键是 (attempt_number, id)：attempt_number 在同一步骤内唯一，附加 id 使排序
// 在数据异常时仍然确定，分页不会因行顺序抖动而漏读或重复。
func (s attemptStore) ListAttemptPage(
	ctx context.Context,
	filter storage.AttemptFilter,
) (storage.AttemptPage, error) {
	if err := filter.StepID.Validate(); err != nil {
		return storage.AttemptPage{}, err
	}
	conditions := []string{"step_id = ?"}
	args := []any{toIDParam(filter.StepID)}
	if len(filter.Statuses) > 0 {
		statusPlaceholders := make([]string, 0, len(filter.Statuses))
		for _, status := range filter.Statuses {
			statusPlaceholders = append(statusPlaceholders, "?")
			args = append(args, string(status))
		}
		conditions = append(conditions, "status IN ("+strings.Join(statusPlaceholders, ", ")+")")
	}
	var countBuilder strings.Builder
	countBuilder.WriteString(`SELECT COUNT(*) FROM attempts`)
	appendConditions(&countBuilder, conditions)
	page := storage.AttemptPage{Attempts: []domain.Attempt{}}
	if err := s.q.QueryRowContext(ctx, countBuilder.String(), args...).Scan(&page.Total); err != nil {
		return storage.AttemptPage{}, wrapError("统计执行尝试数量", err)
	}
	if page.Total == 0 {
		return page, nil
	}
	var builder strings.Builder
	builder.WriteString(`SELECT `)
	builder.WriteString(attemptColumns)
	builder.WriteString(` FROM attempts`)
	appendConditions(&builder, conditions)
	builder.WriteString(` ORDER BY attempt_number, id`)
	queryArgs := append([]any{}, args...)
	queryArgs = appendPaging(&builder, queryArgs, filter.Limit, filter.Offset)
	rows, err := s.q.QueryContext(ctx, builder.String(), queryArgs...)
	if err != nil {
		return storage.AttemptPage{}, wrapError("查询执行尝试", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return storage.AttemptPage{}, wrapError("读取执行尝试", err)
		}
		page.Attempts = append(page.Attempts, *attempt)
	}
	if err := rows.Err(); err != nil {
		return storage.AttemptPage{}, wrapError("查询执行尝试", err)
	}
	return page, nil
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
	before, err := encodeGitState("Attempt.git_before", attempt.GitBefore)
	if err != nil {
		return attemptRecord{}, err
	}
	after, err := encodeGitState("Attempt.git_after", attempt.GitAfter)
	if err != nil {
		return attemptRecord{}, err
	}
	return attemptRecord{
		attempt:       attempt,
		sessionJSON:   encoded,
		gitBeforeJSON: before,
		gitAfterJSON:  after,
	}, nil
}

// encodeGitState 把 Git 工作区状态编码为 JSON；nil 表示本次没有采集，返回空字符串。
func encodeGitState(field string, state *domain.GitWorktreeState) (string, error) {
	if state == nil {
		return "", nil
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("sqlite: 编码 %s 失败: %w", field, err)
	}
	return string(encoded), nil
}

// decodeGitState 把 JSON 快照还原为 Git 工作区状态；空值表示本次没有采集。
func decodeGitState(field, raw string) (*domain.GitWorktreeState, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var state domain.GitWorktreeState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("sqlite: 解析 %s 失败: %w", field, err)
	}
	return &state, nil
}

// attemptArgs 构造写入尝试的参数列表。
func attemptArgs(record attemptRecord) []any {
	attempt := record.attempt
	return []any{
		toIDParam(attempt.ID),
		toIDParam(attempt.TaskID),
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
		nullIfBlank(attempt.GitBranch),
		nullIfBlank(record.gitBeforeJSON),
		nullIfBlank(record.gitAfterJSON),
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
		taskID              sql.NullString
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
		gitBranch           sql.NullString
		gitBeforeJSON       sql.NullString
		gitAfterJSON        sql.NullString
		startedAt           int64
		finishedAt          sql.NullInt64
		createdAt           int64
	)
	err := row.Scan(
		&rawID,
		&taskID,
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
		&gitBranch,
		&gitBeforeJSON,
		&gitAfterJSON,
		&startedAt,
		&finishedAt,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}
	attempt.ID = domain.ID(rawID)
	attempt.TaskID = domain.ID(taskID.String)
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
	attempt.GitBranch = gitBranch.String
	before, err := decodeGitState("Attempt.git_before", gitBeforeJSON.String)
	if err != nil {
		return nil, err
	}
	attempt.GitBefore = before
	after, err := decodeGitState("Attempt.git_after", gitAfterJSON.String)
	if err != nil {
		return nil, err
	}
	attempt.GitAfter = after
	attempt.StartedAt = fromMillis(startedAt)
	attempt.FinishedAt = fromMillisPtr(finishedAt)
	attempt.CreatedAt = fromMillis(createdAt)
	if err := unmarshalJSON("Attempt.session_ids", sessionJSON, &attempt.SessionIDs); err != nil {
		return nil, err
	}
	return &attempt, nil
}
