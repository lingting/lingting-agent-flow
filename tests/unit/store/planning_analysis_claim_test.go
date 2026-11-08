package sqlite_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// 规划分析占用与版本快照版本的存储层用例。
//
// 占用是"当前是否有一轮分析在跑"的唯一答案：用例用真实 SQLite 验证抢占、释放与并发
// 冲突，断言以数据库读回的事实为准，不以方法返回值代替数据库状态。

// analyzingPlanningSession 写入一条处于 analyzing 的规划会话。
func analyzingPlanningSession(t *testing.T, db *sqlite.DB) domain.PlanningSession {
	t.Helper()
	session := newPlanningSession(t, db)
	if err := session.BeginAnalysis(planningBaseTime().Add(time.Minute)); err != nil {
		t.Fatalf("规划会话进入 analyzing 失败: %v", err)
	}
	if err := db.UpdatePlanningSessionIfStatus(
		testContext(t),
		session,
		[]domain.PlanningSessionStatus{domain.PlanningSessionActive},
	); err != nil {
		t.Fatalf("写入 analyzing 状态失败: %v", err)
	}
	return session
}

// TestClaimPlanningAnalysisRejectsSecondClaim 验证同一会话只允许一个占用。
func TestClaimPlanningAnalysisRejectsSecondClaim(t *testing.T) {
	t.Parallel()
	ctx := testContext(t)
	db := newTestDB(t)
	session := analyzingPlanningSession(t, db)
	at := planningBaseTime().Add(2 * time.Minute)

	if err := db.ClaimPlanningAnalysis(ctx, session.ID, "claim-a", at); err != nil {
		t.Fatalf("首次抢占失败: %v", err)
	}
	err := db.ClaimPlanningAnalysis(ctx, session.ID, "claim-b", at.Add(time.Second))
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("第二个占用未被拒绝: %v", err)
	}
	// 释放必须带同一个标识：错误的标识不能清掉别人的占用。
	err = db.ReleasePlanningAnalysis(ctx, session.ID, "claim-b", at.Add(2*time.Second))
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("非持有者释放未被拒绝: %v", err)
	}
	if err := db.ReleasePlanningAnalysis(ctx, session.ID, "claim-a", at.Add(3*time.Second)); err != nil {
		t.Fatalf("持有者释放失败: %v", err)
	}
	if err := db.ClaimPlanningAnalysis(ctx, session.ID, "claim-c", at.Add(4*time.Second)); err != nil {
		t.Fatalf("释放后重新抢占失败: %v", err)
	}
}

// TestClaimPlanningAnalysisRequiresAnalyzingStatus 验证非 analyzing 会话不能被占用。
func TestClaimPlanningAnalysisRequiresAnalyzingStatus(t *testing.T) {
	t.Parallel()
	ctx := testContext(t)
	db := newTestDB(t)
	session := newPlanningSession(t, db)
	err := db.ClaimPlanningAnalysis(ctx, session.ID, "claim-a", planningBaseTime().Add(time.Minute))
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("active 会话被抢占: %v", err)
	}
}

// TestReleasePlanningAnalysisKeepsSessionFacts 验证释放占用不改变会话状态。
func TestReleasePlanningAnalysisKeepsSessionFacts(t *testing.T) {
	t.Parallel()
	ctx := testContext(t)
	db := newTestDB(t)
	session := analyzingPlanningSession(t, db)
	at := planningBaseTime().Add(2 * time.Minute)
	if err := db.ClaimPlanningAnalysis(ctx, session.ID, "claim-a", at); err != nil {
		t.Fatalf("抢占失败: %v", err)
	}
	if err := db.ReleasePlanningAnalysis(ctx, session.ID, "claim-a", at.Add(time.Minute)); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	stored, err := db.GetPlanningSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	if stored.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("释放占用改变了状态: %s", stored.Status)
	}
	if stored.UpdatedAt.Before(at.Add(time.Minute)) {
		t.Fatalf("释放占用没有刷新更新时间: %v", stored.UpdatedAt)
	}
}

// TestClaimPlanningAnalysisSerializesConcurrentCallers 验证并发抢占只有一方成功。
func TestClaimPlanningAnalysisSerializesConcurrentCallers(t *testing.T) {
	t.Parallel()
	ctx := testContext(t)
	db := newTestDB(t)
	session := analyzingPlanningSession(t, db)
	at := planningBaseTime().Add(2 * time.Minute)

	const callers = 8
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
	)
	start := make(chan struct{})
	for index := 0; index < callers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			err := db.ClaimPlanningAnalysis(
				ctx,
				session.ID,
				"claim-"+string(rune('a'+index)),
				at,
			)
			if err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
				return
			}
			if !errors.Is(err, storage.ErrConflict) {
				t.Errorf("抢占返回了意外错误: %v", err)
			}
		}(index)
	}
	close(start)
	wg.Wait()
	if succeeded != 1 {
		t.Fatalf("并发抢占成功次数应为 1，实际 %d", succeeded)
	}
}

// TestAppendPlanningRevisionPersistsSnapshotVersion 验证版本快照版本随记录落库。
func TestAppendPlanningRevisionPersistsSnapshotVersion(t *testing.T) {
	t.Parallel()
	ctx := testContext(t)
	db := newTestDB(t)
	session := newPlanningSession(t, db)
	revision, err := db.AppendPlanningRevision(ctx, storage.AppendPlanningRevisionRequest{
		PlanningSessionID:       session.ID,
		RequirementAnalysisJSON: []byte(`{"summary":"需求"}`),
		ImplementationPlanJSON:  []byte(`{"summary":"方案"}`),
		At:                      planningBaseTime().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("追加规划版本失败: %v", err)
	}
	if revision.SnapshotVersion != domain.PlanningRevisionSnapshotVersion {
		t.Fatalf("版本快照版本未写入: %d", revision.SnapshotVersion)
	}
	revisions, err := db.ListPlanningRevisions(ctx, session.ID)
	if err != nil {
		t.Fatalf("查询规划版本失败: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("版本数量不为 1: %d", len(revisions))
	}
	if revisions[0].SnapshotVersion != domain.PlanningRevisionSnapshotVersion {
		t.Fatalf("读回的版本快照版本不一致: %d", revisions[0].SnapshotVersion)
	}
}
