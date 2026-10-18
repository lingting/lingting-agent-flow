package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// imageRefPattern 约束镜像仓库名与标签的书写形式。
//
// 镜像引用会进入远程 docker build 参数与标签，因此只允许小写字母、数字、
// 点、下划线与连字符，禁止空格、斜杠与 shell 元字符。
var imageRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]*(/[a-z0-9][a-z0-9._-]*)*:[a-z0-9][a-z0-9._-]*$`)

// imageIDPattern 约束 Docker 镜像 ID 的书写形式。
//
// 实际镜像 ID 形如 sha256:<64 位十六进制>；这里只接受带算法前缀的完整形式，
// 避免把短 ID 或标签误当成镜像 ID 持久化。
var imageIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidateImageRef 校验镜像引用是否形如 repo/name:tag。
//
// 标签不允许是 latest：镜像版本必须可追溯，禁止用浮动标签作为唯一标识。
func ValidateImageRef(ref string) error {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return invalidField("Image", "ref", "镜像引用不能为空")
	}
	if !imageRefPattern.MatchString(trimmed) {
		return invalidField(
			"Image",
			"ref",
			"镜像引用必须形如 repository/name:tag，且只包含小写字母、数字与 . _ - /",
		)
	}
	tag := trimmed[strings.LastIndex(trimmed, ":")+1:]
	if tag == "latest" {
		return invalidField("Image", "ref", "禁止使用 latest 作为镜像标签")
	}
	return nil
}

// ValidateImageID 校验实际镜像 ID 的书写形式。
func ValidateImageID(id string) error {
	if !imageIDPattern.MatchString(strings.TrimSpace(id)) {
		return invalidField("Image", "id", "镜像 ID 必须是 sha256:<64 位十六进制> 形式")
	}
	return nil
}

// ContainerState 是 AgentFlow 记录的容器管理状态。
//
// 它与 Docker 侧实际状态是两个独立事实：管理状态表达"AgentFlow 打算怎么管理它"，
// Docker 状态表达"容器现在实际是什么状态"。两者不一致时必须分别记录。
type ContainerState string

// 容器管理状态取值。
const (
	// ContainerStateCreating 表示已登记但尚未确认创建完成。
	ContainerStateCreating ContainerState = "creating"
	// ContainerStateCreated 表示容器已创建且尚未启动。
	ContainerStateCreated ContainerState = "created"
	// ContainerStateRunning 表示容器已启动并核验过运行状态。
	ContainerStateRunning ContainerState = "running"
	// ContainerStateStopped 表示容器已停止但仍保留现场。
	ContainerStateStopped ContainerState = "stopped"
	// ContainerStateRemoved 表示容器已删除。
	ContainerStateRemoved ContainerState = "removed"
	// ContainerStateDetached 表示容器已脱离 AgentFlow 自动管理。
	ContainerStateDetached ContainerState = "detached"
	// ContainerStateUnknown 表示远程状态无法确认，需要人工诊断。
	ContainerStateUnknown ContainerState = "unknown"
)

// containerStateMachine 定义容器管理状态迁移。
//
// detached 与 removed 都是终态：detached 之后 AgentFlow 不再自动启停或删除，
// removed 之后不会再次回到运行状态。
var containerStateMachine = statusMachine{
	entity: "Container",
	transitions: map[string][]string{
		string(ContainerStateCreating): {
			string(ContainerStateCreated),
			string(ContainerStateRunning),
			string(ContainerStateUnknown),
			string(ContainerStateDetached),
			string(ContainerStateRemoved),
		},
		string(ContainerStateCreated): {
			string(ContainerStateRunning),
			string(ContainerStateStopped),
			string(ContainerStateUnknown),
			string(ContainerStateDetached),
			string(ContainerStateRemoved),
		},
		string(ContainerStateRunning): {
			string(ContainerStateStopped),
			string(ContainerStateUnknown),
			string(ContainerStateDetached),
			string(ContainerStateRemoved),
		},
		string(ContainerStateStopped): {
			string(ContainerStateRunning),
			string(ContainerStateDetached),
			string(ContainerStateRemoved),
		},
		string(ContainerStateUnknown): {
			string(ContainerStateCreated),
			string(ContainerStateRunning),
			string(ContainerStateStopped),
			string(ContainerStateDetached),
			string(ContainerStateRemoved),
		},
	},
	terminal: []string{string(ContainerStateRemoved), string(ContainerStateDetached)},
}

// String 返回容器管理状态的字符串形式。
func (s ContainerState) String() string {
	return string(s)
}

// Validate 校验容器管理状态是否属于已知取值。
func (s ContainerState) Validate() error {
	if containerStateMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("Container", "state", "未知的容器管理状态 "+string(s))
}

// IsTerminal 判断容器管理状态是否为终态。
func (s ContainerState) IsTerminal() bool {
	return containerStateMachine.isTerminal(string(s))
}

// Detached 判断容器是否已脱离自动管理。
func (s ContainerState) Detached() bool {
	return s == ContainerStateDetached
}

// Managed 判断容器是否仍处于 AgentFlow 自动管理范围。
//
// 已删除与已脱离管理的容器都不再接受自动启停与删除操作。
func (s ContainerState) Managed() bool {
	return s != ContainerStateDetached && s != ContainerStateRemoved
}

// DockerContainerState 是 Docker 侧观测到的容器状态。
//
// 该类型只记录观测结果，不参与管理决策；管理决策一律以 ContainerState 为准。
type DockerContainerState string

// Docker 容器状态取值。
const (
	// DockerStateUnknown 表示尚未观测或无法确认。
	DockerStateUnknown DockerContainerState = "unknown"
	// DockerStateCreated 表示容器已创建但未启动。
	DockerStateCreated DockerContainerState = "created"
	// DockerStateRunning 表示容器正在运行。
	DockerStateRunning DockerContainerState = "running"
	// DockerStatePaused 表示容器已暂停。
	DockerStatePaused DockerContainerState = "paused"
	// DockerStateRestarting 表示容器正在重启。
	DockerStateRestarting DockerContainerState = "restarting"
	// DockerStateExited 表示容器已退出。
	DockerStateExited DockerContainerState = "exited"
	// DockerStateDead 表示容器处于不可恢复状态。
	DockerStateDead DockerContainerState = "dead"
	// DockerStateRemoving 表示容器正在被删除。
	DockerStateRemoving DockerContainerState = "removing"
)

// String 返回 Docker 容器状态的字符串形式。
func (s DockerContainerState) String() string {
	return string(s)
}

// Validate 校验 Docker 容器状态是否属于已知取值。
func (s DockerContainerState) Validate() error {
	switch s {
	case DockerStateUnknown,
		DockerStateCreated,
		DockerStateRunning,
		DockerStatePaused,
		DockerStateRestarting,
		DockerStateExited,
		DockerStateDead,
		DockerStateRemoving:
		return nil
	default:
		return invalidField("Container", "docker_state", "未知的 Docker 容器状态 "+string(s))
	}
}

// ParseDockerContainerState 把 docker inspect 返回的状态文本转换为已知取值。
//
// 未知取值不会被猜测为某个已知状态，而是返回 DockerStateUnknown，
// 由调用方按"状态不确定"处理。
func ParseDockerContainerState(raw string) DockerContainerState {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "created":
		return DockerStateCreated
	case "running":
		return DockerStateRunning
	case "paused":
		return DockerStatePaused
	case "restarting":
		return DockerStateRestarting
	case "exited":
		return DockerStateExited
	case "dead":
		return DockerStateDead
	case "removing":
		return DockerStateRemoving
	default:
		return DockerStateUnknown
	}
}

// ContainerEndReason 是容器生命周期结束的原因。
type ContainerEndReason string

// 生命周期结束原因取值。
const (
	// ContainerEndReasonNone 表示尚未结束。
	ContainerEndReasonNone ContainerEndReason = ""
	// ContainerEndReasonTaskSucceeded 表示任务成功后的清理。
	ContainerEndReasonTaskSucceeded ContainerEndReason = "task_succeeded"
	// ContainerEndReasonTaskFailed 表示任务失败后保留现场。
	ContainerEndReasonTaskFailed ContainerEndReason = "task_failed"
	// ContainerEndReasonInterrupted 表示任务中断。
	ContainerEndReasonInterrupted ContainerEndReason = "interrupted"
	// ContainerEndReasonDetached 表示被用户脱离管理。
	ContainerEndReasonDetached ContainerEndReason = "detached"
	// ContainerEndReasonManual 表示人工停止或删除。
	ContainerEndReasonManual ContainerEndReason = "manual"
)

// String 返回结束原因的字符串形式。
func (r ContainerEndReason) String() string {
	return string(r)
}

// Validate 校验结束原因是否属于已知取值。
func (r ContainerEndReason) Validate() error {
	switch r {
	case ContainerEndReasonNone,
		ContainerEndReasonTaskSucceeded,
		ContainerEndReasonTaskFailed,
		ContainerEndReasonInterrupted,
		ContainerEndReasonDetached,
		ContainerEndReasonManual:
		return nil
	default:
		return invalidField("Container", "end_reason", "未知的生命周期结束原因 "+string(r))
	}
}

// containerNamePattern 约束容器名称的书写形式。
//
// Docker 容器名称只允许字母、数字、下划线、点与连字符，且必须以字母或数字开头。
var containerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ValidateContainerName 校验容器名称。
func ValidateContainerName(name string) error {
	if !containerNamePattern.MatchString(name) {
		return invalidField(
			"Container",
			"name",
			"容器名称只能包含字母、数字与 _ . -，且不超过 128 个字符",
		)
	}
	return nil
}

// Container 是 AgentFlow 管理的远程 Docker 容器记录。
//
// 记录同时保存管理状态与最近一次观测到的 Docker 状态，两者不得互相替代。
// 容器所有权通过本记录与 Docker 标签共同核验，不能只依赖容器名称。
type Container struct {
	ID     ID `json:"id"`
	HostID ID `json:"host_id"`
	// DockerID 是 Docker 侧容器 ID，创建后核验取得。
	DockerID string `json:"docker_id,omitempty"`
	// Name 是容器名称，由安全唯一标识生成。
	Name string `json:"name"`
	// Environment 是容器使用的环境引用。
	Environment EnvironmentRef `json:"environment"`
	// EnvironmentVersion 是环境版本标签，例如 v0001。
	EnvironmentVersion string `json:"environment_version"`
	// BuildConfigSHA256 是构建该镜像使用的构建配置哈希。
	BuildConfigSHA256 string `json:"build_config_sha256"`
	// ImageID 是创建容器时实际使用的镜像 ID。
	ImageID string `json:"image_id"`
	// TaskID 是可选的任务关联，工作流引擎实现后再正式使用。
	TaskID *ID `json:"task_id,omitempty"`
	// AttemptID 是可选的尝试关联。
	AttemptID *ID `json:"attempt_id,omitempty"`
	// State 是 AgentFlow 的容器管理状态。
	State ContainerState `json:"state"`
	// DockerState 是最近一次观测到的 Docker 状态。
	DockerState DockerContainerState `json:"docker_state"`
	// CreatedAt 是容器创建时间。
	CreatedAt time.Time `json:"created_at"`
	// StartedAt 是最近一次启动时间。
	StartedAt *time.Time `json:"started_at,omitempty"`
	// FinishedAt 是生命周期结束时间。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// EndReason 是生命周期结束原因。
	EndReason ContainerEndReason `json:"end_reason"`
	// Detached 表示容器已脱离 AgentFlow 自动管理。
	Detached bool `json:"detached"`
	// DetachedAt 是脱离管理的时间。
	DetachedAt *time.Time `json:"detached_at,omitempty"`
	// SyncedAt 是最近一次状态同步时间。
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	// ErrorSummary 是最近一次失败或状态不一致的可诊断摘要。
	ErrorSummary string `json:"error_summary,omitempty"`
	// UpdatedAt 是记录最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// NewContainer 创建并校验容器登记记录。
func NewContainer(
	id, hostID ID,
	name string,
	ref EnvironmentRef,
	versionLabel, buildConfigHash, imageID string,
	at time.Time,
) (Container, error) {
	createdAt := normalizeTime(at)
	if id.IsEmpty() {
		id = NewID()
	}
	container := Container{
		ID:                 id,
		HostID:             hostID,
		Name:               strings.TrimSpace(name),
		Environment:        ref,
		EnvironmentVersion: strings.TrimSpace(versionLabel),
		BuildConfigSHA256:  strings.TrimSpace(buildConfigHash),
		ImageID:            strings.TrimSpace(imageID),
		State:              ContainerStateCreating,
		DockerState:        DockerStateUnknown,
		EndReason:          ContainerEndReasonNone,
		CreatedAt:          createdAt,
		UpdatedAt:          createdAt,
	}
	if err := container.Validate(); err != nil {
		return Container{}, err
	}
	return container, nil
}

// Validate 校验容器记录是否完整自洽。
func (c Container) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	if err := c.HostID.Validate(); err != nil {
		return err
	}
	if err := ValidateContainerName(c.Name); err != nil {
		return err
	}
	if err := c.Environment.Validate(); err != nil {
		return err
	}
	if _, err := ParseVersionLabel(c.EnvironmentVersion); err != nil {
		return err
	}
	if err := ValidateSHA256("build_config_sha256", c.BuildConfigSHA256); err != nil {
		return err
	}
	if err := ValidateImageID(c.ImageID); err != nil {
		return err
	}
	if c.AttemptID != nil {
		if err := c.AttemptID.Validate(); err != nil {
			return err
		}
		if c.TaskID == nil {
			return invalidField("Container", "attempt_id", "关联尝试时必须同时指定任务")
		}
	}
	if err := c.State.Validate(); err != nil {
		return err
	}
	if err := c.DockerState.Validate(); err != nil {
		return err
	}
	if err := c.EndReason.Validate(); err != nil {
		return err
	}
	return validateDetachedFields(c)
}

// validateDetachedFields 校验 detached 标记与状态是否一致。
func validateDetachedFields(c Container) error {
	if c.Detached && c.State != ContainerStateDetached {
		return invalidField("Container", "state", "已脱离管理的容器状态必须是 detached")
	}
	if c.State == ContainerStateDetached && !c.Detached {
		return invalidField("Container", "detached", "detached 状态必须同时标记 detached 字段")
	}
	if c.Detached && c.DetachedAt == nil {
		return invalidField("Container", "detached_at", "脱离管理必须记录时间")
	}
	if c.State == ContainerStateRemoved && c.FinishedAt == nil {
		return invalidField("Container", "finished_at", "已删除的容器必须记录结束时间")
	}
	return nil
}

// transitionTo 校验并执行一次管理状态迁移。
func (c *Container) transitionTo(next ContainerState, at time.Time) error {
	if c == nil {
		return invalidField("Container", "", "实体为空")
	}
	if err := containerStateMachine.validate(string(c.State), string(next)); err != nil {
		return err
	}
	c.State = next
	c.UpdatedAt = normalizeTime(at)
	return nil
}

// MarkCreated 记录容器已创建并核验到实际容器 ID。
func (c *Container) MarkCreated(dockerID string, dockerState DockerContainerState, at time.Time) error {
	if strings.TrimSpace(dockerID) == "" {
		return invalidField("Container", "docker_id", "容器 ID 不能为空")
	}
	if err := dockerState.Validate(); err != nil {
		return err
	}
	if err := c.transitionTo(ContainerStateCreated, at); err != nil {
		return err
	}
	c.DockerID = strings.TrimSpace(dockerID)
	c.DockerState = dockerState
	return nil
}

// MarkRunning 记录容器已启动并核验到运行状态。
func (c *Container) MarkRunning(at time.Time) error {
	if c.Detached {
		return fmt.Errorf("%w: 已脱离管理的容器不能自动启动", ErrInvalidState)
	}
	if err := c.transitionTo(ContainerStateRunning, at); err != nil {
		return err
	}
	startedAt := normalizeTime(at)
	c.StartedAt = &startedAt
	c.DockerState = DockerStateRunning
	return nil
}

// MarkStopped 记录容器已停止并保留现场。
func (c *Container) MarkStopped(at time.Time) error {
	if c.Detached {
		return fmt.Errorf("%w: 已脱离管理的容器不能自动停止", ErrInvalidState)
	}
	if err := c.transitionTo(ContainerStateStopped, at); err != nil {
		return err
	}
	c.DockerState = DockerStateExited
	return nil
}

// MarkRemoved 记录容器已删除。
func (c *Container) MarkRemoved(reason ContainerEndReason, at time.Time) error {
	if c.Detached {
		return fmt.Errorf("%w: 已脱离管理的容器不能自动删除", ErrInvalidState)
	}
	if err := reason.Validate(); err != nil {
		return err
	}
	if reason == ContainerEndReasonNone {
		reason = ContainerEndReasonManual
	}
	if err := c.transitionTo(ContainerStateRemoved, at); err != nil {
		return err
	}
	finishedAt := normalizeTime(at)
	c.FinishedAt = &finishedAt
	c.EndReason = reason
	c.DockerState = DockerStateRemovedOrUnknown(c.DockerState)
	return nil
}

// Detach 把容器标记为脱离 AgentFlow 自动管理。
//
// 脱离管理是终态：标签无法原地修改，因此不能依靠给旧容器补标签来恢复管理权。
func (c *Container) Detach(at time.Time) error {
	if c.Detached {
		return fmt.Errorf("%w: 容器已脱离管理", ErrInvalidState)
	}
	if c.State == ContainerStateRemoved {
		return fmt.Errorf("%w: 已删除的容器不能脱离管理", ErrInvalidState)
	}
	if err := c.transitionTo(ContainerStateDetached, at); err != nil {
		return err
	}
	detachedAt := normalizeTime(at)
	c.Detached = true
	c.DetachedAt = &detachedAt
	c.EndReason = ContainerEndReasonDetached
	return nil
}

// SyncDockerState 更新最近一次观测到的 Docker 状态。
//
// 该操作只更新观测事实，不改变管理状态；已脱离管理的容器仍可记录只读观测结果，
// 但不会因此恢复管理权。
func (c *Container) SyncDockerState(state DockerContainerState, at time.Time) error {
	if err := state.Validate(); err != nil {
		return err
	}
	syncedAt := normalizeTime(at)
	c.DockerState = state
	c.SyncedAt = &syncedAt
	c.UpdatedAt = syncedAt
	return nil
}

// MarkUnknown 记录远程状态无法确认。
func (c *Container) MarkUnknown(summary string, at time.Time) error {
	if err := c.transitionTo(ContainerStateUnknown, at); err != nil {
		return err
	}
	c.ErrorSummary = strings.TrimSpace(summary)
	return nil
}

// DockerStateRemovedOrUnknown 在删除后保留可核验的观测状态。
//
// 容器已删除时 Docker 不再报告状态，因此这里只保留原有的退出状态，
// 避免把"无法观测"写成"仍在运行"。
func DockerStateRemovedOrUnknown(current DockerContainerState) DockerContainerState {
	switch current {
	case DockerStateRunning, DockerStatePaused, DockerStateRestarting:
		return DockerStateExited
	default:
		return current
	}
}

// ContainerFilter 约束容器查询条件，零值表示不限制。
type ContainerFilter struct {
	// HostID 限制容器所属节点。
	HostID ID
	// States 限制管理状态，为空表示不限制。
	States []ContainerState
	// IncludeDetached 表示是否包含已脱离管理的容器。
	IncludeDetached bool
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
}
