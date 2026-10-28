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
)

// 项目接口路径。
const (
	// PathProjects 是项目集合路径。
	PathProjects = APIPrefix + "/projects"
	// projectPathPrefix 是单项目路径前缀，实际路径为 {projectPathPrefix}{projectID}。
	projectPathPrefix = PathProjects + "/"
)

// maxProjectRequestBytes 是项目请求体的最大字节数。
//
// 项目字段都是短文本与缓存类型数组，请求体限制防止超大请求占用内存。
const maxProjectRequestBytes = 16 << 10

// ProjectResponse 是项目的对外表示。
//
// 项目只包含非机密元数据：仓库地址在写入前已经剥离凭据，响应里也不会出现 Token、
// 私钥或密码。
type ProjectResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// RepositoryURL 是已脱敏的仓库地址。
	RepositoryURL string `json:"repository_url"`
	// BaseBranch 是规范化后的基础分支名。
	BaseBranch string `json:"base_branch"`
	// Environment 是语言环境名称。
	Environment string `json:"environment"`
	// EnvironmentVersion 是锁定的环境版本标签，缺省表示使用当前可用版本。
	EnvironmentVersion string `json:"environment_version,omitempty"`
	// Caches 是允许挂载的缓存类型。
	Caches []string `json:"caches"`
	// WorkDir 是生效的容器内项目工作目录。
	WorkDir string `json:"work_dir"`
	// CreatedAt 是项目创建时间。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 是项目最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// ProjectListResponse 是项目列表响应。
//
// 项目数量有限，不需要分页；响应固定使用 records 包装，与角色、执行节点列表一致。
type ProjectListResponse struct {
	Records []ProjectResponse `json:"records"`
	Total   int               `json:"total"`
}

// CreateProjectRequest 是新增项目的请求体。
//
// 客户端不能指定创建时间、更新时间或删除标记：这些字段一律由服务端写入。
type CreateProjectRequest struct {
	// ID 是项目标识，必填；它同时是缓存共享范围键与远程管理工作目录名。
	ID string `json:"id"`
	// Name 是项目名称，必填。
	Name string `json:"name"`
	// RepositoryURL 是仓库地址，必填；内嵌凭据的地址被拒绝。
	RepositoryURL string `json:"repository_url"`
	// BaseBranch 是基础分支，必填。
	BaseBranch string `json:"base_branch"`
	// Environment 是语言环境名称，必填。
	Environment string `json:"environment"`
	// EnvironmentVersion 是锁定的环境版本标签，可空。
	EnvironmentVersion string `json:"environment_version"`
	// Caches 是允许挂载的缓存类型。
	Caches []string `json:"caches"`
	// WorkDir 是容器内项目工作目录，空值使用默认目录。
	WorkDir string `json:"work_dir"`
}

// UpdateProjectRequest 是修改项目的请求体。
//
// 与执行节点不同，这里不做"nil 表示不修改"的部分更新：项目配置是一个整体，部分
// 更新会让调用方难以判断某次修改后缓存与目录到底是什么。标识不在请求体中，项目
// 标识不可修改。
type UpdateProjectRequest struct {
	Name               string   `json:"name"`
	RepositoryURL      string   `json:"repository_url"`
	BaseBranch         string   `json:"base_branch"`
	Environment        string   `json:"environment"`
	EnvironmentVersion string   `json:"environment_version"`
	Caches             []string `json:"caches"`
	WorkDir            string   `json:"work_dir"`
}

// projectHandler 处理项目接口请求。
type projectHandler struct {
	projects *app.ProjectService
	logger   *slog.Logger
}

// newProjectHandler 构造项目接口处理器。
func newProjectHandler(projects *app.ProjectService, logger *slog.Logger) *projectHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &projectHandler{projects: projects, logger: logger}
}

// serve 分发项目集合路径与单项目路径的请求。
//
// 项目服务未初始化时返回 503：伪造空列表会让调用方以为确实没有项目，而任务又
// 必须关联项目，最终表现为"建不出任务却看不出原因"。
func (h *projectHandler) serve(w http.ResponseWriter, r *http.Request) {
	if h.projects == nil {
		writeError(w, r, h.logger, http.StatusServiceUnavailable, CodeInternal, "项目服务未初始化")
		return
	}
	if r.URL.Path == PathProjects {
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
	remainder := strings.TrimPrefix(r.URL.Path, projectPathPrefix)
	if remainder == r.URL.Path || strings.TrimSpace(remainder) == "" {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	if !validProjectPathID(remainder) {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.detail(w, r, remainder)
	case http.MethodPut:
		h.update(w, r, remainder)
	case http.MethodDelete:
		h.remove(w, r, remainder)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 GET、PUT 与 DELETE 请求",
		)
	}
}

// validProjectPathID 校验路径中的项目标识不含多余路径段。
func validProjectPathID(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed != "" && !strings.Contains(trimmed, "/")
}

// list 处理 GET /api/v1/projects。
func (h *projectHandler) list(w http.ResponseWriter, r *http.Request) {
	projects, err := h.projects.List(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	records := make([]ProjectResponse, 0, len(projects))
	for _, project := range projects {
		records = append(records, projectResponse(project))
	}
	writeJSON(
		w,
		r,
		h.logger,
		http.StatusOK,
		ProjectListResponse{Records: records, Total: len(records)},
	)
}

// detail 处理 GET /api/v1/projects/{projectID}。
func (h *projectHandler) detail(w http.ResponseWriter, r *http.Request, projectID string) {
	project, err := h.projects.Get(r.Context(), projectID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, projectResponse(project))
}

// create 处理 POST /api/v1/projects。
func (h *projectHandler) create(w http.ResponseWriter, r *http.Request) {
	var body CreateProjectRequest
	if !decodeProjectJSONBody(w, r, h.logger, &body) {
		return
	}
	project, err := h.projects.Create(r.Context(), app.CreateProjectRequest{
		ID:                 body.ID,
		Name:               body.Name,
		RepositoryURL:      body.RepositoryURL,
		BaseBranch:         body.BaseBranch,
		Environment:        body.Environment,
		EnvironmentVersion: body.EnvironmentVersion,
		Caches:             body.Caches,
		WorkDir:            body.WorkDir,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusCreated, projectResponse(project))
}

// update 处理 PUT /api/v1/projects/{projectID}。
func (h *projectHandler) update(w http.ResponseWriter, r *http.Request, projectID string) {
	var body UpdateProjectRequest
	if !decodeProjectJSONBody(w, r, h.logger, &body) {
		return
	}
	project, err := h.projects.Update(r.Context(), projectID, app.UpdateProjectRequest{
		Name:               body.Name,
		RepositoryURL:      body.RepositoryURL,
		BaseBranch:         body.BaseBranch,
		Environment:        body.Environment,
		EnvironmentVersion: body.EnvironmentVersion,
		Caches:             body.Caches,
		WorkDir:            body.WorkDir,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, projectResponse(project))
}

// remove 处理 DELETE /api/v1/projects/{projectID}。
//
// 删除是逻辑删除；成功时返回 204，不返回响应体。历史任务配置不会因此改变。
func (h *projectHandler) remove(w http.ResponseWriter, r *http.Request, projectID string) {
	if err := h.projects.Delete(r.Context(), projectID); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// projectResponse 把领域项目转换为对外表示。
func projectResponse(project domain.Project) ProjectResponse {
	caches := project.Caches
	if caches == nil {
		caches = []string{}
	}
	return ProjectResponse{
		ID:                 project.ID,
		Name:               project.Name,
		RepositoryURL:      project.RepositoryURL,
		BaseBranch:         project.NormalizedBaseBranch(),
		Environment:        project.Environment.Name,
		EnvironmentVersion: project.EnvironmentVersion,
		Caches:             caches,
		WorkDir:            project.EffectiveWorkDir(),
		CreatedAt:          project.CreatedAt,
		UpdatedAt:          project.UpdatedAt,
	}
}

// writeServiceError 把项目服务错误映射为统一错误响应。
//
// 参数错误 400、项目不存在 404、标识冲突 409；其余一律 500，原始原因只写日志。
func (h *projectHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, app.ErrProjectInvalidInput):
		writeError(w, r, h.logger, http.StatusBadRequest, CodeInvalidRequest, err.Error())
	case errors.Is(err, app.ErrProjectNotFound):
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, app.ErrProjectStateConflict):
		writeError(w, r, h.logger, http.StatusConflict, CodeConflict, err.Error())
	default:
		h.logger.ErrorContext(r.Context(), "项目接口处理失败", slog.String("error", err.Error()))
		writeError(w, r, h.logger, http.StatusInternalServerError, CodeInternal, "服务内部错误")
	}
}

// decodeProjectJSONBody 解析项目请求体，失败时写出 400 并返回 false。
func decodeProjectJSONBody(
	w http.ResponseWriter,
	r *http.Request,
	logger *slog.Logger,
	target any,
) bool {
	limited := http.MaxBytesReader(w, r.Body, maxProjectRequestBytes)
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
