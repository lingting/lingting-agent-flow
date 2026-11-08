package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// 方案确认的应用编排用例。
//
// 每个断言都重新从存储读取记录：事务边界、条件更新与回滚的实际效果只能由数据库状态
// 证明，不能用服务方法返回的内存对象代替。

// confirmProjectID 是确认用例使用的项目标识。
const confirmProjectID = "confirm-demo"

// confirmAnalysisJSON 返回一份通过领域校验的需求分析快照。
func confirmAnalysisJSON(summary string) json.RawMessage {
	return json.RawMessage(`{"summary":"` + summary + `",` +
		`"goals":["生成正式需求与任务"],` +
		`"current_state":["已有规划会话与方案版本"],` +
		`"scope":["internal/app"],` +
		`"out_of_scope":["不做调度"],` +
		`"constraints":["不新增外键"],` +
		`"risks":["事务边界容易写错"],` +
		`"acceptance_criteria":["失败不留半成品"]}`)
}

// confirmPlanJSON 返回一份包含 count 个任务的实施方案快照。
func confirmPlanJSON(count int) json.RawMessage {
	tasks := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		dependencies := "[]"
		if index > 1 {
			dependencies = fmt.Sprintf(`["task-%d"]`, index-1)
		}
		tasks = append(tasks, fmt.Sprintf(
			`{"id":"task-%d","title":"任务 %d","description":"实现任务 %d",`+
				`"depends_on":%s,"acceptance_criteria":["可以独立验收"]}`,
			index, index, index, dependencies,
		))
	}
	return json.RawMessage(`{"summary":"按阶段推进","tasks":[` + strings.Join(tasks, ",") + `]}`)
}

// confirmFixture 是方案确认用例的测试环境。
type confirmFixture struct {
	db               *sqlite.DB
	ctx              context.Context
	service          *app.RequirementService
	sessions         *app.PlanningSessionService
	projects         *app.ProjectService
	project          domain.Project
	host             domain.ExecutionHost
	requirementStore storage.Store
}

// newConfirmFixture 打开测试数据库、补齐固定角色并装配需求与规划会话服务。
func newConfirmFixture(t *testing.T) *confirmFixture {
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
	if _, err := db.SeedRoles(ctx, app.RoleSeeds()); err != nil {
		t.Fatalf("补齐固定角色失败: %v", err)
	}
	host, err := domain.NewExecutionHost(
		"confirm-host",
		"确认测试节点",
		"10.0.0.1",
		22,
		"agent",
		"/srv/agentflow",
		time.Now().UTC(),
		domain.WithPrivateKey(testkit.PrivateKeyPath(t)),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	projects, err := app.NewProjectService(app.ProjectServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造项目服务失败: %v", err)
	}
	sessions, err := app.NewPlanningSessionService(app.PlanningSessionServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造规划会话服务失败: %v", err)
	}
	service, err := app.NewRequirementService(app.RequirementServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造需求服务失败: %v", err)
	}
	return &confirmFixture{
		ctx:              ctx,
		db:               db,
		host:             host,
		projects:         projects,
		requirementStore: db,
		service:          service,
		sessions:         sessions,
	}
}

// ensureProject 创建（或复用）一个带默认执行节点的测试项目。
func (f *confirmFixture) ensureProject(t *testing.T) domain.Project {
	t.Helper()
	if f.project.ID != "" {
		return f.project
	}
	project, err := f.projects.Create(f.ctx, app.CreateProjectRequest{
		BaseBranch:    "main",
		Caches:        []string{"go-mod"},
		DefaultHostID: f.host.ID.String(),
		Environment:   "go",
		ID:            confirmProjectID,
		Name:          "确认示例项目",
		RepositoryURL: "ssh://git.example.com/team/confirm-repo",
	})
	if err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	f.project = project
	return project
}

// newSession 创建一个关联测试项目的规划会话。
func (f *confirmFixture) newSession(t *testing.T) domain.PlanningSession {
	t.Helper()
	project := f.ensureProject(t)
	session, err := f.sessions.Create(f.ctx, app.CreatePlanningSessionRequest{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	return session
}

// toPreviewReady 把会话推进到 preview_ready 并返回本轮生成的方案版本。
func (f *confirmFixture) toPreviewReady(
	t *testing.T,
	sessionID domain.ID,
	analysis, plan json.RawMessage,
) domain.PlanningRevision {
	t.Helper()
	if _, err := f.sessions.SubmitMessage(f.ctx, sessionID.String(), app.SubmitPlanningMessageRequest{
		Content: "帮我确认这版方案",
	}); err != nil {
		t.Fatalf("提交规划消息失败: %v", err)
	}
	result, err := f.sessions.CompleteAnalysis(
		f.ctx,
		sessionID.String(),
		app.CompletePlanningAnalysisRequest{
			RequirementAnalysisJSON: analysis,
			ImplementationPlanJSON:  plan,
			Messages: []app.PlanningRoleMessage{
				{SenderType: domain.PlanningSenderRequirementAnalyst, Content: "需求分析师：结论"},
				{SenderType: domain.PlanningSenderPlanner, Content: "方案规划师：方案"},
			},
		},
	)
	if err != nil {
		t.Fatalf("完成分析失败: %v", err)
	}
	return result.Revision
}

// previewSession 创建并推进到 preview_ready 的会话与方案版本。
func (f *confirmFixture) previewSession(
	t *testing.T,
	analysis, plan json.RawMessage,
) (domain.PlanningSession, domain.PlanningRevision) {
	t.Helper()
	session := f.newSession(t)
	revision := f.toPreviewReady(t, session.ID, analysis, plan)
	return session, revision
}

// loadSession 重新读取规划会话。
func (f *confirmFixture) loadSession(t *testing.T, sessionID domain.ID) domain.PlanningSession {
	t.Helper()
	session, err := f.db.GetPlanningSession(f.ctx, sessionID)
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	return *session
}

// requirements 返回全部需求。
func (f *confirmFixture) requirements(t *testing.T) []domain.Requirement {
	t.Helper()
	requirements, err := f.db.ListRequirements(f.ctx, storage.RequirementFilter{})
	if err != nil {
		t.Fatalf("查询需求失败: %v", err)
	}
	return requirements
}

// tasks 返回全部任务。
func (f *confirmFixture) tasks(t *testing.T) []domain.Task {
	t.Helper()
	tasks, err := f.db.ListTasks(f.ctx, storage.TaskFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	return tasks
}

// requireNoEntities 断言没有写入任何正式实体与会话状态变化。
func (f *confirmFixture) requireNoEntities(t *testing.T, sessionID domain.ID, original domain.PlanningSession) {
	t.Helper()
	if count := len(f.requirements(t)); count != 0 {
		t.Fatalf("失败后不应留下需求，实际 %d 条", count)
	}
	if count := len(f.tasks(t)); count != 0 {
		t.Fatalf("失败后不应留下任务，实际 %d 条", count)
	}
	session := f.loadSession(t, sessionID)
	if session.Status != domain.PlanningSessionPreviewReady {
		t.Fatalf("失败后会话状态应保持 preview_ready，实际 %s", session.Status)
	}
	if session.AdoptedRevisionID != nil {
		t.Fatalf("失败后不应写入采纳的 Revision: %+v", session.AdoptedRevisionID)
	}
	if original.AdoptedRevisionID != nil {
		t.Fatalf("用例前置状态应没有采纳的 Revision")
	}
}

// TestConfirmCreatesSingleRequirementAndTasks 验收 1 需求 + N 任务的完整生成结果。
func TestConfirmCreatesSingleRequirementAndTasks(t *testing.T) {
	t.Parallel()
	for _, count := range []int{1, 3} {
		t.Run(fmt.Sprintf("%d个任务", count), func(t *testing.T) {
			t.Parallel()
			f := newConfirmFixture(t)
			analysis := confirmAnalysisJSON("补齐方案确认")
			plan := confirmPlanJSON(count)
			session, revision := f.previewSession(t, analysis, plan)

			result, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
				PlanningSessionID: session.ID.String(),
				RevisionID:        revision.ID.String(),
			})
			if err != nil {
				t.Fatalf("确认方案失败: %v", err)
			}
			if result.AdoptedRevisionID != revision.ID {
				t.Fatalf("采纳的版本不符: %s", result.AdoptedRevisionID)
			}

			// 需求快照必须与选定 Revision 完全一致。
			requirements := f.requirements(t)
			if len(requirements) != 1 {
				t.Fatalf("确认应生成 1 条需求，实际 %d 条", len(requirements))
			}
			persisted := requirements[0]
			if persisted.ID != result.Requirement.ID {
				t.Fatalf("返回的需求标识与数据库不一致")
			}
			if string(persisted.FinalAnalysisJSON) != string(analysis) ||
				string(persisted.FinalPlanJSON) != string(plan) {
				t.Fatalf("需求快照与选定 Revision 不一致")
			}
			if persisted.ProjectID != confirmProjectID {
				t.Fatalf("需求项目关联不符: %s", persisted.ProjectID)
			}
			if persisted.PlanningSessionID == nil || *persisted.PlanningSessionID != session.ID {
				t.Fatalf("需求规划会话关联不符: %+v", persisted.PlanningSessionID)
			}
			if persisted.ConfirmedAt == nil {
				t.Fatalf("需求必须记录确认时间")
			}
			if persisted.Title != "补齐方案确认" {
				t.Fatalf("需求标题应取自分析摘要: %q", persisted.Title)
			}

			// 任务顺序与 Revision 数组顺序一致，状态保持 queued。
			tasks := f.tasks(t)
			if len(tasks) != count {
				t.Fatalf("确认应生成 %d 个任务，实际 %d 个", count, len(tasks))
			}
			ordered := make(map[int]domain.Task, count)
			for _, task := range tasks {
				if task.RequirementID != persisted.ID {
					t.Fatalf("任务未关联到新需求: %+v", task)
				}
				if task.Status != domain.TaskStatusQueued {
					t.Fatalf("任务状态应保持 queued，实际 %s", task.Status)
				}
				if task.RequirementSequence == nil {
					t.Fatalf("任务必须记录需求内顺序")
				}
				ordered[*task.RequirementSequence] = task
			}
			for index := 1; index <= count; index++ {
				task, ok := ordered[index]
				if !ok {
					t.Fatalf("缺少顺序 %d 的任务", index)
				}
				// 项目配置被完整继承：仓库、分支、环境、缓存、工作目录与执行节点。
				if task.Config.ProjectID != confirmProjectID {
					t.Fatalf("任务配置缺少项目标识: %+v", task.Config)
				}
				if task.Config.Environment.Name != "go" {
					t.Fatalf("任务配置未继承项目环境: %+v", task.Config.Environment)
				}
				if task.Config.WorkDir != domain.DefaultContainerWorkDir {
					t.Fatalf("任务配置未继承项目工作目录: %q", task.Config.WorkDir)
				}
				if len(task.Config.Caches) != 1 || task.Config.Caches[0] != "go-mod" {
					t.Fatalf("任务配置未继承项目缓存: %+v", task.Config.Caches)
				}
				if task.Config.PlannerRoleID != domain.RoleIDPlanner ||
					task.Config.DeveloperRoleID != domain.RoleIDDeveloper {
					t.Fatalf("任务配置角色不符: %+v", task.Config)
				}
				if task.HostID == nil || *task.HostID != f.host.ID {
					t.Fatalf("任务执行节点应继承项目默认节点: %+v", task.HostID)
				}
				if len(task.Config.Verification.AcceptanceCriteria) != 1 {
					t.Fatalf("任务配置未写入验收条件: %+v", task.Config.Verification)
				}
			}

			// 会话收敛为 confirmed 并记录采纳版本。
			converged := f.loadSession(t, session.ID)
			if converged.Status != domain.PlanningSessionConfirmed {
				t.Fatalf("会话状态应收敛为 confirmed，实际 %s", converged.Status)
			}
			if converged.AdoptedRevisionID == nil || *converged.AdoptedRevisionID != revision.ID {
				t.Fatalf("会话未记录采纳的版本: %+v", converged.AdoptedRevisionID)
			}
			if converged.FinishedAt == nil {
				t.Fatalf("已确认会话必须记录结束时间")
			}

			// 不创建执行记录：没有步骤、尝试、会话或工作流执行。
			steps, err := f.db.ListStepsByTask(f.ctx, persisted.ID)
			if err == nil && len(steps) != 0 {
				t.Fatalf("确认阶段不应创建步骤: %d 个", len(steps))
			}
			for _, task := range tasks {
				attempts, attemptErr := f.db.ListAttemptsByStep(f.ctx, task.ID)
				if attemptErr == nil && len(attempts) != 0 {
					t.Fatalf("确认阶段不应创建执行尝试")
				}
			}
			// 防回归：确认阶段不启动执行，也不产生任何执行现场。
			runs, runErr := f.db.ListUnfinishedWorkflowRuns(f.ctx)
			if runErr != nil {
				t.Fatalf("查询工作流执行记录失败: %v", runErr)
			}
			if len(runs) != 0 {
				t.Fatalf("确认阶段不应创建工作流执行记录: %d 条", len(runs))
			}
			for _, task := range tasks {
				sessions, sessionErr := f.db.ListSessionsByTask(f.ctx, task.ID)
				if sessionErr != nil {
					t.Fatalf("查询执行会话失败: %v", sessionErr)
				}
				if len(sessions) != 0 {
					t.Fatalf("确认阶段不应创建执行会话记录")
				}
				if task.ContainerID != "" || task.StartedAt != nil || task.FinishedAt != nil {
					t.Fatalf("确认生成的任务不应带执行现场: %+v", task)
				}
			}
			containers, containerErr := f.db.ListContainers(f.ctx, domain.ContainerFilter{})
			if containerErr != nil {
				t.Fatalf("查询容器记录失败: %v", containerErr)
			}
			if len(containers) != 0 {
				t.Fatalf("确认阶段不应创建容器: %d 个", len(containers))
			}
		})
	}
}

// TestConfirmKeepsSelectedOldRevision 验收确认使用用户指定的旧版本。
func TestConfirmKeepsSelectedOldRevision(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	first := confirmAnalysisJSON("第一版需求")
	plan := confirmPlanJSON(2)
	session := f.newSession(t)
	oldRevision := f.toPreviewReady(t, session.ID, first, plan)
	newRevision := f.toPreviewReady(t, session.ID, confirmAnalysisJSON("第二版需求"), plan)
	if oldRevision.ID == newRevision.ID {
		t.Fatalf("两次分析应产生不同版本")
	}

	result, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        oldRevision.ID.String(),
	})
	if err != nil {
		t.Fatalf("确认旧版本失败: %v", err)
	}
	if result.AdoptedRevisionID != oldRevision.ID {
		t.Fatalf("应采用用户指定的旧版本，实际 %s", result.AdoptedRevisionID)
	}
	if result.Requirement.Title != "第一版需求" {
		t.Fatalf("需求快照应来自旧版本: %q", result.Requirement.Title)
	}
	converged := f.loadSession(t, session.ID)
	if converged.AdoptedRevisionID == nil || *converged.AdoptedRevisionID != oldRevision.ID {
		t.Fatalf("会话未记录旧版本: %+v", converged.AdoptedRevisionID)
	}
}

// TestConfirmRejectsOtherSessionRevision 验收跨会话的 Revision 不能被采纳。
func TestConfirmRejectsOtherSessionRevision(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	analysis := confirmAnalysisJSON("需求")
	plan := confirmPlanJSON(1)
	session, _ := f.previewSession(t, analysis, plan)
	otherSession := f.newSession(t)
	otherRevision := f.toPreviewReady(t, otherSession.ID, analysis, plan)

	_, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        otherRevision.ID.String(),
	})
	if !errors.Is(err, app.ErrRequirementRevisionNotFound) {
		t.Fatalf("跨会话版本应返回版本不存在，实际 %v", err)
	}
	f.requireNoEntities(t, session.ID, f.loadSession(t, session.ID))
}

// TestConfirmRejectsUnexpectedSessionState 验收会话状态不满足前置条件时拒绝确认。
func TestConfirmRejectsUnexpectedSessionState(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	session := f.newSession(t)
	_, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        domain.NewID().String(),
	})
	if !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("active 会话不应被确认，实际 %v", err)
	}
	if len(f.requirements(t)) != 0 || len(f.tasks(t)) != 0 {
		t.Fatalf("状态不满足时不应写入正式实体")
	}
}

// TestConfirmRejectsMissingSession 验收未知会话按不存在处理。
func TestConfirmRejectsMissingSession(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	_, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: domain.NewID().String(),
		RevisionID:        domain.NewID().String(),
	})
	if !errors.Is(err, app.ErrRequirementNotFound) {
		t.Fatalf("未知会话应返回目标不存在，实际 %v", err)
	}
}

// TestConfirmRejectsInvalidPlanSnapshot 验收非法方案不能创建正式实体。
func TestConfirmRejectsInvalidPlanSnapshot(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	// 空任务列表在结构化校验阶段被拒绝，因此这一版快照不可能生成正式任务。
	session, revision := f.previewSession(
		t,
		confirmAnalysisJSON("需求"),
		json.RawMessage(`{"summary":"空方案","tasks":[]}`),
	)
	_, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        revision.ID.String(),
	})
	if !errors.Is(err, app.ErrRequirementInvalidInput) {
		t.Fatalf("非法方案应返回参数非法，实际 %v", err)
	}
	f.requireNoEntities(t, session.ID, f.loadSession(t, session.ID))
}

// TestConfirmRejectsUnparseableRevisionSnapshot 验收结构不可解析的 Revision 快照。
//
// Revision 落库时只做 JSON 合法性检查，字段语义由读取路径负责：这里保存一份"合法 JSON
// 但不符合任务草案结构"的方案快照，确认必须拒绝解析，且不写入任何正式实体。
func TestConfirmRejectsUnparseableRevisionSnapshot(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	session, revision := f.previewSession(
		t,
		confirmAnalysisJSON("需求"),
		json.RawMessage(`{"summary":"方案","tasks":[{"id":"task-1"}]}`),
	)
	_, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        revision.ID.String(),
	})
	if !errors.Is(err, app.ErrRequirementInvalidInput) {
		t.Fatalf("不可解析的方案快照应返回参数非法，实际 %v", err)
	}
	f.requireNoEntities(t, session.ID, f.loadSession(t, session.ID))
}

// TestConfirmRequiresUsableHost 验收缺少可用执行节点时拒绝确认。
func TestConfirmRequiresUsableHost(t *testing.T) {
	t.Parallel()
	t.Run("项目没有默认节点", func(t *testing.T) {
		t.Parallel()
		f := newConfirmFixture(t)
		project, err := f.projects.Create(f.ctx, app.CreateProjectRequest{
			BaseBranch:    "main",
			Environment:   "go",
			ID:            confirmProjectID,
			Name:          "无默认节点项目",
			RepositoryURL: "ssh://git.example.com/team/no-host",
		})
		if err != nil {
			t.Fatalf("创建项目失败: %v", err)
		}
		f.project = project
		session, revision := f.previewSession(t, confirmAnalysisJSON("需求"), confirmPlanJSON(1))
		_, confirmErr := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
			PlanningSessionID: session.ID.String(),
			RevisionID:        revision.ID.String(),
		})
		if !errors.Is(confirmErr, app.ErrRequirementPrerequisite) {
			t.Fatalf("缺少执行节点应返回前置条件不满足，实际 %v", confirmErr)
		}
		f.requireNoEntities(t, session.ID, f.loadSession(t, session.ID))
	})

	t.Run("显式节点可用", func(t *testing.T) {
		t.Parallel()
		f := newConfirmFixture(t)
		project, err := f.projects.Create(f.ctx, app.CreateProjectRequest{
			BaseBranch:    "main",
			Environment:   "go",
			ID:            confirmProjectID,
			Name:          "显式节点项目",
			RepositoryURL: "ssh://git.example.com/team/explicit-host",
		})
		if err != nil {
			t.Fatalf("创建项目失败: %v", err)
		}
		f.project = project
		session, revision := f.previewSession(t, confirmAnalysisJSON("需求"), confirmPlanJSON(1))
		if _, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
			HostID:            f.host.ID.String(),
			PlanningSessionID: session.ID.String(),
			RevisionID:        revision.ID.String(),
		}); err != nil {
			t.Fatalf("显式给出可用节点时应确认成功: %v", err)
		}
	})

	t.Run("显式节点不存在", func(t *testing.T) {
		t.Parallel()
		f := newConfirmFixture(t)
		session, revision := f.previewSession(t, confirmAnalysisJSON("需求"), confirmPlanJSON(1))
		_, confirmErr := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
			HostID:            domain.NewID().String(),
			PlanningSessionID: session.ID.String(),
			RevisionID:        revision.ID.String(),
		})
		if !errors.Is(confirmErr, app.ErrRequirementPrerequisite) {
			t.Fatalf("不存在的执行节点应返回前置条件不满足，实际 %v", confirmErr)
		}
		f.requireNoEntities(t, session.ID, f.loadSession(t, session.ID))
	})

	t.Run("显式节点已禁用", func(t *testing.T) {
		t.Parallel()
		f := newConfirmFixture(t)
		disabled := f.host
		disabled.Enabled = false
		if err := f.db.UpdateExecutionHost(f.ctx, disabled); err != nil {
			t.Fatalf("禁用执行节点失败: %v", err)
		}
		session, revision := f.previewSession(t, confirmAnalysisJSON("需求"), confirmPlanJSON(1))
		_, confirmErr := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
			PlanningSessionID: session.ID.String(),
			RevisionID:        revision.ID.String(),
		})
		if !errors.Is(confirmErr, app.ErrRequirementPrerequisite) {
			t.Fatalf("禁用节点应返回前置条件不满足，实际 %v", confirmErr)
		}
		f.requireNoEntities(t, session.ID, f.loadSession(t, session.ID))
	})
}

// TestConfirmRejectsDeletedProject 验收已逻辑删除的项目不能生成正式实体。
func TestConfirmRejectsDeletedProject(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	session, revision := f.previewSession(t, confirmAnalysisJSON("需求"), confirmPlanJSON(1))
	if err := f.projects.Delete(f.ctx, confirmProjectID); err != nil {
		t.Fatalf("删除项目失败: %v", err)
	}
	_, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        revision.ID.String(),
	})
	if !errors.Is(err, app.ErrRequirementNotFound) {
		t.Fatalf("已删除项目应返回目标不存在，实际 %v", err)
	}
	if len(f.requirements(t)) != 0 || len(f.tasks(t)) != 0 {
		t.Fatalf("项目已删除时不应写入正式实体")
	}
}

// TestConfirmRejectsDuplicateConfirmation 验收同一会话只能确认一次。
func TestConfirmRejectsDuplicateConfirmation(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	session, revision := f.previewSession(t, confirmAnalysisJSON("需求"), confirmPlanJSON(2))
	request := app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        revision.ID.String(),
	}
	if _, err := f.service.Confirm(f.ctx, request); err != nil {
		t.Fatalf("首次确认失败: %v", err)
	}
	if _, err := f.service.Confirm(f.ctx, request); !errors.Is(err, app.ErrRequirementStateConflict) {
		t.Fatalf("重复确认应返回状态冲突，实际 %v", err)
	}
	if len(f.requirements(t)) != 1 {
		t.Fatalf("重复确认不应生成第二个需求: %d", len(f.requirements(t)))
	}
	if len(f.tasks(t)) != 2 {
		t.Fatalf("重复确认不应生成第二批任务: %d", len(f.tasks(t)))
	}
}

// TestConfirmConcurrentOnlyOneSucceeds 验收并发确认只有一个成功。
func TestConfirmConcurrentOnlyOneSucceeds(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	session, revision := f.previewSession(t, confirmAnalysisJSON("需求"), confirmPlanJSON(2))
	request := app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        revision.ID.String(),
	}
	results := make([]error, 2)
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < len(results); index++ {
		wait.Add(1)
		go func(slot int) {
			defer wait.Done()
			<-start
			_, results[slot] = f.service.Confirm(context.Background(), request)
		}(index)
	}
	close(start)
	wait.Wait()

	succeeded := 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, app.ErrRequirementStateConflict):
		default:
			t.Fatalf("并发确认的失败方应返回状态冲突，实际 %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("并发确认应只有一个成功，实际 %d 个", succeeded)
	}
	if len(f.requirements(t)) != 1 {
		t.Fatalf("并发确认只能创建一个需求: %d", len(f.requirements(t)))
	}
	if len(f.tasks(t)) != 2 {
		t.Fatalf("并发确认只能创建一批任务: %d", len(f.tasks(t)))
	}
}

// TestConfirmIsolatesProjects 验收不同项目的数据互相隔离。
func TestConfirmIsolatesProjects(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	analysis := confirmAnalysisJSON("需求")
	plan := confirmPlanJSON(1)
	first, firstRevision := f.previewSession(t, analysis, plan)
	if _, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: first.ID.String(),
		RevisionID:        firstRevision.ID.String(),
	}); err != nil {
		t.Fatalf("第一个项目确认失败: %v", err)
	}

	otherProject, err := f.projects.Create(f.ctx, app.CreateProjectRequest{
		BaseBranch:    "main",
		DefaultHostID: f.host.ID.String(),
		Environment:   "go",
		ID:            "confirm-other",
		Name:          "另一个项目",
		RepositoryURL: "ssh://git.example.com/team/other-repo",
	})
	if err != nil {
		t.Fatalf("创建第二个项目失败: %v", err)
	}
	otherSession, err := f.sessions.Create(f.ctx, app.CreatePlanningSessionRequest{
		ProjectID: otherProject.ID,
	})
	if err != nil {
		t.Fatalf("创建第二个会话失败: %v", err)
	}
	otherRevision := f.toPreviewReady(t, otherSession.ID, analysis, plan)
	if _, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: otherSession.ID.String(),
		RevisionID:        otherRevision.ID.String(),
	}); err != nil {
		t.Fatalf("第二个项目确认失败: %v", err)
	}

	firstList, err := f.service.ListByProject(f.ctx, confirmProjectID)
	if err != nil {
		t.Fatalf("查询第一个项目需求失败: %v", err)
	}
	if len(firstList) != 1 || firstList[0].ProjectID != confirmProjectID {
		t.Fatalf("第一个项目的需求列表不隔离: %+v", firstList)
	}
	otherList, err := f.service.ListByProject(f.ctx, otherProject.ID)
	if err != nil {
		t.Fatalf("查询第二个项目需求失败: %v", err)
	}
	if len(otherList) != 1 || otherList[0].ProjectID != otherProject.ID {
		t.Fatalf("第二个项目的需求列表不隔离: %+v", otherList)
	}
}

// 故障注入：在确认事务的不同写入位置制造失败。
//
// 只断言函数返回错误不足以证明事务边界：必须重新读取数据库，确认没有留下需求、任务，
// 会话状态与采纳版本也没有被改写。
type confirmFault int

const (
	confirmFaultNone confirmFault = iota
	confirmFaultCreateRequirement
	confirmFaultCreateTask
	confirmFaultSessionUpdate
	confirmFaultCommit
)

// confirmFaultError 是注入失败使用的固定错误。
var confirmFaultError = errors.New("注入的写入失败")

// confirmFaultStore 在指定位置注入一次失败，其余行为与真实存储一致。
type confirmFaultStore struct {
	storage.Store
	fault confirmFault
	// taskIndex 是第几个 CreateTask 调用失败（从 1 开始）。
	taskIndex int
	taskCalls int
}

// WithTx 在回调成功返回后按需注入提交失败，使真实事务整体回滚。
func (s *confirmFaultStore) WithTx(
	ctx context.Context,
	fn func(ctx context.Context, tx storage.Tx) error,
) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if err := fn(ctx, &confirmFaultTx{Tx: tx, store: s}); err != nil {
			return err
		}
		if s.fault == confirmFaultCommit {
			return confirmFaultError
		}
		return nil
	})
}

// confirmFaultTx 在指定写入方法上注入失败。
type confirmFaultTx struct {
	storage.Tx
	store *confirmFaultStore
}

// CreateRequirement 在注入点为需求写入失败。
func (t *confirmFaultTx) CreateRequirement(ctx context.Context, requirement domain.Requirement) error {
	if t.store.fault == confirmFaultCreateRequirement {
		return confirmFaultError
	}
	return t.Tx.CreateRequirement(ctx, requirement)
}

// CreateTask 在注入点按调用序号让任务写入失败。
func (t *confirmFaultTx) CreateTask(ctx context.Context, task domain.Task) error {
	t.store.taskCalls++
	if t.store.fault == confirmFaultCreateTask && t.store.taskCalls == t.store.taskIndex {
		return confirmFaultError
	}
	return t.Tx.CreateTask(ctx, task)
}

// UpdatePlanningSessionIfStatus 在注入点让会话条件更新失败。
func (t *confirmFaultTx) UpdatePlanningSessionIfStatus(
	ctx context.Context,
	session domain.PlanningSession,
	expected []domain.PlanningSessionStatus,
) error {
	if t.store.fault == confirmFaultSessionUpdate {
		return confirmFaultError
	}
	return t.Tx.UpdatePlanningSessionIfStatus(ctx, session, expected)
}

// TestConfirmRollsBackOnWriteFailure 验收任一写入失败都整体回滚。
func TestConfirmRollsBackOnWriteFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		fault     confirmFault
		taskIndex int
	}{
		{name: "创建需求失败", fault: confirmFaultCreateRequirement},
		{name: "创建第一个任务失败", fault: confirmFaultCreateTask, taskIndex: 1},
		{name: "创建中间任务失败", fault: confirmFaultCreateTask, taskIndex: 2},
		{name: "创建最后一个任务失败", fault: confirmFaultCreateTask, taskIndex: 3},
		{name: "写入确认快照失败", fault: confirmFaultCreateRequirement},
		{name: "条件更新会话失败", fault: confirmFaultSessionUpdate},
		{name: "事务提交失败", fault: confirmFaultCommit},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newConfirmFixture(t)
			session, revision := f.previewSession(t, confirmAnalysisJSON("需求"), confirmPlanJSON(3))
			faulted := &confirmFaultStore{
				Store:     f.db,
				fault:     testCase.fault,
				taskIndex: testCase.taskIndex,
			}
			service, err := app.NewRequirementService(app.RequirementServiceOptions{Store: faulted})
			if err != nil {
				t.Fatalf("构造注入失败的需求服务失败: %v", err)
			}
			if _, err := service.Confirm(f.ctx, app.ConfirmRequirementRequest{
				PlanningSessionID: session.ID.String(),
				RevisionID:        revision.ID.String(),
			}); err == nil {
				t.Fatalf("注入失败后确认应返回错误")
			}
			f.requireNoEntities(t, session.ID, f.loadSession(t, session.ID))
		})
	}
}

// TestRequirementServiceQueries 验收需求详情与项目需求列表的读取结果。
func TestRequirementServiceQueries(t *testing.T) {
	t.Parallel()
	f := newConfirmFixture(t)
	session, revision := f.previewSession(t, confirmAnalysisJSON("查询需求"), confirmPlanJSON(2))
	result, err := f.service.Confirm(f.ctx, app.ConfirmRequirementRequest{
		PlanningSessionID: session.ID.String(),
		RevisionID:        revision.ID.String(),
	})
	if err != nil {
		t.Fatalf("确认失败: %v", err)
	}

	detail, err := f.service.Detail(f.ctx, result.Requirement.ID.String())
	if err != nil {
		t.Fatalf("读取需求详情失败: %v", err)
	}
	if detail.Requirement.ID != result.Requirement.ID {
		t.Fatalf("需求详情标识不符")
	}
	if len(detail.Tasks) != 2 {
		t.Fatalf("需求详情任务数量不符: %d", len(detail.Tasks))
	}
	for index, task := range detail.Tasks {
		if task.RequirementSequence == nil || *task.RequirementSequence != index+1 {
			t.Fatalf("需求详情任务顺序不符: %+v", task.RequirementSequence)
		}
	}

	list, err := f.service.ListByProject(f.ctx, strings.ToUpper(confirmProjectID))
	if err != nil {
		t.Fatalf("查询项目需求失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("项目需求列表数量不符: %d", len(list))
	}

	if _, err := f.service.Get(f.ctx, domain.NewID().String()); !errors.Is(err, app.ErrRequirementNotFound) {
		t.Fatalf("未知需求应返回目标不存在，实际 %v", err)
	}
	if _, err := f.service.Get(f.ctx, "  "); !errors.Is(err, app.ErrRequirementInvalidInput) {
		t.Fatalf("空需求标识应返回参数非法，实际 %v", err)
	}
	if _, err := f.service.ListByProject(f.ctx, "missing-project"); !errors.Is(err, app.ErrRequirementNotFound) {
		t.Fatalf("未知项目应返回目标不存在，实际 %v", err)
	}
}
