package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// executeStepsSegment 是顺序执行动作的路径段。
const executeStepsSegment = "execute-steps"

// ExecuteStepsRequest 是顺序执行开发步骤的请求体。
//
// 只接受执行参数：任务标识来自路径，步骤顺序完全由数据库中的计划决定，调用方
// 不能指定要执行哪些步骤、跳过哪些步骤或改变顺序。
type ExecuteStepsRequest struct {
	// Prompt 是可选补充说明，会追加在每一步骤的提示词之后。
	Prompt string `json:"prompt"`
	// TimeoutSeconds 是单个步骤的调用超时秒数，0 表示使用任务配置或服务端默认值。
	TimeoutSeconds int `json:"timeout_seconds"`
}

// ExecuteStepsResponse 是一次顺序执行的结果。
//
// 结构显式区分三件事：每一步发生了什么、为什么停止、任务当前处于什么状态。
// `stopped` 与 `success` 不能互相替代：前者表示是否提前停止，后者表示全部步骤是否
// 都以正常结束且状态已完整写入。
type ExecuteStepsResponse struct {
	TaskID string `json:"task_id"`
	// Steps 是按执行顺序排列的步骤结果。
	Steps []ExecuteStepsStepResponse `json:"steps"`
	// ExecutedSteps 是本次实际执行的步骤数量。
	ExecutedSteps int `json:"executed_steps"`
	// Stopped 表示是否因失败、中断或前置条件缺失提前停止。
	Stopped bool `json:"stopped"`
	// Success 表示全部开发步骤都已完成且状态已完整写入。
	Success bool `json:"success"`
	// StopReason 是停止原因，未提前停止时为空。
	StopReason string `json:"stop_reason,omitempty"`
	// StoppedStepID 是触发停止的步骤，未提前停止时为空。
	StoppedStepID string `json:"stopped_step_id,omitempty"`
	// TaskStatus 是执行结束后读取到的任务状态。
	TaskStatus string    `json:"task_status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// ExecuteStepsStepResponse 是顺序执行中单个步骤的结果。
type ExecuteStepsStepResponse struct {
	StepID   string `json:"step_id"`
	Sequence int    `json:"sequence"`
	Title    string `json:"title"`
	// Skipped 表示该步骤本次没有产生真实执行。
	Skipped    bool      `json:"skipped"`
	SkipReason string    `json:"skip_reason,omitempty"`
	AttemptID  string    `json:"attempt_id,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	StepStatus string    `json:"step_status,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	Error      string    `json:"error,omitempty"`
	Finalized  bool      `json:"finalized"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// serveSteps 处理 POST /api/v1/tasks/{taskID}/execute-steps。
//
// 该入口是同步的：只有已执行的步骤全部进入终态后才写出响应。执行本身已经由服务层
// 持久化，因此即使响应写入失败，执行记录与状态也不会丢失。
func (h *stepExecutionHandler) serveSteps(w http.ResponseWriter, r *http.Request, taskID string) {
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
	var body ExecuteStepsRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toAppRequest(taskID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	result, err := h.executor.ExecuteSteps(r.Context(), request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, executeStepsResponse(result))
}

// toAppRequest 把请求体转换为应用层请求。
func (b ExecuteStepsRequest) toAppRequest(taskID string) (app.ExecuteStepsRequest, error) {
	if b.TimeoutSeconds < 0 || b.TimeoutSeconds > maxExecuteTimeoutSeconds {
		return app.ExecuteStepsRequest{}, errors.Join(
			app.ErrStepExecutionInvalidInput,
			errors.New("超时秒数必须在 0 到 21600 之间"),
		)
	}
	return app.ExecuteStepsRequest{
		TaskID:  taskID,
		Prompt:  b.Prompt,
		Timeout: time.Duration(b.TimeoutSeconds) * time.Second,
	}, nil
}

// executeStepsResponse 把顺序执行结果转换为对外表示。
func executeStepsResponse(result app.StepSequenceResult) ExecuteStepsResponse {
	response := ExecuteStepsResponse{
		TaskID:        result.TaskID.String(),
		Steps:         make([]ExecuteStepsStepResponse, 0, len(result.Steps)),
		ExecutedSteps: len(result.Steps),
		Stopped:       result.Stopped,
		StopReason:    result.StopReason,
		TaskStatus:    string(result.TaskStatus),
		StartedAt:     result.StartedAt,
		FinishedAt:    result.FinishedAt,
	}
	if result.StoppedStepID != nil {
		response.StoppedStepID = result.StoppedStepID.String()
	}
	success := !result.Stopped && len(result.Steps) > 0
	for _, step := range result.Steps {
		entry := ExecuteStepsStepResponse{
			StepID:     step.StepID.String(),
			Sequence:   step.Sequence,
			Title:      step.Title,
			Skipped:    step.Skipped,
			SkipReason: step.SkipReason,
			Finalized:  step.Result.Finalized,
			StartedAt:  step.Result.StartedAt,
			FinishedAt: step.Result.FinishedAt,
		}
		if !step.Skipped {
			entry.AttemptID = step.Result.AttemptID.String()
			entry.SessionID = step.Result.SessionID.String()
			entry.Outcome = string(step.Result.Outcome)
			entry.StepStatus = string(step.Result.StepStatus)
			entry.Summary = step.Result.Summary
			entry.Error = step.Result.Failure
		}
		if step.Skipped ||
			step.Result.Outcome != domain.StepExecutionCompleted ||
			!step.Result.Finalized {
			success = false
		}
		response.Steps = append(response.Steps, entry)
	}
	response.Success = success
	return response
}

// parseExecuteStepsPath 判断路径是否指向顺序执行动作。
//
// 只接受 {taskID}/execute-steps 这一种形状：其余形状返回 false，由任务处理器给出
// 各自的 404 与 405 语义。
func parseExecuteStepsPath(path string) (taskID string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 2 || segments[1] != executeStepsSegment {
		return "", false
	}
	if !validTaskPathID(segments[0]) {
		return "", false
	}
	return segments[0], true
}
