package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 任务容器接口的路径段。
//
// 两条路径都挂在单任务之下，形状不同：
//   - `{taskID}/containers` 是只读的容器记录列表（2 段）；
//   - `{taskID}/containers/{containerID}/delete` 与 `.../detach` 是显式处置动作（4 段）。
//
// 动作放在容器标识之后，使"操作对象"在路径上显式可见；处置动作必须同时给出
// 任务与容器两个标识，而归属是否成立完全由应用层依据数据库记录判定。
const (
	// containersSegment 是任务下容器记录的集合路径段。
	containersSegment = "containers"
	// containerDeleteSegment 是删除容器的动作路径段。
	containerDeleteSegment = "delete"
	// containerDetachSegment 是脱离管理的动作路径段。
	containerDetachSegment = "detach"
)

// TaskContainerResponse 是一条任务容器的对外表示。
//
// 字段全部来自数据库中的容器登记记录，只暴露"处置时需要看到的事实"：
//   - 管理状态、Docker 观测状态、脱离标记与结束原因分别输出，不压缩成一个结论；
//   - `origin` 说明这条记录与任务的关联方式（当前代次 / 历史代次），前端不得自行猜测；
//   - 不输出镜像引用、构建哈希、执行节点凭据或任何远程连接信息。
type TaskContainerResponse struct {
	// ID 是容器登记记录标识，也是两个处置动作路径里的标识。
	ID string `json:"id"`
	// TaskID 是登记的任务归属，未登记时为空。
	TaskID string `json:"task_id,omitempty"`
	// Name 是容器名称。
	Name string `json:"name"`
	// State 是 AgentFlow 的容器管理状态。
	State string `json:"state"`
	// DockerState 是最近一次观测到的 Docker 状态。
	DockerState string `json:"docker_state"`
	// Detached 表示容器已脱离 AgentFlow 自动管理。
	Detached bool `json:"detached"`
	// DetachedAt 是脱离管理的时间，未脱离时缺省。
	DetachedAt *time.Time `json:"detached_at,omitempty"`
	// EndReason 是生命周期结束原因，未结束时为空字符串。
	EndReason string `json:"end_reason"`
	// Origin 是该记录与任务的关联方式：current 或 history。
	Origin string `json:"origin"`
	// CreatedAt 是容器创建时间。
	CreatedAt time.Time `json:"created_at"`
	// FinishedAt 是生命周期结束时间，未结束时缺省。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// UpdatedAt 是记录最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskContainerListResponse 是任务下容器记录的列表响应。
//
// 与代码保存记录列表一致使用 `records` + `total`：容器记录按任务归属且数量有限，
// 因此不引入分页，`total` 即本次返回的记录数。
type TaskContainerListResponse struct {
	Records []TaskContainerResponse `json:"records"`
	Total   int                     `json:"total"`
}

// ContainerDisposalResponse 是一次显式处置的响应。
//
// 三个事实分别输出，不得互相替代：
//   - `outcome` 是本次处置的应用层结论（completed / failed / uncertain）；
//   - `action` 是本次动作（delete / detach），使响应自证语义，不与"停止"混淆；
//   - `container` 是处置后重新读取的真实记录，而不是请求前的旧副本。
type ContainerDisposalResponse struct {
	// TaskID 是任务标识。
	TaskID string `json:"task_id"`
	// Action 是本次动作。
	Action string `json:"action"`
	// Outcome 是本次处置结论。
	Outcome string `json:"outcome"`
	// Detail 是可读说明；失败时是服务端真实原因。
	Detail string `json:"detail,omitempty"`
	// Container 是处置后的容器登记记录。
	Container TaskContainerResponse `json:"container"`
}

// containerHandler 处理任务容器的只读查询与显式处置。
type containerHandler struct {
	service *app.TaskContainerDisposalService
	logger  *slog.Logger
}

// isTaskContainerPath 判断路径是否指向任务容器接口。
//
// 判定按路径段形状进行：`{taskID}/containers`（2 段）与
// `{taskID}/containers/{containerID}/{delete|detach}`（4 段）属于本接口；
// 其余形状仍由任务处理器给出 404 / 405，不在这里抢占。
func isTaskContainerPath(path string) bool {
	_, ok := parseTaskContainerListPath(path)
	if ok {
		return true
	}
	_, _, _, ok = parseContainerDisposalPath(path)
	return ok
}

// parseTaskContainerListPath 解析 {taskID}/containers 形状的路径。
func parseTaskContainerListPath(path string) (string, bool) {
	taskID, ok := actionTaskID(path, containersSegment)
	if !ok {
		return "", false
	}
	return taskID, true
}

// parseContainerDisposalPath 解析 {taskID}/containers/{containerID}/{action} 形状的路径。
func parseContainerDisposalPath(path string) (taskID, containerID, action string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", "", "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 4 || segments[1] != containersSegment {
		return "", "", "", false
	}
	action = segments[3]
	if action != containerDeleteSegment && action != containerDetachSegment {
		return "", "", "", false
	}
	if !validTaskPathID(segments[0]) || !validTaskPathID(segments[2]) {
		return "", "", "", false
	}
	return segments[0], segments[2], action, true
}

// serveTaskContainers 分发任务容器的查询与处置请求。
func (rt *router) serveTaskContainers(w http.ResponseWriter, r *http.Request) {
	handler := &containerHandler{service: rt.taskContainers, logger: rt.logger}
	if taskID, ok := parseTaskContainerListPath(r.URL.Path); ok {
		handler.list(w, r, taskID)
		return
	}
	taskID, containerID, action, ok := parseContainerDisposalPath(r.URL.Path)
	if !ok {
		writeError(w, r, rt.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	handler.dispose(w, r, taskID, containerID, action)
}

// list 处理 GET /api/v1/tasks/{taskID}/containers。
//
// 只读：不建立 SSH / Docker 会话、不修改记录、不推进任务状态。因此容器列表在
// 服务缺少远程能力时仍然可用，返回的是数据库中的真实登记记录。
func (h *containerHandler) list(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 GET 请求")
		return
	}
	if h.service == nil {
		writeError(w, r, h.logger, http.StatusServiceUnavailable, CodeInternal,
			"任务容器服务未初始化")
		return
	}
	views, err := h.service.TaskContainers(r.Context(), app.TaskContainersRequest{TaskID: taskID})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, taskContainerListResponse(views))
}

// dispose 处理 POST /api/v1/tasks/{taskID}/containers/{containerID}/{delete|detach}。
//
// 两个动作是两个独立接口，不存在"一个模糊的清理动作"：删除会删除容器（以及服务端
// 规则允许清理的任务级临时资源），脱离管理只保留现场并放弃自动管理权。
func (h *containerHandler) dispose(
	w http.ResponseWriter,
	r *http.Request,
	taskID, containerID, action string,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 POST 请求")
		return
	}
	if h.service == nil {
		writeError(w, r, h.logger, http.StatusServiceUnavailable, CodeInternal,
			"任务容器服务未初始化")
		return
	}
	parsed := app.ContainerDisposalAction(action)
	result, err := h.service.Disposal(r.Context(), app.TaskContainerDisposalRequest{
		TaskID:      taskID,
		ContainerID: containerID,
		Action:      parsed,
		Reason:      domain.ContainerEndReasonManual,
	})
	if err != nil {
		// 失败一律使用统一错误结构：调用方按错误代码区分"没有执行"（409）、
		// "远程或本地环节失败、结果不确定"（500 + execution_failed），然后重新读取
		// 容器列表确认真实状态。不在这里返回 2xx，也不把失败伪装成已完成。
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, containerDisposalResponse(result, parsed))
}

// writeServiceError 把应用层错误映射为统一错误响应。
//
// 映射依据错误类别而不是文本：参数错误 400、任务或记录不存在 404、归属不匹配 409、
// 状态不允许 409、远程或本地失败 500、结果不确定 500（`execution_failed`）。
func (h *containerHandler) writeServiceError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	status, code := disposalStatus(err)
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError && code == CodeInternal {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "任务容器接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// disposalStatus 把应用层错误映射为状态码与错误代码。
func disposalStatus(err error) (int, string) {
	switch {
	case errors.Is(err, app.ErrContainerInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		return http.StatusBadRequest, CodeInvalidRequest
	case errors.Is(err, app.ErrContainerTaskNotFound),
		errors.Is(err, app.ErrContainerNotFound),
		errors.Is(err, app.ErrTaskNotFound),
		errors.Is(err, storage.ErrNotFound):
		return http.StatusNotFound, CodeNotFound
	case errors.Is(err, app.ErrContainerOwnershipRejected),
		errors.Is(err, app.ErrContainerOperationRejected),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, storage.ErrConflict):
		return http.StatusConflict, CodeConflict
	case errors.Is(err, app.ErrContainerOperationUncertain):
		// 结果无法确认既不是成功，也不是"什么都没做"：单独归类为执行失败。
		return http.StatusInternalServerError, CodeExecutionFailed
	case errors.Is(err, app.ErrContainerOperationFailed):
		return http.StatusInternalServerError, CodeExecutionFailed
	default:
		return http.StatusInternalServerError, CodeInternal
	}
}

// taskContainerListResponse 把应用层视图转换为列表响应。
func taskContainerListResponse(
	views []app.TaskContainerView,
) TaskContainerListResponse {
	response := TaskContainerListResponse{
		Records: make([]TaskContainerResponse, 0, len(views)),
		Total:   len(views),
	}
	for _, view := range views {
		response.Records = append(response.Records, taskContainerResponse(view.Container, view.Origin))
	}
	return response
}

// containerDisposalResponse 把处置结果转换为对外表示。
func containerDisposalResponse(
	result app.TaskContainerDisposalResult,
	action app.ContainerDisposalAction,
) ContainerDisposalResponse {
	return ContainerDisposalResponse{
		TaskID:  result.TaskID.String(),
		Action:  action.String(),
		Outcome: result.Outcome.String(),
		Detail:  result.Detail,
		// 处置结论里没有关联方式：它描述的是这一条记录本身，origin 由列表接口给出。
		Container: taskContainerResponse(result.Container, ""),
	}
}

// taskContainerResponse 转换单条任务容器记录。
func taskContainerResponse(
	container domain.Container,
	origin app.ContainerOrigin,
) TaskContainerResponse {
	response := TaskContainerResponse{
		ID:          container.ID.String(),
		Name:        container.Name,
		State:       container.State.String(),
		DockerState: container.DockerState.String(),
		Detached:    container.Detached,
		DetachedAt:  container.DetachedAt,
		EndReason:   container.EndReason.String(),
		Origin:      origin.String(),
		CreatedAt:   container.CreatedAt,
		FinishedAt:  container.FinishedAt,
		UpdatedAt:   container.UpdatedAt,
	}
	if container.TaskID != nil {
		response.TaskID = container.TaskID.String()
	}
	return response
}
