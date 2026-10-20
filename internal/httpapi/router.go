package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/lingting/agentflow/internal/app"
)

// 具体接口路径，供路由注册与文档引用共用。
const (
	// PathHealth 是健康检查路径。
	PathHealth = APIPrefix + "/health"
	// PathSystemInfo 是系统信息路径。
	PathSystemInfo = APIPrefix + "/system/info"
)

// Deps 是构造 API 处理器所需的依赖。
type Deps struct {
	// Health 提供健康检查结果，为空时健康检查返回服务不可用。
	Health *app.HealthService
	// SystemInfo 返回系统信息，为空时接口返回服务不可用。
	SystemInfo func() app.SystemInfo
	// Tasks 是任务管理服务，为空时任务接口返回服务不可用。
	Tasks *app.TaskService
	// Hosts 是执行节点服务，为空时执行节点接口返回服务不可用。
	Hosts *app.ExecutionHostService
	// ExecutionRecords 是执行记录查询服务，为空时执行记录接口返回服务不可用。
	ExecutionRecords *app.ExecutionRecordService
	// Logger 是日志器，为空时使用 slog.Default。
	Logger *slog.Logger
}

// NewHandler 构造挂在根路径上的 API 处理器。
//
// 未注册的 /api 路径（含 /api 本身）一律返回统一 JSON 404 或 405，不会落到前端的
// SPA 回退上；/api 保留路径空间的判定由 IsAPIPath 负责。
func NewHandler(deps Deps) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	router := &router{
		health:     deps.Health,
		systemInfo: deps.SystemInfo,
		tasks:      newTaskHandler(deps.Tasks, logger),
		hosts:      newHostHandler(deps.Hosts, logger),
		executions: newExecutionHandler(deps.ExecutionRecords, logger),
		logger:     logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(PathHealth, router.healthHandler)
	mux.HandleFunc(PathSystemInfo, router.systemInfoHandler)
	// 任务集合路径与单任务路径分别注册：ServeMux 会按更具体的模式优先匹配。
	mux.HandleFunc(PathTasks, router.tasksHandler)
	mux.HandleFunc(taskPathPrefix, router.tasksHandler)
	// 执行节点集合路径与单节点路径分别注册，与任务接口保持一致。
	mux.HandleFunc(PathHosts, router.hostsHandler)
	mux.HandleFunc(hostPathPrefix, router.hostsHandler)
	mux.HandleFunc("/", router.notFoundHandler)
	return withRequestID(mux)
}

// router 持有 API 处理器依赖。
type router struct {
	health     *app.HealthService
	systemInfo func() app.SystemInfo
	tasks      *taskHandler
	hosts      *hostHandler
	executions *executionHandler
	logger     *slog.Logger
}

// tasksHandler 分发任务集合路径与单任务路径的请求。
//
// 任务服务未初始化时返回 503，而不是让请求落到 404 或前端页面上。
func (rt *router) tasksHandler(w http.ResponseWriter, r *http.Request) {
	if rt.tasks == nil || rt.tasks.tasks == nil {
		writeError(
			w,
			r,
			rt.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"任务服务未初始化",
		)
		return
	}
	if r.URL.Path == PathTasks {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			rt.tasks.list(w, r)
		case http.MethodPost:
			rt.tasks.create(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD, POST")
			writeError(
				w,
				r,
				rt.logger,
				http.StatusMethodNotAllowed,
				CodeMethodNotAllowed,
				"该接口只支持 GET 与 POST 请求",
			)
		}
		return
	}
	// 执行记录路径（steps / attempts / sessions）与单任务路径共享前缀，
	// 因此先判定是否属于执行记录形状，再交给任务处理器。
	if isExecutionRecordPath(r.URL.Path) {
		if rt.executions == nil || rt.executions.records == nil {
			writeError(
				w,
				r,
				rt.logger,
				http.StatusServiceUnavailable,
				CodeInternal,
				"执行记录服务未初始化",
			)
			return
		}
		rt.executions.serve(w, r)
		return
	}
	rt.tasks.serve(w, r)
}

// isExecutionRecordPath 判断路径是否指向任务下的执行记录资源。
//
// 判定依据第二段是否是执行记录资源名：单任务动作（例如 cancel）与未知子路径
// 都留给任务处理器，从而保持它们原有的 405 与 404 语义。
func isExecutionRecordPath(path string) bool {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return false
	}
	segments := splitPathSegments(remainder)
	if len(segments) < 2 {
		return false
	}
	switch segments[1] {
	case stepsSegment, sessionsSegment:
		return true
	default:
		return false
	}
}

// hostsHandler 分发执行节点集合路径与单节点路径的请求。
//
// 执行节点服务未初始化时返回 503，而不是让请求落到 404 或前端页面上。
func (rt *router) hostsHandler(w http.ResponseWriter, r *http.Request) {
	if rt.hosts == nil || rt.hosts.hosts == nil {
		writeError(
			w,
			r,
			rt.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"执行节点服务未初始化",
		)
		return
	}
	rt.hosts.serve(w, r)
}

// healthHandler 处理健康检查请求。
//
// 只有整体状态为 ok 时返回 200；存在不可用依赖时返回 503，前端据此显示未连接。
func (rt *router) healthHandler(w http.ResponseWriter, r *http.Request) {
	if !rt.allowGet(w, r) {
		return
	}
	if rt.health == nil {
		writeError(w, r, rt.logger, http.StatusServiceUnavailable, CodeInternal, "健康检查服务未初始化")
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
	if rt.systemInfo == nil {
		writeError(w, r, rt.logger, http.StatusServiceUnavailable, CodeInternal, "系统信息服务未初始化")
		return
	}
	writeJSON(w, r, rt.logger, http.StatusOK, rt.systemInfo())
}

// notFoundHandler 返回统一格式的 404。
func (rt *router) notFoundHandler(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, rt.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
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
