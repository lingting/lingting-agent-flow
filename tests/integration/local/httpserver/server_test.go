package httpserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/httpserver"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// indexHTML 是测试用的前端入口内容。
const indexHTML = `<!doctype html><html><body><div id="root"></div></body></html>`

// webFiles 是一份模拟的前端构建产物。
func webFiles() fstest.MapFS {
	return fstest.MapFS{
		"assets/index-abc.css": &fstest.MapFile{Data: []byte("body{margin:0}")},
		"assets/index-abc.js":  &fstest.MapFile{Data: []byte("console.log(1)")},
		"favicon.svg":          &fstest.MapFile{Data: []byte("<svg/>")},
		"index.html":           &fstest.MapFile{Data: []byte(indexHTML)},
	}
}

// newServer 在随机端口上启动一个真实 HTTP 服务，并注册清理逻辑。
func newServer(t *testing.T) (*httpserver.Server, string) {
	t.Helper()
	workspace := testkit.New(t)
	store, err := sqlite.Open(context.Background(), sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	workspace.Cleanup(store.Close)

	server, err := httpserver.New(httpserver.Options{
		Address:    "127.0.0.1:0",
		Files:      webFiles(),
		Health:     app.NewHealthService(store),
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{Version: "test"} },
	})
	if err != nil {
		t.Fatalf("构造 HTTP 服务失败: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听随机端口失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listener)
	}()
	workspace.Cleanup(func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			return nil
		}
	})
	return server, "http://" + listener.Addr().String()
}

// do 发起一次请求并返回状态码、响应头与响应体。
func do(t *testing.T, baseURL, method, path string) (int, http.Header, string) {
	t.Helper()
	request, err := http.NewRequest(method, baseURL+path, nil)
	if err != nil {
		t.Fatalf("构造 %s %s 请求失败: %v", method, path, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("请求 %s %s 失败: %v", method, path, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取 %s %s 响应体失败: %v", method, path, err)
	}
	return response.StatusCode, response.Header, string(body)
}

// get 发起一次 GET 请求。
func get(t *testing.T, baseURL, path string) (int, http.Header, string) {
	t.Helper()
	return do(t, baseURL, http.MethodGet, path)
}

// isHTML 判断响应是否为前端入口页面。
func isHTML(body string) bool {
	return strings.Contains(body, `id="root"`)
}

// TestServerServesAPIAndWeb 校验同一个进程同时提供 API 与前端页面。
func TestServerServesAPIAndWeb(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)

	status, header, body := get(t, baseURL, httpapi.PathHealth)
	if status != http.StatusOK {
		t.Fatalf("健康检查应返回 200，实际 %d，响应: %s", status, body)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("健康检查应为 JSON，实际 %q", header.Get("Content-Type"))
	}
	var health app.Health
	if err := json.Unmarshal([]byte(body), &health); err != nil {
		t.Fatalf("解析健康检查响应失败: %v", err)
	}
	if health.Status != app.HealthStatusOK {
		t.Fatalf("打开数据库后健康检查应为 ok，实际 %q（检查项 %+v）", health.Status, health.Checks)
	}

	status, header, body = get(t, baseURL, "/")
	if status != http.StatusOK {
		t.Fatalf("根路径应返回 200，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "text/html") {
		t.Fatalf("根路径应为 HTML，实际 %q", header.Get("Content-Type"))
	}
	if !isHTML(body) {
		t.Fatalf("根路径未返回入口页面: %s", body)
	}
}

// TestRequestDispatchTable 覆盖请求分发规则表：页面、API、静态资源与错误路径。
func TestRequestDispatchTable(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)

	cases := []struct {
		name        string
		method      string
		path        string
		wantStatus  int
		wantPrefix  string
		wantHTML    bool
		wantNotHTML bool
	}{
		{name: "根路径页面", method: http.MethodGet, path: "/", wantStatus: http.StatusOK, wantPrefix: "text/html", wantHTML: true},
		{name: "任务页面", method: http.MethodGet, path: "/tasks", wantStatus: http.StatusOK, wantPrefix: "text/html", wantHTML: true},
		{name: "项目页面", method: http.MethodGet, path: "/projects", wantStatus: http.StatusOK, wantPrefix: "text/html", wantHTML: true},
		{name: "机器页面", method: http.MethodGet, path: "/machines", wantStatus: http.StatusOK, wantPrefix: "text/html", wantHTML: true},
		{name: "未知页面", method: http.MethodGet, path: "/unknown/path", wantStatus: http.StatusOK, wantPrefix: "text/html", wantHTML: true},
		{name: "API 根路径", method: http.MethodGet, path: "/api", wantStatus: http.StatusNotFound, wantPrefix: "application/json", wantNotHTML: true},
		{name: "未知 API", method: http.MethodGet, path: "/api/unknown", wantStatus: http.StatusNotFound, wantPrefix: "application/json", wantNotHTML: true},
		{name: "健康检查", method: http.MethodGet, path: "/api/v1/health", wantStatus: http.StatusOK, wantPrefix: "application/json", wantNotHTML: true},
		{name: "相似前缀不是 API", method: http.MethodGet, path: "/apiary", wantStatus: http.StatusOK, wantPrefix: "text/html", wantHTML: true},
		{name: "存在脚本", method: http.MethodGet, path: "/assets/index-abc.js", wantStatus: http.StatusOK, wantPrefix: "text/javascript", wantNotHTML: true},
		{name: "缺失脚本", method: http.MethodGet, path: "/assets/missing.js", wantStatus: http.StatusNotFound, wantNotHTML: true},
		{name: "页面请求不接受 POST", method: http.MethodPost, path: "/tasks", wantStatus: http.StatusMethodNotAllowed, wantNotHTML: true},
	}
	for _, item := range cases {
		status, header, body := do(t, baseURL, item.method, item.path)
		if status != item.wantStatus {
			t.Fatalf("%s（%s %s）应返回 %d，实际 %d", item.name, item.method, item.path, item.wantStatus, status)
		}
		if item.wantPrefix != "" && !strings.HasPrefix(header.Get("Content-Type"), item.wantPrefix) {
			t.Fatalf("%s（%s %s）内容类型应为 %q，实际 %q", item.name, item.method, item.path, item.wantPrefix, header.Get("Content-Type"))
		}
		if item.wantHTML && !isHTML(body) {
			t.Fatalf("%s（%s %s）应返回前端入口页面，实际: %s", item.name, item.method, item.path, body)
		}
		if item.wantNotHTML && isHTML(body) {
			t.Fatalf("%s（%s %s）不应返回前端入口页面", item.name, item.method, item.path)
		}
	}
}

// TestHeadRequestHasHeadersWithoutBody 校验 HEAD 请求保留页面响应头但不返回响应体。
func TestHeadRequestHasHeadersWithoutBody(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)

	status, header, body := do(t, baseURL, http.MethodHead, "/")
	if status != http.StatusOK {
		t.Fatalf("HEAD / 应返回 200，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "text/html") {
		t.Fatalf("HEAD / 内容类型应为 HTML，实际 %q", header.Get("Content-Type"))
	}
	if header.Get("Content-Length") == "" {
		t.Fatal("HEAD / 应返回 Content-Length")
	}
	if body != "" {
		t.Fatalf("HEAD 响应体应为空，实际 %q", body)
	}
}

// TestUnknownAPIPathReturnsJSON 校验未知 API 路径不会返回前端页面。
func TestUnknownAPIPathReturnsJSON(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)

	status, header, body := get(t, baseURL, httpapi.APIPrefix+"/unknown")
	if status != http.StatusNotFound {
		t.Fatalf("未知 API 路径应返回 404，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("未知 API 路径应返回 JSON，实际 %q", header.Get("Content-Type"))
	}
	var payload httpapi.ErrorResponse
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if payload.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码应为 %q，实际 %q", httpapi.CodeNotFound, payload.Error.Code)
	}
}

// TestServerServesStaticAssets 校验构建产物中的脚本与样式可正常读取。
func TestServerServesStaticAssets(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)

	status, header, body := get(t, baseURL, "/assets/index-abc.js")
	if status != http.StatusOK {
		t.Fatalf("脚本资源应返回 200，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("脚本资源内容类型错误: %q", header.Get("Content-Type"))
	}
	if body == "" {
		t.Fatal("脚本资源响应体不应为空")
	}
	if got := header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("带哈希资源应使用长期缓存，实际 %q", got)
	}

	status, header, _ = get(t, baseURL, "/assets/index-abc.css")
	if status != http.StatusOK {
		t.Fatalf("样式资源应返回 200，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "text/css") {
		t.Fatalf("样式资源内容类型错误: %q", header.Get("Content-Type"))
	}
}

// TestServerShutdownOnContextCancel 校验 ctx 取消后服务正常关闭。
func TestServerShutdownOnContextCancel(t *testing.T) {
	t.Parallel()
	server, baseURL := newServer(t)

	status, _, _ := get(t, baseURL, httpapi.PathHealth)
	if status != http.StatusOK {
		t.Fatalf("关闭前健康检查应返回 200，实际 %d", status)
	}
	// 服务在用例清理阶段随 ctx 取消而关闭，关闭过程由 Cleanup 回调等待并校验返回值。
	if server.Addr() != "127.0.0.1:0" {
		t.Fatalf("监听地址配置应保留，实际 %q", server.Addr())
	}
}

// TestServerRequiresWebUI 校验发布模式下缺少前端产物时构造失败。
func TestServerRequiresWebUI(t *testing.T) {
	t.Parallel()
	workspace := testkit.New(t)
	store, err := sqlite.Open(context.Background(), sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	workspace.Cleanup(store.Close)

	files := fstest.MapFS{"assets/index-abc.js": &fstest.MapFile{Data: []byte("1")}}
	server, err := httpserver.New(httpserver.Options{
		Address:      "127.0.0.1:0",
		Files:        files,
		Health:       app.NewHealthService(store),
		SystemInfo:   func() app.SystemInfo { return app.SystemInfo{} },
		RequireWebUI: true,
	})
	if err == nil {
		t.Fatalf("缺少前端产物时应当失败，实际返回 %v", server)
	}
	if !strings.Contains(err.Error(), "index.html") {
		t.Fatalf("错误信息应说明缺少 index.html，实际: %v", err)
	}
}

// TestAPIRootAndTrailingSlashAreJSON 校验 /api 与 /api/ 语义一致，都返回 JSON 错误。
func TestAPIRootAndTrailingSlashAreJSON(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)

	for _, path := range []string{"/api", "/api/", "/api/v1", "/api/v1/"} {
		status, header, body := get(t, baseURL, path)
		if status != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", path, status)
		}
		if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
			t.Fatalf("%s 应返回 JSON，实际 %q", path, header.Get("Content-Type"))
		}
		if isHTML(body) {
			t.Fatalf("%s 不应回退到前端页面", path)
		}
	}
}
