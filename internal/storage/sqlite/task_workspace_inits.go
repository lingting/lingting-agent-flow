package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// taskWorkspaceInitColumns 是任务级工作区初始化记录表的完整列顺序。
const taskWorkspaceInitColumns = `id, task_id, container_id, docker_id, source_type,
	repository, work_dir, base_branch, base_selection, base_commit, task_branch,
	task_branch_source, commit_sha, status, stage, error_code, error_summary,
	started_at, finished_at, created_at, updated_at`

// taskWorkspaceInitInsertCount 是写入记录时的参数个数。
const taskWorkspaceInitInsertCount = 21

// taskWorkspaceInitStore 实现 storage.TaskWorkspaceInitStore。
type taskWorkspaceInitStore struct {
	q queryer
}

// CreateTaskWorkspaceInit 写入一条任务级工作区初始化记录。
func (s taskWorkspaceInitStore) CreateTaskWorkspaceInit(
	ctx context.Context,
	record domain.TaskWorkspaceInitRecord,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	statement := `INSERT INTO task_workspace_initializations (` + taskWorkspaceInitColumns + `)
		VALUES (` + placeholders(taskWorkspaceInitInsertCount) + `)`
	if _, err := s.q.ExecContext(ctx, statement, taskWorkspaceInitArgs(record)...); err != nil {
		return wrapError("写入工作区初始化记录", err)
	}
	return nil
}

// UpdateTaskWorkspaceInit 按主键更新记录。
//
// 标识与创建时间一经写入不再修改；记录不存在时返回 storage.ErrNotFound。
// 已经进入终态的记录不会被重新打开：状态迁移由领域层状态机负责校验。
func (s taskWorkspaceInitStore) UpdateTaskWorkspaceInit(
	ctx context.Context,
	record domain.TaskWorkspaceInitRecord,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	statement := `
UPDATE task_workspace_initializations SET
	container_id = ?, docker_id = ?, source_type = ?, repository = ?, work_dir = ?,
	base_branch = ?, base_selection = ?, base_commit = ?, task_branch = ?,
	task_branch_source = ?, commit_sha = ?, status = ?, stage = ?, error_code = ?,
	error_summary = ?, started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(ctx, statement, taskWorkspaceInitUpdateArgs(record)...)
	if err != nil {
		return wrapError("更新工作区初始化记录", err)
	}
	return ensureAffected(result, "工作区初始化记录", record.ID)
}

// UpdateTaskWorkspaceInitIfStatus 在数据库中的状态仍属于 expected 时写入记录。
//
// 恢复与收敛路径必须使用本方法：`pending` / `running` 的记录可能被多个实例同时
// 观测，只有先完成写入的一方生效，后者按 storage.ErrConflict 返回并重新读取真实
// 记录。此前的无条件更新会让"冲突按已完成处理"的分支变成死代码，后写入者会覆盖
// 已经收敛的结论。
func (s taskWorkspaceInitStore) UpdateTaskWorkspaceInitIfStatus(
	ctx context.Context,
	record domain.TaskWorkspaceInitRecord,
	expected []domain.TaskWorkspaceInitStatus,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if len(expected) == 0 {
		return wrapError(
			"条件更新工作区初始化记录",
			errors.Join(storage.ErrInvalidData, errors.New("期望状态集合不能为空")),
		)
	}
	conditions := make([]string, 0, len(expected))
	args := taskWorkspaceInitUpdateArgs(record)
	for _, status := range expected {
		conditions = append(conditions, "?")
		args = append(args, status.String())
	}
	statement := `
UPDATE task_workspace_initializations SET
	container_id = ?, docker_id = ?, source_type = ?, repository = ?, work_dir = ?,
	base_branch = ?, base_selection = ?, base_commit = ?, task_branch = ?,
	task_branch_source = ?, commit_sha = ?, status = ?, stage = ?, error_code = ?,
	error_summary = ?, started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ? AND status IN (` + strings.Join(conditions, ", ") + `)`
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return wrapError("条件更新工作区初始化记录", err)
	}
	return ensureStatusAffected(result, "工作区初始化记录", record.ID)
}

// taskWorkspaceInitUpdateArgs 构造更新记录时的参数列表。
//
// 顺序必须与 UpdateTaskWorkspaceInit / UpdateTaskWorkspaceInitIfStatus 的 SET 子句一致。
func taskWorkspaceInitUpdateArgs(record domain.TaskWorkspaceInitRecord) []any {
	return []any{
		record.ContainerID,
		nullIfBlank(record.DockerID),
		record.SourceType.String(),
		nullIfBlank(record.Repository),
		record.WorkDir,
		nullIfBlank(record.BaseBranch),
		nullIfBlank(string(record.BaseSelection)),
		nullIfBlank(record.BaseCommit),
		record.TaskBranch,
		nullIfBlank(record.TaskBranchSource.String()),
		nullIfBlank(record.CommitSHA),
		record.Status.String(),
		nullIfBlank(record.Stage.String()),
		nullIfBlank(record.ErrorCode),
		nullIfBlank(record.ErrorSummary),
		toMillis(record.StartedAt),
		toMillisPtr(record.FinishedAt),
		toMillis(record.UpdatedAt),
		toIDParam(record.ID),
	}
}

// taskWorkspaceInitArgs 构造写入记录时的参数列表。
//
// 顺序必须与 taskWorkspaceInitColumns 完全一致。
func taskWorkspaceInitArgs(record domain.TaskWorkspaceInitRecord) []any {
	return []any{
		toIDParam(record.ID),
		record.TaskID,
		record.ContainerID,
		nullIfBlank(record.DockerID),
		record.SourceType.String(),
		nullIfBlank(record.Repository),
		record.WorkDir,
		nullIfBlank(record.BaseBranch),
		nullIfBlank(string(record.BaseSelection)),
		nullIfBlank(record.BaseCommit),
		record.TaskBranch,
		nullIfBlank(record.TaskBranchSource.String()),
		nullIfBlank(record.CommitSHA),
		record.Status.String(),
		nullIfBlank(record.Stage.String()),
		nullIfBlank(record.ErrorCode),
		nullIfBlank(record.ErrorSummary),
		toMillis(record.StartedAt),
		toMillisPtr(record.FinishedAt),
		toMillis(record.CreatedAt),
		toMillis(record.UpdatedAt),
	}
}

// GetTaskWorkspaceInit 按主键读取任务级工作区初始化记录。
func (s taskWorkspaceInitStore) GetTaskWorkspaceInit(
	ctx context.Context,
	id domain.ID,
) (*domain.TaskWorkspaceInitRecord, error) {
	statement := `SELECT ` + taskWorkspaceInitColumns + `
		FROM task_workspace_initializations WHERE id = ?`
	row := s.q.QueryRowContext(ctx, statement, toIDParam(id))
	record, err := scanTaskWorkspaceInit(row)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// ListTaskWorkspaceInitsByTask 按任务列出记录，最新的在前。
func (s taskWorkspaceInitStore) ListTaskWorkspaceInitsByTask(
	ctx context.Context,
	taskID string,
) ([]domain.TaskWorkspaceInitRecord, error) {
	return s.list(ctx, "task_id", taskID)
}

// ListTaskWorkspaceInitsByContainer 按容器列出记录，最新的在前。
func (s taskWorkspaceInitStore) ListTaskWorkspaceInitsByContainer(
	ctx context.Context,
	containerID string,
) ([]domain.TaskWorkspaceInitRecord, error) {
	return s.list(ctx, "container_id", containerID)
}

// ListInterruptedTaskWorkspaceInits 返回仍停留在 pending / running 的记录。
//
// 它只按状态过滤，不做任何推断：调用方据此把中断现场收敛为 uncertain，
// 不重放任何远程操作。
func (s taskWorkspaceInitStore) ListInterruptedTaskWorkspaceInits(
	ctx context.Context,
) ([]domain.TaskWorkspaceInitRecord, error) {
	const statement = `SELECT ` + taskWorkspaceInitColumns + `
		FROM task_workspace_initializations
		WHERE status IN (?, ?)
		ORDER BY created_at ASC, id ASC`
	rows, err := s.q.QueryContext(
		ctx,
		statement,
		domain.TaskWorkspaceInitStatusPending.String(),
		domain.TaskWorkspaceInitStatusRunning.String(),
	)
	if err != nil {
		return nil, wrapError("查询中断的工作区初始化记录", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	records := make([]domain.TaskWorkspaceInitRecord, 0, 4)
	for rows.Next() {
		record, scanErr := scanTaskWorkspaceInit(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询中断的工作区初始化记录", err)
	}
	return records, nil
}

// list 按给定列查询记录。
//
// 列名来自本文件内的固定常量，用户输入只通过参数绑定传入。
func (s taskWorkspaceInitStore) list(
	ctx context.Context,
	column, value string,
) ([]domain.TaskWorkspaceInitRecord, error) {
	statement := `SELECT ` + taskWorkspaceInitColumns + `
		FROM task_workspace_initializations
		WHERE ` + column + ` = ? ORDER BY created_at DESC, id DESC`
	rows, err := s.q.QueryContext(ctx, statement, strings.TrimSpace(value))
	if err != nil {
		return nil, wrapError("查询工作区初始化记录", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	records := make([]domain.TaskWorkspaceInitRecord, 0, 4)
	for rows.Next() {
		record, scanErr := scanTaskWorkspaceInit(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询工作区初始化记录", err)
	}
	return records, nil
}

// scanTaskWorkspaceInit 扫描一行任务级工作区初始化记录。
func scanTaskWorkspaceInit(row scanner) (domain.TaskWorkspaceInitRecord, error) {
	var (
		id               string
		taskID           string
		containerID      string
		dockerID         sql.NullString
		sourceType       string
		repository       sql.NullString
		workDir          string
		baseBranch       sql.NullString
		baseSelection    sql.NullString
		baseCommit       sql.NullString
		taskBranch       string
		taskBranchSource sql.NullString
		commitSHA        sql.NullString
		status           string
		stage            sql.NullString
		errorCode        sql.NullString
		errorSummary     sql.NullString
		startedAt        int64
		finishedAt       sql.NullInt64
		createdAt        int64
		updatedAt        int64
	)
	err := row.Scan(
		&id,
		&taskID,
		&containerID,
		&dockerID,
		&sourceType,
		&repository,
		&workDir,
		&baseBranch,
		&baseSelection,
		&baseCommit,
		&taskBranch,
		&taskBranchSource,
		&commitSHA,
		&status,
		&stage,
		&errorCode,
		&errorSummary,
		&startedAt,
		&finishedAt,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.TaskWorkspaceInitRecord{}, notFoundError("工作区初始化记录", id)
		}
		return domain.TaskWorkspaceInitRecord{}, wrapError("读取工作区初始化记录", err)
	}
	return domain.TaskWorkspaceInitRecord{
		ID:               domain.ID(id),
		TaskID:           taskID,
		ContainerID:      containerID,
		DockerID:         dockerID.String,
		SourceType:       domain.CodeSourceType(sourceType),
		Repository:       repository.String,
		WorkDir:          workDir,
		BaseBranch:       baseBranch.String,
		BaseSelection:    domain.GitBranchSelection(baseSelection.String),
		BaseCommit:       baseCommit.String,
		TaskBranch:       taskBranch,
		TaskBranchSource: domain.TaskBranchSource(taskBranchSource.String),
		CommitSHA:        commitSHA.String,
		Status:           domain.TaskWorkspaceInitStatus(status),
		Stage:            domain.CodeFetchStage(stage.String),
		ErrorCode:        errorCode.String,
		ErrorSummary:     errorSummary.String,
		StartedAt:        fromMillis(startedAt),
		FinishedAt:       fromMillisPtr(finishedAt),
		CreatedAt:        fromMillis(createdAt),
		UpdatedAt:        fromMillis(updatedAt),
	}, nil
}
