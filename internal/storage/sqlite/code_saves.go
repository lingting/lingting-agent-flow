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

// codeSaveColumns 是代码保存表的完整列顺序。
const codeSaveColumns = `id, project_id, task_id, container_id, docker_id, attempt_id,
	remote_name, repository, task_branch, base_branch, commit_status, commit_sha,
	commit_message, push_status, remote_commit_sha, merge_enabled, merge_status,
	merge_sha, stage, error_code, error_summary, created_at, started_at,
	finished_at, updated_at, baseline_commit_sha, baseline_base_commit_sha,
	persist_status, persist_error, recovery_status`

// codeSaveInsertCount 是写入代码保存记录时的参数个数。
const codeSaveInsertCount = 30

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
	started_at = ?, finished_at = ?, updated_at = ?,
	baseline_commit_sha = ?, baseline_base_commit_sha = ?,
	persist_status = ?, persist_error = ?, recovery_status = ?
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
		nullIfBlank(record.BaselineCommitSHA),
		nullIfBlank(record.BaselineBaseCommitSHA),
		nullIfBlank(record.PersistStatus.String()),
		nullIfBlank(record.PersistError),
		nullIfBlank(record.RecoveryStatus.String()),
		toIDParam(record.ID),
	)
	if err != nil {
		return wrapError("更新代码保存记录", err)
	}
	return ensureAffected(result, "代码保存记录", record.ID)
}

// UpdateCodeSaveRecovery 以读取时的恢复结论为条件写入一次只读核验结论。
//
// 只更新 recovery_status 与 updated_at：核验流程不产生 Git 副作用，因此不得用一条
// 携带全部字段的 UPDATE 把读取之后被其他执行推进的提交、推送、合并状态覆盖回旧值。
//
// expected 是调用方读取记录时的取值（空值表示尚未核验）。条件不匹配时返回
// storage.ErrConflict，由调用方重新读取真实记录后再决定是否重新核验，而不是覆盖。
func (s codeSaveStore) UpdateCodeSaveRecovery(
	ctx context.Context,
	id domain.ID,
	expected domain.RecoveryStatus,
	status domain.RecoveryStatus,
	at time.Time,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := status.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE code_saves SET recovery_status = ?, updated_at = ?
WHERE id = ? AND COALESCE(recovery_status, '') = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		nullIfBlank(status.String()),
		toMillis(at),
		toIDParam(id),
		strings.TrimSpace(expected.String()),
	)
	if err != nil {
		return wrapError("写入代码保存核验结论", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("写入代码保存核验结论", err)
	}
	if affected > 0 {
		return nil
	}
	// 影响行数为 0 有两种可能：记录不存在，或恢复结论已被其他操作改写。
	// 两者都必须让调用方重新读取，不能当作写入成功。
	current, readErr := s.GetCodeSaveRecord(ctx, id)
	if readErr != nil {
		if errors.Is(readErr, storage.ErrNotFound) {
			return readErr
		}
		return wrapError("写入代码保存核验结论", readErr)
	}
	return wrapError(
		"写入代码保存核验结论",
		errors.Join(
			storage.ErrConflict,
			errors.New("恢复结论已被其他操作改写，当前值为 "+current.RecoveryStatus.String()),
		),
	)
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
		nullIfBlank(record.BaselineCommitSHA),
		nullIfBlank(record.BaselineBaseCommitSHA),
		nullIfBlank(record.PersistStatus.String()),
		nullIfBlank(record.PersistError),
		nullIfBlank(record.RecoveryStatus.String()),
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
		baselineCommit  sql.NullString
		baselineBase    sql.NullString
		persistStatus   sql.NullString
		persistError    sql.NullString
		recoveryStatus  sql.NullString
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
		&baselineCommit,
		&baselineBase,
		&persistStatus,
		&persistError,
		&recoveryStatus,
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
		// 空值保持为空：数据库里没有写过的字段不得被映射成"默认状态"。
		BaselineCommitSHA:     baselineCommit.String,
		BaselineBaseCommitSHA: baselineBase.String,
		PersistStatus:         domain.PersistStatus(persistStatus.String),
		PersistError:          persistError.String,
		RecoveryStatus:        domain.RecoveryStatus(recoveryStatus.String),
	}, nil
}
