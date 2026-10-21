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
}

// NewWorkspace 构造并校验容器内工作区。
//
// 空的项目目录使用 DefaultProjectDir，空的 Session 目录使用 DefaultSessionDir。
func NewWorkspace(project, sessionDir string, caches []domain.CacheMount) (Workspace, error) {
	workspace := Workspace{
		Project:    strings.TrimSpace(project),
		SessionDir: strings.TrimSpace(sessionDir),
		Caches:     caches,
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

// Validate 校验容器内工作区是否自洽。
//
// 关键约束：项目代码目录必须位于容器内 /workspace 之下；Session 目录不得位于项目
// 代码目录之内，否则容器删除后项目代码会与临时文件一起消失。
func (w Workspace) Validate() error {
	if err := domain.ValidateContainerWorkDir(w.Project); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	if err := validateAbsolutePath("Session 目录", w.SessionDir); err != nil {
		return err
	}
	if path.Clean(w.SessionDir) == path.Clean(w.Project) || isWithin(w.SessionDir, w.Project) {
		return fmt.Errorf(
			"%w: Session 目录 %s 不能位于项目代码目录 %s 之内",
			ErrInvalidValue,
			w.SessionDir,
			w.Project,
		)
	}
	for _, mount := range w.Caches {
		if err := mount.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidValue, err)
		}
	}
	return nil
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
