package planninganalysis_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 服务端启动时的分析占用恢复用例。
//
// 恢复只处理"状态是 analyzing 且仍有占用"的会话：占用是"有执行者正在推进这一轮分析"
// 的唯一持久化证据，没有占用就不能推断进程中断。全部断言以数据库读回的事实为准。

// startRecovery 在既有现场上执行一次启动恢复。
func startRecovery(t *testing.T, f *analysisFixture, store app.PlanningAnalysisRecoveryStore) {
	t.Helper()
	recovery, err := app.NewPlanningAnalysisRecoveryService(
		app.PlanningAnalysisRecoveryOptions{Store: store},
	)
	if err != nil {
		t.Fatalf("构造分析占用恢复服务失败: %v", err)
	}
	if err := recovery.Start(f.ctx); err != nil {
		t.Fatalf("执行分析占用恢复失败: %v", err)
	}
}

// claimAnalysis 为 analyzing 会话写入一条分析占用并返回占用标识。
func claimAnalysis(t *testing.T, f *analysisFixture, sessionID domain.ID) string {
	t.Helper()
	claimID := domain.NewID().String()
	if err := f.db.ClaimPlanningAnalysis(f.ctx, sessionID, claimID, time.Now().UTC()); err != nil {
		t.Fatalf("抢占规划分析执行权失败: %v", err)
	}
	return claimID
}

// TestPlanningAnalysisRecoveryConvergesInterruptedClaim 验证遗留占用被收敛为失败。
func TestPlanningAnalysisRecoveryConvergesInterruptedClaim(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session := f.analyzingSession(t)
	claimAnalysis(t, f, session.ID)

	startRecovery(t, f, f.db)

	stored := f.loadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionFailed {
		t.Fatalf("遗留占用应被收敛为 failed，实际 %s", stored.Status)
	}
	if !strings.Contains(stored.ErrorSummary, "进程中断") {
		t.Fatalf("失败摘要没有记录进程中断原因: %q", stored.ErrorSummary)
	}
	if stored.FinishedAt == nil {
		t.Fatal("失败的会话必须记录结束时间")
	}
	assertClaimReleased(t, f, session.ID)
	// 恢复之后必须有明确出口：显式重试能够重新开始一轮分析。
	start, err := f.sessions.RetryAnalysis(f.ctx, session.ID.String(), app.RetryPlanningAnalysisRequest{})
	if err != nil {
		t.Fatalf("恢复后显式重试失败: %v", err)
	}
	if _, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
		ClaimID:           start.ClaimID,
	}); err != nil {
		t.Fatalf("恢复后重试分析失败: %v", err)
	}
	if revisions := f.revisions(t, session.ID); len(revisions) != 1 {
		t.Fatalf("恢复后重试应产生一条版本，实际 %d", len(revisions))
	}
}

// TestPlanningAnalysisRecoveryLeavesClaimlessAnalyzingUntouched 验证无占用的 analyzing 不动。
//
// 提交消息之后、抢占执行权之前进程退出会留下这个现场：它可能只是"用户已经提交需求，
// 还没触发分析"，因此不能直接判定为进程中断。
func TestPlanningAnalysisRecoveryLeavesClaimlessAnalyzingUntouched(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session := f.analyzingSession(t)

	startRecovery(t, f, f.db)

	stored := f.loadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("无占用的 analyzing 会话不应被收敛，实际 %s", stored.Status)
	}
	if stored.ErrorSummary != "" {
		t.Fatalf("无占用的 analyzing 会话不应写入失败摘要: %q", stored.ErrorSummary)
	}
}

// TestPlanningAnalysisRecoverySkipsNonAnalyzingSession 验证非 analyzing 会话不被覆盖。
func TestPlanningAnalysisRecoverySkipsNonAnalyzingSession(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session := f.analyzingSession(t)
	claimID := claimAnalysis(t, f, session.ID)

	// 直接构造"占用仍在、状态已被推进"的现场：恢复只处理 analyzing，不能覆盖它。
	advanced := session
	if err := advanced.MarkPreviewReady(time.Now().UTC()); err != nil {
		t.Fatalf("推进到 preview_ready 失败: %v", err)
	}
	if err := f.db.UpdatePlanningSessionIfStatus(
		f.ctx,
		advanced,
		[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
	); err != nil {
		t.Fatalf("写入 preview_ready 状态失败: %v", err)
	}

	startRecovery(t, f, f.db)

	stored := f.loadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionPreviewReady {
		t.Fatalf("恢复不得覆盖已推进的状态，实际 %s", stored.Status)
	}
	if stored.ErrorSummary != "" {
		t.Fatalf("恢复不得为已推进的会话写入失败摘要: %q", stored.ErrorSummary)
	}
	assertClaimHeld(t, f, session.ID, claimID)
}

// staleClaimStore 在列出占用之后把占用换成另一个执行者的标识。
//
// 它模拟"读取与写入之间执行权已被重新取得"的现场：恢复必须整体回滚，不能覆盖新的
// 占用，更不能把正在运行的会话改写成 failed。
type staleClaimStore struct {
	storage.Store
	replacement string
}

// ListPlanningAnalysisClaims 返回原始占用清单，并在此之前换掉占用标识。
func (s *staleClaimStore) ListPlanningAnalysisClaims(
	ctx context.Context,
) ([]storage.PlanningAnalysisClaim, error) {
	claims, err := s.Store.ListPlanningAnalysisClaims(ctx)
	if err != nil {
		return nil, err
	}
	at := time.Now().UTC()
	for _, claim := range claims {
		if err := s.Store.ReleasePlanningAnalysis(ctx, claim.SessionID, claim.ClaimID, at); err != nil {
			return nil, err
		}
		if err := s.Store.ClaimPlanningAnalysis(ctx, claim.SessionID, s.replacement, at); err != nil {
			return nil, err
		}
	}
	return claims, nil
}

// TestPlanningAnalysisRecoverySkipsClaimTakenByAnotherExecutor 验证并发占用不被覆盖。
func TestPlanningAnalysisRecoverySkipsClaimTakenByAnotherExecutor(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session := f.analyzingSession(t)
	claimAnalysis(t, f, session.ID)
	replacement := domain.NewID().String()

	startRecovery(t, f, &staleClaimStore{Store: f.db, replacement: replacement})

	stored := f.loadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("占用已换人时恢复不得推进状态，实际 %s", stored.Status)
	}
	if stored.ErrorSummary != "" {
		t.Fatalf("占用已换人时恢复不得写入失败摘要: %q", stored.ErrorSummary)
	}
	assertClaimHeld(t, f, session.ID, replacement)
}

// TestPlanningAnalysisRecoveryIsIdempotent 验证重复执行恢复不会再改动任何会话。
func TestPlanningAnalysisRecoveryIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session := f.analyzingSession(t)
	claimAnalysis(t, f, session.ID)

	startRecovery(t, f, f.db)
	once := f.loadSession(t, session.ID)
	startRecovery(t, f, f.db)
	twice := f.loadSession(t, session.ID)

	if once.Status != domain.PlanningSessionFailed || twice.Status != once.Status {
		t.Fatalf("重复恢复改变了状态: %s / %s", once.Status, twice.Status)
	}
	if twice.ErrorSummary != once.ErrorSummary {
		t.Fatalf("重复恢复改写了失败摘要: %q / %q", once.ErrorSummary, twice.ErrorSummary)
	}
	if twice.UpdatedAt.After(once.UpdatedAt) {
		t.Fatalf("重复恢复不应再次写入会话: %v / %v", once.UpdatedAt, twice.UpdatedAt)
	}
}
