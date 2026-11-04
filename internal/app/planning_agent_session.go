package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// planningRoleBindingAttempts 是角色会话绑定冲突后的最大尝试次数。
//
// 冲突说明另一个执行者已经写入绑定：重新读取并复用它的结果，只在对方仍未写全时才再
// 试一次，绝不退化成无条件覆盖。
const planningRoleBindingAttempts = 2

// parsePlanningRoleType 校验本次调用只使用两个规划角色。
func parsePlanningRoleType(value string) (domain.RoleType, error) {
	roleType := domain.RoleType(strings.TrimSpace(value))
	switch roleType {
	case domain.RoleTypeRequirementAnalyst, domain.RoleTypePlanner:
		return roleType, nil
	default:
		return "", fmt.Errorf(
			"%w: 只支持 %s 与 %s，实际 %q",
			ErrPlanningAgentInvalidInput,
			domain.RoleTypeRequirementAnalyst,
			domain.RoleTypePlanner,
			value,
		)
	}
}

// planningRoleID 返回规划角色固定的角色标识。
//
// 角色标识与角色类型同名，配置由角色服务统一维护：这里只做映射，不新增角色，也不用
// 其他角色的配置兜底。
func planningRoleID(roleType domain.RoleType) (domain.ID, error) {
	switch roleType {
	case domain.RoleTypeRequirementAnalyst:
		return domain.RoleIDRequirementAnalyst, nil
	case domain.RoleTypePlanner:
		return domain.RoleIDPlanner, nil
	default:
		return "", fmt.Errorf(
			"%w: 角色类型 %q 不是规划角色",
			ErrPlanningAgentInvalidInput,
			roleType,
		)
	}
}

// planningRoleSessionID 返回规划会话中当前角色已绑定的 Pi 会话标识。
func planningRoleSessionID(
	session domain.PlanningSession,
	roleType domain.RoleType,
) (string, error) {
	var sessionID string
	switch roleType {
	case domain.RoleTypeRequirementAnalyst:
		sessionID = session.AnalystSessionID
	case domain.RoleTypePlanner:
		sessionID = session.PlannerSessionID
	default:
		return "", fmt.Errorf(
			"%w: 角色类型 %q 不是规划角色",
			ErrPlanningAgentInvalidInput,
			roleType,
		)
	}
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return "", fmt.Errorf(
			"%w: 规划会话 %s 尚未绑定 %s 角色的 Pi 会话标识",
			ErrPlanningAgentStateConflict,
			session.ID,
			roleType,
		)
	}
	return trimmed, nil
}

// ensureRoleSessions 保证两个规划角色的 Pi 会话标识已稳定绑定。
//
// 缺失的标识由服务端生成并成对写入；写入以读到的那对标识为前置条件，因此并发初始化时
// 只会有一个写入获胜，另一个重新读取并复用获胜的绑定，不会用新生成的值覆盖已绑定的
// 标识。已有标识一律保留：重新初始化、重新准备运行环境或角色配置变化都不改写它们。
func (s *PlanningAgentService) ensureRoleSessions(
	ctx context.Context,
	session domain.PlanningSession,
) (domain.PlanningSession, error) {
	current := session
	for attempt := 0; attempt < planningRoleBindingAttempts; attempt++ {
		if storage.PlanningSessionRoleBindingOf(current).Complete() {
			return current, nil
		}
		updated, err := s.bindRoleSessionsOnce(ctx, current)
		if err == nil {
			return updated, nil
		}
		if !errors.Is(err, storage.ErrConflict) {
			return session, err
		}
		// 绑定已被并发推进：重新读取数据库当前记录并复用获胜的绑定。
		latest, readErr := s.loadPlanningAgentSession(ctx, current.ID)
		if readErr != nil {
			return session, readErr
		}
		current = latest
	}
	if storage.PlanningSessionRoleBindingOf(current).Complete() {
		return current, nil
	}
	return session, fmt.Errorf(
		"%w: 规划会话 %s 的角色会话绑定被并发改写，请重新读取会话后再调用",
		ErrPlanningAgentStateConflict,
		session.ID,
	)
}

// bindRoleSessionsOnce 在单个事务内补齐缺失的角色会话标识。
func (s *PlanningAgentService) bindRoleSessionsOnce(
	ctx context.Context,
	session domain.PlanningSession,
) (domain.PlanningSession, error) {
	var updated domain.PlanningSession
	err := s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		current, err := tx.GetPlanningSession(ctx, session.ID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return fmt.Errorf("%w: 规划会话 %s 不存在", ErrPlanningAgentNotFound, session.ID)
			}
			return fmt.Errorf("读取规划会话失败: %w", err)
		}
		binding := storage.PlanningSessionRoleBindingOf(*current)
		if binding.Complete() {
			// 另一个执行者已经写全绑定：直接复用它的结果，不再生成新的标识。
			updated = *current
			return nil
		}
		next, err := planningRoleBindingNext(binding)
		if err != nil {
			return err
		}
		candidate := *current
		at := s.now()
		if err := candidate.BindRoleSessions(next.AnalystSessionID, next.PlannerSessionID, at); err != nil {
			return fmt.Errorf("%w: %w", ErrPlanningAgentInvalidInput, err)
		}
		if err := tx.UpdatePlanningSessionRoleBindings(ctx, session.ID, binding, next, at); err != nil {
			return mapPlanningRoleBindingFailure(err, session.ID)
		}
		updated = candidate
		return nil
	})
	if err != nil {
		return session, err
	}
	return updated, nil
}

// planningRoleBindingNext 为缺失的角色生成新的 Pi 会话标识。
//
// 已绑定的标识保持不变；两个标识必须不同，否则重新生成缺失的一侧。
func planningRoleBindingNext(
	current storage.PlanningSessionRoleBinding,
) (storage.PlanningSessionRoleBinding, error) {
	analyst := strings.TrimSpace(current.AnalystSessionID)
	planner := strings.TrimSpace(current.PlannerSessionID)
	if analyst == "" {
		analyst = domain.NewID().String()
	}
	if planner == "" {
		planner = domain.NewID().String()
	}
	if analyst == planner {
		if strings.TrimSpace(current.AnalystSessionID) == "" {
			analyst = domain.NewID().String()
		} else {
			planner = domain.NewID().String()
		}
	}
	if analyst == planner {
		return storage.PlanningSessionRoleBinding{}, fmt.Errorf(
			"%w: 无法为两个规划角色生成互不相同的会话标识",
			ErrPlanningAgentStateConflict,
		)
	}
	return storage.PlanningSessionRoleBinding{
		AnalystSessionID: analyst,
		PlannerSessionID: planner,
	}, nil
}

// mapPlanningRoleBindingFailure 把角色会话绑定的条件更新失败映射为业务错误。
func mapPlanningRoleBindingFailure(err error, sessionID domain.ID) error {
	switch {
	case errors.Is(err, storage.ErrConflict):
		return fmt.Errorf("%w: %w", storage.ErrConflict, err)
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%w: 规划会话 %s 不存在", ErrPlanningAgentNotFound, sessionID)
	default:
		return fmt.Errorf("更新规划会话角色会话绑定失败: %w", err)
	}
}
