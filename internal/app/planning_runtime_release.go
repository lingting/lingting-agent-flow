package app

import (
	"context"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// PlanningRuntimeReleasePort 是释放规划运行环境需要的容器能力。
//
// 它只包含 Release 实际使用的能力：读取执行节点、读取规划容器记录、删除规划容器与删除
// 本会话快照卷。容器的创建、复用与核验不在其中，准备路径继续直接使用 ContainerService，
// 因此该端口不会成为第二条容器或 SSH 调用链。
//
// 它同时是释放路径的唯一测试缝隙：生产装配由 planningRuntimeContainerRelease 包装
// ContainerService 提供，测试可以注入受控替身来观察"租约冲突时是否真的进入远程删除"。
type PlanningRuntimeReleasePort interface {
	// LoadExecutionHost 读取规划会话绑定的执行节点。
	LoadExecutionHost(ctx context.Context, hostID string) (domain.ExecutionHost, error)
	// LoadContainer 读取规划容器登记记录。
	LoadContainer(ctx context.Context, containerID string) (domain.Container, error)
	// DeleteContainer 删除规划容器并返回删除后的登记记录。
	DeleteContainer(ctx context.Context, request DeleteContainerRequest) (ContainerDetail, error)
	// RemoveSnapshotVolume 删除本会话的固定 commit 快照卷。
	RemoveSnapshotVolume(
		ctx context.Context,
		session domain.PlanningSession,
		host domain.ExecutionHost,
	) (bool, error)
}

// planningRuntimeContainerRelease 用受管理容器服务实现释放能力。
type planningRuntimeContainerRelease struct {
	containers *ContainerService
}

// LoadExecutionHost 读取执行节点配置。
func (r planningRuntimeContainerRelease) LoadExecutionHost(
	ctx context.Context,
	hostID string,
) (domain.ExecutionHost, error) {
	return r.containers.loadHost(ctx, hostID)
}

// LoadContainer 读取规划容器登记记录。
func (r planningRuntimeContainerRelease) LoadContainer(
	ctx context.Context,
	containerID string,
) (domain.Container, error) {
	return r.containers.loadContainer(ctx, containerID)
}

// DeleteContainer 删除规划容器。
func (r planningRuntimeContainerRelease) DeleteContainer(
	ctx context.Context,
	request DeleteContainerRequest,
) (ContainerDetail, error) {
	return r.containers.DeleteContainer(ctx, request)
}

// RemoveSnapshotVolume 删除本会话的快照卷。
//
// 只有名称、管理标签、节点标签与会话标签全部匹配时才删除；卷不存在视为已释放，
// 归属不成立时拒绝删除并保留会话中的定位信息。
func (r planningRuntimeContainerRelease) RemoveSnapshotVolume(
	ctx context.Context,
	session domain.PlanningSession,
	host domain.ExecutionHost,
) (bool, error) {
	volumeName := planningSnapshotVolumeName(session.ID, host.ID)
	released := false
	err := r.containers.withRemoteDocker(
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
		return released, planningRuntimeStagedOrFailed(
			err,
			PlanningRuntimeStageReleaseSnapshot,
			"释放快照卷失败",
		)
	}
	return released, nil
}
