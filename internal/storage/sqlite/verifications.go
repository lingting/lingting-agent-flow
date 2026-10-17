package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// verificationColumns 是验证结果表的完整列顺序。
const verificationColumns = `id, task_id, step_id, attempt_id, validator_session_id, passed,
	summary, checks_json, command_results_json, failure_summary, created_at`

// verificationStore 实现 storage.VerificationStore。
type verificationStore struct {
	q queryer
}

// CreateVerificationResult 追加一条验证结果。
//
// 只提供追加语义：每次验证都会写入新记录，历史记录不会被覆盖。写入前校验任务、
// 步骤与尝试的归属是否一致，避免把验证结果挂到不匹配的执行尝试上。
func (s verificationStore) CreateVerificationResult(
	ctx context.Context,
	result domain.VerificationResult,
) error {
	if err := result.Validate(); err != nil {
		return err
	}
	checks, err := marshalSlice("VerificationResult.checks", result.Checks)
	if err != nil {
		return err
	}
	commands, err := marshalSlice("VerificationResult.command_results", result.CommandResults)
	if err != nil {
		return err
	}
	if err := s.ensureScopeConsistent(ctx, result); err != nil {
		return err
	}
	const statement = `INSERT INTO verification_results (` + verificationColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.q.ExecContext(
		ctx,
		statement,
		toIDParam(result.ID),
		toIDParam(result.TaskID),
		toIDParam(result.StepID),
		toIDParam(result.AttemptID),
		toIDPtrParam(result.ValidatorSessionID),
		toBoolInt(result.Passed),
		sqlNullString(result.Summary),
		checks,
		commands,
		sqlNullString(result.FailureSummary),
		toMillis(result.CreatedAt),
	)
	if err != nil {
		return wrapError("写入验证结果", err)
	}
	return nil
}

// GetVerificationResult 读取单条验证结果。
func (s verificationStore) GetVerificationResult(
	ctx context.Context,
	id domain.ID,
) (*domain.VerificationResult, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + verificationColumns + ` FROM verification_results WHERE id = ?`
	result, err := scanVerificationResult(
		s.q.QueryRowContext(ctx, statement, toIDParam(id)),
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("验证结果", id.String())
		}
		return nil, wrapError("读取验证结果", err)
	}
	return result, nil
}

// ListVerificationResultsByAttempt 按创建时间返回某次尝试的全部验证结果。
func (s verificationStore) ListVerificationResultsByAttempt(
	ctx context.Context,
	attemptID domain.ID,
) ([]domain.VerificationResult, error) {
	if err := attemptID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + verificationColumns + ` FROM verification_results
		WHERE attempt_id = ? ORDER BY created_at, rowid`
	rows, err := s.q.QueryContext(ctx, statement, toIDParam(attemptID))
	if err != nil {
		return nil, wrapError("查询验证结果", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	results := make([]domain.VerificationResult, 0, 4)
	for rows.Next() {
		result, err := scanVerificationResult(rows)
		if err != nil {
			return nil, wrapError("读取验证结果", err)
		}
		results = append(results, *result)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询验证结果", err)
	}
	return results, nil
}

// ensureScopeConsistent 校验验证结果的任务、步骤与尝试归属一致。
func (s verificationStore) ensureScopeConsistent(
	ctx context.Context,
	result domain.VerificationResult,
) error {
	const statement = `SELECT steps.task_id, attempts.step_id
		FROM attempts JOIN steps ON steps.id = attempts.step_id
		WHERE attempts.id = ?`
	var taskID, stepID string
	err := s.q.QueryRowContext(ctx, statement, toIDParam(result.AttemptID)).Scan(&taskID, &stepID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFoundError("执行尝试", result.AttemptID.String())
		}
		return wrapError("读取尝试归属", err)
	}
	if stepID != result.StepID.String() {
		return fmt.Errorf(
			"sqlite: 验证结果 %s 的步骤 %s 与尝试所属步骤 %s 不一致: %w",
			result.ID,
			result.StepID,
			stepID,
			storage.ErrInvalidData,
		)
	}
	if taskID != result.TaskID.String() {
		return fmt.Errorf(
			"sqlite: 验证结果 %s 的任务 %s 与尝试所属任务 %s 不一致: %w",
			result.ID,
			result.TaskID,
			taskID,
			storage.ErrInvalidData,
		)
	}
	return nil
}

// scanVerificationResult 从一行结果读取验证结果。
func scanVerificationResult(row scanner) (*domain.VerificationResult, error) {
	var (
		result             domain.VerificationResult
		rawID              string
		taskID             string
		stepID             string
		attemptID          string
		validatorSessionID sql.NullString
		passed             int
		summary            sql.NullString
		checksJSON         string
		commandsJSON       string
		failureSummary     sql.NullString
		createdAt          int64
	)
	err := row.Scan(
		&rawID,
		&taskID,
		&stepID,
		&attemptID,
		&validatorSessionID,
		&passed,
		&summary,
		&checksJSON,
		&commandsJSON,
		&failureSummary,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}
	result.ID = domain.ID(rawID)
	result.TaskID = domain.ID(taskID)
	result.StepID = domain.ID(stepID)
	result.AttemptID = domain.ID(attemptID)
	result.ValidatorSessionID = fromIDPtr(validatorSessionID)
	result.Passed = fromBoolInt(passed)
	result.Summary = summary.String
	result.FailureSummary = failureSummary.String
	result.CreatedAt = fromMillis(createdAt)
	if err := unmarshalJSON("VerificationResult.checks", checksJSON, &result.Checks); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(
		"VerificationResult.command_results",
		commandsJSON,
		&result.CommandResults,
	); err != nil {
		return nil, err
	}
	return &result, nil
}
