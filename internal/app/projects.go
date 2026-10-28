package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 项目管理 API 的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。
var (
	// ErrProjectInvalidInput 表示项目请求参数不合法，例如名称为空、仓库地址非法。
	ErrProjectInvalidInput = errors.New("app: 项目请求参数非法")
	// ErrProjectNotFound 表示项目不存在，或已被逻辑删除。
	ErrProjectNotFound = errors.New("app: 项目不存在")
	// ErrProjectStateConflict 表示项目标识已被占用，或当前状态不允许该操作。
	ErrProjectStateConflict = errors.New("app: 项目状态不允许该操作")
)

// ProjectService 是项目管理的应用服务入口。
//
// 项目是任务必填的关联对象：任务从项目继承仓库地址、基础分支、语言环境、环境版本、
// 缓存类型与容器内工作目录。因此项目校验必须比任务更严格——项目写错会让所有基于
// 它的任务一起落在错误的仓库或缓存范围里。
type ProjectService struct {
	store  storage.Store
	logger *slog.Logger
	now    func() time.Time
}

// ProjectServiceOptions 是构造项目服务的输入。
type ProjectServiceOptions struct {
	Store  storage.Store
	Logger *slog.Logger
	Now    func() time.Time
}

// NewProjectService 构造项目服务。
func NewProjectService(options ProjectServiceOptions) (*ProjectService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("项目服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ProjectService{store: options.Store, logger: logger, now: now}, nil
}

// CreateProjectRequest 是创建项目的输入。
//
// 只包含非机密配置：仓库地址在这里已经不允许内嵌凭据，凭据一律不进入项目记录。
type CreateProjectRequest struct {
	// ID 是项目标识，必填；它同时是缓存共享范围键与远程管理工作目录名。
	ID string
	// Name 是项目名称，必填。
	Name string
	// RepositoryURL 是仓库地址，必填。
	RepositoryURL string
	// BaseBranch 是基础分支，必填。
	BaseBranch string
	// Environment 是语言环境名称，必填。
	Environment string
	// EnvironmentVersion 是锁定的环境版本标签，可空。
	EnvironmentVersion string
	// Caches 是允许挂载的缓存类型。
	Caches []string
	// WorkDir 是容器内项目工作目录，空值使用默认目录。
	WorkDir string
}

// toProjectInput 校验并归一化创建输入。
func (r CreateProjectRequest) toProjectInput() (domain.ProjectInput, error) {
	projectID := strings.ToLower(strings.TrimSpace(r.ID))
	if err := domain.RequireProjectID(projectID); err != nil {
		return domain.ProjectInput{}, fmt.Errorf("%w: %w", ErrProjectInvalidInput, err)
	}
	environment, err := domain.NewEnvironmentRef(
		domain.EnvironmentTypeLanguage,
		strings.TrimSpace(r.Environment),
	)
	if err != nil {
		return domain.ProjectInput{}, fmt.Errorf("%w: %w", ErrProjectInvalidInput, err)
	}
	return domain.ProjectInput{
		ID:                 projectID,
		Name:               strings.TrimSpace(r.Name),
		RepositoryURL:      strings.TrimSpace(r.RepositoryURL),
		BaseBranch:         strings.TrimSpace(r.BaseBranch),
		Environment:        environment,
		EnvironmentVersion: strings.TrimSpace(r.EnvironmentVersion),
		Caches:             r.Caches,
		WorkDir:            strings.TrimSpace(r.WorkDir),
	}, nil
}

// Create 校验并创建项目。
func (s *ProjectService) Create(
	ctx context.Context,
	request CreateProjectRequest,
) (domain.Project, error) {
	input, err := request.toProjectInput()
	if err != nil {
		return domain.Project{}, err
	}
	project, err := domain.NewProject(input, s.now())
	if err != nil {
		return domain.Project{}, fmt.Errorf("%w: %w", ErrProjectInvalidInput, err)
	}
	if err := s.store.CreateProject(ctx, project); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return domain.Project{}, fmt.Errorf(
				"%w: 项目标识 %s 已存在",
				ErrProjectStateConflict,
				project.ID,
			)
		}
		return domain.Project{}, fmt.Errorf("创建项目失败: %w", err)
	}
	return project, nil
}

// UpdateProjectRequest 是更新项目的输入。
//
// 全部字段按整体替换处理：调用方必须给出完整的项目配置，避免遗漏字段被误读成
// "保持原值"而悄悄清空缓存或目录。标识不在其中，项目标识不可修改。
type UpdateProjectRequest struct {
	// Name 是项目名称，必填。
	Name string
	// RepositoryURL 是仓库地址，必填。
	RepositoryURL string
	// BaseBranch 是基础分支，必填。
	BaseBranch string
	// Environment 是语言环境名称，必填。
	Environment string
	// EnvironmentVersion 是锁定的环境版本标签，可空。
	EnvironmentVersion string
	// Caches 是允许挂载的缓存类型。
	Caches []string
	// WorkDir 是容器内项目工作目录，空值使用默认目录。
	WorkDir string
}

// Update 按标识整体更新项目配置。
//
// 更新不会改写任何历史任务：任务在创建时已经把配置快照写入自身配置，项目变更只
// 影响之后新建的任务。
func (s *ProjectService) Update(
	ctx context.Context,
	projectID string,
	request UpdateProjectRequest,
) (domain.Project, error) {
	existing, err := s.Get(ctx, projectID)
	if err != nil {
		return domain.Project{}, err
	}
	environment, refErr := domain.NewEnvironmentRef(
		domain.EnvironmentTypeLanguage,
		strings.TrimSpace(request.Environment),
	)
	if refErr != nil {
		return domain.Project{}, fmt.Errorf("%w: %w", ErrProjectInvalidInput, refErr)
	}
	updated := existing
	updated.Name = strings.TrimSpace(request.Name)
	updated.RepositoryURL = strings.TrimSpace(request.RepositoryURL)
	updated.BaseBranch = strings.TrimSpace(request.BaseBranch)
	updated.Environment = environment
	updated.EnvironmentVersion = strings.TrimSpace(request.EnvironmentVersion)
	updated.Caches = request.Caches
	updated.WorkDir = strings.TrimSpace(request.WorkDir)
	updated.UpdatedAt = s.now()
	// 重新走一次领域构造，保证更新路径与创建路径使用同一套校验，不会因为绕过
	// NewProject 而写入一份创建时会被拒绝的配置。
	normalized, normalizeErr := domain.NewProject(domain.ProjectInput{
		ID:                 updated.ID,
		Name:               updated.Name,
		RepositoryURL:      updated.RepositoryURL,
		BaseBranch:         updated.BaseBranch,
		Environment:        updated.Environment,
		EnvironmentVersion: updated.EnvironmentVersion,
		Caches:             updated.Caches,
		WorkDir:            updated.WorkDir,
	}, updated.CreatedAt)
	if normalizeErr != nil {
		return domain.Project{}, fmt.Errorf("%w: %w", ErrProjectInvalidInput, normalizeErr)
	}
	normalized.UpdatedAt = updated.UpdatedAt
	if err := s.store.UpdateProject(ctx, normalized); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
		}
		return domain.Project{}, fmt.Errorf("更新项目失败: %w", err)
	}
	return normalized, nil
}

// Delete 逻辑删除项目。
//
// 删除只标记 deleted_at：历史任务、代码获取与代码保存记录仍然指向该标识，任务
// 自身的配置快照也保持不变，因此删除项目不会破坏任何历史事实，只是不再允许新的
// 任务关联它。
func (s *ProjectService) Delete(ctx context.Context, projectID string) error {
	project, err := s.Get(ctx, projectID)
	if err != nil {
		return err
	}
	if err := s.store.DeleteProject(ctx, domain.ID(project.ID)); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
		}
		return fmt.Errorf("删除项目失败: %w", err)
	}
	return nil
}

// Get 读取单个未删除的项目。
func (s *ProjectService) Get(ctx context.Context, projectID string) (domain.Project, error) {
	trimmed := strings.TrimSpace(projectID)
	if err := domain.ValidateProjectID(trimmed); err != nil {
		return domain.Project{}, fmt.Errorf("%w: %w", ErrProjectInvalidInput, err)
	}
	if trimmed == "" {
		return domain.Project{}, fmt.Errorf("%w: 项目标识不能为空", ErrProjectInvalidInput)
	}
	project, err := s.store.GetProject(ctx, domain.ID(trimmed))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, trimmed)
		}
		return domain.Project{}, fmt.Errorf("读取项目失败: %w", err)
	}
	return *project, nil
}

// List 返回全部未删除的项目。
//
// 项目数量有限，不需要分页；响应仍固定使用 records 包装，与角色、执行节点列表一致。
func (s *ProjectService) List(ctx context.Context) ([]domain.Project, error) {
	projects, err := s.store.ListProjects(ctx, storage.ProjectFilter{})
	if err != nil {
		return nil, fmt.Errorf("查询项目列表失败: %w", err)
	}
	return projects, nil
}

// resolveTaskProject 读取任务必须关联的项目。
//
// 任务创建的唯一项目入口：标识必须给出、必须指向一条真实存在且未删除的记录。
// 项目不存在时返回 ErrTaskInvalidInput 而不是 ErrProjectNotFound——对建任务请求
// 而言这是参数错误（写错标识），不是"要操作的项目不存在"。
func resolveTaskProject(
	ctx context.Context,
	store storage.Store,
	projectID string,
) (domain.Project, error) {
	trimmed := strings.ToLower(strings.TrimSpace(projectID))
	if err := domain.RequireProjectID(trimmed); err != nil {
		return domain.Project{}, fmt.Errorf("%w: %w", ErrTaskInvalidInput, err)
	}
	project, err := store.GetProject(ctx, domain.ID(trimmed))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Project{}, fmt.Errorf(
				"%w: 项目 %s 不存在或已删除",
				ErrTaskInvalidInput,
				trimmed,
			)
		}
		return domain.Project{}, fmt.Errorf("创建任务失败: 读取项目失败: %w", err)
	}
	return *project, nil
}
