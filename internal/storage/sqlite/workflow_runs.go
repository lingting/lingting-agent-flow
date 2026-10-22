package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// workflowRunColumns 是工作流执行表的完整列顺序。
const workflowRunColumns = `id, task_id, plan_digest, status, stage, current_step_id,
	current_attempt_id, last_completed_step_id, last_completed_sequence, completed_steps,
	total_steps, cancel_requested, cancel_requested_at, worker_id, lease_expires_at,
	error_class, error_summary, revision, started_at, finished_at, created_at, updated_at`

// workflowRunInsertCount 是写入执行记录时的参数个数。
const workflowRunInsertCount = 22

// workflowRunActivePredicate 是"未结束执行记录"的判定条件。
//
// 与迁移脚本中的部分唯一索引保持一致：两处必须描述同一集合，否则"唯一性由数据库
// 保证"就会失效。
const workflowRunActivePredicate = `status NOT IN ('completed', 'cancelled', 'failed')`

// workflowRunStore 实现 storage.WorkflowRunStore。
//
// 写入一律以版本号为前置条件：读取到的 revision 是本次写入的期望值，数据库中的
// 版本号变化时返回 ErrConflict，因此迟到的结果无法覆盖更新的状态。
type workflowRunStore struct {
	q queryer
}

// CreateWorkflowRun 写入一条新的执行记录。
func (s workflowRunStore) CreateWorkflowRun(
	ctx context.Context,
	run domain.WorkflowRun,
) error {
	if err := run.Validate(); err != nil {
		return err
	}
	if run.Revision != 0 {
		return wrapError(
			"写入工作流执行记录",
			errors.Join(storage.ErrInvalidData, errors.New("新建记录的版本号必须为 0")),
		)
	}
	statement := `INSERT INTO workflow_runs (` + workflowRunColumns + `)
		VALUES (` + placeholders(workflowRunInsertCount) + `)`
	_, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(run.ID),
		toIDParam(run.TaskID),
		run.PlanDigest,
		string(run.Status),
		string(run.Stage),
		toIDPtrParam(run.CurrentStepID),
		toIDPtrParam(run.CurrentAttemptID),
		toIDPtrParam(run.LastCompletedStepID),
		run.LastCompletedSequence,
		run.CompletedSteps,
		run.TotalSteps,
		toBoolInt(run.CancelRequested),
		toMillisPtr(run.CancelRequestedAt),
		sqlNullString(run.WorkerID),
		toMillisPtr(run.LeaseExpiresAt),
		sqlNullString(run.ErrorClass),
		sqlNullString(run.ErrorSummary),
		run.Revision,
		toMillisPtr(run.StartedAt),
		toMillisPtr(run.FinishedAt),
		toMillis(run.CreatedAt),
		toMillis(run.UpdatedAt),
	)
	if err != nil {
		return wrapError("写入工作流执行记录", err)
	}
	return nil
}

// UpdateWorkflowRun 以版本号为条件写入执行记录。
//
// 成功后调用方必须把 Revision 加一：版本号由数据库递增，不由调用方自增，
// 这样两个并发写入不会同时成功。
func (s workflowRunStore) UpdateWorkflowRun(
	ctx context.Context,
	run domain.WorkflowRun,
) error {
	if err := run.Validate(); err != nil {
		return err
	}
	statement := `
UPDATE workflow_runs SET
	plan_digest = ?, status = ?, stage = ?, current_step_id = ?, current_attempt_id = ?,
	last_completed_step_id = ?, last_completed_sequence = ?, completed_steps = ?,
	total_steps = ?, cancel_requested = ?, cancel_requested_at = ?, worker_id = ?,
	lease_expires_at = ?, error_class = ?, error_summary = ?, revision = revision + 1,
	started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ? AND revision = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		run.PlanDigest,
		string(run.Status),
		string(run.Stage),
		toIDPtrParam(run.CurrentStepID),
		toIDPtrParam(run.CurrentAttemptID),
		toIDPtrParam(run.LastCompletedStepID),
		run.LastCompletedSequence,
		run.CompletedSteps,
		run.TotalSteps,
		toBoolInt(run.CancelRequested),
		toMillisPtr(run.CancelRequestedAt),
		sqlNullString(run.WorkerID),
		toMillisPtr(run.LeaseExpiresAt),
		sqlNullString(run.ErrorClass),
		sqlNullString(run.ErrorSummary),
		toMillisPtr(run.StartedAt),
		toMillisPtr(run.FinishedAt),
		toMillis(run.UpdatedAt),
		toIDParam(run.ID),
		run.Revision,
	)
	if err != nil {
		return wrapError("更新工作流执行记录", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("更新工作流执行记录", err)
	}
	if affected == 0 {
		// 影响行数为 0 有两种可能：记录不存在，或版本号已变化。两者都不能被当作
		// 写入成功，统一按冲突处理并由调用方重新读取真实值。
		return wrapError(
			"更新工作流执行记录",
			errors.Join(
				storage.ErrConflict,
				errors.New("执行记录不存在或已被其他操作推进"),
			),
		)
	}
	return nil
}

// GetWorkflowRun 读取单条执行记录。
func (s workflowRunStore) GetWorkflowRun(
	ctx context.Context,
	id domain.ID,
) (*domain.WorkflowRun, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + workflowRunColumns + ` FROM workflow_runs WHERE id = ?`
	run, err := scanWorkflowRun(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("工作流执行记录", id.String())
		}
		return nil, wrapError("读取工作流执行记录", err)
	}
	return run, nil
}

// GetActiveWorkflowRun 返回任务当前未结束的执行记录。
func (s workflowRunStore) GetActiveWorkflowRun(
	ctx context.Context,
	taskID domain.ID,
) (*domain.WorkflowRun, error) {
	if err := taskID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + workflowRunColumns + ` FROM workflow_runs
		WHERE task_id = ? AND ` + workflowRunActivePredicate + `
		ORDER BY created_at DESC, rowid DESC LIMIT 1`
	run, err := scanWorkflowRun(s.q.QueryRowContext(ctx, statement, toIDParam(taskID)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("工作流执行记录", taskID.String())
		}
		return nil, wrapError("读取工作流执行记录", err)
	}
	return run, nil
}

// ListWorkflowRunsByTask 按创建时间倒序返回任务的执行记录。
func (s workflowRunStore) ListWorkflowRunsByTask(
	ctx context.Context,
	taskID domain.ID,
) ([]domain.WorkflowRun, error) {
	if err := taskID.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + workflowRunColumns + ` FROM workflow_runs
		WHERE task_id = ? ORDER BY created_at DESC, rowid DESC`
	return s.queryRuns(ctx, statement, toIDParam(taskID))
}

// ListUnfinishedWorkflowRuns 返回全部未结束的执行记录。
//
// 服务启动时用它恢复停留在 running / blocked / recovery_required 的记录。
func (s workflowRunStore) ListUnfinishedWorkflowRuns(
	ctx context.Context,
) ([]domain.WorkflowRun, error) {
	const statement = `SELECT ` + workflowRunColumns + ` FROM workflow_runs
		WHERE ` + workflowRunActivePredicate + `
		ORDER BY updated_at ASC, rowid ASC`
	return s.queryRuns(ctx, statement)
}

// queryRuns 执行查询并扫描全部执行记录。
func (s workflowRunStore) queryRuns(
	ctx context.Context,
	statement string,
	args ...any,
) ([]domain.WorkflowRun, error) {
	rows, err := s.q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, wrapError("查询工作流执行记录", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	runs := make([]domain.WorkflowRun, 0, 4)
	for rows.Next() {
		run, scanErr := scanWorkflowRun(rows)
		if scanErr != nil {
			return nil, wrapError("读取工作流执行记录", scanErr)
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询工作流执行记录", err)
	}
	return runs, nil
}

// scanWorkflowRun 从一行结果读取执行记录。
func scanWorkflowRun(scanner interface {
	Scan(dest ...any) error
}) (*domain.WorkflowRun, error) {
	var (
		run             domain.WorkflowRun
		id              string
		taskID          string
		status          string
		stage           string
		currentStep     sql.NullString
		currentAttempt  sql.NullString
		lastStep        sql.NullString
		cancelRequested int64
		cancelRequestAt sql.NullInt64
		workerID        sql.NullString
		leaseExpiresAt  sql.NullInt64
		errorClass      sql.NullString
		errorSummary    sql.NullString
		startedAt       sql.NullInt64
		finishedAt      sql.NullInt64
		createdAt       int64
		updatedAt       int64
	)
	err := scanner.Scan(
		&id,
		&taskID,
		&run.PlanDigest,
		&status,
		&stage,
		&currentStep,
		&currentAttempt,
		&lastStep,
		&run.LastCompletedSequence,
		&run.CompletedSteps,
		&run.TotalSteps,
		&cancelRequested,
		&cancelRequestAt,
		&workerID,
		&leaseExpiresAt,
		&errorClass,
		&errorSummary,
		&run.Revision,
		&startedAt,
		&finishedAt,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	run.ID = domain.ID(id)
	run.TaskID = domain.ID(taskID)
	run.Status = domain.WorkflowRunStatus(status)
	run.Stage = domain.WorkflowStage(stage)
	run.CurrentStepID = fromIDPtr(currentStep)
	run.CurrentAttemptID = fromIDPtr(currentAttempt)
	run.LastCompletedStepID = fromIDPtr(lastStep)
	run.CancelRequested = cancelRequested != 0
	run.CancelRequestedAt = fromMillisPtr(cancelRequestAt)
	run.WorkerID = workerID.String
	run.LeaseExpiresAt = fromMillisPtr(leaseExpiresAt)
	run.ErrorClass = errorClass.String
	run.ErrorSummary = errorSummary.String
	run.StartedAt = fromMillisPtr(startedAt)
	run.FinishedAt = fromMillisPtr(finishedAt)
	run.CreatedAt = fromMillis(createdAt)
	run.UpdatedAt = fromMillis(updatedAt)
	return &run, nil
}
