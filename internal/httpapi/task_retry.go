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

// 任务显式重试接口使用的路径段。
const (
	// retrySegment 是显式重试动作的路径段。
	retrySegment = "retry"
	// retryEligibilitySegment 是只读重试资格查询的路径段。
	retryEligibilitySegment = "retry-eligibility"
)

// RetryTaskRequest 是显式重试请求的请求体。
//
// 只接受执行参数：任务标识来自路径，重开哪个执行周期、用哪些步骤完全由服务端依据
// 持久化事实决定，调用方不能指定步骤列表、Session 路径、工作目录或角色覆盖。
type RetryTaskRequest struct {
	// Prompt 是可选补充说明，会追加在计划与每一步骤的提示词之后。
	Prompt string `json:"prompt"`
	// TimeoutSeconds 是单个步骤开发阶段的超时秒数，0 表示使用任务配置或服务端默认值。
	TimeoutSeconds int `json:"timeout_seconds"`
	// ValidationTimeoutSeconds 是单个步骤验证阶段的超时秒数，0 表示使用服务端默认值。
	ValidationTimeoutSeconds int `json:"validation_timeout_seconds"`
	// PlanTimeoutSeconds 是本次规划调用的超时秒数，0 表示使用任务配置或服务端默认值。
	PlanTimeoutSeconds int `json:"plan_timeout_seconds"`
}

// RetryTaskResponse 是一次显式重试的结果。
//
// 结构显式区分四件事：重试前的状态、当前真实状态、新执行代次，以及编排是否真的启动。
// `workflow_started` 为 false 时任务已进入新周期但没有启动编排，调用方不得把它当作
// 已开始执行。
type RetryTaskResponse struct {
	TaskID string `json:"task_id"`
	// PreviousStatus 是重试前的任务状态（通常是 failed）。
	PreviousStatus string `json:"previous_status"`
	// TaskStatus 是重试结束后读取到的任务真实状态。
	TaskStatus string `json:"task_status"`
	// LeaseEpoch 是本次重试取得的任务领取代次，用于区分执行周期。
	LeaseEpoch int64 `json:"lease_epoch"`
	// LeaseOwner 是本次重试的领取者标识。
	LeaseOwner string `json:"lease_owner"`
	// WorkflowStarted 表示本次重试真的启动了工作流推进。
	WorkflowStarted bool `json:"workflow_started"`
	// Run 是启动后的执行记录，未启动时为空。
	Run *WorkflowRunResponse `json:"run,omitempty"`
	// FailureStage / Failure 是启动流程失败时的阶段与原因摘要。
	FailureStage string    `json:"failure_stage,omitempty"`
	Failure      string    `json:"failure,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
}

// RetryEligibilityResponse 是只读的重试资格结论。
type RetryEligibilityResponse struct {
	TaskID string `json:"task_id"`
	// TaskStatus 是任务当前状态。
	TaskStatus string `json:"task_status"`
	// CanRetry 表示当前没有阻塞项。
	CanRetry bool `json:"can_retry"`
	// Blocking 是阻塞项分类列表（稳定字符串）。
	Blocking []string `json:"blocking"`
	// Summary 是可读说明。
	Summary string `json:"summary"`
}

// isTaskRetryPath 判断路径是否指向任务显式重试接口。
//
// 判定按路径段形状进行：只有 {taskID}/retry 与 {taskID}/retry-eligibility 属于重试
// 接口，其余形状仍由任务处理器处理，从而保持它们原有的 405 与 404 语义。
func isTaskRetryPath(path string) bool {
	if _, ok := actionTaskID(path, retrySegment); ok {
		return true
	}
	_, ok := actionTaskID(path, retryEligibilitySegment)
	return ok
}

// serveTaskRetry 分发显式重试与重试资格查询请求。
func (rt *router) serveTaskRetry(w http.ResponseWriter, r *http.Request) {
	if rt.taskRetry == nil {
		writeError(
			w,
			r,
			rt.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"任务重试服务未初始化",
		)
		return
	}
	handler := &taskRetryHandler{service: rt.taskRetry, logger: rt.logger}
	if taskID, ok := actionTaskID(r.URL.Path, retryEligibilitySegment); ok {
		handler.eligibility(w, r, taskID)
		return
	}
	taskID, ok := actionTaskID(r.URL.Path, retrySegment)
	if !ok {
		writeError(w, r, rt.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	handler.retry(w, r, taskID)
}

// taskRetryHandler 处理显式重试接口。
type taskRetryHandler struct {
	service *app.TaskRetryService
	logger  *slog.Logger
}

// retry 处理 POST /api/v1/tasks/{taskID}/retry。
//
// 该入口是同步的：只有任务被重开、启动流程得出真实结论后才写出响应。工作流本身在
// 受生命周期管理的后台推进，因此调用方不能把 202 当作"已完成"。
func (h *taskRetryHandler) retry(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 POST 请求")
		return
	}
	var body RetryTaskRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toRetryRequest()
	if err != nil {
		writeRetryError(w, r, h.logger, err)
		return
	}
	result, err := h.service.Retry(r.Context(), taskID, request)
	if err != nil {
		writeRetryError(w, r, h.logger, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusAccepted, retryTaskResponse(result))
}

// eligibility 处理 GET /api/v1/tasks/{taskID}/retry-eligibility。
//
// 只读：它不领取租约、不写任何状态，使用户能在执行重试之前看到阻塞项。
func (h *taskRetryHandler) eligibility(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 GET 请求")
		return
	}
	decision, err := h.service.Eligibility(r.Context(), taskID)
	if err != nil {
		writeRetryError(w, r, h.logger, err)
		return
	}
	blocking := make([]string, 0, len(decision.Rejections))
	for _, reason := range decision.Rejections {
		blocking = append(blocking, string(reason))
	}
	summary := decision.Summary
	if !decision.Allowed {
		summary = domain.TaskRetrySummary(decision)
	}
	writeJSON(w, r, h.logger, http.StatusOK, RetryEligibilityResponse{
		TaskID:     strings.TrimSpace(taskID),
		CanRetry:   decision.Allowed,
		Blocking:   blocking,
		Summary:    summary,
		TaskStatus: string(decision.TaskStatus),
	})
}

// toRetryRequest 把请求体转换为应用层请求。
func (b RetryTaskRequest) toRetryRequest() (app.TaskRetryRequest, error) {
	timeout, validationTimeout, err := workflowTimeouts(b.TimeoutSeconds, b.ValidationTimeoutSeconds)
	if err != nil {
		return app.TaskRetryRequest{}, err
	}
	planTimeout, err := optionalTimeout(b.PlanTimeoutSeconds, "规划超时秒数")
	if err != nil {
		return app.TaskRetryRequest{}, err
	}
	return app.TaskRetryRequest{
		Prompt:            b.Prompt,
		Timeout:           timeout,
		ValidationTimeout: validationTimeout,
		PlanTimeout:       planTimeout,
	}, nil
}

// retryTaskResponse 把重试结果转换为对外表示。
func retryTaskResponse(result app.TaskRetryResult) RetryTaskResponse {
	response := RetryTaskResponse{
		TaskID:          result.TaskID.String(),
		PreviousStatus:  string(result.PreviousStatus),
		TaskStatus:      string(result.TaskStatus),
		LeaseEpoch:      result.Epoch,
		LeaseOwner:      result.Owner,
		WorkflowStarted: result.WorkflowStarted,
		FailureStage:    result.FailureStage,
		Failure:         result.Failure,
		StartedAt:       result.Launch.StartedAt,
		FinishedAt:      result.Launch.FinishedAt,
	}
	if result.WorkflowStarted {
		run := workflowRunResponse(result.Launch.Run)
		response.Run = &run
	}
	return response
}

// writeRetryError 把重试服务的错误映射为统一错误响应。
//
// 映射依据错误类别而不是文本，状态码如实反映结果：参数错误 400、目标不存在 404、
// 状态或执行权冲突 409、前置条件缺失 412、重试已发生但未成功 500、服务已停止 503。
func writeRetryError(
	w http.ResponseWriter,
	r *http.Request,
	logger *slog.Logger,
	err error,
) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrTaskRetryConflict),
		errors.Is(err, app.ErrTaskLeaseConflict),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrTaskRetryNotFound),
		errors.Is(err, app.ErrTaskLeaseNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrTaskRetryPrerequisite),
		errors.Is(err, app.ErrTaskLeaseInvalid):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	case errors.Is(err, app.ErrTaskRetryFailed):
		// 重试已经真实开始但没有成功：这不是参数错误，也不是前置条件缺失。
		status = http.StatusInternalServerError
		code = CodeExecutionFailed
	case errors.Is(err, app.ErrTaskLaunchStopped),
		errors.Is(err, app.ErrWorkflowStopped):
		status = http.StatusServiceUnavailable
		code = CodeInternal
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		logger.ErrorContext(r.Context(), "任务重试接口处理失败", "error", message)
		if code == CodeInternal {
			message = "服务内部错误"
		}
	}
	writeError(w, r, logger, status, code, message)
}
