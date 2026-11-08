package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 方案确认与正式需求查询接口的协议测试。
//
// 这些用例只覆盖协议转换、错误分类与真实返回结构：确认本身的原子性由应用层用例证明。

// requirementProjectID 是确认接口用例使用的项目标识。
const requirementProjectID = "requirement-api-demo"

// requirementAPIAnalysisJSON 返回一份通过领域校验的需求分析快照。
func requirementAPIAnalysisJSON() json.RawMessage {
	return json.RawMessage(`{"summary":"确认生成正式实体",` +
		`"goals":["生成正式需求与任务"],` +
		`"current_state":["已有规划会话"],` +
		`"scope":["internal/httpapi"],` +
		`"out_of_scope":["不做调度"],` +
		`"constraints":["不新增外键"],` +
		`"risks":["事务边界容易写错"],` +
		`"acceptance_criteria":["失败不留半成品"]}`)
}

// requirementAPIPlanJSON 返回一份包含两个任务的实施方案快照。
func requirementAPIPlanJSON() json.RawMessage {
	return json.RawMessage(`{"summary":"按阶段推进","tasks":[` +
		`{"id":"task-1","title":"第一个任务","description":"实现第一部分",` +
		`"depends_on":[],"acceptance_criteria":["可以独立验收"]},` +
		`{"id":"task-2","title":"第二个任务","description":"实现第二部分",` +
		`"depends_on":["task-1"],"acceptance_criteria":["可以独立验收"]}]}`)
}

// requirementAPIFixture 是确认接口的测试现场。
type requirementAPIFixture struct {
	db         *sqlite.DB
	ctx        context.Context
	host       domain.ExecutionHost
	project    domain.Project
	projects   *app.ProjectService
	service    *app.RequirementService
	sessions   *app.PlanningSessionService
	handler    http.Handler
	bare       http.Handler
	store      storage.Store
	projectSet bool
}

// newRequirementAPIFixture 打开测试数据库并装配需求与规划会话接口。
func newRequirementAPIFixture(t *testing.T) *requirementAPIFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
		"requirement-api-host",
		"确认接口测试节点",
		"10.0.0.2",
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
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
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
	return &requirementAPIFixture{
		bare:     httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog()}),
		ctx:      ctx,
		db:       db,
		handler:  httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog(), Requirement: service}),
		host:     host,
		projects: projects,
		service:  service,
		sessions: sessions,
		store:    db,
	}
}

// ensureProject 创建带默认执行节点的测试项目。
func (f *requirementAPIFixture) ensureProject(t *testing.T) domain.Project {
	t.Helper()
	if f.projectSet {
		return f.project
	}
	project, err := f.projects.Create(f.ctx, app.CreateProjectRequest{
		BaseBranch:    "main",
		DefaultHostID: f.host.ID.String(),
		Environment:   "go",
		ID:            requirementProjectID,
		Name:          "确认接口示例项目",
		RepositoryURL: "ssh://git.example.com/team/requirement-api",
	})
	if err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	f.project = project
	f.projectSet = true
	return project
}

// previewSession 创建并推进到 preview_ready 的会话与方案版本。
func (f *requirementAPIFixture) previewSession(
	t *testing.T,
) (domain.PlanningSession, domain.PlanningRevision) {
	t.Helper()
	project := f.ensureProject(t)
	session, err := f.sessions.Create(f.ctx, app.CreatePlanningSessionRequest{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	if _, err := f.sessions.SubmitMessage(f.ctx, session.ID.String(), app.SubmitPlanningMessageRequest{
		Content: "确认这一版方案",
	}); err != nil {
		t.Fatalf("提交规划消息失败: %v", err)
	}
	result, err := f.sessions.CompleteAnalysis(
		f.ctx,
		session.ID.String(),
		app.CompletePlanningAnalysisRequest{
			RequirementAnalysisJSON: requirementAPIAnalysisJSON(),
			ImplementationPlanJSON:  requirementAPIPlanJSON(),
			Messages: []app.PlanningRoleMessage{
				{SenderType: domain.PlanningSenderRequirementAnalyst, Content: "需求分析师：结论"},
				{SenderType: domain.PlanningSenderPlanner, Content: "方案规划师：方案"},
			},
		},
	)
	if err != nil {
		t.Fatalf("完成分析失败: %v", err)
	}
	return session, result.Revision
}

// send 发送一次请求并返回响应记录器。
func (f *requirementAPIFixture) send(
	t *testing.T,
	handler http.Handler,
	method, target string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else if raw, ok := body.(string); ok {
		reader = bytes.NewReader([]byte(raw))
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// confirmPath 返回确认接口路径。
func confirmPath(sessionID domain.ID) string {
	return httpapi.PathPlanningSessions + "/" + sessionID.String() + "/confirm"
}

// TestRequirementAPIConfirmAndQuery 验收确认接口返回真实实体并可按需求与项目查询。
func TestRequirementAPIConfirmAndQuery(t *testing.T) {
	t.Parallel()
	f := newRequirementAPIFixture(t)
	session, revision := f.previewSession(t)
	recorder := f.send(t, f.handler, http.MethodPost, confirmPath(session.ID), map[string]string{
		"revision_id": revision.ID.String(),
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("确认应返回 201，实际 %d，响应体 %s", recorder.Code, recorder.Body.String())
	}
	var payload httpapi.ConfirmRequirementResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析确认响应失败: %v", err)
	}
	if payload.Requirement.ID == "" {
		t.Fatalf("确认响应必须返回真实需求标识: %+v", payload)
	}
	if payload.Requirement.PlanningSessionID != session.ID.String() {
		t.Fatalf("需求规划会话关联不符: %s", payload.Requirement.PlanningSessionID)
	}
	if payload.Requirement.ProjectID != requirementProjectID {
		t.Fatalf("需求项目关联不符: %s", payload.Requirement.ProjectID)
	}
	if payload.Requirement.ConfirmedAt == nil {
		t.Fatalf("确认响应缺少确认时间")
	}
	if payload.AdoptedRevisionID != revision.ID.String() {
		t.Fatalf("采纳的版本标识不符: %s", payload.AdoptedRevisionID)
	}
	if len(payload.Tasks) != 2 {
		t.Fatalf("确认响应任务数量不符: %d", len(payload.Tasks))
	}
	for index, task := range payload.Tasks {
		if task.RequirementSequence == nil || *task.RequirementSequence != index+1 {
			t.Fatalf("第 %d 个任务的顺序不符: %+v", index+1, task.RequirementSequence)
		}
		if task.Status != string(domain.TaskStatusQueued) {
			t.Fatalf("任务状态应为 queued，实际 %s", task.Status)
		}
		if task.ID == "" || task.RequirementID != payload.Requirement.ID {
			t.Fatalf("任务标识或归属不符: %+v", task)
		}
		if task.HostID != f.host.ID.String() {
			t.Fatalf("任务执行节点应继承项目默认节点: %s", task.HostID)
		}
	}

	detail := f.send(
		t,
		f.handler,
		http.MethodGet,
		httpapi.PathRequirements+"/"+payload.Requirement.ID,
		nil,
	)
	if detail.Code != http.StatusOK {
		t.Fatalf("读取需求详情应返回 200，实际 %d，响应体 %s", detail.Code, detail.Body.String())
	}
	var detailPayload httpapi.RequirementDetailResponse
	if err := json.Unmarshal(detail.Body.Bytes(), &detailPayload); err != nil {
		t.Fatalf("解析需求详情失败: %v", err)
	}
	if detailPayload.Requirement.ID != payload.Requirement.ID {
		t.Fatalf("需求详情标识不符")
	}
	if len(detailPayload.Tasks) != 2 {
		t.Fatalf("需求详情任务数量不符: %d", len(detailPayload.Tasks))
	}

	list := f.send(
		t,
		f.handler,
		http.MethodGet,
		httpapi.PathProjects+"/"+requirementProjectID+"/requirements",
		nil,
	)
	if list.Code != http.StatusOK {
		t.Fatalf("查询项目需求应返回 200，实际 %d，响应体 %s", list.Code, list.Body.String())
	}
	var listPayload httpapi.RequirementListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("解析项目需求列表失败: %v", err)
	}
	if listPayload.Total != 1 || len(listPayload.Records) != 1 {
		t.Fatalf("项目需求列表数量不符: %+v", listPayload)
	}
	if listPayload.Records[0].ID != payload.Requirement.ID {
		t.Fatalf("项目需求列表内容不符: %+v", listPayload.Records)
	}
}

// TestRequirementAPIConfirmRejectsInvalidRequest 验收请求结构与参数非法返回 400。
func TestRequirementAPIConfirmRejectsInvalidRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body any
	}{
		{name: "非法 JSON", body: `{`},
		{name: "未知字段", body: map[string]string{"revision_id": "x", "task_list": "[]"}},
		{name: "缺少 revision_id", body: map[string]string{}},
		{name: "revision_id 为空白", body: map[string]string{"revision_id": "   "}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newRequirementAPIFixture(t)
			session, _ := f.previewSession(t)
			recorder := f.send(t, f.handler, http.MethodPost, confirmPath(session.ID), testCase.body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("非法请求应返回 400，实际 %d，响应体 %s",
					recorder.Code, recorder.Body.String())
			}
			if code := planningErrorCode(t, recorder); code != httpapi.CodeInvalidRequest {
				t.Fatalf("非法请求的错误代码应为 %s，实际 %s", httpapi.CodeInvalidRequest, code)
			}
		})
	}
}

// TestRequirementAPIConfirmReportsNotFound 验收未知会话与跨会话版本返回 404。
func TestRequirementAPIConfirmReportsNotFound(t *testing.T) {
	t.Parallel()
	t.Run("会话不存在", func(t *testing.T) {
		t.Parallel()
		f := newRequirementAPIFixture(t)
		recorder := f.send(t, f.handler, http.MethodPost, confirmPath(domain.NewID()), map[string]string{
			"revision_id": domain.NewID().String(),
		})
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("未知会话应返回 404，实际 %d，响应体 %s",
				recorder.Code, recorder.Body.String())
		}
	})

	t.Run("版本不属于当前会话", func(t *testing.T) {
		t.Parallel()
		f := newRequirementAPIFixture(t)
		session, _ := f.previewSession(t)
		_, otherRevision := f.previewSession(t)
		recorder := f.send(t, f.handler, http.MethodPost, confirmPath(session.ID), map[string]string{
			"revision_id": otherRevision.ID.String(),
		})
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("跨会话版本应返回 404，实际 %d，响应体 %s",
				recorder.Code, recorder.Body.String())
		}
		if code := planningErrorCode(t, recorder); code != httpapi.CodeNotFound {
			t.Fatalf("跨会话版本的错误代码应为 %s，实际 %s", httpapi.CodeNotFound, code)
		}
	})

	t.Run("需求不存在", func(t *testing.T) {
		t.Parallel()
		f := newRequirementAPIFixture(t)
		recorder := f.send(
			t,
			f.handler,
			http.MethodGet,
			httpapi.PathRequirements+"/"+domain.NewID().String(),
			nil,
		)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("未知需求应返回 404，实际 %d，响应体 %s",
				recorder.Code, recorder.Body.String())
		}
	})
}

// TestRequirementAPIConfirmReportsStateConflict 验收状态冲突与重复确认返回 409。
func TestRequirementAPIConfirmReportsStateConflict(t *testing.T) {
	t.Parallel()
	t.Run("会话未生成方案", func(t *testing.T) {
		t.Parallel()
		f := newRequirementAPIFixture(t)
		project := f.ensureProject(t)
		session, err := f.sessions.Create(f.ctx, app.CreatePlanningSessionRequest{ProjectID: project.ID})
		if err != nil {
			t.Fatalf("创建规划会话失败: %v", err)
		}
		recorder := f.send(t, f.handler, http.MethodPost, confirmPath(session.ID), map[string]string{
			"revision_id": domain.NewID().String(),
		})
		if recorder.Code != http.StatusConflict {
			t.Fatalf("状态不满足应返回 409，实际 %d，响应体 %s",
				recorder.Code, recorder.Body.String())
		}
		if code := planningErrorCode(t, recorder); code != httpapi.CodeConflict {
			t.Fatalf("状态冲突的错误代码应为 %s，实际 %s", httpapi.CodeConflict, code)
		}
	})

	t.Run("重复确认", func(t *testing.T) {
		t.Parallel()
		f := newRequirementAPIFixture(t)
		session, revision := f.previewSession(t)
		body := map[string]string{"revision_id": revision.ID.String()}
		first := f.send(t, f.handler, http.MethodPost, confirmPath(session.ID), body)
		if first.Code != http.StatusCreated {
			t.Fatalf("首次确认应返回 201，实际 %d，响应体 %s",
				first.Code, first.Body.String())
		}
		second := f.send(t, f.handler, http.MethodPost, confirmPath(session.ID), body)
		if second.Code != http.StatusConflict {
			t.Fatalf("重复确认应返回 409，实际 %d，响应体 %s",
				second.Code, second.Body.String())
		}
	})
}

// TestRequirementAPIConfirmReportsPrerequisite 验收前置条件不满足返回 412。
func TestRequirementAPIConfirmReportsPrerequisite(t *testing.T) {
	t.Parallel()
	f := newRequirementAPIFixture(t)
	// 项目没有默认执行节点，确认必须显式指定节点，否则返回 412。
	project, err := f.projects.Create(f.ctx, app.CreateProjectRequest{
		BaseBranch:    "main",
		Environment:   "go",
		ID:            requirementProjectID,
		Name:          "无默认节点项目",
		RepositoryURL: "ssh://git.example.com/team/no-default-host",
	})
	if err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}
	f.project = project
	f.projectSet = true
	session, revision := f.previewSession(t)
	recorder := f.send(t, f.handler, http.MethodPost, confirmPath(session.ID), map[string]string{
		"revision_id": revision.ID.String(),
	})
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("缺少执行节点应返回 412，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	if code := planningErrorCode(t, recorder); code != httpapi.CodePreconditionFailed {
		t.Fatalf("前置条件不满足的错误代码应为 %s，实际 %s",
			httpapi.CodePreconditionFailed, code)
	}
}

// failingRequirementStore 让事务开启即失败，用于验收持久化失败的错误分类。
type failingRequirementStore struct {
	storage.Store
}

// WithTx 直接返回存储失败，不写入任何记录。
func (s *failingRequirementStore) WithTx(
	ctx context.Context,
	fn func(ctx context.Context, tx storage.Tx) error,
) error {
	return errors.New("存储不可用")
}

// TestRequirementAPIConfirmReportsPersistenceFailure 验收持久化失败返回 500。
func TestRequirementAPIConfirmReportsPersistenceFailure(t *testing.T) {
	t.Parallel()
	f := newRequirementAPIFixture(t)
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	broken, err := app.NewRequirementService(app.RequirementServiceOptions{
		Store: &failingRequirementStore{Store: f.store},
	})
	if err != nil {
		t.Fatalf("构造失败存储的需求服务失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog(), Requirement: broken})
	session, revision := f.previewSession(t)
	recorder := f.send(t, handler, http.MethodPost, confirmPath(session.ID), map[string]string{
		"revision_id": revision.ID.String(),
	})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("持久化失败应返回 500，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	// 失败不得留下任何正式实体。
	if len(f.requirements(t)) != 0 {
		t.Fatalf("持久化失败后不应留下需求")
	}
}

// requirements 返回当前库中的全部需求。
func (f *requirementAPIFixture) requirements(t *testing.T) []domain.Requirement {
	t.Helper()
	requirements, err := f.db.ListRequirements(f.ctx, storage.RequirementFilter{})
	if err != nil {
		t.Fatalf("查询需求失败: %v", err)
	}
	return requirements
}

// TestRequirementAPIConfirmConcurrent 验收并发确认只有一个成功。
func TestRequirementAPIConfirmConcurrent(t *testing.T) {
	t.Parallel()
	f := newRequirementAPIFixture(t)
	session, revision := f.previewSession(t)
	body := map[string]string{"revision_id": revision.ID.String()}
	codes := make([]int, 2)
	done := make(chan struct{}, 2)
	for index := range codes {
		go func(slot int) {
			recorder := f.send(t, f.handler, http.MethodPost, confirmPath(session.ID), body)
			codes[slot] = recorder.Code
			done <- struct{}{}
		}(index)
	}
	for range codes {
		<-done
	}
	created := 0
	for _, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Fatalf("并发确认的失败方应返回 409，实际状态码 %d", code)
		}
	}
	if created != 1 {
		t.Fatalf("并发确认应只有一个成功，实际 %d 个", created)
	}
	if len(f.requirements(t)) != 1 {
		t.Fatalf("并发确认只能创建一个需求: %d", len(f.requirements(t)))
	}
}

// TestRequirementAPIUnavailable 验收未装配需求服务时接口返回 503。
func TestRequirementAPIUnavailable(t *testing.T) {
	t.Parallel()
	f := newRequirementAPIFixture(t)
	session, revision := f.previewSession(t)
	recorder := f.send(t, f.bare, http.MethodPost, confirmPath(session.ID), map[string]string{
		"revision_id": revision.ID.String(),
	})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配需求服务应返回 503，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
}
