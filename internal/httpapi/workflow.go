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

// 工作流接口使用的路径段。
const (
	// workflowSegment 是任务下工作流资源的路径段。
	workflowSegment = "workflow"
	// executeWorkflowSegment 是启动工作流的动作路径段。
	executeWorkflowSegment = "execute-workflow"
	// cancelWorkflowSegment 是取消工作流的动作路径段。
	cancelWorkflowSegment = "cancel-workflow"
	// resumeWorkflowSegment 是恢复工作流的动作路径段。
	resumeWorkflowSegment = "resume-workflow"
)

// StartWorkflowRequest 是启动任务级工作流的请求体。
//
// 只接受执行参数：任务标识来自路径，要执行哪些步骤、顺序如何完全由数据库中已持久化
// 的计划决定，调用方不能指定步骤列表、Session 路径或角色覆盖。
type StartWorkflowRequest struct {
	// Prompt 是可选补充说明，会追加在每一步骤的提示词之后。
	Prompt string `json:"prompt"`
	// TimeoutSeconds 是单个步骤开发阶段的超时秒数，0 表示使用任务配置或服务端默认值。
	TimeoutSeconds int `json:"timeout_seconds"`
	// ValidationTimeoutSeconds 是单个步骤验证阶段的超时秒数，0 表示使用服务端默认值。
	ValidationTimeoutSeconds int `json:"validation_timeout_seconds"`
}

// ResumeWorkflowRequest 是恢复工作流的请求体。
type ResumeWorkflowRequest struct {
	// RunID 是待恢复的执行记录标识，必填。
	RunID string `json:"run_id"`
	// 其余字段与启动时同义。
	Prompt                   string `json:"prompt"`
	TimeoutSeconds           int    `json:"timeout_seconds"`
	ValidationTimeoutSeconds int    `json:"validation_timeout_seconds"`
}

// WorkflowRunResponse 是一次工作流执行的对外表示。
//
// 结构显式区分"编排进度"与"任务状态"：`status` 只描述这一次编排执行是否在推进，
// `code_saver_available` 为 false 时任务不会因为步骤全部通过而被判定为完整成功。
type WorkflowRunResponse struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
	// Status 是工作流执行状态：pending / running / blocked / cancel_requested /
	// cancelled / completed / failed / recovery_required。
	Status string `json:"status"`
	// StatusTerminal 表示工作流执行是否已结束。
	StatusTerminal bool `json:"status_terminal"`
	// StatusActive 表示工作流是否正在推进。
	StatusActive bool `json:"status_active"`
	// Stage 是当前执行阶段。
	Stage string `json:"stage"`
	// CurrentStepID / CurrentAttemptID 是当前游标。
	CurrentStepID    string `json:"current_step_id,omitempty"`
	CurrentAttemptID string `json:"current_attempt_id,omitempty"`
	// LastCompletedStepID / LastCompletedSequence 是最后一个已确认完成的步骤。
	LastCompletedStepID   string `json:"last_completed_step_id,omitempty"`
	LastCompletedSequence int    `json:"last_completed_sequence"`
	// CompletedSteps / TotalSteps 是进度计数。
	CompletedSteps int `json:"completed_steps"`
	TotalSteps     int `json:"total_steps"`
	// CancelRequested 表示已记录取消请求。
	CancelRequested bool `json:"cancel_requested"`
	// ErrorClass / ErrorSummary 是最近一次停止的原因。
	ErrorClass   string `json:"error_class,omitempty"`
	ErrorSummary string `json:"error_summary,omitempty"`
	// CodeSaverAvailable 表示本次运行是否具备代码保存能力。
	CodeSaverAvailable bool `json:"code_saver_available"`
	// PlanDigest 是本次执行所用计划的内容摘要。
	PlanDigest string `json:"plan_digest"`
	// 时间字段。
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
	// Revision 是乐观并发版本号。
	Revision int64 `json:"revision"`
}

// WorkflowStepResponse 是单个步骤的工作流进度。
type WorkflowStepResponse struct {
	StepID     string `json:"step_id"`
	Sequence   int    `json:"sequence"`
	Title      string `json:"title"`
	StepType   string `json:"step_type"`
	Status     string `json:"status"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	AttemptID  string `json:"attempt_id,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// WorkflowStatusResponse 是工作流状态查询的响应。
type WorkflowStatusResponse struct {
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"`
	// HasRun 表示任务是否存在执行记录；为 false 时 run 为空对象。
	HasRun bool                 `json:"has_run"`
	Run    *WorkflowRunResponse `json:"run,omitempty"`
	// Steps 是按序号排列的步骤进度快照。
	Steps []WorkflowStepResponse `json:"steps"`
}

// serveWorkflow 分发任务级工作流的启动、取消、恢复与状态查询请求。
//
// 路径形状固定为 {taskID}/execute-workflow、{taskID}/cancel-workflow、
// {taskID}/resume-workflow 与 {taskID}/workflow；其余形状返回 false，由任务处理器
// 给出各自的 404 与 405 语义。
func (rt *router) serveWorkflow(w http.ResponseWriter, r *http.Request) {
	handler := newWorkflowHandler(rt.workflow, rt.logger)
	if handler == nil {
		writeError(
			w,
			r,
			rt.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"工作流编排服务未初始化",
		)
		return
	}
	switch {
	case matchActionPath(r.URL.Path, executeWorkflowSegment):
		handler.start(w, r)
	case matchActionPath(r.URL.Path, cancelWorkflowSegment):
		handler.cancel(w, r)
	case matchActionPath(r.URL.Path, resumeWorkflowSegment):
		handler.resume(w, r)
	case matchStatusPath(r.URL.Path):
		handler.status(w, r)
	default:
		writeError(w, r, rt.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
	}
}

// workflowHandler 处理工作流相关接口。
type workflowHandler struct {
	workflow *app.TaskWorkflowService
	logger   *slog.Logger
}

// newWorkflowHandler 构造工作流接口处理器，服务未初始化时返回 nil。
func newWorkflowHandler(
	workflow *app.TaskWorkflowService,
	logger *slog.Logger,
) *workflowHandler {
	if workflow == nil {
		return nil
	}
	return &workflowHandler{workflow: workflow, logger: logger}
}

// start 处理 POST /api/v1/tasks/{taskID}/execute-workflow。
//
// 该入口立即返回执行记录的真实初始状态：编排在受生命周期管理的后台推进，HTTP 请求
// 不承担整个开发周期。
func (h *workflowHandler) start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 POST 请求")
		return
	}
	taskID, ok := actionTaskID(r.URL.Path, executeWorkflowSegment)
	if !ok {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	var body StartWorkflowRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toStartRequest(taskID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.workflow.StartWorkflow(r.Context(), request)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusAccepted, workflowRunResponse(result))
}

// cancel 处理 POST /api/v1/tasks/{taskID}/cancel-workflow。
func (h *workflowHandler) cancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 POST 请求")
		return
	}
	taskID, ok := actionTaskID(r.URL.Path, cancelWorkflowSegment)
	if !ok {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	result, err := h.workflow.CancelWorkflow(r.Context(), taskID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, workflowRunResponse(result))
}

// resume 处理 POST /api/v1/tasks/{taskID}/resume-workflow。
func (h *workflowHandler) resume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 POST 请求")
		return
	}
	taskID, ok := actionTaskID(r.URL.Path, resumeWorkflowSegment)
	if !ok {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	var body ResumeWorkflowRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toResumeRequest(taskID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.workflow.ResumeWorkflow(r.Context(), request)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusAccepted, workflowRunResponse(result))
}

// status 处理 GET /api/v1/tasks/{taskID}/workflow。
func (h *workflowHandler) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 GET 请求")
		return
	}
	taskID, ok := statusTaskID(r.URL.Path)
	if !ok {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	result, err := h.workflow.GetWorkflowStatus(r.Context(), taskID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, workflowStatusResponse(result))
}

// writeError 把工作流编排器的错误映射为统一错误响应。
func (h *workflowHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrWorkflowInvalidInput),
		errors.Is(err, app.ErrStepExecutionInvalidInput),
		errors.Is(err, app.ErrStepWorkflowInvalidConfig),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrWorkflowNotFound),
		errors.Is(err, app.ErrStepExecutionNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrWorkflowConflict),
		errors.Is(err, app.ErrStepExecutionConflict),
		errors.Is(err, app.ErrValidationConflict),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, domain.ErrStepExecutionBlocked),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrWorkflowPrerequisite),
		errors.Is(err, app.ErrStepExecutionPrerequisite),
		errors.Is(err, app.ErrValidationPrerequisite),
		errors.Is(err, domain.ErrValidationPrerequisite):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	case errors.Is(err, app.ErrWorkflowStopped):
		status = http.StatusServiceUnavailable
		code = CodeInternal
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "工作流接口处理失败", "error", message)
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// toStartRequest 把请求体转换为应用层请求。
func (b StartWorkflowRequest) toStartRequest(taskID string) (app.StartWorkflowRequest, error) {
	timeout, validationTimeout, err := workflowTimeouts(
		b.TimeoutSeconds,
		b.ValidationTimeoutSeconds,
	)
	if err != nil {
		return app.StartWorkflowRequest{}, err
	}
	return app.StartWorkflowRequest{
		TaskID:            taskID,
		Prompt:            b.Prompt,
		Timeout:           timeout,
		ValidationTimeout: validationTimeout,
	}, nil
}

// toResumeRequest 把请求体转换为应用层恢复请求。
func (b ResumeWorkflowRequest) toResumeRequest(taskID string) (app.ResumeWorkflowRequest, error) {
	runID := strings.TrimSpace(b.RunID)
	if runID == "" {
		return app.ResumeWorkflowRequest{}, errors.Join(
			app.ErrWorkflowInvalidInput,
			errors.New("恢复工作流必须给出 run_id"),
		)
	}
	timeout, validationTimeout, err := workflowTimeouts(
		b.TimeoutSeconds,
		b.ValidationTimeoutSeconds,
	)
	if err != nil {
		return app.ResumeWorkflowRequest{}, err
	}
	return app.ResumeWorkflowRequest{
		TaskID:            taskID,
		RunID:             runID,
		Prompt:            b.Prompt,
		Timeout:           timeout,
		ValidationTimeout: validationTimeout,
	}, nil
}

// workflowTimeouts 校验并转换超时参数。
func workflowTimeouts(timeoutSeconds, validationTimeoutSeconds int) (time.Duration, time.Duration, error) {
	if timeoutSeconds < 0 || timeoutSeconds > maxExecuteTimeoutSeconds {
		return 0, 0, errors.Join(
			app.ErrWorkflowInvalidInput,
			errors.New("开发超时秒数必须在 0 到 21600 之间"),
		)
	}
	if validationTimeoutSeconds < 0 || validationTimeoutSeconds > maxValidateTimeoutSeconds {
		return 0, 0, errors.Join(
			app.ErrWorkflowInvalidInput,
			errors.New("验证超时秒数必须在 0 到 21600 之间"),
		)
	}
	return time.Duration(timeoutSeconds) * time.Second,
		time.Duration(validationTimeoutSeconds) * time.Second,
		nil
}

// workflowRunResponse 把执行记录转换为对外表示。
func workflowRunResponse(run app.WorkflowRunView) WorkflowRunResponse {
	return WorkflowRunResponse{
		RunID:                 run.RunID.String(),
		TaskID:                run.TaskID.String(),
		Status:                string(run.Status),
		StatusTerminal:        run.Status.IsTerminal(),
		StatusActive:          run.Status.IsActive(),
		Stage:                 string(run.Stage),
		CurrentStepID:         workflowIDString(run.CurrentStepID),
		CurrentAttemptID:      workflowIDString(run.CurrentAttemptID),
		LastCompletedStepID:   workflowIDString(run.LastCompletedStepID),
		LastCompletedSequence: run.LastCompletedSequence,
		CompletedSteps:        run.CompletedSteps,
		TotalSteps:            run.TotalSteps,
		CancelRequested:       run.CancelRequested,
		ErrorClass:            run.ErrorClass,
		ErrorSummary:          run.ErrorSummary,
		CodeSaverAvailable:    run.CodeSaverAvailable,
		PlanDigest:            run.PlanDigest,
		StartedAt:             run.StartedAt,
		FinishedAt:            run.FinishedAt,
		UpdatedAt:             run.UpdatedAt,
		Revision:              run.Revision,
	}
}

// workflowStatusResponse 把状态查询结果转换为对外表示。
func workflowStatusResponse(result app.WorkflowStatusResult) WorkflowStatusResponse {
	response := WorkflowStatusResponse{
		TaskID:     result.TaskID.String(),
		TaskStatus: string(result.TaskStatus),
		HasRun:     result.HasRun,
		Steps:      make([]WorkflowStepResponse, 0, len(result.Steps)),
	}
	if result.HasRun {
		run := workflowRunResponse(result.Run)
		response.Run = &run
	}
	for _, step := range result.Steps {
		response.Steps = append(response.Steps, WorkflowStepResponse{
			StepID:    step.StepID.String(),
			Sequence:  step.Sequence,
			Title:     step.Title,
			StepType:  string(step.Type),
			Status:    string(step.Status),
			Result:    step.Result,
			Error:     step.FailureSummary,
			AttemptID: workflowIDString(step.CurrentAttemptID),
		})
	}
	return response
}

// workflowIDString 返回可空标识的字符串形式。
func workflowIDString(id *domain.ID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// matchActionPath 判断路径是否指向任务下的工作流动作。
func matchActionPath(path, action string) bool {
	_, ok := actionTaskID(path, action)
	return ok
}

// actionTaskID 解析 {taskID}/{action} 形状的路径。
func actionTaskID(path, action string) (string, bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 2 || segments[1] != action {
		return "", false
	}
	if !validTaskPathID(segments[0]) {
		return "", false
	}
	return segments[0], true
}

// matchStatusPath 判断路径是否指向任务的工作流状态查询。
func matchStatusPath(path string) bool {
	_, ok := statusTaskID(path)
	return ok
}

// statusTaskID 解析 {taskID}/workflow 形状的路径。
func statusTaskID(path string) (string, bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 2 || segments[1] != workflowSegment {
		return "", false
	}
	if !validTaskPathID(segments[0]) {
		return "", false
	}
	return segments[0], true
}
