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
	verdict, summary, checks_json, command_results_json, assessment_json,
	review_skipped, failure_summary, created_at`

// verificationInsertCount 是写入验证结果时的参数个数。
const verificationInsertCount = 14

// verificationStore 实现 storage.VerificationStore。
type verificationStore struct {
	q queryer
}

// verificationRecord 是写入验证结果前的 JSON 编码结果。
type verificationRecord struct {
	result       domain.VerificationResult
	checksJSON   string
	commandsJSON string
	assessment   any
}

// CreateVerificationResult 追加一条验证结果。
//
// 只提供追加语义：每次验证都会写入新记录，历史记录不会被覆盖。写入前校验任务、
// 步骤与尝试的归属是否一致，避免把验证结果挂到不匹配的执行尝试上。
func (s verificationStore) CreateVerificationResult(
	ctx context.Context,
	result domain.VerificationResult,
) error {
	record, err := encodeVerification(result)
	if err != nil {
		return err
	}
	if err := s.ensureScopeConsistent(ctx, result); err != nil {
		return err
	}
	statement := `INSERT INTO verification_results (` + verificationColumns + `)
		VALUES (` + placeholders(verificationInsertCount) + `)`
	_, err = s.q.ExecContext(
		ctx,
		statement,
		toIDParam(record.result.ID),
		toIDParam(record.result.TaskID),
		toIDParam(record.result.StepID),
		toIDParam(record.result.AttemptID),
		toIDPtrParam(record.result.ValidatorSessionID),
		toBoolInt(record.result.Passed),
		string(record.result.Verdict),
		sqlNullString(record.result.Summary),
		record.checksJSON,
		record.commandsJSON,
		record.assessment,
		toBoolInt(record.result.ReviewSkipped),
		sqlNullString(record.result.FailureSummary),
		toMillis(record.result.CreatedAt),
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
//
// 数据库不声明外键，因此这里只校验能够读到父记录时的归属关系；尝试或步骤记录
// 已不存在（历史孤立 ID）时不阻止写入。
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
			return nil
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

// encodeVerification 校验验证结果并编码其 JSON 字段。
func encodeVerification(result domain.VerificationResult) (verificationRecord, error) {
	if err := result.Validate(); err != nil {
		return verificationRecord{}, err
	}
	checks, err := marshalSlice("VerificationResult.checks", result.Checks)
	if err != nil {
		return verificationRecord{}, err
	}
	commands, err := marshalSlice("VerificationResult.command_results", result.CommandResults)
	if err != nil {
		return verificationRecord{}, err
	}
	var assessment any
	if result.Assessment != nil {
		encoded, err := marshalJSON("VerificationResult.assessment", result.Assessment)
		if err != nil {
			return verificationRecord{}, err
		}
		assessment = encoded
	}
	return verificationRecord{
		result:       result,
		checksJSON:   checks,
		commandsJSON: commands,
		assessment:   assessment,
	}, nil
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
		verdict            string
		summary            sql.NullString
		checksJSON         string
		commandsJSON       string
		assessmentJSON     sql.NullString
		reviewSkipped      int
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
		&verdict,
		&summary,
		&checksJSON,
		&commandsJSON,
		&assessmentJSON,
		&reviewSkipped,
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
	result.Verdict = domain.VerificationVerdict(verdict)
	result.Summary = summary.String
	result.ReviewSkipped = fromBoolInt(reviewSkipped)
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
	if assessmentJSON.Valid && assessmentJSON.String != "" {
		var assessment domain.AgentAssessment
		if err := unmarshalJSON(
			"VerificationResult.assessment",
			assessmentJSON.String,
			&assessment,
		); err != nil {
			return nil, err
		}
		result.Assessment = &assessment
	}
	return &result, nil
}
