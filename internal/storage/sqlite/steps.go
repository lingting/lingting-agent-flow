package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
)

// stepColumns 是步骤表的完整列顺序。
const stepColumns = `id, task_id, sequence, step_type, title, objective, instructions_json,
	role_id, role_snapshot_json, verification_spec_json, retry_json, max_attempts,
	current_attempt_id, status, result, failure_stage, started_at, finished_at,
	created_at, updated_at`

// stepInsertCount 是写入步骤时的参数个数。
const stepInsertCount = 20

// stepStore 实现 storage.StepStore。
type stepStore struct {
	q queryer
}

// stepRecord 是写入步骤前的 JSON 编码结果。
type stepRecord struct {
	step             domain.Step
	instructionsJSON string
	roleSnapshotJSON any
	verificationJSON string
	retryJSON        string
}

// CreateStep 写入单个步骤。
//
// 同一任务内 sequence 重复时返回 storage.ErrConflict。
func (s stepStore) CreateStep(ctx context.Context, step domain.Step) error {
	record, err := encodeStep(step)
	if err != nil {
		return err
	}
	return s.insertStep(ctx, record)
}

// CreateSteps 在一个事务边界内写入多个步骤。
//
// 任一步骤写入失败时整体回滚，不留下部分步骤。若当前已处于事务中，则复用该事务。
func (s stepStore) CreateSteps(ctx context.Context, steps []domain.Step) error {
	records, err := encodeSteps(steps)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	withTx, ok := s.q.(transactioner)
	if !ok {
		for _, record := range records {
			if err := s.insertStep(ctx, record); err != nil {
				return err
			}
		}
		return nil
	}
	return withTx.runInTx(ctx, func(tx queryer) error {
		inner := stepStore{q: tx}
		for _, record := range records {
			if err := inner.insertStep(ctx, record); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateStep 按主键更新步骤。
func (s stepStore) UpdateStep(ctx context.Context, step domain.Step) error {
	record, err := encodeStep(step)
	if err != nil {
		return err
	}
	const statement = `
UPDATE steps SET
	sequence = ?, step_type = ?, title = ?, objective = ?, instructions_json = ?,
	role_id = ?, role_snapshot_json = ?, verification_spec_json = ?, retry_json = ?,
	max_attempts = ?, current_attempt_id = ?, status = ?, result = ?, failure_stage = ?,
	started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		record.step.Sequence,
		string(record.step.Type),
		record.step.Title,
		record.step.Objective,
		record.instructionsJSON,
		toIDPtrParam(roleIDPtr(record.step.RoleID)),
		record.roleSnapshotJSON,
		record.verificationJSON,
		record.retryJSON,
		record.step.MaxAttempts,
		toIDPtrParam(record.step.CurrentAttemptID),
		string(record.step.Status),
		nullIfBlank(record.step.Result),
		nullIfBlank(record.step.FailureStage),
		toMillisPtr(record.step.StartedAt),
		toMillisPtr(record.step.FinishedAt),
		toMillis(record.step.UpdatedAt),
		toIDParam(record.step.ID),
	)
	if err != nil {
		return wrapError("更新步骤", err)
	}
	return ensureAffected(result, "步骤", step.ID)
}

// GetStep 读取单个步骤。
func (s stepStore) GetStep(ctx context.Context, id domain.ID) (*domain.Step, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + stepColumns + ` FROM steps WHERE id = ?`
	step, err := scanStep(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("步骤", id.String())
		}
		return nil, wrapError("读取步骤", err)
	}
	return step, nil
}

// ListStepsByTask 按序号返回任务的全部步骤。
func (s stepStore) ListStepsByTask(ctx context.Context, taskID domain.ID) ([]domain.Step, error) {
	if err := taskID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + stepColumns + ` FROM steps WHERE task_id = ? ORDER BY sequence`
	rows, err := s.q.QueryContext(ctx, statement, toIDParam(taskID))
	if err != nil {
		return nil, wrapError("查询步骤", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	steps := make([]domain.Step, 0, 8)
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, wrapError("读取步骤", err)
		}
		steps = append(steps, *step)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询步骤", err)
	}
	return steps, nil
}

// insertStep 写入编码后的步骤记录。
func (s stepStore) insertStep(ctx context.Context, record stepRecord) error {
	statement := `INSERT INTO steps (` + stepColumns + `)
		VALUES (` + placeholders(stepInsertCount) + `)`
	_, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(record.step.ID),
		toIDParam(record.step.TaskID),
		record.step.Sequence,
		string(record.step.Type),
		record.step.Title,
		record.step.Objective,
		record.instructionsJSON,
		toIDPtrParam(roleIDPtr(record.step.RoleID)),
		record.roleSnapshotJSON,
		record.verificationJSON,
		record.retryJSON,
		record.step.MaxAttempts,
		toIDPtrParam(record.step.CurrentAttemptID),
		string(record.step.Status),
		nullIfBlank(record.step.Result),
		nullIfBlank(record.step.FailureStage),
		toMillisPtr(record.step.StartedAt),
		toMillisPtr(record.step.FinishedAt),
		toMillis(record.step.CreatedAt),
		toMillis(record.step.UpdatedAt),
	)
	if err != nil {
		return wrapError("写入步骤", err)
	}
	return nil
}

// roleIDPtr 把可选角色标识转换为指针，空标识视为未关联角色。
func roleIDPtr(id domain.ID) *domain.ID {
	if id.IsEmpty() {
		return nil
	}
	return &id
}

// encodeSteps 批量编码步骤并校验计划约束。
func encodeSteps(steps []domain.Step) ([]stepRecord, error) {
	records := make([]stepRecord, 0, len(steps))
	for _, step := range steps {
		record, err := encodeStep(step)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// encodeStep 校验步骤并编码其 JSON 字段。
func encodeStep(step domain.Step) (stepRecord, error) {
	if err := step.Validate(); err != nil {
		return stepRecord{}, err
	}
	instructions, err := marshalJSON("Step.instructions", step.Instructions)
	if err != nil {
		return stepRecord{}, err
	}
	var snapshot any
	if !step.RoleID.IsEmpty() {
		encoded, err := marshalJSON("Step.role_snapshot", step.RoleSnapshot)
		if err != nil {
			return stepRecord{}, err
		}
		snapshot = encoded
	}
	verification, err := marshalJSON("Step.verification_spec", step.VerificationSpec)
	if err != nil {
		return stepRecord{}, err
	}
	retry, err := marshalJSON("Step.retry", step.Retry)
	if err != nil {
		return stepRecord{}, err
	}
	return stepRecord{
		step:             step,
		instructionsJSON: instructions,
		roleSnapshotJSON: snapshot,
		verificationJSON: verification,
		retryJSON:        retry,
	}, nil
}

// scanStep 从一行结果读取步骤。
func scanStep(row scanner) (*domain.Step, error) {
	var (
		step             domain.Step
		rawID            string
		taskID           string
		stepType         string
		instructionsJSON string
		roleID           sql.NullString
		snapshotJSON     sql.NullString
		verificationJSON string
		retryJSON        string
		currentAttemptID sql.NullString
		status           string
		result           sql.NullString
		failureStage     sql.NullString
		startedAt        sql.NullInt64
		finishedAt       sql.NullInt64
		createdAt        int64
		updatedAt        int64
	)
	err := row.Scan(
		&rawID,
		&taskID,
		&step.Sequence,
		&stepType,
		&step.Title,
		&step.Objective,
		&instructionsJSON,
		&roleID,
		&snapshotJSON,
		&verificationJSON,
		&retryJSON,
		&step.MaxAttempts,
		&currentAttemptID,
		&status,
		&result,
		&failureStage,
		&startedAt,
		&finishedAt,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	step.ID = domain.ID(rawID)
	step.TaskID = domain.ID(taskID)
	step.Type = domain.StepType(stepType)
	step.RoleID = domain.ID(roleID.String)
	step.Status = domain.StepStatus(status)
	step.Result = result.String
	step.FailureStage = failureStage.String
	step.CurrentAttemptID = fromIDPtr(currentAttemptID)
	step.StartedAt = fromMillisPtr(startedAt)
	step.FinishedAt = fromMillisPtr(finishedAt)
	step.CreatedAt = fromMillis(createdAt)
	step.UpdatedAt = fromMillis(updatedAt)
	if err := unmarshalJSON("Step.instructions", instructionsJSON, &step.Instructions); err != nil {
		return nil, err
	}
	if snapshotJSON.Valid && snapshotJSON.String != "" {
		if err := unmarshalJSON(
			"Step.role_snapshot",
			snapshotJSON.String,
			&step.RoleSnapshot,
		); err != nil {
			return nil, err
		}
	}
	if err := unmarshalJSON(
		"Step.verification_spec",
		verificationJSON,
		&step.VerificationSpec,
	); err != nil {
		return nil, err
	}
	if err := unmarshalJSON("Step.retry", retryJSON, &step.Retry); err != nil {
		return nil, err
	}
	if step.RoleID.IsEmpty() {
		return &step, nil
	}
	if step.RoleSnapshot.RoleID != step.RoleID {
		return nil, fmt.Errorf("sqlite: 步骤 %s 的角色快照与角色标识不一致", step.ID)
	}
	return &step, nil
}
