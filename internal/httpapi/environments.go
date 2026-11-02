package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 环境接口路径。
//
// 路径形状与执行节点接口保持一致：集合路径 + 单资源子路径。环境引用由两个路径段
// （type 与 name）表达，不使用 domain.EnvironmentRef.Label() 的 "type/name" 单段形式：
// 斜杠在 URL 中是路径分隔符，把它当作单一路径段会依赖隐式的转义解码规则。
const (
	// PathEnvironments 是环境集合路径。
	PathEnvironments = APIPrefix + "/environments"
	// environmentPathPrefix 是环境资源路径前缀。
	environmentPathPrefix = PathEnvironments + "/"
	// environmentInitPath 是环境工作空间初始化路径。
	environmentInitPath = PathEnvironments + "/init"
	// environmentVersionsSegment 是版本集合的子路径段。
	environmentVersionsSegment = "versions"
	// environmentBuildSegment 是触发构建的子路径段。
	environmentBuildSegment = "build"
	// environmentDockerfileSegment 是 Dockerfile 读取的子路径段。
	environmentDockerfileSegment = "dockerfile"
	// environmentBuildLogSegment 是构建日志读取的子路径段。
	environmentBuildLogSegment = "build-log"
	// environmentBuildStatusSegment 是构建状态查询的子路径段。
	environmentBuildStatusSegment = "build-status"
)

// maxEnvironmentRequestBytes 是环境写接口的请求体上限。
//
// 单个上下文文件（Dockerfile 或额外文件）另有 docker.MaxContextFileBytes 限制，
// 整个构建上下文另有 docker.MaxContextBytes 限制；这里只拦住明显超出范围的请求体，
// 避免把不受限的内容读入内存。
const maxEnvironmentRequestBytes = 8 << 20

// EnvironmentWriter 是环境写路径的同步能力集合。
//
// 由 app.EnvironmentService 实现：初始化工作空间、写入不可覆盖的新版本、读取远程
// Dockerfile 与构建日志都由该服务承担。HTTP 层只做协议转换，不建立 SSH 连接、
// 不拼接远程路径、也不调用 Docker。
type EnvironmentWriter interface {
	// Init 初始化远程环境工作空间。
	Init(ctx context.Context, options app.InitOptions) (app.InitResult, error)
	// CreateVersion 写入一个新的不可覆盖的环境版本。
	CreateVersion(
		ctx context.Context,
		request app.CreateVersionRequest,
	) (domain.EnvironmentVersion, error)
	// ReadDockerfile 读取指定版本的 Dockerfile 正文。
	ReadDockerfile(
		ctx context.Context,
		request app.ReadDockerfileRequest,
	) (app.DockerfileContent, error)
	// ReadBuildLog 读取指定版本的构建日志正文。
	ReadBuildLog(ctx context.Context, request app.ReadBuildLogRequest) (app.BuildLog, error)
}

// EnvironmentBuilder 是受理后台环境构建的最小能力。
//
// 由 app.EnvironmentBuildManager 实现：受理后构建在后台执行，响应只表达"已受理"，
// 不代表镜像已经构建成功。
type EnvironmentBuilder interface {
	// Submit 受理一次后台构建。
	Submit(ctx context.Context, request app.BuildRequest) (app.EnvironmentBuildTask, error)
	// Status 报告指定环境当前是否有在途构建。
	Status(
		hostID domain.ID,
		ref domain.EnvironmentRef,
	) (app.EnvironmentBuildTask, bool)
}

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
	// BuildStatus 是真实构建状态：pending / building / succeeded / failed / uncertain。
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

// EnvironmentInitRequest 是环境工作空间初始化的请求体。
type EnvironmentInitRequest struct {
	// HostID 是目标执行节点。
	HostID string `json:"host_id"`
	// WithLanguages 是同时按模板初始化的语言环境名称。
	WithLanguages []string `json:"with_languages,omitempty"`
}

// EnvironmentPreflightResponse 是远程 Docker 预检的对外表示。
type EnvironmentPreflightResponse struct {
	// Ready 表示预检全部通过。
	Ready bool `json:"ready"`
	// ErrorCode 是预检失败的类别，成功时为空。
	ErrorCode string `json:"error_code,omitempty"`
	// Detail 是预检失败的可读说明。
	Detail string `json:"detail,omitempty"`
	// CLIVersion 是远程 Docker CLI 版本。
	CLIVersion string `json:"cli_version,omitempty"`
	// EngineVersion 是远程 Docker Engine 版本。
	EngineVersion string `json:"engine_version,omitempty"`
	// APIVersion 是 Engine 声明的最高 API 版本。
	APIVersion string `json:"api_version,omitempty"`
	// WorkspaceWritable 表示远程工作空间可写。
	WorkspaceWritable bool `json:"workspace_writable"`
	// FreeDiskBytes 是远程工作空间的剩余空间。
	FreeDiskBytes int64 `json:"free_disk_bytes"`
}

// EnvironmentInitResponse 是初始化结果的对外表示。
type EnvironmentInitResponse struct {
	// HostID 是目标执行节点。
	HostID string `json:"host_id"`
	// HostName 是目标执行节点名称。
	HostName string `json:"host_name"`
	// WorkspaceRoot 是解析后的远程工作空间根路径。
	WorkspaceRoot string `json:"workspace_root"`
	// Preflight 是本次预检的真实结果。
	Preflight EnvironmentPreflightResponse `json:"preflight"`
	// Created 是本次新建的远程路径。
	Created []string `json:"created"`
	// Environments 是初始化后存在的环境引用。
	Environments []string `json:"environments"`
}

// EnvironmentContextFileRequest 是构建上下文中的额外文件。
type EnvironmentContextFileRequest struct {
	// Path 是相对构建上下文的路径，服务端会再次规范化与校验。
	Path string `json:"path"`
	// Content 是文件内容。
	Content string `json:"content"`
	// Mode 是文件权限位，省略时使用 0644。
	Mode *uint32 `json:"mode,omitempty"`
}

// EnvironmentCreateVersionRequest 是创建环境版本的请求体。
//
// 提交的永远是"新版本的内容"：历史版本不会被修改，内容与已有成功版本一致时由
// 应用层复用既有版本号。
type EnvironmentCreateVersionRequest struct {
	// HostID 是目标执行节点。
	HostID string `json:"host_id"`
	// Dockerfile 是新版本的 Dockerfile 全文。
	Dockerfile string `json:"dockerfile"`
	// ParentVersion 是语言环境依赖的父基础环境版本标签。
	ParentVersion string `json:"parent_version,omitempty"`
	// ExtraFiles 是构建上下文中的额外文件。
	ExtraFiles []EnvironmentContextFileRequest `json:"extra_files,omitempty"`
}

// EnvironmentBuildRequest 是触发环境构建的请求体。
type EnvironmentBuildRequest struct {
	// HostID 是目标执行节点。
	HostID string `json:"host_id"`
	// Version 是要构建的版本号；必填，不接受"最新版本"的隐式语义。
	Version int `json:"version"`
	// NoCache 表示禁用 Docker 构建缓存。
	NoCache bool `json:"no_cache,omitempty"`
}

// EnvironmentBuildResponse 是受理后台构建的响应。
//
// InProgress 只表示服务端已经受理并在后台执行，不代表构建成功：构建结论必须由
// 构建状态接口返回的真实状态表达。
type EnvironmentBuildResponse struct {
	// Version 是受理时的版本快照，构建状态可能仍是受理前的真实值。
	Version EnvironmentVersionResponse `json:"version"`
	// VersionNumber 是被构建的版本号。
	VersionNumber int `json:"version_number"`
	// AcceptedAt 是服务端受理时间。
	AcceptedAt time.Time `json:"accepted_at"`
	// InProgress 表示构建已在后台执行。
	InProgress bool `json:"in_progress"`
}

// EnvironmentBuildStatusResponse 是构建状态查询的响应。
type EnvironmentBuildStatusResponse struct {
	// Version 是版本元数据，构建状态取自数据库真实记录。
	Version EnvironmentVersionResponse `json:"version"`
	// InProgress 表示本服务当前确实有一个在途构建在执行该环境。
	InProgress bool `json:"in_progress"`
	// AcceptedAt 是在途构建的受理时间，没有在途构建时为空。
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
}

// EnvironmentDockerfileResponse 是 Dockerfile 内容的对外表示。
type EnvironmentDockerfileResponse struct {
	// Type 是环境类型。
	Type string `json:"type"`
	// Name 是环境名称。
	Name string `json:"name"`
	// Version 是版本标签。
	Version string `json:"version"`
	// VersionNumber 是版本号。
	VersionNumber int `json:"version_number"`
	// Content 是 Dockerfile 全文。
	Content string `json:"content"`
	// RemotePath 是 Dockerfile 在远程工作空间中的路径，只用于诊断展示。
	RemotePath string `json:"remote_path"`
}

// EnvironmentBuildLogResponse 是构建日志内容的对外表示。
type EnvironmentBuildLogResponse struct {
	// Type 是环境类型。
	Type string `json:"type"`
	// Name 是环境名称。
	Name string `json:"name"`
	// Version 是版本标签。
	Version string `json:"version"`
	// VersionNumber 是版本号。
	VersionNumber int `json:"version_number"`
	// Content 是构建日志全文。
	Content string `json:"content"`
	// RemotePath 是日志在远程工作空间中的路径，只用于诊断展示。
	RemotePath string `json:"remote_path"`
}

// environmentHandler 处理环境查询与环境管理请求。
type environmentHandler struct {
	// queries 是只读环境查询；为空时读接口返回服务不可用。
	queries *app.EnvironmentQueryService
	// writer 是同步写路径；为空时初始化、创建版本与远程读取返回服务不可用。
	writer EnvironmentWriter
	// builds 是后台构建；为空时构建接口返回服务不可用。
	builds EnvironmentBuilder
	logger *slog.Logger
}

// newEnvironmentHandler 构造环境处理器。
func newEnvironmentHandler(
	queries *app.EnvironmentQueryService,
	writer EnvironmentWriter,
	builds EnvironmentBuilder,
	logger *slog.Logger,
) *environmentHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &environmentHandler{queries: queries, writer: writer, builds: builds, logger: logger}
}

// serve 分发环境请求。
//
// 支持以下形状：
//
//	GET  /api/v1/environments?host_id=…
//	POST /api/v1/environments/init
//	GET  /api/v1/environments/{type}/{name}?host_id=…
//	POST /api/v1/environments/{type}/{name}/versions
//	POST /api/v1/environments/{type}/{name}/build
//	GET  /api/v1/environments/{type}/{name}/versions/{number}?host_id=…
//	GET  /api/v1/environments/{type}/{name}/versions/{number}/dockerfile?host_id=…
//	GET  /api/v1/environments/{type}/{name}/versions/{number}/build-log?host_id=…
//	GET  /api/v1/environments/{type}/{name}/versions/{number}/build-status?host_id=…
//
// 其余形状一律 404；形状正确但取值非法返回 400，不会被伪装成"资源不存在"。
func (h *environmentHandler) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == PathEnvironments {
		h.serveList(w, r)
		return
	}
	if r.URL.Path == environmentInitPath {
		h.serveInit(w, r)
		return
	}
	target := parseEnvironmentPath(r.URL.Path)
	if !target.matched {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	if target.err != nil {
		writeError(w, r, h.logger, http.StatusBadRequest, CodeInvalidRequest, target.err.Error())
		return
	}
	switch target.kind {
	case environmentPathDefinition:
		h.serveDefinition(w, r, target.ref)
	case environmentPathVersions:
		h.serveCreateVersion(w, r, target.ref)
	case environmentPathBuild:
		h.serveBuild(w, r, target.ref)
	case environmentPathVersion:
		h.serveVersion(w, r, target.ref, target.number)
	case environmentPathDockerfile:
		h.serveDockerfile(w, r, target.ref, target.number)
	case environmentPathBuildLog:
		h.serveBuildLog(w, r, target.ref, target.number)
	case environmentPathBuildStatus:
		h.serveBuildStatus(w, r, target.ref, target.number)
	default:
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
	}
}

// serveList 处理 GET /api/v1/environments。
func (h *environmentHandler) serveList(w http.ResponseWriter, r *http.Request) {
	if !h.allowMethods(w, r, "GET, HEAD") {
		return
	}
	if !h.requireQueries(w, r) {
		return
	}
	hostID, ok := h.requireHostID(w, r)
	if !ok {
		return
	}
	summaries, err := h.queries.List(r.Context(), app.ListRequest{MachineID: hostID})
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

// serveDefinition 处理 GET /api/v1/environments/{type}/{name}。
func (h *environmentHandler) serveDefinition(
	w http.ResponseWriter,
	r *http.Request,
	ref domain.EnvironmentRef,
) {
	if !h.allowMethods(w, r, "GET, HEAD") {
		return
	}
	if !h.requireQueries(w, r) {
		return
	}
	hostID, ok := h.requireHostID(w, r)
	if !ok {
		return
	}
	summary, err := h.queries.Get(r.Context(), app.GetRequest{MachineID: hostID, Ref: ref})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, environmentResponse(summary))
}

// serveVersion 处理单个环境版本的查询。
func (h *environmentHandler) serveVersion(
	w http.ResponseWriter,
	r *http.Request,
	ref domain.EnvironmentRef,
	number int,
) {
	if !h.allowMethods(w, r, "GET, HEAD") {
		return
	}
	if !h.requireQueries(w, r) {
		return
	}
	hostID, ok := h.requireHostID(w, r)
	if !ok {
		return
	}
	version, err := h.queries.GetVersion(r.Context(), app.GetVersionRequest{
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

// serveInit 处理 POST /api/v1/environments/init。
func (h *environmentHandler) serveInit(w http.ResponseWriter, r *http.Request) {
	if !h.allowMethods(w, r, "POST") {
		return
	}
	if h.writer == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeEnvironmentUnavailable,
			"环境管理服务未装配",
		)
		return
	}
	var body EnvironmentInitRequest
	if !decodeEnvironmentJSONBody(w, r, h.logger, &body) {
		return
	}
	result, err := h.writer.Init(r.Context(), app.InitOptions{
		MachineID:     strings.TrimSpace(body.HostID),
		WithLanguages: body.WithLanguages,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, environmentInitResponse(result))
}

// serveCreateVersion 处理 POST /api/v1/environments/{type}/{name}/versions。
//
// 返回 201 表示写入了新版本；内容与已有成功版本一致时应用层会复用该版本，此时
// 仍返回版本元数据，但版本号不会增加。
func (h *environmentHandler) serveCreateVersion(
	w http.ResponseWriter,
	r *http.Request,
	ref domain.EnvironmentRef,
) {
	if !h.allowMethods(w, r, "POST") {
		return
	}
	if h.writer == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeEnvironmentUnavailable,
			"环境管理服务未装配",
		)
		return
	}
	var body EnvironmentCreateVersionRequest
	if !decodeEnvironmentJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toAppRequest(ref)
	if err != nil {
		writeError(w, r, h.logger, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	version, err := h.writer.CreateVersion(r.Context(), request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusCreated, environmentVersionResponse(version))
}

// serveBuild 处理 POST /api/v1/environments/{type}/{name}/build。
//
// 构建在后台执行，响应固定为 202 且 in_progress=true：它只表示"已受理"，
// 构建结论必须通过构建状态接口读取真实记录。
func (h *environmentHandler) serveBuild(
	w http.ResponseWriter,
	r *http.Request,
	ref domain.EnvironmentRef,
) {
	if !h.allowMethods(w, r, "POST") {
		return
	}
	if h.writer == nil || h.builds == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeEnvironmentUnavailable,
			"环境构建服务未装配",
		)
		return
	}
	var body EnvironmentBuildRequest
	if !decodeEnvironmentJSONBody(w, r, h.logger, &body) {
		return
	}
	if body.Version < domain.MinEnvironmentVersionNumber ||
		body.Version > domain.MaxEnvironmentVersionNumber {
		writeError(
			w,
			r,
			h.logger,
			http.StatusBadRequest,
			CodeInvalidRequest,
			fmt.Sprintf(
				"version 必须在 %d-%d 之间: %d",
				domain.MinEnvironmentVersionNumber,
				domain.MaxEnvironmentVersionNumber,
				body.Version,
			),
		)
		return
	}
	task, err := h.builds.Submit(r.Context(), app.BuildRequest{
		MachineID:     strings.TrimSpace(body.HostID),
		Ref:           ref,
		VersionNumber: body.Version,
		NoCache:       body.NoCache,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	// 版本快照来自应用层受理时读到的真实记录；它只说明"受理的是哪个版本"，
	// 构建结论必须由构建状态接口读取。
	writeJSON(w, r, h.logger, http.StatusAccepted, EnvironmentBuildResponse{
		Version:       environmentVersionResponse(task.Version),
		VersionNumber: task.VersionNumber,
		AcceptedAt:    task.AcceptedAt,
		InProgress:    true,
	})
}

// serveDockerfile 处理指定版本 Dockerfile 的读取。
func (h *environmentHandler) serveDockerfile(
	w http.ResponseWriter,
	r *http.Request,
	ref domain.EnvironmentRef,
	number int,
) {
	if !h.allowMethods(w, r, "GET, HEAD") {
		return
	}
	if h.writer == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeEnvironmentUnavailable,
			"环境管理服务未装配",
		)
		return
	}
	hostID, ok := h.requireHostID(w, r)
	if !ok {
		return
	}
	content, err := h.writer.ReadDockerfile(r.Context(), app.ReadDockerfileRequest{
		MachineID:     hostID,
		Ref:           ref,
		VersionNumber: number,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, EnvironmentDockerfileResponse{
		Type:          content.Version.Ref.Type.String(),
		Name:          content.Version.Ref.Name,
		Version:       content.Version.Label(),
		VersionNumber: content.Version.Number,
		Content:       string(content.Content),
		RemotePath:    content.RemotePath,
	})
}

// serveBuildLog 处理指定版本构建日志的读取。
//
// 日志位置由服务端从已验证的节点、环境与版本记录推导，请求不接受任何路径参数。
func (h *environmentHandler) serveBuildLog(
	w http.ResponseWriter,
	r *http.Request,
	ref domain.EnvironmentRef,
	number int,
) {
	if !h.allowMethods(w, r, "GET, HEAD") {
		return
	}
	if h.writer == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeEnvironmentUnavailable,
			"环境管理服务未装配",
		)
		return
	}
	hostID, ok := h.requireHostID(w, r)
	if !ok {
		return
	}
	log, err := h.writer.ReadBuildLog(r.Context(), app.ReadBuildLogRequest{
		MachineID:     hostID,
		Ref:           ref,
		VersionNumber: number,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, EnvironmentBuildLogResponse{
		Type:          log.Version.Ref.Type.String(),
		Name:          log.Version.Ref.Name,
		Version:       log.Version.Label(),
		VersionNumber: log.Version.Number,
		Content:       string(log.Content),
		RemotePath:    log.RemotePath,
	})
}

// serveBuildStatus 处理指定版本构建状态的查询。
//
// 状态码表达真实结论，不用 200 掩盖失败：
//   - 成功：200；
//   - 待构建或构建中：202，in_progress 说明本服务是否确有在途构建；
//   - 构建失败：500 + environment_build_failed，message 为服务端记录的原因；
//   - 构建结果不确定：500 + environment_build_uncertain。
func (h *environmentHandler) serveBuildStatus(
	w http.ResponseWriter,
	r *http.Request,
	ref domain.EnvironmentRef,
	number int,
) {
	if !h.allowMethods(w, r, "GET, HEAD") {
		return
	}
	if !h.requireQueries(w, r) {
		return
	}
	hostID, ok := h.requireHostID(w, r)
	if !ok {
		return
	}
	version, err := h.queries.GetVersion(r.Context(), app.GetVersionRequest{
		MachineID: hostID,
		Ref:       ref,
		Number:    number,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	payload := EnvironmentBuildStatusResponse{Version: environmentVersionResponse(version)}
	if h.builds != nil {
		if task, running := h.builds.Status(version.HostID, version.Ref); running {
			acceptedAt := task.AcceptedAt
			payload.InProgress = true
			payload.AcceptedAt = &acceptedAt
		}
	}
	switch version.Status {
	case domain.BuildStatusSucceeded:
		writeJSON(w, r, h.logger, http.StatusOK, payload)
	case domain.BuildStatusPending, domain.BuildStatusBuilding:
		writeJSON(w, r, h.logger, http.StatusAccepted, payload)
	case domain.BuildStatusFailed:
		message := strings.TrimSpace(version.ErrorSummary)
		if message == "" {
			message = "构建失败，服务端未记录原因；请查看构建日志"
		}
		writeJSON(w, r, h.logger, http.StatusInternalServerError, errorResponse(
			CodeEnvironmentBuildFailed,
			message,
			requestIDFrom(r.Context()),
		))
	case domain.BuildStatusUncertain:
		message := strings.TrimSpace(version.ErrorSummary)
		if message == "" {
			message = "构建结果无法确认，请显式重新构建"
		}
		writeJSON(w, r, h.logger, http.StatusInternalServerError, errorResponse(
			CodeEnvironmentBuildUncertain,
			message,
			requestIDFrom(r.Context()),
		))
	default:
		// 未知取值原样报告，不猜测成成功或失败。
		writeJSON(w, r, h.logger, http.StatusAccepted, payload)
	}
}

// allowMethods 校验请求方法，不满足时写出 405 并返回 false。
func (h *environmentHandler) allowMethods(
	w http.ResponseWriter,
	r *http.Request,
	allow string,
) bool {
	for _, method := range strings.Split(allow, ",") {
		if r.Method == strings.TrimSpace(method) {
			return true
		}
	}
	w.Header().Set("Allow", allow)
	writeError(
		w,
		r,
		h.logger,
		http.StatusMethodNotAllowed,
		CodeMethodNotAllowed,
		"该接口只支持 "+allow+" 请求",
	)
	return false
}

// requireQueries 确认只读查询服务已装配。
func (h *environmentHandler) requireQueries(w http.ResponseWriter, r *http.Request) bool {
	if h.queries != nil {
		return true
	}
	writeError(w, r, h.logger, http.StatusServiceUnavailable, CodeInternal, "环境查询服务未初始化")
	return false
}

// requireQueryHostID 读取并校验 host_id 查询参数。
//
// host_id 是环境查询的唯一节点定位方式：缺少它无法确定要查询哪台机器上的环境，
// 因此按参数错误返回 400，而不是隐式回退到默认节点。
func (h *environmentHandler) requireHostID(w http.ResponseWriter, r *http.Request) (string, bool) {
	hostID := strings.TrimSpace(r.URL.Query().Get("host_id"))
	if hostID == "" {
		writeError(
			w,
			r,
			h.logger,
			http.StatusBadRequest,
			CodeInvalidRequest,
			"缺少查询参数 host_id",
		)
		return "", false
	}
	return hostID, true
}

// environmentPathKind 是环境接口路径的业务形状。
type environmentPathKind int

// 环境接口路径形状取值。
//
// 零值是"未匹配任何形状"，因此路径解析失败时不需要额外分支：调用方只看 matched。
const (
	// environmentPathDefinition 是 {type}/{name}。
	environmentPathDefinition environmentPathKind = iota
	// environmentPathVersions 是 {type}/{name}/versions。
	environmentPathVersions
	// environmentPathBuild 是 {type}/{name}/build。
	environmentPathBuild
	// environmentPathVersion 是 {type}/{name}/versions/{number}。
	environmentPathVersion
	// environmentPathDockerfile 是 {type}/{name}/versions/{number}/dockerfile。
	environmentPathDockerfile
	// environmentPathBuildLog 是 {type}/{name}/versions/{number}/build-log。
	environmentPathBuildLog
	// environmentPathBuildStatus 是 {type}/{name}/versions/{number}/build-status。
	environmentPathBuildStatus
)

// environmentPath 是一条环境接口路径的解析结果。
type environmentPath struct {
	// matched 表示路径形状确实属于环境接口。
	matched bool
	// kind 是匹配到的资源形状。
	kind environmentPathKind
	// ref 是解析出的环境引用。
	ref domain.EnvironmentRef
	// number 是版本号，仅版本相关形状有效。
	number int
	// err 表示路径形状正确但取值非法（环境类型、名称或版本号）。
	err error
}

// parseEnvironmentPath 解析 {type}/{name}[/…] 形状的路径。
//
// 解析严格按路径段数量与位置进行，并区分两类失败：
//   - 形状不符（段数不对、缺少 versions 段、未知子资源名）：matched=false，按 404 处理；
//   - 形状正确但取值非法（未知环境类型、非法名称、非法版本号）：err 非空，按 400 处理。
//
// 这样既不会把形状错误的路径误当作合法资源，也不会把参数错误伪装成"资源不存在"。
func parseEnvironmentPath(path string) environmentPath {
	remainder := strings.TrimPrefix(path, environmentPathPrefix)
	if remainder == path {
		return environmentPath{}
	}
	segments := splitPathSegments(remainder)
	if len(segments) < 2 || len(segments) > 5 {
		return environmentPath{}
	}
	ref, err := environmentRefFromSegments(segments[0], segments[1])
	if err != nil {
		return environmentPath{matched: true, err: err}
	}
	switch len(segments) {
	case 2:
		return environmentPath{kind: environmentPathDefinition, matched: true, ref: ref}
	case 3:
		switch segments[2] {
		case environmentVersionsSegment:
			return environmentPath{kind: environmentPathVersions, matched: true, ref: ref}
		case environmentBuildSegment:
			return environmentPath{kind: environmentPathBuild, matched: true, ref: ref}
		default:
			return environmentPath{}
		}
	case 4:
		number, numberErr := parseEnvironmentVersionNumber(segments[3])
		if numberErr != nil {
			return environmentPath{matched: true, ref: ref, err: numberErr}
		}
		return environmentPath{
			kind:    environmentPathVersion,
			matched: true,
			ref:     ref,
			number:  number,
		}
	default:
		if segments[2] != environmentVersionsSegment {
			return environmentPath{}
		}
		number, numberErr := parseEnvironmentVersionNumber(segments[3])
		if numberErr != nil {
			return environmentPath{matched: true, ref: ref, err: numberErr}
		}
		var kind environmentPathKind
		switch segments[4] {
		case environmentDockerfileSegment:
			kind = environmentPathDockerfile
		case environmentBuildLogSegment:
			kind = environmentPathBuildLog
		case environmentBuildStatusSegment:
			kind = environmentPathBuildStatus
		default:
			return environmentPath{}
		}
		return environmentPath{kind: kind, matched: true, ref: ref, number: number}
	}
}

// parseEnvironmentVersionNumber 解析并校验版本号路径段。
func parseEnvironmentVersionNumber(raw string) (int, error) {
	number, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("版本号必须是整数: " + raw)
	}
	if number < domain.MinEnvironmentVersionNumber || number > domain.MaxEnvironmentVersionNumber {
		return 0, fmt.Errorf(
			"版本号必须在 %d-%d 之间: %d",
			domain.MinEnvironmentVersionNumber,
			domain.MaxEnvironmentVersionNumber,
			number,
		)
	}
	return number, nil
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

// toAppRequest 把创建版本请求转换为应用层输入。
//
// 本地校验只覆盖"不需要远程状态就能判断"的部分：Dockerfile 语法、上下文文件路径与
// 权限位、请求体必填字段。节点归属、父版本可用性与版本号分配仍由应用层决定。
func (b EnvironmentCreateVersionRequest) toAppRequest(
	ref domain.EnvironmentRef,
) (app.CreateVersionRequest, error) {
	hostID := strings.TrimSpace(b.HostID)
	if hostID == "" {
		return app.CreateVersionRequest{}, errors.New("host_id 不能为空")
	}
	dockerfile := []byte(b.Dockerfile)
	if err := docker.ValidateDockerfile(dockerfile); err != nil {
		return app.CreateVersionRequest{}, err
	}
	extra := make([]docker.ContextFile, 0, len(b.ExtraFiles))
	for _, file := range b.ExtraFiles {
		mode := uint32(0o644)
		if file.Mode != nil {
			mode = *file.Mode
		}
		if mode > 0o777 {
			return app.CreateVersionRequest{}, fmt.Errorf(
				"额外文件 %s 的权限位不合法: %o",
				file.Path,
				mode,
			)
		}
		extra = append(extra, docker.ContextFile{
			RelativePath: file.Path,
			Content:      []byte(file.Content),
			Mode:         os.FileMode(mode),
		})
	}
	return app.CreateVersionRequest{
		MachineID:     hostID,
		Ref:           ref,
		Dockerfile:    dockerfile,
		ParentVersion: strings.TrimSpace(b.ParentVersion),
		ExtraFiles:    extra,
	}, nil
}

// environmentInitResponse 把初始化结果转换为对外表示。
func environmentInitResponse(result app.InitResult) EnvironmentInitResponse {
	environments := make([]string, 0, len(result.Environments))
	for _, ref := range result.Environments {
		environments = append(environments, ref.Label())
	}
	created := append([]string(nil), result.Created...)
	return EnvironmentInitResponse{
		HostID:        result.Host.ID.String(),
		HostName:      result.Host.Name,
		WorkspaceRoot: result.WorkspaceRoot,
		Preflight: EnvironmentPreflightResponse{
			Ready:             result.Preflight.Ready(),
			ErrorCode:         result.Preflight.ErrorCode.String(),
			Detail:            result.Preflight.Detail,
			CLIVersion:        result.Preflight.CLIVersion,
			EngineVersion:     result.Preflight.EngineVersion,
			APIVersion:        result.Preflight.APIVersion,
			WorkspaceWritable: result.Preflight.WorkspaceWritable,
			FreeDiskBytes:     result.Preflight.FreeDiskBytes,
		},
		Created:      created,
		Environments: environments,
	}
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

// writeServiceError 把环境服务错误映射为统一错误响应。
//
// 映射依据错误类别而不是文本：参数非法 400、目标不存在 404、构建进行中或状态冲突
// 409、节点未通过预检 412、服务未装配或已停止 503，其余按服务内部错误返回 500。
// 存储错误不会被伪装成成功的空列表。
func (h *environmentHandler) writeServiceError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	status := http.StatusInternalServerError
	code := CodeInternal
	message := "服务内部错误"
	switch {
	case errors.Is(err, app.ErrEnvironmentInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID),
		errors.Is(err, docker.ErrInvalidContext):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
		message = strings.TrimSpace(err.Error())
	case errors.Is(err, app.ErrEnvironmentHostNotFound),
		errors.Is(err, app.ErrEnvironmentNotFound),
		errors.Is(err, app.ErrEnvironmentVersionNotFound),
		errors.Is(err, app.ErrEnvironmentBuildLogMissing),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
		message = strings.TrimSpace(err.Error())
	case errors.Is(err, app.ErrEnvironmentBuildInProgress):
		status = http.StatusConflict
		code = CodeEnvironmentBuildInProgress
		message = strings.TrimSpace(err.Error())
	case errors.Is(err, storage.ErrConflict),
		errors.Is(err, domain.ErrInvalidState):
		status = http.StatusConflict
		code = CodeConflict
		message = strings.TrimSpace(err.Error())
	case errors.Is(err, app.ErrHostValidationUnavailable):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
		message = strings.TrimSpace(err.Error())
	case errors.Is(err, app.ErrEnvironmentBuildStopped):
		status = http.StatusServiceUnavailable
		code = CodeEnvironmentUnavailable
		message = strings.TrimSpace(err.Error())
	}
	if status == http.StatusInternalServerError {
		h.logger.ErrorContext(r.Context(), "环境接口处理失败", slog.String("error", err.Error()))
	}
	writeError(w, r, h.logger, status, code, message)
}

// decodeEnvironmentJSONBody 解析环境写接口的请求体，失败时写出 400 并返回 false。
func decodeEnvironmentJSONBody(
	w http.ResponseWriter,
	r *http.Request,
	logger *slog.Logger,
	target any,
) bool {
	limited := http.MaxBytesReader(w, r.Body, maxEnvironmentRequestBytes)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		message := "请求体不是合法的 JSON"
		var maxBytesErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			message = "请求体不能为空"
		case errors.As(err, &maxBytesErr):
			message = fmt.Sprintf("请求体超过上限 %d 字节", maxEnvironmentRequestBytes)
		}
		writeError(w, r, logger, http.StatusBadRequest, CodeInvalidRequest, message)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, r, logger, http.StatusBadRequest, CodeInvalidRequest, "请求体只能包含一个 JSON 对象")
		return false
	}
	return true
}
