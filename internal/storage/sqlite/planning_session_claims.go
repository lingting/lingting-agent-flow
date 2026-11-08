package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 规划分析占用的两条条件更新语句。
//
// 抢占要求"状态是 analyzing 且当前没有占用"；释放要求"占用仍属于本次调用"。两条语句
// 都不带进程内状态：并发抢占只有一方能把 analysis_claim_id 从 NULL 改成自己的标识，
// 因此在多个进程共用同一份数据库时同样成立。
const (
	claimPlanningAnalysisStatement = `UPDATE planning_sessions
		SET analysis_claim_id = ?, analysis_claimed_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND analysis_claim_id IS NULL`

	releasePlanningAnalysisStatement = `UPDATE planning_sessions
		SET analysis_claim_id = NULL, analysis_claimed_at = NULL, updated_at = ?
		WHERE id = ? AND analysis_claim_id = ?`
)

// ClaimPlanningAnalysis 以数据库条件更新抢占一轮分析的执行权。
//
// 条件不成立（状态不是 analyzing 或已有占用）时影响行数为 0：前者说明这一轮还没开始或
// 已经结束，后者说明另一轮正在运行。两种情况都按 storage.ErrConflict 返回，调用方必须
// 重新读取会话，不得假定自己拿到了执行权。
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
