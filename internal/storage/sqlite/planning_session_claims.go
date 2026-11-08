package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 规划分析占用的三条语句。
//
// 抢占要求"状态是 analyzing 且当前没有占用，或占用标识就是本次提交的标识"；释放要求
// "占用仍属于本次调用"。语句都不带进程内状态：并发抢占只有一方能把 analysis_claim_id
// 从 NULL 改成自己的标识，因此在多个进程共用同一份数据库时同样成立。
//
// 允许"标识相同"的抢占是为了让显式重试入口能先把执行权拿到手，再由分析编排确认同一次
// 执行的占用：确认是幂等的，别人持有的占用仍然抢占不到。
const (
	claimPlanningAnalysisStatement = `UPDATE planning_sessions
		SET analysis_claim_id = ?, analysis_claimed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?
		AND (analysis_claim_id IS NULL OR analysis_claim_id = ?)`

	releasePlanningAnalysisStatement = `UPDATE planning_sessions
		SET analysis_claim_id = NULL, analysis_claimed_at = NULL, updated_at = ?
		WHERE id = ? AND analysis_claim_id = ?`

	listPlanningAnalysisClaimsStatement = `SELECT id, analysis_claim_id, analysis_claimed_at
		FROM planning_sessions
		WHERE analysis_claim_id IS NOT NULL
		ORDER BY analysis_claimed_at, id`
)

// ClaimPlanningAnalysis 以数据库条件更新抢占一轮分析的执行权。
//
// 条件不成立（状态不是 analyzing 或已有占用）时影响行数为 0：前者说明这一轮还没开始或
// 已经结束，后者说明另一轮正在运行。两种情况都按 storage.ErrConflict 返回，调用方必须
// 重新读取会话，不得假定自己拿到了执行权。
//
// 占用标识与数据库中已有的标识相同时视为同一次执行的确认，语句成功且只是刷新占用时间。
func (s planningSessionStore) ClaimPlanningAnalysis(
	ctx context.Context,
	id domain.ID,
	claimID string,
	at time.Time,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	claim := strings.TrimSpace(claimID)
	if claim == "" {
		return fmt.Errorf("sqlite: 抢占规划分析执行权失败: 占用标识不能为空")
	}
	millis := toMillis(at)
	result, err := s.q.ExecContext(
		ctx,
		claimPlanningAnalysisStatement,
		claim,
		millis,
		millis,
		toIDParam(id),
		string(domain.PlanningSessionAnalyzing),
		claim,
	)
	if err != nil {
		return wrapError("抢占规划分析执行权", err)
	}
	return ensureStatusAffected(result, "规划会话", id)
}

// ReleasePlanningAnalysis 以占用标识为条件释放一轮分析的执行权。
//
// 只清除占用列并刷新 updated_at：状态、结束时间、采纳的方案版本、失败摘要与运行时绑定
// 保持数据库中的值，因此释放占用既不能推进也不能回退会话状态。标识不匹配说明占用已不
// 属于本次调用（例如已被显式重试重新抢占），返回 storage.ErrConflict。
func (s planningSessionStore) ReleasePlanningAnalysis(
	ctx context.Context,
	id domain.ID,
	claimID string,
	at time.Time,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	claim := strings.TrimSpace(claimID)
	if claim == "" {
		return fmt.Errorf("sqlite: 释放规划分析执行权失败: 占用标识不能为空")
	}
	result, err := s.q.ExecContext(
		ctx,
		releasePlanningAnalysisStatement,
		toMillis(at),
		toIDParam(id),
		claim,
	)
	if err != nil {
		return wrapError("释放规划分析执行权", err)
	}
	return ensureStatusAffected(result, "规划会话", id)
}

// ListPlanningAnalysisClaims 返回当前仍持有分析占用的规划会话。
//
// 只按"存在占用"筛选，不附加状态条件：状态条件由启动恢复在事务内再次核对，这样
// 读取与写入之间被其他执行者推进的会话仍然只按数据库中的新事实处理。
func (s planningSessionStore) ListPlanningAnalysisClaims(
	ctx context.Context,
) ([]storage.PlanningAnalysisClaim, error) {
	rows, err := s.q.QueryContext(ctx, listPlanningAnalysisClaimsStatement)
	if err != nil {
		return nil, wrapError("查询规划分析占用", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	claims := make([]storage.PlanningAnalysisClaim, 0, 4)
	for rows.Next() {
		var (
			sessionID string
			claimID   string
			claimedAt sql.NullInt64
		)
		if err := rows.Scan(&sessionID, &claimID, &claimedAt); err != nil {
			return nil, wrapError("读取规划分析占用", err)
		}
		claims = append(claims, storage.PlanningAnalysisClaim{
			SessionID: domain.ID(sessionID),
			ClaimID:   claimID,
			ClaimedAt: fromMillis(claimedAt.Int64),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("查询规划分析占用", err)
	}
	return claims, nil
}

// PlanningAnalysisClaim 返回单个会话当前的分析占用标识。
//
// 没有占用时返回空字符串；会话不存在时返回 storage.ErrNotFound，因此"不存在"与
// "没有占用"不会被混为一谈。
func (s planningSessionStore) PlanningAnalysisClaim(
	ctx context.Context,
	id domain.ID,
) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	const statement = `SELECT analysis_claim_id FROM planning_sessions WHERE id = ?`
	var claim sql.NullString
	if err := s.q.QueryRowContext(ctx, statement, toIDParam(id)).Scan(&claim); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", notFoundError("规划会话", id.String())
		}
		return "", wrapError("读取规划分析占用", err)
	}
	return strings.TrimSpace(claim.String), nil
}
