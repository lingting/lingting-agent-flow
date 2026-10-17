package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// taskColumns 是任务表的完整列顺序。
const taskColumns = `id, requirement_id, status, repository_path, base_ref, worktree_path,
	current_step_id, created_at, started_at, finished_at, updated_at`

// taskStore 实现 storage.TaskStore。
type taskStore struct {
	q queryer
}

// CreateTask 写入任务。
//
// requirement_id 必须引用已存在的需求，否则返回 storage.ErrForeignKey。
func (s taskStore) CreateTask(ctx context.Context, task domain.Task) error {
	if err := task.Validate(); err != nil {
		return err
	}
	const statement = `INSERT INTO tasks (` + taskColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.q.ExecContext(
		ctx,
		statement,
		toIDParam(task.ID),
		toIDParam(task.RequirementID),
		string(task.Status),
		task.RepositoryPath,
		task.BaseRef,
		sqlNullString(task.WorktreePath),
		toIDPtrParam(task.CurrentStepID),
		toMillis(task.CreatedAt),
		toMillisPtr(task.StartedAt),
		toMillisPtr(task.FinishedAt),
		toMillis(task.UpdatedAt),
	)
	if err != nil {
		return wrapError("写入任务", err)
	}
	return nil
}

// UpdateTask 按主键更新任务。
func (s taskStore) UpdateTask(ctx context.Context, task domain.Task) error {
	if err := task.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE tasks SET
	status = ?, repository_path = ?, base_ref = ?, worktree_path = ?,
	current_step_id = ?, started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		string(task.Status),
		task.RepositoryPath,
		task.BaseRef,
		sqlNullString(task.WorktreePath),
		toIDPtrParam(task.CurrentStepID),
		toMillisPtr(task.StartedAt),
		toMillisPtr(task.FinishedAt),
		toMillis(task.UpdatedAt),
		toIDParam(task.ID),
	)
	if err != nil {
		return wrapError("更新任务", err)
	}
	return ensureAffected(result, "任务", task.ID)
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

// buildTaskQuery 构造任务查询语句与参数。
func buildTaskQuery(filter storage.TaskFilter) (string, []any) {
	var builder strings.Builder
	builder.WriteString(`SELECT `)
	builder.WriteString(taskColumns)
	builder.WriteString(` FROM tasks`)
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 4)
	if !filter.RequirementID.IsEmpty() {
		conditions = append(conditions, `requirement_id = ?`)
		args = append(args, toIDParam(filter.RequirementID))
	}
	if len(filter.Statuses) > 0 {
		conditions = append(conditions, `status IN (`+placeholders(len(filter.Statuses))+`)`)
		for _, status := range filter.Statuses {
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

// scanTask 从一行结果读取任务。
func scanTask(row scanner) (*domain.Task, error) {
	var (
		task          domain.Task
		rawID         string
		requirementID string
		status        string
		worktreePath  sql.NullString
		currentStepID sql.NullString
		createdAt     int64
		startedAt     sql.NullInt64
		finishedAt    sql.NullInt64
		updatedAt     int64
	)
	err := row.Scan(
		&rawID,
		&requirementID,
		&status,
		&task.RepositoryPath,
		&task.BaseRef,
		&worktreePath,
		&currentStepID,
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
	task.WorktreePath = worktreePath.String
	task.CurrentStepID = fromIDPtr(currentStepID)
	task.CreatedAt = fromMillis(createdAt)
	task.StartedAt = fromMillisPtr(startedAt)
	task.FinishedAt = fromMillisPtr(finishedAt)
	task.UpdatedAt = fromMillis(updatedAt)
	return &task, nil
}
