package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/workflow"
)

// WorkflowService 把容器、代码获取、Agent、命令与代码保存能力适配为工作流引擎依赖。
//
// 每个适配器只做协议转换：业务判断（是否重试、任务是否成功、容器如何处置）一律由
// workflow 包负责，本层不重复实现状态机。
type WorkflowService struct {
	containers *ContainerService
	sessions   *SessionService
	store      storage.Store
	logger     *slog.Logger
	// credentials 提供本次运行所需的 SSH 凭据，只存在于内存中。
	credentials sshclient.CredentialProvider
	// commandTimeout 是单条容器内命令的默认超时。
	commandTimeout time.Duration
}

// WorkflowOptions 是构造工作流服务的输入。
type WorkflowOptions struct {
	// Containers 是容器服务，必填。
	Containers *ContainerService
	// Sessions 是会话记录服务，必填。
	Sessions *SessionService
	// Store 是持久化入口，必填。
	Store storage.Store
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Credentials 提供 SSH 凭据，可为空（Agent 认证方式不需要）。
	Credentials sshclient.CredentialProvider
	// CommandTimeout 覆盖容器内命令默认超时。
	CommandTimeout time.Duration
}

// NewWorkflowService 构造工作流服务。
func NewWorkflowService(options WorkflowOptions) (*WorkflowService, error) {
	if options.Containers == nil {
		return nil, fmt.Errorf("工作流服务初始化失败: 容器服务不能为空")
	}
	if options.Sessions == nil {
		return nil, fmt.Errorf("工作流服务初始化失败: 会话服务不能为空")
	}
	if options.Store == nil {
		return nil, fmt.Errorf("工作流服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := options.CommandTimeout
	if timeout <= 0 {
		timeout = defaultWorkflowCommandTimeout
	}
	return &WorkflowService{
		containers:     options.Containers,
		sessions:       options.Sessions,
		store:          options.Store,
		logger:         logger,
		credentials:    options.Credentials,
		commandTimeout: timeout,
	}, nil
}

// defaultWorkflowCommandTimeout 是工作流容器内命令的默认超时。
const defaultWorkflowCommandTimeout = 30 * time.Minute

// Ports 返回工作流引擎所需的依赖集合。
func (s *WorkflowService) Ports() workflow.Ports {
	return workflow.Ports{
		Containers: &workflowContainers{service: s},
		Source:     &workflowSource{service: s},
		Agents:     &workflowAgents{service: s},
		Commands:   &workflowCommands{service: s},
		CodeSaver:  &workflowCodeSaver{service: s},
	}
}

// workflowContainers 把容器服务适配为 workflow.ContainerPort。
type workflowContainers struct {
	service *WorkflowService
}

// Create 创建并启动任务容器。
func (c *workflowContainers) Create(
	ctx context.Context,
	spec workflow.ContainerSpec,
) (workflow.ContainerInfo, error) {
	versionNumber := 0
	if label := strings.TrimSpace(spec.VersionLabel); label != "" {
		parsed, err := domain.ParseVersionLabel(label)
		if err != nil {
			return workflow.ContainerInfo{}, fmt.Errorf("解析环境版本失败: %w", err)
		}
		versionNumber = parsed
	}
	attemptID := spec.AttemptID
	detail, err := c.service.containers.CreateContainer(ctx, CreateContainerRequest{
		MachineID:     spec.HostID,
		Ref:           spec.Ref,
		VersionNumber: versionNumber,
		ProjectID:     spec.ProjectID,
		Caches:        spec.Caches,
		TaskID:        &spec.TaskID,
		AttemptID:     attemptID,
		WorkDir:       spec.WorkDir,
		Credentials:   c.service.credentials,
	})
	if err != nil {
		return workflow.ContainerInfo{}, err
	}
	return containerInfo(detail), nil
}

// Inspect 核验容器实际状态。
func (c *workflowContainers) Inspect(
	ctx context.Context,
	containerID string,
) (workflow.ContainerInfo, error) {
	detail, err := c.service.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: containerID,
		Credentials: c.service.credentials,
		Sync:        true,
	})
	if err != nil {
		// 容器在远程已不存在属于可确定的事实，不是状态不确定：
		// 按错误类别判断，避免依赖错误文本。
		if docker.ErrorCodeOf(err) == docker.CodeContainerNotFound {
			return workflow.ContainerInfo{ID: containerID, Removed: true}, nil
		}
		return workflow.ContainerInfo{}, err
	}
	return containerInfo(detail), nil
}

// Dispose 按处置方式处理容器。
func (c *workflowContainers) Dispose(
	ctx context.Context,
	containerID string,
	disposition domain.RestartDisposition,
) error {
	if err := disposition.Validate(); err != nil {
		return err
	}
	switch disposition {
	case domain.RestartDispositionDelete:
		_, err := c.service.containers.DeleteContainer(ctx, DeleteContainerRequest{
			ContainerID: containerID,
			Reason:      domain.ContainerEndReasonTaskSucceeded,
			Credentials: c.service.credentials,
		})
		return err
	default:
		_, err := c.service.containers.DetachContainer(ctx, DetachContainerRequest{
			ContainerID: containerID,
			Credentials: c.service.credentials,
		})
		return err
	}
}

// containerInfo 把容器详情转换为工作流的最小投影。
func containerInfo(detail ContainerDetail) workflow.ContainerInfo {
	info := workflow.ContainerInfo{
		ID:       detail.Container.ID.String(),
		DockerID: detail.Container.DockerID,
		HostID:   detail.Container.HostID,
		Detached: detail.Container.Detached,
		Removed:  detail.Container.State == domain.ContainerStateRemoved,
	}
	if detail.Inspect != nil {
		info.Running = detail.Inspect.Running
	}
	return info
}

// workflowSource 把代码获取能力适配为 workflow.SourcePort。
type workflowSource struct {
	service *WorkflowService
}

// Fetch 在任务容器内获取仓库代码。
func (s *workflowSource) Fetch(
	ctx context.Context,
	spec workflow.SourceSpec,
) (workflow.SourceInfo, error) {
	result, err := s.service.containers.FetchSource(ctx, FetchSourceRequest{
		ContainerID:   spec.ContainerID,
		TaskID:        spec.TaskID,
		RepositoryURL: spec.RepositoryURL,
		BaseBranch:    spec.BaseBranch,
		WorkDir:       spec.WorkDir,
		Credentials:   s.service.credentials,
	})
	if err != nil {
		return workflow.SourceInfo{}, classifySourceError(err)
	}
	return workflow.SourceInfo{
		Repository: result.Repository,
		BaseBranch: result.BaseBranch,
		BaseCommit: result.BaseCommit,
		TaskBranch: result.TaskBranch,
		Commit:     result.Commit,
		WorkDir:    result.WorkDir,
	}, nil
}

// classifySourceError 保留代码获取的错误类别，便于诊断。
func classifySourceError(err error) error {
	code := source.ErrorCodeOf(err)
	if code == source.CodeOK {
		return err
	}
	return fmt.Errorf("代码获取失败(%s): %w", code, err)
}

// workflowAgents 把容器内 Agent 调用能力适配为 workflow.AgentPort。
type workflowAgents struct {
	service *WorkflowService
}

// Run 执行一次 Agent 调用并把 Session 元数据写入数据库。
//
// 每次调用都使用新的 InvocationID 与独立 Session 文件；失败、超时与取消同样会
// 写入一条真实状态的会话记录，不会因为调用异常而丢失诊断信息。
func (a *workflowAgents) Run(
	ctx context.Context,
	invocation workflow.AgentInvocation,
) (workflow.AgentOutcome, error) {
	if invocation.Scope.StepID == nil || invocation.Scope.AttemptID == nil {
		return workflow.AgentOutcome{}, fmt.Errorf(
			"Agent 调用失败: 必须给出步骤与尝试归属",
		)
	}
	result, err := a.service.containers.RunAgent(ctx, ContainerAgentRequest{
		ContainerID:  invocation.ContainerID,
		Credentials:  a.service.credentials,
		TaskID:       invocation.Scope.TaskID.String(),
		StepID:       invocation.Scope.StepID.String(),
		AttemptID:    invocation.Scope.AttemptID.String(),
		RoleType:     invocation.Role.Type.String(),
		Model:        invocation.Role.Model,
		SystemPrompt: invocation.Role.SystemPrompt,
		Prompt:       invocation.Prompt,
		SessionRoot:  a.service.sessions.LocalRoot(),
		InvocationID: agent.NewInvocationID(),
		Timeout:      invocation.Timeout,
	})
	outcome := workflow.AgentOutcome{Result: result.Result}
	session, recordErr := a.service.sessions.Record(ctx, RecordRequest{
		Scope:     invocation.Scope,
		Role:      invocation.Role,
		SessionID: result.Result.SessionID,
		Result:    result.Result,
	})
	if recordErr != nil {
		// 会话元数据写入失败不能被忽略：它是恢复与诊断的唯一入口。
		return outcome, errors.Join(err, recordErr)
	}
	outcome.Session = session
	if err != nil {
		return outcome, err
	}
	return outcome, nil
}

// workflowCommands 把容器内命令执行能力适配为 workflow.CommandPort。
type workflowCommands struct {
	service *WorkflowService
}

// Exec 在任务容器内执行一条验证命令。
//
// 命令以参数切片传入，由容器执行层负责转义；结果区分"非零退出码正常结束"与
// "超时、取消或结果无法确认"。
func (c *workflowCommands) Exec(
	ctx context.Context,
	containerID string,
	run workflow.CommandRun,
) (domain.CommandResult, error) {
	command := append([]string{run.Command.Command}, run.Command.Args...)
	timeout := run.Timeout
	if timeout <= 0 {
		timeout = c.service.commandTimeout
	}
	result, err := c.service.containers.Exec(ctx, ExecRequest{
		ContainerID: containerID,
		Command:     command,
		Timeout:     timeout,
		Credentials: c.service.credentials,
	})
	converted := domain.CommandResult{
		Name:             run.Command.Name,
		Command:          strings.Join(command, " "),
		Required:         run.Command.IsRequired(),
		AllowTimeoutPass: run.Command.AllowTimeoutPass,
		AllowedExitCodes: run.Command.AllowedExitCodes,
		ExitCode:         result.Result.ExitCode,
		Stdout:           result.Result.Stdout,
		Stderr:           result.Result.Stderr,
		TimedOut:         result.Result.TimedOut(),
		Cancelled:        result.Result.Cancelled(),
		Certain:          result.Result.Certain,
		Duration:         result.Result.Duration,
		Truncated:        result.Result.Truncated,
	}
	return converted, err
}

// workflowCodeSaver 把代码保存能力适配为 workflow.CodeSavePort。
type workflowCodeSaver struct {
	service *WorkflowService
}

// Save 执行一次代码保存。
func (s *workflowCodeSaver) Save(
	ctx context.Context,
	spec workflow.CodeSaveSpec,
) (workflow.CodeSaveOutcome, error) {
	result, err := s.service.containers.SaveCode(ctx, CodeSaveRequest{
		ContainerID:    spec.ContainerID,
		ProjectID:      spec.ProjectID,
		TaskID:         spec.TaskID,
		AttemptID:      spec.AttemptID,
		WorkDir:        spec.WorkDir,
		BaseBranch:     spec.BaseBranch,
		MergeAfterPush: spec.MergeAfterPush,
		Credentials:    s.service.credentials,
	})
	return workflow.CodeSaveOutcome{Record: result.Result.Record}, err
}

// WorkflowOptionsFromConfig 从配置推导工作流默认参数。
func WorkflowOptionsFromConfig(cfg *config.Config) (int, int) {
	if cfg == nil {
		return workflow.DefaultMaxParallel, 0
	}
	return cfg.Scheduler.MaxConcurrentTasks, cfg.Scheduler.MaxRetriesPerStep
}
