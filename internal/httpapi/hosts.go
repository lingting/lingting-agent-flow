package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 执行节点接口路径。
const (
	// PathHosts 是执行节点集合路径。
	PathHosts = APIPrefix + "/hosts"
	// hostPathPrefix 是单节点路径前缀，实际路径为 {hostPathPrefix}{hostID}。
	hostPathPrefix = PathHosts + "/"
	// hostValidateSuffix 是重新校验动作的子路径。
	hostValidateSuffix = "/validate"
)

// maxHostRequestBytes 是执行节点请求体的最大字节数。
const maxHostRequestBytes = 16 << 10

// HostResponse 是执行节点的对外表示。
//
// 校验字段直接来自持久化的节点记录：状态取值与 Go 端 domain.ValidationStatus 一致，
// 不存在第二套对外状态名。凭据相关字段（密码、私钥口令）永远不会出现在响应里。
type HostResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	WorkspaceRoot string `json:"workspace_root"`
	IsDefault     bool   `json:"is_default"`
	Enabled       bool   `json:"enabled"`
	AuthMethod    string `json:"auth_method"`
	// PrivateKeyPath 只暴露本机路径，不包含私钥内容。
	PrivateKeyPath string `json:"private_key_path,omitempty"`
	// HostKeyAlgorithm 是已确认信任的主机密钥算法。
	HostKeyAlgorithm string `json:"host_key_algorithm,omitempty"`
	// HostKeyFingerprint 是已确认信任的主机密钥指纹。
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
	// LastConnectStatus 是最近一次连接测试的结果类别。
	LastConnectStatus string `json:"last_connect_status"`
	// ValidationStatus 是环境预检总体状态：checking、normal 或 abnormal。
	ValidationStatus string `json:"validation_status"`
	// ValidationMessage 是最新校验结果的摘要或失败原因。
	ValidationMessage string `json:"validation_message,omitempty"`
	// ValidationDetails 是各项校验的结构化结果。
	ValidationDetails *domain.ValidationDetails `json:"validation_details,omitempty"`
	// ValidationStartedAt 是最近一次校验开始时间。
	ValidationStartedAt *time.Time `json:"validation_started_at,omitempty"`
	// ValidationCompletedAt 是最近一次校验结束时间。
	ValidationCompletedAt *time.Time `json:"validation_completed_at,omitempty"`
	// ValidationGeneration 是校验代次，用于识别结果是否属于当前配置。
	ValidationGeneration int64 `json:"validation_generation"`
	// UsableForRemoteTask 表示当前是否允许用于真实远程任务执行。
	//
	// 该字段由服务端按与后端准入校验相同的规则计算，前端据此禁用操作；
	// 前端状态被篡改也不会绕过后端校验。
	UsableForRemoteTask bool      `json:"usable_for_remote_task"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// HostListResponse 是执行节点列表响应。
//
// 执行节点数量有限，不需要分页；响应仍然固定使用 records 包装，便于后续扩展。
type HostListResponse struct {
	Records []HostResponse `json:"records"`
	Total   int            `json:"total"`
}

// CreateHostRequest 是新增执行节点的请求体。
//
// 客户端不能指定 validation_status、validation_generation 等校验字段：
// 状态一律由服务端设置为 checking 并在后台异步校验。
type CreateHostRequest struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	WorkspaceRoot  string `json:"workspace_root"`
	AuthMethod     string `json:"auth_method"`
	PrivateKeyPath string `json:"private_key_path"`
	Default        bool   `json:"default"`
}

// UpdateHostRequest 是修改执行节点的请求体。
//
// 字段为 nil 表示不修改该项，避免把"未指定"与"设置为零值"混为一谈。
// 客户端同样不能指定任何校验字段。
type UpdateHostRequest struct {
	Name           *string `json:"name"`
	Host           *string `json:"host"`
	Port           *int    `json:"port"`
	Username       *string `json:"username"`
	WorkspaceRoot  *string `json:"workspace_root"`
	AuthMethod     *string `json:"auth_method"`
	PrivateKeyPath *string `json:"private_key_path"`
	Enabled        *bool   `json:"enabled"`
	Default        *bool   `json:"default"`
}

// hostHandler 处理执行节点接口请求。
type hostHandler struct {
	hosts  *app.ExecutionHostService
	logger *slog.Logger
}

// newHostHandler 构造执行节点接口处理器。
func newHostHandler(hosts *app.ExecutionHostService, logger *slog.Logger) *hostHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &hostHandler{hosts: hosts, logger: logger}
}

// serve 分发执行节点集合路径与单节点路径的请求。
func (h *hostHandler) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == PathHosts {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			h.list(w, r)
		case http.MethodPost:
			h.create(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD, POST")
			writeError(
				w,
				r,
				h.logger,
				http.StatusMethodNotAllowed,
				CodeMethodNotAllowed,
				"该接口只支持 GET 与 POST 请求",
			)
		}
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, hostPathPrefix)
	if remainder == r.URL.Path || strings.TrimSpace(remainder) == "" {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	if strings.HasSuffix(remainder, hostValidateSuffix) {
		hostID := strings.TrimSuffix(remainder, hostValidateSuffix)
		if !validHostPathID(hostID) {
			writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
			return
		}
		h.validate(w, r, hostID)
		return
	}
	if !validHostPathID(remainder) {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.detail(w, r, remainder)
	case http.MethodPatch, http.MethodPut:
		h.update(w, r, remainder)
	case http.MethodDelete:
		h.remove(w, r, remainder)
	default:
		w.Header().Set("Allow", "GET, HEAD, PATCH, PUT, DELETE")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 GET、PATCH、PUT 与 DELETE 请求",
		)
	}
}

// validHostPathID 校验路径中的节点标识不含多余路径段。
func validHostPathID(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed != "" && !strings.Contains(trimmed, "/")
}

// list 处理 GET /api/v1/hosts。
func (h *hostHandler) list(w http.ResponseWriter, r *http.Request) {
	hosts, err := h.hosts.List(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	records := make([]HostResponse, 0, len(hosts))
	for _, host := range hosts {
		records = append(records, hostResponse(host))
	}
	writeJSON(w, r, h.logger, http.StatusOK, HostListResponse{Records: records, Total: len(records)})
}

// detail 处理 GET /api/v1/hosts/{hostID}。
func (h *hostHandler) detail(w http.ResponseWriter, r *http.Request, hostID string) {
	host, err := h.hosts.Get(r.Context(), hostID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, hostResponse(host))
}

// create 处理 POST /api/v1/hosts。
//
// 创建成功返回 201 与真实初始状态（checking），不等待后台校验完成。
func (h *hostHandler) create(w http.ResponseWriter, r *http.Request) {
	var body CreateHostRequest
	if !decodeHostJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toAppRequest()
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	host, err := h.hosts.Create(r.Context(), request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusCreated, hostResponse(host))
}

// update 处理 PATCH 与 PUT /api/v1/hosts/{hostID}。
func (h *hostHandler) update(w http.ResponseWriter, r *http.Request, hostID string) {
	var body UpdateHostRequest
	if !decodeHostJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toAppRequest(hostID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	host, err := h.hosts.Update(r.Context(), request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, hostResponse(host))
}

// validate 处理 POST /api/v1/hosts/{hostID}/validate。
//
// 请求立即返回当前配置与 checking 状态，校验在后台服务中异步执行。
func (h *hostHandler) validate(w http.ResponseWriter, r *http.Request, hostID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 POST 请求",
		)
		return
	}
	host, err := h.hosts.Revalidate(r.Context(), hostID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusAccepted, hostResponse(host))
}

// remove 处理 DELETE /api/v1/hosts/{hostID}。
//
// 删除是逻辑删除；成功时返回 204，不返回响应体。
func (h *hostHandler) remove(w http.ResponseWriter, r *http.Request, hostID string) {
	if _, err := h.hosts.Remove(r.Context(), hostID); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hostResponse 把领域执行节点转换为对外表示。
func hostResponse(host domain.ExecutionHost) HostResponse {
	return HostResponse{
		ID:                    host.ID.String(),
		Name:                  host.Name,
		Host:                  host.Host,
		Port:                  host.Port,
		Username:              host.Username,
		WorkspaceRoot:         host.WorkspaceRoot,
		IsDefault:             host.IsDefault,
		Enabled:               host.Enabled,
		AuthMethod:            host.AuthMethod.String(),
		PrivateKeyPath:        host.PrivateKeyPath,
		HostKeyAlgorithm:      host.HostKeyAlgorithm,
		HostKeyFingerprint:    host.HostKeyFingerprint,
		LastConnectStatus:     host.LastConnectStatus.String(),
		ValidationStatus:      host.ValidationStatus.String(),
		ValidationMessage:     host.ValidationMessage,
		ValidationDetails:     host.ValidationDetails,
		ValidationStartedAt:   host.ValidationStartedAt,
		ValidationCompletedAt: host.ValidationCompletedAt,
		ValidationGeneration:  host.ValidationGeneration,
		UsableForRemoteTask:   app.EnsureHostUsableForRemoteTask(host) == nil,
		CreatedAt:             host.CreatedAt,
		UpdatedAt:             host.UpdatedAt,
	}
}

// toAppRequest 把请求体转换为应用层新增请求。
func (b CreateHostRequest) toAppRequest() (app.CreateExecutionHostRequest, error) {
	method := strings.TrimSpace(b.AuthMethod)
	if method == "" {
		// 本版本只支持私钥认证，默认值即私钥。
		method = string(domain.AuthMethodPrivateKey)
	}
	parsed, err := domain.ParseAuthMethod(method)
	if err != nil {
		return app.CreateExecutionHostRequest{}, errors.Join(app.ErrHostInvalidInput, err)
	}
	return app.CreateExecutionHostRequest{
		ID:             b.ID,
		Name:           b.Name,
		Host:           b.Host,
		Port:           b.Port,
		Username:       b.Username,
		WorkspaceRoot:  b.WorkspaceRoot,
		AuthMethod:     parsed,
		PrivateKeyPath: b.PrivateKeyPath,
		Default:        b.Default,
	}, nil
}

// toAppRequest 把请求体转换为应用层更新请求。
func (b UpdateHostRequest) toAppRequest(hostID string) (app.UpdateExecutionHostRequest, error) {
	request := app.UpdateExecutionHostRequest{
		ID:             hostID,
		Name:           b.Name,
		Host:           b.Host,
		Port:           b.Port,
		Username:       b.Username,
		WorkspaceRoot:  b.WorkspaceRoot,
		PrivateKeyPath: b.PrivateKeyPath,
		Enabled:        b.Enabled,
		Default:        b.Default,
	}
	if b.AuthMethod != nil {
		parsed, err := domain.ParseAuthMethod(*b.AuthMethod)
		if err != nil {
			return app.UpdateExecutionHostRequest{}, errors.Join(app.ErrHostInvalidInput, err)
		}
		request.AuthMethod = &parsed
	}
	return request, nil
}

// decodeHostJSONBody 解析请求体 JSON，失败时写出 400 并返回 false。
func decodeHostJSONBody(
	w http.ResponseWriter,
	r *http.Request,
	logger *slog.Logger,
	target any,
) bool {
	limited := http.MaxBytesReader(w, r.Body, maxHostRequestBytes)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		message := "请求体不是合法的 JSON"
		if errors.Is(err, io.EOF) {
			message = "请求体不能为空"
		}
		writeError(w, r, logger, http.StatusBadRequest, CodeInvalidRequest, message)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(
			w,
			r,
			logger,
			http.StatusBadRequest,
			CodeInvalidRequest,
			"请求体只能包含一个 JSON 对象",
		)
		return false
	}
	return true
}

// writeServiceError 把应用层错误映射为统一错误响应。
//
// 映射依据错误类别而不是文本：参数错误 400、资源不存在 404、状态冲突 409、
// 其余为 500。内部错误不把底层细节返回给调用方。
func (h *hostHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrHostInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, storage.ErrConflict),
		errors.Is(err, domain.ErrInvalidState):
		status = http.StatusConflict
		code = CodeConflict
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		h.logger.ErrorContext(r.Context(), "执行节点接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}
