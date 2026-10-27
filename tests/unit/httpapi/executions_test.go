package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// executionAPIFixture 是执行记录查询接口的测试环境。
type executionAPIFixture struct {
	handler   http.Handler
	db        *sqlite.DB
	ctx       context.Context
	task      domain.Task
	otherTask domain.Task
	developer domain.Role
	validator domain.Role
	step      domain.Step
	otherStep domain.Step
	attempt   domain.Attempt
}

// newExecutionAPIFixture 准备任务、步骤、尝试与会话，并构造处理器。
func newExecutionAPIFixture(t *testing.T) *executionAPIFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	now := time.Now().UTC()
	fixture := &executionAPIFixture{db: db, ctx: ctx}
	fixture.developer = apiRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	fixture.validator = apiRole(t, db, ctx, domain.RoleTypeValidator, "验证工程师", now)
	fixture.task = apiTaskWithStep(t, db, ctx, now, fixture.developer, &fixture.step, 1)
	fixture.otherTask = apiTaskWithStep(t, db, ctx, now, fixture.developer, &fixture.otherStep, 1)

	fixture.attempt, err = db.StartAttempt(ctx, storage.StartAttemptRequest{
		TaskID: fixture.task.ID,
		StepID: fixture.step.ID,
		At:     now,
	})
	if err != nil {
		t.Fatalf("开始执行尝试失败: %v", err)
	}
	stepID := fixture.step.ID
	attemptID := fixture.attempt.ID
	apiSession(t, db, ctx, domain.SessionScope{
		TaskID:    fixture.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, fixture.developer)
	apiSession(t, db, ctx, domain.SessionScope{
		TaskID:    fixture.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, fixture.validator)

	records, err := app.NewExecutionRecordService(db)
	if err != nil {
		t.Fatalf("构造执行记录服务失败: %v", err)
	}
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	fixture.handler = httpapi.NewHandler(httpapi.Deps{
		Health:           app.NewHealthService(db),
		SystemInfo:       func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:            tasks,
		ExecutionRecords: records,
	})
	return fixture
}

// handlerWithHistory 用同一个数据库构造额外装配历史查询服务的处理器。
func handlerWithHistory(
	t *testing.T,
	fixture *executionAPIFixture,
) http.Handler {
	t.Helper()
	history, err := app.NewHistoryQueryService(fixture.db)
	if err != nil {
		t.Fatalf("构造历史查询服务失败: %v", err)
	}
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: fixture.db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	records, err := app.NewExecutionRecordService(fixture.db)
	if err != nil {
		t.Fatalf("构造执行记录服务失败: %v", err)
	}
	return httpapi.NewHandler(httpapi.Deps{
		Health:           app.NewHealthService(fixture.db),
		SystemInfo:       func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:            tasks,
		ExecutionRecords: records,
		History:          history,
	})
}

// apiTaskWithStep 写入需求、任务与一个步骤，并把步骤回填到 out。
func apiTaskWithStep(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
	role domain.Role,
	out *domain.Step,
	sequence int,
) domain.Task {
	t.Helper()
	requirement, err := domain.NewRequirement(
		"需求标题",
		"需求正文",
		domain.PriorityNormal,
		now,
	)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	task, err := domain.NewTask(requirement.ID, "/repo", "main", "", now)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	step, err := domain.NewStep(
		task.ID,
		sequence,
		"步骤标题",
		"步骤目标",
		"执行要求",
		role,
		domain.VerificationSpec{},
		now,
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	if err := db.CreateStep(ctx, step); err != nil {
		t.Fatalf("写入步骤失败: %v", err)
	}
	*out = step
	return task
}

// apiSession 写入一个会话并返回它。
func apiSession(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	scope domain.SessionScope,
	role domain.Role,
) domain.Session {
	t.Helper()
	session, err := domain.NewSession(scope, role, "pi", "provider/model", time.Now().UTC())
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatalf("写入会话失败: %v", err)
	}
	return session
}

// get 发送 GET 请求。
func (f *executionAPIFixture) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

// stepsPath 返回任务的步骤列表路径。
func (f *executionAPIFixture) stepsPath(taskID string) string {
	return httpapi.PathTasks + "/" + taskID + "/steps"
}

// attemptsPath 返回步骤的尝试列表路径。
func (f *executionAPIFixture) attemptsPath(taskID, stepID string) string {
	return f.stepsPath(taskID) + "/" + stepID + "/attempts"
}

// sessionsPath 返回尝试的会话列表路径。
func (f *executionAPIFixture) sessionsPath(taskID, stepID, attemptID string) string {
	return f.attemptsPath(taskID, stepID) + "/" + attemptID + "/sessions"
}

// sessionPath 返回会话详情路径。
func (f *executionAPIFixture) sessionPath(taskID, sessionID string) string {
	return httpapi.PathTasks + "/" + taskID + "/sessions/" + sessionID
}

// TestListStepsReturnsPaginatedRecords 校验步骤列表返回统一分页信封。
func TestListStepsReturnsPaginatedRecords(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	recorder := fixture.get(t, fixture.stepsPath(fixture.task.ID.String()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("步骤列表应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.StepListResponse
	decodeBody(t, recorder, &response)
	if response.Total != 1 || len(response.Records) != 1 {
		t.Fatalf("步骤列表数量不符: %+v", response)
	}
	if response.Current != 1 || response.Size != app.DefaultTaskPageSize {
		t.Fatalf("分页回显不符: current=%d size=%d", response.Current, response.Size)
	}
	step := response.Records[0]
	if step.ID != fixture.step.ID.String() {
		t.Fatalf("步骤标识不符: %s", step.ID)
	}
	if step.TaskID != fixture.task.ID.String() {
		t.Fatalf("任务归属不符: %s", step.TaskID)
	}
	if step.Status != string(domain.StepStatusPending) {
		t.Fatalf("新建步骤状态应为 pending，实际 %q", step.Status)
	}
	if step.StatusTerminal {
		t.Fatal("未执行的步骤不应标记为已结束")
	}
	if step.Sequence != 1 {
		t.Fatalf("步骤序号不符: %d", step.Sequence)
	}
}

// TestListAttemptsReturnsRecords 校验尝试列表返回真实尝试。
func TestListAttemptsReturnsRecords(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	recorder := fixture.get(
		t,
		fixture.attemptsPath(fixture.task.ID.String(), fixture.step.ID.String()),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("尝试列表应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.AttemptListResponse
	decodeBody(t, recorder, &response)
	if response.Total != 1 || len(response.Records) != 1 {
		t.Fatalf("尝试列表数量不符: %+v", response)
	}
	attempt := response.Records[0]
	if attempt.ID != fixture.attempt.ID.String() {
		t.Fatalf("尝试标识不符: %s", attempt.ID)
	}
	if attempt.TaskID != fixture.task.ID.String() {
		t.Fatalf("任务归属不符: %s", attempt.TaskID)
	}
	if attempt.StepID != fixture.step.ID.String() {
		t.Fatalf("步骤归属不符: %s", attempt.StepID)
	}
	if attempt.AttemptNumber != 1 {
		t.Fatalf("尝试序号不符: %d", attempt.AttemptNumber)
	}
	if attempt.Status != string(domain.AttemptStatusRunning) {
		t.Fatalf("尝试状态不符: %s", attempt.Status)
	}
	if attempt.ExitCode != -1 {
		t.Fatalf("未结束的尝试退出码应为 -1，实际 %d", attempt.ExitCode)
	}
}

// TestListSessionsReturnsIndependentSessionsPerRole 校验不同角色拥有独立会话。
func TestListSessionsReturnsIndependentSessionsPerRole(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	recorder := fixture.get(t, fixture.sessionsPath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
		fixture.attempt.ID.String(),
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("会话列表应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.SessionListResponse
	decodeBody(t, recorder, &response)
	if response.Total != 2 || len(response.Records) != 2 {
		t.Fatalf("同一尝试下应当有两个角色会话: %+v", response)
	}
	roles := make(map[string]string, len(response.Records))
	for _, session := range response.Records {
		roles[session.AgentRole] = session.ID
		if session.AttemptID != fixture.attempt.ID.String() {
			t.Fatalf("会话尝试归属不符: %s", session.AttemptID)
		}
		if session.StepID != fixture.step.ID.String() {
			t.Fatalf("会话步骤归属不符: %s", session.StepID)
		}
		if session.Status != string(domain.SessionStatusStarting) {
			t.Fatalf("会话状态不符: %s", session.Status)
		}
		// 归档状态与导出状态独立输出，初始都未尝试。
		if session.ArchiveStatus != string(domain.SessionArchiveNotAttempted) {
			t.Fatalf("归档状态应为未归档，实际 %q", session.ArchiveStatus)
		}
		if session.ExportStatus != string(domain.SessionExportNotAttempted) {
			t.Fatalf("导出状态应为未尝试，实际 %q", session.ExportStatus)
		}
	}
	if _, ok := roles[string(domain.RoleTypeDeveloper)]; !ok {
		t.Fatalf("缺少开发者会话: %+v", roles)
	}
	if _, ok := roles[string(domain.RoleTypeValidator)]; !ok {
		t.Fatalf("缺少验证者会话: %+v", roles)
	}
}

// TestGetSessionReturnsDetail 校验会话详情接口。
func TestGetSessionReturnsDetail(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	stepID := fixture.step.ID
	attemptID := fixture.attempt.ID
	session := apiSession(t, fixture.db, fixture.ctx, domain.SessionScope{
		TaskID:    fixture.task.ID,
		StepID:    &stepID,
		AttemptID: &attemptID,
	}, fixture.developer)

	recorder := fixture.get(
		t,
		fixture.sessionPath(fixture.task.ID.String(), session.ID.String()),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("会话详情应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.SessionResponse
	decodeBody(t, recorder, &response)
	if response.ID != session.ID.String() {
		t.Fatalf("会话标识不符: %s", response.ID)
	}
	if response.TaskID != fixture.task.ID.String() {
		t.Fatalf("任务归属不符: %s", response.TaskID)
	}
	if response.RoleID != fixture.developer.ID.String() {
		t.Fatalf("角色标识不符: %s", response.RoleID)
	}
	if response.Provider != "pi" || response.Model != "provider/model" {
		t.Fatalf("模型信息不符: %s / %s", response.Provider, response.Model)
	}
}

// TestExecutionPathsRejectUnknownTask 校验不存在的任务返回 404。
func TestExecutionPathsRejectUnknownTask(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	recorder := fixture.get(t, fixture.stepsPath(domain.NewID().String()))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的任务应返回 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var response httpapi.ErrorResponse
	decodeBody(t, recorder, &response)
	if response.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码不符: %s", response.Error.Code)
	}
}

// TestExecutionPathsDoNotLeakOtherTaskData 校验跨任务访问不泄露数据。
func TestExecutionPathsDoNotLeakOtherTaskData(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	// 用其他任务的路径读取本任务的步骤与尝试。
	recorder := fixture.get(
		t,
		fixture.attemptsPath(fixture.otherTask.ID.String(), fixture.step.ID.String()),
	)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("跨任务访问应返回 404，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	sessionRecorder := fixture.get(t, fixture.sessionPath(
		fixture.otherTask.ID.String(),
		domain.NewID().String(),
	))
	if sessionRecorder.Code != http.StatusNotFound {
		t.Fatalf(
			"跨任务会话详情应返回 404，实际 %d: %s",
			sessionRecorder.Code,
			sessionRecorder.Body.String(),
		)
	}
}

// TestExecutionPathsRejectMissingRecords 校验不存在的步骤与尝试返回 404。
func TestExecutionPathsRejectMissingRecords(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	missing := domain.NewID().String()
	for name, target := range map[string]string{
		"步骤尝试列表": fixture.attemptsPath(fixture.task.ID.String(), missing),
		"尝试会话列表": fixture.sessionsPath(
			fixture.task.ID.String(),
			fixture.step.ID.String(),
			missing,
		),
		"会话详情": fixture.sessionPath(fixture.task.ID.String(), missing),
	} {
		recorder := fixture.get(t, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

// TestExecutionListsRejectUnsupportedMethods 校验只读接口拒绝写方法。
func TestExecutionListsRejectUnsupportedMethods(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPatch} {
		request := httptest.NewRequest(
			method,
			fixture.stepsPath(fixture.task.ID.String()),
			nil,
		)
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf(
				"%s 步骤列表应返回 405，实际 %d: %s",
				method,
				recorder.Code,
				recorder.Body.String(),
			)
		}
	}
}

// TestExecutionListsValidatePaging 校验分页参数校验。
func TestExecutionListsValidatePaging(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	base := fixture.stepsPath(fixture.task.ID.String())
	for name, target := range map[string]string{
		"页号非法":  base + "?current=0",
		"页大小非法": base + "?size=0",
		"页大小超限": base + "?size=101",
	} {
		recorder := fixture.get(t, target)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

// TestExecutionListsRejectUnknownStatusFilter 校验未知状态筛选返回 400。
func TestExecutionListsRejectUnknownStatusFilter(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	recorder := fixture.get(
		t,
		fixture.stepsPath(fixture.task.ID.String())+"?status=not_a_status",
	)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("未知状态应返回 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestExecutionPathsRejectUnknownSubPaths 校验未知子路径返回 JSON 404。
func TestExecutionPathsRejectUnknownSubPaths(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	for name, target := range map[string]string{
		"未知资源": httpapi.PathTasks + "/" + fixture.task.ID.String() + "/steps/x/unknown",
		"层级过深": httpapi.PathTasks + "/" + fixture.task.ID.String() + "/steps/" +
			fixture.step.ID.String() + "/attempts/" + fixture.attempt.ID.String() +
			"/sessions/" + domain.NewID().String(),
	} {
		recorder := fixture.get(t, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
		// 必须是统一 JSON 错误，不能回退成前端页面。
		var response httpapi.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s 应返回 JSON 错误，实际: %s", name, recorder.Body.String())
		}
		if response.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码不符: %s", name, response.Error.Code)
		}
	}
}

// TestExecutionListsAreStableAndPaginated 校验分页结果稳定。
func TestExecutionListsAreStableAndPaginated(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	for index := 0; index < 3; index++ {
		apiSession(t, fixture.db, fixture.ctx, domain.SessionScope{
			TaskID:    fixture.task.ID,
			StepID:    &fixture.step.ID,
			AttemptID: &fixture.attempt.ID,
		}, fixture.developer)
	}
	base := fixture.sessionsPath(
		fixture.task.ID.String(),
		fixture.step.ID.String(),
		fixture.attempt.ID.String(),
	)
	first := fixture.get(t, base+"?current=1&size=2")
	if first.Code != http.StatusOK {
		t.Fatalf("首页应返回 200，实际 %d: %s", first.Code, first.Body.String())
	}
	var firstPage httpapi.SessionListResponse
	decodeBody(t, first, &firstPage)
	if firstPage.Total != 5 || len(firstPage.Records) != 2 {
		t.Fatalf("首页结果不符: total=%d len=%d", firstPage.Total, len(firstPage.Records))
	}
	second := fixture.get(t, base+"?current=2&size=2")
	var secondPage httpapi.SessionListResponse
	decodeBody(t, second, &secondPage)
	if len(secondPage.Records) != 2 {
		t.Fatalf("第二页长度不符: %d", len(secondPage.Records))
	}
	if firstPage.Records[0].ID == secondPage.Records[0].ID {
		t.Fatal("分页结果出现重复记录")
	}
}

// TestExecutionRecordsSurviveTaskDeletion 校验逻辑删除任务后执行记录仍不可读。
func TestExecutionRecordsSurviveTaskDeletion(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	tasks, err := app.NewTaskService(app.TaskServiceOptions{Store: fixture.db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	// 只有已结束的任务允许逻辑删除，因此先把任务收敛为 cancelled。
	task, err := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if err := task.CancelImmediately(time.Now().UTC()); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if err := fixture.db.UpdateTask(fixture.ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	if err := tasks.DeleteTask(fixture.ctx, fixture.task.ID.String()); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	recorder := fixture.get(t, fixture.stepsPath(fixture.task.ID.String()))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("已删除任务的执行记录应返回 404，实际 %d", recorder.Code)
	}
	// 历史记录仍然保留在数据库中，只是不再通过已删除任务读取。
	page, err := fixture.db.ListStepPage(fixture.ctx, storage.StepFilter{TaskID: fixture.task.ID})
	if err != nil {
		t.Fatalf("查询步骤失败: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("逻辑删除不应删除历史步骤，实际数量: %d", page.Total)
	}
}

// TestExecutionAPIRequiresService 校验服务缺失时返回 503 而不是 404。
func TestExecutionAPIRequiresService(t *testing.T) {
	t.Parallel()
	handler := httpapi.NewHandler(httpapi.Deps{})
	request := httptest.NewRequest(
		http.MethodGet,
		httpapi.PathTasks+"/"+domain.NewID().String()+"/steps",
		nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("服务缺失应返回 503，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestUnknownAPIPathReturnsJSONError 校验 /api 下未知路径仍是 JSON 错误。
func TestUnknownAPIPathReturnsJSONError(t *testing.T) {
	t.Parallel()
	fixture := newExecutionAPIFixture(t)
	// /api 保留路径空间里的未知路径必须是 JSON 404，不能回退成页面。
	for name, target := range map[string]string{
		"未知资源段": httpapi.PathTasks + "/" + fixture.task.ID.String() + "/unknown",
		"未知顶层":  "/api/v1/unknown",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf(
				"%s 应返回 404，实际 %d: %s",
				name,
				recorder.Code,
				recorder.Body.String(),
			)
		}
		var response httpapi.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s 应返回 JSON 错误: %s", name, recorder.Body.String())
		}
		if response.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码不符: %s", name, response.Error.Code)
		}
	}
}
