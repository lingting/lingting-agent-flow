package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// environmentVersionColumns 是环境版本表的完整列顺序。
const environmentVersionColumns = `host_id, env_type, env_name, version_number,
	dockerfile_sha256, build_config_sha256, context_sha256, parent_version,
	image_ref, image_id, build_status, error_summary, created_at, built_at`

// environmentVersionInsertCount 是写入环境版本时的参数个数。
const environmentVersionInsertCount = 14

// environmentVersionStore 实现 storage.EnvironmentVersionStore。
type environmentVersionStore struct {
	q queryer
}

// CreateEnvironmentVersion 写入一个不可覆盖的环境版本。
//
// 版本号在同一节点、同一环境内唯一：重复写入返回 storage.ErrConflict，
// 已保存的历史版本不会被覆盖。
func (s environmentVersionStore) CreateEnvironmentVersion(
	ctx context.Context,
	version domain.EnvironmentVersion,
) error {
	if err := version.Validate(); err != nil {
		return err
	}
	statement := `INSERT INTO environment_versions (` + environmentVersionColumns + `)
		VALUES (` + placeholders(environmentVersionInsertCount) + `)`
	_, err := s.q.ExecContext(ctx, statement, environmentVersionArgs(version)...)
	if err != nil {
		return wrapError("写入环境版本", err)
	}
	return nil
}

// UpdateEnvironmentVersion 更新环境版本的构建结果。
//
// 只更新构建状态、镜像 ID、构建时间、错误摘要与镜像引用；版本号、
// Dockerfile 哈希、构建配置哈希与上下文哈希一经写入不再修改。
func (s environmentVersionStore) UpdateEnvironmentVersion(
	ctx context.Context,
	version domain.EnvironmentVersion,
) error {
	if err := version.Validate(); err != nil {
		return err
	}
	const statement = `
UPDATE environment_versions SET
	image_ref = ?, image_id = ?, build_status = ?, error_summary = ?, built_at = ?
WHERE host_id = ? AND env_type = ? AND env_name = ? AND version_number = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		version.ImageRef,
		nullIfBlank(version.ImageID),
		version.Status.String(),
		nullIfBlank(version.ErrorSummary),
		toMillisPtr(version.BuiltAt),
		toIDParam(version.HostID),
		version.Ref.Type.String(),
		version.Ref.Name,
		version.Number,
	)
	if err != nil {
		return wrapError("更新环境版本", err)
	}
	return ensureAffected(result, "环境版本", domain.ID(version.Label()))
}

// environmentVersionArgs 构造写入环境版本的参数列表。
//
// 顺序必须与 environmentVersionColumns 完全一致。
func environmentVersionArgs(version domain.EnvironmentVersion) []any {
	return []any{
		toIDParam(version.HostID),
		version.Ref.Type.String(),
		version.Ref.Name,
		version.Number,
		version.DockerfileSHA256,
		version.BuildConfigSHA256,
		version.ContextSHA256,
		nullIfBlank(version.ParentVersion),
		version.ImageRef,
		nullIfBlank(version.ImageID),
		version.Status.String(),
		nullIfBlank(version.ErrorSummary),
		toMillis(version.CreatedAt),
		toMillisPtr(version.BuiltAt),
	}
}

// GetEnvironmentVersion 读取指定版本。
func (s environmentVersionStore) GetEnvironmentVersion(
	ctx context.Context,
	hostID domain.ID,
	ref domain.EnvironmentRef,
	number int,
) (*domain.EnvironmentVersion, error) {
	statement := `SELECT ` + environmentVersionColumns + ` FROM environment_versions
		WHERE host_id = ? AND env_type = ? AND env_name = ? AND version_number = ?`
	row := s.q.QueryRowContext(ctx, statement, hostID.String(), ref.Type.String(), ref.Name, number)
	version, err := scanEnvironmentVersion(row)
	if err != nil {
		return nil, err
	}
	return &version, nil
}

// ListEnvironmentVersions 按条件列出环境版本，按版本号升序返回。
func (s environmentVersionStore) ListEnvironmentVersions(
	ctx context.Context,
	filter storage.EnvironmentVersionFilter,
) ([]domain.EnvironmentVersion, error) {
	var builder strings.Builder
	builder.WriteString(`SELECT ` + environmentVersionColumns + ` FROM environment_versions`)
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 8)
	if !filter.HostID.IsEmpty() {
		conditions = append(conditions, `host_id = ?`)
		args = append(args, filter.HostID.String())
	}
	if filter.Ref != nil {
		conditions = append(conditions, `env_type = ?`)
		args = append(args, filter.Ref.Type.String())
		if filter.Ref.Name != "" {
			conditions = append(conditions, `env_name = ?`)
			args = append(args, filter.Ref.Name)
		}
	}
	if len(filter.Statuses) > 0 {
		conditions = append(conditions, `build_status IN (`+placeholders(len(filter.Statuses))+`)`)
		for _, status := range filter.Statuses {
			args = append(args, status.String())
		}
	}
	appendConditions(&builder, conditions)
	builder.WriteString(` ORDER BY host_id, env_type, env_name, version_number`)
	if filter.Limit > 0 {
		builder.WriteString(` LIMIT ?`)
		args = append(args, filter.Limit)
	}
	return s.queryEnvironmentVersions(ctx, builder.String(), args...)
}

// queryEnvironmentVersions 执行查询并扫描全部结果。
func (s environmentVersionStore) queryEnvironmentVersions(
	ctx context.Context,
	statement string,
	args ...any,
) ([]domain.EnvironmentVersion, error) {
	rows, err := s.q.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, wrapError("查询环境版本", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	versions := make([]domain.EnvironmentVersion, 0, 8)
	for rows.Next() {
		version, scanErr := scanEnvironmentVersion(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询环境版本", err)
	}
	return versions, nil
}

// NextEnvironmentVersionNumber 返回该环境下已用最大版本号加一。
func (s environmentVersionStore) NextEnvironmentVersionNumber(
	ctx context.Context,
	hostID domain.ID,
	ref domain.EnvironmentRef,
) (int, error) {
	const statement = `SELECT COALESCE(MAX(version_number), 0) FROM environment_versions
		WHERE host_id = ? AND env_type = ? AND env_name = ?`
	var highest int
	err := s.q.QueryRowContext(
		ctx,
		statement,
		hostID.String(),
		ref.Type.String(),
		ref.Name,
	).Scan(&highest)
	if err != nil {
		return 0, wrapError("读取环境版本号", err)
	}
	return domain.NextVersionNumber([]int{highest})
}

// scanEnvironmentVersion 扫描一行环境版本记录。
func scanEnvironmentVersion(row scanner) (domain.EnvironmentVersion, error) {
	var (
		hostID       string
		envType      string
		envName      string
		number       int
		dockerfile   string
		buildConfig  string
		contextHash  string
		parent       sql.NullString
		imageRef     string
		imageID      sql.NullString
		status       string
		errorSummary sql.NullString
		createdAt    int64
		builtAt      sql.NullInt64
	)
	err := row.Scan(
		&hostID,
		&envType,
		&envName,
		&number,
		&dockerfile,
		&buildConfig,
		&contextHash,
		&parent,
		&imageRef,
		&imageID,
		&status,
		&errorSummary,
		&createdAt,
		&builtAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.EnvironmentVersion{}, notFoundError("环境版本", envType+"/"+envName)
		}
		return domain.EnvironmentVersion{}, wrapError("读取环境版本", err)
	}
	version := domain.EnvironmentVersion{
		HostID: domain.ID(hostID),
		Ref: domain.EnvironmentRef{
			Type: domain.EnvironmentType(envType),
			Name: envName,
		},
		Number:            number,
		DockerfileSHA256:  dockerfile,
		BuildConfigSHA256: buildConfig,
		ContextSHA256:     contextHash,
		ParentVersion:     parent.String,
		ImageRef:          imageRef,
		ImageID:           imageID.String,
		Status:            domain.BuildStatus(status),
		ErrorSummary:      errorSummary.String,
		CreatedAt:         fromMillis(createdAt),
		BuiltAt:           fromMillisPtr(builtAt),
	}
	return version, nil
}
