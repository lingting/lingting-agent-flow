package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 规划会话业务编排用例。
//
// 每个断言都重新从存储读取记录：事务边界、条件更新与回滚的实际效果只能由数据库
// 状态证明，不能用 App 方法返回的内存对象代替。

// planningProjectID 是规划会话用例使用的项目标识。
const planningProjectID = "planning-demo"

// planningFixture 是规划会话服务的测试环境。
type planningFixture struct {
	db       *sqlite.DB
	service  *app.PlanningSessionService
	projects *app.ProjectService
	ctx      context.Context
}

// newPlanningFixture 在测试工作空间中构造规划会话服务与真实 SQLite。
func newPlanningFixture(t *testing.T) *planningFixture {
	t.Helper()
	workspace := testkit.New(t)
	workspace.Report()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	service, err := app.NewPlanningSessionService(app.PlanningSessionServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造规划会话服务失败: %v", err)
	}
	projects, err := app.NewProjectService(app.ProjectServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造项目服务失败: %v", err)
	}
	return &planningFixture{db: db, service: service, projects: projects, ctx: ctx}
}

// ensureProject 创建（或复用）一个可用的测试项目。
func (f *planningFixture) ensureProject(t *testing.T) domain.Project {
	t.Helper()
	if project, err := f.projects.Get(f.ctx, planningProjectID); err == nil {
		return project
	} else if !errors.Is(err, app.ErrProjectNotFound) {
		t.Fatalf("读取项目失败: %v", err)
	}
	project, err := f.projects.Create(f.ctx, app.CreateProjectRequest{
		ID:            planningProjectID,
		Name:          "规划会话示例项目",
		RepositoryURL: "ssh://git.example.com/team/planning-repo",
		BaseBranch:    "main",
		Environment:   "go",
		Caches:        []string{"go-mod"},
	})
	if err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	return project
}

// createSession 创建一个关联测试项目的规划会话。
func (f *planningFixture) createSession(t *testing.T) domain.PlanningSession {
	t.Helper()
	f.ensureProject(t)
	session, err := f.service.Create(f.ctx, app.CreatePlanningSessionRequest{ProjectID: planningProjectID})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	return session
}

// loadSession 重新读取规划会话的持久化事实。
func (f *planningFixture) loadSession(t *testing.T, sessionID domain.ID) domain.PlanningSession {
	t.Helper()
	session, err := f.db.GetPlanningSession(f.ctx, sessionID)
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	return *session
}

// messages 返回会话已持久化的全部消息。
func (f *planningFixture) messages(t *testing.T, sessionID domain.ID) []domain.PlanningMessage {
	t.Helper()
	messages, err := f.db.ListPlanningMessages(f.ctx, sessionID)
	if err != nil {
		t.Fatalf("查询规划消息失败: %v", err)
	}
	return messages
}

// revisions 返回会话已持久化的全部方案版本。
func (f *planningFixture) revisions(t *testing.T, sessionID domain.ID) []domain.PlanningRevision {
	t.Helper()
	revisions, err := f.db.ListPlanningRevisions(f.ctx, sessionID)
	if err != nil {
		t.Fatalf("查询规划版本失败: %v", err)
	}
	return revisions
}

// submit 提交一条用户消息，失败时终止用例。
func (f *planningFixture) submit(
	t *testing.T,
	sessionID domain.ID,
	content string,
) app.PlanningAnalysisStart {
	t.Helper()
	start, err := f.service.SubmitMessage(f.ctx, sessionID.String(), app.SubmitPlanningMessageRequest{
		Content: content,
	})
	if err != nil {
		t.Fatalf("提交规划消息失败: %v", err)
	}
	return start
}

// completeRequest 返回一份合法的分析完成请求。
func completeRequest() app.CompletePlanningAnalysisRequest {
	return app.CompletePlanningAnalysisRequest{
		RequirementAnalysisJSON: json.RawMessage(`{"goal":"补齐规划会话测试"}`),
		ImplementationPlanJSON:  json.RawMessage(`{"steps":["实现","验证"]}`),
		Messages: []app.PlanningRoleMessage{
			{SenderType: domain.PlanningSenderRequirementAnalyst, Content: "需求分析师：目标已明确"},
			{SenderType: domain.PlanningSenderPlanner, Content: "方案规划师：分两步实施"},
		},
	}
}

// toPreviewReady 把会话推进到 preview_ready 并返回本轮生成的方案版本。
func (f *planningFixture) toPreviewReady(
	t *testing.T,
	sessionID domain.ID,
) domain.PlanningRevision {
	t.Helper()
	f.submit(t, sessionID, "帮我规划一次需求分析")
	result, err := f.service.CompleteAnalysis(f.ctx, sessionID.String(), completeRequest())
	if err != nil {
		t.Fatalf("完成分析失败: %v", err)
	}
	return result.Revision
}

// toAnalyzing 把会话推进到 analyzing。
func (f *planningFixture) toAnalyzing(t *testing.T, sessionID domain.ID) {
	t.Helper()
	f.submit(t, sessionID, "开始一轮分析")
}

// failAnalyzing 把运行中的分析收敛为失败。
func (f *planningFixture) failAnalyzing(t *testing.T, sessionID domain.ID) {
	t.Helper()
	if _, err := f.service.FailAnalysis(f.ctx, sessionID.String(), app.FailPlanningAnalysisRequest{
		Summary: "模型不可用",
	}); err != nil {
		t.Fatalf("记录分析失败失败: %v", err)
	}
}

// toFailed 把 active 会话推进到 failed。
func (f *planningFixture) toFailed(t *testing.T, sessionID domain.ID) {
	t.Helper()
	f.toAnalyzing(t, sessionID)
	f.failAnalyzing(t, sessionID)
}

// complete 完成一轮分析并返回本轮生成的方案版本。
func (f *planningFixture) complete(t *testing.T, sessionID domain.ID) domain.PlanningRevision {
	t.Helper()
	result, err := f.service.CompleteAnalysis(f.ctx, sessionID.String(), completeRequest())
	if err != nil {
		t.Fatalf("完成分析失败: %v", err)
	}
	return result.Revision
}

// TestPlanningSessionServiceCreateAndQuery 校验创建、项目引用与查询返回真实持久化记录。
func TestPlanningSessionServiceCreateAndQuery(t *testing.T) {
	t.Parallel()
	fixture := newPlanningFixture(t)

	// 只允许关联真实且未删除的项目。
	if _, err := fixture.service.Create(
		fixture.ctx,
		app.CreatePlanningSessionRequest{ProjectID: "missing-project"},
	); !errors.Is(err, app.ErrPlanningSessionNotFound) {
		t.Fatalf("不存在的项目应返回 ErrPlanningSessionNotFound，实际 %v", err)
	}
	project := fixture.ensureProject(t)
	created, err := fixture.service.Create(
		fixture.ctx,
		app.CreatePlanningSessionRequest{ProjectID: project.ID},
	)
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	if created.Status != domain.PlanningSessionActive {
		t.Fatalf("初始状态应为 active，实际 %s", created.Status)
	}
	if created.ProjectID != project.ID {
		t.Fatalf("所属项目不符: %s", created.ProjectID)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("时间应由服务端生成: %+v", created)
	}
	if created.HostID != nil || created.ContainerID != "" || created.SourceCommit != "" {
		t.Fatalf("新建会话不应带有运行时绑定: %+v", created)
	}
	if created.AnalystSessionID != "" || created.PlannerSessionID != "" {
		t.Fatalf("新建会话不应带有角色会话标识: %+v", created)
	}
	if created.AdoptedRevisionID != nil || created.FinishedAt != nil {
		t.Fatalf("新建会话不应有采纳版本或结束时间: %+v", created)
	}

	loaded, err := fixture.service.Get(fixture.ctx, created.ID.String())
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	if loaded.ID != created.ID || loaded.Status != domain.PlanningSessionActive {
		t.Fatalf("读取结果不符: %+v", loaded)
	}
	if _, err := fixture.service.Get(fixture.ctx, domain.NewID().String()); !errors.Is(
		err,
		app.ErrPlanningSessionNotFound,
	) {
		t.Fatalf("不存在的会话应返回 ErrPlanningSessionNotFound，实际 %v", err)
	}

	sessions, err := fixture.service.ListByProject(fixture.ctx, project.ID)
	if err != nil {
		t.Fatalf("按项目列出会话失败: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("项目会话列表不符: %+v", sessions)
	}

	// 消息与版本查询返回真实持久化记录，而不是空列表。
	fixture.toPreviewReady(t, created.ID)
	messages, err := fixture.service.Messages(fixture.ctx, created.ID.String())
	if err != nil {
		t.Fatalf("查询规划消息失败: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("应返回 1 条用户消息与 2 条角色消息，实际 %d", len(messages))
	}
	for index, message := range messages {
		if message.Sequence != index+1 {
			t.Fatalf("消息序号应为 %d，实际 %d", index+1, message.Sequence)
		}
	}
	revisions, err := fixture.service.Revisions(fixture.ctx, created.ID.String())
	if err != nil {
		t.Fatalf("查询规划版本失败: %v", err)
	}
	if len(revisions) != 1 || revisions[0].RevisionNumber != 1 {
		t.Fatalf("应返回 1 个版本，实际 %+v", revisions)
	}
	if _, err := fixture.service.Messages(fixture.ctx, domain.NewID().String()); !errors.Is(
		err,
		app.ErrPlanningSessionNotFound,
	) {
		t.Fatalf("不存在的会话查询消息应返回 ErrPlanningSessionNotFound，实际 %v", err)
	}

	// 项目被逻辑删除后不再允许从该项目入口继续规划。
	if err := fixture.projects.Delete(fixture.ctx, project.ID); err != nil {
		t.Fatalf("删除项目失败: %v", err)
	}
	if _, err := fixture.service.Create(
		fixture.ctx,
		app.CreatePlanningSessionRequest{ProjectID: project.ID},
	); !errors.Is(err, app.ErrPlanningSessionNotFound) {
		t.Fatalf("已删除项目应返回 ErrPlanningSessionNotFound，实际 %v", err)
	}
	if _, err := fixture.service.ListByProject(fixture.ctx, project.ID); !errors.Is(
		err,
		app.ErrPlanningSessionNotFound,
	) {
		t.Fatalf("已删除项目列出会话应返回 ErrPlanningSessionNotFound，实际 %v", err)
	}
}

// TestPlanningSessionServiceSubmitMessage 校验提交消息的准入、事务与并发互斥。
func TestPlanningSessionServiceSubmitMessage(t *testing.T) {
	t.Parallel()

	t.Run("active 会话进入 analyzing", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		start := fixture.submit(t, session.ID, "帮我规划一次需求分析")
		if start.Message == nil {
			t.Fatal("提交消息应返回已保存的用户消息")
		}
		persisted := fixture.loadSession(t, session.ID)
		if persisted.Status != domain.PlanningSessionAnalyzing {
			t.Fatalf("提交消息后应进入 analyzing，实际 %s", persisted.Status)
		}
		messages := fixture.messages(t, session.ID)
		if len(messages) != 1 {
			t.Fatalf("应只有 1 条用户消息，实际 %d", len(messages))
		}
		if messages[0].SenderType != domain.PlanningSenderUser {
			t.Fatalf("发送方应为 user，实际 %s", messages[0].SenderType)
		}
		if messages[0].Content != "帮我规划一次需求分析" {
			t.Fatalf("消息正文不符: %q", messages[0].Content)
		}
		if messages[0].Sequence != 1 {
			t.Fatalf("首条消息序号应为 1，实际 %d", messages[0].Sequence)
		}
	})

	t.Run("preview_ready 允许继续提交", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		fixture.toPreviewReady(t, session.ID)
		before := len(fixture.messages(t, session.ID))
		fixture.submit(t, session.ID, "再补充一条约束")
		persisted := fixture.loadSession(t, session.ID)
		if persisted.Status != domain.PlanningSessionAnalyzing {
			t.Fatalf("继续提交后应回到 analyzing，实际 %s", persisted.Status)
		}
		messages := fixture.messages(t, session.ID)
		if len(messages) != before+1 {
			t.Fatalf("应新增 1 条消息，实际 %d", len(messages))
		}
		if messages[len(messages)-1].Content != "再补充一条约束" {
			t.Fatalf("新增消息正文不符: %q", messages[len(messages)-1].Content)
		}
	})

	t.Run("不可提交状态被拒绝", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)

		analyzing := fixture.createSession(t)
		fixture.toAnalyzing(t, analyzing.ID)
		messagesBefore := len(fixture.messages(t, analyzing.ID))
		if _, err := fixture.service.SubmitMessage(
			fixture.ctx,
			analyzing.ID.String(),
			app.SubmitPlanningMessageRequest{Content: "运行中追加"},
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("analyzing 会话应返回 ErrPlanningSessionStateConflict，实际 %v", err)
		}
		if got := len(fixture.messages(t, analyzing.ID)); got != messagesBefore {
			t.Fatalf("被拒绝的提交不得留下消息，期望 %d 条，实际 %d", messagesBefore, got)
		}

		failed := fixture.createSession(t)
		fixture.toFailed(t, failed.ID)
		if _, err := fixture.service.SubmitMessage(
			fixture.ctx,
			failed.ID.String(),
			app.SubmitPlanningMessageRequest{Content: "失败后追加"},
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("failed 会话应返回 ErrPlanningSessionStateConflict，实际 %v", err)
		}
		if persisted := fixture.loadSession(t, failed.ID); persisted.Status != domain.PlanningSessionFailed {
			t.Fatalf("被拒绝的提交不得改变状态，实际 %s", persisted.Status)
		}

		confirmed := fixture.createSession(t)
		revision := fixture.toPreviewReady(t, confirmed.ID)
		if _, err := fixture.service.Confirm(
			fixture.ctx,
			confirmed.ID.String(),
			revision.ID.String(),
		); err != nil {
			t.Fatalf("确认方案失败: %v", err)
		}
		confirmedMessages := len(fixture.messages(t, confirmed.ID))
		if _, err := fixture.service.SubmitMessage(
			fixture.ctx,
			confirmed.ID.String(),
			app.SubmitPlanningMessageRequest{Content: "确认后追加"},
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("confirmed 会话应返回 ErrPlanningSessionStateConflict，实际 %v", err)
		}
		if got := len(fixture.messages(t, confirmed.ID)); got != confirmedMessages {
			t.Fatalf("被拒绝的提交不得留下消息，期望 %d 条，实际 %d", confirmedMessages, got)
		}

		abandoned := fixture.createSession(t)
		if _, err := fixture.service.Abandon(fixture.ctx, abandoned.ID.String()); err != nil {
			t.Fatalf("放弃会话失败: %v", err)
		}
		if _, err := fixture.service.SubmitMessage(
			fixture.ctx,
			abandoned.ID.String(),
			app.SubmitPlanningMessageRequest{Content: "放弃后追加"},
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("abandoned 会话应返回 ErrPlanningSessionStateConflict，实际 %v", err)
		}
		if len(fixture.messages(t, abandoned.ID)) != 0 {
			t.Fatal("放弃的会话不应出现新消息")
		}
	})

	t.Run("并发提交只有一个成功", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		results := make(chan error, 2)
		var group sync.WaitGroup
		for index := 0; index < 2; index++ {
			content := "并发提交一"
			if index == 1 {
				content = "并发提交二"
			}
			group.Add(1)
			go func() {
				defer group.Done()
				_, err := fixture.service.SubmitMessage(
					fixture.ctx,
					session.ID.String(),
					app.SubmitPlanningMessageRequest{Content: content},
				)
				results <- err
			}()
		}
		group.Wait()
		close(results)
		succeeded := 0
		conflicted := 0
		for err := range results {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, app.ErrPlanningSessionStateConflict):
				conflicted++
			default:
				t.Fatalf("并发提交出现意外错误: %v", err)
			}
		}
		if succeeded != 1 || conflicted != 1 {
			t.Fatalf("并发提交应恰好一成功一冲突，实际成功 %d、冲突 %d", succeeded, conflicted)
		}
		persisted := fixture.loadSession(t, session.ID)
		if persisted.Status != domain.PlanningSessionAnalyzing {
			t.Fatalf("并发提交后应处于 analyzing，实际 %s", persisted.Status)
		}
		messages := fixture.messages(t, session.ID)
		if len(messages) != 1 {
			t.Fatalf("失败请求不得留下消息，期望 1 条，实际 %d", len(messages))
		}
	})
}

// TestPlanningSessionServiceCompleteAnalysis 校验完成分析的事务、角色消息与版本分配。
func TestPlanningSessionServiceCompleteAnalysis(t *testing.T) {
	t.Parallel()

	t.Run("成功完成并追加版本", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		fixture.toAnalyzing(t, session.ID)
		result, err := fixture.service.CompleteAnalysis(
			fixture.ctx,
			session.ID.String(),
			completeRequest(),
		)
		if err != nil {
			t.Fatalf("完成分析失败: %v", err)
		}
		if result.Revision.RevisionNumber != 1 {
			t.Fatalf("首个版本号应为 1，实际 %d", result.Revision.RevisionNumber)
		}
		persisted := fixture.loadSession(t, session.ID)
		if persisted.Status != domain.PlanningSessionPreviewReady {
			t.Fatalf("完成后应处于 preview_ready，实际 %s", persisted.Status)
		}
		revisions := fixture.revisions(t, session.ID)
		if len(revisions) != 1 {
			t.Fatalf("应追加 1 个版本，实际 %d", len(revisions))
		}
		messages := fixture.messages(t, session.ID)
		if len(messages) != 3 {
			t.Fatalf("应追加 2 条角色消息，实际共 %d 条", len(messages))
		}
		senders := []domain.PlanningSenderType{
			messages[1].SenderType,
			messages[2].SenderType,
		}
		if senders[0] != domain.PlanningSenderRequirementAnalyst ||
			senders[1] != domain.PlanningSenderPlanner {
			t.Fatalf("角色消息发送方不符: %v", senders)
		}

		// 第二轮：preview_ready → analyzing → preview_ready，版本号由存储层递增。
		fixture.submit(t, session.ID, "继续第二轮")
		second, err := fixture.service.CompleteAnalysis(
			fixture.ctx,
			session.ID.String(),
			completeRequest(),
		)
		if err != nil {
			t.Fatalf("第二轮完成分析失败: %v", err)
		}
		if second.Revision.RevisionNumber != 2 {
			t.Fatalf("第二个版本号应为 2，实际 %d", second.Revision.RevisionNumber)
		}
		if revisions := fixture.revisions(t, session.ID); len(revisions) != 2 {
			t.Fatalf("历史版本不得被覆盖，实际 %d", len(revisions))
		}
	})

	t.Run("非规划角色被拒绝", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		fixture.toAnalyzing(t, session.ID)
		request := completeRequest()
		request.Messages = []app.PlanningRoleMessage{
			{SenderType: domain.PlanningSenderUser, Content: "伪造的用户消息"},
		}
		if _, err := fixture.service.CompleteAnalysis(
			fixture.ctx,
			session.ID.String(),
			request,
		); !errors.Is(err, app.ErrPlanningSessionInvalidInput) {
			t.Fatalf("非规划角色应返回 ErrPlanningSessionInvalidInput，实际 %v", err)
		}
		if len(fixture.revisions(t, session.ID)) != 0 {
			t.Fatal("被拒绝的完成请求不得留下版本")
		}
		if persisted := fixture.loadSession(t, session.ID); persisted.Status != domain.PlanningSessionAnalyzing {
			t.Fatalf("被拒绝的完成请求不得改变状态，实际 %s", persisted.Status)
		}
	})

	t.Run("结构化 JSON 无效不回滚成功状态", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		fixture.toAnalyzing(t, session.ID)
		request := completeRequest()
		request.ImplementationPlanJSON = json.RawMessage(`{"steps":`)
		if _, err := fixture.service.CompleteAnalysis(
			fixture.ctx,
			session.ID.String(),
			request,
		); !errors.Is(err, app.ErrPlanningSessionInvalidInput) {
			t.Fatalf("非法 JSON 应返回 ErrPlanningSessionInvalidInput，实际 %v", err)
		}
		if len(fixture.revisions(t, session.ID)) != 0 {
			t.Fatal("非法 JSON 不得留下版本")
		}
		messages := fixture.messages(t, session.ID)
		if len(messages) != 1 {
			t.Fatalf("非法 JSON 不得留下角色消息，实际 %d 条", len(messages))
		}
		if persisted := fixture.loadSession(t, session.ID); persisted.Status != domain.PlanningSessionAnalyzing {
			t.Fatalf("非法 JSON 不得改写状态，实际 %s", persisted.Status)
		}
	})

	t.Run("非 analyzing 状态被拒绝", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		if _, err := fixture.service.CompleteAnalysis(
			fixture.ctx,
			session.ID.String(),
			completeRequest(),
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("active 会话完成分析应返回 ErrPlanningSessionStateConflict，实际 %v", err)
		}
		if len(fixture.revisions(t, session.ID)) != 0 {
			t.Fatal("被拒绝的完成请求不得留下版本")
		}
	})
}

// TestPlanningSessionServiceFailAndRetry 校验失败收敛、脱敏裁剪与显式重试。
func TestPlanningSessionServiceFailAndRetry(t *testing.T) {
	t.Parallel()

	t.Run("失败只允许 analyzing", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		if _, err := fixture.service.FailAnalysis(
			fixture.ctx,
			session.ID.String(),
			app.FailPlanningAnalysisRequest{Summary: "不该成功"},
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("active 会话失败应收敛为冲突，实际 %v", err)
		}
		if persisted := fixture.loadSession(t, session.ID); persisted.Status != domain.PlanningSessionActive {
			t.Fatalf("被拒绝的失败不得改变状态，实际 %s", persisted.Status)
		}
	})

	t.Run("失败摘要脱敏裁剪且不写成功产物", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		fixture.toAnalyzing(t, session.ID)
		if _, err := fixture.service.FailAnalysis(
			fixture.ctx,
			session.ID.String(),
			app.FailPlanningAnalysisRequest{Summary: "分析失败 token=abc123secret"},
		); err != nil {
			t.Fatalf("记录分析失败失败: %v", err)
		}
		persisted := fixture.loadSession(t, session.ID)
		if persisted.Status != domain.PlanningSessionFailed {
			t.Fatalf("失败后应处于 failed，实际 %s", persisted.Status)
		}
		if strings.Contains(persisted.ErrorSummary, "abc123secret") {
			t.Fatalf("失败摘要未脱敏: %q", persisted.ErrorSummary)
		}
		if !strings.Contains(persisted.ErrorSummary, "***已隐藏***") {
			t.Fatalf("失败摘要应包含脱敏占位符: %q", persisted.ErrorSummary)
		}
		if persisted.FinishedAt == nil {
			t.Fatal("失败会话应记录结束时间")
		}
		if len(fixture.revisions(t, session.ID)) != 0 {
			t.Fatal("失败不得写入方案版本")
		}
		if messages := fixture.messages(t, session.ID); len(messages) != 1 {
			t.Fatalf("失败不得写入角色成功消息，实际 %d 条", len(messages))
		}

		// 超长摘要按既有上限裁剪，并带明确截断标记。
		clipped := newPlanningFixture(t)
		longSession := clipped.createSession(t)
		clipped.toAnalyzing(t, longSession.ID)
		if _, err := clipped.service.FailAnalysis(
			clipped.ctx,
			longSession.ID.String(),
			app.FailPlanningAnalysisRequest{Summary: strings.Repeat("失败原因", 400)},
		); err != nil {
			t.Fatalf("记录超长失败摘要失败: %v", err)
		}
		clippedSummary := clipped.loadSession(t, longSession.ID).ErrorSummary
		if !strings.HasSuffix(clippedSummary, "…（已截断）") {
			t.Fatalf("超长失败摘要应被裁剪: %q", clippedSummary)
		}
		if len(clippedSummary) > domain.MaxFeedbackReason+len("…（已截断）") {
			t.Fatalf("裁剪后的失败摘要超出上限: %d", len(clippedSummary))
		}
	})

	t.Run("显式重试带补充消息", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		fixture.toFailed(t, session.ID)
		before := len(fixture.messages(t, session.ID))

		// 普通入口不得隐式重置失败结论。
		if _, err := fixture.service.SubmitMessage(
			fixture.ctx,
			session.ID.String(),
			app.SubmitPlanningMessageRequest{Content: "隐式重试"},
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("failed 会话普通提交应返回冲突，实际 %v", err)
		}
		if got := len(fixture.messages(t, session.ID)); got != before {
			t.Fatalf("被拒绝的提交不得留下消息，实际 %d", got)
		}

		start, err := fixture.service.RetryAnalysis(
			fixture.ctx,
			session.ID.String(),
			app.RetryPlanningAnalysisRequest{Content: "补充：改用另一种方案"},
		)
		if err != nil {
			t.Fatalf("显式重试失败: %v", err)
		}
		if start.Message == nil {
			t.Fatal("带补充消息的重试应返回已保存的消息")
		}
		persisted := fixture.loadSession(t, session.ID)
		if persisted.Status != domain.PlanningSessionAnalyzing {
			t.Fatalf("重试后应回到 analyzing，实际 %s", persisted.Status)
		}
		if persisted.ErrorSummary != "" {
			t.Fatalf("重试应清空失败摘要，实际 %q", persisted.ErrorSummary)
		}
		if persisted.FinishedAt != nil {
			t.Fatalf("重试后不应保留结束时间，实际 %v", persisted.FinishedAt)
		}
		messages := fixture.messages(t, session.ID)
		if len(messages) != before+1 {
			t.Fatalf("补充消息应与状态迁移一同落库，期望 %d 条，实际 %d", before+1, len(messages))
		}
		if messages[len(messages)-1].Content != "补充：改用另一种方案" {
			t.Fatalf("补充消息正文不符: %q", messages[len(messages)-1].Content)
		}

		// 不带补充消息的重试不追加消息。
		fixture.failAnalyzing(t, session.ID)
		beforeEmpty := len(fixture.messages(t, session.ID))
		if _, err := fixture.service.RetryAnalysis(
			fixture.ctx,
			session.ID.String(),
			app.RetryPlanningAnalysisRequest{},
		); err != nil {
			t.Fatalf("无补充消息的重试失败: %v", err)
		}
		if got := len(fixture.messages(t, session.ID)); got != beforeEmpty {
			t.Fatalf("无补充消息的重试不应追加消息，实际 %d", got)
		}
	})

	t.Run("非失败状态不能重试", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		if _, err := fixture.service.RetryAnalysis(
			fixture.ctx,
			session.ID.String(),
			app.RetryPlanningAnalysisRequest{Content: "直接重试"},
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("active 会话重试应返回冲突，实际 %v", err)
		}
		if persisted := fixture.loadSession(t, session.ID); persisted.Status != domain.PlanningSessionActive {
			t.Fatalf("被拒绝的重试不得改变状态，实际 %s", persisted.Status)
		}
		if len(fixture.messages(t, session.ID)) != 0 {
			t.Fatal("被拒绝的重试不得留下消息")
		}
	})
}

// TestPlanningSessionServiceConfirmAndAbandon 校验确认归属、采纳版本与放弃的持久化事实。
func TestPlanningSessionServiceConfirmAndAbandon(t *testing.T) {
	t.Parallel()

	t.Run("只能确认属于本会话的版本", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		other := fixture.createSession(t)
		fixture.toPreviewReady(t, session.ID)
		otherRevision := fixture.toPreviewReady(t, other.ID)
		if _, err := fixture.service.Confirm(
			fixture.ctx,
			session.ID.String(),
			otherRevision.ID.String(),
		); !errors.Is(err, app.ErrPlanningSessionRevisionNotFound) {
			t.Fatalf("跨会话版本应返回 ErrPlanningSessionRevisionNotFound，实际 %v", err)
		}
		persisted := fixture.loadSession(t, session.ID)
		if persisted.Status != domain.PlanningSessionPreviewReady {
			t.Fatalf("被拒绝的确认不得改变状态，实际 %s", persisted.Status)
		}
		if persisted.AdoptedRevisionID != nil {
			t.Fatalf("被拒绝的确认不得写入采纳版本，实际 %v", persisted.AdoptedRevisionID)
		}
	})

	t.Run("确认写入采纳版本并拒绝重复确认", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		revision := fixture.toPreviewReady(t, session.ID)
		// 第二轮产生更新的版本：确认之后再次确认必须被拒绝。
		secondRevision := fixture.toPreviewReady(t, session.ID)
		if _, err := fixture.service.Confirm(
			fixture.ctx,
			session.ID.String(),
			revision.ID.String(),
		); err != nil {
			t.Fatalf("确认方案失败: %v", err)
		}
		persisted := fixture.loadSession(t, session.ID)
		if persisted.Status != domain.PlanningSessionConfirmed {
			t.Fatalf("确认后应处于 confirmed，实际 %s", persisted.Status)
		}
		// 同一会话的更新版本也不能改写已采纳的方案。
		if _, err := fixture.service.Confirm(
			fixture.ctx,
			session.ID.String(),
			secondRevision.ID.String(),
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("重复确认应返回冲突，实际 %v", err)
		}
		reconfirmed := fixture.loadSession(t, session.ID)
		if reconfirmed.AdoptedRevisionID == nil ||
			*reconfirmed.AdoptedRevisionID != revision.ID {
			t.Fatalf("重复确认不得改写采纳版本，实际 %v", reconfirmed.AdoptedRevisionID)
		}
		if persisted.AdoptedRevisionID == nil || *persisted.AdoptedRevisionID != revision.ID {
			t.Fatalf("采纳版本未落库: %v", persisted.AdoptedRevisionID)
		}
		if persisted.FinishedAt == nil {
			t.Fatal("确认后应记录结束时间")
		}

		// 跨会话的版本不能作为本会话的采纳目标，错误类别必须能区分。
		other := fixture.createSession(t)
		otherRevision := fixture.toPreviewReady(t, other.ID)
		if _, err := fixture.service.Confirm(
			fixture.ctx,
			session.ID.String(),
			otherRevision.ID.String(),
		); !errors.Is(err, app.ErrPlanningSessionRevisionNotFound) {
			t.Fatalf("跨会话版本应返回 ErrPlanningSessionRevisionNotFound，实际 %v", err)
		}
		reloaded := fixture.loadSession(t, session.ID)
		if reloaded.AdoptedRevisionID == nil || *reloaded.AdoptedRevisionID != revision.ID {
			t.Fatalf("被拒绝的确认不得改写采纳版本，实际 %v", reloaded.AdoptedRevisionID)
		}
	})

	t.Run("放弃遵守状态机并保留历史", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		fixture.toAnalyzing(t, session.ID)
		if _, err := fixture.service.Abandon(
			fixture.ctx,
			session.ID.String(),
		); !errors.Is(err, app.ErrPlanningSessionStateConflict) {
			t.Fatalf("analyzing 会话放弃应返回冲突，实际 %v", err)
		}
		if persisted := fixture.loadSession(t, session.ID); persisted.Status != domain.PlanningSessionAnalyzing {
			t.Fatalf("被拒绝的放弃不得改变状态，实际 %s", persisted.Status)
		}

		// 完成本轮分析后追加第二轮：消息、版本与运行时绑定都必须保留。
		fixture.complete(t, session.ID)
		fixture.submit(t, session.ID, "补充第二轮输入")
		fixture.complete(t, session.ID)
		bound := fixture.loadSession(t, session.ID)
		hostID := domain.NewID()
		commit := strings.Repeat("a", 40)
		if err := fixture.db.UpdatePlanningSessionRuntimeBinding(
			fixture.ctx,
			session.ID,
			storage.PlanningSessionRuntimeBindingOf(bound),
			storage.PlanningSessionRuntimeBinding{
				HostID:       &hostID,
				ContainerID:  "container-planning-1",
				SourceCommit: commit,
			},
			time.Now().UTC(),
		); err != nil {
			t.Fatalf("绑定规划运行时失败: %v", err)
		}
		messagesBefore := len(fixture.messages(t, session.ID))
		revisionsBefore := len(fixture.revisions(t, session.ID))
		if _, err := fixture.service.Abandon(fixture.ctx, session.ID.String()); err != nil {
			t.Fatalf("放弃 preview_ready 会话失败: %v", err)
		}
		persisted := fixture.loadSession(t, session.ID)
		if persisted.Status != domain.PlanningSessionAbandoned {
			t.Fatalf("放弃后应处于 abandoned，实际 %s", persisted.Status)
		}
		if persisted.FinishedAt == nil {
			t.Fatal("放弃会话应记录结束时间")
		}
		if got := len(fixture.messages(t, session.ID)); got != messagesBefore {
			t.Fatalf("放弃不得删除消息，期望 %d 条，实际 %d", messagesBefore, got)
		}
		if got := len(fixture.revisions(t, session.ID)); got != revisionsBefore {
			t.Fatalf("放弃不得删除版本，期望 %d 个，实际 %d", revisionsBefore, got)
		}
		if persisted.ContainerID != "container-planning-1" || persisted.SourceCommit != commit {
			t.Fatalf("放弃不得清空运行时绑定: %+v", persisted)
		}
		if persisted.AdoptedRevisionID != nil {
			t.Fatalf("放弃不应记录采纳版本，实际 %v", persisted.AdoptedRevisionID)
		}
	})

	t.Run("确认规划不产生正式需求与任务", func(t *testing.T) {
		t.Parallel()
		fixture := newPlanningFixture(t)
		session := fixture.createSession(t)
		revision := fixture.toPreviewReady(t, session.ID)
		if _, err := fixture.service.Confirm(
			fixture.ctx,
			session.ID.String(),
			revision.ID.String(),
		); err != nil {
			t.Fatalf("确认方案失败: %v", err)
		}
		requirements, err := fixture.db.ListRequirements(
			fixture.ctx,
			storage.RequirementFilter{},
		)
		if err != nil {
			t.Fatalf("查询需求失败: %v", err)
		}
		if len(requirements) != 0 {
			t.Fatalf("确认规划不得创建正式需求，实际 %d 条", len(requirements))
		}
		tasks, err := fixture.db.ListTasks(fixture.ctx, storage.TaskFilter{})
		if err != nil {
			t.Fatalf("查询任务失败: %v", err)
		}
		if len(tasks) != 0 {
			t.Fatalf("确认规划不得创建任务，实际 %d 条", len(tasks))
		}
	})
}
