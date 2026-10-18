package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// taskColumns 是任务表的完整列顺序。
const taskColumns = `id, requirement_id, status, host_id, config_json, repository_path,
	base_ref, worktree_path, container_id, current_step_id, current_attempt_id,
	cancel_requested, cancel_requested_at, final_result, failure_summary,
	idempotency_key, lease_owner, lease_expires_at, created_at, started_at,
	finished_at, updated_at`

// taskInsertCount 是写入任务时的参数个数。
const taskInsertCount = 22

// taskStore 实现 storage.TaskStore。
type taskStore struct {
	q queryer
}

// taskRecord 是写入任务前的 JSON 编码结果。
type taskRecord struct {
	task       domain.Task
	configJSON string
}

// CreateTask 写入任务。
//
// requirement_id 与 host_id 必须引用已存在的记录，否则返回 storage.ErrForeignKey。
// 幂等键重复时返回 storage.ErrConflict。
func (s taskStore) CreateTask(ctx context.Context, task domain.Task) error {
	record, err := encodeTask(task)
	if err != nil {
		return err
	}
	statement := `INSERT INTO tasks (` + taskColumns + `)
		VALUES (` + placeholders(taskInsertCount) + `)`
	_, err = s.q.ExecContext(ctx, statement, taskInsertArgs(record)...)
	if err != nil {
		return wrapError("写入任务", err)
	}
	return nil
}

// UpdateTask 按主键更新任务。
func (s taskStore) UpdateTask(ctx context.Context, task domain.Task) error {
	record, err := encodeTask(task)
	if err != nil {
		return err
	}
	const statement = `
UPDATE tasks SET
	status = ?, host_id = ?, config_json = ?, repository_path = ?, base_ref = ?,
	worktree_path = ?, container_id = ?, current_step_id = ?, current_attempt_id = ?,
	cancel_requested = ?, cancel_requested_at = ?, final_result = ?, failure_summary = ?,
	idempotency_key = ?, lease_owner = ?, lease_expires_at = ?, started_at = ?,
	finished_at = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		string(record.task.Status),
		toIDPtrParam(record.task.HostID),
		record.configJSON,
		record.task.RepositoryPath,
		record.task.BaseRef,
		sqlNullString(record.task.WorktreePath),
		nullIfBlank(record.task.ContainerID),
		toIDPtrParam(record.task.CurrentStepID),
		toIDPtrParam(record.task.CurrentAttemptID),
		toBoolInt(record.task.CancelRequested),
		toMillisPtr(record.task.CancelRequestedAt),
		nullIfBlank(string(record.task.FinalResult)),
		nullIfBlank(record.task.FailureSummary),
		nullIfBlank(record.task.IdempotencyKey),
		nullIfBlank(record.task.LeaseOwner),
		toMillisPtr(record.task.LeaseExpiresAt),
		toMillisPtr(record.task.StartedAt),
		toMillisPtr(record.task.FinishedAt),
		toMillis(record.task.UpdatedAt),
		toIDParam(record.task.ID),
	)
	if err != nil {
		return wrapError("更新任务", err)
	}
	return ensureAffected(result, "任务", task.ID)
}

// UpdateTaskIfStatus 在数据库中的状态仍属于 expected 时更新任务。
//
// 该条件更新是并发互斥的基础：过期执行者持有的旧状态不会覆盖更新的状态。
func (s taskStore) UpdateTaskIfStatus(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
) error {
	if len(expected) == 0 {
		return wrapError(
			"条件更新任务",
			errors.Join(storage.ErrInvalidData, errors.New("期望状态集合不能为空")),
		)
	}
	record, err := encodeTask(task)
	if err != nil {
		return err
	}
	conditions := make([]string, 0, len(expected))
	args := make([]any, 0, len(expected)+20)
	args = append(
		args,
		string(record.task.Status),
		toIDPtrParam(record.task.HostID),
		record.configJSON,
		record.task.RepositoryPath,
		record.task.BaseRef,
		sqlNullString(record.task.WorktreePath),
		nullIfBlank(record.task.ContainerID),
		toIDPtrParam(record.task.CurrentStepID),
		toIDPtrParam(record.task.CurrentAttemptID),
		toBoolInt(record.task.CancelRequested),
		toMillisPtr(record.task.CancelRequestedAt),
		nullIfBlank(string(record.task.FinalResult)),
		nullIfBlank(record.task.FailureSummary),
		nullIfBlank(record.task.IdempotencyKey),
		nullIfBlank(record.task.LeaseOwner),
		toMillisPtr(record.task.LeaseExpiresAt),
		toMillisPtr(record.task.StartedAt),
		toMillisPtr(record.task.FinishedAt),
		toMillis(record.task.UpdatedAt),
		toIDParam(record.task.ID),
	)
	for _, status := range expected {
		conditions = append(conditions, "?")
		args = append(args, string(status))
	}
	statement := `
UPDATE tasks SET
	status = ?, host_id = ?, config_json = ?, repository_path = ?, base_ref = ?,
	worktree_path = ?, container_id = ?, current_step_id = ?, current_attempt_id = ?,
	cancel_requested = ?, cancel_requested_at = ?, final_result = ?, failure_summary = ?,
	idempotency_key = ?, lease_owner = ?, lease_expires_at = ?, started_at = ?,
	finished_at = ?, updated_at = ?
WHERE id = ? AND status IN (` + strings.Join(conditions, ", ") + `)`
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return wrapError("条件更新任务", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("条件更新任务", err)
	}
	if affected == 0 {
		return wrapError(
			"条件更新任务",
			errors.Join(
				storage.ErrConflict,
				errors.New("任务状态已变更，拒绝过期更新: "+task.ID.String()),
			),
		)
	}
	return nil
}

// GetTask 读取单个任务。
func (s taskStore) GetTask(ctx context.Context, id domain.ID) (*domain.Task, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + taskColumns + ` FROM tasks WHERE id = ?`
	task, err := scanTask(s.q.QueryRowContext(ctx, statement, toIDParam(id)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("任务", id.String())
		}
		return nil, wrapError("读取任务", err)
	}
	return task, nil
}

// GetTaskByIdempotencyKey 按幂等键读取任务。
func (s taskStore) GetTaskByIdempotencyKey(
	ctx context.Context,
	key string,
) (*domain.Task, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return nil, notFoundError("任务", "幂等键为空")
	}
	const statement = `SELECT ` + taskColumns + ` FROM tasks WHERE idempotency_key = ?`
	task, err := scanTask(s.q.QueryRowContext(ctx, statement, trimmed))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("任务", trimmed)
		}
		return nil, wrapError("读取任务", err)
	}
	return task, nil
}

// ListTasks 按创建时间倒序返回任务。
func (s taskStore) ListTasks(ctx context.Context, filter storage.TaskFilter) ([]domain.Task, error) {
	statement, args := buildTaskQuery(filter)
	rows, err := s.q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, wrapError("查询任务", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	tasks := make([]domain.Task, 0, 8)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, wrapError("读取任务", err)
		}
		tasks = append(tasks, *task)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询任务", err)
	}
	return tasks, nil
}

// AcquireTaskLease 以原子条件更新领取任务租约。
//
// 条件同时覆盖任务状态与租约有效性：只有任务处于允许状态，且租约为空、
// 已过期或已由同一 owner 持有时才更新成功。
func (s taskStore) AcquireTaskLease(
	ctx context.Context,
	request storage.TaskLeaseRequest,
) error {
	if err := request.TaskID.Validate(); err != nil {
		return err
	}
	owner := strings.TrimSpace(request.Owner)
	if owner == "" {
		return wrapError(
			"领取任务租约",
			errors.Join(storage.ErrInvalidData, errors.New("执行者标识不能为空")),
		)
	}
	if request.TTL <= 0 {
		return wrapError(
			"领取任务租约",
			errors.Join(storage.ErrInvalidData, errors.New("租约有效期必须大于 0")),
		)
	}
	at := request.At
	if at.IsZero() {
		at = time.Now()
	}
	nowMillis := toMillis(at)
	expiresAt := toMillis(at.Add(request.TTL))
	statuses := request.Statuses
	if len(statuses) == 0 {
		// 默认可领取状态与 domain.TaskStatus.CanStart 保持一致：进程中断后停留在
		// 中间状态的任务也必须能被重新推进，否则恢复流程无法继续。
		statuses = []domain.TaskStatus{
			domain.TaskStatusQueued,
			domain.TaskStatusPreparing,
			domain.TaskStatusPlanned,
			domain.TaskStatusRetrying,
			domain.TaskStatusFailed,
			domain.TaskStatusPaused,
			domain.TaskStatusRecoveryRequired,
		}
	}
	conditions := make([]string, 0, len(statuses))
	args := make([]any, 0, len(statuses)+5)
	args = append(args, owner, expiresAt, nowMillis, toIDParam(request.TaskID))
	for _, status := range statuses {
		conditions = append(conditions, "?")
		args = append(args, string(status))
	}
	statement := `
UPDATE tasks SET lease_owner = ?, lease_expires_at = ?, updated_at = ?
WHERE id = ? AND status IN (` + strings.Join(conditions, ", ") + `)
  AND (
      lease_owner IS NULL
      OR lease_expires_at IS NULL
      OR lease_expires_at <= ?
      OR lease_owner = ?
  )`
	args = append(args, nowMillis, owner)
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return wrapError("领取任务租约", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("领取任务租约", err)
	}
	if affected == 0 {
		return wrapError(
			"领取任务租约",
			errors.Join(
				storage.ErrConflict,
				errors.New("任务状态不允许开始或租约仍被其他执行者持有: "+request.TaskID.String()),
			),
		)
	}
	return nil
}

// ReleaseTaskLease 释放由 owner 持有的租约。
func (s taskStore) ReleaseTaskLease(
	ctx context.Context,
	id domain.ID,
	owner string,
	at time.Time,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(owner)
	if trimmed == "" {
		return wrapError(
			"释放任务租约",
			errors.Join(storage.ErrInvalidData, errors.New("执行者标识不能为空")),
		)
	}
	const statement = `
UPDATE tasks SET lease_owner = NULL, lease_expires_at = NULL, updated_at = ?
WHERE id = ? AND lease_owner = ?`
	if _, err := s.q.ExecContext(
		ctx,
		statement,
		toMillis(at),
		toIDParam(id),
		trimmed,
	); err != nil {
		return wrapError("释放任务租约", err)
	}
	return nil
}

// buildTaskQuery 构造任务查询语句与参数。
func buildTaskQuery(filter storage.TaskFilter) (string, []any) {
	var builder strings.Builder
	builder.WriteString(`SELECT `)
	builder.WriteString(taskColumns)
	builder.WriteString(` FROM tasks`)
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 8)
	if !filter.RequirementID.IsEmpty() {
		conditions = append(conditions, `requirement_id = ?`)
		args = append(args, toIDParam(filter.RequirementID))
	}
	if !filter.HostID.IsEmpty() {
		conditions = append(conditions, `host_id = ?`)
		args = append(args, toIDParam(filter.HostID))
	}
	if len(filter.Statuses) > 0 {
		conditions = append(conditions, `status IN (`+placeholders(len(filter.Statuses))+`)`)
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if len(filter.ExcludeStatuses) > 0 {
		conditions = append(
			conditions,
			`status NOT IN (`+placeholders(len(filter.ExcludeStatuses))+`)`,
		)
		for _, status := range filter.ExcludeStatuses {
			args = append(args, string(status))
		}
	}
	appendConditions(&builder, conditions)
	builder.WriteString(` ORDER BY created_at DESC, rowid DESC`)
	if filter.Limit > 0 {
		builder.WriteString(` LIMIT ?`)
		args = append(args, filter.Limit)
	}
	return builder.String(), args
}

// encodeTask 校验任务并编码其 JSON 字段。
func encodeTask(task domain.Task) (taskRecord, error) {
	if err := task.Validate(); err != nil {
		return taskRecord{}, err
	}
	config := "{}"
	if !task.Config.IsZero() {
		encoded, err := marshalJSON("Task.config", task.Config)
		if err != nil {
			return taskRecord{}, err
		}
		config = encoded
	}
	return taskRecord{task: task, configJSON: config}, nil
}

// taskInsertArgs 构造写入任务的参数列表。
func taskInsertArgs(record taskRecord) []any {
	task := record.task
	return []any{
		toIDParam(task.ID),
		toIDParam(task.RequirementID),
		string(task.Status),
		toIDPtrParam(task.HostID),
		record.configJSON,
		task.RepositoryPath,
		task.BaseRef,
		sqlNullString(task.WorktreePath),
		nullIfBlank(task.ContainerID),
		toIDPtrParam(task.CurrentStepID),
		toIDPtrParam(task.CurrentAttemptID),
		toBoolInt(task.CancelRequested),
		toMillisPtr(task.CancelRequestedAt),
		nullIfBlank(string(task.FinalResult)),
		nullIfBlank(task.FailureSummary),
		nullIfBlank(task.IdempotencyKey),
		nullIfBlank(task.LeaseOwner),
		toMillisPtr(task.LeaseExpiresAt),
		toMillis(task.CreatedAt),
		toMillisPtr(task.StartedAt),
		toMillisPtr(task.FinishedAt),
		toMillis(task.UpdatedAt),
	}
}

// scanTask 从一行结果读取任务。
func scanTask(row scanner) (*domain.Task, error) {
	var (
		task              domain.Task
		rawID             string
		requirementID     string
		status            string
		hostID            sql.NullString
		configJSON        string
		worktreePath      sql.NullString
		containerID       sql.NullString
		currentStepID     sql.NullString
		currentAttemptID  sql.NullString
		cancelRequested   int
		cancelRequestedAt sql.NullInt64
		finalResult       sql.NullString
		failureSummary    sql.NullString
		idempotencyKey    sql.NullString
		leaseOwner        sql.NullString
		leaseExpiresAt    sql.NullInt64
		createdAt         int64
		startedAt         sql.NullInt64
		finishedAt        sql.NullInt64
		updatedAt         int64
	)
	err := row.Scan(
		&rawID,
		&requirementID,
		&status,
		&hostID,
		&configJSON,
		&task.RepositoryPath,
		&task.BaseRef,
		&worktreePath,
		&containerID,
		&currentStepID,
		&currentAttemptID,
		&cancelRequested,
		&cancelRequestedAt,
		&finalResult,
		&failureSummary,
		&idempotencyKey,
		&leaseOwner,
		&leaseExpiresAt,
		&createdAt,
		&startedAt,
		&finishedAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	task.ID = domain.ID(rawID)
	task.RequirementID = domain.ID(requirementID)
	task.Status = domain.TaskStatus(status)
	task.HostID = fromIDPtr(hostID)
	task.WorktreePath = worktreePath.String
	task.ContainerID = containerID.String
	task.CurrentStepID = fromIDPtr(currentStepID)
	task.CurrentAttemptID = fromIDPtr(currentAttemptID)
	task.CancelRequested = fromBoolInt(cancelRequested)
	task.CancelRequestedAt = fromMillisPtr(cancelRequestedAt)
	task.FinalResult = domain.TaskResult(finalResult.String)
	task.FailureSummary = failureSummary.String
	task.IdempotencyKey = idempotencyKey.String
	task.LeaseOwner = leaseOwner.String
	task.LeaseExpiresAt = fromMillisPtr(leaseExpiresAt)
	task.CreatedAt = fromMillis(createdAt)
	task.StartedAt = fromMillisPtr(startedAt)
	task.FinishedAt = fromMillisPtr(finishedAt)
	task.UpdatedAt = fromMillis(updatedAt)
	if err := unmarshalJSON("Task.config", configJSON, &task.Config); err != nil {
		return nil, err
	}
	if task.CurrentAttemptID != nil && task.CurrentStepID == nil {
		return nil, errors.New("sqlite: 任务 " + task.ID.String() + " 的尝试关联缺少步骤")
	}
	return &task, nil
}
