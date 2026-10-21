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

// validationRunColumns 是验证记录表的完整列顺序。
const validationRunColumns = `id, task_id, step_id, attempt_id, status, verdict, summary,
	failure_summary, spec_json, spec_snapshot, total_checks, passed_checks, failed_checks,
	skipped_checks, execution_env, workspace, container_id, validator_session_id,
	started_at, finished_at, duration_ms, error_detail, created_at, updated_at`

// validationRunInsertCount 是写入验证记录时的参数个数。
const validationRunInsertCount = 24

// validationCheckResultColumns 是检查结果表的完整列顺序。
const validationCheckResultColumns = `id, validation_run_id, check_id, check_name, required,
	sequence, definition_json, status, exit_code, stdout_path, stderr_path, stdout_summary,
	stderr_summary, output_truncated, output_archive_error, timed_out, cancelled, certain,
	failure_reason, error_detail, duration_ms, started_at, finished_at, created_at`

// validationCheckResultInsertCount 是写入检查结果时的参数个数。
const validationCheckResultInsertCount = 24

// validationStore 实现 storage.ValidationStore。
type validationStore struct {
	q queryer
}

// BeginValidationRun 在一个事务内校验归属并创建验证记录。
//
// 事务内的顺序固定为：读取任务、步骤与尝试 → 校验归属与状态前置条件 → 检查该尝试
// 是否已有运行中的验证 → 写入验证记录。任一步不成立时整体回滚，因此并发请求最多
// 只有一个能成功创建 running 验证，进程内互斥锁不承担一致性职责。
func (s validationStore) BeginValidationRun(
	ctx context.Context,
	request storage.ValidationRunStartRequest,
) (domain.ValidationRun, error) {
	if err := request.TaskID.Validate(); err != nil {
		return domain.ValidationRun{}, err
	}
	if err := request.StepID.Validate(); err != nil {
		return domain.ValidationRun{}, err
	}
	if err := request.AttemptID.Validate(); err != nil {
		return domain.ValidationRun{}, err
	}
	if err := request.ExpectedTaskStatus.Validate(); err != nil {
		return domain.ValidationRun{}, err
	}
	at := request.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	withTx, ok := s.q.(transactioner)
	if !ok {
		// 已在事务内：复用当前事务，原子性由调用方的事务边界保证。
		return s.begin(ctx, request, at)
	}
	var run domain.ValidationRun
	err := withTx.runInTx(ctx, func(tx queryer) error {
		inner := validationStore{q: tx}
		created, err := inner.begin(ctx, request, at)
		if err != nil {
			return err
		}
		run = created
		return nil
	})
	if err != nil {
		return domain.ValidationRun{}, err
	}
	return run, nil
}

// begin 在已开启的事务内创建验证记录。
func (s validationStore) begin(
	ctx context.Context,
	request storage.ValidationRunStartRequest,
	at time.Time,
) (domain.ValidationRun, error) {
	task, err := (taskStore{q: s.q}).GetTask(ctx, request.TaskID)
	if err != nil {
		return domain.ValidationRun{}, err
	}
	if task.Deleted() {
		return domain.ValidationRun{}, notFoundError("任务", request.TaskID.String())
	}
	if task.Status != request.ExpectedTaskStatus {
		return domain.ValidationRun{}, fmt.Errorf(
			"sqlite: 任务 %s 的状态已从 %s 变为 %s，拒绝开始验证: %w",
			task.ID,
			request.ExpectedTaskStatus,
			task.Status,
			storage.ErrConflict,
		)
	}
	step, err := (stepStore{q: s.q}).GetStep(ctx, request.StepID)
	if err != nil {
		return domain.ValidationRun{}, err
	}
	if step.TaskID != task.ID {
		return domain.ValidationRun{}, wrapError(
			"开始验证",
			errors.Join(
				storage.ErrInvalidData,
				fmt.Errorf("步骤 %s 不属于任务 %s", step.ID, task.ID),
			),
		)
	}
	attempt, err := (attemptStore{q: s.q}).GetAttempt(ctx, request.AttemptID)
	if err != nil {
		return domain.ValidationRun{}, err
	}
	if attempt.StepID != step.ID {
		return domain.ValidationRun{}, wrapError(
			"开始验证",
			errors.Join(
				storage.ErrInvalidData,
				fmt.Errorf("尝试 %s 不属于步骤 %s", attempt.ID, step.ID),
			),
		)
	}
	if !attempt.TaskID.IsEmpty() && attempt.TaskID != task.ID {
		return domain.ValidationRun{}, wrapError(
			"开始验证",
			errors.Join(
				storage.ErrInvalidData,
				fmt.Errorf("尝试 %s 不属于任务 %s", attempt.ID, task.ID),
			),
		)
	}
	// 同一尝试同一时刻只允许一个有效验证：并发与重复请求都必须返回冲突，
	// 不能各自启动一组检查。
	const activeStatement = `SELECT id FROM validation_runs
		WHERE attempt_id = ? AND status IN ('pending', 'running') LIMIT 1`
	var activeID string
	err = s.q.QueryRowContext(ctx, activeStatement, toIDParam(attempt.ID)).Scan(&activeID)
	if err == nil {
		return domain.ValidationRun{}, fmt.Errorf(
			"sqlite: 尝试 %s 已有进行中的验证 %s，拒绝重复开始: %w",
			attempt.ID,
			activeID,
			storage.ErrConflict,
		)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.ValidationRun{}, wrapError("查询进行中的验证", err)
	}
	run, err := domain.NewValidationRun(domain.ValidationRunInput{
		TaskID:       task.ID,
		StepID:       step.ID,
		AttemptID:    attempt.ID,
		Spec:         request.Spec,
		SpecSnapshot: request.SpecSnapshot,
		ExecutionEnv: request.ExecutionEnv,
		Workspace:    request.Workspace,
		ContainerID:  request.ContainerID,
	}, at)
	if err != nil {
		return domain.ValidationRun{}, wrapError("构造验证记录", err)
	}
	// 创建即进入 running：验证记录只在真正开始执行时写入，不存在长期占位的
	// pending 记录。
	if err := run.Transition(domain.ValidationRunStatusRunning, at); err != nil {
		return domain.ValidationRun{}, wrapError("推进验证状态", err)
	}
	record, err := encodeValidationRun(run)
	if err != nil {
		return domain.ValidationRun{}, err
	}
	if err := s.insertRun(ctx, record); err != nil {
		return domain.ValidationRun{}, err
	}
	return run, nil
}

// insertRun 写入一条验证记录。
func (s validationStore) insertRun(ctx context.Context, record validationRunRecord) error {
	statement := `INSERT INTO validation_runs (` + validationRunColumns + `)
		VALUES (` + placeholders(validationRunInsertCount) + `)`
	_, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(record.run.ID),
		toIDParam(record.run.TaskID),
		toIDParam(record.run.StepID),
		toIDParam(record.run.AttemptID),
		string(record.run.Status),
		sqlNullVerdict(record.run.Verdict),
		sqlNullString(record.run.Summary),
		sqlNullString(record.run.FailureSummary),
		record.specJSON,
		sqlNullString(record.run.SpecSnapshot),
		record.run.TotalChecks,
		record.run.PassedChecks,
		record.run.FailedChecks,
		record.run.SkippedChecks,
		sqlNullString(record.run.ExecutionEnv),
		sqlNullString(record.run.Workspace),
		sqlNullString(record.run.ContainerID),
		toIDPtrParam(record.run.ValidatorSessionID),
		toMillis(record.run.StartedAt),
		toMillisPtr(record.run.FinishedAt),
		record.run.Duration.Milliseconds(),
		sqlNullString(record.run.ErrorDetail),
		toMillis(record.run.CreatedAt),
		toMillis(record.run.UpdatedAt),
	)
	if err != nil {
		return wrapError("写入验证记录", err)
	}
	return nil
}

// GetValidationRun 读取单条验证记录。
func (s validationStore) GetValidationRun(
	ctx context.Context,
	id domain.ID,
) (*domain.ValidationRun, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + validationRunColumns + ` FROM validation_runs WHERE id = ?`
	run, err := scanValidationRun(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("验证记录", id.String())
		}
		return nil, wrapError("读取验证记录", err)
	}
	return run, nil
}

// ListValidationRunsByAttempt 按创建时间返回某次尝试的全部验证记录。
func (s validationStore) ListValidationRunsByAttempt(
	ctx context.Context,
	attemptID domain.ID,
) ([]domain.ValidationRun, error) {
	if err := attemptID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + validationRunColumns + ` FROM validation_runs
		WHERE attempt_id = ? ORDER BY created_at, rowid`
	rows, err := s.q.QueryContext(ctx, statement, toIDParam(attemptID))
	if err != nil {
		return nil, wrapError("查询验证记录", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	runs := make([]domain.ValidationRun, 0, 4)
	for rows.Next() {
		run, err := scanValidationRun(rows)
		if err != nil {
			return nil, wrapError("读取验证记录", err)
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询验证记录", err)
	}
	return runs, nil
}

// ListValidationRunsByStep 按创建时间返回某个步骤的全部验证记录。
//
// 步骤级查询用于"最新验证结果"：同一尝试下验证只追加，重试会创建新的尝试与新的
// 验证记录，历史记录仍然保留，调用方按创建时间取最后一条即为最新结果。
func (s validationStore) ListValidationRunsByStep(
	ctx context.Context,
	stepID domain.ID,
) ([]domain.ValidationRun, error) {
	if err := stepID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + validationRunColumns + ` FROM validation_runs
		WHERE step_id = ? ORDER BY created_at, rowid`
	rows, err := s.q.QueryContext(ctx, statement, toIDParam(stepID))
	if err != nil {
		return nil, wrapError("查询验证记录", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	runs := make([]domain.ValidationRun, 0, 4)
	for rows.Next() {
		run, err := scanValidationRun(rows)
		if err != nil {
			return nil, wrapError("读取验证记录", err)
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询验证记录", err)
	}
	return runs, nil
}

// FinishValidationRun 以"仍在运行"为条件写入结论与终态。
//
// 条件更新保证迟到的结论不会覆盖已确认的终态：影响行数为 0 时返回 ErrConflict，
// 调用方必须读取数据库真实值，而不是假定写入成功。
func (s validationStore) FinishValidationRun(
	ctx context.Context,
	request storage.ValidationRunFinishRequest,
) error {
	if err := request.RunID.Validate(); err != nil {
		return err
	}
	if err := request.Status.Validate(); err != nil {
		return err
	}
	if err := request.Verdict.Validate(); err != nil {
		return err
	}
	if request.Verdict == domain.VerificationVerdictNone {
		return wrapError(
			"写入验证结论",
			errors.Join(storage.ErrInvalidData, errors.New("验证结论不能为空")),
		)
	}
	if request.Verdict != domain.VerificationVerdictPassed && strings.TrimSpace(request.FailureSummary) == "" {
		return wrapError(
			"写入验证结论",
			errors.Join(storage.ErrInvalidData, errors.New("未通过的验证必须给出原因")),
		)
	}
	at := request.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	const statement = `UPDATE validation_runs
		SET status = ?, verdict = ?, summary = ?, failure_summary = ?, error_detail = ?,
			passed_checks = ?, failed_checks = ?, skipped_checks = ?, duration_ms = ?,
			finished_at = ?, updated_at = ?
		WHERE id = ? AND status = 'running'`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		string(request.Status),
		string(request.Verdict),
		sqlNullString(request.Summary),
		sqlNullString(request.FailureSummary),
		sqlNullString(request.ErrorDetail),
		request.PassedChecks,
		request.FailedChecks,
		request.SkippedChecks,
		request.Duration.Milliseconds(),
		toMillis(at),
		toMillis(at),
		toIDParam(request.RunID),
	)
	if err != nil {
		return wrapError("写入验证结论", err)
	}
	return ensureStatusAffected(result, "验证记录", request.RunID)
}

// FailValidationRun 把验证记录收敛为失败终态并记录原因。
func (s validationStore) FailValidationRun(
	ctx context.Context,
	id domain.ID,
	status domain.ValidationRunStatus,
	reason string,
	at time.Time,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := status.Validate(); err != nil {
		return err
	}
	if !status.IsTerminal() {
		return wrapError(
			"写入验证终态",
			errors.Join(storage.ErrInvalidData, errors.New("验证终态必须是已结束状态")),
		)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	const statement = `UPDATE validation_runs
		SET status = ?, verdict = 'failed', failure_summary = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('pending', 'running')`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		string(status),
		sqlNullString(strings.TrimSpace(reason)),
		toMillis(at),
		toMillis(at),
		toIDParam(id),
	)
	if err != nil {
		return wrapError("写入验证终态", err)
	}
	return ensureStatusAffected(result, "验证记录", id)
}

// AppendValidationCheckResult 追加一条检查结果。
func (s validationStore) AppendValidationCheckResult(
	ctx context.Context,
	result domain.ValidationCheckResult,
) error {
	record, err := encodeValidationCheckResult(result)
	if err != nil {
		return err
	}
	statement := `INSERT INTO validation_check_results (` + validationCheckResultColumns + `)
		VALUES (` + placeholders(validationCheckResultInsertCount) + `)`
	_, err = s.q.ExecContext(
		ctx,
		statement,
		toIDParam(record.result.ID),
		toIDParam(record.result.ValidationRunID),
		record.result.CheckID,
		record.result.CheckName,
		toBoolInt(record.result.Required),
		record.result.Sequence,
		record.definitionJSON,
		string(record.result.Status),
		record.result.ExitCode,
		sqlNullString(record.result.StdoutPath),
		sqlNullString(record.result.StderrPath),
		sqlNullString(record.result.StdoutSummary),
		sqlNullString(record.result.StderrSummary),
		toBoolInt(record.result.OutputTruncated),
		sqlNullString(record.result.OutputArchiveError),
		toBoolInt(record.result.TimedOut),
		toBoolInt(record.result.Cancelled),
		toBoolInt(record.result.Certain),
		sqlNullString(record.result.FailureReason),
		sqlNullString(record.result.ErrorDetail),
		record.result.Duration.Milliseconds(),
		toMillis(record.result.StartedAt),
		toMillis(record.result.FinishedAt),
		toMillis(record.result.CreatedAt),
	)
	if err != nil {
		return wrapError("写入检查结果", err)
	}
	return nil
}

// ListValidationCheckResults 按执行顺序返回某个验证记录的全部检查结果。
func (s validationStore) ListValidationCheckResults(
	ctx context.Context,
	runID domain.ID,
) ([]domain.ValidationCheckResult, error) {
	if err := runID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + validationCheckResultColumns + `
		FROM validation_check_results WHERE validation_run_id = ?
		ORDER BY sequence, rowid`
	rows, err := s.q.QueryContext(ctx, statement, toIDParam(runID))
	if err != nil {
		return nil, wrapError("查询检查结果", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	results := make([]domain.ValidationCheckResult, 0, 8)
	for rows.Next() {
		result, err := scanValidationCheckResult(rows)
		if err != nil {
			return nil, wrapError("读取检查结果", err)
		}
		results = append(results, *result)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询检查结果", err)
	}
	return results, nil
}

// validationRunRecord 是写入验证记录前的 JSON 编码结果。
type validationRunRecord struct {
	run      domain.ValidationRun
	specJSON string
}

// validationCheckResultRecord 是写入检查结果前的 JSON 编码结果。
type validationCheckResultRecord struct {
	result         domain.ValidationCheckResult
	definitionJSON string
}

// encodeValidationRun 校验验证记录并编码其 JSON 字段。
func encodeValidationRun(run domain.ValidationRun) (validationRunRecord, error) {
	if err := run.Validate(); err != nil {
		return validationRunRecord{}, wrapError("校验验证记录", err)
	}
	spec, err := marshalJSON("ValidationRun.spec", run.Spec)
	if err != nil {
		return validationRunRecord{}, err
	}
	return validationRunRecord{run: run, specJSON: spec}, nil
}

// encodeValidationCheckResult 校验检查结果并编码其 JSON 字段。
func encodeValidationCheckResult(
	result domain.ValidationCheckResult,
) (validationCheckResultRecord, error) {
	if err := result.Validate(); err != nil {
		return validationCheckResultRecord{}, wrapError("校验检查结果", err)
	}
	definition, err := marshalJSON("ValidationCheckResult.definition", result.Definition)
	if err != nil {
		return validationCheckResultRecord{}, err
	}
	return validationCheckResultRecord{result: result, definitionJSON: definition}, nil
}

// scanValidationRun 从一行结果读取验证记录。
func scanValidationRun(row scanner) (*domain.ValidationRun, error) {
	var (
		run                domain.ValidationRun
		rawID              string
		taskID             string
		stepID             string
		attemptID          string
		status             string
		verdict            sql.NullString
		summary            sql.NullString
		failureSummary     sql.NullString
		specJSON           string
		specSnapshot       sql.NullString
		executionEnv       sql.NullString
		workspace          sql.NullString
		containerID        sql.NullString
		validatorSessionID sql.NullString
		startedAt          int64
		finishedAt         sql.NullInt64
		durationMillis     int64
		errorDetail        sql.NullString
		createdAt          int64
		updatedAt          int64
	)
	err := row.Scan(
		&rawID,
		&taskID,
		&stepID,
		&attemptID,
		&status,
		&verdict,
		&summary,
		&failureSummary,
		&specJSON,
		&specSnapshot,
		&run.TotalChecks,
		&run.PassedChecks,
		&run.FailedChecks,
		&run.SkippedChecks,
		&executionEnv,
		&workspace,
		&containerID,
		&validatorSessionID,
		&startedAt,
		&finishedAt,
		&durationMillis,
		&errorDetail,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	run.ID = domain.ID(rawID)
	run.TaskID = domain.ID(taskID)
	run.StepID = domain.ID(stepID)
	run.AttemptID = domain.ID(attemptID)
	run.Status = domain.ValidationRunStatus(status)
	run.Verdict = domain.VerificationVerdict(verdict.String)
	run.Summary = summary.String
	run.FailureSummary = failureSummary.String
	run.SpecSnapshot = specSnapshot.String
	run.ExecutionEnv = executionEnv.String
	run.Workspace = workspace.String
	run.ContainerID = containerID.String
	run.ValidatorSessionID = fromIDPtr(validatorSessionID)
	run.StartedAt = fromMillis(startedAt)
	run.FinishedAt = fromMillisPtr(finishedAt)
	run.Duration = time.Duration(durationMillis) * time.Millisecond
	run.ErrorDetail = errorDetail.String
	run.CreatedAt = fromMillis(createdAt)
	run.UpdatedAt = fromMillis(updatedAt)
	if err := unmarshalJSON("ValidationRun.spec", specJSON, &run.Spec); err != nil {
		return nil, err
	}
	return &run, nil
}

// scanValidationCheckResult 从一行结果读取检查结果。
func scanValidationCheckResult(row scanner) (*domain.ValidationCheckResult, error) {
	var (
		result             domain.ValidationCheckResult
		rawID              string
		runID              string
		status             string
		definitionJSON     string
		stdoutPath         sql.NullString
		stderrPath         sql.NullString
		stdoutSummary      sql.NullString
		stderrSummary      sql.NullString
		outputArchiveError sql.NullString
		failureReason      sql.NullString
		errorDetail        sql.NullString
		required           int
		outputTruncated    int
		timedOut           int
		cancelled          int
		certain            int
		durationMillis     int64
		startedAt          int64
		finishedAt         int64
		createdAt          int64
	)
	err := row.Scan(
		&rawID,
		&runID,
		&result.CheckID,
		&result.CheckName,
		&required,
		&result.Sequence,
		&definitionJSON,
		&status,
		&result.ExitCode,
		&stdoutPath,
		&stderrPath,
		&stdoutSummary,
		&stderrSummary,
		&outputTruncated,
		&outputArchiveError,
		&timedOut,
		&cancelled,
		&certain,
		&failureReason,
		&errorDetail,
		&durationMillis,
		&startedAt,
		&finishedAt,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}
	result.ID = domain.ID(rawID)
	result.ValidationRunID = domain.ID(runID)
	result.Status = domain.ValidationCheckStatus(status)
	result.Required = fromBoolInt(required)
	result.StdoutPath = stdoutPath.String
	result.StderrPath = stderrPath.String
	result.StdoutSummary = stdoutSummary.String
	result.StderrSummary = stderrSummary.String
	result.OutputTruncated = fromBoolInt(outputTruncated)
	result.OutputArchiveError = outputArchiveError.String
	result.TimedOut = fromBoolInt(timedOut)
	result.Cancelled = fromBoolInt(cancelled)
	result.Certain = fromBoolInt(certain)
	result.FailureReason = failureReason.String
	result.ErrorDetail = errorDetail.String
	result.Duration = time.Duration(durationMillis) * time.Millisecond
	result.StartedAt = fromMillis(startedAt)
	result.FinishedAt = fromMillis(finishedAt)
	result.CreatedAt = fromMillis(createdAt)
	if err := unmarshalJSON(
		"ValidationCheckResult.definition",
		definitionJSON,
		&result.Definition,
	); err != nil {
		return nil, err
	}
	return &result, nil
}

// sqlNullVerdict 把空结论转换为 NULL。
//
// 未完成的验证没有结论，必须保存为 NULL 而不是空字符串：两者在审计时的含义不同。
func sqlNullVerdict(verdict domain.VerificationVerdict) any {
	if verdict == domain.VerificationVerdictNone {
		return nil
	}
	return string(verdict)
}
