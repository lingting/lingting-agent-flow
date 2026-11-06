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
	// containerStateReloadTimeout 是终止流程结束后重新读取本地容器记录的时间上限。
	//
	// 该读取只访问本地 Store，不需要远程连接；给出明确上限是为了避免数据库异常时把已经
	// 结束的调用挂在一次读取上。
	containerStateReloadTimeout = 5 * time.Second
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
	// RoleType 是执行本次调用的角色类型：它决定系统固定的能力上限，未识别的类型会
	// 阻止本次调用，不会回退到开发者权限。
	RoleType string
	// Model 是 Pi CLI 使用的模型标识，必须显式给出。
	Model string
	// SystemPrompt 是追加到 Pi 默认系统提示词之后的内容。
	SystemPrompt string
	// ToolsPolicy 是本次调用使用的角色工具策略，必须与 Session 快照取自同一份角色配置。
	//
	// 为空值表示该角色没有任何工具能力：本方法不会用默认开发权限兜底。
	ToolsPolicy domain.ToolsPolicy
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
	// StableSession 是跨轮复用的稳定 Session 位置，为空时每次调用使用独立 Session 文件。
	//
	// 规划角色用它把多轮调用落在同一个容器内 Session 文件与本机归档目录上；路径由
	// 规划 Agent 服务生成，调用方不提交任意路径。
	StableSession *agent.StableSessionTarget
	// PlanningSessionID 是本次调用所属的规划会话标识，普通任务调用留空。
	PlanningSessionID string
	// StopContainerOnUncertain 表示本次调用启用规划容器终止策略。
	//
	// 只有独占一个规划容器、且该容器不承载其他业务执行的规划角色调用才设置它：Pi 已经
	// 启动但执行结果无法确认时，容器服务会停止该容器并核验实际状态。普通任务容器可能
	// 承载其他执行，因此普通任务调用必须保持零值。
	StopContainerOnUncertain bool
	// PiOptions 覆盖 Pi Runner 的默认配置。
	PiOptions pi.Options
}

// ContainerAgentResult 是一次容器内 Agent 调用的结果。
type ContainerAgentResult struct {
	// Container 是容器登记记录。
	//
	// 触发过远端终止流程时它是重新读取的持久化记录（stopped 或 unknown），不是调用前的
	// 内存副本；读取失败时它是最后一份可用记录，由 ContainerStateReloadError 说明。
	Container domain.Container
	// Result 是 Runner 返回的执行结果。
	Result agent.RunResult
	// SessionRoot 是本次调用使用的本机 Session 根目录。
	SessionRoot string
	// ContainerStateReloadError 是"终止后读回本地容器记录失败"的可诊断说明。
	//
	// 它只描述本地读取失败：远端终止结论仍以 Result.RemoteTerminationConfirmed 与
	// Result.RemoteTerminationError 为准，不得把本地读取失败误报成远端进程终止失败。
	ContainerStateReloadError string
}

// RunAgent 在受管理的任务容器内部执行一次 Pi 调用并把 Session 保存到本机。
//
// 容器只被核验与使用，不会被启动、删除或重新接管：生命周期仍由容器管理层决定。
// 唯一的例外由调用方显式声明：启用 StopContainerOnUncertain 的规划调用在 Pi 主命令已经
// 启动但执行结果无法确认时，会停止该独占容器并以实际检查结果核验终止；容器记录、快照卷
// 与运行时绑定全部保留，下一次调用可以按原容器 ID 重新启动它。普通任务调用不启用该策略，
// 因此不会因为一次调用取消而停止可能承载其他执行的容器。本方法返回后调用方已经拿到真实的
// Session 持久化结果与远端终止结论，可以据此决定后续处置。
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
	// 工具列表在这里统一计算一次：三条调用路径都只提交角色类型与策略，不各自映射工具。
	// 计算失败必须阻止本次调用，不能改用 Pi 的默认工具列表。
	tools, err := domain.EffectivePiTools(
		domain.RoleType(strings.TrimSpace(request.RoleType)),
		request.ToolsPolicy,
	)
	if err != nil {
		return ContainerAgentResult{}, fmt.Errorf("容器内 Agent 调用失败: %w", err)
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
			executor := &containerSessionExecutor{
				session:   session,
				service:   s,
				container: current,
			}
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
				SessionID:     sessionID,
				TaskID:        strings.TrimSpace(request.TaskID),
				StepID:        strings.TrimSpace(request.StepID),
				AttemptID:     strings.TrimSpace(request.AttemptID),
				RoleType:      strings.TrimSpace(request.RoleType),
				Model:         strings.TrimSpace(request.Model),
				SystemPrompt:  request.SystemPrompt,
				Tools:         tools,
				Prompt:        request.Prompt,
				WorkingDir:    workDir,
				SessionRoot:   sessionRoot,
				InvocationID:  invocationID,
				Timeout:       request.Timeout,
				StableSession: request.StableSession,
				Container: &agent.ContainerTarget{
					ContainerID: current.ID.String(),
					DockerID:    current.DockerID,
					WorkDir:     workDir,
					SessionDir:  strings.TrimSpace(request.SessionDir),
				},
				// 终止策略只随调用方声明传播：普通任务调用既不给出规划会话标识，
				// 也不启用停止容器。
				PlanningSessionID:        strings.TrimSpace(request.PlanningSessionID),
				StopContainerOnUncertain: request.StopContainerOnUncertain,
			}
			result, runErr := runner.Run(ctx, runRequest, func(event agent.Event) {
				s.logAgentEvent(ctx, event)
			})
			reloadErr := ""
			if result.RemoteTerminationConfirmed || strings.TrimSpace(result.RemoteTerminationError) != "" {
				// 触发过远端终止流程：读回本地记录，避免把中断前的运行状态当成现状。停止成功
				// 时记录是 stopped，无法确认时记录是 unknown，两种情况都必须反映真实落库结果。
				//
				// 触发终止的调用通常已经取消（用户取消、Server 停止或超时），原始上下文读取会
				// 立即失败，因此改用独立的有界上下文；它只读本地 Store，不重新建立远程连接。
				reloadCtx, cancelReload := context.WithTimeout(
					context.WithoutCancel(ctx),
					containerStateReloadTimeout,
				)
				reloaded, loadErr := s.loadContainer(reloadCtx, request.ContainerID)
				cancelReload()
				if loadErr == nil {
					current = reloaded
				} else {
					// 读取失败不得伪造成已持久化的状态：保留最后一份可用记录并如实报告。
					reloadErr = loadErr.Error()
					s.debug(
						ctx,
						"读取终止后的容器记录失败",
						slog.String("container", request.ContainerID),
						slog.String("error", loadErr.Error()),
					)
				}
			}
			outcome = ContainerAgentResult{
				Container:                 current,
				Result:                    result,
				SessionRoot:               sessionRoot,
				ContainerStateReloadError: reloadErr,
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
// 它只在单次应用服务调用内有效：连接与 SFTP 通道由 withRemoteDocker 统一关闭。
//
// 它也承载"执行结果不确定时终止独占容器"的能力：终止发生在 ContainerService.RunAgent
// 的临界区内，因此本类型必须持有容器服务与本次调用使用的容器记录，才能复用已持有的
// 远程会话与容器锁。
type containerSessionExecutor struct {
	session *remoteDocker
	// service 是重放停止流程的容器服务，为空时不能终止容器。
	service *ContainerService
	// container 是本次调用核验过的容器记录。
	container domain.Container
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
		Started:         run.Started,
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

// UploadFromLocal 把本机文件上传到远程宿主机临时路径。
//
// 目标位于受控的远程临时目录，权限按保守模式写入；同一调用标识下的中转文件属于本次
// 调用自己，因此允许覆盖。
func (e *containerSessionExecutor) UploadFromLocal(
	ctx context.Context,
	localPath, hostPath string,
) error {
	if e == nil || e.session == nil {
		return fmt.Errorf("上传文件失败: 远程会话不可用")
	}
	if !strings.HasPrefix(hostPath, "/") {
		return fmt.Errorf("上传文件失败: 宿主机路径 %q 必须是绝对路径", hostPath)
	}
	if err := e.session.transfer.MkdirAll(ctx, path.Dir(hostPath)); err != nil {
		return fmt.Errorf("创建宿主机临时目录失败: %w", err)
	}
	if err := e.session.transfer.Upload(ctx, localPath, hostPath, sshclient.UploadOptions{
		Overwrite:    true,
		Mode:         0o600,
		CreateParent: true,
	}); err != nil {
		return fmt.Errorf("上传文件失败: %w", err)
	}
	return nil
}

// CopyToContainer 用 docker cp 把远程宿主机文件复制到容器内路径。
func (e *containerSessionExecutor) CopyToContainer(
	ctx context.Context,
	dockerID, hostPath, containerPath string,
) error {
	if e == nil || e.session == nil {
		return fmt.Errorf("写入容器文件失败: 远程会话不可用")
	}
	if strings.TrimSpace(dockerID) == "" {
		return fmt.Errorf("写入容器文件失败: 容器 ID 不能为空")
	}
	if !strings.HasPrefix(hostPath, "/") {
		return fmt.Errorf("写入容器文件失败: 宿主机路径 %q 必须是绝对路径", hostPath)
	}
	if !strings.HasPrefix(containerPath, "/") {
		return fmt.Errorf("写入容器文件失败: 容器内路径 %q 必须是绝对路径", containerPath)
	}
	run, runErr := e.session.runner.CopyToContainer(
		ctx,
		dockerID,
		hostPath,
		containerPath,
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

// TerminateContainer 停止本次调用独占的容器并核验其实际状态。
//
// 调用发生在 ContainerService.RunAgent 的临界区内：容器锁与远程会话都已由该路径持有。
// 因此这里只能走持锁内部的停止流程，不能调用公开的 ContainerService.StopContainer —
// 后者会再次获取同一把容器锁，造成自锁死锁。
//
// 停止成功与否只由停止后的实际检查结果决定：本次调用使用的容器登记记录与 Docker 容器
// ID 都必须与再次复核的结果一致，规划会话标签也必须与请求一致，任一不满足都拒绝停止。
func (e *containerSessionExecutor) TerminateContainer(
	ctx context.Context,
	request agent.ContainerTerminationRequest,
) (agent.ContainerTerminationResult, error) {
	if e == nil || e.session == nil || e.service == nil {
		return agent.ContainerTerminationResult{}, fmt.Errorf("终止容器失败: 远程会话不可用")
	}
	if strings.TrimSpace(request.ContainerID) != e.container.ID.String() {
		return agent.ContainerTerminationResult{}, fmt.Errorf(
			"终止容器失败: 请求的容器 %q 与本次调用使用的容器 %s 不一致",
			request.ContainerID,
			e.container.ID,
		)
	}
	if strings.TrimSpace(request.DockerID) != e.container.DockerID {
		return agent.ContainerTerminationResult{}, fmt.Errorf(
			"终止容器失败: 请求的 Docker 容器 ID %q 与本次调用使用的 %s 不一致",
			request.DockerID,
			e.container.DockerID,
		)
	}
	detail, err := e.service.stopContainerLocked(ctx, e.session, e.container, stopContainerOptions{
		TimeoutSeconds:    request.GraceSeconds,
		PlanningSessionID: request.PlanningSessionID,
	})
	if detail.Inspect != nil && !detail.Inspect.Running {
		// 已核实的远端事实优先：登记同步等收尾失败不改变"容器已停止"这一结论，错误照实返回。
		return agent.ContainerTerminationResult{Stopped: true}, err
	}
	if err != nil {
		return agent.ContainerTerminationResult{}, err
	}
	if detail.Inspect == nil {
		return agent.ContainerTerminationResult{}, fmt.Errorf(
			"终止容器失败: 容器 %s 停止后没有取得实际状态",
			e.container.ID,
		)
	}
	return agent.ContainerTerminationResult{}, fmt.Errorf(
		"终止容器失败: 容器 %s 停止后仍在运行（Docker 状态 %s）",
		e.container.ID,
		detail.Inspect.State,
	)
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
var _ agent.ContainerFileWriter = (*containerSessionExecutor)(nil)
var _ agent.ContainerTerminator = (*containerSessionExecutor)(nil)
var _ agent.ContainerGuard = (*sessionContainerGuard)(nil)
