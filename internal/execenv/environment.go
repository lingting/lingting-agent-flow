package execenv

import (
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// ContainerRef 是执行环境背后的容器标识。
//
// 同时保存 AgentFlow 登记记录 ID 与 Docker 容器 ID：前者是业务引用，后者是执行
// 命令时真正使用的目标，两者都必须存在，不能只保留其中一个。
type ContainerRef struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// DockerID 是 Docker 侧容器 ID。
	DockerID string
	// HostID 是所属执行节点。
	HostID string
	// Name 是容器名称。
	Name string
}

// NewContainerRef 构造并校验容器引用。
func NewContainerRef(containerID, dockerID, hostID, name string) (ContainerRef, error) {
	ref := ContainerRef{
		ContainerID: strings.TrimSpace(containerID),
		DockerID:    strings.TrimSpace(dockerID),
		HostID:      strings.TrimSpace(hostID),
		Name:        strings.TrimSpace(name),
	}
	if err := ref.Validate(); err != nil {
		return ContainerRef{}, err
	}
	return ref, nil
}

// ContainerRefFrom 从容器登记记录构造引用。
func ContainerRefFrom(container domain.Container) (ContainerRef, error) {
	return NewContainerRef(
		container.ID.String(),
		container.DockerID,
		container.HostID.String(),
		container.Name,
	)
}

// Validate 校验容器引用是否完整。
func (r ContainerRef) Validate() error {
	if err := domain.ID(r.ContainerID).Validate(); err != nil {
		return fmt.Errorf("%w: 容器登记记录 ID 不合法: %w", ErrInvalidValue, err)
	}
	if r.DockerID == "" {
		return fmt.Errorf("%w: Docker 容器 ID 不能为空", ErrInvalidValue)
	}
	if err := domain.ID(r.HostID).Validate(); err != nil {
		return fmt.Errorf("%w: 执行节点 ID 不合法: %w", ErrInvalidValue, err)
	}
	if err := domain.ValidateContainerName(r.Name); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	return nil
}

// Status 是执行环境的当前状态。
//
// 它与任务状态是两个独立事实：任务状态表达业务进展，执行环境状态表达资源现状，
// 不能由其中一个推断另一个。
type Status string

// 执行环境状态取值。
const (
	// StatusUnprepared 表示尚未准备。
	StatusUnprepared Status = "unprepared"
	// StatusReady 表示已就绪且容器正在运行。
	StatusReady Status = "ready"
	// StatusNotRunning 表示已就绪但容器当前未运行。
	StatusNotRunning Status = "not_running"
	// StatusUnknown 表示远程状态无法确认，需要人工诊断。
	StatusUnknown Status = "unknown"
	// StatusReleased 表示已释放。
	StatusReleased Status = "released"
)

// String 返回状态的字符串形式。
func (s Status) String() string {
	return string(s)
}

// Usable 判断该状态下是否可以执行命令。
func (s Status) Usable() bool {
	return s == StatusReady
}

// Environment 是一次执行使用的、已准备好的执行环境。
//
// 它是不可变的值对象：调用方拿到它之后只读取，不会因为其他并发操作而改变。
// 释放动作由 Provider.Release 完成，并返回一个新的状态结果。
type Environment struct {
	kind       Kind
	workspace  Workspace
	container  ContainerRef
	preparedAt time.Time
}

// NewEnvironment 构造并校验执行环境。
func NewEnvironment(
	kind Kind,
	workspace Workspace,
	container ContainerRef,
	preparedAt time.Time,
) (Environment, error) {
	if err := kind.RequireSupported(); err != nil {
		return Environment{}, err
	}
	if err := workspace.Validate(); err != nil {
		return Environment{}, err
	}
	if err := container.Validate(); err != nil {
		return Environment{}, err
	}
	at := preparedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return Environment{
		kind:       kind,
		workspace:  workspace,
		container:  container,
		preparedAt: at.UTC(),
	}, nil
}

// Kind 返回执行环境类型。
func (e Environment) Kind() Kind {
	return e.kind
}

// Workspace 返回容器内工作区。
func (e Environment) Workspace() Workspace {
	return e.workspace
}

// Container 返回容器引用。
func (e Environment) Container() ContainerRef {
	return e.container
}

// WorkDir 返回命令默认工作目录。
func (e Environment) WorkDir() string {
	return e.workspace.Project
}

// SessionDir 返回容器内临时 Session 目录。
func (e Environment) SessionDir() string {
	return e.workspace.SessionDir
}

// PreparedAt 返回准备完成时间。
func (e Environment) PreparedAt() time.Time {
	return e.preparedAt
}

// Label 返回可写入执行记录的描述。
//
// 它只包含容器内工作区与容器登记记录 ID，不包含任何凭据或本机路径。
func (e Environment) Label() string {
	return fmt.Sprintf("%s@%s", e.kind, e.container.ContainerID)
}

// Validate 校验执行环境是否完整。
func (e Environment) Validate() error {
	if err := e.kind.RequireSupported(); err != nil {
		return err
	}
	if err := e.workspace.Validate(); err != nil {
		return err
	}
	return e.container.Validate()
}

// EnsureWorkDir 校验目标工作目录，空值回退到项目目录。
//
// 命令执行一律经过本方法：调用方不能指定容器内 /workspace 之外的目录，
// 也不能退化为服务进程的当前目录。
func (e Environment) EnsureWorkDir(requested string) (string, error) {
	trimmed := strings.TrimSpace(requested)
	if trimmed == "" {
		trimmed = e.workspace.Project
	}
	if err := domain.ValidateContainerWorkDir(trimmed); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	return trimmed, nil
}
