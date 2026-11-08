package planninganalysis_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 持久化失败与重试语义的用例。
//
// 失败注入只替换"某一步写入"，事务边界、回滚与条件更新仍然由真实 SQLite 执行：断言
// 因此来自数据库状态，而不是服务返回值。

// errSimulatedCommitFailure 表示事务在提交阶段失败。
var errSimulatedCommitFailure = errors.New("模拟事务提交失败")

// txHookStore 在真实事务内用替身包装 Tx，并可让提交阶段失败。
//
// 它让"某一类写入失败"与"提交失败"落在真实的 SQLite 事务里：fn 返回错误或提交返回
// 错误时数据库整体回滚，因此用例可以断言"没有半成品"。
type txHookStore struct {
	storage.Store
	// wrapTx 为空表示只注入提交失败。
	wrapTx func(tx storage.Tx) storage.Tx
	// failCommit 为 true 时事务在 fn 成功之后返回错误，模拟提交失败。
	failCommit bool
}

// WithTx 在真实事务内执行并注入失败。
func (s txHookStore) WithTx(
	ctx context.Context,
	fn func(ctx context.Context, tx storage.Tx) error,
) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		wrapped := tx
		if s.wrapTx != nil {
			wrapped = s.wrapTx(tx)
		}
		if err := fn(ctx, wrapped); err != nil {
			return err
		}
		if s.failCommit {
			return errSimulatedCommitFailure
		}
		return nil
	})
}

// failingMessageTx 让指定发送方的角色消息写入失败。
type failingMessageTx struct {
	storage.Tx
	sender domain.PlanningSenderType
}

// AppendPlanningMessage 模拟指定发送方的消息写入失败。
func (t failingMessageTx) AppendPlanningMessage(
	ctx context.Context,
	request storage.AppendPlanningMessageRequest,
) (domain.PlanningMessage, error) {
	if request.SenderType == t.sender {
		return domain.PlanningMessage{}, fmt.Errorf(
			"模拟 %s 角色消息写入失败",
			request.SenderType,
		)
	}
	return t.Tx.AppendPlanningMessage(ctx, request)
}

// failingPreviewReadyTx 让"推进到 preview_ready"的条件更新失败。
//
// 失败记录使用的是 failed 状态，因此不受影响：用例可以区分"成功路径回滚"与"失败
// 事实落库"两件事。
type failingPreviewReadyTx struct {
	storage.Tx
}

// UpdatePlanningSessionIfStatus 在目标状态为 preview_ready 时返回冲突。
func (t failingPreviewReadyTx) UpdatePlanningSessionIfStatus(
	ctx context.Context,
	session domain.PlanningSession,
	expected []domain.PlanningSessionStatus,
) error {
	if session.Status == domain.PlanningSessionPreviewReady {
		return fmt.Errorf("模拟状态条件更新失败: %w", storage.ErrConflict)
	}
	return t.Tx.UpdatePlanningSessionIfStatus(ctx, session, expected)
}

// rejectReleaseTx 让前若干次事务内占用释放失败。
//
// 计数器有上限：成功路径的释放失败会让整个持久化事务回滚，随后失败记录事务里的释放
// 必须能够成功，用例才能观察到"回滚 + 失败结论落库"两件事同时成立。
type rejectReleaseTx struct {
	storage.Tx
	remaining *int32
}

// ReleasePlanningAnalysis 在前 remaining 次调用时失败。
func (t rejectReleaseTx) ReleasePlanningAnalysis(
	ctx context.Context,
	id domain.ID,
	claimID string,
	at time.Time,
) error {
	if atomic.AddInt32(t.remaining, -1) >= 0 {
		return errors.New("模拟占用释放失败")
	}
	return t.Tx.ReleasePlanningAnalysis(ctx, id, claimID, at)
}

// analysisServiceWithStore 用给定存储构造分析服务并套在既有现场上。
func analysisServiceWithStore(
	t *testing.T,
	base *analysisFixture,
	store storage.Store,
) *analysisFixture {
	t.Helper()
	analysis, err := app.NewPlanningAnalysisService(app.PlanningAnalysisServiceOptions{
		Store: store,
		Agent: base.agent,
	})
	if err != nil {
		t.Fatalf("构造规划分析服务失败: %v", err)
	}
	f := newAnalysisFixtureOver(t, base)
	f.analysis = analysis
	return f
}

// assertNoSuccessArtifacts 断言本轮没有留下任何成功产物。
func assertNoSuccessArtifacts(t *testing.T, f *analysisFixture, sessionID domain.ID) {
	t.Helper()
	if revisions := f.revisions(t, sessionID); len(revisions) != 0 {
		t.Fatalf("持久化失败仍留下版本: %d", len(revisions))
	}
	// 只剩提交需求时的用户消息：两条角色消息必须随事务一起回滚。
	if messages := f.messages(t, sessionID); len(messages) != 1 {
		t.Fatalf("持久化失败仍留下角色消息: %d", len(messages))
	}
}

// TestAnalyzeRollsBackWhenStageMessageNotPersisted 验证任一角色消息写入失败都整体回滚。
func TestAnalyzeRollsBackWhenStageMessageNotPersisted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		sender domain.PlanningSenderType
	}{
		{name: "需求分析师消息写入失败", sender: domain.PlanningSenderRequirementAnalyst},
		{name: "规划师消息写入失败", sender: domain.PlanningSenderPlanner},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			base := newAnalysisFixture(t, analysisFixtureOptions{})
			f := analysisServiceWithStore(t, base, txHookStore{
				Store: base.db,
				wrapTx: func(tx storage.Tx) storage.Tx {
					return failingMessageTx{Tx: tx, sender: testCase.sender}
				},
			})
			session := f.analyzingSession(t)

			_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
				PlanningSessionID: session.ID.String(),
			})
			if !errors.Is(err, app.ErrPlanningAnalysisNotPersisted) {
				t.Fatalf("消息写入失败未被报告: %v", err)
			}
			assertNoSuccessArtifacts(t, f, session.ID)
			stored := f.loadSession(t, session.ID)
			if stored.Status != domain.PlanningSessionFailed {
				t.Fatalf("写入失败后状态应为 failed，实际 %s", stored.Status)
			}
			if !strings.Contains(stored.ErrorSummary, "未能保存") {
				t.Fatalf("失败摘要没有记录持久化失败: %q", stored.ErrorSummary)
			}
			assertClaimReleased(t, f, session.ID)
		})
	}
}

// TestAnalyzeRollsBackWhenStatusUpdateConflicts 验证状态条件更新失败时整体回滚。
func TestAnalyzeRollsBackWhenStatusUpdateConflicts(t *testing.T) {
	t.Parallel()
	base := newAnalysisFixture(t, analysisFixtureOptions{})
	f := analysisServiceWithStore(t, base, txHookStore{
		Store: base.db,
		wrapTx: func(tx storage.Tx) storage.Tx {
			return failingPreviewReadyTx{Tx: tx}
		},
	})
	session := f.analyzingSession(t)

	_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	})
	if !errors.Is(err, app.ErrPlanningAnalysisNotPersisted) {
		t.Fatalf("状态写入失败未被报告: %v", err)
	}
	assertNoSuccessArtifacts(t, f, session.ID)
	stored := f.loadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionFailed {
		t.Fatalf("状态写入失败后应记为 failed，实际 %s", stored.Status)
	}
	assertClaimReleased(t, f, session.ID)
}

// TestAnalyzeRollsBackWhenClaimNotReleased 验证占用释放失败时成功路径整体回滚。
func TestAnalyzeRollsBackWhenClaimNotReleased(t *testing.T) {
	t.Parallel()
	base := newAnalysisFixture(t, analysisFixtureOptions{})
	remaining := int32(1)
	f := analysisServiceWithStore(t, base, txHookStore{
		Store: base.db,
		wrapTx: func(tx storage.Tx) storage.Tx {
			return rejectReleaseTx{Tx: tx, remaining: &remaining}
		},
	})
	session := f.analyzingSession(t)

	_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	})
	if !errors.Is(err, app.ErrPlanningAnalysisNotPersisted) {
		t.Fatalf("占用释放失败未被报告: %v", err)
	}
	assertNoSuccessArtifacts(t, f, session.ID)
	stored := f.loadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionFailed {
		t.Fatalf("占用释放失败后应记为 failed，实际 %s", stored.Status)
	}
	assertClaimReleased(t, f, session.ID)
}

// TestAnalyzeReportsNotPersistedWhenFailureRecordFails 验证失败记录写不成时不虚报失败。
func TestAnalyzeReportsNotPersistedWhenFailureRecordFails(t *testing.T) {
	t.Parallel()
	base := newAnalysisFixture(t, analysisFixtureOptions{})
	f := analysisServiceWithStore(t, base, txHookStore{
		Store:      base.db,
		failCommit: true,
	})
	session := f.analyzingSession(t)

	_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	})
	if !errors.Is(err, app.ErrPlanningAnalysisNotPersisted) {
		t.Fatalf("提交失败未被报告: %v", err)
	}
	if !errors.Is(err, errSimulatedCommitFailure) {
		t.Fatalf("错误链应保留提交失败原因: %v", err)
	}
	assertNoSuccessArtifacts(t, f, session.ID)
	// 失败事实也没能落库：会话必须保持原状态，不能虚报 failed。
	stored := f.loadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("失败记录写不成时状态不得变化，实际 %s", stored.Status)
	}
	if stored.ErrorSummary != "" {
		t.Fatalf("失败记录写不成时不得写入摘要: %q", stored.ErrorSummary)
	}
	// 占用必须由兜底路径释放，否则会话既不能重试也无法解释。
	assertClaimReleased(t, f, session.ID)
}

// TestRetryAnalysisRecoversClaimlessAnalyzingSession 验证无占用的 analyzing 可以重试。
//
// 提交消息之后、抢占执行权之前进程退出会留下这个现场：它没有执行者，状态却仍是
// analyzing，因此必须能通过显式重试恢复，同时不能重复追加用户消息。
func TestRetryAnalysisRecoversClaimlessAnalyzingSession(t *testing.T) {
	t.Parallel()
	agentPort := newFakeAnalysisAgent()
	f := newAnalysisFixture(t, analysisFixtureOptions{agent: agentPort})
	session := f.analyzingSession(t)
	before := len(f.messages(t, session.ID))

	start, err := f.sessions.RetryAnalysis(f.ctx, session.ID.String(), app.RetryPlanningAnalysisRequest{
		Content: "补充：先给出结构化分析",
	})
	if err != nil {
		t.Fatalf("无占用的 analyzing 会话重试失败: %v", err)
	}
	if start.Session.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("重试后状态应为 analyzing，实际 %s", start.Session.Status)
	}
	assertClaimHeld(t, f, session.ID, start.ClaimID)
	messages := f.messages(t, session.ID)
	if len(messages) != before+1 {
		t.Fatalf("补充消息应与执行权一同落库，期望 %d 条，实际 %d", before+1, len(messages))
	}

	// 第二个重试请求发生在执行权已被占用之后：必须冲突，且不得重复追加消息。
	if _, err := f.sessions.RetryAnalysis(
		f.ctx,
		session.ID.String(),
		app.RetryPlanningAnalysisRequest{Content: "重复请求"},
	); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
		t.Fatalf("已有执行占用时重试应返回冲突，实际 %v", err)
	}
	if got := len(f.messages(t, session.ID)); got != before+1 {
		t.Fatalf("被拒绝的重试不得留下消息，实际 %d", got)
	}

	if _, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
		ClaimID:           start.ClaimID,
	}); err != nil {
		t.Fatalf("重试后分析失败: %v", err)
	}
	if revisions := f.revisions(t, session.ID); len(revisions) != 1 {
		t.Fatalf("重试应产生一条版本，实际 %d", len(revisions))
	}
	assertClaimReleased(t, f, session.ID)
}

// TestRetryAnalysisRejectsActiveSession 验证不处于失败或无占用 analyzing 的会话不能重试。
func TestRetryAnalysisRejectsActiveSession(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session, err := f.sessions.Create(f.ctx, app.CreatePlanningSessionRequest{ProjectID: f.project.ID})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	if _, err := f.sessions.RetryAnalysis(
		f.ctx,
		session.ID.String(),
		app.RetryPlanningAnalysisRequest{Content: "直接重试"},
	); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
		t.Fatalf("active 会话重试应返回冲突，实际 %v", err)
	}
	if stored := f.loadSession(t, session.ID); stored.Status != domain.PlanningSessionActive {
		t.Fatalf("被拒绝的重试不得改变状态，实际 %s", stored.Status)
	}
}
