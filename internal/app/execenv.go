package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
)

// ExecutionEnvironmentImagePort 是执行环境服务对镜像准备能力的需求。
//
// 它只包含"预检远程 Docker 环境"与"构建或复用环境镜像"两项：
// 执行环境不负责环境版本的编辑与历史管理。
type ExecutionEnvironmentImagePort interface {
	// Preflight 只读预检远程 Docker 环境并解析管理工作区路径。
	Preflight(ctx context.Context, options InitOptions) (InitResult, error)
	// Build 构建或复用指定环境版本的镜像。
	Build(ctx context.Context, request BuildRequest) (BuildResult, error)
	// ResolveVersion 解析环境版本，0 表示当前最新版本。
	ResolveVersion(
		ctx context.Context,
		hostID domain.ID,
		ref domain.EnvironmentRef,
		number int,
	) (domain.EnvironmentVersion, error)
}

// environmentImagePort 把 EnvironmentService 适配为执行环境的镜像端口。
//
// 适配层只暴露 ResolveVersion：EnvironmentService 内部的版本解析方法不导出，
// 这里以薄包装补齐，避免把版本管理细节暴露给执行环境的使用者。
type environmentImagePort struct {
	service *EnvironmentService
}

// Preflight 只读预检远程 Docker 环境。
func (p environmentImagePort) Preflight(
	ctx context.Context,
	options InitOptions,
) (InitResult, error) {
	return p.service.Preflight(ctx, options)
}

// Build 构建或复用环境镜像。
func (p environmentImagePort) Build(
	ctx context.Context,
	request BuildRequest,
) (BuildResult, error) {
	return p.service.Build(ctx, request)
}

// ResolveVersion 解析环境版本。
func (p environmentImagePort) ResolveVersion(
	ctx context.Context,
	hostID domain.ID,
	ref domain.EnvironmentRef,
	number int,
) (domain.EnvironmentVersion, error) {
	return p.service.resolveVersion(ctx, hostID, ref, number)
}

// ExecutionEnvironmentContainerPort 是执行环境服务对容器能力的需求。
//
// 容器只被创建、核验、使用与按策略处置：执行环境不重新实现容器状态机，
// 也不直接拼 Docker 命令。
type ExecutionEnvironmentContainerPort interface {
	// CreateContainer 创建并启动受管理的任务容器。
	CreateContainer(ctx context.Context, request CreateContainerRequest) (ContainerDetail, error)
	// InspectContainer 核验容器当前状态。
	InspectContainer(ctx context.Context, request InspectContainerRequest) (ContainerDetail, error)
	// Exec 在受管理容器内执行一条非交互命令。
	Exec(ctx context.Context, request ExecRequest) (ExecResult, error)
	// DeleteContainer 删除容器与任务级临时资源。
	DeleteContainer(ctx context.Context, request DeleteContainerRequest) (ContainerDetail, error)
	// DetachContainer 保留容器现场并脱离自动管理。
	DetachContainer(ctx context.Context, request DetachContainerRequest) (ContainerDetail, error)
	// LoadContainer 读取容器登记记录。
	LoadContainer(ctx context.Context, containerID string) (domain.Container, error)
	// LoadHost 读取执行节点配置。
	LoadHost(ctx context.Context, hostID string) (domain.ExecutionHost, error)
}

// ExecutionEnvironmentTaskStore 是执行环境服务对任务记录的读写需求。
type ExecutionEnvironmentTaskStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	GetAttempt(ctx context.Context, id domain.ID) (*domain.Attempt, error)
	UpdateTaskIfStatus(
		ctx context.Context,
		task domain.Task,
		expected []domain.TaskStatus,
	) error
	// AppendEvent 追加任务事件，用于记录需要人工处置的现场。
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
}

// ExecutionEnvironmentOptions 是构造执行环境服务的输入。
type ExecutionEnvironmentOptions struct {
	// Environments 提供环境版本解析与镜像构建能力，必填。
	Environments *EnvironmentService
	// Containers 提供容器创建、核验、命令执行与生命周期能力，必填。
	Containers *ContainerService
	// Images 覆盖镜像能力，为空时使用 Environments。
	//
	// 该字段只用于测试注入受控替身：生产代码始终使用 Environments。
	Images ExecutionEnvironmentImagePort
	// ContainerPort 覆盖容器能力，为空时使用 Containers。
	//
	// 该字段只用于测试注入受控替身：生产代码始终使用 Containers。
	ContainerPort ExecutionEnvironmentContainerPort
	// Tasks 覆盖任务记录能力，为空时使用环境服务的存储。
	//
	// 该字段只用于测试注入受控替身：生产代码始终使用真实存储。
	Tasks ExecutionEnvironmentTaskStore
	// Logger 是日志器，可为空。
	Logger *logging.Logger
}

// ExecutionEnvironmentService 是远程 Linux + Docker 的统一执行环境实现。
//
// 它把 execenv.Provider 的四个动作翻译为既有能力：
//   - Prepare：校验节点与工作区 → 准备镜像 → 创建容器 → 核验容器可用性；
//   - Status：核验容器实际状态，不修改资源；
//   - Exec：在受管理容器内的指定工作目录执行命令；
//   - Release：按处置方式删除、保留或脱离容器。
//
// 本类型不重新实现容器生命周期、镜像版本管理与命令转义：它们分别由
// ContainerService、EnvironmentService 与 docker.Runner 负责。因此 Task Service、
// Pi Runner 与 Validator 都不需要接触 Docker CLI。
type ExecutionEnvironmentService struct {
	images     ExecutionEnvironmentImagePort
	containers ExecutionEnvironmentContainerPort
	tasks      ExecutionEnvironmentTaskStore
	logger     *logging.Logger
}

// NewExecutionEnvironmentService 构造远程 Docker 执行环境服务。
func NewExecutionEnvironmentService(
	options ExecutionEnvironmentOptions,
) (*ExecutionEnvironmentService, error) {
	if options.Environments == nil {
		return nil, fmt.Errorf("执行环境服务初始化失败: 环境服务不能为空")
	}
	if options.Containers == nil {
		return nil, fmt.Errorf("执行环境服务初始化失败: 容器服务不能为空")
	}
	service := &ExecutionEnvironmentService{
		images:     options.Images,
		containers: options.ContainerPort,
		tasks:      options.Tasks,
		logger:     options.Logger,
	}
	if service.images == nil {
		service.images = environmentImagePort{service: options.Environments}
	}
	if service.containers == nil {
		service.containers = containerServicePort{service: options.Containers}
	}
	if service.tasks == nil {
		if options.Environments.store == nil {
			return nil, fmt.Errorf("执行环境服务初始化失败: 存储不能为空")
		}
		service.tasks = taskStorePort{store: options.Environments.store}
	}
	return service, nil
}

// containerServicePort 把 ContainerService 适配为执行环境的容器端口。
//
// 适配层只转发调用，不复制任何业务判断；容器归属、状态机与命令转义仍在
// ContainerService 内实施。
type containerServicePort struct {
	service *ContainerService
}

// CreateContainer 创建并启动受管理的任务容器。
func (p containerServicePort) CreateContainer(
	ctx context.Context,
	request CreateContainerRequest,
) (ContainerDetail, error) {
	return p.service.CreateContainer(ctx, request)
}

// InspectContainer 核验容器当前状态。
func (p containerServicePort) InspectContainer(
	ctx context.Context,
	request InspectContainerRequest,
) (ContainerDetail, error) {
	return p.service.InspectContainer(ctx, request)
}

// Exec 在受管理容器内执行一条非交互命令。
func (p containerServicePort) Exec(
	ctx context.Context,
	request ExecRequest,
) (ExecResult, error) {
	return p.service.Exec(ctx, request)
}

// DeleteContainer 删除容器与任务级临时资源。
func (p containerServicePort) DeleteContainer(
	ctx context.Context,
	request DeleteContainerRequest,
) (ContainerDetail, error) {
	return p.service.DeleteContainer(ctx, request)
}

// DetachContainer 保留容器现场并脱离自动管理。
func (p containerServicePort) DetachContainer(
	ctx context.Context,
	request DetachContainerRequest,
) (ContainerDetail, error) {
	return p.service.DetachContainer(ctx, request)
}

// LoadContainer 读取容器登记记录。
func (p containerServicePort) LoadContainer(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	return p.service.loadContainer(ctx, containerID)
}

// LoadHost 读取执行节点配置。
func (p containerServicePort) LoadHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	return p.service.loadHost(ctx, hostID)
}

// taskStorePort 把 storage.Store 适配为执行环境的任务端口。
type taskStorePort struct {
	store storage.Store
}

// GetTask 读取任务。
func (p taskStorePort) GetTask(ctx context.Context, id domain.ID) (*domain.Task, error) {
	return p.store.GetTask(ctx, id)
}

// GetAttempt 读取执行尝试。
func (p taskStorePort) GetAttempt(
	ctx context.Context,
	id domain.ID,
) (*domain.Attempt, error) {
	return p.store.GetAttempt(ctx, id)
}

// UpdateTaskIfStatus 在状态前置条件下写入任务。
func (p taskStorePort) UpdateTaskIfStatus(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
) error {
	return p.store.UpdateTaskIfStatus(ctx, task, expected)
}

// AppendEvent 追加任务事件。
//
// 执行环境只用它记录"容器已创建但未能写回任务"这类需要人工处置的现场，
// 不通过事件推进任何状态。
func (p taskStorePort) AppendEvent(
	ctx context.Context,
	event domain.Event,
) (domain.Event, error) {
	return p.store.AppendEvent(ctx, event)
}

// 编译期确认适配器满足执行环境契约。
var _ execenv.Provider = (*ExecutionEnvironmentService)(nil)
var _ ExecutionEnvironmentContainerPort = containerServicePort{}
var _ ExecutionEnvironmentTaskStore = taskStorePort{}

// Kind 返回该服务实现的执行环境类型。
func (s *ExecutionEnvironmentService) Kind() execenv.Kind {
	return execenv.KindRemoteContainer
}

// Status 查询执行环境当前状态。
//
// 查询只读：不启动、不停止、不接管容器。容器记录不存在时返回"未运行"，
// 而不是把它当成"状态正常"。
func (s *ExecutionEnvironmentService) Status(
	ctx context.Context,
	environment execenv.Environment,
) (execenv.Status, error) {
	if err := environment.Validate(); err != nil {
		return execenv.StatusUnknown, err
	}
	ref := environment.Container()
	detail, err := s.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: ref.ContainerID,
		Sync:        true,
	})
	if err != nil {
		if isContainerMissing(err) {
			return execenv.StatusNotRunning, nil
		}
		return execenv.StatusUnknown, err
	}
	if detail.Container.Detached {
		// 已脱离管理的容器不再由 AgentFlow 自动执行命令，也不再被自动启停。
		return execenv.StatusNotRunning, nil
	}
	if detail.Container.State == domain.ContainerStateRemoved {
		return execenv.StatusReleased, nil
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		return execenv.StatusNotRunning, nil
	}
	return execenv.StatusReady, nil
}

// Exec 在环境内执行一条命令。
//
// 执行前核验容器受管理且正在运行：容器不可用时返回明确错误，绝不回退到服务
// 进程的当前目录执行任务代码。
func (s *ExecutionEnvironmentService) Exec(
	ctx context.Context,
	environment execenv.Environment,
	spec execenv.ExecSpec,
) (execenv.ExecResult, error) {
	if err := environment.Validate(); err != nil {
		return execenv.ExecResult{}, err
	}
	resolved, err := spec.Resolve(environment)
	if err != nil {
		return execenv.ExecResult{}, err
	}
	ref := environment.Container()
	container, err := s.containers.LoadContainer(ctx, ref.ContainerID)
	if err != nil {
		return execenv.ExecResult{}, err
	}
	if container.DockerID != ref.DockerID {
		return execenv.ExecResult{}, fmt.Errorf(
			"%w: 容器记录中的 Docker 容器 ID 已变化: 记录 %s，环境 %s",
			execenv.ErrStateUncertain,
			container.DockerID,
			ref.DockerID,
		)
	}
	if container.HostID.String() != ref.HostID {
		return execenv.ExecResult{}, fmt.Errorf(
			"%w: 容器所属节点与执行环境不一致",
			execenv.ErrInvalidValue,
		)
	}
	run, runErr := s.containers.Exec(ctx, ExecRequest{
		ContainerID:    container.ID.String(),
		Command:        resolved.Argv(),
		WorkDir:        resolved.WorkDir,
		Env:            resolved.Env,
		Timeout:        resolved.Timeout,
		MaxOutputBytes: resolved.MaxOutputBytes,
		Stdin:          resolved.Stdin,
	})
	result := convertExecEnvResult(run.Result, resolved)
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

// convertExecEnvResult 把容器命令结果转换为执行环境结果。
func convertExecEnvResult(
	run docker.CommandResult,
	spec execenv.ExecSpec,
) execenv.ExecResult {
	result := execenv.ExecResult{
		Command:    strings.Join(spec.Argv(), " "),
		ExitCode:   run.ExitCode,
		Stdout:     run.Stdout,
		Stderr:     run.Stderr,
		Truncated:  run.Truncated,
		Duration:   run.Duration,
		Started:    run.Started,
		Certain:    run.Certain,
		StdinBytes: run.Stdin.Bytes,
		StdinError: run.Stdin.Error,
	}
	switch {
	case run.TimedOut():
		result.Status = execenv.ExecStatusTimeout
	case run.Cancelled():
		result.Status = execenv.ExecStatusCancelled
	case !run.Certain:
		result.Status = execenv.ExecStatusUnknown
	case run.Certain && run.ExitCode == 0 && run.Started:
		result.Status = execenv.ExecStatusSucceeded
	case run.Started:
		result.Status = execenv.ExecStatusExitError
	default:
		result.Status = execenv.ExecStatusConnectionError
	}
	return result
}

// Release 按处置方式释放执行环境拥有的资源。
//
// 处置方式与既有容器生命周期策略一致：成功删除、失败保留、中断默认保留。
// 已脱离管理的容器不会被自动删除或重新接管；容器已不存在时按删除成功处理。
func (s *ExecutionEnvironmentService) Release(
	ctx context.Context,
	environment execenv.Environment,
	request execenv.ReleaseRequest,
) (execenv.ReleaseResult, error) {
	if err := environment.Validate(); err != nil {
		return execenv.ReleaseResult{}, err
	}
	retention := request.Retention
	if retention == "" {
		decided, err := execenv.DecideRetention(request.Outcome, request.KeepInterrupted)
		if err != nil {
			return execenv.ReleaseResult{}, err
		}
		retention = decided
	}
	if err := retention.Validate(); err != nil {
		return execenv.ReleaseResult{}, err
	}
	ref := environment.Container()
	result := execenv.ReleaseResult{Retention: retention}
	switch retention {
	case execenv.RetentionDelete:
		return s.releaseDelete(ctx, ref, request, result)
	case execenv.RetentionDetach:
		return s.releaseDetach(ctx, ref, result)
	default:
		container, err := s.containers.LoadContainer(ctx, ref.ContainerID)
		if err != nil {
			result.Err = err
			return result, err
		}
		result.ContainerRetained = true
		result.Detail = describeRetained(container, request.Note)
		return result, nil
	}
}

// releaseDelete 删除容器与任务级临时资源。
func (s *ExecutionEnvironmentService) releaseDelete(
	ctx context.Context,
	ref execenv.ContainerRef,
	request execenv.ReleaseRequest,
	result execenv.ReleaseResult,
) (execenv.ReleaseResult, error) {
	// 删除前核验管理状态：已脱离管理的容器不再由 AgentFlow 自动处置，
	// 必须先由用户显式接管，不能借释放流程重新获得管理权。
	container, loadErr := s.containers.LoadContainer(ctx, ref.ContainerID)
	if loadErr != nil {
		if isContainerMissing(loadErr) {
			result.ContainerRemoved = true
			result.TaskResourcesRemoved = true
			result.Detail = "容器已不存在，按删除成功处理"
			return result, nil
		}
		result.Err = loadErr
		return result, loadErr
	}
	if err := ensureManaged(container); err != nil {
		result.Err = err
		return result, err
	}
	detail, err := s.containers.DeleteContainer(ctx, DeleteContainerRequest{
		ContainerID: ref.ContainerID,
		Reason:      endReasonOf(request),
	})
	if err != nil {
		// 容器已不存在时按删除成功处理，避免留下"永远删不掉"的记录。
		if isContainerMissing(err) {
			result.ContainerRemoved = true
			result.TaskResourcesRemoved = true
			result.Detail = "容器已不存在，按删除成功处理"
			return result, nil
		}
		result.Err = err
		return result, err
	}
	result.ContainerRemoved = detail.Container.State == domain.ContainerStateRemoved
	result.TaskResourcesRemoved = result.ContainerRemoved
	result.Detail = "容器与任务级临时资源已删除；共享镜像、缓存卷与项目管理目录保留"
	return result, nil
}

// releaseDetach 保留容器现场并脱离 AgentFlow 自动管理。
func (s *ExecutionEnvironmentService) releaseDetach(
	ctx context.Context,
	ref execenv.ContainerRef,
	result execenv.ReleaseResult,
) (execenv.ReleaseResult, error) {
	detail, err := s.containers.DetachContainer(ctx, DetachContainerRequest{
		ContainerID: ref.ContainerID,
	})
	if err != nil {
		result.Err = err
		return result, err
	}
	result.ContainerRetained = true
	result.Detail = fmt.Sprintf(
		"容器 %s 已保留并脱离 AgentFlow 自动管理",
		detail.Container.Name,
	)
	return result, nil
}

// endReasonOf 依据任务结果推导容器生命周期结束原因。
func endReasonOf(request execenv.ReleaseRequest) domain.ContainerEndReason {
	switch request.Outcome {
	case domain.ContainerOutcomeSucceeded:
		return domain.ContainerEndReasonTaskSucceeded
	case domain.ContainerOutcomeFailed:
		return domain.ContainerEndReasonTaskFailed
	case domain.ContainerOutcomeInterrupted:
		return domain.ContainerEndReasonInterrupted
	default:
		return domain.ContainerEndReasonManual
	}
}

// describeRetained 生成"保留容器"的可读说明。
func describeRetained(container domain.Container, note string) string {
	detail := fmt.Sprintf("容器 %s 已保留，状态 %s", container.Name, container.State)
	if container.Detached {
		detail += "（已脱离 AgentFlow 自动管理）"
	}
	if trimmed := strings.TrimSpace(note); trimmed != "" {
		detail += "：" + trimmed
	}
	return detail
}

// isContainerMissing 判断错误是否表示容器在远程已不存在。
func isContainerMissing(err error) bool {
	if err == nil {
		return false
	}
	code := docker.ErrorCodeOf(err)
	return code == docker.CodeContainerNotFound || code == docker.CodeOwnershipMismatch
}

// debug 输出调试日志；未配置日志器时忽略。
func (s *ExecutionEnvironmentService) debug(
	ctx context.Context,
	message string,
	attrs ...slog.Attr,
) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Slog().LogAttrs(ctx, slog.LevelDebug, message, attrs...)
}

// requireHost 读取并校验执行节点，供准备阶段在做任何远程操作前给出明确结论。
func (s *ExecutionEnvironmentService) requireHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	host, err := s.containers.LoadHost(ctx, hostID)
	if err != nil {
		return domain.ExecutionHost{}, err
	}
	if err := EnsureHostUsableForRemoteTask(host); err != nil {
		return domain.ExecutionHost{}, err
	}
	return host, nil
}

// resolveProjectManagementDir 返回项目的远程管理目录。
//
// 未关联项目时使用固定占位名：该目录只存放项目配置与构建资源，
// 不存放项目源代码。
func resolveProjectManagementDir(layout docker.Layout, projectID string) string {
	trimmed := strings.TrimSpace(projectID)
	if trimmed == "" {
		trimmed = "unassigned"
	}
	return layout.ProjectDir(trimmed)
}

// EnvironmentFor 依据已登记的容器与准备请求构造执行环境。
//
// 它用于"环境已经准备好、只需要执行命令或释放资源"的场景（例如命令行工具与
// 后续的工作流步骤）。容器标识必须属于同一执行节点，且必须带有 Docker 容器 ID：
// 缺少任一项都返回明确错误，而不是退化到其他目录执行。
func (s *ExecutionEnvironmentService) EnvironmentFor(
	ctx context.Context,
	containerID string,
	spec execenv.PrepareSpec,
) (execenv.Environment, error) {
	if err := spec.Validate(); err != nil {
		return execenv.Environment{}, err
	}
	container, err := s.containers.LoadContainer(ctx, containerID)
	if err != nil {
		return execenv.Environment{}, err
	}
	if container.HostID.String() != spec.HostID {
		return execenv.Environment{}, fmt.Errorf(
			"%w: 容器 %s 属于节点 %s，与请求的节点 %s 不一致",
			execenv.ErrInvalidValue,
			container.ID,
			container.HostID,
			spec.HostID,
		)
	}
	if err := ensureManaged(container); err != nil {
		return execenv.Environment{}, err
	}
	workspace, err := execenv.NewWorkspace(spec.WorkDir, spec.SessionDir, nil)
	if err != nil {
		return execenv.Environment{}, err
	}
	ref, err := execenv.ContainerRefFrom(container)
	if err != nil {
		return execenv.Environment{}, err
	}
	return execenv.NewEnvironment(
		execenv.KindRemoteContainer,
		workspace,
		ref,
		time.Now().UTC(),
	)
}
