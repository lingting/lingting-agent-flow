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
	idempotency_key, lease_owner, lease_expires_at, deleted_at, created_at, started_at,
	finished_at, updated_at`

// taskInsertCount 是写入任务时的参数个数。
const taskInsertCount = 23

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
// requirement_id 与 host_id 作为普通文本保存，数据库不校验引用是否存在；
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
	idempotency_key = ?, lease_owner = ?, lease_expires_at = ?, deleted_at = ?,
	started_at = ?, finished_at = ?, updated_at = ?
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
		toMillisPtr(record.task.DeletedAt),
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
		toMillisPtr(record.task.DeletedAt),
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
	idempotency_key = ?, lease_owner = ?, lease_expires_at = ?, deleted_at = ?,
	started_at = ?, finished_at = ?, updated_at = ?
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

// ListTaskPage 分页返回任务并给出满足筛选条件的总数。
//
// 两条查询使用同一组筛选条件，总数与分页无关；已逻辑删除的记录默认不参与统计。
func (s taskStore) ListTaskPage(
	ctx context.Context,
	filter storage.TaskFilter,
) (storage.TaskPage, error) {
	page := storage.TaskPage{Tasks: []domain.Task{}}
	countStatement, countArgs := buildTaskCountQuery(filter)
	if err := s.q.QueryRowContext(ctx, countStatement, countArgs...).Scan(&page.Total); err != nil {
		return storage.TaskPage{}, wrapError("统计任务数量", err)
	}
	if page.Total == 0 {
		return page, nil
	}
	tasks, err := s.ListTasks(ctx, filter)
	if err != nil {
		return storage.TaskPage{}, err
	}
	page.Tasks = tasks
	return page, nil
}

// DeleteTask 逻辑删除任务。
//
// 只写入 deleted_at 与 updated_at：任务行、步骤、尝试、会话等历史记录全部保留。
// 条件同时覆盖"未被删除"与允许的状态集合，两个请求并发删除时只有一个成功。
func (s taskStore) DeleteTask(
	ctx context.Context,
	id domain.ID,
	expected []domain.TaskStatus,
	at time.Time,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if len(expected) == 0 {
		return wrapError(
			"删除任务",
			errors.Join(storage.ErrInvalidData, errors.New("允许删除的状态集合不能为空")),
		)
	}
	conditions := make([]string, 0, len(expected))
	args := make([]any, 0, len(expected)+3)
	args = append(args, toMillis(at), toMillis(at), toIDParam(id))
	for _, status := range expected {
		conditions = append(conditions, "?")
		args = append(args, string(status))
	}
	statement := `
UPDATE tasks SET deleted_at = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL
  AND status IN (` + strings.Join(conditions, ", ") + `)`
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return wrapError("删除任务", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("删除任务", err)
	}
	if affected == 0 {
		return wrapError(
			"删除任务",
			errors.Join(
				storage.ErrConflict,
				errors.New("任务已被删除或当前状态不允许删除: "+id.String()),
			),
		)
	}
	return nil
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

// taskFilterConditions 把筛选条件转换为 SQL 条件片段与绑定参数。
//
// 条件全部使用占位符，用户输入只通过参数绑定传入；已逻辑删除的任务默认排除。
func taskFilterConditions(filter storage.TaskFilter) ([]string, []any) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 8)
	if !filter.IncludeDeleted {
		conditions = append(conditions, `deleted_at IS NULL`)
	}
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
	return conditions, args
}

// buildTaskQuery 构造任务列表查询语句与参数。
//
// 排序固定为创建时间倒序、同一毫秒内按写入顺序倒序，保证分页结果稳定。
func buildTaskQuery(filter storage.TaskFilter) (string, []any) {
	conditions, args := taskFilterConditions(filter)
	var builder strings.Builder
	builder.WriteString(`SELECT `)
	builder.WriteString(taskColumns)
	builder.WriteString(` FROM tasks`)
	appendConditions(&builder, conditions)
	builder.WriteString(` ORDER BY created_at DESC, rowid DESC`)
	args = appendTaskPaging(&builder, args, filter)
	return builder.String(), args
}

// buildTaskCountQuery 构造任务总数查询语句与参数。
func buildTaskCountQuery(filter storage.TaskFilter) (string, []any) {
	conditions, args := taskFilterConditions(filter)
	var builder strings.Builder
	builder.WriteString(`SELECT COUNT(*) FROM tasks`)
	appendConditions(&builder, conditions)
	return builder.String(), args
}

// appendTaskPaging 按需追加分页子句。
func appendTaskPaging(builder *strings.Builder, args []any, filter storage.TaskFilter) []any {
	if filter.Limit > 0 {
		builder.WriteString(` LIMIT ?`)
		args = append(args, filter.Limit)
		if filter.Offset > 0 {
			builder.WriteString(` OFFSET ?`)
			args = append(args, filter.Offset)
		}
	}
	return args
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
		toMillisPtr(task.DeletedAt),
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
		deletedAt         sql.NullInt64
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
		&deletedAt,
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
	task.DeletedAt = fromMillisPtr(deletedAt)
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
