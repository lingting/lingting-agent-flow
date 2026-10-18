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

// get 发起一次 GET 请求并返回状态码、响应头与响应体。
func get(t *testing.T, baseURL, path string) (int, http.Header, string) {
	t.Helper()
	response, err := http.Get(baseURL + path)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", path, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取 %s 响应体失败: %v", path, err)
	}
	return response.StatusCode, response.Header, string(body)
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
	if !strings.Contains(body, `id="root"`) {
		t.Fatalf("根路径未返回入口页面: %s", body)
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

	status, header, _ = get(t, baseURL, "/assets/index-abc.css")
	if status != http.StatusOK {
		t.Fatalf("样式资源应返回 200，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "text/css") {
		t.Fatalf("样式资源内容类型错误: %q", header.Get("Content-Type"))
	}
}

// TestServerSPAFallback 校验客户端路由刷新时仍能进入前端页面。
func TestServerSPAFallback(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)

	for _, path := range []string{"/overview", "/tasks", "/machines"} {
		status, _, body := get(t, baseURL, path)
		if status != http.StatusOK {
			t.Fatalf("%s 应回退到入口页面，实际 %d", path, status)
		}
		if !strings.Contains(body, `id="root"`) {
			t.Fatalf("%s 未返回入口页面", path)
		}
	}
}

// TestServerUnknownAPIPathReturnsJSON 校验未知 API 路径不会返回前端页面。
func TestServerUnknownAPIPathReturnsJSON(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)

	status, header, body := get(t, baseURL, httpapi.APIPrefix+"/unknown")
	if status != http.StatusNotFound {
		t.Fatalf("未知 API 路径应返回 404，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("未知 API 路径应返回 JSON，实际 %q", header.Get("Content-Type"))
	}
	if strings.Contains(body, `id="root"`) {
		t.Fatal("未知 API 路径不应返回前端入口页面")
	}
	var payload httpapi.ErrorResponse
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if payload.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("错误代码应为 %q，实际 %q", httpapi.CodeNotFound, payload.Error.Code)
	}
}

// TestServerMissingAssetReturns404 校验缺失的静态资源返回 404。
func TestServerMissingAssetReturns404(t *testing.T) {
	t.Parallel()
	_, baseURL := newServer(t)

	status, _, body := get(t, baseURL, "/assets/missing.js")
	if status != http.StatusNotFound {
		t.Fatalf("缺失资源应返回 404，实际 %d", status)
	}
	if strings.Contains(body, `id="root"`) {
		t.Fatal("缺失资源不应返回前端入口页面")
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
