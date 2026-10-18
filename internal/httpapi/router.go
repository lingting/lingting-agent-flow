package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/lingting/agentflow/internal/app"
)

// APIPrefix 是全部 API 路径的公共前缀。
//
// 前端只使用相对路径（例如 /api/v1/health），开发环境由 Vite+ 代理转发，
// 生产环境由同一个 Go 进程提供，因此路径在两套环境里保持一致。
const APIPrefix = "/api/v1"

// 具体接口路径，供路由注册与文档引用共用。
const (
	// PathHealth 是健康检查路径。
	PathHealth = APIPrefix + "/health"
	// PathSystemInfo 是系统信息路径。
	PathSystemInfo = APIPrefix + "/system/info"
)

// Deps 是构造 API 处理器所需的依赖。
type Deps struct {
	// Health 提供健康检查结果，必填。
	Health *app.HealthService
	// SystemInfo 返回系统信息，必填。
	SystemInfo func() app.SystemInfo
	// Logger 是日志器，为空时使用 slog.Default。
	Logger *slog.Logger
}

// NewHandler 构造挂在根路径上的 API 处理器。
//
// 未注册的 /api/ 路径一律返回统一 JSON 404，不会落到前端的 SPA 回退上。
func NewHandler(deps Deps) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	router := &router{
		health:     deps.Health,
		systemInfo: deps.SystemInfo,
		logger:     logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(PathHealth, router.healthHandler)
	mux.HandleFunc(PathSystemInfo, router.systemInfoHandler)
	mux.HandleFunc("/", router.notFoundHandler)
	return withRequestID(mux)
}

// router 持有 API 处理器依赖。
type router struct {
	health     *app.HealthService
	systemInfo func() app.SystemInfo
	logger     *slog.Logger
}

// healthHandler 处理健康检查请求。
//
// 只有整体状态为 ok 时返回 200；存在不可用依赖时返回 503，前端据此显示未连接。
func (rt *router) healthHandler(w http.ResponseWriter, r *http.Request) {
	if !rt.allowGet(w, r) {
		return
	}
	health := rt.health.Check(r.Context())
	status := http.StatusOK
	if health.Status != app.HealthStatusOK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, r, rt.logger, status, health)
}

// systemInfoHandler 处理系统信息请求。
func (rt *router) systemInfoHandler(w http.ResponseWriter, r *http.Request) {
	if !rt.allowGet(w, r) {
		return
	}
	writeJSON(w, r, rt.logger, http.StatusOK, rt.systemInfo())
}

// notFoundHandler 返回统一格式的 404。
func (rt *router) notFoundHandler(w http.ResponseWriter, r *http.Request) {
	writeError(
		w,
		r,
		rt.logger,
		http.StatusNotFound,
		CodeNotFound,
		"接口不存在: "+r.URL.Path,
	)
}

// allowGet 校验请求方法，不满足时写出 405 并返回 false。
func (rt *router) allowGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	writeError(
		w,
		r,
		rt.logger,
		http.StatusMethodNotAllowed,
		CodeMethodNotAllowed,
		"该接口只支持 GET 请求",
	)
	return false
}
