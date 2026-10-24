package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// containerColumns 是容器表的完整列顺序。
const containerColumns = `id, host_id, docker_id, name, env_type, env_name,
	env_version, build_config_sha256, image_id, task_id, attempt_id, state,
	docker_state, created_at, started_at, finished_at, end_reason, detached,
	detached_at, synced_at, error_summary, updated_at`

// containerInsertCount 是写入容器时的参数个数。
const containerInsertCount = 22

// containerStore 实现 storage.ContainerStore。
type containerStore struct {
	q queryer
}

// CreateContainer 写入容器登记记录。
//
// 同一节点内的容器名称唯一：重复名称返回 storage.ErrConflict，
// 避免同名容器被误认为同一实例。
func (s containerStore) CreateContainer(ctx context.Context, container domain.Container) error {
	if err := container.Validate(); err != nil {
		return err
	}
	statement := `INSERT INTO containers (` + containerColumns + `)
		VALUES (` + placeholders(containerInsertCount) + `)`
	_, err := s.q.ExecContext(ctx, statement, containerArgs(container)...)
	if err != nil {
		return wrapError("写入容器记录", err)
	}
	return nil
}

// UpdateContainer 按主键更新容器记录。
//
// 容器 ID 与创建时间一经写入不再修改；记录不存在时返回 storage.ErrNotFound。
//
// docker_id 必须可以被更新：容器登记记录在 `docker create` 之前写入，此时还没有
// Docker 容器 ID，MarkCreated 之后才拿到真实 ID。若更新语句忽略该列，登记记录会
// 永久停留在空值，后续所有容器内操作都会因为"记录中的 Docker 容器 ID 已变化"
// 而无法定位真实容器。
func (s containerStore) UpdateContainer(ctx context.Context, container domain.Container) error {
	if err := container.Validate(); err != nil {
		return err
	}
	statement := `
UPDATE containers SET
	docker_id = ?, state = ?, docker_state = ?, started_at = ?, finished_at = ?,
	end_reason = ?, detached = ?, detached_at = ?, synced_at = ?,
	error_summary = ?, updated_at = ?
WHERE id = ?`
	result, err := s.q.ExecContext(ctx, statement, containerUpdateArgs(container)...)
	if err != nil {
		return wrapError("更新容器记录", err)
	}
	return ensureAffected(result, "容器", container.ID)
}

// UpdateContainerIfState 在数据库中的管理状态仍属于 expected 时写入容器记录。
//
// 恢复与收敛路径必须使用本方法：两个实例同时核验同一容器时，只有先完成写入的一方
// 生效，后者按 storage.ErrConflict 返回并重新读取真实记录，而不是用本地副本覆盖
// 已经收敛的结论。
func (s containerStore) UpdateContainerIfState(
	ctx context.Context,
	container domain.Container,
	expected []domain.ContainerState,
) error {
	if err := container.Validate(); err != nil {
		return err
	}
	if len(expected) == 0 {
		return wrapError(
			"条件更新容器记录",
			errors.Join(storage.ErrInvalidData, errors.New("期望状态集合不能为空")),
		)
	}
	conditions := make([]string, 0, len(expected))
	args := containerUpdateArgs(container)
	for _, state := range expected {
		conditions = append(conditions, "?")
		args = append(args, state.String())
	}
	statement := `
UPDATE containers SET
	docker_id = ?, state = ?, docker_state = ?, started_at = ?, finished_at = ?,
	end_reason = ?, detached = ?, detached_at = ?, synced_at = ?,
	error_summary = ?, updated_at = ?
WHERE id = ? AND state IN (` + strings.Join(conditions, ", ") + `)`
	result, err := s.q.ExecContext(ctx, statement, args...)
	if err != nil {
		return wrapError("条件更新容器记录", err)
	}
	return ensureStatusAffected(result, "容器", container.ID)
}

// containerUpdateArgs 构造更新容器的参数列表。
//
// 顺序必须与 UpdateContainer / UpdateContainerIfState 的 SET 子句完全一致。
func containerUpdateArgs(container domain.Container) []any {
	return []any{
		nullIfBlank(container.DockerID),
		container.State.String(),
		container.DockerState.String(),
		toMillisPtr(container.StartedAt),
		toMillisPtr(container.FinishedAt),
		nullIfBlank(container.EndReason.String()),
		toBoolInt(container.Detached),
		toMillisPtr(container.DetachedAt),
		toMillisPtr(container.SyncedAt),
		nullIfBlank(container.ErrorSummary),
		toMillis(container.UpdatedAt),
		toIDParam(container.ID),
	}
}

// containerArgs 构造写入容器的参数列表。
//
// 顺序必须与 containerColumns 完全一致。
func containerArgs(container domain.Container) []any {
	return []any{
		toIDParam(container.ID),
		toIDParam(container.HostID),
		nullIfBlank(container.DockerID),
		container.Name,
		container.Environment.Type.String(),
		container.Environment.Name,
		container.EnvironmentVersion,
		container.BuildConfigSHA256,
		container.ImageID,
		toIDPtrParam(container.TaskID),
		toIDPtrParam(container.AttemptID),
		container.State.String(),
		container.DockerState.String(),
		toMillis(container.CreatedAt),
		toMillisPtr(container.StartedAt),
		toMillisPtr(container.FinishedAt),
		nullIfBlank(container.EndReason.String()),
		toBoolInt(container.Detached),
		toMillisPtr(container.DetachedAt),
		toMillisPtr(container.SyncedAt),
		nullIfBlank(container.ErrorSummary),
		toMillis(container.UpdatedAt),
	}
}

// GetContainer 按主键读取容器记录。
func (s containerStore) GetContainer(
	ctx context.Context,
	id domain.ID,
) (*domain.Container, error) {
	statement := `SELECT ` + containerColumns + ` FROM containers WHERE id = ?`
	row := s.q.QueryRowContext(ctx, statement, toIDParam(id))
	container, err := scanContainer(row)
	if err != nil {
		return nil, err
	}
	return &container, nil
}

// GetContainerByName 按节点与容器名称读取容器记录。
//
// 该方法用于远程操作结果不确定时按名称恢复本地状态，避免重复创建容器。
func (s containerStore) GetContainerByName(
	ctx context.Context,
	hostID domain.ID,
	name string,
) (*domain.Container, error) {
	statement := `SELECT ` + containerColumns + ` FROM containers WHERE host_id = ? AND name = ?`
	row := s.q.QueryRowContext(ctx, statement, toIDParam(hostID), name)
	container, err := scanContainer(row)
	if err != nil {
		return nil, err
	}
	return &container, nil
}

// ListContainers 按条件列出容器，按创建时间与标识升序返回。
func (s containerStore) ListContainers(
	ctx context.Context,
	filter domain.ContainerFilter,
) ([]domain.Container, error) {
	var builder strings.Builder
	builder.WriteString(`SELECT ` + containerColumns + ` FROM containers`)
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 8)
	if !filter.HostID.IsEmpty() {
		conditions = append(conditions, `host_id = ?`)
		args = append(args, filter.HostID.String())
	}
	if filter.TaskID != nil {
		conditions = append(conditions, `task_id = ?`)
		args = append(args, filter.TaskID.String())
	}
	if len(filter.States) > 0 {
		conditions = append(conditions, `state IN (`+placeholders(len(filter.States))+`)`)
		for _, state := range filter.States {
			args = append(args, state.String())
		}
	}
	if !filter.IncludeDetached {
		conditions = append(conditions, `detached = 0`)
	}
	if filter.Orphans {
		// 孤儿容器的判据只表达"登记了任务关联，但不是该任务当前使用的容器"：
		// 它既不推断容器是否可用，也不决定如何处置，避免在查询层混入业务规则。
		conditions = append(conditions, `task_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM tasks WHERE tasks.id = containers.task_id
				AND tasks.container_id = containers.id)`)
	}
	appendConditions(&builder, conditions)
	builder.WriteString(` ORDER BY created_at, id`)
	if filter.Limit > 0 {
		builder.WriteString(` LIMIT ?`)
		args = append(args, filter.Limit)
	}
	return s.queryContainers(ctx, builder.String(), args...)
}

// queryContainers 执行查询并扫描全部结果。
func (s containerStore) queryContainers(
	ctx context.Context,
	statement string,
	args ...any,
) ([]domain.Container, error) {
	rows, err := s.q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, wrapError("查询容器记录", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	containers := make([]domain.Container, 0, 8)
	for rows.Next() {
		container, scanErr := scanContainer(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		containers = append(containers, container)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询容器记录", err)
	}
	return containers, nil
}

// scanContainer 扫描一行容器记录。
func scanContainer(row scanner) (domain.Container, error) {
	var (
		id          string
		hostID      string
		dockerID    sql.NullString
		name        string
		envType     string
		envName     string
		envVersion  string
		buildConfig string
		imageID     string
		taskID      sql.NullString
		attemptID   sql.NullString
		state       string
		dockerState string
		createdAt   int64
		startedAt   sql.NullInt64
		finishedAt  sql.NullInt64
		endReason   sql.NullString
		detached    int
		detachedAt  sql.NullInt64
		syncedAt    sql.NullInt64
		errorText   sql.NullString
		updatedAt   int64
	)
	err := row.Scan(
		&id,
		&hostID,
		&dockerID,
		&name,
		&envType,
		&envName,
		&envVersion,
		&buildConfig,
		&imageID,
		&taskID,
		&attemptID,
		&state,
		&dockerState,
		&createdAt,
		&startedAt,
		&finishedAt,
		&endReason,
		&detached,
		&detachedAt,
		&syncedAt,
		&errorText,
		&updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Container{}, notFoundError("容器", id)
		}
		return domain.Container{}, wrapError("读取容器记录", err)
	}
	return domain.Container{
		ID:       domain.ID(id),
		HostID:   domain.ID(hostID),
		DockerID: dockerID.String,
		Name:     name,
		Environment: domain.EnvironmentRef{
			Type: domain.EnvironmentType(envType),
			Name: envName,
		},
		EnvironmentVersion: envVersion,
		BuildConfigSHA256:  buildConfig,
		ImageID:            imageID,
		TaskID:             fromIDPtr(taskID),
		AttemptID:          fromIDPtr(attemptID),
		State:              domain.ContainerState(state),
		DockerState:        domain.DockerContainerState(dockerState),
		CreatedAt:          fromMillis(createdAt),
		StartedAt:          fromMillisPtr(startedAt),
		FinishedAt:         fromMillisPtr(finishedAt),
		EndReason:          domain.ContainerEndReason(endReason.String),
		Detached:           fromBoolInt(detached),
		DetachedAt:         fromMillisPtr(detachedAt),
		SyncedAt:           fromMillisPtr(syncedAt),
		ErrorSummary:       errorText.String,
		UpdatedAt:          fromMillis(updatedAt),
	}, nil
}
