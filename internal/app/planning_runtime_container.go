package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/execenv"
	"github.com/lingting/agentflow/internal/storage"
)

// 规划容器的容器内目录。
const (
	// planningSessionDir 是规划容器内临时 Session 目录。
	planningSessionDir = execenv.DefaultSessionDir
	// planningScratchDir 是规划容器内唯一可写的临时目录。
	//
	// 它覆盖 Session 目录，并为规划调用的临时文件提供写入能力；它不与项目代码
	// 目录重叠，容器删除后内容随之消失。
	planningScratchDir = "/tmp"
)

// planningContainerRequest 是创建或复用规划容器的输入。
type planningContainerRequest struct {
	session    domain.PlanningSession
	project    domain.Project
	host       domain.ExecutionHost
	version    domain.EnvironmentVersion
	volumeName string
	workspace  execenv.Workspace
}

// planningReadOnlyWorkspace 构造规划容器的只读工作区。
func planningReadOnlyWorkspace(project domain.Project) (execenv.Workspace, error) {
	return execenv.NewReadOnlyWorkspace(
		project.EffectiveWorkDir(),
		planningSessionDir,
		[]string{planningScratchDir},
	)
}

// createPlanningContainer 创建并启动只读规划容器。
//
// 容器由 ContainerService 登记为受管理容器，并核验所有权与实际运行状态；
// 只有创建成功后才把登记记录 ID 写入 PlanningSession。
func (s *PlanningRuntimeService) createPlanningContainer(
	ctx context.Context,
	request planningContainerRequest,
) (domain.Container, error) {
	detail, err := s.containers.CreateContainer(ctx, CreateContainerRequest{
		MachineID:     request.host.ID.String(),
		Ref:           request.project.Environment,
		VersionNumber: request.version.Number,
		ProjectID:     request.project.ID,
		WorkDir:       request.workspace.Project,
		Planning: &PlanningContainerSpec{
			CodeVolumeName: request.volumeName,
			Workspace:      request.workspace,
			SessionID:      request.session.ID.String(),
		},
	})
	return detail.Container, err
}

// reusePlanningContainer 核验并复用本会话登记的规划容器。
//
// 记录与远程实际资源不一致、容器已脱离管理或已不存在时返回明确错误，不创建第二个
// 容器覆盖记录；容器已停止时按受管理容器的既定策略重新启动。
func (s *PlanningRuntimeService) reusePlanningContainer(
	ctx context.Context,
	request planningContainerRequest,
) (domain.Container, error) {
	container, err := s.containers.loadContainer(ctx, request.session.ContainerID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Container{}, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				PlanningRuntimeStageContainerReuse,
				"规划会话登记的容器记录不存在，不能自动创建第二个容器",
				err,
			)
		}
		return domain.Container{}, fmt.Errorf("读取规划容器记录失败: %w", err)
	}
	if container.HostID != request.host.ID {
		return domain.Container{}, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageContainerReuse,
			"规划容器不属于本次指定的执行节点",
			nil,
		)
	}
	if err := ensureManaged(container); err != nil {
		return domain.Container{}, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageContainerReuse,
			"规划容器已脱离管理或已删除，不能自动复用",
			err,
		)
	}
	detail, err := s.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: container.ID.String(),
		Sync:        true,
	})
	if err != nil {
		return domain.Container{}, planningContainerObserveFailure(err)
	}
	if detail.Inspect == nil {
		return domain.Container{}, planningRuntimeFailure(
			ErrPlanningRuntimeUncertain,
			PlanningRuntimeStageContainerReuse,
			"未能取得规划容器的实际状态",
			nil,
		)
	}
	if actual := detail.Inspect.Labels[docker.PlanningSessionLabelKey]; actual != request.session.ID.String() {
		return domain.Container{}, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageContainerReuse,
			"规划容器的规划会话标签不一致，拒绝把它当作本会话资源",
			fmt.Errorf("期望 %s，实际 %s", request.session.ID, actual),
		)
	}
	if detail.Inspect.Running {
		return detail.Container, nil
	}
	started, startErr := s.containers.StartContainer(ctx, StartContainerRequest{
		ContainerID: container.ID.String(),
	})
	if startErr != nil {
		code := docker.ErrorCodeOf(startErr)
		if code == docker.CodeStateUncertain || code == docker.CodeSSHUnavailable {
			return domain.Container{}, planningRuntimeFailure(
				ErrPlanningRuntimeUncertain,
				PlanningRuntimeStageContainerReuse,
				"重新启动已登记的规划容器时结果无法确认",
				startErr,
			)
		}
		return domain.Container{}, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageContainerReuse,
			"重新启动已登记的规划容器失败",
			startErr,
		)
	}
	if started.Inspect == nil || !started.Inspect.Running {
		return domain.Container{}, planningRuntimeFailure(
			ErrPlanningRuntimeUncertain,
			PlanningRuntimeStageContainerReuse,
			"规划容器启动后未确认进入运行状态",
			nil,
		)
	}
	return started.Container, nil
}

// planningContainerObserveFailure 把容器观测错误转换为带阶段的失败。
func planningContainerObserveFailure(err error) error {
	switch docker.ErrorCodeOf(err) {
	case docker.CodeContainerNotFound:
		return planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageContainerReuse,
			"规划会话登记的容器在远程已不存在，不能自动创建第二个容器",
			err,
		)
	case docker.CodeStateUncertain, docker.CodeSSHUnavailable:
		return planningRuntimeFailure(
			ErrPlanningRuntimeUncertain,
			PlanningRuntimeStageContainerReuse,
			"核验规划容器状态时结果无法确认",
			err,
		)
	default:
		return planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageContainerReuse,
			"核验规划容器失败",
			err,
		)
	}
}

// releasePlanningContainer 删除本会话登记的规划容器。
//
// 返回的第二个错误是"容器已删除但远程登记清单未同步"这类不阻断后续释放的告警：
// 容器已确认删除时仍然继续删除快照卷，并在最终结果中如实返回该告警。
func (s *PlanningRuntimeService) releasePlanningContainer(
	ctx context.Context,
	session domain.PlanningSession,
) (bool, error, error) {
	container, err := s.containers.loadContainer(ctx, session.ContainerID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return false, nil, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				PlanningRuntimeStageReleaseContainer,
				"规划容器登记记录不存在，无法确认远程容器已删除",
				err,
			)
		}
		return false, nil, fmt.Errorf("读取规划容器记录失败: %w", err)
	}
	if session.HostID == nil || container.HostID != *session.HostID {
		return false, nil, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageReleaseContainer,
			"规划容器与规划会话的执行节点不一致，拒绝删除",
			nil,
		)
	}
	if container.State == domain.ContainerStateRemoved {
		// 已确认删除：重复 Release 仍然视为成功，不删除同名的其他资源。
		return true, nil, nil
	}
	if container.Detached {
		return false, nil, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			PlanningRuntimeStageReleaseContainer,
			"规划容器已脱离 AgentFlow 管理，拒绝自动删除",
			nil,
		)
	}
	detail, err := s.containers.DeleteContainer(ctx, DeleteContainerRequest{
		ContainerID: container.ID.String(),
		Reason:      domain.ContainerEndReasonManual,
	})
	if err != nil {
		if detail.Container.State == domain.ContainerStateRemoved {
			return true, planningRuntimeFailure(
				ErrPlanningRuntimeFailed,
				PlanningRuntimeStageReleaseContainer,
				"规划容器已删除，但远程登记清单同步失败",
				err,
			), nil
		}
		code := docker.ErrorCodeOf(err)
		if code == docker.CodeStateUncertain || code == docker.CodeSSHUnavailable {
			return false, nil, planningRuntimeFailure(
				ErrPlanningRuntimeUncertain,
				PlanningRuntimeStageReleaseContainer,
				"删除规划容器的结果无法确认",
				err,
			)
		}
		return false, nil, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageReleaseContainer,
			"删除规划容器失败",
			err,
		)
	}
	if detail.Container.State != domain.ContainerStateRemoved {
		return false, nil, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			PlanningRuntimeStageReleaseContainer,
			"容器删除后状态未收敛为已删除",
			nil,
		)
	}
	return true, nil, nil
}

// releasePlanningSnapshotVolume 删除本会话的快照卷。
//
// 只有名称、管理标签、节点标签与会话标签全部匹配时才删除；卷不存在视为已释放，
// 归属不成立时拒绝删除并保留会话中的定位信息。
func (s *PlanningRuntimeService) releasePlanningSnapshotVolume(
	ctx context.Context,
	session domain.PlanningSession,
	host domain.ExecutionHost,
) (bool, error) {
	volumeName := planningSnapshotVolumeName(session.ID, host.ID)
	released := false
	err := s.containers.withRemoteDocker(
		ctx,
		host.ID.String(),
		false,
		func(ctx context.Context, remote *remoteDocker) error {
			volume, found, inspectErr := inspectPlanningVolume(ctx, remote, volumeName)
			if inspectErr != nil {
				return planningRuntimeFailure(
					ErrPlanningRuntimeFailed,
					PlanningRuntimeStageReleaseSnapshot,
					"读取快照卷失败",
					inspectErr,
				)
			}
			if !found {
				return nil
			}
			if err := verifyPlanningSnapshotVolume(volume, volumeName, session.ID, host.ID); err != nil {
				return planningRuntimeFailure(
					ErrPlanningRuntimeConflict,
					PlanningRuntimeStageReleaseSnapshot,
					"快照卷归属与当前规划会话不一致，拒绝删除",
					err,
				)
			}
			existed, removeErr := remote.runner.RemoveVolume(ctx, volumeName)
			if removeErr != nil {
				if docker.ErrorCodeOf(removeErr) == docker.CodeStateUncertain {
					return planningRuntimeFailure(
						ErrPlanningRuntimeUncertain,
						PlanningRuntimeStageReleaseSnapshot,
						"删除快照卷的结果无法确认",
						removeErr,
					)
				}
				return planningRuntimeFailure(
					ErrPlanningRuntimeFailed,
					PlanningRuntimeStageReleaseSnapshot,
					"删除快照卷失败",
					removeErr,
				)
			}
			released = existed
			return nil
		},
	)
	if err != nil {
		return released, planningRuntimeStagedOrFailed(err, PlanningRuntimeStageReleaseSnapshot, "释放快照卷失败")
	}
	return released, nil
}

// deleteUnboundPlanningContainer 清理尚未写入会话关联的规划容器。
//
// 它只用于"容器已核验运行、但会话关联写入失败"的确定失败：容器此时不被任何记录
// 引用，保留只会成为孤儿。远程容器已删除但登记清单同步失败时不再视为清理失败。
func (s *PlanningRuntimeService) deleteUnboundPlanningContainer(
	ctx context.Context,
	container domain.Container,
) error {
	detail, err := s.containers.DeleteContainer(ctx, DeleteContainerRequest{
		ContainerID: container.ID.String(),
		Reason:      domain.ContainerEndReasonManual,
	})
	if err == nil || detail.Container.State == domain.ContainerStateRemoved {
		return nil
	}
	return fmt.Errorf("清理未写入会话关联的规划容器失败: %w", err)
}
