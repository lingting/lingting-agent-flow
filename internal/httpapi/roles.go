package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// PathRoles 是角色列表路径。
//
// 本阶段只提供只读查询：角色由首次初始化从配置导入，不存在 CRUD 或同步入口。
const PathRoles = APIPrefix + "/roles"

// RoleResponse 是角色的对外表示。
//
// 只暴露创建任务选择角色所需的最小字段：system_prompt、工具策略与时间戳都不
// 属于选择器需要的数据，因此不出现在响应里。
type RoleResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Runner  string `json:"runner"`
	Model   string `json:"model"`
	Enabled bool   `json:"enabled"`
}

// RoleListResponse 是角色列表响应。
//
// 角色数量有限，不需要分页；响应仍固定使用 records 包装，与执行节点列表一致。
type RoleListResponse struct {
	Records []RoleResponse `json:"records"`
	Total   int            `json:"total"`
}

// roleHandler 处理角色接口请求。
type roleHandler struct {
	roles  *app.RoleService
	logger *slog.Logger
}

// newRoleHandler 构造角色接口处理器。
func newRoleHandler(roles *app.RoleService, logger *slog.Logger) *roleHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &roleHandler{roles: roles, logger: logger}
}

// serve 分发角色接口请求。
//
// 角色服务未初始化时返回 503：伪造空列表会让调用方以为数据库中确实没有角色。
func (h *roleHandler) serve(w http.ResponseWriter, r *http.Request) {
	if h.roles == nil {
		writeError(w, r, h.logger, http.StatusServiceUnavailable, CodeInternal, "角色服务未初始化")
		return
	}
	if r.URL.Path != PathRoles {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.list(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 GET 请求",
		)
	}
}

// list 处理 GET /api/v1/roles。
//
// 已禁用角色保留在结果中并如实返回 enabled=false，便于诊断；创建任务时的角色
// 选择器必须据此排除它们。
func (h *roleHandler) list(w http.ResponseWriter, r *http.Request) {
	roles, err := h.roles.List(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	records := make([]RoleResponse, 0, len(roles))
	for _, role := range roles {
		records = append(records, roleResponse(role))
	}
	writeJSON(w, r, h.logger, http.StatusOK, RoleListResponse{Records: records, Total: len(records)})
}

// roleResponse 把领域角色转换为对外表示。
func roleResponse(role domain.Role) RoleResponse {
	return RoleResponse{
		ID:      role.ID.String(),
		Name:    role.Name,
		Type:    role.Type.String(),
		Runner:  role.Runner,
		Model:   role.Model,
		Enabled: role.Enabled,
	}
}

// writeServiceError 把查询错误映射为统一错误响应。
//
// 只读查询没有可归因于调用方的失败，因此一律按服务内部错误返回：不得把存储错误
// 伪装成"没有角色"的空列表。原始原因只写日志，不返回给调用方。
func (h *roleHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "角色接口处理失败", slog.String("error", err.Error()))
	writeError(w, r, h.logger, http.StatusInternalServerError, CodeInternal, "服务内部错误")
}
