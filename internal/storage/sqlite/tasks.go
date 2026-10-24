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
	idempotency_key, lease_owner, lease_expires_at, lease_epoch, deleted_at, created_at,
	started_at, finished_at, updated_at`

// taskInsertCount 是写入任务时的参数个数。
const taskInsertCount = 24

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
	idempotency_key = ?, lease_owner = ?, lease_expires_at = ?, lease_epoch = ?,
	deleted_at = ?, started_at = ?, finished_at = ?, updated_at = ?
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
		record.task.LeaseEpoch,
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
		record.task.LeaseEpoch,
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
	idempotency_key = ?, lease_owner = ?, lease_expires_at = ?, lease_epoch = ?,
	deleted_at = ?, started_at = ?, finished_at = ?, updated_at = ?
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

// AcquireTaskLease 以原子条件更新领取任务租约，并返回本次领取的代次。
//
// 语义固定为：
//   - 条件是"任务未被删除 + 状态属于给定集合 + 租约空闲 / 已过期 / 已由同一 owner
//     持有"，三者由同一条 UPDATE 判定，因此并发领取最多只有一个成功；
//   - 状态集合为空时直接返回参数错误：默认集合会包含 failed / paused /
//     recovery_required，依赖它会让自动调度重放未经核验的现场；
//   - 领取成功时 lease_epoch 在同一语句内原子加一，旧代次的迟到写入因此被拒绝；
//   - 返回的 TaskLeaseHandle 是数据库确认过的所有权身份，调用方不得自行构造。
func (s taskStore) AcquireTaskLease(
	ctx context.Context,
	request storage.TaskLeaseRequest,
) (domain.TaskLeaseHandle, error) {
	if err := request.TaskID.Validate(); err != nil {
		return domain.TaskLeaseHandle{}, err
	}
	owner := strings.TrimSpace(request.Owner)
	if owner == "" {
		return domain.TaskLeaseHandle{}, wrapError(
			"领取任务租约",
			errors.Join(storage.ErrInvalidData, errors.New("执行者标识不能为空")),
		)
	}
	if request.TTL <= 0 {
		return domain.TaskLeaseHandle{}, wrapError(
			"领取任务租约",
			errors.Join(storage.ErrInvalidData, errors.New("租约有效期必须大于 0")),
		)
	}
	if len(request.Statuses) == 0 {
		// 可领取状态必须由调用方显式给出：默认值会包含 failed / paused /
		// recovery_required，使自动调度绕过 P2 的核验门控。
		return domain.TaskLeaseHandle{}, wrapError(
			"领取任务租约",
			errors.Join(
				storage.ErrInvalidData,
				errors.New("可领取状态集合不能为空，必须显式指定"),
			),
		)
	}
	at := request.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	nowMillis := toMillis(at)
	expiresAt := toMillis(at.Add(request.TTL))
	conditions := make([]string, 0, len(request.Statuses))
	args := make([]any, 0, len(request.Statuses)+6)
	args = append(args, owner, expiresAt, nowMillis, toIDParam(request.TaskID))
	for _, status := range request.Statuses {
		conditions = append(conditions, "?")
		args = append(args, string(status))
	}
	statement := `
UPDATE tasks SET lease_owner = ?, lease_expires_at = ?, lease_epoch = lease_epoch + 1,
	updated_at = ?
WHERE id = ? AND deleted_at IS NULL AND status IN (` + strings.Join(conditions, ", ") + `)
  AND (
      lease_owner IS NULL
      OR lease_expires_at IS NULL
      OR lease_expires_at <= ?
      OR lease_owner = ?
  )`
	args = append(args, nowMillis, owner)
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return domain.TaskLeaseHandle{}, wrapError("领取任务租约", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.TaskLeaseHandle{}, wrapError("领取任务租约", err)
	}
	if affected == 0 {
		return domain.TaskLeaseHandle{}, wrapError(
			"领取任务租约",
			errors.Join(
				storage.ErrConflict,
				errors.New("任务状态不允许开始或租约仍被其他执行者持有: "+request.TaskID.String()),
			),
		)
	}
	epoch, err := s.readLeaseEpoch(ctx, request.TaskID, owner)
	if err != nil {
		return domain.TaskLeaseHandle{}, err
	}
	return domain.TaskLeaseHandle{TaskID: request.TaskID, Owner: owner, Epoch: epoch}, nil
}

// readLeaseEpoch 读取刚写入的领取代次。
//
// 代次由数据库在同一语句内递增，调用方无法预先知道结果，因此必须回读。条件同时
// 覆盖 owner 与租约有效期：若在读取之前租约已被他人接管，回读不会命中，此时领取
// 按失败处理，绝不返回一个不属于本次调用的代次。
func (s taskStore) readLeaseEpoch(
	ctx context.Context,
	id domain.ID,
	owner string,
) (int64, error) {
	const statement = `
SELECT lease_epoch FROM tasks
WHERE id = ? AND lease_owner = ? AND lease_expires_at IS NOT NULL`
	var epoch int64
	err := s.q.QueryRowContext(ctx, statement, toIDParam(id), owner).Scan(&epoch)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, wrapError(
				"读取领取代次",
				errors.Join(
					storage.ErrConflict,
					errors.New("领取成功后租约已被接管: "+id.String()),
				),
			)
		}
		return 0, wrapError("读取领取代次", err)
	}
	if epoch < 1 {
		return 0, wrapError(
			"读取领取代次",
			errors.Join(storage.ErrInvalidData, errors.New("领取代次必须大于 0")),
		)
	}
	return epoch, nil
}

// RenewTaskLease 以 owner + epoch 为条件延长任务租约。
//
// 只有"仍是同一代次、仍在有效期内、任务未被删除"才允许续期：租约一旦过期，
// 旧持有者就不得通过迟到续期把执行权抢回来，必须由新的原子领取建立新代次。
// 影响行数为 0 返回 storage.ErrConflict，调用方必须按"执行权已丢失"处理。
func (s taskStore) RenewTaskLease(
	ctx context.Context,
	request storage.TaskLeaseRenewRequest,
) error {
	if err := request.Handle.Validate(); err != nil {
		return err
	}
	if request.TTL <= 0 {
		return wrapError(
			"续期任务租约",
			errors.Join(storage.ErrInvalidData, errors.New("租约有效期必须大于 0")),
		)
	}
	at := request.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	nowMillis := toMillis(at)
	expiresAt := toMillis(at.Add(request.TTL))
	const statement = `
UPDATE tasks SET lease_expires_at = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL
  AND lease_owner = ? AND lease_epoch = ?
  AND lease_expires_at IS NOT NULL AND lease_expires_at > ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		expiresAt,
		nowMillis,
		toIDParam(request.Handle.TaskID),
		request.Handle.Owner,
		request.Handle.Epoch,
		nowMillis,
	)
	if err != nil {
		return wrapError("续期任务租约", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("续期任务租约", err)
	}
	if affected == 0 {
		return wrapError(
			"续期任务租约",
			errors.Join(
				storage.ErrConflict,
				errors.New("租约已过期或已转移到其他代次: "+request.Handle.TaskID.String()),
			),
		)
	}
	return nil
}

// ReleaseTaskLease 释放由指定租约身份持有的任务租约。
//
// 条件同时覆盖 owner 与 epoch：旧代次无法释放新持有者的租约。没有命中任何记录时
// 返回 storage.ErrConflict 与 false，调用方不得据此认为名额已经空出。
func (s taskStore) ReleaseTaskLease(
	ctx context.Context,
	handle domain.TaskLeaseHandle,
	at time.Time,
) (bool, error) {
	if err := handle.Validate(); err != nil {
		return false, err
	}
	const statement = `
UPDATE tasks SET lease_owner = NULL, lease_expires_at = NULL, updated_at = ?
WHERE id = ? AND lease_owner = ? AND lease_epoch = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		toMillis(at),
		toIDParam(handle.TaskID),
		handle.Owner,
		handle.Epoch,
	)
	if err != nil {
		return false, wrapError("释放任务租约", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, wrapError("释放任务租约", err)
	}
	if affected == 0 {
		return false, wrapError(
			"释放任务租约",
			errors.Join(
				storage.ErrConflict,
				errors.New("租约已不属于本次领取: "+handle.TaskID.String()),
			),
		)
	}
	return true, nil
}

// UpdateTaskLeaseGuard 在任务租约仍由 handle 持有且状态属于 expected 时写入任务。
//
// 它是执行期状态写入的 fencing 落点：条件同时覆盖状态、owner 与 epoch，因此旧代次
// 的迟到写入无法覆盖新领取者的记录。与 UpdateTaskIfStatus 的分工是：
//   - 执行者推进自己领取的任务 → 本方法（要求仍持有执行权）；
//   - 用户或恢复流程推进任务（取消、删除、启动恢复收敛）→ UpdateTaskIfStatus。
func (s taskStore) UpdateTaskLeaseGuard(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
	handle domain.TaskLeaseHandle,
) error {
	if err := handle.Validate(); err != nil {
		return err
	}
	if len(expected) == 0 {
		return wrapError(
			"条件更新任务（租约栅栏）",
			errors.Join(storage.ErrInvalidData, errors.New("期望状态集合不能为空")),
		)
	}
	record, err := encodeTask(task)
	if err != nil {
		return err
	}
	conditions := make([]string, 0, len(expected))
	args := make([]any, 0, len(expected)+25)
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
		record.task.LeaseEpoch,
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
	args = append(args, handle.Owner, handle.Epoch)
	statement := `
UPDATE tasks SET
	status = ?, host_id = ?, config_json = ?, repository_path = ?, base_ref = ?,
	worktree_path = ?, container_id = ?, current_step_id = ?, current_attempt_id = ?,
	cancel_requested = ?, cancel_requested_at = ?, final_result = ?, failure_summary = ?,
	idempotency_key = ?, lease_owner = ?, lease_expires_at = ?, lease_epoch = ?,
	deleted_at = ?, started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ? AND status IN (` + strings.Join(conditions, ", ") + `)
  AND lease_owner = ? AND lease_epoch = ?`
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return wrapError("条件更新任务（租约栅栏）", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("条件更新任务（租约栅栏）", err)
	}
	if affected == 0 {
		return wrapError(
			"条件更新任务（租约栅栏）",
			errors.Join(
				storage.ErrConflict,
				errors.New("任务状态已变更或执行权已转移，拒绝过期更新: "+task.ID.String()),
			),
		)
	}
	return nil
}

// AppendTaskLeaseEvent 在一个事务内追加领取或释放事件。
//
// 事件与租约写入必须同事务：领取成功却没有事件会让"谁在何时领取"无法追溯，
// 而事件写入失败又会让数据库里留下一个没有审计痕迹的有效租约。事件写入失败时
// 整体回滚，因此不存在"看起来领取成功"的中间状态。
func (s taskStore) AppendTaskLeaseEvent(
	ctx context.Context,
	event domain.Event,
) (domain.Event, error) {
	withTx, ok := s.q.(transactioner)
	if !ok {
		// 已在事务内：复用当前事务，原子性由调用方的事务边界保证。
		return eventStore{q: s.q}.AppendEvent(ctx, event)
	}
	var stored domain.Event
	err := withTx.runInTx(ctx, func(tx queryer) error {
		written, writeErr := eventStore{q: tx}.AppendEvent(ctx, event)
		if writeErr != nil {
			return writeErr
		}
		stored = written
		return nil
	})
	if err != nil {
		return domain.Event{}, err
	}
	return stored, nil
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
		task.LeaseEpoch,
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
		leaseEpoch        int64
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
		&leaseEpoch,
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
	task.LeaseEpoch = leaseEpoch
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
