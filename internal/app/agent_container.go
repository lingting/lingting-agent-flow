package app

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
)

// 容器内 Agent 执行的默认超时。
const (
	// containerExecTimeout 是容器内单条命令的默认超时。
	containerExecTimeout = 30 * time.Minute
	// containerCopyTimeout 是容器内文件复制与下载的默认超时。
	containerCopyTimeout = 10 * time.Minute
)

// ContainerAgentRequest 是在任务容器内执行一次 Agent 调用的输入。
type ContainerAgentRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// TaskID 是本次调用所属任务，用于确定 Session 目录归属。
	TaskID string
	// StepID 是本次调用所属步骤，可为空。
	StepID string
	// AttemptID 是本次调用所属尝试，可为空。
	AttemptID string
	// RoleType 是执行本次调用的角色类型，只用于日志与诊断。
	RoleType string
	// Model 是 Pi CLI 使用的模型标识，必须显式给出。
	Model string
	// SystemPrompt 是追加到 Pi 默认系统提示词之后的内容。
	SystemPrompt string
	// Prompt 是本次调用的完整提示词。
	Prompt string
	// SessionRoot 是本机 Session 持久化根目录，为空时使用配置数据目录。
	SessionRoot string
	// SessionID 是调用方已经创建的会话标识，为空时由本方法生成。
	//
	// 显式传入是为了让 Runner 使用的会话标识与数据库记录一致：一旦 Runner 自行
	// 生成标识，Session 文件目录与数据库记录就会指向两次不同的"会话"。
	SessionID string
	// InvocationID 是本次调用的唯一标识，为空时自动生成。
	InvocationID string
	// Timeout 是本次调用的超时时间，零值使用执行器默认值。
	Timeout time.Duration
	// WorkDir 是容器内工作目录，空值使用默认项目目录。
	//
	// 它来自任务的执行环境描述，不在本方法内硬编码：Pi 的工作目录必须与代码
	// 拉取、验证检查使用同一个目录。
	WorkDir string
	// SessionDir 是容器内临时 Session 目录，空值使用 Runner 默认目录。
	SessionDir string
	// PiOptions 覆盖 Pi Runner 的默认配置。
	PiOptions pi.Options
}

// ContainerAgentResult 是一次容器内 Agent 调用的结果。
type ContainerAgentResult struct {
	// Container 是容器登记记录。
	Container domain.Container
	// Result 是 Runner 返回的执行结果。
	Result agent.RunResult
	// SessionRoot 是本次调用使用的本机 Session 根目录。
	SessionRoot string
}

// RunAgent 在受管理的任务容器内部执行一次 Pi 调用并把 Session 保存到本机。
//
// 容器只被核验与使用，不会被启动、停止、删除或重新接管：生命周期仍由容器管理层
// 决定。本方法返回后调用方已经拿到真实的 Session 持久化结果，可以据此决定容器处置。
func (s *ContainerService) RunAgent(
	ctx context.Context,
	request ContainerAgentRequest,
) (ContainerAgentResult, error) {
	if strings.TrimSpace(request.Model) == "" {
		return ContainerAgentResult{}, fmt.Errorf("容器内 Agent 调用失败: 模型标识不能为空")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return ContainerAgentResult{}, fmt.Errorf("容器内 Agent 调用失败: 提示词不能为空")
	}
	container, err := s.loadContainer(ctx, request.ContainerID)
	if err != nil {
		return ContainerAgentResult{}, err
	}
	if err := ensureManaged(container); err != nil {
		return ContainerAgentResult{}, fmt.Errorf("容器内 Agent 调用失败: %w", err)
	}
	sessionRoot := strings.TrimSpace(request.SessionRoot)
	if sessionRoot == "" {
		return ContainerAgentResult{}, fmt.Errorf(
			"容器内 Agent 调用失败: 必须给出本机 Session 根目录",
		)
	}
	// 工作目录来自执行环境描述：Pi 必须在项目代码目录内运行，不能使用其他目录。
	workDir := strings.TrimSpace(request.WorkDir)
	if workDir == "" {
		workDir = domain.DefaultContainerWorkDir
	}
	if err := domain.ValidateContainerWorkDir(workDir); err != nil {
		return ContainerAgentResult{}, fmt.Errorf("容器内 Agent 调用失败: %w", err)
	}
	var outcome ContainerAgentResult
	err = s.withRemoteDocker(
		ctx,
		container.HostID.String(),
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.locks.Lock(container.ID.String())
			defer unlock()
			current, loadErr := s.loadContainer(ctx, request.ContainerID)
			if loadErr != nil {
				return loadErr
			}
			if err := ensureManaged(current); err != nil {
				return fmt.Errorf("容器内 Agent 调用失败: %w", err)
			}
			if _, err := s.observe(ctx, session, current); err != nil {
				return fmt.Errorf("执行前核验容器失败: %w", err)
			}
			executor := &containerSessionExecutor{session: session}
			guard := &sessionContainerGuard{service: s, session: session}
			runner, runnerErr := pi.NewContainerRunner(request.PiOptions, executor, guard)
			if runnerErr != nil {
				return fmt.Errorf("容器内 Agent 调用失败: %w", runnerErr)
			}
			invocationID := strings.TrimSpace(request.InvocationID)
			if invocationID == "" {
				invocationID = agent.NewInvocationID()
			}
			sessionID := strings.TrimSpace(request.SessionID)
			if sessionID == "" {
				sessionID = domain.NewID().String()
			}
			runRequest := agent.RunRequest{
				SessionID:    sessionID,
				TaskID:       strings.TrimSpace(request.TaskID),
				StepID:       strings.TrimSpace(request.StepID),
				AttemptID:    strings.TrimSpace(request.AttemptID),
				RoleType:     strings.TrimSpace(request.RoleType),
				Model:        strings.TrimSpace(request.Model),
				SystemPrompt: request.SystemPrompt,
				Prompt:       request.Prompt,
				WorkingDir:   workDir,
				SessionRoot:  sessionRoot,
				InvocationID: invocationID,
				Timeout:      request.Timeout,
				Container: &agent.ContainerTarget{
					ContainerID: current.ID.String(),
					DockerID:    current.DockerID,
					WorkDir:     workDir,
					SessionDir:  strings.TrimSpace(request.SessionDir),
				},
			}
			result, runErr := runner.Run(ctx, runRequest, func(event agent.Event) {
				s.logAgentEvent(ctx, event)
			})
			outcome = ContainerAgentResult{
				Container:   current,
				Result:      result,
				SessionRoot: sessionRoot,
			}
			if runErr != nil {
				return runErr
			}
			return nil
		},
	)
	if err != nil {
		return outcome, err
	}
	return outcome, nil
}

// logAgentEvent 把内部事件写入日志，只记录类型与摘要。
func (s *ContainerService) logAgentEvent(ctx context.Context, event agent.Event) {
	if s.logger == nil {
		return
	}
	s.logger.Slog().DebugContext(
		ctx,
		"容器内 Agent 事件",
		slog.String("session_id", event.SessionID),
		slog.String("event_type", string(event.Type)),
		slog.String("data", agent.TruncateText(strings.TrimSpace(string(event.Data)), 256)),
	)
}

// containerSessionExecutor 把一次远程 Docker 会话适配为 agent.ContainerExecutor。
//
// 它只在单次应用服务调用内有效：连接与 SFTP 通道由 withRemoteDocker 统一关闭。
type containerSessionExecutor struct {
	session *remoteDocker
}

// Exec 在容器内执行一条命令。
func (e *containerSessionExecutor) Exec(
	ctx context.Context,
	request agent.ContainerExecRequest,
) (agent.ContainerExecResult, error) {
	if e == nil || e.session == nil || e.session.runner == nil {
		return agent.ContainerExecResult{}, fmt.Errorf("容器内执行失败: 远程会话不可用")
	}
	if len(request.Command) == 0 {
		return agent.ContainerExecResult{}, fmt.Errorf("容器内执行失败: 命令不能为空")
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = containerExecTimeout
	}
	var stdin *bytes.Reader
	if len(request.Stdin) > 0 {
		stdin = bytes.NewReader(request.Stdin)
	}
	var (
		run docker.CommandResult
		err error
	)
	if stdin == nil {
		run, err = e.session.runner.ExecCommand(ctx, request.DockerID, request.Command, timeout)
	} else {
		run, err = e.session.runner.ExecCommandWithInput(
			ctx,
			request.DockerID,
			request.Command,
			stdin,
			timeout,
		)
	}
	return convertExecResult(run), err
}

// convertExecResult 把 Docker 命令结果转换为 Agent 侧的结果类型。
func convertExecResult(run docker.CommandResult) agent.ContainerExecResult {
	return agent.ContainerExecResult{
		ExitCode:        run.ExitCode,
		Stdout:          run.Stdout,
		Stderr:          run.Stderr,
		StdoutTruncated: run.Status == sshclient.CommandStatusSucceeded && run.Truncated,
		StderrTruncated: run.Truncated,
		StdinStatus:     run.Stdin.Status,
		StdinBytes:      run.Stdin.Bytes,
		StdinError:      run.Stdin.Error,
		Duration:        run.Duration,
		TimedOut:        run.TimedOut(),
		Cancelled:       run.Cancelled(),
		Certain:         run.Certain,
	}
}

// CopyFromContainer 用 docker cp 把容器内文件复制到远程宿主机临时目录。
//
// 复制目标目录会先被创建；命令通过已转义的参数构造，用户输入不会进入 shell。
func (e *containerSessionExecutor) CopyFromContainer(
	ctx context.Context,
	dockerID, containerPath, hostPath string,
) error {
	if e == nil || e.session == nil {
		return fmt.Errorf("导出容器文件失败: 远程会话不可用")
	}
	if strings.TrimSpace(dockerID) == "" {
		return fmt.Errorf("导出容器文件失败: 容器 ID 不能为空")
	}
	if !strings.HasPrefix(containerPath, "/") {
		return fmt.Errorf("导出容器文件失败: 容器内路径 %q 必须是绝对路径", containerPath)
	}
	if !strings.HasPrefix(hostPath, "/") {
		return fmt.Errorf("导出容器文件失败: 宿主机路径 %q 必须是绝对路径", hostPath)
	}
	if err := e.session.transfer.MkdirAll(ctx, path.Dir(hostPath)); err != nil {
		return fmt.Errorf("创建宿主机临时目录失败: %w", err)
	}
	run, runErr := e.session.runner.CopyFromContainer(
		ctx,
		dockerID,
		containerPath,
		hostPath,
		containerCopyTimeout,
	)
	if runErr != nil {
		return fmt.Errorf("docker cp 失败: %w", runErr)
	}
	if !run.Succeeded() {
		return fmt.Errorf("docker cp 失败（退出码 %d）: %s", run.ExitCode, run.Stderr)
	}
	return nil
}

// DownloadToLocal 把远程宿主机文件下载到本机路径。
func (e *containerSessionExecutor) DownloadToLocal(
	ctx context.Context,
	hostPath, localPath string,
	maxBytes int64,
) (int64, error) {
	if e == nil || e.session == nil {
		return 0, fmt.Errorf("下载文件失败: 远程会话不可用")
	}
	if err := e.session.transfer.Download(ctx, hostPath, localPath, sshclient.DownloadOptions{
		Overwrite:    true,
		CreateParent: true,
		MaxBytes:     maxBytes,
	}); err != nil {
		return 0, err
	}
	info, err := e.session.transfer.Stat(ctx, hostPath)
	if err != nil {
		// 下载已经成功，远程属性读取失败不影响本机文件；返回 0 让调用方按字节数判断。
		return 0, nil
	}
	return info.Size, nil
}

// RemoveHostFile 删除远程宿主机临时文件，目标不存在时视为成功。
func (e *containerSessionExecutor) RemoveHostFile(ctx context.Context, hostPath string) error {
	if e == nil || e.session == nil {
		return fmt.Errorf("清理临时文件失败: 远程会话不可用")
	}
	return e.session.transfer.RemoveAll(ctx, hostPath)
}

// sessionContainerGuard 在一次远程会话内核验容器归属与运行状态。
type sessionContainerGuard struct {
	service *ContainerService
	session *remoteDocker
}

// EnsureExecutable 核验容器存在、受管理且正在运行。
func (g *sessionContainerGuard) EnsureExecutable(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	container, err := g.service.loadContainer(ctx, containerID)
	if err != nil {
		return domain.Container{}, err
	}
	if err := ensureManaged(container); err != nil {
		return domain.Container{}, err
	}
	inspect, err := g.session.runner.InspectContainer(ctx, container.DockerID)
	if err != nil {
		return domain.Container{}, err
	}
	if err := docker.VerifyOwnership(inspect, container); err != nil {
		return domain.Container{}, err
	}
	if !inspect.Running {
		return domain.Container{}, fmt.Errorf(
			"容器 %s 当前未运行，无法执行命令",
			container.ID,
		)
	}
	return container, nil
}

// 编译期确认适配器满足 Agent 侧接口。
var _ agent.ContainerExecutor = (*containerSessionExecutor)(nil)
var _ agent.ContainerGuard = (*sessionContainerGuard)(nil)
