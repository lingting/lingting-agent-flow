package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// 环境查询接口路径。
//
// 路径形状与执行节点接口保持一致：集合路径 + 单资源子路径。环境引用由两个路径段
// （type 与 name）表达，不使用 domain.EnvironmentRef.Label() 的 "type/name" 单段形式：
// 斜杠在 URL 中是路径分隔符，把它当作单一路径段会依赖隐式的转义解码规则。
const (
	// PathEnvironments 是环境集合路径。
	PathEnvironments = APIPrefix + "/environments"
	// environmentPathPrefix 是环境资源路径前缀。
	environmentPathPrefix = PathEnvironments + "/"
	// environmentVersionsSegment 是版本集合的子路径段。
	environmentVersionsSegment = "versions"
)

// EnvironmentVersionResponse 是环境版本的对外表示。
//
// 只暴露选择与诊断所需的事实：构建状态直接取 domain.BuildStatus 的真实取值，
// Usable 由服务端按与创建容器相同的规则计算，前端不需要（也不允许）从 image_ref
// 或版本号推断可用性。Dockerfile 正文、远程路径与凭据都不出现在响应里。
type EnvironmentVersionResponse struct {
	// Version 是版本标签，例如 v0001。
	Version string `json:"version"`
	// Number 是递增版本号。
	Number int `json:"number"`
	// BuildStatus 是真实构建状态：pending / building / succeeded / failed。
	BuildStatus string `json:"build_status"`
	// Usable 表示该版本当前是否可以用于创建容器。
	//
	// 判定依据是构建成功且已记录实际镜像 ID，与任务启动时的准入规则一致。
	Usable bool `json:"usable"`
	// ImageRef 是创建版本时确定的镜像引用，供容器创建使用。
	ImageRef string `json:"image_ref"`
	// ImageID 是构建成功后核实的实际镜像 ID，未构建成功时为空。
	ImageID string `json:"image_id,omitempty"`
	// ParentVersion 是依赖的父基础环境版本标签，基础环境为空。
	ParentVersion string `json:"parent_version,omitempty"`
	// ErrorSummary 是构建失败的可诊断摘要，不包含凭据内容。
	ErrorSummary string `json:"error_summary,omitempty"`
	// CreatedAt 是版本创建时间。
	CreatedAt time.Time `json:"created_at"`
	// BuiltAt 是构建完成时间，未构建成功时为空。
	BuiltAt *time.Time `json:"built_at,omitempty"`
}

// EnvironmentResponse 是单个环境的对外表示。
type EnvironmentResponse struct {
	// Type 是环境类型：base 或 language。
	Type string `json:"type"`
	// Name 是环境名称；基础环境固定为 base。
	Name string `json:"name"`
	// CurrentVersion 是当前可用版本标签，没有可用版本时为空。
	//
	// 它取最后一个构建成功且已核实镜像 ID 的版本，不是最新创建的版本。
	CurrentVersion string `json:"current_version,omitempty"`
	// LatestVersion 是已创建的最大版本标签。
	LatestVersion string `json:"latest_version"`
	// Versions 是该环境的全部版本，按版本号升序排列。
	Versions []EnvironmentVersionResponse `json:"versions"`
}

// EnvironmentListResponse 是环境列表响应。
//
// 环境数量有限，不需要分页；响应仍固定使用 records 包装，与角色和执行节点列表一致。
type EnvironmentListResponse struct {
	Records []EnvironmentResponse `json:"records"`
	Total   int                   `json:"total"`
}

// environmentHandler 处理环境只读查询请求。
type environmentHandler struct {
	environments *app.EnvironmentQueryService
	logger       *slog.Logger
}

// newEnvironmentHandler 构造环境查询处理器。
func newEnvironmentHandler(
	environments *app.EnvironmentQueryService,
	logger *slog.Logger,
) *environmentHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &environmentHandler{environments: environments, logger: logger}
}

// serve 分发环境查询请求。
//
// 支持两种形状：
//
//	GET /api/v1/environments?host_id=…
//	GET /api/v1/environments/{type}/{name}/versions/{number}?host_id=…
//
// 其余形状一律 404：本阶段不提供环境 CRUD、构建或 Dockerfile 接口。
func (h *environmentHandler) serve(w http.ResponseWriter, r *http.Request) {
	if h.environments == nil {
		writeError(w, r, h.logger, http.StatusServiceUnavailable, CodeInternal, "环境查询服务未初始化")
		return
	}
	if r.URL.Path == PathEnvironments {
		h.serveList(w, r)
		return
	}
	ref, number, parsed := parseEnvironmentVersionPath(r.URL.Path)
	if !parsed.matched {
		// 形状不符：该路径不是本阶段的资源，按接口不存在处理。
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	if parsed.err != nil {
		// 形状正确但取值非法：这是调用方的参数问题，按 400 返回并给出可定位原因。
		writeError(w, r, h.logger, http.StatusBadRequest, CodeInvalidRequest, parsed.err.Error())
		return
	}
	h.serveVersion(w, r, ref, number)
}

// serveList 处理 GET /api/v1/environments。
func (h *environmentHandler) serveList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 GET 请求",
		)
		return
	}
	hostID, ok := h.requireHostID(w, r)
	if !ok {
		return
	}
	summaries, err := h.environments.List(r.Context(), app.ListRequest{MachineID: hostID})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	records := make([]EnvironmentResponse, 0, len(summaries))
	for _, summary := range summaries {
		records = append(records, environmentResponse(summary))
	}
	writeJSON(
		w,
		r,
		h.logger,
		http.StatusOK,
		EnvironmentListResponse{Records: records, Total: len(records)},
	)
}

// serveVersion 处理单个环境版本的查询。
func (h *environmentHandler) serveVersion(
	w http.ResponseWriter,
	r *http.Request,
	ref domain.EnvironmentRef,
	number int,
) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 GET 请求",
		)
		return
	}
	hostID, ok := h.requireHostID(w, r)
	if !ok {
		return
	}
	version, err := h.environments.GetVersion(r.Context(), app.GetVersionRequest{
		MachineID: hostID,
		Ref:       ref,
		Number:    number,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, environmentVersionResponse(version))
}

// requireHostID 读取并校验 host_id 查询参数。
//
// host_id 是环境查询的唯一节点定位方式：缺少它无法确定要查询哪台机器上的环境，
// 因此按参数错误返回 400，而不是隐式回退到默认节点。
func (h *environmentHandler) requireHostID(w http.ResponseWriter, r *http.Request) (string, bool) {
	hostID := strings.TrimSpace(r.URL.Query().Get("host_id"))
	if hostID == "" {
		writeError(w, r, h.logger, http.StatusBadRequest, CodeInvalidRequest, "缺少查询参数 host_id")
		return "", false
	}
	return hostID, true
}

// environmentVersionPath 是版本路径的解析结果。
type environmentVersionPath struct {
	// matched 表示路径形状确实属于版本查询接口。
	matched bool
	// err 表示路径形状正确但取值非法（环境类型、名称或版本号）。
	err error
}

// parseEnvironmentVersionPath 解析 {type}/{name}/versions/{number} 形状的路径。
//
// 解析严格按路径段数量与位置进行，并区分两类失败：
//   - 形状不符（段数不对、缺少 versions 段）：matched=false，由调用方按 404 处理；
//   - 形状正确但取值非法（未知环境类型、非法名称、非法版本号）：err 非空，按 400 处理。
//
// 这样既不会把形状错误的路径误当作合法资源，也不会把参数错误伪装成"资源不存在"。
func parseEnvironmentVersionPath(
	path string,
) (domain.EnvironmentRef, int, environmentVersionPath) {
	remainder := strings.TrimPrefix(path, environmentPathPrefix)
	if remainder == path {
		return domain.EnvironmentRef{}, 0, environmentVersionPath{}
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 4 || segments[2] != environmentVersionsSegment {
		return domain.EnvironmentRef{}, 0, environmentVersionPath{}
	}
	matched := environmentVersionPath{matched: true}
	ref, err := environmentRefFromSegments(segments[0], segments[1])
	if err != nil {
		return domain.EnvironmentRef{}, 0, environmentVersionPath{matched: true, err: err}
	}
	number, err := strconv.Atoi(segments[3])
	if err != nil {
		return domain.EnvironmentRef{}, 0, environmentVersionPath{
			matched: true,
			err:     errors.New("版本号必须是整数: " + segments[3]),
		}
	}
	if number < domain.MinEnvironmentVersionNumber || number > domain.MaxEnvironmentVersionNumber {
		return domain.EnvironmentRef{}, 0, environmentVersionPath{
			matched: true,
			err: fmt.Errorf(
				"版本号必须在 %d-%d 之间: %d",
				domain.MinEnvironmentVersionNumber,
				domain.MaxEnvironmentVersionNumber,
				number,
			),
		}
	}
	return ref, number, matched
}

// environmentRefFromSegments 由路径段构造环境引用。
//
// 基础环境的名称段固定为 base；语言环境名称经 domain.NewEnvironmentRef 规范化与校验，
// 因此非法名称在这里就被拒绝，不会进入应用层。
func environmentRefFromSegments(environmentType, name string) (domain.EnvironmentRef, error) {
	parsedType, err := domain.ParseEnvironmentType(environmentType)
	if err != nil {
		return domain.EnvironmentRef{}, err
	}
	return domain.NewEnvironmentRef(parsedType, name)
}

// environmentResponse 把应用层概览转换为对外表示。
func environmentResponse(summary app.EnvironmentSummary) EnvironmentResponse {
	versions := make([]EnvironmentVersionResponse, 0, len(summary.Versions))
	for _, version := range summary.Versions {
		versions = append(versions, environmentVersionResponse(version))
	}
	return EnvironmentResponse{
		Type:           summary.Ref.Type.String(),
		Name:           summary.Ref.Name,
		CurrentVersion: summary.CurrentVersion,
		LatestVersion:  summary.LatestVersion,
		Versions:       versions,
	}
}

// environmentVersionResponse 把领域版本转换为对外表示。
func environmentVersionResponse(version domain.EnvironmentVersion) EnvironmentVersionResponse {
	return EnvironmentVersionResponse{
		Version:       version.Label(),
		Number:        version.Number,
		BuildStatus:   version.Status.String(),
		Usable:        version.Usable(),
		ImageRef:      version.ImageRef,
		ImageID:       version.ImageID,
		ParentVersion: version.ParentVersion,
		ErrorSummary:  version.ErrorSummary,
		CreatedAt:     version.CreatedAt,
		BuiltAt:       version.BuiltAt,
	}
}

// writeServiceError 把环境查询错误映射为统一错误响应。
//
// 映射依据错误类别而不是文本：参数非法 400、节点/环境/版本不存在 404，其余按服务
// 内部错误返回 500。存储错误不会被伪装成成功的空列表。
func (h *environmentHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := CodeInternal
	message := "服务内部错误"
	switch {
	case errors.Is(err, app.ErrEnvironmentInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
		message = strings.TrimSpace(err.Error())
	case errors.Is(err, app.ErrEnvironmentHostNotFound),
		errors.Is(err, app.ErrEnvironmentNotFound),
		errors.Is(err, app.ErrEnvironmentVersionNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
		message = strings.TrimSpace(err.Error())
	}
	if status == http.StatusInternalServerError {
		h.logger.ErrorContext(r.Context(), "环境查询接口处理失败", slog.String("error", err.Error()))
	}
	writeError(w, r, h.logger, status, code, message)
}
