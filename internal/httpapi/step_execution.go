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

// 步骤执行接口路径。
//
// 路径挂在任务与步骤之下，与执行记录查询保持一致：执行永远属于某条任务链，
// 独立顶层路径会让调用方以为可以跨任务执行任意步骤。
const (
	// executeSegment 是步骤执行动作的路径段。
	executeSegment = "execute"
	// maxExecuteTimeoutSeconds 是单次执行允许请求的最大超时秒数。
	maxExecuteTimeoutSeconds = 6 * 60 * 60
)

// ExecuteStepRequest 是执行单个步骤的请求体。
//
// 只接受执行参数：任务与步骤标识来自路径，Session 路径、工作目录与命令行参数
// 都由服务端按数据库记录生成，调用方无法指定。
type ExecuteStepRequest struct {
	// Prompt 是可选补充说明，会追加在编排器生成的提示词之后。
	Prompt string `json:"prompt"`
	// TimeoutSeconds 是本次调用超时秒数，0 表示使用任务配置或服务端默认值。
	TimeoutSeconds int `json:"timeout_seconds"`
	// Role 不对外暴露：执行角色由步骤在计划生成时锁定，不能由调用方随意替换。
}

// ExecuteStepResponse 是一次步骤执行的结果。
//
// 结构显式区分"执行是否成功"与"状态是否已完整持久化"：
//   - Outcome 为 completed 仅表示本次 Agent 调用正常结束；
//   - StepStatus 为 completed 不代表任务验收通过，任务仍需验证与代码保存阶段；
//   - Finalized 为 false 时 FinalizationErrors 说明哪些写入失败。
type ExecuteStepResponse struct {
	TaskID        string `json:"task_id"`
	StepID        string `json:"step_id"`
	AttemptID     string `json:"attempt_id"`
	AttemptNumber int    `json:"attempt_number"`
	// SessionID 是本次调用对应的独立会话标识。
	SessionID string `json:"session_id,omitempty"`
	// Outcome 是执行结论：completed / failed / timed_out / cancelled / interrupted。
	Outcome string `json:"outcome"`
	// Success 是"本次 Agent 调用是否正常结束"的便捷布尔值，不表示任务成功。
	Success bool `json:"success"`
	// Summary 是结果摘要。
	Summary string `json:"summary,omitempty"`
	// Error 是失败或异常的可诊断原因。
	Error string `json:"error,omitempty"`
	// ExitCode 是 Agent 进程退出码，未能取得时为 -1。
	ExitCode int `json:"exit_code"`
	// AttemptStatus 是写入后的尝试状态。
	AttemptStatus string `json:"attempt_status"`
	// StepStatus 是写入后的步骤状态。
	StepStatus string `json:"step_status"`
	// TaskStatus 是写入后的任务状态。
	TaskStatus string `json:"task_status"`
	// SessionStatus 是会话执行状态。
	SessionStatus string `json:"session_status,omitempty"`
	// SessionRunStatus 是会话自身的运行结果。
	SessionRunStatus string `json:"session_run_status,omitempty"`
	// ArchiveStatus 与 ExportStatus 分别记录原始会话归档与 HTML 导出结果。
	ArchiveStatus string `json:"archive_status"`
	ExportStatus  string `json:"export_status"`
	// Finalized 表示最终状态是否全部写入成功。
	Finalized bool `json:"finalized"`
	// FinalizationErrors 是最终状态写入失败的原因，成功时为空。
	FinalizationErrors []string  `json:"finalization_errors,omitempty"`
	StartedAt          time.Time `json:"started_at"`
	FinishedAt         time.Time `json:"finished_at"`
}

// stepExecutionHandler 处理步骤执行请求。
type stepExecutionHandler struct {
	executor *app.StepExecutor
	// workflow 是开发—验证闭环协调器，为空时闭环接口返回 503。
	workflow *app.StepWorkflowService
	logger   *slog.Logger
}

// newStepExecutionHandler 构造步骤执行处理器。
//
// 服务为空时仍然返回处理器：请求会得到 503，而不是 404 或前端页面。
func newStepExecutionHandler(
	executor *app.StepExecutor,
	workflow *app.StepWorkflowService,
	logger *slog.Logger,
) *stepExecutionHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &stepExecutionHandler{executor: executor, workflow: workflow, logger: logger}
}

// serve 处理 POST /api/v1/tasks/{taskID}/steps/{stepID}/execute。
//
// 该入口是同步的：只有 Pi 调用结束、尝试进入终态后才写出响应。执行本身已经由
// 服务层持久化，因此即使响应写入失败，执行记录与状态也不会丢失。
func (h *stepExecutionHandler) serve(
	w http.ResponseWriter,
	r *http.Request,
	taskID, stepID string,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
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
	if h.executor == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"步骤执行服务未初始化",
		)
		return
	}
	var body ExecuteStepRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toAppRequest(taskID, stepID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	result, err := h.executor.Execute(r.Context(), request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	response := executeStepResponse(result)
	switch {
	case !result.Finalized:
		// 执行事实与持久化完整性必须分开报告：写入失败时不能返回成功。
		message := "执行已结束，但最终状态未能完整写入"
		if len(result.FinalizationErrors) > 0 {
			message += ": " + strings.Join(result.FinalizationErrors, "；")
		}
		writeError(
			w,
			r,
			h.logger,
			http.StatusInternalServerError,
			CodeExecutionNotPersisted,
			message,
		)
		return
	case result.Outcome != domain.StepExecutionCompleted:
		writeError(
			w,
			r,
			h.logger,
			http.StatusInternalServerError,
			CodeExecutionFailed,
			executionFailureMessage(result),
		)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, response)
}

// decodeOptionalJSONBody 解析允许为空的请求体。
//
// 执行请求的全部字段都是可选的，因此空请求体等价于"使用默认执行参数"；
// 其余校验（未知字段、多余内容）与通用解析保持一致，不放松对畸形请求的拒绝。
func decodeOptionalJSONBody(
	w http.ResponseWriter,
	r *http.Request,
	logger *slog.Logger,
	target any,
) bool {
	limited := http.MaxBytesReader(w, r.Body, maxTaskRequestBytes)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeError(w, r, logger, http.StatusBadRequest, CodeInvalidRequest, "请求体不是合法的 JSON")
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

// toAppRequest 把请求体转换为应用层请求。
func (b ExecuteStepRequest) toAppRequest(
	taskID, stepID string,
) (app.StepExecuteRequest, error) {
	if b.TimeoutSeconds < 0 || b.TimeoutSeconds > maxExecuteTimeoutSeconds {
		return app.StepExecuteRequest{}, errors.Join(
			app.ErrStepExecutionInvalidInput,
			errors.New("超时秒数必须在 0 到 21600 之间"),
		)
	}
	timeout := time.Duration(b.TimeoutSeconds) * time.Second
	return app.StepExecuteRequest{
		TaskID:  taskID,
		StepID:  stepID,
		Prompt:  b.Prompt,
		Timeout: timeout,
	}, nil
}

// executeStepResponse 把执行结果转换为对外表示。
func executeStepResponse(result app.StepExecutionResult) ExecuteStepResponse {
	return ExecuteStepResponse{
		TaskID:             result.TaskID.String(),
		StepID:             result.StepID.String(),
		AttemptID:          result.AttemptID.String(),
		AttemptNumber:      result.AttemptNumber,
		SessionID:          result.SessionID.String(),
		Outcome:            string(result.Outcome),
		Success:            result.Outcome == domain.StepExecutionCompleted && result.Finalized,
		Summary:            result.Summary,
		Error:              result.Failure,
		ExitCode:           result.ExitCode,
		AttemptStatus:      string(result.AttemptStatus),
		StepStatus:         string(result.StepStatus),
		TaskStatus:         string(result.TaskStatus),
		SessionStatus:      string(result.SessionStatus),
		SessionRunStatus:   string(result.SessionRunStatus),
		ArchiveStatus:      archiveStatusOf(result.ArchiveStatus),
		ExportStatus:       exportStatusOf(result.ExportStatus),
		Finalized:          result.Finalized,
		FinalizationErrors: result.FinalizationErrors,
		StartedAt:          result.StartedAt,
		FinishedAt:         result.FinishedAt,
	}
}

// executionFailureMessage 生成失败响应中的说明。
func executionFailureMessage(result app.StepExecutionResult) string {
	detail := strings.TrimSpace(result.Failure)
	if detail == "" {
		detail = strings.TrimSpace(result.Summary)
	}
	if detail == "" {
		detail = "步骤执行未成功"
	}
	return "步骤执行未成功（" + string(result.Outcome) + "）: " + detail
}

// writeServiceError 把应用层错误映射为统一错误响应。
func (h *stepExecutionHandler) writeServiceError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrStepExecutionInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrStepExecutionNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrStepExecutionConflict),
		errors.Is(err, domain.ErrStepExecutionBlocked),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrStepExecutionPrerequisite):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "步骤执行接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// parseStepExecutePath 判断路径是否指向步骤执行动作。
//
// 只有 {taskID}/steps/{stepID}/execute 这一种形状成立；其余形状返回 false，
// 由执行记录查询或任务处理器给出各自的 404 / 405 语义。
func parseStepExecutePath(path string) (taskID, stepID string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 4 {
		return "", "", false
	}
	if segments[1] != stepsSegment || segments[3] != executeSegment {
		return "", "", false
	}
	if !validTaskPathID(segments[0]) || !validTaskPathID(segments[2]) {
		return "", "", false
	}
	return segments[0], segments[2], true
}
