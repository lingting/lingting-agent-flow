package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/testrunner"
)

// newHandler 构造被测 API 处理器；store 为 nil 时健康检查应报告数据库不可用。
func newHandler(store storage.Store) http.Handler {
	return httpapi.NewHandler(httpapi.Deps{
		Health: app.NewHealthService(store),
		SystemInfo: func() app.SystemInfo {
			return app.SystemInfo{
				Version:   "1.2.3",
				Commit:    "abc1234",
				BuildDate: "2025-01-01T00:00:00Z",
				GoVersion: "go1.24.5",
				Platform:  "windows/amd64",
			}
		},
	})
}

// do 执行一次请求并返回响应记录器。
func do(t *testrunner.Context, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// decodeBody 解析响应体 JSON。
func decodeBody(t *testrunner.Context, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("解析响应体失败: %v，响应内容: %s", err, recorder.Body.String())
	}
}

func runHealthReportsDegradedWithoutStore(t *testrunner.Context) {
	handler := newHandler(nil)
	recorder := do(t, handler, http.MethodGet, httpapi.PathHealth)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("存储不可用时健康检查应返回 503，实际 %d", recorder.Code)
	}
	var body app.Health
	decodeBody(t, recorder, &body)
	if body.Status != app.HealthStatusDegraded {
		t.Fatalf("总体状态应为 degraded，实际 %q", body.Status)
	}
	if len(body.Checks) != 2 {
		t.Fatalf("检查项数量应为 2，实际 %d", len(body.Checks))
	}
	if body.Checks[0].Name != app.CheckNameHTTPAPI || body.Checks[0].Status != app.CheckStatusPass {
		t.Fatalf("HTTP API 检查项应通过，实际 %+v", body.Checks[0])
	}
	if body.Checks[1].Name != app.CheckNameDatabase || body.Checks[1].Status != app.CheckStatusFail {
		t.Fatalf("数据库检查项应失败，实际 %+v", body.Checks[1])
	}
	if body.CheckedAt.IsZero() {
		t.Fatal("检查时间不应为零值")
	}
}

func runHealthSetsJSONContentType(t *testrunner.Context) {
	recorder := do(t, newHandler(nil), http.MethodGet, httpapi.PathHealth)

	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type 应为 JSON，实际 %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control 应为 no-store，实际 %q", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got == "" {
		t.Fatal("响应应包含 X-Request-Id 头")
	}
}

func runSystemInfoReturnsBuildFields(t *testrunner.Context) {
	recorder := do(t, newHandler(nil), http.MethodGet, httpapi.PathSystemInfo)

	if recorder.Code != http.StatusOK {
		t.Fatalf("系统信息应返回 200，实际 %d", recorder.Code)
	}
	var body app.SystemInfo
	decodeBody(t, recorder, &body)
	if body.Version != "1.2.3" || body.Commit != "abc1234" || body.Platform == "" {
		t.Fatalf("系统信息字段不完整: %+v", body)
	}
}

func runUnknownAPIPathReturnsJSONNotFound(t *testrunner.Context) {
	recorder := do(t, newHandler(nil), http.MethodGet, httpapi.APIPrefix+"/nope")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知 API 路径应返回 404，实际 %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("错误响应应为 JSON，实际 %q", got)
	}
	var body httpapi.ErrorResponse
	decodeBody(t, recorder, &body)
	if body.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码应为 %q，实际 %q", httpapi.CodeNotFound, body.Error.Code)
	}
	if body.Error.Message == "" {
		t.Fatal("错误响应应包含面向用户的说明")
	}
	if body.Error.RequestID == "" {
		t.Fatal("错误响应应包含请求标识")
	}
}

func runUnsupportedMethodReturnsJSONError(t *testrunner.Context) {
	recorder := do(t, newHandler(nil), http.MethodPost, httpapi.PathHealth)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应返回 405，实际 %d", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow 头应为 \"GET, HEAD\"，实际 %q", got)
	}
	var body httpapi.ErrorResponse
	decodeBody(t, recorder, &body)
	if body.Error.Code != httpapi.CodeMethodNotAllowed {
		t.Fatalf("错误代码应为 %q，实际 %q", httpapi.CodeMethodNotAllowed, body.Error.Code)
	}
}

func runAPIPathBoundary(t *testrunner.Context) {
	cases := map[string]bool{
		"/api":           true,
		"/api/":          true,
		"/api/health":    true,
		"/api/v1/tasks":  true,
		"/apiary":        false,
		"/apix":          false,
		"/ap":            false,
		"/":              false,
		"/tasks":         false,
		"/assets/app.js": false,
	}
	for path, want := range cases {
		if got := httpapi.IsAPIPath(path); got != want {
			t.Fatalf("IsAPIPath(%q) 应为 %v，实际 %v", path, want, got)
		}
	}
}

func runAPIRootReturnsJSONError(t *testrunner.Context) {
	for _, path := range []string{"/api", "/api/", "/api/v1"} {
		recorder := do(t, newHandler(nil), http.MethodGet, path)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", path, recorder.Code)
		}
		if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s 应返回 JSON，实际 %q", path, recorder.Header().Get("Content-Type"))
		}
		var body httpapi.ErrorResponse
		decodeBody(t, recorder, &body)
		if body.Error.Code != httpapi.CodeNotFound {
			t.Fatalf("%s 错误代码应为 %q，实际 %q", path, httpapi.CodeNotFound, body.Error.Code)
		}
	}
}

func runHealthWithoutServiceReportsUnavailable(t *testrunner.Context) {
	handler := httpapi.NewHandler(httpapi.Deps{
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
	})
	recorder := do(t, handler, http.MethodGet, httpapi.PathHealth)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("缺少健康检查服务时应返回 503，实际 %d", recorder.Code)
	}
	var body httpapi.ErrorResponse
	decodeBody(t, recorder, &body)
	if body.Error.Code != httpapi.CodeInternal {
		t.Fatalf("错误代码应为 %q，实际 %q", httpapi.CodeInternal, body.Error.Code)
	}
}

func runTrailingSlashIsNotFound(t *testrunner.Context) {
	recorder := do(t, newHandler(nil), http.MethodGet, httpapi.APIPrefix+"/unknown/")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知路径应返回 404，实际 %d", recorder.Code)
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatal("未知 API 路径必须返回 JSON 错误，而不是前端页面")
	}
}
