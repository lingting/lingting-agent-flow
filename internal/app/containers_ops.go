package app

import (
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
	// IncludeDetached 表示包含已脱离管理的容器。
	IncludeDetached bool
}

// ListContainers 返回容器登记记录。
func (s *ContainerService) ListContainers(
	ctx context.Context,
	request ListContainersRequest,
) ([]domain.Container, error) {
	filter := domain.ContainerFilter{IncludeDetached: request.IncludeDetached}
	if strings.TrimSpace(request.MachineID) != "" {
		host, err := s.loadHost(ctx, request.MachineID)
		if err != nil {
			return nil, err
		}
		filter.HostID = host.ID
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
		if !inspect.Running {
			if err := session.runner.StartContainer(ctx, container.DockerID); err != nil {
				return ContainerDetail{Container: container, Inspect: inspect}, err
			}
			inspect, err = s.observe(ctx, session, container)
			if err != nil {
				return ContainerDetail{Container: container}, err
			}
		}
		if !inspect.Running {
			return ContainerDetail{Container: container, Inspect: inspect}, fmt.Errorf(
				"容器启动后仍未运行: Docker 状态 %s",
				inspect.State,
			)
		}
		updated := container
		if err := updated.MarkRunning(time.Now()); err != nil {
			return ContainerDetail{Container: container, Inspect: inspect}, err
		}
		if err := s.store.UpdateContainer(ctx, updated); err != nil {
			return ContainerDetail{Container: updated, Inspect: inspect}, err
		}
		if err := s.syncRegistry(ctx, session, updated); err != nil {
			return ContainerDetail{Container: updated, Inspect: inspect}, err
		}
		return ContainerDetail{Container: updated, Inspect: inspect}, nil
	})
}

// StopContainerRequest 是停止容器的输入。
type StopContainerRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// TimeoutSeconds 是停止等待秒数，零值使用 10 秒。
	TimeoutSeconds int
}

// StopContainer 停止受管理容器，保留容器现场。
func (s *ContainerService) StopContainer(
	ctx context.Context,
	request StopContainerRequest,
) (ContainerDetail, error) {
	return s.withContainerLock(ctx, request.ContainerID, func(
		ctx context.Context,
		session *remoteDocker,
		container domain.Container,
	) (ContainerDetail, error) {
		if err := ensureManaged(container); err != nil {
			return ContainerDetail{Container: container}, err
		}
		if err := session.runner.StopContainer(ctx, container.DockerID, request.TimeoutSeconds); err != nil {
			return ContainerDetail{Container: container}, err
		}
		inspect, err := s.observe(ctx, session, container)
		if err != nil {
			return ContainerDetail{Container: container}, err
		}
		updated := container
		if err := updated.MarkStopped(time.Now()); err != nil {
			return ContainerDetail{Container: container, Inspect: inspect}, err
		}
		if err := s.store.UpdateContainer(ctx, updated); err != nil {
			return ContainerDetail{Container: updated, Inspect: inspect}, err
		}
		if err := s.syncRegistry(ctx, session, updated); err != nil {
			return ContainerDetail{Container: updated, Inspect: inspect}, err
		}
		return ContainerDetail{Container: updated, Inspect: inspect}, nil
	})
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
	// Timeout 是执行超时时间，零值使用默认值。
	Timeout time.Duration
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
			if !inspect.Running {
				return fmt.Errorf("容器 %s 当前未运行，无法执行命令", container.ID)
			}
			run, runErr := session.runner.ExecCommand(
				ctx,
				container.DockerID,
				request.Command,
				request.Timeout,
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
