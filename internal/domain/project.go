package domain

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// DefaultContainerWorkDir 是任务容器内部的项目工作目录。
//
// 该目录只存在于容器自身的可写文件系统，不绑定挂载到远程宿主机目录；
// 后续 SourceProvider 在容器内执行 Git Clone 时把代码放在这里。
const DefaultContainerWorkDir = "/workspace/project"

// CacheScope 是依赖缓存的共享范围。
//
// 共享范围必须显式声明：不能因为卷名相似就默认让所有项目共用同一份缓存。
type CacheScope string

// 缓存共享范围取值。
const (
	// CacheScopeProject 表示缓存只在同一个项目内共享。
	CacheScopeProject CacheScope = "project"
	// CacheScopeMachine 表示缓存在同一台机器的所有项目间共享。
	CacheScopeMachine CacheScope = "machine"
)

// String 返回缓存共享范围的字符串形式。
func (s CacheScope) String() string {
	return string(s)
}

// Validate 校验缓存共享范围。
func (s CacheScope) Validate() error {
	switch s {
	case CacheScopeProject, CacheScopeMachine:
		return nil
	default:
		return invalidField("CacheMount", "scope", "未知的缓存共享范围 "+string(s))
	}
}

// CacheMount 是一个语言环境的依赖缓存挂载定义。
//
// 缓存一律使用带 AgentFlow 标签的 Docker Named Volume，不使用宿主机 bind mount。
// 缓存目录不能与项目工作目录重叠，避免项目代码被写入共享缓存卷。
type CacheMount struct {
	// Type 是缓存类型，例如 go-mod、node-npm。
	Type string `json:"type"`
	// ContainerPath 是容器内的缓存目录绝对路径。
	ContainerPath string `json:"container_path"`
	// Scope 是缓存共享范围。
	Scope CacheScope `json:"scope"`
	// ABI 是缓存的兼容标识，例如工具链主版本；变化时不得复用旧缓存。
	ABI string `json:"abi,omitempty"`
}

// Validate 校验缓存定义。
func (m CacheMount) Validate() error {
	if !EnvironmentNamePattern.MatchString(strings.TrimSpace(m.Type)) {
		return invalidField("CacheMount", "type", "缓存类型只能包含小写字母、数字与连字符")
	}
	if err := validateContainerPath("CacheMount", "container_path", m.ContainerPath); err != nil {
		return err
	}
	if IsReservedContainerPath(m.ContainerPath) || isWithinWorkspace(m.ContainerPath) {
		return invalidField(
			"CacheMount",
			"container_path",
			"缓存目录不能使用保留路径 "+m.ContainerPath,
		)
	}
	if err := m.Scope.Validate(); err != nil {
		return err
	}
	return nil
}

// ReservedContainerPaths 是禁止用于缓存挂载与项目目录的敏感容器路径。
//
// 这些路径是系统目录或宿主机敏感挂载点，缓存卷与项目工作目录一律不得占用。
var ReservedContainerPaths = []string{
	"/",
	"/root",
	"/home",
	"/etc",
	"/proc",
	"/sys",
	"/dev",
	"/var/run",
	"/var/run/docker.sock",
}

// WorkspaceContainerRoot 是容器内项目工作区的根目录。
//
// 该目录本身不可作为缓存挂载点或项目工作目录，但允许在其下创建项目目录，
// 例如 /workspace/project。
const WorkspaceContainerRoot = "/workspace"

// IsReservedContainerPath 判断路径是否落在保留路径之内（含自身）。
func IsReservedContainerPath(target string) bool {
	cleaned := path.Clean(target)
	for _, reserved := range ReservedContainerPaths {
		if cleaned == reserved || strings.HasPrefix(cleaned, reserved+"/") {
			return true
		}
	}
	return false
}

// isWithinWorkspace 判断路径是否位于项目工作区内。
//
// 项目代码目录不得与缓存卷混用，因此缓存挂载一律不能落在 /workspace 之下。
func isWithinWorkspace(target string) bool {
	cleaned := path.Clean(target)
	return cleaned == WorkspaceContainerRoot || strings.HasPrefix(cleaned, WorkspaceContainerRoot+"/")
}

// ValidateContainerWorkDir 校验容器内项目工作目录。
//
// 工作目录必须是 /workspace 之下的安全绝对路径：既不能落在保留的系统路径中，
// 也不能正好是 /workspace 本身。
func ValidateContainerWorkDir(workDir string) error {
	if err := validateContainerPath("Project", "work_dir", workDir); err != nil {
		return err
	}
	if IsReservedContainerPath(workDir) {
		return invalidField("Project", "work_dir", "项目工作目录不能使用保留路径")
	}
	if path.Clean(workDir) == WorkspaceContainerRoot {
		return invalidField("Project", "work_dir", "项目工作目录不能是 "+WorkspaceContainerRoot+" 本身")
	}
	if !strings.HasPrefix(path.Clean(workDir), WorkspaceContainerRoot+"/") {
		return invalidField(
			"Project",
			"work_dir",
			"项目工作目录必须位于 "+WorkspaceContainerRoot+" 之下",
		)
	}
	return nil
}

// validateContainerPath 校验容器内路径必须是安全的绝对 POSIX 路径。
func validateContainerPath(entity, field, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return invalidField(entity, field, "容器内路径不能为空")
	}
	if strings.ContainsRune(trimmed, '\\') {
		return invalidField(entity, field, "容器内路径必须使用 POSIX 分隔符 /")
	}
	for _, char := range trimmed {
		if char == 0 || char == '\n' || char == '\r' || char == '\t' {
			return invalidField(entity, field, "容器内路径不能包含控制字符")
		}
	}
	if !strings.HasPrefix(trimmed, "/") {
		return invalidField(entity, field, "容器内路径必须是绝对路径")
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return invalidField(entity, field, "容器内路径不能包含上级目录引用 ..")
		}
	}
	if path.Clean(trimmed) != trimmed {
		return invalidField(entity, field, "容器内路径必须已经规范化")
	}
	return nil
}

// ProjectConfig 是一个项目的环境配置。
//
// 这里只保存非机密元数据：项目源代码与 Git 工作区不在本配置中，也不保存在
// 远程项目配置目录。访问仓库所需的 Token、SSH 私钥、账号密码等凭据一律不写入
// 本配置，也不写入数据库、日志或仓库 URL。
//
// 需要在容器内执行 Git 操作（Clone、Push、Merge）时，凭据必须由项目所使用的
// 环境镜像自行注入：在环境 Dockerfile 或项目容器配置中通过 BuildKit secret、
// 构建期挂载的只读凭据文件等方式准备，并在容器内配置对应的 Git 认证（例如
// credential helper 或 core.sshCommand）。修改本配置或环境版本不会自动带上
// 凭据，凭据的注入范围由对应容器配置决定。
type ProjectConfig struct {
	// ID 是项目标识，同时作为远程目录名，因此只允许安全字符。
	ID string `json:"id"`
	// Name 是项目名称。
	Name string `json:"name"`
	// Environment 是项目使用的语言环境。
	Environment EnvironmentRef `json:"environment"`
	// EnvironmentVersion 是锁定的环境版本标签。
	EnvironmentVersion string `json:"environment_version"`
	// WorkDir 是容器内的项目工作目录。
	WorkDir string `json:"work_dir"`
	// Caches 是允许挂载的缓存类型。
	Caches []string `json:"caches"`
	// CreatedAt 是配置创建时间。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 是配置最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// NewProjectConfig 创建并校验项目配置。
func NewProjectConfig(
	id, name string,
	ref EnvironmentRef,
	versionLabel string,
	caches []string,
	at time.Time,
) (ProjectConfig, error) {
	createdAt := normalizeTime(at)
	config := ProjectConfig{
		ID:                 strings.ToLower(strings.TrimSpace(id)),
		Name:               strings.TrimSpace(name),
		Environment:        ref,
		EnvironmentVersion: strings.TrimSpace(versionLabel),
		WorkDir:            DefaultContainerWorkDir,
		Caches:             normalizeStringList(caches),
		CreatedAt:          createdAt,
		UpdatedAt:          createdAt,
	}
	if err := config.Validate(); err != nil {
		return ProjectConfig{}, err
	}
	return config, nil
}

// ValidateProjectID 校验项目标识的书写形式。
//
// 它只校验书写形式，空值视为合法：是否需要关联项目由调用方按业务规则决定，任务
// 创建使用 RequireProjectID 要求必须给出项目标识。
//
// 非空时与 ProjectConfig.ID 使用同一套书写规则，因为该标识同样会进入缓存卷名与
// 远程目录名。
func ValidateProjectID(id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil
	}
	if !EnvironmentNamePattern.MatchString(trimmed) {
		return invalidField("Project", "id", "项目标识只能包含小写字母、数字与连字符，且不超过 32 个字符")
	}
	return nil
}

// Validate 校验项目配置。
func (c ProjectConfig) Validate() error {
	if !EnvironmentNamePattern.MatchString(strings.TrimSpace(c.ID)) {
		return invalidField("Project", "id", "项目标识只能包含小写字母、数字与连字符")
	}
	if strings.TrimSpace(c.Name) == "" {
		return invalidField("Project", "name", "项目名称不能为空")
	}
	if err := c.Environment.Validate(); err != nil {
		return err
	}
	if c.Environment.IsBase() {
		return invalidField("Project", "environment", "项目必须使用语言环境")
	}
	// 环境版本可空：空值表示使用当前可用版本，与任务配置的语义一致。项目需要锁定
	// 具体版本时显式给出标签即可。
	if version := strings.TrimSpace(c.EnvironmentVersion); version != "" {
		if _, err := ParseVersionLabel(version); err != nil {
			return err
		}
	}
	if err := ValidateContainerWorkDir(c.WorkDir); err != nil {
		return err
	}
	for _, cacheType := range c.Caches {
		if !EnvironmentNamePattern.MatchString(cacheType) {
			return invalidField("Project", "caches", "缓存类型书写非法: "+cacheType)
		}
	}
	return nil
}

// normalizeStringList 去空白、去重并保持稳定顺序。
func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// SelectCaches 从语言环境的缓存定义中挑选项目允许挂载的缓存。
//
// 未在项目配置中列出的缓存不会挂载，避免默认共享不属于该项目的缓存。
func SelectCaches(available []CacheMount, allowed []string) ([]CacheMount, error) {
	if len(allowed) == 0 {
		return nil, nil
	}
	byType := make(map[string]CacheMount, len(available))
	for _, mount := range available {
		if err := mount.Validate(); err != nil {
			return nil, err
		}
		byType[mount.Type] = mount
	}
	selected := make([]CacheMount, 0, len(allowed))
	for _, cacheType := range allowed {
		mount, ok := byType[strings.TrimSpace(cacheType)]
		if !ok {
			return nil, fmt.Errorf(
				"%w: Project.caches: 语言环境未定义缓存类型 %s",
				ErrInvalidValue,
				cacheType,
			)
		}
		selected = append(selected, mount)
	}
	return selected, nil
}

// Project 是一个持久化的项目实体。
//
// 项目是本产品里"同一份代码、同一套执行环境"的登记处：仓库地址、基础分支与环境
// 配置在这里只写一次，任务创建时按标识继承，避免同一项目在多次建任务时出现不同
// 的仓库写法、分支、缓存范围或容器内工作目录。
//
// 项目标识（ID）同时是缓存卷的共享范围键与远程管理工作目录名，因此它必须指向一条
// 真实存在且未被删除的记录：写错标识会让同一项目的任务落在不同的缓存卷与目录里，
// 而系统无法自行发现这种分裂。
//
// 项目只保存非机密元数据，与 ProjectConfig 一致：凭据一律不进入本实体。
type Project struct {
	// ProjectConfig 是项目的环境配置部分（标识、名称、环境、版本、缓存、目录）。
	ProjectConfig
	// RepositoryURL 是已脱敏的仓库地址，内嵌凭据的地址在校验阶段被拒绝。
	RepositoryURL string `json:"repository_url"`
	// BaseBranch 是规范化后的基础分支名。
	BaseBranch string `json:"base_branch"`
	// DeletedAt 是逻辑删除时间，nil 表示未删除。
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// ProjectInput 是创建一个项目的输入。
//
// 它与 Project 的区别是只包含调用方可以决定的事实：标识、名称、仓库、分支与环境
// 配置；时间戳与逻辑删除标记由领域层与服务层填。
type ProjectInput struct {
	// ID 是项目标识。
	ID string
	// Name 是项目名称。
	Name string
	// RepositoryURL 是仓库地址。
	RepositoryURL string
	// BaseBranch 是基础分支。
	BaseBranch string
	// Environment 是项目使用的语言环境。
	Environment EnvironmentRef
	// EnvironmentVersion 是锁定的环境版本标签，空值表示使用当前可用版本。
	EnvironmentVersion string
	// Caches 是允许挂载的缓存类型。
	Caches []string
	// WorkDir 是容器内项目工作目录，空值使用默认目录。
	WorkDir string
}

// NewProject 创建并校验项目。
func NewProject(input ProjectInput, at time.Time) (Project, error) {
	config, err := NewProjectConfig(
		input.ID,
		input.Name,
		input.Environment,
		input.EnvironmentVersion,
		input.Caches,
		at,
	)
	if err != nil {
		return Project{}, err
	}
	// NewProjectConfig 只负责环境与目录的默认值，这里补上调用方显式给出的
	// 工作目录与规范化后的基础分支，使写入数据库的都是规范化取值。
	if workDir := strings.TrimSpace(input.WorkDir); workDir != "" {
		config.WorkDir = workDir
	}
	baseBranch, err := NormalizeGitBranch(input.BaseBranch)
	if err != nil {
		return Project{}, err
	}
	project := Project{
		ProjectConfig: config,
		RepositoryURL: strings.TrimSpace(input.RepositoryURL),
		BaseBranch:    baseBranch,
	}
	if err := project.Validate(); err != nil {
		return Project{}, err
	}
	return project, nil
}

// Validate 校验项目。
//
// 仓库地址与基础分支在这里一并校验：它们是项目的核心事实，不能等到任务创建或
// 容器内 Git 操作时才失败。
func (p Project) Validate() error {
	if err := p.ProjectConfig.Validate(); err != nil {
		return err
	}
	if _, err := ParseGitRepositoryURL(p.RepositoryURL); err != nil {
		return err
	}
	if _, err := NormalizeGitBranch(p.BaseBranch); err != nil {
		return err
	}
	return nil
}

// NormalizedBaseBranch 返回规范化后的基础分支名。
func (p Project) NormalizedBaseBranch() string {
	normalized, err := NormalizeGitBranch(p.BaseBranch)
	if err != nil {
		return strings.TrimSpace(p.BaseBranch)
	}
	return normalized
}

// EffectiveWorkDir 返回生效的容器内项目工作目录。
func (p Project) EffectiveWorkDir() string {
	if trimmed := strings.TrimSpace(p.WorkDir); trimmed != "" {
		return trimmed
	}
	return DefaultContainerWorkDir
}

// MarkDeleted 标记项目为逻辑删除。
//
// 删除只置 deleted_at：项目被历史任务与代码保存记录引用，物理删除会让这些记录
// 指向不存在的标识。重复删除返回错误，避免改写首次删除时间。
func (p *Project) MarkDeleted(at time.Time) error {
	if p.Deleted() {
		return invalidField("Project", "deleted_at", "项目已删除")
	}
	moment := normalizeTime(at)
	p.DeletedAt = &moment
	p.UpdatedAt = moment
	return nil
}

// Deleted 判断项目是否已被逻辑删除。
func (p Project) Deleted() bool {
	return p.DeletedAt != nil
}

// RequireProjectID 校验任务必须关联一个项目标识。
//
// 任务只能落在真实存在的项目上：项目标识决定缓存共享范围与远程管理工作目录名，
// 空值或手写错字都会让同一项目的任务落在不同位置，且无法事后自动合并。
func RequireProjectID(id string) error {
	if strings.TrimSpace(id) == "" {
		return invalidField("Task", "project_id", "任务必须关联项目")
	}
	return ValidateProjectID(id)
}
