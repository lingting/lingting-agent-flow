package webui_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lingting/agentflow/internal/webui"
)

// indexHTML 是测试用的前端入口内容。
const indexHTML = `<!doctype html><html><body><div id="root"></div><script src="/assets/index-abc.js"></script></body></html>`

// newFS 构造一份模拟的前端构建产物。
func newFS() fstest.MapFS {
	return fstest.MapFS{
		"assets/index-abc.css": &fstest.MapFile{Data: []byte("body{margin:0}")},
		"assets/index-abc.js":  &fstest.MapFile{Data: []byte("console.log(1)")},
		"favicon.svg":          &fstest.MapFile{Data: []byte("<svg/>")},
		"index.html":           &fstest.MapFile{Data: []byte(indexHTML)},
	}
}

// newHandler 构造被测静态资源处理器。
func newHandler(t *testing.T, files fstest.MapFS) http.Handler {
	t.Helper()
	handler, err := webui.NewHandler(files, nil)
	if err != nil {
		t.Fatalf("构造静态资源处理器失败: %v", err)
	}
	return handler
}

// do 执行一次请求并返回响应记录器。
func do(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// TestServeIndexAtRoot 校验根路径返回前端入口。
func TestServeIndexAtRoot(t *testing.T) {
	t.Parallel()
	recorder := do(newHandler(t, newFS()), http.MethodGet, "/")

	if recorder.Code != http.StatusOK {
		t.Fatalf("根路径应返回 200，实际 %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("入口内容类型应为 HTML，实际 %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("入口缓存策略应为 no-cache，实际 %q", got)
	}
	if !strings.Contains(recorder.Body.String(), `id="root"`) {
		t.Fatalf("入口内容不完整: %s", recorder.Body.String())
	}
}

// TestServeHashedAssets 校验带哈希的构建产物可读取且使用长期缓存。
func TestServeHashedAssets(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, newFS())
	cases := []struct {
		target      string
		contentType string
	}{
		{target: "/assets/index-abc.js", contentType: "text/javascript"},
		{target: "/assets/index-abc.css", contentType: "text/css"},
		{target: "/favicon.svg", contentType: "image/svg+xml"},
	}
	for _, item := range cases {
		recorder := do(handler, http.MethodGet, item.target)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s 应返回 200，实际 %d", item.target, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, item.contentType) {
			t.Fatalf("%s 内容类型应为 %q，实际 %q", item.target, item.contentType, got)
		}
		if recorder.Body.Len() == 0 {
			t.Fatalf("%s 响应体不应为空", item.target)
		}
	}
}

// TestHashedAssetUsesImmutableCache 校验 assets/ 下的资源使用不可变缓存策略。
func TestHashedAssetUsesImmutableCache(t *testing.T) {
	t.Parallel()
	recorder := do(newHandler(t, newFS()), http.MethodGet, "/assets/index-abc.js")

	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("带哈希资源应使用 immutable 缓存，实际 %q", got)
	}
}

// TestSPAFallbackForClientRoutes 校验未知页面路由回退到入口页面。
func TestSPAFallbackForClientRoutes(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"/overview", "/tasks", "/settings", "/deep/nested/route"} {
		recorder := do(newHandler(t, newFS()), http.MethodGet, target)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s 应回退到入口页面，实际 %d", target, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), `id="root"`) {
			t.Fatalf("%s 未返回入口页面内容", target)
		}
	}
}

// TestMissingAssetReturns404 校验缺失的静态资源返回 404，不伪装成 HTML。
func TestMissingAssetReturns404(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"/assets/missing.js", "/missing.css", "/data.json"} {
		recorder := do(newHandler(t, newFS()), http.MethodGet, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", target, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), `id="root"`) {
			t.Fatalf("%s 不应返回入口页面", target)
		}
	}
}

// TestAPIPathIsNeverIndex 校验 /api/ 路径不会回退到入口页面。
func TestAPIPathIsNeverIndex(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, newFS())
	for _, target := range []string{"/api", "/api/v1/health", "/api/v1/unknown"} {
		recorder := do(handler, http.MethodGet, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", target, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), `id="root"`) {
			t.Fatalf("%s 不应返回入口页面", target)
		}
	}
}

// TestMissingAssetDirectoryReturns404 校验 assets 目录下缺失的无扩展名文件也返回 404。
func TestMissingAssetDirectoryReturns404(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"/assets/missing", "/assets/", "/assets/deep/missing.js"} {
		recorder := do(newHandler(t, newFS()), http.MethodGet, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，实际 %d", target, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), `id="root"`) {
			t.Fatalf("%s 不应返回入口页面", target)
		}
	}
}

// TestSimilarPrefixIsPage 校验 /apiary 这类相似前缀不被当作 API 路径。
func TestSimilarPrefixIsPage(t *testing.T) {
	t.Parallel()
	handler := newHandler(t, newFS())
	for _, target := range []string{"/apiary", "/apix", "/ap"} {
		recorder := do(handler, http.MethodGet, target)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s 应按页面路径处理并返回 200，实际 %d", target, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), `id="root"`) {
			t.Fatalf("%s 应返回入口页面", target)
		}
	}
}

// TestUnsupportedMethodIsRejected 校验非 GET/HEAD 请求被拒绝。
func TestUnsupportedMethodIsRejected(t *testing.T) {
	t.Parallel()
	recorder := do(newHandler(t, newFS()), http.MethodPost, "/")

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应返回 405，实际 %d", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow 头应为 \"GET, HEAD\"，实际 %q", got)
	}
}

// TestHeadRequestHasNoBody 校验 HEAD 请求返回相同的头但不带响应体。
func TestHeadRequestHasNoBody(t *testing.T) {
	t.Parallel()
	recorder := do(newHandler(t, newFS()), http.MethodHead, "/")

	if recorder.Code != http.StatusOK {
		t.Fatalf("HEAD 应返回 200，实际 %d", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("HEAD 响应体应为空，实际 %d 字节", recorder.Body.Len())
	}
}

// TestMissingIndexFailsFast 校验缺少入口文件时构造失败，而不是返回空白页。
func TestMissingIndexFailsFast(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{"assets/index-abc.js": &fstest.MapFile{Data: []byte("1")}}
	handler, err := webui.NewHandler(files, nil)

	if handler != nil {
		t.Fatal("缺少 index.html 时不应返回处理器")
	}
	if !errors.Is(err, webui.ErrNoIndex) {
		t.Fatalf("应返回 ErrNoIndex，实际 %v", err)
	}
}

// TestEmbeddedFSHasNoIndexInSourceCheckout 校验源码检出状态下嵌入资源不含入口文件。
//
// 该断言说明：仅凭 go:embed 的占位文件不能提供页面，必须先执行前端生产构建。
func TestEmbeddedFSHasNoIndexInSourceCheckout(t *testing.T) {
	t.Parallel()
	if webui.HasIndex(webui.FS()) {
		t.Skip("当前工作区已包含前端构建产物，跳过源码检出断言")
	}
	if _, err := webui.NewHandler(nil, nil); !errors.Is(err, webui.ErrNoIndex) {
		t.Fatalf("缺少构建产物时应返回 ErrNoIndex，实际 %v", err)
	}
}

// TestDiagnosticHandlerExplainsMissingBuild 校验诊断页明确说明原因，且不返回成功状态。
func TestDiagnosticHandlerExplainsMissingBuild(t *testing.T) {
	t.Parallel()
	handler := webui.NewDiagnosticHandler(webui.ErrNoIndex, nil)
	recorder := do(handler, http.MethodGet, "/overview")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("诊断页应返回 503，实际 %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "前端资源未就绪") || !strings.Contains(body, "make web-build") {
		t.Fatalf("诊断页应说明原因与恢复方式: %s", body)
	}
}
