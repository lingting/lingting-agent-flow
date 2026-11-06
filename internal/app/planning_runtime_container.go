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
