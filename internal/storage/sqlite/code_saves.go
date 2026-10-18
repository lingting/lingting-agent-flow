package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// codeSaveColumns 是代码保存表的完整列顺序。
const codeSaveColumns = `id, project_id, task_id, container_id, docker_id, attempt_id,
	remote_name, repository, task_branch, base_branch, commit_status, commit_sha,
	commit_message, push_status, remote_commit_sha, merge_enabled, merge_status,
	merge_sha, stage, error_code, error_summary, created_at, started_at,
	finished_at, updated_at`

// codeSaveInsertCount 是写入代码保存记录时的参数个数。
const codeSaveInsertCount = 25

// codeSaveStore 实现 storage.CodeSaveStore。
type codeSaveStore struct {
	q queryer
}

// CreateCodeSaveRecord 写入一条代码保存记录。
func (s codeSaveStore) CreateCodeSaveRecord(
	ctx context.Context,
	record domain.CodeSaveRecord,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	statement := `INSERT INTO code_saves (` + codeSaveColumns + `)
		VALUES (` + placeholders(codeSaveInsertCount) + `)`
	_, err := s.q.ExecContext(ctx, statement, codeSaveArgs(record)...)
	if err != nil {
		return wrapError("写入代码保存记录", err)
	}
	return nil
}

// UpdateCodeSaveRecord 按主键更新代码保存记录。
//
// 标识与创建时间一经写入不再修改；记录不存在时返回 storage.ErrNotFound。
func (s codeSaveStore) UpdateCodeSaveRecord(
	ctx context.Context,
	record domain.CodeSaveRecord,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE code_saves SET
	project_id = ?, docker_id = ?, attempt_id = ?, remote_name = ?, repository = ?,
	task_branch = ?, base_branch = ?, commit_status = ?, commit_sha = ?,
	commit_message = ?, push_status = ?, remote_commit_sha = ?, merge_enabled = ?,
	merge_status = ?, merge_sha = ?, stage = ?, error_code = ?, error_summary = ?,
	started_at = ?, finished_at = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		nullIfBlank(record.ProjectID),
		nullIfBlank(record.DockerID),
		nullIfBlank(record.AttemptID),
		record.RemoteName,
		nullIfBlank(record.Repository),
		record.TaskBranch,
		nullIfBlank(record.BaseBranch),
		record.CommitStatus.String(),
		nullIfBlank(record.CommitSHA),
		nullIfBlank(record.CommitMessage),
		record.PushStatus.String(),
		nullIfBlank(record.RemoteCommitSHA),
		toBoolInt(record.MergeEnabled),
		record.MergeStatus.String(),
		nullIfBlank(record.MergeSHA),
		nullIfBlank(record.Stage),
		nullIfBlank(record.ErrorCode),
		nullIfBlank(record.ErrorSummary),
		toMillisPtr(record.StartedAt),
		toMillisPtr(record.FinishedAt),
		toMillis(record.UpdatedAt),
		toIDParam(record.ID),
	)
	if err != nil {
		return wrapError("更新代码保存记录", err)
	}
	return ensureAffected(result, "代码保存记录", record.ID)
}

// codeSaveArgs 构造写入代码保存记录时的参数列表。
//
// 顺序必须与 codeSaveColumns 完全一致。
func codeSaveArgs(record domain.CodeSaveRecord) []any {
	return []any{
		toIDParam(record.ID),
		nullIfBlank(record.ProjectID),
		record.TaskID,
		record.ContainerID,
		nullIfBlank(record.DockerID),
		nullIfBlank(record.AttemptID),
		record.RemoteName,
		nullIfBlank(record.Repository),
		record.TaskBranch,
		nullIfBlank(record.BaseBranch),
		record.CommitStatus.String(),
		nullIfBlank(record.CommitSHA),
		nullIfBlank(record.CommitMessage),
		record.PushStatus.String(),
		nullIfBlank(record.RemoteCommitSHA),
		toBoolInt(record.MergeEnabled),
		record.MergeStatus.String(),
		nullIfBlank(record.MergeSHA),
		nullIfBlank(record.Stage),
		nullIfBlank(record.ErrorCode),
		nullIfBlank(record.ErrorSummary),
		toMillis(record.CreatedAt),
		toMillisPtr(record.StartedAt),
		toMillisPtr(record.FinishedAt),
		toMillis(record.UpdatedAt),
	}
}

// GetCodeSaveRecord 按主键读取代码保存记录。
func (s codeSaveStore) GetCodeSaveRecord(
	ctx context.Context,
	id domain.ID,
) (*domain.CodeSaveRecord, error) {
	statement := `SELECT ` + codeSaveColumns + ` FROM code_saves WHERE id = ?`
	row := s.q.QueryRowContext(ctx, statement, toIDParam(id))
	record, err := scanCodeSaveRecord(row)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// ListCodeSaveRecordsByTask 按任务列出代码保存记录，最新的在前。
func (s codeSaveStore) ListCodeSaveRecordsByTask(
	ctx context.Context,
	taskID string,
) ([]domain.CodeSaveRecord, error) {
	statement := `SELECT ` + codeSaveColumns + `
		FROM code_saves WHERE task_id = ? ORDER BY created_at DESC, id DESC`
	rows, err := s.q.QueryContext(ctx, statement, strings.TrimSpace(taskID))
	if err != nil {
		return nil, wrapError("查询代码保存记录", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	records := make([]domain.CodeSaveRecord, 0, 4)
	for rows.Next() {
		record, scanErr := scanCodeSaveRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询代码保存记录", err)
	}
	return records, nil
}

// scanCodeSaveRecord 扫描一行代码保存记录。
func scanCodeSaveRecord(row scanner) (domain.CodeSaveRecord, error) {
	var (
		id              string
		projectID       sql.NullString
		taskID          string
		containerID     string
		dockerID        sql.NullString
		attemptID       sql.NullString
		remoteName      string
		repository      sql.NullString
		taskBranch      string
		baseBranch      sql.NullString
		commitStatus    string
		commitSHA       sql.NullString
		commitMessage   sql.NullString
		pushStatus      string
		remoteCommitSHA sql.NullString
		mergeEnabled    int
		mergeStatus     string
		mergeSHA        sql.NullString
		stage           sql.NullString
		errorCode       sql.NullString
		errorSummary    sql.NullString
		createdAt       int64
		startedAt       sql.NullInt64
		finishedAt      sql.NullInt64
		updatedAt       int64
	)
	err := row.Scan(
		&id,
		&projectID,
		&taskID,
		&containerID,
		&dockerID,
		&attemptID,
		&remoteName,
		&repository,
		&taskBranch,
		&baseBranch,
		&commitStatus,
		&commitSHA,
		&commitMessage,
		&pushStatus,
		&remoteCommitSHA,
		&mergeEnabled,
		&mergeStatus,
		&mergeSHA,
		&stage,
		&errorCode,
		&errorSummary,
		&createdAt,
		&startedAt,
		&finishedAt,
		&updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.CodeSaveRecord{}, notFoundError("代码保存记录", id)
		}
		return domain.CodeSaveRecord{}, wrapError("读取代码保存记录", err)
	}
	return domain.CodeSaveRecord{
		ID:              domain.ID(id),
		ProjectID:       projectID.String,
		TaskID:          taskID,
		ContainerID:     containerID,
		DockerID:        dockerID.String,
		AttemptID:       attemptID.String,
		RemoteName:      remoteName,
		Repository:      repository.String,
		TaskBranch:      taskBranch,
		BaseBranch:      baseBranch.String,
		CommitStatus:    domain.CommitStatus(commitStatus),
		CommitSHA:       commitSHA.String,
		CommitMessage:   commitMessage.String,
		PushStatus:      domain.PushStatus(pushStatus),
		RemoteCommitSHA: remoteCommitSHA.String,
		MergeEnabled:    fromBoolInt(mergeEnabled),
		MergeStatus:     domain.MergeStatus(mergeStatus),
		MergeSHA:        mergeSHA.String,
		Stage:           stage.String,
		ErrorCode:       errorCode.String,
		ErrorSummary:    errorSummary.String,
		CreatedAt:       fromMillis(createdAt),
		StartedAt:       fromMillisPtr(startedAt),
		FinishedAt:      fromMillisPtr(finishedAt),
		UpdatedAt:       fromMillis(updatedAt),
	}, nil
}
