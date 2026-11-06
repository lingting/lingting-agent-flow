package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 本文件实现规划运行环境的释放：规划容器与快照卷都必须先核验远端事实，再执行删除。
//
// 三条不变式：
//   - 只有归属核验通过的资源才会被删除；
//   - 删除结果不确定时保留数据库中的定位信息，并在本次调用内立即返回；
//   - 只有容器与快照卷都已确认不存在，才允许清空会话的运行时绑定。

// releasePlanningContainer 删除本会话登记的规划容器。
//
// 返回的第二个错误是"容器已删除但远程登记清单未同步"这类不阻断后续释放的告警：容器已
// 确认删除时仍然继续删除快照卷，并在最终结果中如实返回该告警。
//
// 每次调用都重新核验远端状态：上一次删除的结果可能不确定，本次必须先拿到事实；已确认
// 不存在的容器按幂等完成处理，不再重复执行有副作用的删除。
func (s *PlanningRuntimeService) releasePlanningContainer(
	ctx context.Context,
	session domain.PlanningSession,
) (bool, error, error) {
	stage := PlanningRuntimeStageReleaseContainer
	container, err := s.releasePlanningContainerRecord(ctx, session)
	if err != nil {
		return false, nil, err
	}
	if container.State == domain.ContainerStateRemoved {
		// 已确认删除：重复 Release 仍然视为成功，不删除同名的其他资源。
		return true, nil, nil
	}
	if container.Detached {
		return false, nil, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"规划容器已脱离 AgentFlow 管理，拒绝自动删除",
			nil,
		)
	}
	observed, observeErr := s.remote.InspectManagedContainer(ctx, container.ID.String())
	switch {
	case observeErr == nil:
		if observed.Inspect == nil {
			return false, nil, planningRuntimeFailure(
				ErrPlanningRuntimeUncertain,
				stage,
				"未能取得规划容器的实际状态",
				nil,
			)
		}
		if actual := observed.Inspect.Labels[docker.PlanningSessionLabelKey]; actual != session.ID.String() {
			return false, nil, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				stage,
				"规划容器的规划会话标签不一致，拒绝删除",
				fmt.Errorf("期望 %s，实际 %s", session.ID, actual),
			)
		}
	case docker.ErrorCodeOf(observeErr) == docker.CodeContainerNotFound:
		// 远端已确认不存在：登记记录仍需收敛为已删除，删除动作对不存在的容器是幂等的。
	case docker.ErrorCodeOf(observeErr) == docker.CodeOwnershipMismatch:
		return false, nil, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"规划容器的远端标签与登记记录不一致，拒绝删除",
			observeErr,
		)
	default:
		return false, nil, planningRuntimeRemoteFailure(observeErr, stage, "核验规划容器状态失败")
	}
	detail, deleteErr := s.remote.DeleteManagedContainer(ctx, container.ID.String())
	if deleteErr != nil {
		if detail.Container.State == domain.ContainerStateRemoved {
			return true, planningRuntimeFailure(
				ErrPlanningRuntimeFailed,
				stage,
				"规划容器已删除，但远程登记清单同步失败",
				deleteErr,
			), nil
		}
		switch docker.ErrorCodeOf(deleteErr) {
		case docker.CodeOwnershipMismatch:
			return false, nil, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				stage,
				"规划容器所有权核验失败，拒绝删除",
				deleteErr,
			)
		case docker.CodeDetached:
			return false, nil, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				stage,
				"规划容器已脱离 AgentFlow 管理，拒绝自动删除",
				deleteErr,
			)
		}
		return false, nil, planningRuntimeRemoteFailure(deleteErr, stage, "删除规划容器失败")
	}
	if detail.Container.State != domain.ContainerStateRemoved {
		return false, nil, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			stage,
			"容器删除后状态未收敛为已删除",
			nil,
		)
	}
	return true, nil, nil
}

// releasePlanningContainerRecord 读取并校验本会话的规划容器登记记录。
func (s *PlanningRuntimeService) releasePlanningContainerRecord(
	ctx context.Context,
	session domain.PlanningSession,
) (domain.Container, error) {
	stage := PlanningRuntimeStageReleaseContainer
	containerID := domain.ID(strings.TrimSpace(session.ContainerID))
	if err := containerID.Validate(); err != nil {
		return domain.Container{}, planningRuntimeFailure(
			ErrPlanningRuntimeInvalidInput,
			stage,
			"规划会话的容器标识非法",
			err,
		)
	}
	container, err := s.store.GetContainer(ctx, containerID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Container{}, planningRuntimeFailure(
				ErrPlanningRuntimeConflict,
				stage,
				"规划容器登记记录不存在，无法确认远程容器已删除",
				err,
			)
		}
		return domain.Container{}, fmt.Errorf("读取规划容器记录失败: %w", err)
	}
	if session.HostID == nil || container.HostID != *session.HostID {
		return domain.Container{}, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"规划容器与规划会话的执行节点不一致，拒绝删除",
			nil,
		)
	}
	return *container, nil
}

// releasePlanningSnapshotVolume 删除本会话的固定 commit 快照卷。
//
// 只有名称、管理标签、节点标签与会话标签全部匹配时才删除；卷已确认不存在时按已释放
// 处理，不执行删除；删除后再次核验，只有确认卷确实不存在才报告已释放。
func (s *PlanningRuntimeService) releasePlanningSnapshotVolume(
	ctx context.Context,
	session domain.PlanningSession,
	host domain.ExecutionHost,
) (bool, error) {
	stage := PlanningRuntimeStageReleaseSnapshot
	volumeName := planningSnapshotVolumeName(session.ID, host.ID)
	volume, found, err := s.remote.InspectSnapshotVolume(ctx, host.ID, volumeName)
	if err != nil {
		return false, planningRuntimeRemoteFailure(err, stage, "读取快照卷失败")
	}
	if !found {
		// 已确认不存在：不执行删除，按已释放处理。
		return true, nil
	}
	if err := verifyPlanningSnapshotVolume(volume, volumeName, session.ID, host.ID); err != nil {
		return false, planningRuntimeFailure(
			ErrPlanningRuntimeConflict,
			stage,
			"快照卷归属与当前规划会话不一致，拒绝删除",
			err,
		)
	}
	if _, err := s.remote.RemoveSnapshotVolume(ctx, host.ID, volumeName); err != nil {
		return false, planningRuntimeRemoteFailure(err, stage, "删除快照卷失败")
	}
	_, stillFound, inspectErr := s.remote.InspectSnapshotVolume(ctx, host.ID, volumeName)
	if inspectErr != nil {
		return false, planningRuntimeRemoteFailure(inspectErr, stage, "删除快照卷后未能核验")
	}
	if stillFound {
		return false, planningRuntimeFailure(
			ErrPlanningRuntimeFailed,
			stage,
			"快照卷删除后仍然存在",
			nil,
		)
	}
	return true, nil
}
