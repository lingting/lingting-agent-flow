package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// ListContainersRequest 是查询容器列表的输入。
type ListContainersRequest struct {
	// MachineID 限制所属执行节点，空值表示不限制。
	MachineID string
	// TaskID 限制登记记录关联的任务，空值表示不限制。
	TaskID string
	// IncludeDetached 表示包含已脱离管理的容器。
	IncludeDetached bool
	// Orphans 表示只返回"登记了任务关联但未被该任务引用"的容器。
	Orphans bool
}

// ListContainers 返回容器登记记录。
func (s *ContainerService) ListContainers(
	ctx context.Context,
	request ListContainersRequest,
) ([]domain.Container, error) {
	filter := domain.ContainerFilter{
		IncludeDetached: request.IncludeDetached,
		Orphans:         request.Orphans,
	}
	if strings.TrimSpace(request.MachineID) != "" {
		host, err := s.loadHost(ctx, request.MachineID)
		if err != nil {
			return nil, err
		}
		filter.HostID = host.ID
	}
	if raw := strings.TrimSpace(request.TaskID); raw != "" {
		taskID := domain.ID(raw)
		if err := taskID.Validate(); err != nil {
			return nil, fmt.Errorf("查询容器记录失败: %w", err)
		}
		filter.TaskID = &taskID
	}
	containers, err := s.store.ListContainers(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("查询容器记录失败: %w", err)
	}
	return containers, nil
}

// InspectContainerRequest 是查询容器详情的输入。
type InspectContainerRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// Sync 表示同时更新本地记录的 Docker 状态观测结果。
	Sync bool
}

// InspectContainer 检查容器并返回本地记录与远程观测结果。
//
// 对已脱离管理的容器只做只读观测，不恢复管理权。
func (s *ContainerService) InspectContainer(
	ctx context.Context,
	request InspectContainerRequest,
) (ContainerDetail, error) {
	container, err := s.loadContainer(ctx, request.ContainerID)
	if err != nil {
		return ContainerDetail{}, err
	}
	var detail ContainerDetail
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
			observed, inspectErr := s.observe(ctx, session, current)
			detail = ContainerDetail{Container: current}
			if inspectErr != nil {
				return inspectErr
			}
			detail.Inspect = observed
			if request.Sync {
				updated := current
				if syncErr := updated.SyncDockerState(observed.State, time.Now()); syncErr == nil {
					if saveErr := s.store.UpdateContainer(ctx, updated); saveErr == nil {
						detail.Container = updated
					}
				}
			}
			return nil
		},
	)
	if err != nil {
		return detail, err
	}
	return detail, nil
}

// observe 观测容器实际状态并核验所有权。
func (s *ContainerService) observe(
	ctx context.Context,
	session *remoteDocker,
	container domain.Container,
) (*docker.ContainerInspect, error) {
	if strings.TrimSpace(container.DockerID) == "" && strings.TrimSpace(container.Name) == "" {
		return nil, fmt.Errorf("容器记录缺少 Docker 容器 ID 与名称: %s", container.ID)
	}
	target := container.DockerID
	if strings.TrimSpace(target) == "" {
		target = container.Name
	}
	inspect, err := session.runner.InspectContainer(ctx, target)
	if err != nil {
		if docker.ErrorCodeOf(err) == docker.CodeContainerNotFound {
			return nil, fmt.Errorf("容器在远程机器上已不存在: %w", err)
		}
		return nil, fmt.Errorf("检查远程容器失败: %w", err)
	}
	if err := docker.VerifyOwnership(inspect, container); err != nil {
		return nil, fmt.Errorf("容器所有权核验失败: %w", err)
	}
	return &inspect, nil
}

// GetContainerRecord 返回容器登记记录，不连接远程机器。
//
// 命令层用它读取容器所属节点，以便按该节点的认证方式收集凭据。
func (s *ContainerService) GetContainerRecord(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	return s.loadContainer(ctx, containerID)
}

// loadContainer 读取容器登记记录。
func (s *ContainerService) loadContainer(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	id := domain.ID(strings.TrimSpace(containerID))
	if err := id.Validate(); err != nil {
		return domain.Container{}, fmt.Errorf("读取容器记录失败: %w", err)
	}
	container, err := s.store.GetContainer(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Container{}, fmt.Errorf("读取容器记录失败: 容器不存在 %s", id)
		}
		return domain.Container{}, fmt.Errorf("读取容器记录失败: %w", err)
	}
	return *container, nil
}

// ensureManaged 确认容器仍处于 AgentFlow 自动管理范围。
func ensureManaged(container domain.Container) error {
	if container.Detached {
		return fmt.Errorf(
			"容器 %s 已脱离 AgentFlow 管理，拒绝自动操作；如需处理请人工介入",
			container.ID,
		)
	}
	if container.State == domain.ContainerStateRemoved {
		return fmt.Errorf("容器 %s 已被删除", container.ID)
	}
	return nil
}

// StartContainerRequest 是启动容器的输入。
type StartContainerRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
}

// StartContainer 启动受管理容器并核验运行状态。
//
// 启动前先 inspect 远端事实：本地管理状态为 running 但容器已经退出（例如停止已生效、本地
// 记录写入失败或 Server 在写库前中断）时，先经领域状态机收敛为 stopped 并落库，再执行
// Docker start，因此不会出现 running → running 的非法迁移。远端状态不属于可安全启动的退出
// 状态（paused / restarting / dead / removing / unknown）时拒绝启动，不改写管理状态。
// 容器已经在运行时启动是幂等的：不重复提交启动命令。
func (s *ContainerService) StartContainer(
	ctx context.Context,
	request StartContainerRequest,
) (ContainerDetail, error) {
	return s.withContainerLock(ctx, request.ContainerID, func(
		ctx context.Context,
		session *remoteDocker,
		container domain.Container,
	) (ContainerDetail, error) {
		if err := ensureManaged(container); err != nil {
			return ContainerDetail{Container: container}, err
		}
		inspect, err := s.observe(ctx, session, container)
		if err != nil {
			return ContainerDetail{Container: container}, err
		}
		if err := ensureObservedDockerID(container, *inspect); err != nil {
			return ContainerDetail{Container: container, Inspect: inspect}, err
		}
		if inspect.IsActivelyRunning() {
			if container.State == domain.ContainerStateRunning {
				// 远端已在运行且本地记录一致：启动是幂等操作，不重复提交启动命令，也不改写记录。
				return ContainerDetail{Container: container, Inspect: inspect}, nil
			}
			return s.recordStartedContainer(ctx, session, container, inspect)
		}
		// 远端事实优先：本地管理状态可能与远端不一致（例如停止已生效但记录写入失败），
		// 必须先经领域状态机收敛为可启动状态，再执行 Docker start。
		reconciled, reconcileErr := s.reconcileBeforeStart(ctx, session, container, *inspect)
		if reconcileErr != nil {
			return ContainerDetail{Container: reconciled, Inspect: inspect}, reconcileErr
		}
		if err := session.runner.StartContainer(ctx, reconciled.DockerID); err != nil {
			return ContainerDetail{Container: reconciled, Inspect: inspect}, err
		}
		inspect, err = s.observe(ctx, session, reconciled)
		if err != nil {
			return ContainerDetail{Container: reconciled}, err
		}
		if !inspect.IsActivelyRunning() {
			return ContainerDetail{Container: reconciled, Inspect: inspect}, fmt.Errorf(
				"容器启动后仍未运行: Docker 状态 %s",
				inspect.State,
			)
		}
		return s.recordStartedContainer(ctx, session, reconciled, inspect)
	})
}

// StopContainerRequest 是停止容器的输入。
type StopContainerRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// TimeoutSeconds 是停止等待秒数，零值使用 10 秒。
	TimeoutSeconds int
}

// stopContainerOptions 是持锁停止流程的附加约束。
type stopContainerOptions struct {
	// TimeoutSeconds 是交给 Docker 的优雅停止宽限期秒数，零值使用 10 秒。
	TimeoutSeconds int
	// PlanningSessionID 非空时要求 Docker 规划会话标签与之一致。
	//
	// 只有规划调用会给出它：规划容器由单个 PlanningSession 独占，停止该容器才是可靠的
	// 终止边界；普通任务容器可能承载其他执行，不得因为一次调用取消而停止。
	PlanningSessionID string
}

// StopContainer 停止受管理容器，保留容器现场。
//
// 停止成功与否以停止后的实际检查结果为准：停止命令返回错误但容器确实已经停止时，按
// 已核实的远端事实收敛结果并同步本地记录与远程登记清单。
func (s *ContainerService) StopContainer(
	ctx context.Context,
	request StopContainerRequest,
) (ContainerDetail, error) {
	return s.withContainerLock(ctx, request.ContainerID, func(
		ctx context.Context,
		session *remoteDocker,
		container domain.Container,
	) (ContainerDetail, error) {
		return s.stopContainerLocked(ctx, session, container, stopContainerOptions{
			TimeoutSeconds: request.TimeoutSeconds,
		})
	})
}

// stopContainerLocked 在已持有容器锁的调用路径内停止容器并核验实际状态。
//
// 它既不获取容器锁，也不建立远程会话：调用方必须已经持有同一容器的进程内锁与远程
// 会话（例如 ContainerService.RunAgent 的执行临界区）。在临界区内部调用公开的
// StopContainer 会再次获取同一把锁并自锁，因此所有停止能力都必须收敛到本方法。
//
// 停止前的复核顺序固定：重新读取本地记录 → 核验受管理 → 核验 Docker 容器 ID →
// 核验所有权与规划会话标签。停止后的判定只依据实际观测：容器仍在运行或状态无法确认
// 时都不会被标记为已停止。
func (s *ContainerService) stopContainerLocked(
	ctx context.Context,
	session *remoteDocker,
	container domain.Container,
	options stopContainerOptions,
) (ContainerDetail, error) {
	current, err := s.loadContainer(ctx, container.ID.String())
	if err != nil {
		return ContainerDetail{Container: container}, err
	}
	if err := ensureManaged(current); err != nil {
		return ContainerDetail{Container: current}, err
	}
	observed, err := s.observe(ctx, session, current)
	if err != nil {
		return ContainerDetail{Container: current}, err
	}
	if strings.TrimSpace(current.DockerID) != "" && observed.ID != current.DockerID {
		return ContainerDetail{Container: current, Inspect: observed}, fmt.Errorf(
			"停止容器失败: 远程容器 ID 与本地记录不一致: 远程 %s，记录 %s",
			observed.ID,
			current.DockerID,
		)
	}
	if expected := strings.TrimSpace(options.PlanningSessionID); expected != "" {
		if actual := observed.Labels[docker.PlanningSessionLabelKey]; actual != expected {
			return ContainerDetail{Container: current, Inspect: observed}, fmt.Errorf(
				"停止容器失败: 容器 %s 的规划会话标签与目标会话不一致: 期望 %s，实际 %s",
				current.ID,
				expected,
				actual,
			)
		}
	}
	if observed.RuntimeCondition().ConfirmsNotRunning() {
		// 已经停止：按核验结果收敛，不重复提交停止命令。
		return s.applyStoppedContainer(ctx, session, current, *observed)
	}
	stopErr := session.runner.StopContainer(ctx, current.DockerID, options.TimeoutSeconds)
	after, inspectErr := s.observe(ctx, session, current)
	if inspectErr != nil {
		// 停止后仍无法核验：保留容器现场与定位信息，如实标记状态不确定。
		cause := errors.Join(stopErr, inspectErr)
		s.markUnknown(ctx, current, cause)
		return ContainerDetail{Container: current}, fmt.Errorf("停止容器结果无法确认: %w", cause)
	}
	// 停止完成的口径与"可以安全启动"一致：只有 Docker 明确报告 exited（或从未启动的
	// created）才能记录 stopped。暂停、重启中、dead、removing 与状态矛盾都不是"已确认退出"。
	condition := after.RuntimeCondition()
	if !condition.ConfirmsNotRunning() {
		if stopErr != nil {
			return ContainerDetail{Container: current, Inspect: after}, fmt.Errorf("停止容器失败: %w", stopErr)
		}
		if condition.IsActivelyRunning() {
			return ContainerDetail{Container: current, Inspect: after}, fmt.Errorf(
				"停止容器失败: 容器 %s 停止后仍在运行（Docker 状态 %s）",
				current.ID,
				after.State,
			)
		}
		return ContainerDetail{Container: current, Inspect: after}, fmt.Errorf(
			"停止容器失败: 容器 %s 停止后未确认退出（Docker 状态 %s，State.Running=%t）",
			current.ID,
			after.State,
			after.Running,
		)
	}
	// 停止命令报错但容器确实已经停止：以核实的远端事实收敛结果。
	return s.applyStoppedContainer(ctx, session, current, *after)
}

// applyStoppedContainer 记录已核验的停止结果并同步远程登记清单。
func (s *ContainerService) applyStoppedContainer(
	ctx context.Context,
	session *remoteDocker,
	container domain.Container,
	inspect docker.ContainerInspect,
) (ContainerDetail, error) {
	updated := container
	if updated.State != domain.ContainerStateStopped {
		// 已经记录为停止时不再重复迁移状态：重复停止必须按核验结果幂等收敛。
		if err := updated.MarkStopped(time.Now()); err != nil {
			return ContainerDetail{Container: container, Inspect: &inspect}, err
		}
	}
	if err := updated.SyncDockerState(inspect.State, time.Now()); err != nil {
		return ContainerDetail{Container: container, Inspect: &inspect}, err
	}
	if err := s.store.UpdateContainer(ctx, updated); err != nil {
		return ContainerDetail{Container: updated, Inspect: &inspect}, err
	}
	if err := s.syncRegistry(ctx, session, updated); err != nil {
		return ContainerDetail{Container: updated, Inspect: &inspect}, err
	}
	return ContainerDetail{Container: updated, Inspect: &inspect}, nil
}

// DeleteContainerRequest 是删除容器的输入。
type DeleteContainerRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// Reason 是生命周期结束原因，空值使用 manual。
	Reason domain.ContainerEndReason
}

// DeleteContainer 删除受管理容器及其任务级临时资源。
//
// 删除只影响容器与任务级临时资源，不删除共享镜像、共享缓存卷与远程项目配置目录。
// 已脱离管理的容器默认拒绝删除，必须由用户独立人工处理。
func (s *ContainerService) DeleteContainer(
	ctx context.Context,
	request DeleteContainerRequest,
) (ContainerDetail, error) {
	return s.withContainerLock(ctx, request.ContainerID, func(
		ctx context.Context,
		session *remoteDocker,
		container domain.Container,
	) (ContainerDetail, error) {
		if err := ensureManaged(container); err != nil {
			return ContainerDetail{Container: container}, err
		}
		return s.deleteLocked(ctx, session, container, request.Reason)
	})
}

// deleteLocked 在持锁状态下删除容器并更新记录。
func (s *ContainerService) deleteLocked(
	ctx context.Context,
	session *remoteDocker,
	container domain.Container,
	reason domain.ContainerEndReason,
) (ContainerDetail, error) {
	if container.State == domain.ContainerStateRemoved {
		// 重复删除视为幂等成功，但仍更新远程清单。
		if err := s.syncRegistry(ctx, session, container); err != nil {
			return ContainerDetail{Container: container}, err
		}
		return ContainerDetail{Container: container}, nil
	}
	target := container.DockerID
	if strings.TrimSpace(target) == "" {
		target = container.Name
	}
	// 删除前核验所有权：标签不一致时不得删除，避免误删同名但非 AgentFlow 创建的容器。
	if _, err := s.observe(ctx, session, container); err != nil {
		if docker.ErrorCodeOf(err) != docker.CodeContainerNotFound {
			return ContainerDetail{Container: container}, fmt.Errorf("删除前核验容器所有权失败: %w", err)
		}
	}
	if _, err := session.runner.RemoveContainer(ctx, target); err != nil {
		return ContainerDetail{Container: container}, fmt.Errorf("删除容器失败: %w", err)
	}
	updated := container
	if err := updated.MarkRemoved(reason, time.Now()); err != nil {
		return ContainerDetail{Container: container}, fmt.Errorf("记录容器删除状态失败: %w", err)
	}
	if err := s.store.UpdateContainer(ctx, updated); err != nil {
		return ContainerDetail{Container: updated}, fmt.Errorf("更新容器登记记录失败: %w", err)
	}
	if err := s.syncRegistry(ctx, session, updated); err != nil {
		return ContainerDetail{Container: updated}, fmt.Errorf("写入远程登记清单失败: %w", err)
	}
	return ContainerDetail{Container: updated}, nil
}

// DetachContainerRequest 是把容器脱离管理的输入。
type DetachContainerRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
}

// DetachContainer 把容器脱离 AgentFlow 自动管理并保留现场。
//
// 脱离管理是终态：Docker 标签无法原地修改，因此不能依靠补标签恢复管理权；
// 本机记录与远程清单都会持久化该事实。
func (s *ContainerService) DetachContainer(
	ctx context.Context,
	request DetachContainerRequest,
) (ContainerDetail, error) {
	return s.withContainerLock(ctx, request.ContainerID, func(
		ctx context.Context,
		session *remoteDocker,
		container domain.Container,
	) (ContainerDetail, error) {
		if container.State == domain.ContainerStateRemoved {
			return ContainerDetail{Container: container}, fmt.Errorf(
				"容器 %s 已被删除，无法脱离管理",
				container.ID,
			)
		}
		// 脱离管理前核验远程所有权：只有确认属于 AgentFlow 的容器才能被标记为 detached。
		observed, observeErr := s.observe(ctx, session, container)
		if observeErr != nil {
			return ContainerDetail{Container: container}, fmt.Errorf("脱离管理前核验容器失败: %w", observeErr)
		}
		updated := container
		if err := updated.Detach(time.Now()); err != nil {
			return ContainerDetail{Container: container}, fmt.Errorf("脱离管理失败: %w", err)
		}
		if syncErr := updated.SyncDockerState(observed.State, time.Now()); syncErr != nil {
			return ContainerDetail{Container: container}, fmt.Errorf("记录观测状态失败: %w", syncErr)
		}
		if err := s.store.UpdateContainer(ctx, updated); err != nil {
			return ContainerDetail{Container: updated}, fmt.Errorf("更新容器登记记录失败: %w", err)
		}
		if err := s.syncRegistry(ctx, session, updated); err != nil {
			return ContainerDetail{Container: updated}, fmt.Errorf("写入远程登记清单失败: %w", err)
		}
		return ContainerDetail{Container: updated}, nil
	})
}

// ContainerLogsRequest 是读取容器日志的输入。
type ContainerLogsRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// TailLines 是返回的末尾行数，零值使用 200。
	TailLines int
}

// ContainerLogs 读取容器日志。
//
// 已脱离管理的容器允许只读查看日志，但不会因此恢复管理权。
func (s *ContainerService) ContainerLogs(
	ctx context.Context,
	request ContainerLogsRequest,
) (string, error) {
	container, err := s.loadContainer(ctx, request.ContainerID)
	if err != nil {
		return "", err
	}
	logs := ""
	err = s.withRemoteDocker(
		ctx,
		container.HostID.String(),
		false,
		func(ctx context.Context, session *remoteDocker) error {
			target := container.DockerID
			if strings.TrimSpace(target) == "" {
				target = container.Name
			}
			content, logErr := session.runner.ContainerLogs(ctx, target, request.TailLines)
			if logErr != nil {
				return fmt.Errorf("读取容器日志失败: %w", logErr)
			}
			logs = content
			return nil
		},
	)
	if err != nil {
		return "", err
	}
	return logs, nil
}

// ExecRequest 是在容器内执行命令的输入。
type ExecRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// Command 是容器内命令及其参数。
	Command []string
	// WorkDir 是容器内执行目录，必须位于 /workspace 之下；空值使用项目目录。
	WorkDir string
	// Env 是显式传入容器内进程的环境变量，形如 KEY=VALUE。
	//
	// 执行层不会自动继承远程宿主机或服务进程的环境变量，只传递这里显式声明的值。
	Env []string
	// Stdin 非空时写入容器内命令的标准输入，并在写入后关闭。
	Stdin []byte
	// Timeout 是执行超时时间，零值使用默认值。
	Timeout time.Duration
	// MaxOutputBytes 是标准输出与标准错误各自保留的字节上限，零值使用默认上限。
	MaxOutputBytes int
}

// ExecResult 是容器内命令的执行结果。
type ExecResult struct {
	// Container 是容器记录。
	Container domain.Container
	// Result 是远程命令结果。
	Result docker.CommandResult
}

// Exec 在受管理容器内执行一条非交互命令。
func (s *ContainerService) Exec(
	ctx context.Context,
	request ExecRequest,
) (ExecResult, error) {
	container, err := s.loadContainer(ctx, request.ContainerID)
	if err != nil {
		return ExecResult{}, err
	}
	var result ExecResult
	err = s.withRemoteDocker(
		ctx,
		container.HostID.String(),
		false,
		func(ctx context.Context, session *remoteDocker) error {
			if err := ensureManaged(container); err != nil {
				return err
			}
			inspect, inspectErr := s.observe(ctx, session, container)
			if inspectErr != nil {
				return inspectErr
			}
			if !inspect.IsActivelyRunning() {
				return fmt.Errorf("容器 %s 当前未运行，无法执行命令", container.ID)
			}
			run, runErr := s.execInContainer(
				ctx,
				session,
				container.DockerID,
				request,
			)
			result = ExecResult{Container: container, Result: run}
			return runErr
		},
	)
	if err != nil {
		return result, err
	}
	return result, nil
}

// execInContainer 按请求执行一条容器内命令。
//
// 无标准输入时使用不带 --interactive 的路径，避免为普通命令额外打开标准输入通道；
// 有标准输入时才启用交互模式，并在写入后立即关闭。
func (s *ContainerService) execInContainer(
	ctx context.Context,
	session *remoteDocker,
	dockerID string,
	request ExecRequest,
) (docker.CommandResult, error) {
	options := docker.ExecOptions{
		WorkDir:        request.WorkDir,
		Env:            request.Env,
		Timeout:        request.Timeout,
		MaxOutputBytes: request.MaxOutputBytes,
	}
	if len(request.Stdin) == 0 {
		return session.runner.ExecCommandIn(ctx, dockerID, request.Command, options)
	}
	return session.runner.ExecCommandInWithInput(
		ctx,
		dockerID,
		request.Command,
		bytes.NewReader(request.Stdin),
		options,
	)
}

// ApplyLifecycleRequest 是应用生命周期策略的输入。
type ApplyLifecycleRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// Outcome 是任务结果分类。
	Outcome domain.ContainerOutcome
	// KeepInterrupted 表示中断时保留容器。
	KeepInterrupted bool
}

// ApplyLifecycleResult 是生命周期策略的执行结果。
type ApplyLifecycleResult struct {
	// Decision 是策略结论。
	Decision domain.ContainerLifecycleDecision
	// Container 是执行后的容器记录。
	Container domain.Container
}

// ApplyLifecycle 依据任务结果应用容器生命周期策略。
//
// 注意：本阶段没有实现 CodeSaver，因此调用方不得把"容器内命令成功"直接当作
// "任务成功"。成功清理只能由工作流引擎在必要业务操作全部完成后调用。
func (s *ContainerService) ApplyLifecycle(
	ctx context.Context,
	request ApplyLifecycleRequest,
) (ApplyLifecycleResult, error) {
	decision, err := domain.DecideContainerLifecycle(
		request.Outcome,
		domain.ContainerLifecycleOptions{KeepInterrupted: request.KeepInterrupted},
	)
	if err != nil {
		return ApplyLifecycleResult{}, fmt.Errorf("计算生命周期策略失败: %w", err)
	}
	result := ApplyLifecycleResult{Decision: decision}
	switch decision.Retention {
	case domain.ContainerRetentionDelete:
		detail, deleteErr := s.DeleteContainer(ctx, DeleteContainerRequest{
			ContainerID: request.ContainerID,
			Reason:      decision.EndReason,
		})
		result.Container = detail.Container
		if deleteErr != nil {
			return result, deleteErr
		}
	case domain.ContainerRetentionDetach:
		detail, detachErr := s.DetachContainer(ctx, DetachContainerRequest{
			ContainerID: request.ContainerID,
		})
		result.Container = detail.Container
		if detachErr != nil {
			return result, detachErr
		}
	default:
		container, loadErr := s.loadContainer(ctx, request.ContainerID)
		if loadErr != nil {
			return result, loadErr
		}
		result.Container = container
	}
	return result, nil
}

// RestartContainerRequest 是重新开始失败任务的输入。
type RestartContainerRequest struct {
	// PreviousContainerID 是旧容器登记记录 ID。
	PreviousContainerID string
	// Disposition 是旧容器处置方式：delete 或 detach。
	Disposition domain.RestartDisposition
	// Create 是创建新容器的输入；新容器总是从配置的镜像版本重新创建。
	Create CreateContainerRequest
}

// RestartContainer 处置旧容器并创建全新的任务容器。
//
// 重新开始必须创建新容器：不会复用旧容器，也不会把旧容器中的未推送代码带入
// 新容器；新容器的代码将在下一阶段的 Git Clone 实现后重新获取。
func (s *ContainerService) RestartContainer(
	ctx context.Context,
	request RestartContainerRequest,
) (ContainerDetail, error) {
	previous, err := s.loadContainer(ctx, request.PreviousContainerID)
	if err != nil {
		return ContainerDetail{}, err
	}
	if err := request.Disposition.Validate(); err != nil {
		return ContainerDetail{}, fmt.Errorf("重新开始失败: %w", err)
	}
	newName, err := docker.ContainerNameFor(domain.NewID())
	if err != nil {
		return ContainerDetail{}, err
	}
	if _, err := domain.PlanContainerRestart(
		previous,
		request.Disposition,
		newName,
		time.Now(),
	); err != nil {
		return ContainerDetail{}, fmt.Errorf("重新开始失败: %w", err)
	}
	switch request.Disposition {
	case domain.RestartDispositionDelete:
		if _, err := s.DeleteContainer(ctx, DeleteContainerRequest{
			ContainerID: previous.ID.String(),
			Reason:      domain.ContainerEndReasonTaskFailed,
		}); err != nil {
			return ContainerDetail{}, fmt.Errorf("处置旧容器失败: %w", err)
		}
	case domain.RestartDispositionDetach:
		if _, err := s.DetachContainer(ctx, DetachContainerRequest{
			ContainerID: previous.ID.String(),
		}); err != nil {
			return ContainerDetail{}, fmt.Errorf("处置旧容器失败: %w", err)
		}
	}
	request.Create.TaskID = previous.TaskID
	request.Create.AttemptID = previous.AttemptID
	if request.Create.MachineID == "" {
		request.Create.MachineID = previous.HostID.String()
	}
	if request.Create.Ref.Name == "" {
		request.Create.Ref = previous.Environment
	}
	return s.CreateContainer(ctx, request.Create)
}

// withContainerLock 取得容器锁并执行一次容器操作。
func (s *ContainerService) withContainerLock(
	ctx context.Context,
	containerID string,
	run func(ctx context.Context, session *remoteDocker, container domain.Container) (ContainerDetail, error),
) (ContainerDetail, error) {
	container, err := s.loadContainer(ctx, containerID)
	if err != nil {
		return ContainerDetail{}, err
	}
	var detail ContainerDetail
	err = s.withRemoteDocker(
		ctx,
		container.HostID.String(),
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.locks.Lock(container.ID.String())
			defer unlock()
			current, loadErr := s.loadContainer(ctx, containerID)
			if loadErr != nil {
				return loadErr
			}
			result, runErr := run(ctx, session, current)
			detail = result
			return runErr
		},
	)
	if err != nil {
		return detail, err
	}
	return detail, nil
}
