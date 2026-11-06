package app

import (
	"context"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// PlanningRuntimeRemotePort 是规划运行时读取远端现场并执行单次删除的能力。
//
// 端口只包含两类动作：读取远端事实、执行一次明确的删除。所有权判定、状态冲突判定与
// 重试策略全部留在 PlanningRuntimeService：把这些规则搬进适配器会让"标签不一致不得删除"
// 这类边界无法被测试观察到，也会让核验、清理、恢复、废弃与释放五条路径各自演化出不同
// 的判定。
//
// 生产装配由 planningRuntimeRemote 包装 ContainerService 提供：所有调用继续经过
// ContainerService.withRemoteSession 与同一个 docker.Runner，因此不存在第二条 SSH 或
// Docker 调用链，也没有环境、镜像或容器生命周期的平行实现。
type PlanningRuntimeRemotePort interface {
	// InspectManagedContainer 只读核验受管理的规划容器，不写本地记录。
	InspectManagedContainer(ctx context.Context, containerID string) (ContainerDetail, error)
	// DeleteManagedContainer 删除受管理的规划容器，返回删除后的登记记录。
	DeleteManagedContainer(ctx context.Context, containerID string) (ContainerDetail, error)
	// InspectPrepContainer 只读核验一次性准备容器；容器不存在时返回 found=false。
	InspectPrepContainer(
		ctx context.Context,
		hostID domain.ID,
		name string,
	) (docker.ContainerInspect, bool, error)
	// RemovePrepContainer 按 Docker 容器 ID 删除一次性准备容器，返回删除前是否存在。
	RemovePrepContainer(ctx context.Context, hostID domain.ID, dockerID string) (bool, error)
	// InspectSnapshotVolume 只读核验快照卷；卷不存在时返回 found=false。
	InspectSnapshotVolume(
		ctx context.Context,
		hostID domain.ID,
		volumeName string,
	) (docker.VolumeInspect, bool, error)
	// RemoveSnapshotVolume 删除快照卷，返回删除前是否存在。
	RemoveSnapshotVolume(ctx context.Context, hostID domain.ID, volumeName string) (bool, error)
	// VerifySnapshotCommit 在只读一次性容器内核验快照卷的实际 commit。
	VerifySnapshotCommit(
		ctx context.Context,
		target PlanningRuntimeSnapshotTarget,
	) (string, error)
}

// PlanningRuntimeSnapshotTarget 是一次只读快照核验的定位信息。
//
// 它只包含定位快照所需的标识与镜像：核验不 Clone、不 checkout、不 reset，也不写快照卷。
type PlanningRuntimeSnapshotTarget struct {
	// SessionID 是快照所属的规划会话。
	SessionID domain.ID
	// HostID 是快照卷所在的执行节点。
	HostID domain.ID
	// VolumeName 是快照卷名称。
	VolumeName string
	// ImageRef 是只读核验容器使用的语言环境镜像。
	ImageRef string
	// WorkDir 是快照卷在容器内的挂载路径。
	WorkDir string
}

// planningRuntimeRemote 用受管理容器服务实现规划运行时的远程能力。
type planningRuntimeRemote struct {
	containers *ContainerService
}

// InspectManagedContainer 核验受管理规划容器；调用路径与容器查询接口一致。
func (r planningRuntimeRemote) InspectManagedContainer(
	ctx context.Context,
	containerID string,
) (ContainerDetail, error) {
	return r.containers.InspectContainer(ctx, InspectContainerRequest{ContainerID: containerID})
}

// DeleteManagedContainer 删除受管理规划容器；所有权核验与登记记录更新由容器服务承担。
func (r planningRuntimeRemote) DeleteManagedContainer(
	ctx context.Context,
	containerID string,
) (ContainerDetail, error) {
	return r.containers.DeleteContainer(ctx, DeleteContainerRequest{
		ContainerID: containerID,
		Reason:      domain.ContainerEndReasonManual,
	})
}

// InspectPrepContainer 按固定名称核验一次性准备容器。
func (r planningRuntimeRemote) InspectPrepContainer(
	ctx context.Context,
	hostID domain.ID,
	name string,
) (docker.ContainerInspect, bool, error) {
	var inspect docker.ContainerInspect
	found := false
	err := r.withRuntimeRemote(ctx, hostID, func(ctx context.Context, remote *remoteDocker) error {
		observed, exists, inspectErr := inspectRemotePrepContainer(ctx, remote, name)
		inspect, found = observed, exists
		return inspectErr
	})
	return inspect, found, err
}

// RemovePrepContainer 按 Docker 容器 ID 删除一次性准备容器。
func (r planningRuntimeRemote) RemovePrepContainer(
	ctx context.Context,
	hostID domain.ID,
	dockerID string,
) (bool, error) {
	existed := false
	err := r.withRuntimeRemote(ctx, hostID, func(ctx context.Context, remote *remoteDocker) error {
		result, removeErr := remote.runner.RemoveContainer(ctx, dockerID)
		existed = result.Existed
		return removeErr
	})
	return existed, err
}

// InspectSnapshotVolume 核验快照卷是否存在并返回其标签。
func (r planningRuntimeRemote) InspectSnapshotVolume(
	ctx context.Context,
	hostID domain.ID,
	volumeName string,
) (docker.VolumeInspect, bool, error) {
	var volume docker.VolumeInspect
	found := false
	err := r.withRuntimeRemote(ctx, hostID, func(ctx context.Context, remote *remoteDocker) error {
		observed, exists, inspectErr := inspectPlanningVolume(ctx, remote, volumeName)
		volume, found = observed, exists
		return inspectErr
	})
	return volume, found, err
}

// RemoveSnapshotVolume 删除快照卷；卷已不存在时按幂等成功处理。
func (r planningRuntimeRemote) RemoveSnapshotVolume(
	ctx context.Context,
	hostID domain.ID,
	volumeName string,
) (bool, error) {
	existed := false
	err := r.withRuntimeRemote(ctx, hostID, func(ctx context.Context, remote *remoteDocker) error {
		removed, removeErr := remote.runner.RemoveVolume(ctx, volumeName)
		existed = removed
		return removeErr
	})
	return existed, err
}

// VerifySnapshotCommit 用只读一次性容器核验快照卷的实际 commit 与工作区状态。
func (r planningRuntimeRemote) VerifySnapshotCommit(
	ctx context.Context,
	target PlanningRuntimeSnapshotTarget,
) (string, error) {
	commit := ""
	err := r.withRuntimeRemote(ctx, target.HostID, func(ctx context.Context, remote *remoteDocker) error {
		verified, verifyErr := verifySnapshotOnRemote(ctx, remote, target)
		commit = verified
		return verifyErr
	})
	return commit, err
}

// withRuntimeRemote 在目标节点上执行一次核验或删除。
//
// 工作空间缺失不阻断这些动作：核验与删除只需要 Docker，创建远程目录不是本端职责；执行
// 节点的准入校验仍然照常生效，因此不会把未通过预检的节点当成可用节点。
func (r planningRuntimeRemote) withRuntimeRemote(
	ctx context.Context,
	hostID domain.ID,
	run func(ctx context.Context, remote *remoteDocker) error,
) error {
	return r.containers.withRemoteSession(ctx, hostID.String(), sessionOptions{
		AllowMissingWorkspace: true,
	}, run)
}

// planningRuntimeRemoteFailure 把一次远程动作的失败映射为运行时错误类别。
//
// 状态无法确认与确定失败必须区分：前者保留现场并等待核验，后者才表示动作明确失败。SSH
// 不可用同样属于无法确认——远程命令可能已经执行完成，不能当作"资源不存在"。
func planningRuntimeRemoteFailure(
	err error,
	stage PlanningRuntimeStage,
	detail string,
) error {
	if planningRuntimeUncertain(err) || docker.ErrorCodeOf(err) == docker.CodeSSHUnavailable {
		return planningRuntimeFailure(ErrPlanningRuntimeUncertain, stage, detail, err)
	}
	return planningRuntimeFailure(ErrPlanningRuntimeFailed, stage, detail, err)
}
