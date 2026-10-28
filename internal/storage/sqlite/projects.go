package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// projectColumns 是项目表的完整列顺序。
const projectColumns = `id, name, repository_url, base_branch, env_type, env_name,
	env_version, work_dir, caches_json, created_at, updated_at, deleted_at`

// projectColumnCount 是项目表的列数，用于生成 INSERT 占位符。
const projectColumnCount = 12

// projectStore 实现 storage.ProjectStore。
//
// 项目标识由调用方给出并与缓存卷共享范围、远程管理工作目录名绑定，因此读取路径
// 不提供"按名称模糊匹配"之类的便利方法：写错标识必须立刻表现为不存在，而不是
// 悄悄匹配到另一条记录。
type projectStore struct {
	q queryer
}

// CreateProject 写入项目。
func (s projectStore) CreateProject(ctx context.Context, project domain.Project) error {
	if err := project.Validate(); err != nil {
		return err
	}
	caches, err := marshalSlice("Project.caches", project.Caches)
	if err != nil {
		return err
	}
	statement := `INSERT INTO projects (` + projectColumns + `)
		VALUES (` + placeholders(projectColumnCount) + `)`
	_, err = s.q.ExecContext(
		ctx,
		statement,
		project.ID,
		project.Name,
		project.RepositoryURL,
		project.BaseBranch,
		project.Environment.Type.String(),
		project.Environment.Name,
		nullIfBlank(project.EnvironmentVersion),
		project.EffectiveWorkDir(),
		caches,
		toMillis(project.CreatedAt),
		toMillis(project.UpdatedAt),
		toMillisPtr(project.DeletedAt),
	)
	if err != nil {
		return wrapError("写入项目", err)
	}
	return nil
}

// UpdateProject 按主键更新项目。
//
// 项目标识不可修改；记录不存在或已被逻辑删除时返回 storage.ErrNotFound，因为
// 更新一条已删除的项目没有业务意义。
func (s projectStore) UpdateProject(ctx context.Context, project domain.Project) error {
	if err := project.Validate(); err != nil {
		return err
	}
	caches, err := marshalSlice("Project.caches", project.Caches)
	if err != nil {
		return err
	}
	const statement = `
UPDATE projects SET
	name = ?, repository_url = ?, base_branch = ?, env_type = ?, env_name = ?,
	env_version = ?, work_dir = ?, caches_json = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		project.Name,
		project.RepositoryURL,
		project.BaseBranch,
		project.Environment.Type.String(),
		project.Environment.Name,
		nullIfBlank(project.EnvironmentVersion),
		project.EffectiveWorkDir(),
		caches,
		toMillis(project.UpdatedAt),
		project.ID,
	)
	if err != nil {
		return wrapError("更新项目", err)
	}
	return ensureAffected(result, "项目", domain.ID(project.ID))
}

// DeleteProject 逻辑删除项目。
//
// 只写 deleted_at：任务、代码获取与代码保存记录都保留指向该标识的历史事实，
// 物理删除会让这些记录指向不存在的项目。
func (s projectStore) DeleteProject(ctx context.Context, id domain.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	now := nowMillis()
	result, err := s.q.ExecContext(
		ctx,
		`UPDATE projects SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now,
		now,
		id.String(),
	)
	if err != nil {
		return wrapError("删除项目", err)
	}
	return ensureAffected(result, "项目", id)
}

// GetProject 读取单个项目。
//
// 已逻辑删除的项目按不存在处理：它已经不再是可以承接新任务的登记对象，返回它
// 会诱导调用方继续使用。
func (s projectStore) GetProject(ctx context.Context, id domain.ID) (*domain.Project, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	const statement = `SELECT ` + projectColumns + ` FROM projects
		WHERE id = ? AND deleted_at IS NULL`
	project, err := scanProject(s.q.QueryRowContext(ctx, statement, id.String()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFoundError("项目", id.String())
		}
		return nil, wrapError("读取项目", err)
	}
	return &project, nil
}

// ListProjects 按条件返回项目列表。
//
// 默认按创建时间升序返回，与任务列表的倒序不同：项目是配置对象，用户需要按建立
// 顺序浏览；同一毫秒内以标识兜底，保证结果稳定。
func (s projectStore) ListProjects(
	ctx context.Context,
	filter storage.ProjectFilter,
) ([]domain.Project, error) {
	var builder strings.Builder
	builder.WriteString(`SELECT ` + projectColumns + ` FROM projects`)
	var conditions []string
	if !filter.IncludeDeleted {
		conditions = append(conditions, `deleted_at IS NULL`)
	}
	appendConditions(&builder, conditions)
	builder.WriteString(` ORDER BY created_at ASC, id ASC`)
	rows, err := s.q.QueryContext(ctx, builder.String())
	if err != nil {
		return nil, wrapError("查询项目列表", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	projects := make([]domain.Project, 0, 16)
	for rows.Next() {
		project, scanErr := scanProject(rows)
		if scanErr != nil {
			return nil, wrapError("读取项目", scanErr)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询项目列表", err)
	}
	return projects, nil
}

// projectScanner 抽象单行与多行查询结果。
type projectScanner interface {
	Scan(dest ...any) error
}

// scanProject 从一行结果还原项目。
func scanProject(row projectScanner) (domain.Project, error) {
	var (
		id         string
		name       string
		repository string
		baseBranch string
		envType    string
		envName    string
		envVersion sql.NullString
		workDir    string
		caches     string
		createdAt  int64
		updatedAt  int64
		deletedAt  sql.NullInt64
	)
	if err := row.Scan(
		&id,
		&name,
		&repository,
		&baseBranch,
		&envType,
		&envName,
		&envVersion,
		&workDir,
		&caches,
		&createdAt,
		&updatedAt,
		&deletedAt,
	); err != nil {
		return domain.Project{}, err
	}
	// 环境类型按原值还原，不在读取路径做枚举校验：数据库不限定取值集合，未知取值
	// 必须能原样读回，不得被映射为默认值或被当作损坏数据丢弃。
	environment := domain.EnvironmentRef{
		Type: domain.EnvironmentType(envType),
		Name: envName,
	}
	var cacheTypes []string
	if err := unmarshalJSON("Project.caches", caches, &cacheTypes); err != nil {
		return domain.Project{}, err
	}
	return domain.Project{
		ProjectConfig: domain.ProjectConfig{
			ID:                 id,
			Name:               name,
			Environment:        environment,
			EnvironmentVersion: envVersion.String,
			WorkDir:            workDir,
			Caches:             cacheTypes,
			CreatedAt:          fromMillis(createdAt),
			UpdatedAt:          fromMillis(updatedAt),
		},
		RepositoryURL: repository,
		BaseBranch:    baseBranch,
		DeletedAt:     fromMillisPtr(deletedAt),
	}, nil
}
