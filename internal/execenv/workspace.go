package execenv

import (
	"fmt"
	"path"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// 容器内固定目录。
const (
	// DefaultProjectDir 是容器内项目代码目录。
	DefaultProjectDir = domain.DefaultContainerWorkDir
	// DefaultSessionDir 是容器内临时 Session 目录。
	//
	// 该目录只在容器内存在，容器删除后随之消失，因此必须在容器销毁之前回传本机。
	DefaultSessionDir = "/tmp/agentflow/sessions"
)

// ProjectAccess 是容器内项目代码目录的访问模式。
//
// 只读与可写必须由结构化的取值表达，不能只靠 Prompt 约定或字符串常量：
// 规划容器要求项目代码只读，普通任务容器要求项目代码可写，两者不可互相替代。
type ProjectAccess string

// 项目代码目录的访问模式取值。
const (
	// ProjectAccessReadWrite 表示项目代码目录可写，普通任务容器使用该模式。
	ProjectAccessReadWrite ProjectAccess = "read_write"
	// ProjectAccessReadOnly 表示项目代码目录只读，只用于规划容器。
	ProjectAccessReadOnly ProjectAccess = "read_only"
)

// Validate 校验访问模式是否属于已知取值。
func (a ProjectAccess) Validate() error {
	switch a {
	case ProjectAccessReadWrite, ProjectAccessReadOnly:
		return nil
	default:
		return fmt.Errorf("%w: 未知的项目代码访问模式 %q", ErrInvalidValue, string(a))
	}
}

// ReadOnly 判断项目代码目录是否只读。
func (a ProjectAccess) ReadOnly() bool {
	return a == ProjectAccessReadOnly
}

// Workspace 描述容器内的执行工作区。
//
// 它只包含容器内路径：远程宿主机路径由 ManagementWorkspace 表达，两者不得混用。
type Workspace struct {
	// Project 是容器内项目代码目录，代码拉取、Agent 开发与验证检查都在这里。
	Project string
	// SessionDir 是容器内临时 Session 目录。
	SessionDir string
	// Caches 是本次执行挂载的依赖缓存定义。
	Caches []domain.CacheMount
	// ProjectAccess 是项目代码目录的访问模式，空值按可写处理。
	//
	// 规划容器必须显式声明为只读；普通任务容器保持可写，两者不共享同一取值。
	ProjectAccess ProjectAccess
	// WritableDirs 是只读工作区中显式允许写入的容器内目录。
	//
	// 它只在项目代码目录只读时生效：可写目录必须全部显式声明，且不得与项目代码
	// 目录互相包含，否则只读语义会被同一容器内的其他挂载点绕过。
	WritableDirs []string
}

// NewWorkspace 构造并校验可写的容器内工作区。
//
// 空的项目目录使用 DefaultProjectDir，空的 Session 目录使用 DefaultSessionDir。
// 该构造函数保持普通任务容器的既有语义：项目代码目录可写、不声明任何额外的
// 可写目录，也不挂载只读代码卷。
func NewWorkspace(project, sessionDir string, caches []domain.CacheMount) (Workspace, error) {
	workspace := Workspace{
		Project:       strings.TrimSpace(project),
		SessionDir:    strings.TrimSpace(sessionDir),
		Caches:        caches,
		ProjectAccess: ProjectAccessReadWrite,
	}
	if workspace.Project == "" {
		workspace.Project = DefaultProjectDir
	}
	if workspace.SessionDir == "" {
		workspace.SessionDir = DefaultSessionDir
	}
	if err := workspace.Validate(); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// NewReadOnlyWorkspace 构造并校验只读的容器内工作区。
//
// 只读工作区只用于规划容器：项目代码目录以只读方式使用，容器内唯一可写的目录必须
// 通过 writableDirs 显式声明，且必须覆盖 Session 目录。只读工作区不接受缓存卷，
// 使规划容器无法通过可写缓存挂载绕过项目代码只读限制。
func NewReadOnlyWorkspace(
	project, sessionDir string,
	writableDirs []string,
) (Workspace, error) {
	workspace := Workspace{
		Project:       strings.TrimSpace(project),
		SessionDir:    strings.TrimSpace(sessionDir),
		ProjectAccess: ProjectAccessReadOnly,
		WritableDirs:  normalizeWritableDirs(writableDirs),
	}
	if workspace.Project == "" {
		workspace.Project = DefaultProjectDir
	}
	if workspace.SessionDir == "" {
		workspace.SessionDir = DefaultSessionDir
	}
	if err := workspace.Validate(); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// normalizeWritableDirs 去除空白项并保留调用方的声明顺序。
func normalizeWritableDirs(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// Access 返回生效的项目代码访问模式，空值按可写处理。
//
// 空值兼容既有构造路径：结构体字面量忘记填写该字段时按可写处理，不会被误判为只读。
func (w Workspace) Access() ProjectAccess {
	if w.ProjectAccess == "" {
		return ProjectAccessReadWrite
	}
	return w.ProjectAccess
}

// ReadOnly 判断项目代码目录是否以只读方式使用。
func (w Workspace) ReadOnly() bool {
	return w.Access().ReadOnly()
}

// Validate 校验容器内工作区是否自洽。
//
// 关键约束：项目代码目录必须位于容器内 /workspace 之下；Session 目录与项目代码目录
// 不得互相包含，否则容器删除后项目代码会与临时文件一起消失；只读工作区的可写目录
// 必须显式声明，且不得以任何方式与项目代码目录重叠。
func (w Workspace) Validate() error {
	if err := domain.ValidateContainerWorkDir(w.Project); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	if err := validateAbsolutePath("Session 目录", w.SessionDir); err != nil {
		return err
	}
	project := path.Clean(w.Project)
	sessionDir := path.Clean(w.SessionDir)
	if sessionDir == project || isWithin(sessionDir, project) {
		return fmt.Errorf(
			"%w: Session 目录 %s 不能位于项目代码目录 %s 之内",
			ErrInvalidValue,
			w.SessionDir,
			w.Project,
		)
	}
	if isWithin(project, sessionDir) {
		return fmt.Errorf(
			"%w: 项目代码目录 %s 不能位于 Session 目录 %s 之内",
			ErrInvalidValue,
			w.Project,
			w.SessionDir,
		)
	}
	for _, mount := range w.Caches {
		if err := mount.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidValue, err)
		}
	}
	access := w.Access()
	if err := access.Validate(); err != nil {
		return err
	}
	if access.ReadOnly() {
		return w.validateReadOnly(project, sessionDir)
	}
	if len(w.WritableDirs) != 0 {
		return fmt.Errorf(
			"%w: 可写目录只在只读工作区中声明，普通任务容器的文件系统本身可写",
			ErrInvalidValue,
		)
	}
	return nil
}

// validateReadOnly 校验只读工作区的可写目录声明。
func (w Workspace) validateReadOnly(project, sessionDir string) error {
	if len(w.Caches) != 0 {
		return fmt.Errorf(
			"%w: 只读工作区不能挂载可写缓存卷，否则会绕过项目代码只读限制",
			ErrInvalidValue,
		)
	}
	writable := make(map[string]struct{}, len(w.WritableDirs))
	for _, dir := range w.WritableDirs {
		if err := validateAbsolutePath("可写目录", dir); err != nil {
			return err
		}
		cleaned := path.Clean(dir)
		if _, exists := writable[cleaned]; exists {
			return fmt.Errorf("%w: 可写目录 %s 重复声明", ErrInvalidValue, cleaned)
		}
		if domain.IsReservedContainerPath(cleaned) {
			return fmt.Errorf("%w: 可写目录 %s 不能使用保留路径", ErrInvalidValue, cleaned)
		}
		if cleaned == project || isWithin(cleaned, project) {
			return fmt.Errorf(
				"%w: 可写目录 %s 不能位于项目代码目录 %s 之内",
				ErrInvalidValue,
				cleaned,
				w.Project,
			)
		}
		if isWithin(project, cleaned) {
			return fmt.Errorf(
				"%w: 可写目录 %s 不能包含项目代码目录 %s",
				ErrInvalidValue,
				cleaned,
				w.Project,
			)
		}
		writable[cleaned] = struct{}{}
	}
	if !writableCovers(writable, sessionDir) {
		return fmt.Errorf(
			"%w: Session 目录 %s 必须由显式声明的可写目录提供写入能力",
			ErrInvalidValue,
			w.SessionDir,
		)
	}
	return nil
}

// writableCovers 判断目录是否被某个显式声明的可写目录覆盖。
func writableCovers(writable map[string]struct{}, target string) bool {
	if _, ok := writable[target]; ok {
		return true
	}
	for dir := range writable {
		if isWithin(target, dir) {
			return true
		}
	}
	return false
}

// SourceInContainer 判断项目代码是否位于容器内。
//
// 本版本恒为 true：只有容器执行环境。未来若支持在宿主机目录执行，必须显式改写此处，
// 而不是由调用方猜测代码在哪里。
func (w Workspace) SourceInContainer() bool {
	return true
}

// Label 返回可写入执行记录的容器内工作区标识。
func (w Workspace) Label() string {
	return w.Project
}

// ManagementWorkspace 描述远程宿主机上的 AgentFlow 管理工作区。
//
// 它只存放项目配置、Dockerfile 与镜像构建资源，不得存放项目源代码；
// 因此它与 Workspace 是两个不同的概念，不能互相替代。
type ManagementWorkspace struct {
	// Root 是管理工作区根目录的远程绝对路径，默认 ~/.lingting/agentflow 解析后的结果。
	Root string
	// Project 是该项目的管理目录，必须位于 Root 之内。
	Project string
}

// NewManagementWorkspace 构造并校验管理工作区。
func NewManagementWorkspace(root, project string) (ManagementWorkspace, error) {
	workspace := ManagementWorkspace{
		Root:    strings.TrimSpace(root),
		Project: strings.TrimSpace(project),
	}
	if err := workspace.Validate(); err != nil {
		return ManagementWorkspace{}, err
	}
	return workspace, nil
}

// Validate 校验管理工作区是否自洽。
func (w ManagementWorkspace) Validate() error {
	if err := validateAbsolutePath("管理工作区", w.Root); err != nil {
		return err
	}
	if err := validateAbsolutePath("项目管理工作目录", w.Project); err != nil {
		return err
	}
	if !isWithin(w.Project, w.Root) {
		return fmt.Errorf(
			"%w: 项目管理工作目录 %s 必须位于管理工作区 %s 之内",
			ErrInvalidValue,
			w.Project,
			w.Root,
		)
	}
	return nil
}

// ContainsSource 恒为 false：管理工作区不存放项目源代码。
func (w ManagementWorkspace) ContainsSource() bool {
	return false
}

// validateAbsolutePath 校验远程 POSIX 绝对路径。
func validateAbsolutePath(field, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%w: %s不能为空", ErrInvalidValue, field)
	}
	if strings.ContainsRune(trimmed, '\\') {
		return fmt.Errorf("%w: %s必须使用 POSIX 分隔符 /", ErrInvalidValue, field)
	}
	for _, char := range trimmed {
		if char == 0 || char == '\n' || char == '\r' || char == '\t' {
			return fmt.Errorf("%w: %s不能包含控制字符", ErrInvalidValue, field)
		}
	}
	if !strings.HasPrefix(trimmed, "/") {
		return fmt.Errorf("%w: %s必须是绝对路径", ErrInvalidValue, field)
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return fmt.Errorf("%w: %s不能包含上级目录引用 ..", ErrInvalidValue, field)
		}
	}
	if path.Clean(trimmed) != trimmed {
		return fmt.Errorf("%w: %s必须已经规范化", ErrInvalidValue, field)
	}
	return nil
}

// isWithin 判断 target 是否位于 base 之内（不含 base 本身）。
func isWithin(target, base string) bool {
	return strings.HasPrefix(path.Clean(target), path.Clean(base)+"/")
}
