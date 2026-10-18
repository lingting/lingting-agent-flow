package webui

import (
	"log/slog"
	"net/http"
	"strings"
)

// DiagnosticHandler 在前端构建产物缺失时提供可读的诊断信息。
//
// 它只用于非发布场景：开发时直接 `go run` 而不先构建前端，页面会明确说明原因，
// 而不是返回空白页或伪造一个 UI。API 请求仍然由调用方交给 API 处理器。
type DiagnosticHandler struct {
	cause  error
	logger *slog.Logger
}

// NewDiagnosticHandler 构造诊断处理器。
func NewDiagnosticHandler(cause error, logger *slog.Logger) *DiagnosticHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &DiagnosticHandler{cause: cause, logger: logger}
}

// ServeHTTP 实现 http.Handler。
func (h *DiagnosticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "只支持 GET 与 HEAD 请求", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(diagnosticPage(h.cause)))
}

// diagnosticPage 生成诊断页 HTML。
func diagnosticPage(cause error) string {
	reason := "未知原因"
	if cause != nil {
		reason = cause.Error()
	}
	var builder strings.Builder
	builder.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8">`)
	builder.WriteString(`<title>AgentFlow 前端资源未就绪</title></head><body>`)
	builder.WriteString(`<h1>前端资源未就绪</h1>`)
	builder.WriteString(`<p>该可执行文件没有包含前端构建产物，因此无法提供 Web 页面。</p>`)
	builder.WriteString(`<p>请在仓库根目录执行 <code>make web-build</code> 重新构建前端后重新编译。</p>`)
	builder.WriteString(`<p>原因：<code>`)
	builder.WriteString(escapeHTML(reason))
	builder.WriteString(`</code></p>`)
	builder.WriteString(`<p>API 仍然可用：<code>/api/v1/health</code></p>`)
	builder.WriteString(`</body></html>`)
	return builder.String()
}

// escapeHTML 转义 HTML 特殊字符，避免错误信息破坏页面结构。
func escapeHTML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}
