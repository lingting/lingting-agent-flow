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

// 本文件实现项目执行与进度接口。
//
// Handler 只做协议转换、输入校验、DTO 映射与错误映射：状态迁移、执行权与顺序判定全部
// 在 internal/domain 与 internal/app 完成。请求不接受任务状态、租约 owner、epoch、
// 执行结果或任务顺序，也不提供"提前启动某个后续任务"的入口。

// 项目执行相关的路径段。
const (
	// projectStartSegment 是开始项目执行的路径段。
	projectStartSegment = "start"
	// projectCancelSegment 是请求停止项目执行的路径段。
	projectCancelSegment = "cancel"
	// projectExecutionSegment 是项目执行状态的路径段。
	projectExecutionSegment = "execution"
	// projectTasksSegment 是项目正式任务列表的路径段。
	projectTasksSegment = "tasks"
	// requirementProgressSegment 是需求进度的路径段。
	requirementProgressSegment = "progress"
)

// ProjectTaskCountsResponse 是任务数量统计的对外表示。
//
// 口径与领域层一致：completed 只统计真实 completed；cancelled 不并入 failed，两者分别
// 展示；阻塞原因另由稳定字段给出，不由前端推断。
type ProjectTaskCountsResponse struct {
	Total            int `json:"total"`
	Completed        int `json:"completed"`
	Running          int `json:"running"`
	Pending          int `json:"pending"`
	Failed           int `json:"failed"`
	RecoveryRequired int `json:"recovery_required"`
	Cancelled        int `json:"cancelled"`
	Paused           int `json:"paused"`
}

// ProjectExecutionStatusResponse 是项目执行状态的对外表示。
type ProjectExecutionStatusResponse struct {
	// ProjectID 是项目标识。
	ProjectID string `json:"project_id"`
	// Status 是项目执行状态投影，取值 idle / running / blocked / paused。
	Status string `json:"status"`
	// Counts 是项目内任务数量统计。
	Counts ProjectTaskCountsResponse `json:"counts"`
	// Requirements 是项目下正式需求数量。
	Requirements int `json:"requirements"`
	// Blocked 表示项目被前序任务阻塞。
	Blocked bool `json:"blocked"`
	// BlockedTaskID 是造成阻塞的任务标识。
	BlockedTaskID string `json:"blocked_task_id,omitempty"`
	// BlockedReason 是阻塞原因的稳定取值。
	BlockedReason string `json:"blocked_reason,omitempty"`
	// CurrentTaskID 是正在执行的任务标识。
	CurrentTaskID string `json:"current_task_id,omitempty"`
	// NextTaskID 是下一个可执行的任务标识。
	NextTaskID string `json:"next_task_id,omitempty"`
	// LeaseActive 表示项目当前持有有效执行权。
	LeaseActive bool `json:"lease_active"`
	// LeaseEpoch 是执行权代数，用于诊断 fencing。
	LeaseEpoch int64 `json:"lease_epoch,omitempty"`
	// LeaseExpiresAt 是执行权到期时间。
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	// StopRequested 表示用户已请求停止领取新任务。
	StopRequested bool `json:"stop_requested"`
	// Stopping 表示停止请求已记录但仍有任务在执行。
	Stopping bool `json:"stopping"`
}

// ProjectExecutionTaskResponse 是项目内一个正式任务的对外表示。
type ProjectExecutionTaskResponse struct {
	// ID 是任务标识。
	ID string `json:"id"`
	// RequirementID 是所属需求标识。
	RequirementID string `json:"requirement_id"`
	// RequirementTitle 是所属需求标题。
	RequirementTitle string `json:"requirement_title"`
	// RequirementSequence 是需求内顺序，历史任务没有该事实时为空。
	RequirementSequence *int `json:"requirement_sequence"`
	// Position 是任务在项目内的执行位置，从 1 开始。
	Position int `json:"position"`
	// Status 是持久化的任务状态，未知取值原样输出。
	Status string `json:"status"`
	// HostID 是执行节点标识。
	HostID string `json:"host_id,omitempty"`
	// CancelRequested 表示任务已记录取消请求。
	CancelRequested bool `json:"cancel_requested"`
	// Blocking 表示该任务是当前阻塞项目的任务。
	Blocking bool `json:"blocking"`
	// BlockingReason 是阻塞原因的稳定取值。
	BlockingReason string `json:"blocking_reason,omitempty"`
	// FailureSummary 是任务失败摘要，不改写为成功。
	FailureSummary string `json:"failure_summary,omitempty"`
	// CreatedAt 是任务创建时间。
	CreatedAt time.Time `json:"created_at"`
	// StartedAt 是任务开始执行时间。
	StartedAt *time.Time `json:"started_at,omitempty"`
	// FinishedAt 是任务结束时间。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// ProjectExecutionResponse 是项目执行状态与任务列表的组合响应。
type ProjectExecutionResponse struct {
	// ProjectID 是项目标识。
	ProjectID string `json:"project_id"`
	// Execution 是项目执行状态。
	Execution ProjectExecutionStatusResponse `json:"execution"`
	// Tasks 是按项目内执行顺序排列的任务。
	Tasks []ProjectExecutionTaskResponse `json:"tasks"`
}

// ProjectTaskListResponse 是项目正式任务列表响应。
type ProjectTaskListResponse struct {
	Records []ProjectExecutionTaskResponse `json:"records"`
	Total   int                            `json:"total"`
}

// RequirementProgressResponse 是需求进度的对外表示。
type RequirementProgressResponse struct {
	// RequirementID 是需求标识。
	RequirementID string `json:"requirement_id"`
	// Status 是进度投影状态，取值 planned / executing / blocked / completed。
	Status string `json:"status"`
	// Counts 是任务数量统计。
	Counts ProjectTaskCountsResponse `json:"counts"`
	// Current 是已完成任务数量。
	Current int `json:"current"`
	// Total 是任务总数。
	Total int `json:"total"`
	// Blocked 表示存在阻止完成判定与继续领取的任务。
	Blocked bool `json:"blocked"`
	// BlockedTaskID 是造成阻塞的任务标识。
	BlockedTaskID string `json:"blocked_task_id,omitempty"`
	// BlockedReason 是阻塞原因的稳定取值。
	BlockedReason string `json:"blocked_reason,omitempty"`
	// CurrentTaskID 是正在执行的任务标识。
	CurrentTaskID string `json:"current_task_id,omitempty"`
	// NextTaskID 是下一个可执行的任务标识。
	NextTaskID string `json:"next_task_id,omitempty"`
}

// projectExecutionHandler 处理项目执行与进度接口请求。
type projectExecutionHandler struct {
	execution    *app.ProjectExecutionService
	requirements *app.RequirementService
	logger       *slog.Logger
}

// newProjectExecutionHandler 构造项目执行接口处理器。
func newProjectExecutionHandler(
	execution *app.ProjectExecutionService,
	requirements *app.RequirementService,
	logger *slog.Logger,
) *projectExecutionHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &projectExecutionHandler{
		execution:    execution,
		requirements: requirements,
		logger:       logger,
	}
}

// start 处理 POST /api/v1/projects/{projectID}/start。
func (h *projectExecutionHandler) start(w http.ResponseWriter, r *http.Request) {
	projectID := pathID(r, paramProjectID)
	result, err := h.execution.Start(r.Context(), projectID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, projectExecutionResponse(result))
}

// cancel 处理 POST /api/v1/projects/{projectID}/cancel。
func (h *projectExecutionHandler) cancel(w http.ResponseWriter, r *http.Request) {
	projectID := pathID(r, paramProjectID)
	result, err := h.execution.Cancel(r.Context(), projectID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, projectExecutionResponse(result))
}

// execution 处理 GET /api/v1/projects/{projectID}/execution。
//
// 只读：查询不触发启动、续租、释放或任何其他写操作。
func (h *projectExecutionHandler) executionStatus(w http.ResponseWriter, r *http.Request) {
	projectID := pathID(r, paramProjectID)
	result, err := h.execution.Execution(r.Context(), projectID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, projectExecutionResponse(result))
}

// tasks 处理 GET /api/v1/projects/{projectID}/tasks。
//
// 只读：列表顺序由服务端的项目内排序规则固定，不接受客户端排序参数。
func (h *projectExecutionHandler) tasks(w http.ResponseWriter, r *http.Request) {
	projectID := pathID(r, paramProjectID)
	views, err := h.execution.ListTasks(r.Context(), projectID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, ProjectTaskListResponse{
		Records: projectTaskResponses(views),
		Total:   len(views),
	})
}

// requirementProgress 处理 GET /api/v1/requirements/{requirementID}/progress。
//
// 只读：进度只来源于持久化的任务状态，不使用时间戳或模型输出推断。
func (h *projectExecutionHandler) requirementProgress(w http.ResponseWriter, r *http.Request) {
	if h.requirements == nil {
		// 需求进度依赖需求查询能力：缺失时按服务不可用返回，而不是伪造空进度。
		writeError(w, r, h.logger, http.StatusServiceUnavailable, CodeInternal, "需求服务未初始化")
		return
	}
	requirementID := pathID(r, paramRequirementID)
	progress, err := h.requirements.Progress(r.Context(), requirementID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, requirementProgressResponse(progress))
}

// writeServiceError 把项目执行服务错误映射为统一错误响应。
func (h *projectExecutionHandler) writeServiceError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrProjectExecutionInvalidInput),
		errors.Is(err, app.ErrRequirementInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrProjectExecutionNotFound),
		errors.Is(err, app.ErrRequirementNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrProjectExecutionBusy),
		errors.Is(err, app.ErrProjectExecutionNotRunning),
		errors.Is(err, app.ErrProjectExecutionNoTasks),
		errors.Is(err, app.ErrRequirementStateConflict),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrProjectExecutionBlocked),
		errors.Is(err, app.ErrProjectExecutionPrerequisite),
		errors.Is(err, app.ErrRequirementPrerequisite),
		errors.Is(err, domain.ErrValidationPrerequisite):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	case errors.Is(err, app.ErrProjectExecutionUnavailable):
		// 调度器未装配：取得执行权也没有领取者，按服务不可用返回。
		status = http.StatusServiceUnavailable
		code = CodeInternal
	case errors.Is(err, storage.ErrInvalidData):
		// 存储层拒绝写入不是参数问题：它是持久化失败，必须如实返回 500。
		status = http.StatusInternalServerError
		code = CodeExecutionNotPersisted
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError && code == CodeInternal {
		h.logger.ErrorContext(r.Context(), "项目执行接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// projectExecutionResponse 把应用层结果转换为对外响应。
func projectExecutionResponse(result app.ProjectExecution) ProjectExecutionResponse {
	return ProjectExecutionResponse{
		ProjectID: result.Project.ID,
		Execution: projectExecutionStatusResponse(result.Progress),
		Tasks:     projectTaskResponses(result.Tasks),
	}
}

// projectExecutionStatusResponse 把项目执行状态转换为对外表示。
func projectExecutionStatusResponse(
	progress domain.ProjectProgress,
) ProjectExecutionStatusResponse {
	response := ProjectExecutionStatusResponse{
		ProjectID:      progress.ProjectID.String(),
		Status:         string(progress.Status),
		Counts:         projectTaskCountsResponse(progress.Counts),
		Requirements:   progress.Requirements,
		Blocked:        progress.Blocked,
		BlockedReason:  progress.BlockedReason,
		LeaseActive:    progress.LeaseActive,
		LeaseEpoch:     progress.LeaseEpoch,
		LeaseExpiresAt: progress.LeaseExpiresAt,
		StopRequested:  progress.StopRequested,
		Stopping:       progress.Stopping,
	}
	if progress.BlockedTaskID != nil {
		response.BlockedTaskID = progress.BlockedTaskID.String()
	}
	if progress.CurrentTaskID != nil {
		response.CurrentTaskID = progress.CurrentTaskID.String()
	}
	if progress.NextTaskID != nil {
		response.NextTaskID = progress.NextTaskID.String()
	}
	return response
}

// projectTaskCountsResponse 把任务数量统计转换为对外表示。
func projectTaskCountsResponse(counts domain.ProjectTaskCounts) ProjectTaskCountsResponse {
	return ProjectTaskCountsResponse{
		Total:            counts.Total,
		Completed:        counts.Completed,
		Running:          counts.Running,
		Pending:          counts.Pending,
		Failed:           counts.Failed,
		RecoveryRequired: counts.RecoveryRequired,
		Cancelled:        counts.Cancelled,
		Paused:           counts.Paused,
	}
}

// projectTaskResponses 把项目任务视图转换为对外表示。
func projectTaskResponses(views []app.ProjectTaskView) []ProjectExecutionTaskResponse {
	records := make([]ProjectExecutionTaskResponse, 0, len(views))
	for _, view := range views {
		records = append(records, projectTaskResponse(view))
	}
	return records
}

// projectTaskResponse 把单个项目任务视图转换为对外表示。
func projectTaskResponse(view app.ProjectTaskView) ProjectExecutionTaskResponse {
	response := ProjectExecutionTaskResponse{
		ID:                  view.Task.ID.String(),
		RequirementID:       view.Task.RequirementID.String(),
		RequirementTitle:    view.Requirement.Title,
		RequirementSequence: view.Task.RequirementSequence,
		Position:            view.Position,
		Status:              string(view.Task.Status),
		CancelRequested:     view.Task.CancelRequested,
		Blocking:            view.Blocking,
		BlockingReason:      view.BlockingReason,
		FailureSummary:      view.Task.FailureSummary,
		CreatedAt:           view.Task.CreatedAt,
		StartedAt:           view.Task.StartedAt,
		FinishedAt:          view.Task.FinishedAt,
	}
	if view.Task.HostID != nil {
		response.HostID = view.Task.HostID.String()
	}
	return response
}

// requirementProgressResponse 把需求进度转换为对外表示。
func requirementProgressResponse(
	progress domain.RequirementProgress,
) RequirementProgressResponse {
	response := RequirementProgressResponse{
		RequirementID: progress.RequirementID.String(),
		Status:        string(progress.Status),
		Counts:        projectTaskCountsResponse(progress.Counts),
		Current:       progress.Current,
		Total:         progress.Counts.Total,
		Blocked:       progress.Blocked,
		BlockedReason: progress.BlockedReason,
	}
	if progress.BlockedTaskID != nil {
		response.BlockedTaskID = progress.BlockedTaskID.String()
	}
	if progress.CurrentTaskID != nil {
		response.CurrentTaskID = progress.CurrentTaskID.String()
	}
	if progress.NextTaskID != nil {
		response.NextTaskID = progress.NextTaskID.String()
	}
	return response
}
