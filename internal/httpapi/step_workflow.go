package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// executeAndValidateSegment 是开发—验证闭环动作的路径段。
const executeAndValidateSegment = "execute-and-validate"

// ExecuteAndValidateStepRequest 是触发一次开发—验证闭环的请求体。
//
// 只接受执行参数：任务与步骤标识来自路径，Session 路径、工作目录、命令行参数与角色
// 覆盖都由服务端按数据库记录生成，调用方无法指定。
type ExecuteAndValidateStepRequest struct {
	// Prompt 是可选补充说明，会追加在编排器生成的提示词之后。
	Prompt string `json:"prompt"`
	// TimeoutSeconds 是开发阶段单次 Agent 调用的超时秒数，0 表示使用任务配置或默认值。
	TimeoutSeconds int `json:"timeout_seconds"`
	// ValidationTimeoutSeconds 是验证阶段的总超时秒数，0 表示使用服务端默认值。
	ValidationTimeoutSeconds int `json:"validation_timeout_seconds"`
}

// ExecuteAndValidateStepResponse 是一次开发—验证闭环的对外表示。
//
// 结构显式区分开发结论、验证结论与最终状态：`verified` 只有在真实验证通过时才为
// true，`validation_ran` 表达验证是否真的发起过，两者不能互相替代。
type ExecuteAndValidateStepResponse struct {
	TaskID        string `json:"task_id"`
	StepID        string `json:"step_id"`
	AttemptID     string `json:"attempt_id,omitempty"`
	AttemptNumber int    `json:"attempt_number"`
	// SessionID 是本次开发尝试对应的独立会话标识。
	SessionID string `json:"session_id,omitempty"`

	// Outcome 是开发阶段结论：completed / failed / timed_out / cancelled / interrupted。
	Outcome string `json:"outcome"`
	// DeveloperSummary 是开发阶段结果摘要。
	DeveloperSummary string `json:"developer_summary,omitempty"`
	// DeveloperError 是开发阶段失败原因。
	DeveloperError string `json:"developer_error,omitempty"`
	// ExitCode 是开发阶段 Agent 进程退出码，未能取得时为 -1。
	ExitCode int `json:"exit_code"`
	// DeveloperFinalized 表示开发阶段执行记录是否完整写入。
	DeveloperFinalized          bool     `json:"developer_finalized"`
	DeveloperFinalizationErrors []string `json:"developer_finalization_errors,omitempty"`

	// ValidationRan 表示开发成功后是否真的启动了验证。
	ValidationRan bool `json:"validation_ran"`
	// ValidationSkipReason 是验证未启动或未完成的原因。
	ValidationSkipReason string `json:"validation_skip_reason,omitempty"`
	// ValidationRunID 是本次验证记录标识，未发起验证时为空。
	ValidationRunID string `json:"validation_run_id,omitempty"`
	// ValidationStatus 是验证流程状态，与结论分离。
	ValidationStatus string `json:"validation_status,omitempty"`
	// ValidationVerdict 是验证结论：passed / failed / inconclusive。
	ValidationVerdict string `json:"validation_verdict,omitempty"`
	// ValidationPassed 是结论的便捷布尔值，不表示流程一定正常结束。
	ValidationPassed         bool   `json:"validation_passed"`
	ValidationSummary        string `json:"validation_summary,omitempty"`
	ValidationFailureSummary string `json:"validation_failure_summary,omitempty"`
	// Checks 是逐项检查结果摘要，不含完整 stdout / stderr。
	Checks []ValidateCheckResultResponse `json:"checks,omitempty"`
	// ValidationPersisted 表示验证结论与检查结果是否可靠写入。
	ValidationPersisted         bool     `json:"validation_persisted"`
	ValidationPersistenceErrors []string `json:"validation_persistence_errors,omitempty"`
	// ReviewRan 表示开发通过确定性检查后是否真的发起了语义审查。
	ReviewRan bool `json:"review_ran"`
	// ReviewStatus 是语义审查的实际结论：passed / failed / needs_human_review /
	// unparseable / invalid_output / call_failed / timed_out / cancelled /
	// interrupted / not_configured，未发起审查时为空。
	ReviewStatus string `json:"review_status,omitempty"`
	// ReviewSummary 是语义审查的可读说明（已脱敏并裁剪）。
	ReviewSummary string `json:"review_summary,omitempty"`
	// ValidatorSessionID 是语义审查使用的会话标识，用于追溯对应 Session。
	ValidatorSessionID string `json:"validator_session_id,omitempty"`
	// Assessment 是语义审查产出的结构化评估。
	Assessment *domain.AgentAssessment `json:"assessment,omitempty"`
	// InfrastructureError 表示验证因基础设施异常没有拿到可用证据。
	InfrastructureError bool `json:"infrastructure_error"`
	// FeedbackPersisted 表示验证失败反馈是否已保存。
	FeedbackPersisted bool `json:"feedback_persisted"`

	// Verification 是从验证记录派生的步骤验证状态。
	Verification string `json:"verification_state"`
	// StepStatus 是收口后的步骤状态。
	StepStatus string `json:"step_status"`
	// TaskStatus 是收口后的任务状态。
	TaskStatus string `json:"task_status"`
	// Verified 表示步骤是否进入"已验证完成"。
	Verified bool `json:"verified"`

	// Finalized 表示收口状态是否全部写入成功。
	Finalized          bool      `json:"finalized"`
	FinalizationErrors []string  `json:"finalization_errors,omitempty"`
	StartedAt          time.Time `json:"started_at"`
	FinishedAt         time.Time `json:"finished_at"`
}

// serveExecuteAndValidate 处理 POST /api/v1/tasks/{taskID}/steps/{stepID}/execute-and-validate。
//
// 该入口是同步的：只有开发与验证都收敛并完成收口写入后才写出响应。执行与验证本身
// 已经由服务层持久化，因此即使响应写入失败，执行记录与验证记录也不会丢失。
func (h *stepExecutionHandler) serveExecuteAndValidate(
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
	if h.workflow == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"步骤开发—验证闭环服务未初始化",
		)
		return
	}
	var body ExecuteAndValidateStepRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toAppRequest(taskID, stepID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	result, err := h.workflow.ExecuteAndValidate(r.Context(), request)
	if err != nil {
		h.writeWorkflowError(w, r, result, err)
		return
	}
	response := executeAndValidateResponse(result)
	switch {
	case result.ValidationRan && !result.ValidationPersisted:
		// 验证结论没能可靠保存时不能返回成功。
		writeError(
			w,
			r,
			h.logger,
			http.StatusInternalServerError,
			CodeExecutionNotPersisted,
			workflowNotPersistedMessage(result),
		)
		return
	case !result.Verified:
		// 未验证通过的步骤不是成功响应：调用方必须看到真实状态与原因。
		writeError(
			w,
			r,
			h.logger,
			http.StatusInternalServerError,
			CodeExecutionFailed,
			workflowFailureMessage(result),
		)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, response)
}

// writeWorkflowError 把闭环协调器的错误映射为统一错误响应。
//
// 与单步骤执行接口共用同一套映射，并额外区分"验证结果未能持久化"。
func (h *stepExecutionHandler) writeWorkflowError(
	w http.ResponseWriter,
	r *http.Request,
	result app.ExecuteAndValidateResult,
	err error,
) {
	if errors.Is(err, app.ErrValidationNotPersisted) {
		writeError(
			w,
			r,
			h.logger,
			http.StatusInternalServerError,
			CodeExecutionNotPersisted,
			workflowNotPersistedMessage(result),
		)
		return
	}
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrStepExecutionInvalidInput),
		errors.Is(err, app.ErrStepWorkflowInvalidConfig),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrStepExecutionNotFound),
		errors.Is(err, app.ErrValidationNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrStepExecutionConflict),
		errors.Is(err, app.ErrValidationConflict),
		errors.Is(err, domain.ErrStepExecutionBlocked),
		errors.Is(err, domain.ErrValidationBlocked),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrStepExecutionPrerequisite),
		errors.Is(err, app.ErrValidationPrerequisite),
		errors.Is(err, domain.ErrValidationPrerequisite):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "步骤闭环接口处理失败", "error", message)
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// toAppRequest 把请求体转换为应用层请求。
func (b ExecuteAndValidateStepRequest) toAppRequest(
	taskID, stepID string,
) (app.ExecuteAndValidateRequest, error) {
	if b.TimeoutSeconds < 0 || b.TimeoutSeconds > maxExecuteTimeoutSeconds {
		return app.ExecuteAndValidateRequest{}, errors.Join(
			app.ErrStepExecutionInvalidInput,
			errors.New("超时秒数必须在 0 到 21600 之间"),
		)
	}
	if b.ValidationTimeoutSeconds < 0 || b.ValidationTimeoutSeconds > maxValidateTimeoutSeconds {
		return app.ExecuteAndValidateRequest{}, errors.Join(
			app.ErrStepExecutionInvalidInput,
			errors.New("验证超时秒数必须在 0 到 21600 之间"),
		)
	}
	return app.ExecuteAndValidateRequest{
		TaskID:            taskID,
		StepID:            stepID,
		Prompt:            b.Prompt,
		Timeout:           time.Duration(b.TimeoutSeconds) * time.Second,
		ValidationTimeout: time.Duration(b.ValidationTimeoutSeconds) * time.Second,
	}, nil
}

// executeAndValidateResponse 把闭环结果转换为对外表示。
func executeAndValidateResponse(result app.ExecuteAndValidateResult) ExecuteAndValidateStepResponse {
	checks := make([]ValidateCheckResultResponse, 0, len(result.Checks))
	for _, check := range result.Checks {
		checks = append(checks, validateCheckResultResponse(check))
	}
	return ExecuteAndValidateStepResponse{
		TaskID:                      result.TaskID.String(),
		StepID:                      result.StepID.String(),
		AttemptID:                   result.AttemptID.String(),
		AttemptNumber:               result.AttemptNumber,
		SessionID:                   result.SessionID.String(),
		Outcome:                     string(result.Outcome),
		DeveloperSummary:            result.DeveloperSummary,
		DeveloperError:              result.DeveloperFailure,
		ExitCode:                    result.ExitCode,
		DeveloperFinalized:          result.DeveloperFinalized,
		DeveloperFinalizationErrors: result.DeveloperFinalizationErrors,
		ValidationRan:               result.ValidationRan,
		ValidationSkipReason:        result.ValidationSkipReason,
		ValidationRunID:             result.ValidationRunID.String(),
		ValidationStatus:            string(result.ValidationStatus),
		ValidationVerdict:           string(result.ValidationVerdict),
		ValidationPassed:            result.ValidationPassed,
		ValidationSummary:           result.ValidationSummary,
		ValidationFailureSummary:    result.ValidationFailureSummary,
		Checks:                      checks,
		ValidationPersisted:         result.ValidationPersisted,
		ValidationPersistenceErrors: result.ValidationPersistenceErrors,
		InfrastructureError:         result.InfrastructureError,
		ReviewRan:                   result.ReviewRan,
		ReviewStatus:                string(result.ReviewStatus),
		ReviewSummary:               result.ReviewSummary,
		ValidatorSessionID:          validatorSessionIDOf(&result.ValidatorSessionID),
		Assessment:                  result.Assessment,
		FeedbackPersisted:           result.FeedbackPersisted,
		Verification:                string(result.Verification),
		StepStatus:                  string(result.StepStatus),
		TaskStatus:                  string(result.TaskStatus),
		Verified:                    result.Verified,
		Finalized:                   result.Finalized,
		FinalizationErrors:          result.FinalizationErrors,
		StartedAt:                   result.StartedAt,
		FinishedAt:                  result.FinishedAt,
	}
}

// workflowNotPersistedMessage 生成"闭环已执行但状态未完整写入"的说明。
func workflowNotPersistedMessage(result app.ExecuteAndValidateResult) string {
	message := "开发—验证闭环已结束，但结果未能完整写入数据库"
	details := append([]string(nil), result.ValidationPersistenceErrors...)
	details = append(details, result.FinalizationErrors...)
	details = append(details, result.DeveloperFinalizationErrors...)
	if len(details) > 0 {
		message += ": " + strings.Join(details, "；")
	}
	return message
}

// workflowFailureMessage 生成"闭环未通过验证"的说明。
func workflowFailureMessage(result app.ExecuteAndValidateResult) string {
	switch {
	case result.Outcome != domain.StepExecutionCompleted:
		detail := strings.TrimSpace(result.DeveloperFailure)
		if detail == "" {
			detail = strings.TrimSpace(result.DeveloperSummary)
		}
		if detail == "" {
			detail = "开发阶段未成功"
		}
		return "开发阶段未成功（" + string(result.Outcome) + "）: " + detail
	case !result.ValidationRan:
		reason := strings.TrimSpace(result.ValidationSkipReason)
		if reason == "" {
			reason = "验证未能启动"
		}
		return "步骤未通过验证: " + reason
	case result.InfrastructureError:
		return "验证因基础设施异常未拿到可用证据: " + result.ValidationFailureSummary
	default:
		detail := strings.TrimSpace(result.ValidationFailureSummary)
		if detail == "" {
			detail = "验证未通过"
		}
		return "步骤未通过验证（" + string(result.ValidationVerdict) + "）: " + detail
	}
}

// parseExecuteAndValidatePath 判断路径是否指向开发—验证闭环动作。
//
// 只有 {taskID}/steps/{stepID}/execute-and-validate 这一种形状成立；其余形状返回
// false，由执行记录查询或任务处理器给出各自的 404 / 405 语义。
func parseExecuteAndValidatePath(path string) (taskID, stepID string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 4 {
		return "", "", false
	}
	if segments[1] != stepsSegment || segments[3] != executeAndValidateSegment {
		return "", "", false
	}
	if !validTaskPathID(segments[0]) || !validTaskPathID(segments[2]) {
		return "", "", false
	}
	return segments[0], segments[2], true
}
