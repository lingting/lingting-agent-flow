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

// ensurePlanningAgentContainer 确认规划容器的归属与运行状态，必要时准备运行环境。
//
// 规划容器必须属于当前规划会话的执行节点、仍带本会话的规划标签并且正在运行；不在本
// 阶段创建第二个容器，也不接管已 detached 的容器。容器在上一轮调用中断后已停止时，
// 通过既有准备流程按原容器 ID 重新启动同一个容器。
func (s *PlanningAgentService) ensurePlanningAgentContainer(
	ctx context.Context,
	session domain.PlanningSession,
	hostIDValue string,
) (domain.PlanningSession, domain.Container, error) {
	hostID := strings.TrimSpace(hostIDValue)
	if strings.TrimSpace(session.ContainerID) == "" {
		prepared, err := s.preparePlanningAgentRuntime(ctx, session, hostID)
		if err != nil {
			return session, domain.Container{}, err
		}
		session = prepared.Session
		if prepared.Container.ID == "" {
			return session, domain.Container{}, fmt.Errorf(
				"%w: 规划运行环境准备后仍没有可用的规划容器",
				ErrPlanningAgentPrerequisite,
			)
		}
		return s.verifyPlanningAgentContainer(ctx, session, prepared.Container)
	}
	if hostID != "" && session.HostID != nil && hostID != session.HostID.String() {
		return session, domain.Container{}, fmt.Errorf(
			"%w: 规划运行环境已绑定执行节点 %s，不能改用 %s",
			ErrPlanningAgentInvalidInput,
			session.HostID.String(),
			hostID,
		)
	}
	if session.HostID == nil {
		return session, domain.Container{}, fmt.Errorf(
			"%w: 规划会话已登记容器但缺少执行节点，无法核验容器归属",
			ErrPlanningAgentStateConflict,
		)
	}
	container, err := s.loadPlanningAgentContainer(ctx, session.ContainerID)
	if err != nil {
		return session, domain.Container{}, err
	}
	if container.State == domain.ContainerStateStopped {
		// 上一轮规划调用中断后容器已停止：这是正常现场，复用运行时既有的准备流程按原
		// 容器 ID 重新启动它，而不是新建第二个容器或清空运行时绑定。
		return s.restartPlanningAgentContainer(ctx, session, hostID, container)
	}
	return s.verifyPlanningAgentContainer(ctx, session, container)
}

// restartPlanningAgentContainer 重新启动本会话登记且已停止的规划容器。
//
// 只有本地记录明确表示容器已停止时才自动重启：停止是规划调用被中断后的正常现场，
// 容器记录、快照卷与运行时绑定都保留。状态不确定的容器不在此自动处理，调用方需要先
// 核验远端实际状态。
func (s *PlanningAgentService) restartPlanningAgentContainer(
	ctx context.Context,
	session domain.PlanningSession,
	hostID string,
	container domain.Container,
) (domain.PlanningSession, domain.Container, error) {
	if container.State != domain.ContainerStateStopped {
		return session, container, fmt.Errorf(
			"%w: 规划容器当前状态为 %s，不能自动重新启动",
			ErrPlanningAgentStateConflict,
			container.State,
		)
	}
	if err := ensureManaged(container); err != nil {
		return session, container, fmt.Errorf("%w: %w", ErrPlanningAgentStateConflict, err)
	}
	if hostID == "" && session.HostID != nil {
		hostID = session.HostID.String()
	}
	prepared, err := s.preparePlanningAgentRuntime(ctx, session, hostID)
	if err != nil {
		return session, container, err
	}
	if prepared.Container.ID != container.ID {
		return session, container, fmt.Errorf(
			"%w: 重新启动的规划容器与已登记容器不一致: 已登记 %s，本次 %s",
			ErrPlanningAgentStateConflict,
			container.ID,
			prepared.Container.ID,
		)
	}
	return s.verifyPlanningAgentContainer(ctx, prepared.Session, prepared.Container)
}

// preparePlanningAgentRuntime 复用规划运行时服务准备运行环境。
func (s *PlanningAgentService) preparePlanningAgentRuntime(
	ctx context.Context,
	session domain.PlanningSession,
	hostID string,
) (PlanningRuntimeDetail, error) {
	if s.runtime == nil {
		return PlanningRuntimeDetail{}, fmt.Errorf(
			"%w: 规划会话尚未准备运行环境，当前服务没有配置规划运行时入口",
			ErrPlanningAgentPrerequisite,
		)
	}
	if hostID == "" {
		return PlanningRuntimeDetail{}, fmt.Errorf(
			"%w: 规划会话缺少执行节点，无法准备运行环境",
			ErrPlanningAgentInvalidInput,
		)
	}
	// 调用方（RunRole）已经持有本会话锁：内部的准备流程复用它，不重复加锁。
	detail, err := s.runtime.prepareHeld(ctx, PreparePlanningRuntimeRequest{
		PlanningSessionID: session.ID.String(),
		HostID:            hostID,
	})
	if err != nil {
		return detail, fmt.Errorf("%w: %w", ErrPlanningAgentPrerequisite, err)
	}
	return detail, nil
}

// loadPlanningAgentContainer 读取规划容器登记记录。
func (s *PlanningAgentService) loadPlanningAgentContainer(
	ctx context.Context,
	containerIDValue string,
) (domain.Container, error) {
	containerID := domain.ID(strings.TrimSpace(containerIDValue))
	if err := containerID.Validate(); err != nil {
		return domain.Container{}, fmt.Errorf(
			"%w: 规划容器标识非法: %w",
			ErrPlanningAgentInvalidInput,
			err,
		)
	}
	container, err := s.store.GetContainer(ctx, containerID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Container{}, fmt.Errorf(
				"%w: 规划容器记录 %s 不存在，请先核验远程容器",
				ErrPlanningAgentNotFound,
				containerID,
			)
		}
		return domain.Container{}, fmt.Errorf("读取规划容器记录失败: %w", err)
	}
	return *container, nil
}

// verifyPlanningAgentContainer 核验规划容器仍属于本会话且处于运行状态。
func (s *PlanningAgentService) verifyPlanningAgentContainer(
	ctx context.Context,
	session domain.PlanningSession,
	container domain.Container,
) (domain.PlanningSession, domain.Container, error) {
	if session.HostID == nil || container.HostID != *session.HostID {
		return session, container, fmt.Errorf(
			"%w: 规划容器与规划会话的执行节点不一致，拒绝在错误节点上调用",
			ErrPlanningAgentStateConflict,
		)
	}
	if err := ensureManaged(container); err != nil {
		return session, container, fmt.Errorf("%w: %w", ErrPlanningAgentStateConflict, err)
	}
	detail, err := s.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: container.ID.String(),
		Sync:        true,
	})
	if err != nil {
		return session, container, fmt.Errorf("%w: 核验规划容器失败: %w", ErrPlanningAgentPrerequisite, err)
	}
	if detail.Inspect == nil {
		return session, container, fmt.Errorf(
			"%w: 未能取得规划容器的实际状态",
			ErrPlanningAgentPrerequisite,
		)
	}
	if actual := detail.Inspect.Labels[docker.PlanningSessionLabelKey]; actual != session.ID.String() {
		return session, container, fmt.Errorf(
			"%w: 规划容器的规划会话标签不一致，拒绝把它当作本会话资源",
			ErrPlanningAgentStateConflict,
		)
	}
	if !detail.Inspect.Running {
		return session, container, fmt.Errorf(
			"%w: 规划容器当前未运行，无法执行规划调用",
			ErrPlanningAgentPrerequisite,
		)
	}
	return session, detail.Container, nil
}

// loadPlanningRole 读取并校验规划角色配置。
//
// 角色配置只来自统一的角色服务：角色缺失、类型不匹配、被禁用或模型为空时都直接失败，
// 不回退到其他角色、开发权限或 Pi 的默认模型。
func (s *PlanningAgentService) loadPlanningRole(
	ctx context.Context,
	roleType domain.RoleType,
) (domain.Role, error) {
	roleID, err := planningRoleID(roleType)
	if err != nil {
		return domain.Role{}, err
	}
	role, err := s.roles.Get(ctx, roleID.String())
	if err != nil {
		if errors.Is(err, ErrRoleNotFound) {
			return domain.Role{}, fmt.Errorf(
				"%w: 规划角色 %s 的配置不存在",
				ErrPlanningAgentNotFound,
				roleID,
			)
		}
		if errors.Is(err, ErrRoleInvalidInput) {
			return domain.Role{}, fmt.Errorf("%w: %w", ErrPlanningAgentInvalidInput, err)
		}
		return domain.Role{}, fmt.Errorf("读取规划角色配置失败: %w", err)
	}
	if role.Type != roleType {
		return domain.Role{}, fmt.Errorf(
			"%w: 角色 %s 的类型是 %s，与本次调用的 %s 不一致",
			ErrPlanningAgentStateConflict,
			roleID,
			role.Type,
			roleType,
		)
	}
	if !role.Enabled {
		return domain.Role{}, fmt.Errorf(
			"%w: 规划角色 %s 已被禁用",
			ErrPlanningAgentPrerequisite,
			roleID,
		)
	}
	if err := role.Validate(); err != nil {
		return domain.Role{}, fmt.Errorf(
			"%w: 规划角色 %s 的配置不完整: %w",
			ErrPlanningAgentPrerequisite,
			roleID,
			err,
		)
	}
	if strings.TrimSpace(role.Model) == "" {
		return domain.Role{}, fmt.Errorf(
			"%w: 规划角色 %s 没有配置模型",
			ErrPlanningAgentPrerequisite,
			roleID,
		)
	}
	return role, nil
}
