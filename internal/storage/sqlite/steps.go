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

// stepColumns 是步骤表的完整列顺序。
const stepColumns = `id, task_id, sequence, step_type, title, objective, instructions_json,
	role_id, role_snapshot_json, verification_spec_json, retry_json, max_attempts,
	current_attempt_id, status, result, failure_stage, failure_summary, started_at,
	finished_at, created_at, updated_at`

// stepInsertCount 是写入步骤时的参数个数。
const stepInsertCount = 21

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
	return s.updateStep(ctx, record, nil)
}

// UpdateStepIfStatus 在数据库中的状态仍属于 expected 时写入步骤。
func (s stepStore) UpdateStepIfStatus(
	ctx context.Context,
	step domain.Step,
	expected []domain.StepStatus,
) error {
	if len(expected) == 0 {
		return wrapError(
			"条件更新步骤",
			errors.Join(storage.ErrInvalidData, errors.New("期望状态集合不能为空")),
		)
	}
	record, err := encodeStep(step)
	if err != nil {
		return err
	}
	return s.updateStep(ctx, record, expected)
}

// updateStep 执行步骤更新；expected 为空表示无条件更新，否则附加状态前置条件。
func (s stepStore) updateStep(
	ctx context.Context,
	record stepRecord,
	expected []domain.StepStatus,
) error {
	statement := `
UPDATE steps SET
	sequence = ?, step_type = ?, title = ?, objective = ?, instructions_json = ?,
	role_id = ?, role_snapshot_json = ?, verification_spec_json = ?, retry_json = ?,
	max_attempts = ?, current_attempt_id = ?, status = ?, result = ?, failure_stage = ?,
	failure_summary = ?, started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ?`
	args := []any{
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
		nullIfBlank(record.step.FailureSummary),
		toMillisPtr(record.step.StartedAt),
		toMillisPtr(record.step.FinishedAt),
		toMillis(record.step.UpdatedAt),
		toIDParam(record.step.ID),
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
		return wrapError("更新步骤", err)
	}
	if len(expected) == 0 {
		return ensureAffected(result, "步骤", record.step.ID)
	}
	return ensureStatusAffected(result, "步骤", record.step.ID)
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

// ListTasksWithOccupiedSteps 返回存在执行占用步骤的任务标识。
//
// 占用指 running / developing / verifying：进程退出后这些状态不会自行收敛，而它们
// 会阻止对应步骤再次执行，因此启动恢复需要先按任务聚合定位。查询只读，结果按任务
// 标识稳定排序，使重复执行得到一致顺序。
func (s stepStore) ListTasksWithOccupiedSteps(ctx context.Context) ([]domain.ID, error) {
	const statement = `SELECT DISTINCT task_id FROM steps
		WHERE status IN (?, ?, ?)
		ORDER BY task_id`
	rows, err := s.q.QueryContext(
		ctx,
		statement,
		string(domain.StepStatusRunning),
		string(domain.StepStatusDeveloping),
		string(domain.StepStatusVerifying),
	)
	if err != nil {
		return nil, wrapError("查询执行占用步骤", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	taskIDs := make([]domain.ID, 0, 8)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, wrapError("读取执行占用步骤", err)
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		taskIDs = append(taskIDs, domain.ID(raw))
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询执行占用步骤", err)
	}
	return taskIDs, nil
}

// ListStepPage 按序号稳定排序分页返回任务的步骤。
//
// 排序键是 (sequence, id)：sequence 在同一任务内唯一，附加 id 只是为了让排序在
// 数据异常时仍然确定，分页不会因行顺序抖动而漏读或重复。
func (s stepStore) ListStepPage(
	ctx context.Context,
	filter storage.StepFilter,
) (storage.StepPage, error) {
	if err := filter.TaskID.Validate(); err != nil {
		return storage.StepPage{}, err
	}
	conditions := []string{"task_id = ?"}
	args := []any{toIDParam(filter.TaskID)}
	if len(filter.Statuses) > 0 {
		placeholdersForStatus := make([]string, 0, len(filter.Statuses))
		for _, status := range filter.Statuses {
			placeholdersForStatus = append(placeholdersForStatus, "?")
			args = append(args, string(status))
		}
		conditions = append(
			conditions,
			"status IN ("+strings.Join(placeholdersForStatus, ", ")+")",
		)
	}
	var countBuilder strings.Builder
	countBuilder.WriteString(`SELECT COUNT(*) FROM steps`)
	appendConditions(&countBuilder, conditions)
	page := storage.StepPage{Steps: []domain.Step{}}
	if err := s.q.QueryRowContext(ctx, countBuilder.String(), args...).Scan(&page.Total); err != nil {
		return storage.StepPage{}, wrapError("统计步骤数量", err)
	}
	if page.Total == 0 {
		return page, nil
	}
	var builder strings.Builder
	builder.WriteString(`SELECT `)
	builder.WriteString(stepColumns)
	builder.WriteString(` FROM steps`)
	appendConditions(&builder, conditions)
	builder.WriteString(` ORDER BY sequence, id`)
	queryArgs := append([]any{}, args...)
	queryArgs = appendPaging(&builder, queryArgs, filter.Limit, filter.Offset)
	rows, err := s.q.QueryContext(ctx, builder.String(), queryArgs...)
	if err != nil {
		return storage.StepPage{}, wrapError("查询步骤", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return storage.StepPage{}, wrapError("读取步骤", err)
		}
		page.Steps = append(page.Steps, *step)
	}
	if err := rows.Err(); err != nil {
		return storage.StepPage{}, wrapError("查询步骤", err)
	}
	return page, nil
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
		nullIfBlank(record.step.FailureSummary),
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
		failureSummary   sql.NullString
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
		&failureSummary,
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
	step.FailureSummary = failureSummary.String
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
