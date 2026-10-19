package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// taskAPIFixture 是任务接口测试的运行环境。
type taskAPIFixture struct {
	handler   http.Handler
	db        *sqlite.DB
	service   *app.TaskService
	host      domain.ExecutionHost
	planner   domain.Role
	developer domain.Role
}

// newTaskAPIFixture 在测试工作空间中准备任务接口所需的数据与处理器。
func newTaskAPIFixture(t *testing.T) *taskAPIFixture {
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
	service, err := app.NewTaskService(app.TaskServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造任务服务失败: %v", err)
	}
	now := time.Now().UTC()
	fixture := &taskAPIFixture{db: db, service: service}
	fixture.planner = apiRole(t, db, ctx, domain.RoleTypePlanner, "规划师", now)
	fixture.developer = apiRole(t, db, ctx, domain.RoleTypeDeveloper, "开发工程师", now)
	fixture.host = apiHost(t, db, ctx, now)
	fixture.handler = httpapi.NewHandler(httpapi.Deps{
		Health:     app.NewHealthService(db),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
		Tasks:      service,
	})
	return fixture
}

// apiRole 写入一个测试角色。
func apiRole(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	roleType domain.RoleType,
	name string,
	now time.Time,
) domain.Role {
	t.Helper()
	role, err := domain.NewRole(
		name,
		roleType,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(roleType),
		now,
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	if err := db.CreateRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	return role
}

// apiHost 写入一个测试执行节点。
func apiHost(
	t *testing.T,
	db *sqlite.DB,
	ctx context.Context,
	now time.Time,
) domain.ExecutionHost {
	t.Helper()
	// 本版本只支持私钥认证，因此测试节点必须带私钥路径。
	host, err := domain.NewExecutionHost(
		"",
		"测试节点",
		"10.0.0.1",
		22,
		"deploy",
		"/srv/agentflow",
		now,
		domain.WithPrivateKey(filepath.Join(testkit.Dir(t), "id_ed25519")),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if err := db.CreateExecutionHost(ctx, host); err != nil {
		t.Fatalf("写入执行节点失败: %v", err)
	}
	return host
}

// createBody 构造一份合法的建任务请求体。
func (f *taskAPIFixture) createBody(name, description string) map[string]any {
	return map[string]any{
		"name":              name,
		"description":       description,
		"host_id":           f.host.ID.String(),
		"repository_url":    "ssh://git.example.com/team/repo",
		"base_branch":       "main",
		"environment":       "go",
		"planner_role_id":   f.planner.ID.String(),
		"developer_role_id": f.developer.ID.String(),
	}
}

// send 发送一次请求并返回响应记录器。
func (f *taskAPIFixture) send(
	t *testing.T,
	method, target string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

// createTask 通过接口创建任务并返回响应体。
func (f *taskAPIFixture) createTask(t *testing.T, name, description string) httpapi.TaskResponse {
	t.Helper()
	recorder := f.send(t, http.MethodPost, httpapi.PathTasks, f.createBody(name, description))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("创建任务应返回 201，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var created httpapi.TaskResponse
	decodeBody(t, recorder, &created)
	return created
}

// TestCreateTaskReturnsCreatedTask 校验创建接口返回完整任务对象。
func TestCreateTaskReturnsCreatedTask(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "实现登录", "为登录页接入真实接口")

	if created.ID == "" {
		t.Fatal("响应必须包含任务标识")
	}
	if created.Name != "实现登录" || created.Description != "为登录页接入真实接口" {
		t.Fatalf("名称或描述不符: %+v", created)
	}
	if created.Status != string(domain.TaskStatusQueued) {
		t.Fatalf("新建任务状态应为 queued，实际 %q", created.Status)
	}
	if created.StatusTerminal {
		t.Fatal("等待执行的任务不应标记为已结束")
	}
	if created.StatusFinished {
		t.Fatal("等待执行的任务不应标记为已停止执行")
	}
	if created.StartedAt != nil || created.FinishedAt != nil {
		t.Fatal("新建任务不应包含开始或结束时间")
	}
	if created.Error != "" {
		t.Fatalf("新建任务不应包含错误摘要: %q", created.Error)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("创建与更新时间必须返回")
	}
}

// TestCreateTaskRejectsInvalidInput 校验参数错误返回 400 与统一错误结构。
func TestCreateTaskRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	cases := map[string]func(map[string]any){
		"名称为空":   func(body map[string]any) { body["name"] = "  " },
		"描述为空":   func(body map[string]any) { body["description"] = "" },
		"项目标识非法": func(body map[string]any) { body["project_id"] = "Bad Project" },
		"执行节点缺失": func(body map[string]any) { delete(body, "host_id") },
		"未知字段":   func(body map[string]any) { body["status"] = "completed" },
		"优先级非法":  func(body map[string]any) { body["priority"] = "urgent" },
	}
	for name, mutate := range cases {
		body := fixture.createBody("任务", "描述")
		mutate(body)
		recorder := fixture.send(t, http.MethodPost, httpapi.PathTasks, body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: 应返回 400，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
		var failure httpapi.ErrorResponse
		decodeBody(t, recorder, &failure)
		if failure.Error.Code != httpapi.CodeInvalidRequest {
			t.Fatalf("%s: 错误代码应为 %q，实际 %q",
				name, httpapi.CodeInvalidRequest, failure.Error.Code)
		}
		if failure.Error.Message == "" {
			t.Fatalf("%s: 错误响应应包含说明", name)
		}
	}
}

// TestCreateTaskRejectsMalformedBody 校验非法 JSON 返回 400 而不是 500。
func TestCreateTaskRejectsMalformedBody(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	request := httptest.NewRequest(
		http.MethodPost,
		httpapi.PathTasks,
		strings.NewReader("{不是 JSON"),
	)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，实际 %d", recorder.Code)
	}
	var failure httpapi.ErrorResponse
	decodeBody(t, recorder, &failure)
	if failure.Error.Code != httpapi.CodeInvalidRequest {
		t.Fatalf("错误代码应为 %q，实际 %q", httpapi.CodeInvalidRequest, failure.Error.Code)
	}
}

// TestListTasksPaginates 校验列表接口的分页响应结构。
func TestListTasksPaginates(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	for index := 0; index < 5; index++ {
		fixture.createTask(t, "任务", "描述")
	}

	recorder := fixture.send(t, http.MethodGet, httpapi.PathTasks+"?current=2&size=2", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var page httpapi.TaskListResponse
	decodeBody(t, recorder, &page)
	if page.Total != 5 {
		t.Fatalf("总数应为 5，实际 %d", page.Total)
	}
	if page.Current != 2 || page.Size != 2 {
		t.Fatalf("分页元数据不符: current=%d size=%d", page.Current, page.Size)
	}
	if len(page.Records) != 2 {
		t.Fatalf("第二页记录数应为 2，实际 %d", len(page.Records))
	}
}

// TestListTasksDefaultsAndFilters 校验默认分页与状态筛选。
func TestListTasksDefaultsAndFilters(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "任务", "描述")
	fixture.createTask(t, "任务2", "描述")

	recorder := fixture.send(t, http.MethodGet, httpapi.PathTasks, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("列表应返回 200，实际 %d", recorder.Code)
	}
	var page httpapi.TaskListResponse
	decodeBody(t, recorder, &page)
	if page.Current != 1 || page.Size != app.DefaultTaskPageSize {
		t.Fatalf("默认分页参数不符: current=%d size=%d", page.Current, page.Size)
	}
	if page.Total != 2 {
		t.Fatalf("默认列表总数应为 2，实际 %d", page.Total)
	}

	// 取消其中一个任务，再按状态筛选。
	if recorder := fixture.send(
		t,
		http.MethodPost,
		httpapi.PathTasks+"/"+created.ID+"/cancel",
		nil,
	); recorder.Code != http.StatusOK {
		t.Fatalf("取消失败: %d %s", recorder.Code, recorder.Body.String())
	}
	filtered := fixture.send(
		t,
		http.MethodGet,
		httpapi.PathTasks+"?status=cancelled",
		nil,
	)
	var filteredPage httpapi.TaskListResponse
	decodeBody(t, filtered, &filteredPage)
	if filteredPage.Total != 1 || len(filteredPage.Records) != 1 {
		t.Fatalf("状态筛选结果不符: %+v", filteredPage)
	}
	if filteredPage.Records[0].ID != created.ID {
		t.Fatalf("筛选结果标识不符: %s", filteredPage.Records[0].ID)
	}
}

// TestListTasksRejectsInvalidPaging 校验非法分页参数返回 400。
func TestListTasksRejectsInvalidPaging(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	cases := []string{
		"?current=0",
		"?current=-1",
		"?current=abc",
		"?size=0",
		"?size=abc",
		"?size=1000",
		"?status=not-a-status",
	}
	for _, query := range cases {
		recorder := fixture.send(t, http.MethodGet, httpapi.PathTasks+query, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: 应返回 400，实际 %d: %s",
				query, recorder.Code, recorder.Body.String())
		}
		var failure httpapi.ErrorResponse
		decodeBody(t, recorder, &failure)
		if failure.Error.Code != httpapi.CodeInvalidRequest {
			t.Fatalf("%s: 错误代码应为 %q，实际 %q",
				query, httpapi.CodeInvalidRequest, failure.Error.Code)
		}
	}
}

// TestGetTaskDetail 校验详情接口返回当前状态与时间字段。
func TestGetTaskDetail(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "实现登录", "接入真实接口")

	recorder := fixture.send(t, http.MethodGet, httpapi.PathTasks+"/"+created.ID, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("详情应返回 200，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var detail httpapi.TaskResponse
	decodeBody(t, recorder, &detail)
	if detail.ID != created.ID || detail.Name != created.Name {
		t.Fatalf("详情内容不符: %+v", detail)
	}
	if detail.Status != string(domain.TaskStatusQueued) {
		t.Fatalf("详情状态不符: %s", detail.Status)
	}
	if detail.CreatedAt.IsZero() || detail.UpdatedAt.IsZero() {
		t.Fatal("详情必须包含创建与更新时间")
	}
}

// TestGetTaskDetailNotFound 校验不存在的任务返回 JSON 404。
func TestGetTaskDetailNotFound(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	recorder := fixture.send(
		t,
		http.MethodGet,
		httpapi.PathTasks+"/"+domain.NewID().String(),
		nil,
	)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的任务应返回 404，实际 %d", recorder.Code)
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatal("404 必须是 JSON，而不是前端页面")
	}
	var failure httpapi.ErrorResponse
	decodeBody(t, recorder, &failure)
	if failure.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码应为 %q，实际 %q", httpapi.CodeNotFound, failure.Error.Code)
	}
}

// TestCancelQueuedTaskSucceeds 校验等待中的任务取消后直接变为已取消。
func TestCancelQueuedTaskSucceeds(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "任务", "描述")

	recorder := fixture.send(
		t,
		http.MethodPost,
		httpapi.PathTasks+"/"+created.ID+"/cancel",
		nil,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("取消失败: %d %s", recorder.Code, recorder.Body.String())
	}
	var cancelled httpapi.TaskResponse
	decodeBody(t, recorder, &cancelled)
	if cancelled.Status != string(domain.TaskStatusCancelled) {
		t.Fatalf("等待中的任务应直接取消，实际 %s", cancelled.Status)
	}
	if !cancelled.StatusTerminal {
		t.Fatal("已取消任务应标记为已结束")
	}
	if !cancelled.StatusFinished {
		t.Fatal("已取消任务应标记为已停止执行")
	}
	if !cancelled.CancelRequested {
		t.Fatal("应记录取消请求")
	}
	if cancelled.FinishedAt == nil {
		t.Fatal("已取消任务应包含结束时间")
	}
}

// TestCancelFinishedTaskReturnsConflict 校验已结束任务取消返回 409。
func TestCancelFinishedTaskReturnsConflict(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "任务", "描述")
	if recorder := fixture.send(
		t,
		http.MethodPost,
		httpapi.PathTasks+"/"+created.ID+"/cancel",
		nil,
	); recorder.Code != http.StatusOK {
		t.Fatalf("首次取消失败: %d", recorder.Code)
	}
	recorder := fixture.send(
		t,
		http.MethodPost,
		httpapi.PathTasks+"/"+created.ID+"/cancel",
		nil,
	)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("已结束任务取消应返回 409，实际 %d: %s",
			recorder.Code, recorder.Body.String())
	}
	var failure httpapi.ErrorResponse
	decodeBody(t, recorder, &failure)
	if failure.Error.Code != httpapi.CodeConflict {
		t.Fatalf("错误代码应为 %q，实际 %q", httpapi.CodeConflict, failure.Error.Code)
	}
}

// TestCancelRunningTaskMarksCancelling 校验执行中的任务取消后只进入取消中。
func TestCancelRunningTaskMarksCancelling(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "任务", "描述")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	task, err := fixture.db.GetTask(ctx, domain.ID(created.ID))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusPreparing, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := fixture.db.UpdateTask(ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	recorder := fixture.send(
		t,
		http.MethodPost,
		httpapi.PathTasks+"/"+created.ID+"/cancel",
		nil,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("取消失败: %d %s", recorder.Code, recorder.Body.String())
	}
	var cancelling httpapi.TaskResponse
	decodeBody(t, recorder, &cancelling)
	if cancelling.Status != string(domain.TaskStatusCancelling) {
		t.Fatalf("执行中的任务应进入 cancelling，实际 %s", cancelling.Status)
	}
	if cancelling.StatusTerminal {
		t.Fatal("取消中不等于已结束")
	}
	if cancelling.StatusFinished {
		t.Fatal("取消中不等于已停止执行")
	}
	if cancelling.FinishedAt != nil {
		t.Fatal("尚未确认停止时不应有结束时间")
	}
}

// TestDeleteFinishedTaskReturnsNoContent 校验删除已结束任务返回 204。
func TestDeleteFinishedTaskReturnsNoContent(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "任务", "描述")
	// 先取消使任务进入已结束状态。
	if recorder := fixture.send(
		t,
		http.MethodPost,
		httpapi.PathTasks+"/"+created.ID+"/cancel",
		nil,
	); recorder.Code != http.StatusOK {
		t.Fatalf("取消失败: %d", recorder.Code)
	}

	recorder := fixture.send(t, http.MethodDelete, httpapi.PathTasks+"/"+created.ID, nil)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("删除应返回 204，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("204 不应返回响应体: %q", recorder.Body.String())
	}

	// 删除后详情返回 404。
	detail := fixture.send(t, http.MethodGet, httpapi.PathTasks+"/"+created.ID, nil)
	if detail.Code != http.StatusNotFound {
		t.Fatalf("已删除任务详情应返回 404，实际 %d", detail.Code)
	}
}

// TestFailedTaskIsFinishedButNotTerminal 校验 failed 的两种标记语义。
//
// 失败任务可以显式重试，因此不是终态；但它的执行已经停止，管理接口允许删除、
// 拒绝再次取消，前端必须据此禁用取消按钮。
func TestFailedTaskIsFinishedButNotTerminal(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "任务", "描述")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	task, err := fixture.db.GetTask(ctx, domain.ID(created.ID))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	now := time.Now().UTC()
	if err := task.Transition(domain.TaskStatusPreparing, now); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusFailed, now); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := task.Finish(domain.TaskResultFailed, "编译失败", now); err != nil {
		t.Fatalf("写入结论失败: %v", err)
	}
	if err := fixture.db.UpdateTask(ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	recorder := fixture.send(t, http.MethodGet, httpapi.PathTasks+"/"+created.ID, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("读取详情失败: %d %s", recorder.Code, recorder.Body.String())
	}
	var detail httpapi.TaskResponse
	decodeBody(t, recorder, &detail)
	if detail.StatusTerminal {
		t.Fatal("failed 允许显式重试，不应标记为终态")
	}
	if !detail.StatusFinished {
		t.Fatal("failed 的执行已经停止，应标记为已停止执行")
	}
	if detail.Error != "编译失败" {
		t.Fatalf("错误摘要不符: %q", detail.Error)
	}

	// 已停止执行的任务不能再次取消。
	cancelled := fixture.send(
		t,
		http.MethodPost,
		httpapi.PathTasks+"/"+created.ID+"/cancel",
		nil,
	)
	if cancelled.Code != http.StatusConflict {
		t.Fatalf("取消失败任务应返回 409，实际 %d", cancelled.Code)
	}
	// 但允许删除。
	removed := fixture.send(t, http.MethodDelete, httpapi.PathTasks+"/"+created.ID, nil)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("删除失败任务应返回 204，实际 %d: %s", removed.Code, removed.Body.String())
	}
}

// TestDeleteRunningTaskReturnsConflict 校验删除执行中的任务返回 409。
func TestDeleteRunningTaskReturnsConflict(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "任务", "描述")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	task, err := fixture.db.GetTask(ctx, domain.ID(created.ID))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if err := task.Transition(domain.TaskStatusPreparing, time.Now().UTC()); err != nil {
		t.Fatalf("状态迁移失败: %v", err)
	}
	if err := fixture.db.UpdateTask(ctx, *task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	recorder := fixture.send(t, http.MethodDelete, httpapi.PathTasks+"/"+created.ID, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("删除执行中的任务应返回 409，实际 %d: %s",
			recorder.Code, recorder.Body.String())
	}
}

// TestDeleteUnknownTaskReturnsNotFound 校验删除不存在的任务返回 404。
func TestDeleteUnknownTaskReturnsNotFound(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	recorder := fixture.send(
		t,
		http.MethodDelete,
		httpapi.PathTasks+"/"+domain.NewID().String(),
		nil,
	)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("删除不存在的任务应返回 404，实际 %d", recorder.Code)
	}
}

// TestTaskPathsRejectUnsupportedMethods 校验不支持的方法返回 405 与 Allow 头。
func TestTaskPathsRejectUnsupportedMethods(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "任务", "描述")

	cases := []struct {
		method   string
		target   string
		expected string
	}{
		{method: http.MethodDelete, target: httpapi.PathTasks, expected: "GET, HEAD, POST"},
		{method: http.MethodPut, target: httpapi.PathTasks, expected: "GET, HEAD, POST"},
		{
			method:   http.MethodPut,
			target:   httpapi.PathTasks + "/" + created.ID,
			expected: "GET, HEAD, DELETE",
		},
		{
			method:   http.MethodGet,
			target:   httpapi.PathTasks + "/" + created.ID + "/cancel",
			expected: "POST",
		},
	}
	for _, testCase := range cases {
		recorder := fixture.send(t, testCase.method, testCase.target, nil)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s 应返回 405，实际 %d",
				testCase.method, testCase.target, recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != testCase.expected {
			t.Fatalf("%s %s 的 Allow 头应为 %q，实际 %q",
				testCase.method, testCase.target, testCase.expected, got)
		}
		var failure httpapi.ErrorResponse
		decodeBody(t, recorder, &failure)
		if failure.Error.Code != httpapi.CodeMethodNotAllowed {
			t.Fatalf("错误代码应为 %q，实际 %q",
				httpapi.CodeMethodNotAllowed, failure.Error.Code)
		}
	}
}

// TestUnknownTaskSubPathReturnsJSONNotFound 校验未知子路径返回 JSON 404。
func TestUnknownTaskSubPathReturnsJSONNotFound(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	for _, target := range []string{
		httpapi.PathTasks + "/",
		httpapi.PathTasks + "/abc/unknown",
		httpapi.PathTasks + "/abc/cancel/extra",
	} {
		recorder := fixture.send(t, http.MethodGet, target, nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", target, recorder.Code)
		}
		if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s 必须返回 JSON 错误，而不是前端页面", target)
		}
		var failure httpapi.ErrorResponse
		decodeBody(t, recorder, &failure)
		if failure.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码应为 %q，实际 %q",
				target, httpapi.CodeNotFound, failure.Error.Code)
		}
	}
}

// TestTasksWithoutServiceReportsUnavailable 校验未注入任务服务时返回 503。
func TestTasksWithoutServiceReportsUnavailable(t *testing.T) {
	t.Parallel()
	handler := httpapi.NewHandler(httpapi.Deps{
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
	})
	request := httptest.NewRequest(http.MethodGet, httpapi.PathTasks, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("缺少任务服务时应返回 503，实际 %d", recorder.Code)
	}
}

// TestTaskResponseDoesNotExposeInternalFields 校验响应不泄露执行期内部字段。
func TestTaskResponseDoesNotExposeInternalFields(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)
	created := fixture.createTask(t, "任务", "描述")
	recorder := fixture.send(t, http.MethodGet, httpapi.PathTasks+"/"+created.ID, nil)
	body := recorder.Body.String()
	for _, forbidden := range []string{
		"lease_owner",
		"lease_expires_at",
		"idempotency_key",
		"container_id",
		"repository_path",
		"base_ref",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("响应不应包含内部字段 %q: %s", forbidden, body)
		}
	}
}
