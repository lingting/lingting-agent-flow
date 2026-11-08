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

// 规划会话与规划运行时的路径段。
const (
	// PathPlanningSessions 是规划会话集合路径。
	PathPlanningSessions = APIPrefix + "/planning/sessions"
	// planningRuntimeSegment 是规划运行时子资源路径段。
	planningRuntimeSegment = "runtime"
	// planningPrepareSegment 是准备（或复用）规划运行环境的动作路径段。
	planningPrepareSegment = "prepare"
	// planningReleaseSegment 是释放规划运行环境的动作路径段。
	planningReleaseSegment = "release"
)

// 本文件提供规划会话与规划运行时的最小诊断接口与两个显式动作接口。
//
// 本层只做协议转换，不实现任何规划业务：读取会话直接调用
// PlanningSessionService.Get，核验现场调用 PlanningRuntimeService.InspectPlanningRuntime，
// 准备与释放分别调用 Prepare 与 Release。因此接口返回的事实与生产服务完全一致，
// 不存在"接口说资源还在、服务却认为已经释放"的第二套判断。

// PlanningSessionResponse 是单个规划会话的对外表示。
//
// 字段一一对应持久化事实，不做状态归一化：未知状态按原值输出，`host_id` 缺失表示
// 尚未绑定执行节点，`container_id` 与 `source_commit` 缺失表示运行环境尚未准备。
type PlanningSessionResponse struct {
	// ID 是规划会话标识。
	ID string `json:"id"`
	// ProjectID 是会话所属项目标识。
	ProjectID string `json:"project_id"`
	// Status 是持久化的会话状态，未知取值原样输出。
	Status string `json:"status"`
	// HostID 是绑定的执行节点标识，尚未绑定时省略。
	HostID string `json:"host_id,omitempty"`
	// ContainerID 是规划容器登记记录标识，尚未准备时省略。
	ContainerID string `json:"container_id,omitempty"`
	// SourceCommit 是规划容器内代码快照固定的 commit SHA。
	SourceCommit string `json:"source_commit,omitempty"`
	// AnalystSessionID 是需求分析师角色的 Pi 侧会话标识。
	AnalystSessionID string `json:"analyst_session_id,omitempty"`
	// PlannerSessionID 是方案规划师角色的 Pi 侧会话标识。
	PlannerSessionID string `json:"planner_session_id,omitempty"`
	// AdoptedRevisionID 是已采纳的方案版本标识，只有已确认会话才有值。
	AdoptedRevisionID string `json:"adopted_revision_id,omitempty"`
	// ErrorSummary 是最近一次分析失败的摘要。
	ErrorSummary string `json:"error_summary,omitempty"`
	// CreatedAt 是会话创建时间。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 是会话最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
	// FinishedAt 是会话结束时间，未结束时省略。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// PlanningSessionDetailResponse 是单会话查询的响应体。
type PlanningSessionDetailResponse struct {
	Session PlanningSessionResponse `json:"session"`
}

// PlanningRuntimeResourceResponse 是单个远程资源的现场核验结论。
//
// `presence` 的四个取值必须能区分事实与推断：远程读取失败只能是 `unknown`，不会被
// 映射为 `absent`；归属不一致是 `mismatch`，表示现场需要人工处置。
type PlanningRuntimeResourceResponse struct {
	// Presence 是核验结论：present / absent / unknown / mismatch。
	Presence string `json:"presence"`
	// Detail 是可公开的诊断说明。
	Detail string `json:"detail,omitempty"`
}

// PlanningRuntimeContainerResponse 是规划容器与一次性准备容器的现场核验结论。
type PlanningRuntimeContainerResponse struct {
	PlanningRuntimeResourceResponse
	// Name 是用于定位的名称：规划容器用登记名称，准备容器用固定名称。
	Name string `json:"name,omitempty"`
	// DockerID 是远端实际容器 ID，未能确认时为空。
	DockerID string `json:"docker_id,omitempty"`
	// DockerState 是远端 Docker 状态。
	DockerState string `json:"docker_state,omitempty"`
	// Running 表示已确认处于运行状态。
	Running bool `json:"running"`
}

// PlanningRuntimeSnapshotResponse 是固定 commit 快照卷的现场核验结论。
type PlanningRuntimeSnapshotResponse struct {
	PlanningRuntimeResourceResponse
	// VolumeName 是由会话与执行节点推导出的快照卷名称。
	VolumeName string `json:"volume_name,omitempty"`
	// Commit 是只读核验得到的远端实际 commit，未能确认时为空。
	Commit string `json:"commit,omitempty"`
	// CommitVerified 表示远端实际 commit 已经核验。
	CommitVerified bool `json:"commit_verified"`
	// CommitMatches 表示实际 commit 与数据库记录的 SourceCommit 一致。
	CommitMatches bool `json:"commit_matches"`
}

// PlanningRuntimeInspectionResponse 是规划运行时现场核验的响应体。
type PlanningRuntimeInspectionResponse struct {
	// Session 是数据库中的规划会话记录（核验不改写它）。
	Session PlanningSessionResponse `json:"session"`
	// HostID 是会话绑定的执行节点标识，未绑定时省略。
	HostID string `json:"host_id,omitempty"`
	// HostName 是执行节点名称，未绑定时省略。
	HostName string `json:"host_name,omitempty"`
	// Container 是登记规划容器的现场。
	Container PlanningRuntimeContainerResponse `json:"container"`
	// PrepContainer 是一次性快照准备容器的现场。
	PrepContainer PlanningRuntimeContainerResponse `json:"prep_container"`
	// Snapshot 是快照卷的现场。
	Snapshot PlanningRuntimeSnapshotResponse `json:"snapshot"`
}

// PlanningRuntimeDetailResponse 是准备规划运行环境的结果。
type PlanningRuntimeDetailResponse struct {
	// Session 是写入运行期字段后的规划会话。
	Session PlanningSessionResponse `json:"session"`
	// HostID 是本次使用的执行节点标识。
	HostID string `json:"host_id"`
	// HostName 是本次使用的执行节点名称。
	HostName string `json:"host_name,omitempty"`
	// ContainerID 是规划容器登记记录标识。
	ContainerID string `json:"container_id,omitempty"`
	// DockerID 是远端 Docker 容器 ID。
	DockerID string `json:"docker_id,omitempty"`
	// CodeVolumeName 是承载固定 commit 快照的命名卷。
	CodeVolumeName string `json:"code_volume_name,omitempty"`
	// SourceCommit 是快照固定的完整 commit SHA。
	SourceCommit string `json:"source_commit,omitempty"`
	// SnapshotCreated 表示本次新建了快照卷。
	SnapshotCreated bool `json:"snapshot_created"`
	// SnapshotReused 表示复用并核验了已有快照卷。
	SnapshotReused bool `json:"snapshot_reused"`
	// ContainerReused 表示复用并核验了已登记的规划容器。
	ContainerReused bool `json:"container_reused"`
}

// PlanningRuntimeReleaseResponse 是释放规划运行环境的结果。
type PlanningRuntimeReleaseResponse struct {
	// Session 是清空运行时绑定后的规划会话。
	Session PlanningSessionResponse `json:"session"`
	// ContainerReleased 表示规划容器已确认删除或已不存在。
	ContainerReleased bool `json:"container_released"`
	// SnapshotReleased 表示快照卷已确认删除或已不存在。
	SnapshotReleased bool `json:"snapshot_released"`
}

// planningPrepareRequest 是准备规划运行环境的请求体。
//
// 请求体可整体省略：省略时执行节点取自规划会话已持久化的绑定，这正是进程重启后的
// 恢复路径。显式给出 host_id 时仍必须与会话已绑定的节点一致，否则生产服务返回冲突。
type planningPrepareRequest struct {
	// HostID 是可选执行节点标识。
	HostID string `json:"host_id"`
}

// planningHandler 处理规划会话业务、规划运行时诊断与两阶段分析接口。
type planningHandler struct {
	sessions *app.PlanningSessionService
	runtime  *app.PlanningRuntimeService
	analysis *app.PlanningAnalysisService
	logger   *slog.Logger
}

// newPlanningHandler 构造规划接口处理器。
//
// 服务为空时仍返回处理器，接口由路由前置检查统一给出 503。
func newPlanningHandler(
	sessions *app.PlanningSessionService,
	runtime *app.PlanningRuntimeService,
	analysis *app.PlanningAnalysisService,
	logger *slog.Logger,
) *planningHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &planningHandler{
		sessions: sessions,
		runtime:  runtime,
		analysis: analysis,
		logger:   logger,
	}
}

// detail 处理 GET /api/v1/planning/sessions/{sessionID}。
func (h *planningHandler) detail(w http.ResponseWriter, r *http.Request) {
	session, err := h.sessions.Get(r.Context(), pathID(r, paramSessionID))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, PlanningSessionDetailResponse{
		Session: planningSessionResponse(session),
	})
}

// inspectRuntime 处理 GET /api/v1/planning/sessions/{sessionID}/runtime。
//
// 它只读核验远端现场，不创建、启动、停止或删除任何规划资源；现场与数据库绑定不一致
// 时如实返回冲突错误，且不改写数据库中的绑定。
func (h *planningHandler) inspectRuntime(w http.ResponseWriter, r *http.Request) {
	inspection, err := h.runtime.InspectPlanningRuntime(
		r.Context(),
		app.InspectPlanningRuntimeRequest{PlanningSessionID: pathID(r, paramSessionID)},
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, planningInspectionResponse(inspection))
}

// prepareRuntime 处理 POST /api/v1/planning/sessions/{sessionID}/runtime/prepare。
//
// 它复用生产 Prepare：已登记的规划容器与快照卷按既有策略核验并复用，容器已确认退出时
// 重新启动同一个容器；容器或卷与记录不一致时失败关闭，不会创建替代资源。
func (h *planningHandler) prepareRuntime(w http.ResponseWriter, r *http.Request) {
	sessionID := pathID(r, paramSessionID)
	var body planningPrepareRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	hostID, err := h.resolvePrepareHost(r, sessionID, body.HostID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	detail, err := h.runtime.Prepare(r.Context(), app.PreparePlanningRuntimeRequest{
		PlanningSessionID: sessionID,
		HostID:            hostID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, planningRuntimeDetailResponse(detail))
}

// resolvePrepareHost 解析本次准备使用的执行节点标识。
//
// 显式给出的标识优先：是否与会话已绑定的节点一致由生产服务判定，接口层不重复这套规则。
// 未给出时取会话已持久化的绑定，因此进程重启后的恢复不需要调用方重新指定节点。
func (h *planningHandler) resolvePrepareHost(
	r *http.Request,
	sessionID string,
	explicit string,
) (string, error) {
	hostID := strings.TrimSpace(explicit)
	if hostID != "" {
		return hostID, nil
	}
	session, err := h.sessions.Get(r.Context(), sessionID)
	if err != nil {
		return "", err
	}
	if session.HostID == nil {
		return "", errors.Join(
			app.ErrPlanningRuntimeInvalidInput,
			errors.New("规划会话尚未绑定执行节点，准备运行环境必须显式指定 host_id"),
		)
	}
	return session.HostID.String(), nil
}

// releaseRuntime 处理 POST /api/v1/planning/sessions/{sessionID}/runtime/release。
//
// 它复用生产 Release：只删除本会话登记并核验归属的容器与快照卷，释放完成后清空绑定。
func (h *planningHandler) releaseRuntime(w http.ResponseWriter, r *http.Request) {
	result, err := h.runtime.Release(r.Context(), app.ReleasePlanningRuntimeRequest{
		PlanningSessionID: pathID(r, paramSessionID),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, PlanningRuntimeReleaseResponse{
		Session:           planningSessionResponse(result.Session),
		ContainerReleased: result.ContainerReleased,
		SnapshotReleased:  result.SnapshotReleased,
	})
}

// writeError 把规划相关错误映射为统一错误响应。
//
// 类别只决定 HTTP 语义，具体失败位置由消息中的阶段名给出：调用方不需要解析错误文本
// 就能区分"目标不存在""前置条件缺失""现场冲突"与"远程状态不确定"。
func (h *planningHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case isPlanningAnalysisError(err):
		// 两阶段分析已经真实执行但没有产出可用结论：既不能报成功，也不能当作参数错误。
		status = http.StatusInternalServerError
		code = CodeExecutionFailed
	case errors.Is(err, app.ErrPlanningAnalysisNotPersisted):
		status = http.StatusInternalServerError
		code = CodeExecutionNotPersisted
	case errors.Is(err, app.ErrPlanningRuntimeInvalidInput),
		errors.Is(err, app.ErrPlanningAnalysisInvalidInput),
		errors.Is(err, app.ErrPlanningSessionInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrPlanningRuntimeNotFound),
		errors.Is(err, app.ErrPlanningAnalysisNotFound),
		errors.Is(err, app.ErrPlanningSessionNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrPlanningRuntimeConflict),
		errors.Is(err, app.ErrPlanningAnalysisConflict),
		errors.Is(err, app.ErrPlanningSessionStateConflict),
		errors.Is(err, app.ErrPlanningRuntimeUncertain),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, storage.ErrConflict):
		// 现场冲突与"状态无法确认"都不是参数错误：绑定存在但与远程事实不一致，
		// 必须先核验现场，接口不得顺手清理或重建资源。
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrPlanningRuntimePrerequisite),
		errors.Is(err, domain.ErrValidationPrerequisite):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	case errors.Is(err, app.ErrPlanningRuntimeNotPersisted):
		// 远程副作用已经发生但绑定没落库：既不能报成功，也不能当作普通失败。
		status = http.StatusInternalServerError
		code = CodeExecutionNotPersisted
	case errors.Is(err, app.ErrPlanningRuntimeFailed):
		// 操作已经真实执行但没有成功。
		status = http.StatusInternalServerError
		code = CodeExecutionFailed
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError && code == CodeInternal {
		h.logger.ErrorContext(r.Context(), "规划接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	if code == CodeExecutionNotPersisted {
		// 失败事实已经落库到会话的失败摘要；响应只回传同一份脱敏裁剪后的说明，
		// 不把底层存储细节、凭据或完整提示词交给客户端。
		message = domain.ClipFeedbackText(message, domain.MaxFeedbackReason)
	}
	writeError(w, r, h.logger, status, code, message)
}

// planningSessionResponse 把领域记录转换为对外表示。
func planningSessionResponse(session domain.PlanningSession) PlanningSessionResponse {
	response := PlanningSessionResponse{
		ID:               session.ID.String(),
		ProjectID:        session.ProjectID,
		Status:           string(session.Status),
		ContainerID:      session.ContainerID,
		SourceCommit:     session.SourceCommit,
		AnalystSessionID: session.AnalystSessionID,
		PlannerSessionID: session.PlannerSessionID,
		ErrorSummary:     session.ErrorSummary,
		CreatedAt:        session.CreatedAt,
		UpdatedAt:        session.UpdatedAt,
		FinishedAt:       session.FinishedAt,
	}
	if session.HostID != nil {
		response.HostID = session.HostID.String()
	}
	if session.AdoptedRevisionID != nil {
		response.AdoptedRevisionID = session.AdoptedRevisionID.String()
	}
	return response
}

// planningInspectionResponse 把现场核验结果转换为对外表示。
func planningInspectionResponse(
	inspection app.PlanningRuntimeInspection,
) PlanningRuntimeInspectionResponse {
	response := PlanningRuntimeInspectionResponse{
		Session:       planningSessionResponse(inspection.Session),
		Container:     planningContainerResponse(inspection.Container),
		PrepContainer: planningContainerResponse(inspection.PrepContainer),
		Snapshot: PlanningRuntimeSnapshotResponse{
			PlanningRuntimeResourceResponse: PlanningRuntimeResourceResponse{
				Presence: string(inspection.Snapshot.Presence),
				Detail:   inspection.Snapshot.Detail,
			},
			VolumeName:     inspection.Snapshot.VolumeName,
			Commit:         inspection.Snapshot.Commit,
			CommitVerified: inspection.Snapshot.CommitVerified,
			CommitMatches:  inspection.Snapshot.CommitMatches,
		},
	}
	if inspection.Host != nil {
		response.HostID = inspection.Host.ID.String()
		response.HostName = inspection.Host.Name
	}
	return response
}

// planningContainerResponse 把容器现场核验结果转换为对外表示。
func planningContainerResponse(
	inspection app.PlanningRuntimeContainerInspection,
) PlanningRuntimeContainerResponse {
	return PlanningRuntimeContainerResponse{
		PlanningRuntimeResourceResponse: PlanningRuntimeResourceResponse{
			Presence: string(inspection.Presence),
			Detail:   inspection.Detail,
		},
		Name:        inspection.Name,
		DockerID:    inspection.DockerID,
		DockerState: inspection.DockerState.String(),
		Running:     inspection.Running,
	}
}

// planningRuntimeDetailResponse 把准备结果转换为对外表示。
func planningRuntimeDetailResponse(
	detail app.PlanningRuntimeDetail,
) PlanningRuntimeDetailResponse {
	return PlanningRuntimeDetailResponse{
		Session:         planningSessionResponse(detail.Session),
		HostID:          detail.Host.ID.String(),
		HostName:        detail.Host.Name,
		ContainerID:     detail.Container.ID.String(),
		DockerID:        detail.Container.DockerID,
		CodeVolumeName:  detail.CodeVolumeName,
		SourceCommit:    detail.SourceCommit,
		SnapshotCreated: detail.SnapshotCreated,
		SnapshotReused:  detail.SnapshotReused,
		ContainerReused: detail.ContainerReused,
	}
}
