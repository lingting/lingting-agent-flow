package planninganalysis_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// 两阶段分析编排用例。
//
// 每个断言都重新从存储读取记录：占用释放、事务边界与回滚的实际效果只能由数据库状态
// 证明，不能用服务返回值代替。全部用例使用 Fake Agent，不依赖真实模型或外部网络。

// TestAnalyzeProducesSingleRevision 验证两个阶段成功时只产生一个完整 Revision。
func TestAnalyzeProducesSingleRevision(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session := f.analyzingSession(t)

	result, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	})
	if err != nil {
		t.Fatalf("执行两阶段分析失败: %v", err)
	}
	if result.Revision.RevisionNumber != 1 {
		t.Fatalf("首轮版本号应为 1，实际 %d", result.Revision.RevisionNumber)
	}
	if result.Session.Status != domain.PlanningSessionPreviewReady {
		t.Fatalf("会话状态应为 preview_ready，实际 %s", result.Session.Status)
	}
	// 角色顺序必须是分析师在前、规划师在后。
	types := f.agent.roleTypes()
	if len(types) != 2 ||
		types[0] != string(domain.RoleTypeRequirementAnalyst) ||
		types[1] != string(domain.RoleTypePlanner) {
		t.Fatalf("角色调用顺序不符: %v", types)
	}
	// 规划师必须看到分析师给出的完整结构化结论。
	plannerPrompt := f.agent.requestAt(t, 1).Prompt
	for _, fragment := range []string{"constraints", "out_of_scope", "保持项目只读", "不创建正式任务"} {
		if !strings.Contains(plannerPrompt, fragment) {
			t.Fatalf("规划师提示词缺少需求分析内容 %q", fragment)
		}
	}
	// 分析师提示词必须明确项目只读与固定快照。
	analystPrompt := f.agent.requestAt(t, 0).Prompt
	for _, fragment := range []string{"只读取项目内容", f.project.EffectiveWorkDir(), analysisRequirement} {
		if !strings.Contains(analystPrompt, fragment) {
			t.Fatalf("分析师提示词缺少内容 %q", fragment)
		}
	}

	revisions := f.revisions(t, session.ID)
	if len(revisions) != 1 {
		t.Fatalf("版本数量应为 1，实际 %d", len(revisions))
	}
	revision := revisions[0]
	if revision.SnapshotVersion != domain.PlanningRevisionSnapshotVersion {
		t.Fatalf("版本快照版本不符: %d", revision.SnapshotVersion)
	}
	analysis, err := revision.RequirementAnalysis()
	if err != nil {
		t.Fatalf("读回需求分析失败: %v", err)
	}
	if analysis.Summary != "补齐两阶段结构化分析" {
		t.Fatalf("读回的需求分析摘要不符: %q", analysis.Summary)
	}
	plan, err := revision.ImplementationPlan()
	if err != nil {
		t.Fatalf("读回实施方案失败: %v", err)
	}
	if len(plan.Tasks) != 2 {
		t.Fatalf("读回的任务数量不符: %d", len(plan.Tasks))
	}
	if len(revision.ValidationResultJSON) == 0 {
		t.Fatal("版本没有记录结构化校验结论")
	}

	messages := f.messages(t, session.ID)
	if len(messages) != 3 {
		t.Fatalf("消息数量应为 3（1 条用户 + 2 条角色），实际 %d", len(messages))
	}
	if messages[0].SenderType != domain.PlanningSenderUser {
		t.Fatalf("首条消息应为用户消息: %s", messages[0].SenderType)
	}
	if messages[1].SenderType != domain.PlanningSenderRequirementAnalyst ||
		messages[2].SenderType != domain.PlanningSenderPlanner {
		t.Fatalf("角色消息发送方不符: %s / %s", messages[1].SenderType, messages[2].SenderType)
	}
	if messages[1].Content != string(revision.RequirementAnalysisJSON) {
		t.Fatal("分析师消息正文与版本快照不一致")
	}
}

// TestAnalyzeSkipsPlannerWhenAnalystUnusable 验证分析师阶段不可用时不会调用规划师。
func TestAnalyzeSkipsPlannerWhenAnalystUnusable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(agentPort *fakeAnalysisAgent)
		want  error
	}{
		{
			name: "调用失败",
			setup: func(agentPort *fakeAnalysisAgent) {
				agentPort.setError(domain.RoleTypeRequirementAnalyst, errors.New("容器执行失败"))
			},
			want: app.ErrPlanningAnalysisAgentFailed,
		},
		{
			name: "输出为自然语言",
			setup: func(agentPort *fakeAnalysisAgent) {
				agentPort.setText(domain.RoleTypeRequirementAnalyst, "这次需求很清晰。")
			},
			want: app.ErrPlanningAnalysisOutputInvalid,
		},
		{
			name: "输出含未知字段",
			setup: func(agentPort *fakeAnalysisAgent) {
				agentPort.setText(
					domain.RoleTypeRequirementAnalyst,
					strings.Replace(validAnalysisJSON(), `"summary":`, `"confidence":0.9,"summary":`, 1),
				)
			},
			want: app.ErrPlanningAnalysisOutputInvalid,
		},
		{
			name: "输出为空",
			setup: func(agentPort *fakeAnalysisAgent) {
				agentPort.setText(domain.RoleTypeRequirementAnalyst, "   ")
			},
			want: app.ErrPlanningAnalysisAgentFailed,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			agentPort := newFakeAnalysisAgent()
			f := newAnalysisFixture(t, analysisFixtureOptions{agent: agentPort})
			session := f.analyzingSession(t)
			testCase.setup(agentPort)

			_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
				PlanningSessionID: session.ID.String(),
			})
			if err == nil {
				t.Fatal("分析师阶段不可用时分析却报告成功")
			}
			if !errors.Is(err, testCase.want) {
				t.Fatalf("错误类别不符: 期望 %v，实际 %v", testCase.want, err)
			}
			if calls := agentPort.callsFor(domain.RoleTypePlanner); calls != 0 {
				t.Fatalf("分析师阶段失败后仍调用了规划师 %d 次", calls)
			}
			if revisions := f.revisions(t, session.ID); len(revisions) != 0 {
				t.Fatalf("失败的分析产生了版本: %d", len(revisions))
			}
			stored := f.loadSession(t, session.ID)
			if stored.Status != domain.PlanningSessionFailed {
				t.Fatalf("失败后会话状态应为 failed，实际 %s", stored.Status)
			}
			if !strings.Contains(stored.ErrorSummary, "需求分析阶段") {
				t.Fatalf("失败摘要没有记录阶段: %q", stored.ErrorSummary)
			}
			// 失败不写角色消息：消息总数仍只有提交需求时的那一条。
			if messages := f.messages(t, session.ID); len(messages) != 1 {
				t.Fatalf("失败的分析写入了角色消息: %d", len(messages))
			}
		})
	}
}

// TestAnalyzeFailsWithoutRevisionWhenPlannerUnusable 验证规划师阶段失败时不产生版本。
func TestAnalyzeFailsWithoutRevisionWhenPlannerUnusable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(agentPort *fakeAnalysisAgent)
		want  error
	}{
		{
			name: "调用失败",
			setup: func(agentPort *fakeAnalysisAgent) {
				agentPort.setError(domain.RoleTypePlanner, errors.New("容器执行失败"))
			},
			want: app.ErrPlanningAnalysisAgentFailed,
		},
		{
			name: "执行超时",
			setup: func(agentPort *fakeAnalysisAgent) {
				agentPort.setStatus(domain.RoleTypePlanner, agent.RunTimedOut)
			},
			want: app.ErrPlanningAnalysisAgentFailed,
		},
		{
			name: "任务标识重复",
			setup: func(agentPort *fakeAnalysisAgent) {
				agentPort.setText(
					domain.RoleTypePlanner,
					`{"summary":"s","tasks":[`+
						`{"id":"t1","title":"A","description":"a","depends_on":[],`+
						`"acceptance_criteria":["c"]},`+
						`{"id":"t1","title":"B","description":"b","depends_on":[],`+
						`"acceptance_criteria":["c"]}]}`,
				)
			},
			want: app.ErrPlanningAnalysisOutputInvalid,
		},
		{
			name: "依赖顺序非法",
			setup: func(agentPort *fakeAnalysisAgent) {
				agentPort.setText(
					domain.RoleTypePlanner,
					`{"summary":"s","tasks":[`+
						`{"id":"t1","title":"A","description":"a","depends_on":["t2"],`+
						`"acceptance_criteria":["c"]},`+
						`{"id":"t2","title":"B","description":"b","depends_on":[],`+
						`"acceptance_criteria":["c"]}]}`,
				)
			},
			want: app.ErrPlanningAnalysisOutputInvalid,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			agentPort := newFakeAnalysisAgent()
			f := newAnalysisFixture(t, analysisFixtureOptions{agent: agentPort})
			session := f.analyzingSession(t)
			testCase.setup(agentPort)

			_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
				PlanningSessionID: session.ID.String(),
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("错误类别不符: 期望 %v，实际 %v", testCase.want, err)
			}
			if calls := agentPort.callsFor(domain.RoleTypeRequirementAnalyst); calls != 1 {
				t.Fatalf("规划师阶段失败不应重跑分析师，实际调用 %d 次", calls)
			}
			if revisions := f.revisions(t, session.ID); len(revisions) != 0 {
				t.Fatalf("规划师阶段失败仍产生了版本: %d", len(revisions))
			}
			stored := f.loadSession(t, session.ID)
			if stored.Status != domain.PlanningSessionFailed {
				t.Fatalf("失败后会话状态应为 failed，实际 %s", stored.Status)
			}
			if !strings.Contains(stored.ErrorSummary, "方案规划阶段") {
				t.Fatalf("失败摘要没有记录阶段: %q", stored.ErrorSummary)
			}
		})
	}
}

// TestAnalyzeRejectsConcurrentRunOnSameSession 验证同一会话的并发分析只有一个能开始。
func TestAnalyzeRejectsConcurrentRunOnSameSession(t *testing.T) {
	t.Parallel()
	agentPort := newFakeAnalysisAgent()
	f := newAnalysisFixture(t, analysisFixtureOptions{agent: agentPort, holdAgent: true})
	session := f.analyzingSession(t)

	done := make(chan error, 1)
	go func() {
		_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
			PlanningSessionID: session.ID.String(),
		})
		done <- err
	}()
	agentPort.waitEntered(t, 1)

	_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	})
	if !errors.Is(err, app.ErrPlanningAnalysisConflict) {
		t.Fatalf("并发分析未被拒绝: %v", err)
	}
	agentPort.releaseHold()
	if err := <-done; err != nil {
		t.Fatalf("首个分析失败: %v", err)
	}
	if calls := agentPort.callsFor(domain.RoleTypeRequirementAnalyst); calls != 1 {
		t.Fatalf("并发请求启动了额外分析师调用: %d", calls)
	}
	if revisions := f.revisions(t, session.ID); len(revisions) != 1 {
		t.Fatalf("版本数量应为 1，实际 %d", len(revisions))
	}
}

// TestAnalyzeIsolatesDifferentSessions 验证不同会话可以并发分析。
func TestAnalyzeIsolatesDifferentSessions(t *testing.T) {
	t.Parallel()
	agentPort := newFakeAnalysisAgent()
	f := newAnalysisFixture(t, analysisFixtureOptions{agent: agentPort, holdAgent: true})
	first := f.analyzingSession(t)
	second := f.analyzingSession(t)

	done := make(chan error, 2)
	for _, session := range []domain.PlanningSession{first, second} {
		session := session
		go func() {
			_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
				PlanningSessionID: session.ID.String(),
			})
			done <- err
		}()
	}
	// 两个会话的分析都进入执行，说明占用按会话隔离，而不是全局限流。
	agentPort.waitEntered(t, 2)
	agentPort.releaseHold()
	for index := 0; index < 2; index++ {
		if err := <-done; err != nil {
			t.Fatalf("并发分析第 %d 个会话失败: %v", index+1, err)
		}
	}
	if revisions := f.revisions(t, first.ID); len(revisions) != 1 {
		t.Fatalf("会话一版本数量应为 1，实际 %d", len(revisions))
	}
	if revisions := f.revisions(t, second.ID); len(revisions) != 1 {
		t.Fatalf("会话二版本数量应为 1，实际 %d", len(revisions))
	}
}

// TestAnalyzeRetryAfterFailure 验证失败之后可以显式重试并成功。
func TestAnalyzeRetryAfterFailure(t *testing.T) {
	t.Parallel()
	agentPort := newFakeAnalysisAgent()
	f := newAnalysisFixture(t, analysisFixtureOptions{agent: agentPort})
	session := f.analyzingSession(t)
	agentPort.setError(domain.RoleTypeRequirementAnalyst, errors.New("容器执行失败"))
	if _, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	}); err == nil {
		t.Fatal("分析师失败却报告成功")
	}
	agentPort.setError(domain.RoleTypeRequirementAnalyst, nil)

	if _, err := f.sessions.RetryAnalysis(f.ctx, session.ID.String(), app.RetryPlanningAnalysisRequest{}); err != nil {
		t.Fatalf("显式重试失败: %v", err)
	}
	if _, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	}); err != nil {
		t.Fatalf("重试后分析失败: %v", err)
	}
	stored := f.loadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionPreviewReady {
		t.Fatalf("重试成功后状态应为 preview_ready，实际 %s", stored.Status)
	}
	if stored.ErrorSummary != "" {
		t.Fatalf("重试成功后失败摘要未清空: %q", stored.ErrorSummary)
	}
	revisions := f.revisions(t, session.ID)
	if len(revisions) != 1 {
		t.Fatalf("版本数量应为 1，实际 %d", len(revisions))
	}
}

// TestAnalyzeKeepsSessionAnalyzingWhenRevisionNotPersisted 验证版本写入失败不留半成品。
func TestAnalyzeKeepsSessionAnalyzingWhenRevisionNotPersisted(t *testing.T) {
	t.Parallel()
	agentPort := newFakeAnalysisAgent()
	base := newAnalysisFixture(t, analysisFixtureOptions{agent: agentPort})
	f := &analysisFixture{
		db:       base.db,
		ctx:      base.ctx,
		database: base.database,
		project:  base.project,
		sessions: base.sessions,
		agent:    agentPort,
	}
	analysis, err := app.NewPlanningAnalysisService(app.PlanningAnalysisServiceOptions{
		Store: failingRevisionStore{Store: base.db},
		Agent: agentPort,
	})
	if err != nil {
		t.Fatalf("构造规划分析服务失败: %v", err)
	}
	f.analysis = analysis
	session := f.analyzingSession(t)

	_, err = f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	})
	if !errors.Is(err, app.ErrPlanningAnalysisNotPersisted) {
		t.Fatalf("版本写入失败未被报告: %v", err)
	}
	stored := f.loadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("写入失败后状态不应变化，实际 %s", stored.Status)
	}
	if revisions := f.revisions(t, session.ID); len(revisions) != 0 {
		t.Fatalf("写入失败仍留下版本: %d", len(revisions))
	}
	if messages := f.messages(t, session.ID); len(messages) != 1 {
		t.Fatalf("写入失败仍留下角色消息: %d", len(messages))
	}
	// 占用必须已释放：再次抢占应当成功。
	claimID := domain.NewID().String()
	if err := f.db.ClaimPlanningAnalysis(f.ctx, session.ID, claimID, time.Now().UTC()); err != nil {
		t.Fatalf("写入失败后占用没有释放: %v", err)
	}
	if err := f.db.ReleasePlanningAnalysis(f.ctx, session.ID, claimID, time.Now().UTC()); err != nil {
		t.Fatalf("释放占用失败: %v", err)
	}
}

// TestAnalyzeResultSurvivesRestart 验证进程重启后会话与版本仍可读取并解析。
func TestAnalyzeResultSurvivesRestart(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session := f.analyzingSession(t)
	created, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	})
	if err != nil {
		t.Fatalf("执行两阶段分析失败: %v", err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	reopened, err := sqlite.Open(f.ctx, sqlite.Options{Path: f.database})
	if err != nil {
		t.Fatalf("重新打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reopened.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	sessions, err := app.NewPlanningSessionService(app.PlanningSessionServiceOptions{Store: reopened})
	if err != nil {
		t.Fatalf("构造规划会话服务失败: %v", err)
	}
	stored, err := sessions.Get(f.ctx, session.ID.String())
	if err != nil {
		t.Fatalf("重启后读取规划会话失败: %v", err)
	}
	if stored.Status != domain.PlanningSessionPreviewReady {
		t.Fatalf("重启后会话状态不符: %s", stored.Status)
	}
	revisions, err := sessions.Revisions(f.ctx, session.ID.String())
	if err != nil {
		t.Fatalf("重启后读取规划版本失败: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("重启后版本数量应为 1，实际 %d", len(revisions))
	}
	if revisions[0].ID != created.Revision.ID {
		t.Fatalf("重启后版本标识不一致: %s", revisions[0].ID)
	}
	if _, err := revisions[0].ImplementationPlan(); err != nil {
		t.Fatalf("重启后解析实施方案失败: %v", err)
	}
}

// TestAnalyzeCreatesNoFormalEntities 验证规划阶段不创建正式 Requirement / Task / Step。
func TestAnalyzeCreatesNoFormalEntities(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session := f.analyzingSession(t)
	if _, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	}); err != nil {
		t.Fatalf("执行两阶段分析失败: %v", err)
	}
	for _, table := range []string{"requirements", "tasks", "steps", "attempts", "sessions"} {
		if count := f.countRows(t, table); count != 0 {
			t.Fatalf("规划阶段写入了正式实体 %s: %d 行", table, count)
		}
	}
}

// TestAnalyzeLeavesProjectFilesUntouched 验证分析过程不修改项目工作区文件。
//
// 编排服务没有任何写工作区的能力：它只提交提示词并读取数据库。用例仍对一个真实目录做
// 前后快照，作为"不会顺带改动项目文件"的回归守点。
func TestAnalyzeLeavesProjectFilesUntouched(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "README.md")
	if err := os.WriteFile(marker, []byte("项目内容\n"), 0o600); err != nil {
		t.Fatalf("写入项目文件失败: %v", err)
	}
	before := snapshotDirectory(t, workspace)
	session := f.analyzingSession(t)
	if _, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	}); err != nil {
		t.Fatalf("执行两阶段分析失败: %v", err)
	}
	after := snapshotDirectory(t, workspace)
	if before != after {
		t.Fatalf("分析过程修改了项目文件:\n修改前: %s\n修改后: %s", before, after)
	}
}

// snapshotDirectory 返回目录项与文件内容的稳定快照。
func snapshotDirectory(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	var builder strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		builder.WriteString(name + ":" + entry.Type().String())
		if entry.IsDir() {
			builder.WriteString(";")
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取文件失败: %v", err)
		}
		builder.WriteString("=" + string(content) + ";")
	}
	return builder.String()
}

// TestAnalyzeWithUnknownSessionReportsNotFound 验证未知会话被报告为不存在。
func TestAnalyzeWithUnknownSessionReportsNotFound(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	_, err := f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: domain.NewID().String(),
	})
	if !errors.Is(err, app.ErrPlanningAnalysisNotFound) {
		t.Fatalf("未知会话的错误类别不符: %v", err)
	}
	if calls := f.agent.callsFor(domain.RoleTypeRequirementAnalyst); calls != 0 {
		t.Fatalf("未知会话发起了模型调用: %d", calls)
	}
}

// TestAnalyzeRequiresAnalyzingSession 验证未开始分析的会话不会被占用。
func TestAnalyzeRequiresAnalyzingSession(t *testing.T) {
	t.Parallel()
	f := newAnalysisFixture(t, analysisFixtureOptions{})
	session, err := f.sessions.Create(f.ctx, app.CreatePlanningSessionRequest{ProjectID: f.project.ID})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	_, err = f.analysis.Analyze(f.ctx, app.RunPlanningAnalysisRequest{
		PlanningSessionID: session.ID.String(),
	})
	if !errors.Is(err, app.ErrPlanningAnalysisConflict) {
		t.Fatalf("active 会话被占用的错误类别不符: %v", err)
	}
	if calls := f.agent.callsFor(domain.RoleTypeRequirementAnalyst); calls != 0 {
		t.Fatalf("active 会话发起了模型调用: %d", calls)
	}
}
