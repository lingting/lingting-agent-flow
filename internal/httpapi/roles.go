package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// PathRoles 是角色集合路径。
//
// 角色集合固定为四个，由服务启动时按内置默认定义补齐：这里只提供查询与配置更新，
// 不存在角色新增与删除入口。
const PathRoles = APIPrefix + "/roles"

// rolePathPrefix 是单角色路径前缀，供路由注册使用。
const rolePathPrefix = PathRoles + "/"

// maxRoleRequestBytes 是角色配置请求体的最大字节数。
const maxRoleRequestBytes = 64 << 10

// RoleResponse 是角色在列表中的对外表示。
//
// 只暴露创建任务选择角色所需的最小字段：system_prompt、工具策略与时间戳都不
// 属于选择器需要的数据，因此不出现在列表响应里，完整配置由单角色接口提供。
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

// RoleToolsPolicyResponse 是角色工具策略的对外表示。
//
// 它只是保存的策略声明：实际可用工具由 RoleDetailResponse.EffectiveTools 给出，
// 客户端不需要、也不允许自行推算。
type RoleToolsPolicyResponse struct {
	Permissions []string `json:"permissions"`
	AllowTools  []string `json:"allow_tools,omitempty"`
	DenyTools   []string `json:"deny_tools,omitempty"`
}

// RoleDetailResponse 是单个角色的完整配置响应。
//
// 除保存的配置外还返回三项由服务端计算的只读信息：角色身份的固定能力上限、
// 该上限内可选的 Pi 内置工具，以及当前配置实际生效的工具列表。
type RoleDetailResponse struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Type         string                  `json:"type"`
	Runner       string                  `json:"runner"`
	Model        string                  `json:"model"`
	SystemPrompt string                  `json:"system_prompt"`
	ToolsPolicy  RoleToolsPolicyResponse `json:"tools_policy"`
	// CapabilityCeiling 是角色类型固定不可突破的能力上限。
	CapabilityCeiling []string `json:"capability_ceiling"`
	// AvailableTools 是能力上限内的内置工具，设置页只在这些工具中配置允许与拒绝。
	AvailableTools []string `json:"available_tools"`
	// EffectiveTools 是本次配置最终交给 Pi 的工具列表，为空表示不加载任何工具。
	EffectiveTools []string  `json:"effective_tools"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// UpdateRoleToolsPolicyRequest 是请求体中的工具策略。
//
// 字段语义与 domain.ToolsPolicy 一致：permissions 是被授权的能力类别，allow_tools 为空
// 表示不按名称限制已授权的工具，deny_tools 始终优先。
type UpdateRoleToolsPolicyRequest struct {
	Permissions []string `json:"permissions"`
	AllowTools  []string `json:"allow_tools"`
	DenyTools   []string `json:"deny_tools"`
}

// UpdateRoleRequest 是 PUT /api/v1/roles/{roleID} 的请求体。
//
// 只有四个可编辑字段：请求体里出现 id、name、type、runner 或任何未知字段都会因为
// 拒绝未知字段而返回 400。字段为 nil 表示本次不修改该字段；tools_policy 一旦提交即
// 整体替换已保存的策略，越出角色能力上限时返回 400。
type UpdateRoleRequest struct {
	SystemPrompt *string                       `json:"system_prompt"`
	Model        *string                       `json:"model"`
	Enabled      *bool                         `json:"enabled"`
	ToolsPolicy  *UpdateRoleToolsPolicyRequest `json:"tools_policy"`
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
	if r.URL.Path == PathRoles {
		h.serveCollection(w, r)
		return
	}
	roleID, ok := roleIDFromPath(r.URL.Path)
	if !ok {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.detail(w, r, roleID)
	case http.MethodPut:
		h.update(w, r, roleID)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 GET 与 PUT 请求",
		)
	}
}

// serveCollection 处理角色集合路径，只支持列表查询。
func (h *roleHandler) serveCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.list(w, r)
	default:
		// 角色集合固定，集合路径不提供新增：POST 与其他方法一律拒绝。
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

// detail 处理 GET /api/v1/roles/{roleID}。
func (h *roleHandler) detail(w http.ResponseWriter, r *http.Request, roleID string) {
	role, err := h.roles.Get(r.Context(), roleID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	h.writeDetail(w, r, role)
}

// update 处理 PUT /api/v1/roles/{roleID}。
//
// 响应返回重新读取的真实保存结果，调用方据此刷新展示，不需要自行推测保存后的状态。
func (h *roleHandler) update(w http.ResponseWriter, r *http.Request, roleID string) {
	var request UpdateRoleRequest
	if !decodeJSONBodyWithLimit(w, r, h.logger, &request, maxRoleRequestBytes) {
		return
	}
	policy, err := updateToolsPolicyOf(request.ToolsPolicy)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	role, err := h.roles.Update(r.Context(), roleID, app.RoleUpdate{
		SystemPrompt: request.SystemPrompt,
		Model:        request.Model,
		Enabled:      request.Enabled,
		ToolsPolicy:  policy,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	h.writeDetail(w, r, role)
}

// writeDetail 输出单个角色的完整配置。
//
// 工具策略无法换算成实际工具时返回 500：此时既不能返回一个"没有工具限制"的响应，也不能
// 把服务端配置异常伪装成请求参数错误。
func (h *roleHandler) writeDetail(w http.ResponseWriter, r *http.Request, role domain.Role) {
	response, err := roleDetailResponse(role)
	if err != nil {
		h.logger.ErrorContext(
			r.Context(),
			"角色工具策略无法计算",
			slog.String("role_id", role.ID.String()),
			slog.String("error", err.Error()),
		)
		writeError(
			w,
			r,
			h.logger,
			http.StatusInternalServerError,
			CodeInternal,
			"角色 "+role.ID.String()+" 的工具策略无法计算: "+err.Error(),
		)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, response)
}

// updateToolsPolicyOf 把请求中的工具策略转换为领域类型，未提交时返回 nil 表示不修改。
func updateToolsPolicyOf(
	request *UpdateRoleToolsPolicyRequest,
) (*domain.ToolsPolicy, error) {
	if request == nil {
		return nil, nil
	}
	permissions := make([]domain.ToolPermission, 0, len(request.Permissions))
	for _, permission := range request.Permissions {
		value := domain.ToolPermission(strings.TrimSpace(permission))
		if err := value.Validate(); err != nil {
			return nil, err
		}
		permissions = append(permissions, value)
	}
	return &domain.ToolsPolicy{
		Permissions: permissions,
		AllowTools:  request.AllowTools,
		DenyTools:   request.DenyTools,
	}, nil
}

// roleIDFromPath 解析单角色路径中的角色标识。
//
// 只接受 /api/v1/roles/{roleID} 这一段形状：更深或更浅的路径仍由调用方给出 404。
func roleIDFromPath(path string) (string, bool) {
	remainder := strings.TrimPrefix(path, rolePathPrefix)
	if remainder == path {
		return "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 1 {
		return "", false
	}
	return segments[0], true
}

// roleResponse 把领域角色转换为列表项表示。
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

// roleDetailResponse 把领域角色转换为完整配置表示。
//
// 系统能力上限、上限内可选工具与实际生效工具都在这里由 domain 计算一次：Handler 与前端
// 都不重新实现权限判定。
func roleDetailResponse(role domain.Role) (RoleDetailResponse, error) {
	ceiling, err := domain.RoleToolCeiling(role.Type)
	if err != nil {
		return RoleDetailResponse{}, err
	}
	effective, err := domain.EffectivePiTools(role.Type, role.ToolsPolicy)
	if err != nil {
		return RoleDetailResponse{}, err
	}
	return RoleDetailResponse{
		ID:                role.ID.String(),
		Name:              role.Name,
		Type:              role.Type.String(),
		Runner:            role.Runner,
		Model:             role.Model,
		SystemPrompt:      role.SystemPrompt,
		ToolsPolicy:       roleToolsPolicyResponse(role.ToolsPolicy),
		CapabilityCeiling: permissionNames(ceiling),
		AvailableTools:    domain.ToolsForPermissions(ceiling),
		EffectiveTools:    effective,
		Enabled:           role.Enabled,
		CreatedAt:         role.CreatedAt,
		UpdatedAt:         role.UpdatedAt,
	}, nil
}

// roleToolsPolicyResponse 把领域工具策略转换为对外表示。
func roleToolsPolicyResponse(policy domain.ToolsPolicy) RoleToolsPolicyResponse {
	return RoleToolsPolicyResponse{
		Permissions: permissionNames(policy.Permissions),
		AllowTools:  policy.AllowTools,
		DenyTools:   policy.DenyTools,
	}
}

// permissionNames 把能力类别转换为对外字符串表示，保持输入顺序。
func permissionNames(permissions []domain.ToolPermission) []string {
	names := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		names = append(names, string(permission))
	}
	return names
}

// writeServiceError 把角色服务错误映射为统一错误响应。
//
// 参数错误与不存在返回服务端给出的真实原因；内部错误只写日志，不把存储细节返回给
// 调用方，也不把存储错误伪装成"角色不存在"。
func (h *roleHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, app.ErrRoleInvalidInput), errors.Is(err, domain.ErrInvalidValue):
		writeError(w, r, h.logger, http.StatusBadRequest, CodeInvalidRequest, err.Error())
	case errors.Is(err, app.ErrRoleNotFound):
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, err.Error())
	default:
		h.logger.ErrorContext(r.Context(), "角色接口处理失败", slog.String("error", err.Error()))
		writeError(w, r, h.logger, http.StatusInternalServerError, CodeInternal, "服务内部错误")
	}
}
