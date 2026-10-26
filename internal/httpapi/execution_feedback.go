package httpapi

import (
	"net/http"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 验证失败反馈接口路径段。
//
// 反馈按验证记录查询，不提供步骤级"最近一次"入口：验证历史接口已经返回每条验证
// 记录及其检查结果，再开一个"最近一次反馈"入口会让同一语义出现两条路径。
// 判定依据见 `.steps/P4-M2-5-validation-feedback-api.md` §3。
const feedbackSegment = "feedback"

// ValidationFailureFeedbackResponse 是一次验证失败的对外反馈表示。
//
// 数据来源是已持久化的 `domain.ValidationFailureFeedback`：脱敏与长度限制在写入时
// 由领域层完成，本接口不再拼接原始命令输出，也不重新执行验证。
//
// 反馈只描述"上一轮验证失败了什么"，不是当前验证结论：调用方必须结合同一验证记录
// 的 `verdict` 判断结论，不能因为拿到了反馈就认为步骤当前仍未通过。
type ValidationFailureFeedbackResponse struct {
	TaskID    string `json:"task_id"`
	StepID    string `json:"step_id"`
	AttemptID string `json:"developer_attempt_id"`
	// ValidationRunID 是产生该反馈的验证记录标识。
	ValidationRunID string `json:"validation_run_id"`
	// DeveloperSessionIDs 是产生待验证代码的开发会话，便于回溯上下文。
	DeveloperSessionIDs []string `json:"developer_session_ids,omitempty"`
	// Verdict 是上一轮验证的结论。
	Verdict string `json:"verdict"`
	// Summary 是上一轮验证的结论摘要（已脱敏并裁剪）。
	Summary string `json:"summary,omitempty"`
	// InfrastructureError 表示上一轮验证因基础设施异常没有拿到可用证据。
	//
	// 它与"代码检查失败"语义不同：调用方不得把为真的该字段当作代码问题。
	InfrastructureError bool `json:"infrastructure_error"`
	// Checks 是未通过的检查，最多保留领域层定义的上限条数。
	Checks []ValidationCheckFeedbackResponse `json:"checks"`
	// CreatedAt 是反馈生成时间。
	CreatedAt time.Time `json:"created_at"`
}

// ValidationCheckFeedbackResponse 是单条未通过检查的对外反馈表示。
//
// 它与 `ValidateCheckResultResponse` 的差别来自数据来源：反馈是持久化时的快照，
// 因此没有路径、序号与输出归档字段，只有失败判定需要的摘要信息。
type ValidationCheckFeedbackResponse struct {
	CheckID   string `json:"check_id"`
	CheckName string `json:"check_name"`
	// Required 表示该检查是否属于必需检查。
	Required bool `json:"required"`
	// Status 是检查的实际状态：failed / timed_out / cancelled / skipped / error。
	Status string `json:"status"`
	// ExitCode 是实际退出码，未能取得时为 -1。
	ExitCode int `json:"exit_code"`
	// FailureReason 是判定原因（已脱敏并裁剪）。
	FailureReason string `json:"failure_reason,omitempty"`
	// ErrorDetail 是执行层错误摘要（已脱敏并裁剪）。
	ErrorDetail string `json:"error_detail,omitempty"`
	// StdoutSummary / StderrSummary 是有长度限制的输出摘要（已脱敏）。
	StdoutSummary string `json:"stdout_summary,omitempty"`
	StderrSummary string `json:"stderr_summary,omitempty"`
	// TimedOut / Cancelled 分别表达超时与取消，二者互不替代。
	TimedOut  bool `json:"timed_out"`
	Cancelled bool `json:"cancelled"`
	// InfrastructureError 表示该检查没有拿到可用证据。
	InfrastructureError bool      `json:"infrastructure_error"`
	StartedAt           time.Time `json:"started_at"`
	FinishedAt          time.Time `json:"finished_at"`
}

// getValidationFeedback 处理
// GET /api/v1/tasks/{taskID}/steps/{stepID}/validations/{runID}/feedback。
//
// 只读：不创建反馈、不发起 Agent 调用、不执行检查命令，也不改动任务、步骤或验证
// 状态。没有反馈时返回 404，而不是返回一个空反馈对象——空反馈会让调用方误以为
// "上一轮验证没有失败"。
func (h *executionHandler) getValidationFeedback(
	w http.ResponseWriter,
	r *http.Request,
	taskID, stepID, runID string,
) {
	if !h.allowGet(w, r) {
		return
	}
	feedback, err := h.records.GetValidationFailureFeedback(r.Context(), taskID, stepID, runID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, validationFailureFeedbackResponse(feedback))
}

// validationFailureFeedbackResponse 把领域反馈转换为对外表示。
//
// 只做字段映射：脱敏与长度限制已在写入时由领域层完成，这里不得重新拼接原始输出。
func validationFailureFeedbackResponse(
	feedback domain.ValidationFailureFeedback,
) ValidationFailureFeedbackResponse {
	response := ValidationFailureFeedbackResponse{
		TaskID:              feedback.TaskID.String(),
		StepID:              feedback.StepID.String(),
		AttemptID:           feedback.DeveloperAttemptID.String(),
		ValidationRunID:     feedback.ValidationRunID.String(),
		DeveloperSessionIDs: make([]string, 0, len(feedback.DeveloperSessionIDs)),
		Verdict:             string(feedback.Verdict),
		Summary:             feedback.Summary,
		InfrastructureError: feedback.InfrastructureError,
		Checks:              make([]ValidationCheckFeedbackResponse, 0, len(feedback.Checks)),
		CreatedAt:           feedback.CreatedAt,
	}
	for _, id := range feedback.DeveloperSessionIDs {
		response.DeveloperSessionIDs = append(response.DeveloperSessionIDs, id.String())
	}
	for _, check := range feedback.Checks {
		response.Checks = append(response.Checks, ValidationCheckFeedbackResponse{
			CheckID:             check.CheckID,
			CheckName:           check.CheckName,
			Required:            check.Required,
			Status:              string(check.Status),
			ExitCode:            check.ExitCode,
			FailureReason:       check.FailureReason,
			ErrorDetail:         check.ErrorDetail,
			StdoutSummary:       check.StdoutSummary,
			StderrSummary:       check.StderrSummary,
			TimedOut:            check.TimedOut,
			Cancelled:           check.Cancelled,
			InfrastructureError: check.InfrastructureError,
			StartedAt:           check.StartedAt,
			FinishedAt:          check.FinishedAt,
		})
	}
	return response
}
