package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
)

// stepColumns 是步骤表的完整列顺序。
const stepColumns = `id, task_id, sequence, title, objective, instructions_json, role_id,
	role_snapshot_json, verification_spec_json, status, created_at, updated_at`

// stepStore 实现 storage.StepStore。
type stepStore struct {
	q queryer
}

// stepRecord 是写入步骤前的 JSON 编码结果。
type stepRecord struct {
	step             domain.Step
	instructionsJSON string
	roleSnapshotJSON string
	verificationJSON string
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
	sequence = ?, title = ?, objective = ?, instructions_json = ?, role_id = ?,
	role_snapshot_json = ?, verification_spec_json = ?, status = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		record.step.Sequence,
		record.step.Title,
		record.step.Objective,
		record.instructionsJSON,
		toIDParam(record.step.RoleID),
		record.roleSnapshotJSON,
		record.verificationJSON,
		string(record.step.Status),
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
	const statement = `INSERT INTO steps (` + stepColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(record.step.ID),
		toIDParam(record.step.TaskID),
		record.step.Sequence,
		record.step.Title,
		record.step.Objective,
		record.instructionsJSON,
		toIDParam(record.step.RoleID),
		record.roleSnapshotJSON,
		record.verificationJSON,
		string(record.step.Status),
		toMillis(record.step.CreatedAt),
		toMillis(record.step.UpdatedAt),
	)
	if err != nil {
		return wrapError("写入步骤", err)
	}
	return nil
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
	snapshot, err := marshalJSON("Step.role_snapshot", step.RoleSnapshot)
	if err != nil {
		return stepRecord{}, err
	}
	verification, err := marshalJSON("Step.verification_spec", step.VerificationSpec)
	if err != nil {
		return stepRecord{}, err
	}
	return stepRecord{
		step:             step,
		instructionsJSON: instructions,
		roleSnapshotJSON: snapshot,
		verificationJSON: verification,
	}, nil
}

// scanStep 从一行结果读取步骤。
func scanStep(row scanner) (*domain.Step, error) {
	var (
		step             domain.Step
		rawID            string
		taskID           string
		instructionsJSON string
		roleID           string
		snapshotJSON     string
		verificationJSON string
		status           string
		createdAt        int64
		updatedAt        int64
	)
	err := row.Scan(
		&rawID,
		&taskID,
		&step.Sequence,
		&step.Title,
		&step.Objective,
		&instructionsJSON,
		&roleID,
		&snapshotJSON,
		&verificationJSON,
		&status,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	step.ID = domain.ID(rawID)
	step.TaskID = domain.ID(taskID)
	step.RoleID = domain.ID(roleID)
	step.Status = domain.StepStatus(status)
	step.CreatedAt = fromMillis(createdAt)
	step.UpdatedAt = fromMillis(updatedAt)
	if err := unmarshalJSON("Step.instructions", instructionsJSON, &step.Instructions); err != nil {
		return nil, err
	}
	if err := unmarshalJSON("Step.role_snapshot", snapshotJSON, &step.RoleSnapshot); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(
		"Step.verification_spec",
		verificationJSON,
		&step.VerificationSpec,
	); err != nil {
		return nil, err
	}
	if step.RoleSnapshot.RoleID != step.RoleID {
		return nil, fmt.Errorf("sqlite: 步骤 %s 的角色快照与角色标识不一致", step.ID)
	}
	return &step, nil
}
