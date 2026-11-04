package sqlite

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// UpdatePlanningSessionRoleBindings 以读取时的角色会话绑定为条件写入新的绑定。
//
// 只更新 analyst_session_id、planner_session_id 与 updated_at：status、finished_at、
// adopted_revision_id、error_summary、host_id、container_id、source_commit 与
// project_id 保持数据库中的值，因此角色会话初始化不可能覆盖并发推进的会话状态、
// 运行时绑定或采纳的方案版本。
//
// WHERE 条件对两个绑定列做 NULL 安全比较（COALESCE 把 NULL 与空值归一为空串）：
// 未绑定状态必须能与调用方读到的空值正确匹配，否则"首次初始化"永远匹配不上，或者退化
// 成一条不带条件的覆盖写入。影响行数为 0 说明会话不存在或绑定已被其他操作改写，前者
// 返回 storage.ErrNotFound，后者返回 storage.ErrConflict。
func (s planningSessionStore) UpdatePlanningSessionRoleBindings(
	ctx context.Context,
	id domain.ID,
	expected storage.PlanningSessionRoleBinding,
	next storage.PlanningSessionRoleBinding,
	at time.Time,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	analystSessionID := strings.TrimSpace(next.AnalystSessionID)
	plannerSessionID := strings.TrimSpace(next.PlannerSessionID)
	if analystSessionID == "" || plannerSessionID == "" {
		return wrapError(
			"更新规划会话角色会话绑定",
			errors.Join(
				storage.ErrInvalidData,
				errors.New("两个角色的 Pi 会话标识必须成对写入"),
			),
		)
	}
	if analystSessionID == plannerSessionID {
		return wrapError(
			"更新规划会话角色会话绑定",
			errors.Join(
				storage.ErrInvalidData,
				errors.New("两个角色的 Pi 会话标识必须不同"),
			),
		)
	}
	// 空值在 SQL 里是 NULL，不能参与等值比较：归一为 '' 后再与 COALESCE 的结果比对。
	const statement = `
UPDATE planning_sessions SET
	analyst_session_id = ?, planner_session_id = ?, updated_at = ?
WHERE id = ?
	AND COALESCE(analyst_session_id, '') = ?
	AND COALESCE(planner_session_id, '') = ?`
	result, err := s.q.ExecContext(
		ctx,
		statement,
		analystSessionID,
		plannerSessionID,
		toMillis(at),
		toIDParam(id),
		strings.TrimSpace(expected.AnalystSessionID),
		strings.TrimSpace(expected.PlannerSessionID),
	)
	if err != nil {
		return wrapError("更新规划会话角色会话绑定", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapError("更新规划会话角色会话绑定", err)
	}
	if affected > 0 {
		return nil
	}
	// 影响行数为 0 有两种可能：会话不存在，或绑定已被其他操作改写。两者都必须让调用方
	// 重新读取会话，不能当作写入成功。
	current, readErr := s.GetPlanningSession(ctx, id)
	if readErr != nil {
		if errors.Is(readErr, storage.ErrNotFound) {
			return readErr
		}
		return wrapError("更新规划会话角色会话绑定", readErr)
	}
	return wrapError(
		"更新规划会话角色会话绑定",
		errors.Join(
			storage.ErrConflict,
			errors.New(
				"角色会话绑定已被其他操作改写，当前需求分析师会话 "+current.AnalystSessionID+
					"、方案规划师会话 "+current.PlannerSessionID,
			),
		),
	)
}
