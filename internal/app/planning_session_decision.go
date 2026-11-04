package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// Confirm 采纳某一版方案并结束规划会话。
//
// 前置条件固定为：会话存在、方案版本确实属于该会话、会话处于待确认状态。三者都在
// 同一个事务内校验并写入，状态迁移与 AdoptedRevisionID 同时落库，条件更新以
// preview_ready 为前置条件，因此两个并发确认请求不可能采纳不同的版本。
//
// 确认只表示用户采纳了该版规划：本阶段不创建正式 Requirement，也不创建 Task。重复
// 确认、跨会话版本与非法状态确认分别返回明确的业务错误。
func (s *PlanningSessionService) Confirm(
	ctx context.Context,
	sessionIDValue string,
	revisionIDValue string,
) (domain.PlanningSession, error) {
	sessionID, err := parsePlanningSessionID(sessionIDValue)
	if err != nil {
		return domain.PlanningSession{}, err
	}
	revisionID := domain.ID(strings.TrimSpace(revisionIDValue))
	if err := revisionID.Validate(); err != nil {
		return domain.PlanningSession{}, fmt.Errorf(
			"%w: 方案版本标识不能为空",
			ErrPlanningSessionInvalidInput,
		)
	}
	var session domain.PlanningSession
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		loaded, err := readPlanningSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		// 归属校验先于状态迁移：跨会话的版本标识不能被当成本会话的方案采纳。
		if err := ensurePlanningRevisionBelongs(ctx, tx, sessionID, revisionID); err != nil {
			return err
		}
		if err := loaded.Confirm(revisionID, s.now()); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			loaded,
			[]domain.PlanningSessionStatus{domain.PlanningSessionPreviewReady},
		); err != nil {
			return mapPlanningUpdateFailure(err, sessionID)
		}
		session = loaded
		return nil
	})
	if err != nil {
		return domain.PlanningSession{}, err
	}
	return session, nil
}

// Abandon 放弃本次规划并结束会话。
//
// 放弃只写会话状态：消息、方案版本、分析快照与运行期绑定全部保留，不删除任何记录。
// 是否允许放弃由 domain 状态机决定——analyzing 期间不允许放弃，必须先等本轮收敛。
func (s *PlanningSessionService) Abandon(
	ctx context.Context,
	sessionIDValue string,
) (domain.PlanningSession, error) {
	sessionID, err := parsePlanningSessionID(sessionIDValue)
	if err != nil {
		return domain.PlanningSession{}, err
	}
	var session domain.PlanningSession
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		loaded, err := readPlanningSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		previous := loaded.Status
		if err := loaded.Abandon(s.now()); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			loaded,
			[]domain.PlanningSessionStatus{previous},
		); err != nil {
			return mapPlanningUpdateFailure(err, sessionID)
		}
		session = loaded
		return nil
	})
	if err != nil {
		return domain.PlanningSession{}, err
	}
	return session, nil
}

// ensurePlanningRevisionBelongs 确认方案版本存在且属于指定规划会话。
//
// 读取按会话过滤，因此"属于其他会话的版本"与"版本不存在"返回同一个业务错误：调用方
// 不能通过错误差异探测其他会话产生了哪些版本。
func ensurePlanningRevisionBelongs(
	ctx context.Context,
	tx storage.Tx,
	sessionID domain.ID,
	revisionID domain.ID,
) error {
	revisions, err := tx.ListPlanningRevisions(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("读取规划版本失败: %w", err)
	}
	for _, revision := range revisions {
		if revision.ID == revisionID {
			return nil
		}
	}
	return fmt.Errorf(
		"%w: 方案版本 %s 不属于规划会话 %s",
		ErrPlanningSessionRevisionNotFound,
		revisionID,
		sessionID,
	)
}
