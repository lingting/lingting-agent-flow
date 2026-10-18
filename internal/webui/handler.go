package webui

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// 静态资源缓存策略。
const (
	// hashedCacheControl 用于带内容哈希的构建产物：内容变化必然换文件名。
	hashedCacheControl = "public, max-age=31536000, immutable"
	// indexCacheControl 用于入口文件：必须让浏览器每次校验，避免继续使用旧入口。
	indexCacheControl = "no-cache"
	// defaultCacheControl 用于其他未带哈希的静态文件。
	defaultCacheControl = "public, max-age=3600"
)

// assetsPrefix 是构建产物中带哈希资源的目录前缀。
const assetsPrefix = "assets/"

// Handler 提供嵌入的前端静态资源与 SPA 路由回退。
//
// 它只处理页面与静态资源：`/api/` 下的请求一律返回 404，不会回退到 index.html，
// 避免未知 API 请求被伪装成成功的前端页面。
type Handler struct {
	files  fs.FS
	index  []byte
	logger *slog.Logger
}

// NewHandler 基于给定文件系统构造静态资源处理器。
//
// filesystem 为空时使用嵌入的前端资源；缺少 index.html 时返回 ErrNoIndex，
// 调用方据此决定是拒绝启动还是给出诊断信息。
func NewHandler(filesystem fs.FS, logger *slog.Logger) (*Handler, error) {
	if filesystem == nil {
		filesystem = FS()
	}
	if !HasIndex(filesystem) {
		return nil, ErrNoIndex
	}
	index, err := fs.ReadFile(filesystem, indexFile)
	if err != nil {
		return nil, errors.Join(ErrNoIndex, err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{files: filesystem, index: index, logger: logger}, nil
}

// ServeHTTP 实现 http.Handler。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "只支持 GET 与 HEAD 请求", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if isAPIPath(name) {
		http.NotFound(w, r)
		return
	}
	if name == "" || name == "." {
		h.serveIndex(w, r)
		return
	}
	file, err := fs.ReadFile(h.files, name)
	if err == nil {
		h.serveFile(w, r, name, file)
		return
	}
	if !errors.Is(err, fs.ErrNotExist) {
		h.logger.ErrorContext(r.Context(), "读取静态资源失败", slog.String("path", name), slog.String("error", err.Error()))
		http.Error(w, "读取静态资源失败", http.StatusInternalServerError)
		return
	}
	// 缺失的静态资源必须返回 404：把 .js、.css 的缺失伪装成 HTML 会让浏览器
	// 静默拿到错误内容，问题被推迟到运行时才暴露。
	if isAssetPath(name) {
		http.NotFound(w, r)
		return
	}
	h.serveIndex(w, r)
}

// serveIndex 返回前端入口页面，用于根路径与客户端路由回退。
func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", indexCacheControl)
	h.writeContent(w, r, "text/html; charset=utf-8", h.index)
}

// serveFile 返回构建产物中的静态文件。
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string, content []byte) {
	w.Header().Set("Cache-Control", cacheControlFor(name))
	h.writeContent(w, r, contentTypeFor(name, content), content)
}

// writeContent 写出响应内容。
//
// 嵌入资源的修改时间不可靠，因此不提供 Last-Modified / ETag，只用 Cache-Control
// 表达缓存策略；Range 请求对构建产物没有意义，这里不实现部分内容响应。
func (h *Handler) writeContent(w http.ResponseWriter, r *http.Request, contentType string, content []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(content); err != nil {
		h.logger.DebugContext(r.Context(), "写出静态资源失败", slog.String("error", err.Error()))
	}
}

// isAPIPath 判断请求路径是否属于 API 命名空间。
func isAPIPath(name string) bool {
	return name == "api" || strings.HasPrefix(name, "api/")
}

// isAssetPath 判断路径是否指向一个具体文件而非前端页面路由。
//
// 带扩展名的路径视为静态资源：页面路由不会出现扩展名，因此缺失时直接 404。
func isAssetPath(name string) bool {
	return path.Ext(name) != ""
}

// cacheControlFor 返回路径对应的缓存策略。
func cacheControlFor(name string) string {
	switch {
	case name == indexFile:
		return indexCacheControl
	case strings.HasPrefix(name, assetsPrefix):
		return hashedCacheControl
	default:
		return defaultCacheControl
	}
}

// contentTypeFor 按扩展名推导内容类型，未知扩展名时回退到内容嗅探。
func contentTypeFor(name string, content []byte) string {
	if ext := path.Ext(name); ext != "" {
		if detected := mimeTypeByExtension(ext); detected != "" {
			return detected
		}
	}
	return http.DetectContentType(content)
}
