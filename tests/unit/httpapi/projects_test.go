package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// projectAPIFixture 是项目接口测试的运行环境。
type projectAPIFixture struct {
	handler http.Handler
	db      *sqlite.DB
	service *app.ProjectService
}

// newProjectAPIFixture 打开真实数据库并构造项目服务与处理器。
func newProjectAPIFixture(t *testing.T) *projectAPIFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(testkit.Dir(t), "agentflow.db")})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	service, err := app.NewProjectService(app.ProjectServiceOptions{Store: db, Logger: logger.Slog()})
	if err != nil {
		t.Fatalf("构造项目服务失败: %v", err)
	}
	return &projectAPIFixture{
		db:      db,
		service: service,
		handler: httpapi.NewHandler(httpapi.Deps{Projects: service, Logger: logger.Slog()}),
	}
}

// send 发送一次请求并返回响应记录器。
func (f *projectAPIFixture) send(t *testing.T, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else if raw, ok := body.(string); ok {
		reader = bytes.NewReader([]byte(raw))
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

// createBody 返回一份合法的建项目请求体。
func projectCreateBody() map[string]any {
	return map[string]any{
		"id":             "demo-service",
		"name":           "示例服务",
		"repository_url": "ssh://git.example.com/team/repo",
		"base_branch":    "main",
		"environment":    "go",
		"caches":         []string{"go-mod"},
	}
}

// decodeProject 解析项目响应体。
func decodeProject(t *testing.T, recorder *httptest.ResponseRecorder) httpapi.ProjectResponse {
	t.Helper()
	var response httpapi.ProjectResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析项目响应失败: %v: %s", err, recorder.Body.String())
	}
	return response
}

// TestProjectAPILifecycle 校验项目接口在真实 HTTP 处理器上的完整生命周期。
func TestProjectAPILifecycle(t *testing.T) {
	t.Parallel()
	fixture := newProjectAPIFixture(t)

	created := fixture.send(t, http.MethodPost, httpapi.PathProjects, projectCreateBody())
	if created.Code != http.StatusCreated {
		t.Fatalf("创建项目应返回 201，实际 %d: %s", created.Code, created.Body.String())
	}
	project := decodeProject(t, created)
	if project.ID != "demo-service" || project.Name != "示例服务" {
		t.Fatalf("创建结果不符: %+v", project)
	}
	if project.Environment != "go" || project.BaseBranch != "main" {
		t.Fatalf("环境或基础分支不符: %+v", project)
	}
	if len(project.Caches) != 1 || project.Caches[0] != "go-mod" {
		t.Fatalf("缓存类型不符: %+v", project.Caches)
	}

	listed := fixture.send(t, http.MethodGet, httpapi.PathProjects, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("列表应返回 200，实际 %d", listed.Code)
	}
	var listBody httpapi.ProjectListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if listBody.Total != 1 || len(listBody.Records) != 1 {
		t.Fatalf("列表内容不符: %+v", listBody)
	}

	detail := fixture.send(t, http.MethodGet, httpapi.PathProjects+"/demo-service", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("详情应返回 200，实际 %d", detail.Code)
	}
	if decodeProject(t, detail).ID != "demo-service" {
		t.Fatalf("详情标识不符: %s", detail.Body.String())
	}

	updated := fixture.send(t, http.MethodPut, httpapi.PathProjects+"/demo-service", map[string]any{
		"name":                "改名后的项目",
		"repository_url":      "https://git.example.com/team/other.git",
		"base_branch":         "release/1.x",
		"environment":         "node",
		"environment_version": "v0002",
		"caches":              []string{"node-npm"},
		"work_dir":            "/workspace/other",
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("更新应返回 200，实际 %d: %s", updated.Code, updated.Body.String())
	}
	updatedProject := decodeProject(t, updated)
	if updatedProject.Name != "改名后的项目" || updatedProject.Environment != "node" {
		t.Fatalf("更新结果不符: %+v", updatedProject)
	}
	if updatedProject.WorkDir != "/workspace/other" {
		t.Fatalf("工作目录未更新: %s", updatedProject.WorkDir)
	}

	removed := fixture.send(t, http.MethodDelete, httpapi.PathProjects+"/demo-service", nil)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("删除应返回 204，实际 %d: %s", removed.Code, removed.Body.String())
	}
	if body := removed.Body.String(); body != "" {
		t.Fatalf("删除响应不应包含响应体: %q", body)
	}

	missing := fixture.send(t, http.MethodGet, httpapi.PathProjects+"/demo-service", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("已删除项目应返回 404，实际 %d", missing.Code)
	}
}

// TestProjectAPICreateRejectsInvalidInput 校验参数错误返回 400。
func TestProjectAPICreateRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	fixture := newProjectAPIFixture(t)
	cases := map[string]func(map[string]any){
		"标识为空":   func(body map[string]any) { body["id"] = "  " },
		"名称为空":   func(body map[string]any) { body["name"] = "  " },
		"仓库地址为空": func(body map[string]any) { body["repository_url"] = "" },
		"基础分支为空": func(body map[string]any) { body["base_branch"] = "" },
		"环境为空":   func(body map[string]any) { body["environment"] = "" },
		"版本标签非法": func(body map[string]any) { body["environment_version"] = "1" },
		"仓库带凭据":  func(body map[string]any) { body["repository_url"] = "https://u:p@git.example.com/r.git" },
	}
	for name, mutate := range cases {
		body := projectCreateBody()
		mutate(body)
		recorder := fixture.send(t, http.MethodPost, httpapi.PathProjects, body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: 应返回 400，实际 %d: %s", name, recorder.Code, recorder.Body.String())
		}
		assertErrorCode(t, recorder, httpapi.CodeInvalidRequest)
	}
}

// TestProjectAPICreateRejectsUnknownField 校验未知字段被拒绝。
func TestProjectAPICreateRejectsUnknownField(t *testing.T) {
	t.Parallel()
	fixture := newProjectAPIFixture(t)
	body := projectCreateBody()
	body["deleted_at"] = 1
	recorder := fixture.send(t, http.MethodPost, httpapi.PathProjects, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("客户端不应能指定删除标记，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestProjectAPICreateRejectsDuplicateID 校验重复标识返回 409。
func TestProjectAPICreateRejectsDuplicateID(t *testing.T) {
	t.Parallel()
	fixture := newProjectAPIFixture(t)
	if recorder := fixture.send(
		t,
		http.MethodPost,
		httpapi.PathProjects,
		projectCreateBody(),
	); recorder.Code != http.StatusCreated {
		t.Fatalf("首次创建应返回 201，实际 %d", recorder.Code)
	}
	recorder := fixture.send(t, http.MethodPost, httpapi.PathProjects, projectCreateBody())
	if recorder.Code != http.StatusConflict {
		t.Fatalf("重复标识应返回 409，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	assertErrorCode(t, recorder, httpapi.CodeConflict)
}

// TestProjectAPIUnknownProjectReturnsNotFound 校验未知项目返回 404。
func TestProjectAPIUnknownProjectReturnsNotFound(t *testing.T) {
	t.Parallel()
	fixture := newProjectAPIFixture(t)
	for _, target := range []string{
		httpapi.PathProjects + "/missing",
	} {
		record := fixture.send(t, http.MethodGet, target, nil)
		if record.Code != http.StatusNotFound {
			t.Fatalf("%s: 应返回 404，实际 %d", target, record.Code)
		}
		assertErrorCode(t, record, httpapi.CodeNotFound)
	}
	update := fixture.send(t, http.MethodPut, httpapi.PathProjects+"/missing", map[string]any{
		"name":           "示例",
		"repository_url": "ssh://git.example.com/team/repo",
		"base_branch":    "main",
		"environment":    "go",
	})
	if update.Code != http.StatusNotFound {
		t.Fatalf("更新未知项目应返回 404，实际 %d", update.Code)
	}
	remove := fixture.send(t, http.MethodDelete, httpapi.PathProjects+"/missing", nil)
	if remove.Code != http.StatusNotFound {
		t.Fatalf("删除未知项目应返回 404，实际 %d", remove.Code)
	}
}

// TestProjectAPIRejectsUnsupportedMethods 校验不支持的方法返回 405。
func TestProjectAPIRejectsUnsupportedMethods(t *testing.T) {
	t.Parallel()
	fixture := newProjectAPIFixture(t)
	if recorder := fixture.send(
		t,
		http.MethodPost,
		httpapi.PathProjects,
		projectCreateBody(),
	); recorder.Code != http.StatusCreated {
		t.Fatalf("首次创建应返回 201，实际 %d", recorder.Code)
	}
	cases := []struct {
		method   string
		target   string
		expected string
	}{
		{method: http.MethodDelete, target: httpapi.PathProjects, expected: "GET, HEAD, POST"},
		{method: http.MethodPut, target: httpapi.PathProjects, expected: "GET, HEAD, POST"},
		{method: http.MethodPatch, target: httpapi.PathProjects + "/demo-service", expected: "GET, HEAD, PUT, DELETE"},
		{method: http.MethodPost, target: httpapi.PathProjects + "/demo-service", expected: "GET, HEAD, PUT, DELETE"},
	}
	for _, item := range cases {
		recorder := fixture.send(t, item.method, item.target, nil)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: 应返回 405，实际 %d", item.method, item.target, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); allow != item.expected {
			t.Fatalf("%s %s: Allow 头不符: %q", item.method, item.target, allow)
		}
	}
}

// TestProjectAPIPathShapeRejectsNestedSegments 校验多余路径段返回 404。
func TestProjectAPIPathShapeRejectsNestedSegments(t *testing.T) {
	t.Parallel()
	fixture := newProjectAPIFixture(t)
	for _, target := range []string{
		httpapi.PathProjects + "/",
		httpapi.PathProjects + "/demo-service/extra",
	} {
		recorder := fixture.send(t, http.MethodGet, target, nil)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: 应返回 404，实际 %d", target, recorder.Code)
		}
	}
}

// TestProjectAPIWithoutServiceReportsUnavailable 校验未装配服务时返回 503。
func TestProjectAPIWithoutServiceReportsUnavailable(t *testing.T) {
	t.Parallel()
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	handler := httpapi.NewHandler(httpapi.Deps{Logger: logger.Slog()})
	request := httptest.NewRequest(http.MethodGet, httpapi.PathProjects, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配项目服务时应返回 503，实际 %d", recorder.Code)
	}
}

// TestProjectAPITaskCreationRequiresProject 校验 HTTP 建任务接口要求真实项目。
func TestProjectAPITaskCreationRequiresProject(t *testing.T) {
	t.Parallel()
	fixture := newTaskAPIFixture(t)

	body := fixture.createBody("实现登录", "为登录页接入真实接口")
	delete(body, "project_id")
	recorder := fixture.send(t, http.MethodPost, httpapi.PathTasks, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺少项目应返回 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}

	body = fixture.createBody("实现登录", "为登录页接入真实接口")
	body["project_id"] = "missing-project"
	recorder = fixture.send(t, http.MethodPost, httpapi.PathTasks, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("未知项目应返回 400，实际 %d: %s", recorder.Code, recorder.Body.String())
	}

	body = fixture.createBody("实现登录", "为登录页接入真实接口")
	recorder = fixture.send(t, http.MethodPost, httpapi.PathTasks, body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("合法项目应创建成功，实际 %d: %s", recorder.Code, recorder.Body.String())
	}
	var task httpapi.TaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &task); err != nil {
		t.Fatalf("解析任务响应失败: %v", err)
	}
	if task.ProjectID != fixture.project.ID {
		t.Fatalf("任务应记录项目标识，实际: %q", task.ProjectID)
	}
}

// assertErrorCode 校验统一错误响应中的错误代码。
func assertErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, expected string) {
	t.Helper()
	var response httpapi.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析错误响应失败: %v: %s", err, recorder.Body.String())
	}
	if response.Error.Code != expected {
		t.Fatalf("错误代码不符: 期望 %s，实际 %s", expected, response.Error.Code)
	}
}
