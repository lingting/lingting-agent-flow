package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/lingting/agentflow/internal/app"
)

// 概览汇总接口路径。
const (
	// PathOverview 是概览汇总接口的公共前缀。
	PathOverview = APIPrefix + "/overview"
)

// 概览汇总的子资源路径段。
//
// 每类实体一个独立接口：概览页按实体分别加载，某一类失败时其他类仍然显示真实结论，
// 不会因为一个请求失败就让整页变成"未知"。
const (
	overviewRequirementsSegment = "requirements"
	overviewProjectsSegment     = "projects"
	overviewTasksSegment        = "tasks"
	overviewHostsSegment        = "hosts"
)

// OverviewStatusCountsResponse 是按状态分组的计数响应。
//
// `counts` 的键是服务端持久化的状态原值：未知取值原样返回。缺失的键不是"该状态为 0
// 但状态未知"，而是"这个状态确实没有记录"——前端展示固定状态清单时应自行补 0，
// 同时把响应里多出来的未知状态单独展示，不得丢弃。
type OverviewStatusCountsResponse struct {
	// Total 是参与统计的记录总数。
	Total int `json:"total"`
	// Counts 是状态到未删除记录条数的映射。
	Counts map[string]int `json:"counts"`
}

// OverviewProjectCountsResponse 是项目配置维度的计数响应。
//
// 三个数字都是已登记的配置事实：设置了默认执行节点的项目仍可能指向已删除的节点，
// 配置了缓存类型的项目也可能从未挂载过缓存卷。
type OverviewProjectCountsResponse struct {
	// Total 是未删除的项目总数。
	Total int `json:"total"`
	// WithDefaultHost 是设置了默认执行节点的项目数量。
	WithDefaultHost int `json:"with_default_host"`
	// WithCaches 是配置了至少一种缓存类型的项目数量。
	WithCaches int `json:"with_caches"`
}

// overviewHandler 处理概览汇总的只读计数接口。
//
// 它只做协议转换与错误映射：计数本身全部由 app.OverviewService 从持久化事实读出，
// HTTP 层不补 0、不推导执行结论，也不缓存结果。
type overviewHandler struct {
	overview *app.OverviewService
	logger   *slog.Logger
}

// newOverviewHandler 构造概览汇总接口处理器。
//
// 服务为空时仍返回处理器，接口由路由前置检查统一给出 503。
func newOverviewHandler(
	overview *app.OverviewService,
	logger *slog.Logger,
) *overviewHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &overviewHandler{overview: overview, logger: logger}
}

// requirements 处理 GET /api/v1/overview/requirements。
func (h *overviewHandler) requirements(w http.ResponseWriter, r *http.Request) {
	counts, err := h.overview.RequirementStatusCounts(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, statusCountsResponse(counts))
}

// projects 处理 GET /api/v1/overview/projects。
func (h *overviewHandler) projects(w http.ResponseWriter, r *http.Request) {
	counts, err := h.overview.ProjectCounts(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, OverviewProjectCountsResponse{
		Total:           counts.Total,
		WithDefaultHost: counts.WithDefaultHost,
		WithCaches:      counts.WithCaches,
	})
}

// tasks 处理 GET /api/v1/overview/tasks。
func (h *overviewHandler) tasks(w http.ResponseWriter, r *http.Request) {
	counts, err := h.overview.TaskStatusCounts(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, statusCountsResponse(counts))
}

// hosts 处理 GET /api/v1/overview/hosts。
func (h *overviewHandler) hosts(w http.ResponseWriter, r *http.Request) {
	counts, err := h.overview.HostValidationStatusCounts(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, statusCountsResponse(counts))
}

// writeServiceError 把汇总查询失败映射为统一错误响应。
//
// 汇总查询没有业务分支：读取失败就是持久化失败，一律按 500 返回，绝不降级成"全部为
// 0"的假汇总。
func (h *overviewHandler) writeServiceError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	h.logger.ErrorContext(
		r.Context(),
		"概览汇总查询失败",
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()),
	)
	writeError(w, r, h.logger, http.StatusInternalServerError, CodeInternal, "服务内部错误")
}

// statusCountsResponse 把应用层计数转换为对外表示。
//
// 没有任何记录时 `counts` 输出空对象而不是 null，调用方无需区分两种空形态。
func statusCountsResponse(counts app.OverviewStatusCounts) OverviewStatusCountsResponse {
	response := make(map[string]int, len(counts.Counts))
	for status, count := range counts.Counts {
		response[status] = count
	}
	return OverviewStatusCountsResponse{Total: counts.Total, Counts: response}
}
