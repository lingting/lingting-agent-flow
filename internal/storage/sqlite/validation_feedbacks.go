package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// validationFeedbackColumns 是失败反馈表的完整列顺序。
const validationFeedbackColumns = `id, task_id, step_id, developer_attempt_id,
	validation_run_id, verdict, summary, infrastructure_error, checks_json, created_at`

// validationFeedbackInsertCount 是写入反馈时的参数个数。
const validationFeedbackInsertCount = 10

// validationFeedbackStore 实现 storage.ValidationFeedbackStore。
//
// 反馈只追加不覆盖：同一轮验证重复写入返回 ErrConflict，历史反馈永远不会被新的
// 验证改写。
type validationFeedbackStore struct {
	q queryer
}

// CreateValidationFailureFeedback 追加一条失败反馈。
func (s validationFeedbackStore) CreateValidationFailureFeedback(
	ctx context.Context,
	feedback domain.ValidationFailureFeedback,
) error {
	if err := feedback.TaskID.Validate(); err != nil {
		return err
	}
	if err := feedback.StepID.Validate(); err != nil {
		return err
	}
	if err := feedback.DeveloperAttemptID.Validate(); err != nil {
		return err
	}
	if err := feedback.ValidationRunID.Validate(); err != nil {
		return err
	}
	if err := feedback.Verdict.Validate(); err != nil {
		return err
	}
	if feedback.Verdict == domain.VerificationVerdictNone {
		return wrapError(
			"写入验证失败反馈",
			errors.Join(storage.ErrInvalidData, errors.New("失败反馈必须给出验证结论")),
		)
	}
	checks, err := json.Marshal(feedback.Checks)
	if err != nil {
		return wrapError("编码失败检查", err)
	}
	if checks == nil {
		checks = []byte("[]")
	}
	statement := `INSERT INTO validation_feedbacks (` + validationFeedbackColumns + `)
		VALUES (` + placeholders(validationFeedbackInsertCount) + `)`
	_, err = s.q.ExecContext(
		ctx,
		statement,
		toIDParam(domain.NewID()),
		toIDParam(feedback.TaskID),
		toIDParam(feedback.StepID),
		toIDParam(feedback.DeveloperAttemptID),
		toIDParam(feedback.ValidationRunID),
		string(feedback.Verdict),
		sqlNullString(feedback.Summary),
		toBoolInt(feedback.InfrastructureError),
		string(checks),
		toMillis(feedback.CreatedAt),
	)
	if err != nil {
		return wrapError("写入验证失败反馈", err)
	}
	return nil
}

// GetValidationFailureFeedback 读取某轮验证的失败反馈。
func (s validationFeedbackStore) GetValidationFailureFeedback(
	ctx context.Context,
	runID domain.ID,
) (*domain.ValidationFailureFeedback, error) {
	if err := runID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + validationFeedbackColumns + `
		FROM validation_feedbacks WHERE validation_run_id = ? LIMIT 1`
	feedback, err := scanValidationFeedback(s.q.QueryRowContext(ctx, statement, toIDParam(runID)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("验证失败反馈", runID.String())
		}
		return nil, wrapError("读取验证失败反馈", err)
	}
	return feedback, nil
}

// LatestValidationFailureFeedbackByStep 返回步骤最近一次的失败反馈。
func (s validationFeedbackStore) LatestValidationFailureFeedbackByStep(
	ctx context.Context,
	stepID domain.ID,
) (*domain.ValidationFailureFeedback, error) {
	if err := stepID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + validationFeedbackColumns + `
		FROM validation_feedbacks WHERE step_id = ?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`
	feedback, err := scanValidationFeedback(s.q.QueryRowContext(ctx, statement, toIDParam(stepID)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("验证失败反馈", stepID.String())
		}
		return nil, wrapError("读取验证失败反馈", err)
	}
	return feedback, nil
}

// scanValidationFeedback 从一行结果读取失败反馈。
func scanValidationFeedback(row scanner) (*domain.ValidationFailureFeedback, error) {
	var (
		feedback            domain.ValidationFailureFeedback
		rawID               string
		taskID              string
		stepID              string
		attemptID           string
		runID               string
		verdict             string
		summary             sql.NullString
		infrastructureError int
		checksJSON          string
		createdAt           int64
	)
	err := row.Scan(
		&rawID,
		&taskID,
		&stepID,
		&attemptID,
		&runID,
		&verdict,
		&summary,
		&infrastructureError,
		&checksJSON,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}
	feedback.TaskID = domain.ID(taskID)
	feedback.StepID = domain.ID(stepID)
	feedback.DeveloperAttemptID = domain.ID(attemptID)
	feedback.ValidationRunID = domain.ID(runID)
	feedback.Verdict = domain.VerificationVerdict(verdict)
	feedback.Summary = summary.String
	feedback.InfrastructureError = fromBoolInt(infrastructureError)
	feedback.CreatedAt = fromMillis(createdAt)
	if trimmed := strings.TrimSpace(checksJSON); trimmed != "" {
		if err := unmarshalJSON(
			"ValidationFailureFeedback.checks",
			trimmed,
			&feedback.Checks,
		); err != nil {
			return nil, err
		}
	}
	return &feedback, nil
}
