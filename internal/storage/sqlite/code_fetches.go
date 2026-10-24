package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// codeFetchColumns 是代码初始化记录表的完整列顺序。
const codeFetchColumns = `id, project_id, task_id, step_id, attempt_id, source_type,
	repository, work_dir, container_id, docker_id, base_branch, base_selection,
	base_commit, task_branch, task_branch_source, commit_sha, status, stage,
	error_code, error_summary, started_at, finished_at, created_at, updated_at`

// codeFetchInsertCount 是写入代码初始化记录时的参数个数。
const codeFetchInsertCount = 24

// codeFetchStore 实现 storage.CodeFetchStore。
type codeFetchStore struct {
	q queryer
}

// CreateCodeFetchRecord 写入一条代码初始化记录。
func (s codeFetchStore) CreateCodeFetchRecord(
	ctx context.Context,
	record domain.CodeFetchRecord,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	statement := `INSERT INTO code_fetches (` + codeFetchColumns + `)
		VALUES (` + placeholders(codeFetchInsertCount) + `)`
	if _, err := s.q.ExecContext(ctx, statement, codeFetchArgs(record)...); err != nil {
		return wrapError("写入代码初始化记录", err)
	}
	return nil
}

// UpdateCodeFetchRecord 按主键更新代码初始化记录。
//
// 标识与创建时间一经写入不再修改；记录不存在时返回 storage.ErrNotFound。
// 已经进入终态的记录不会被重新打开：状态迁移由领域层状态机负责校验。
func (s codeFetchStore) UpdateCodeFetchRecord(
	ctx context.Context,
	record domain.CodeFetchRecord,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	statement := `
UPDATE code_fetches SET
	project_id = ?, source_type = ?, repository = ?, work_dir = ?, container_id = ?,
	docker_id = ?, base_branch = ?, base_selection = ?, base_commit = ?,
	task_branch = ?, task_branch_source = ?, commit_sha = ?, status = ?, stage = ?,
	error_code = ?, error_summary = ?, started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(ctx, statement, codeFetchUpdateArgs(record)...)
	if err != nil {
		return wrapError("更新代码初始化记录", err)
	}
	return ensureAffected(result, "代码初始化记录", record.ID)
}

// UpdateCodeFetchRecordIfStatus 在数据库中的状态仍属于 expected 时写入记录。
//
// 恢复与收敛路径必须使用本方法：`pending` / `running` 的记录可能同时被多个实例
// 观测，只有先完成写入的一方生效，后者按 storage.ErrConflict 返回并重新读取真实
// 记录，避免用本地副本覆盖已经收敛的结论（例如覆盖掉迟到的 succeeded）。
func (s codeFetchStore) UpdateCodeFetchRecordIfStatus(
	ctx context.Context,
	record domain.CodeFetchRecord,
	expected []domain.CodeFetchStatus,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	if len(expected) == 0 {
		return wrapError(
			"条件更新代码初始化记录",
			errors.Join(storage.ErrInvalidData, errors.New("期望状态集合不能为空")),
		)
	}
	conditions := make([]string, 0, len(expected))
	args := codeFetchUpdateArgs(record)
	for _, status := range expected {
		conditions = append(conditions, "?")
		args = append(args, status.String())
	}
	statement := `
UPDATE code_fetches SET
	project_id = ?, source_type = ?, repository = ?, work_dir = ?, container_id = ?,
	docker_id = ?, base_branch = ?, base_selection = ?, base_commit = ?,
	task_branch = ?, task_branch_source = ?, commit_sha = ?, status = ?, stage = ?,
	error_code = ?, error_summary = ?, started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ? AND status IN (` + strings.Join(conditions, ", ") + `)`
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return wrapError("条件更新代码初始化记录", err)
	}
	return ensureStatusAffected(result, "代码初始化记录", record.ID)
}

// codeFetchUpdateArgs 构造更新代码初始化记录时的参数列表。
//
// 顺序必须与 UpdateCodeFetchRecord / UpdateCodeFetchRecordIfStatus 的 SET 子句一致。
func codeFetchUpdateArgs(record domain.CodeFetchRecord) []any {
	return []any{
		nullIfBlank(record.ProjectID),
		record.SourceType.String(),
		nullIfBlank(record.Repository),
		record.WorkDir,
		record.ContainerID,
		nullIfBlank(record.DockerID),
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

// codeFetchArgs 构造写入代码初始化记录时的参数列表。
//
// 顺序必须与 codeFetchColumns 完全一致。
func codeFetchArgs(record domain.CodeFetchRecord) []any {
	return []any{
		toIDParam(record.ID),
		nullIfBlank(record.ProjectID),
		record.TaskID,
		record.StepID,
		record.AttemptID,
		record.SourceType.String(),
		nullIfBlank(record.Repository),
		record.WorkDir,
		record.ContainerID,
		nullIfBlank(record.DockerID),
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

// GetCodeFetchRecord 按主键读取代码初始化记录。
func (s codeFetchStore) GetCodeFetchRecord(
	ctx context.Context,
	id domain.ID,
) (*domain.CodeFetchRecord, error) {
	statement := `SELECT ` + codeFetchColumns + ` FROM code_fetches WHERE id = ?`
	row := s.q.QueryRowContext(ctx, statement, toIDParam(id))
	record, err := scanCodeFetchRecord(row)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// ListCodeFetchRecordsByAttempt 按尝试列出代码初始化记录，最新的在前。
func (s codeFetchStore) ListCodeFetchRecordsByAttempt(
	ctx context.Context,
	attemptID string,
) ([]domain.CodeFetchRecord, error) {
	return s.list(ctx, "attempt_id", attemptID)
}

// ListCodeFetchRecordsByTask 按任务列出代码初始化记录，最新的在前。
func (s codeFetchStore) ListCodeFetchRecordsByTask(
	ctx context.Context,
	taskID string,
) ([]domain.CodeFetchRecord, error) {
	return s.list(ctx, "task_id", taskID)
}

// ListInterruptedCodeFetchRecords 返回仍停留在 pending / running 的记录。
//
// 它只按状态过滤，不做任何推断：调用方据此把中断现场收敛为 uncertain，不重放
// 任何容器内 Git 操作。与任务级工作区初始化不同，code_fetches 记录带 Attempt
// 归属，因此"是否仍有执行者"必须由调用方按任务执行权判定，查询层不承担该规则。
func (s codeFetchStore) ListInterruptedCodeFetchRecords(
	ctx context.Context,
) ([]domain.CodeFetchRecord, error) {
	statement := `SELECT ` + codeFetchColumns + ` FROM code_fetches
		WHERE status IN (?, ?)
		ORDER BY created_at ASC, id ASC`
	rows, err := s.q.QueryContext(
		ctx,
		statement,
		domain.CodeFetchStatusPending.String(),
		domain.CodeFetchStatusRunning.String(),
	)
	if err != nil {
		return nil, wrapError("查询中断的代码初始化记录", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	records := make([]domain.CodeFetchRecord, 0, 4)
	for rows.Next() {
		record, scanErr := scanCodeFetchRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询中断的代码初始化记录", err)
	}
	return records, nil
}

// 列名来自本文件内的固定常量，用户输入只通过参数绑定传入。
func (s codeFetchStore) list(
	ctx context.Context,
	column, value string,
) ([]domain.CodeFetchRecord, error) {
	statement := `SELECT ` + codeFetchColumns + ` FROM code_fetches
		WHERE ` + column + ` = ? ORDER BY created_at DESC, id DESC`
	rows, err := s.q.QueryContext(ctx, statement, strings.TrimSpace(value))
	if err != nil {
		return nil, wrapError("查询代码初始化记录", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	records := make([]domain.CodeFetchRecord, 0, 4)
	for rows.Next() {
		record, scanErr := scanCodeFetchRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询代码初始化记录", err)
	}
	return records, nil
}

// scanCodeFetchRecord 扫描一行代码初始化记录。
func scanCodeFetchRecord(row scanner) (domain.CodeFetchRecord, error) {
	var (
		id               string
		projectID        sql.NullString
		taskID           string
		stepID           string
		attemptID        string
		sourceType       string
		repository       sql.NullString
		workDir          string
		containerID      string
		dockerID         sql.NullString
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
		&projectID,
		&taskID,
		&stepID,
		&attemptID,
		&sourceType,
		&repository,
		&workDir,
		&containerID,
		&dockerID,
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
			return domain.CodeFetchRecord{}, notFoundError("代码初始化记录", id)
		}
		return domain.CodeFetchRecord{}, wrapError("读取代码初始化记录", err)
	}
	return domain.CodeFetchRecord{
		ID:               domain.ID(id),
		ProjectID:        projectID.String,
		TaskID:           taskID,
		StepID:           stepID,
		AttemptID:        attemptID,
		SourceType:       domain.CodeSourceType(sourceType),
		Repository:       repository.String,
		WorkDir:          workDir,
		ContainerID:      containerID,
		DockerID:         dockerID.String,
		BaseBranch:       baseBranch.String,
		BaseSelection:    domain.GitBranchSelection(baseSelection.String),
		BaseCommit:       baseCommit.String,
		TaskBranch:       taskBranch,
		TaskBranchSource: domain.TaskBranchSource(taskBranchSource.String),
		CommitSHA:        commitSHA.String,
		Status:           domain.CodeFetchStatus(status),
		Stage:            domain.CodeFetchStage(stage.String),
		ErrorCode:        errorCode.String,
		ErrorSummary:     errorSummary.String,
		StartedAt:        fromMillis(startedAt),
		FinishedAt:       fromMillisPtr(finishedAt),
		CreatedAt:        fromMillis(createdAt),
		UpdatedAt:        fromMillis(updatedAt),
	}, nil
}
