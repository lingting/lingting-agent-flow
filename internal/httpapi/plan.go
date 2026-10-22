package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// planSegment 是计划生成动作的路径段。
const planSegment = "plan"

// PlanRequest 是生成计划请求的请求体。
//
// 只接受执行参数：任务标识来自路径，要生成哪些步骤、使用哪个角色完全由服务端依据
// 任务配置决定，调用方不能指定步骤列表、Session 路径、工作目录或角色覆盖。
type PlanRequest struct {
	// Prompt 是可选补充说明，会追加在编排器生成的规划提示词之后。
	Prompt string `json:"prompt"`
	// TimeoutSeconds 是本次 Planner 调用的超时秒数，0 表示使用任务配置或服务端默认值。
	TimeoutSeconds int `json:"timeout_seconds"`
}

// PlanResponse 是一次计划生成的结果。
//
// 结构显式区分四件事：任务最终状态、计划是否真的写入数据库、生成的步骤，以及失败
// 发生在哪个阶段。`persisted` 为 false 时任务不会处于 planned，前端不得把它展示为
// 计划已就绪。
type PlanResponse struct {
	TaskID string `json:"task_id"`
	// TaskStatus 是收尾后读取到的任务真实状态。
	TaskStatus string `json:"task_status"`
	// Persisted 表示计划是否已完整写入数据库。
	Persisted bool `json:"persisted"`
	// Summary 是 Planner 给出的一句话计划摘要。
	Summary string `json:"summary,omitempty"`
	// Steps 是已持久化的计划步骤，按序号升序排列。
	Steps []PlanStepResponse `json:"steps"`
	// SessionID 是本次 Planner 调用的会话标识。
	SessionID string `json:"session_id,omitempty"`
	// FailureStage / Failure 是失败阶段与原因摘要，成功时为空。
	FailureStage string    `json:"failure_stage,omitempty"`
	Failure      string    `json:"failure,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
}

// PlanStepResponse 是计划中单个步骤的对外表示。
type PlanStepResponse struct {
	StepID   string `json:"step_id"`
	Sequence int    `json:"sequence"`
	Title    string `json:"title"`
	Type     string `json:"step_type"`
	Status   string `json:"status"`
	// RoleID 是锁定到步骤上的执行角色。
	RoleID string `json:"role_id,omitempty"`
	// RequiredChecks 是生效的必需检查名称，用于确认任务级命令已合并进步骤。
	RequiredChecks []string `json:"required_checks"`
}

// servePlan 处理 POST /api/v1/tasks/{taskID}/plan。
//
// 该入口是同步的：只有计划写入数据库或失败收敛完成后才写出响应。计划本身已由服务层
// 持久化，因此即使响应写入失败，计划与任务状态也不会丢失。
func (rt *router) servePlan(w http.ResponseWriter, r *http.Request) {
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
	handler.plan(w, r)
}

// plan 处理一次计划生成请求。
func (h *workflowHandler) plan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 POST 请求")
		return
	}
	taskID, ok := actionTaskID(r.URL.Path, planSegment)
	if !ok {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	var body PlanRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toGeneratePlanRequest(taskID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.workflow.GeneratePlan(r.Context(), request)
	if err != nil {
		// 失败结果仍包含可用的诊断信息（会话、阶段、真实任务状态），但接口必须以
		// 错误响应结束：未写入计划时返回 200 会让调用方误以为计划已就绪。
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, planResponse(result))
}

// toGeneratePlanRequest 把请求体转换为应用层请求。
func (b PlanRequest) toGeneratePlanRequest(taskID string) (app.GeneratePlanRequest, error) {
	if b.TimeoutSeconds < 0 || b.TimeoutSeconds > maxExecuteTimeoutSeconds {
		return app.GeneratePlanRequest{}, errors.Join(
			app.ErrWorkflowInvalidInput,
			errors.New("规划超时秒数必须在 0 到 21600 之间"),
		)
	}
	return app.GeneratePlanRequest{
		TaskID:  taskID,
		Prompt:  b.Prompt,
		Timeout: time.Duration(b.TimeoutSeconds) * time.Second,
	}, nil
}

// planResponse 把计划生成结果转换为对外表示。
func planResponse(result app.PlanGenerationResult) PlanResponse {
	steps := make([]PlanStepResponse, 0, len(result.Steps))
	for _, step := range result.Steps {
		steps = append(steps, PlanStepResponse{
			StepID:         step.ID.String(),
			Sequence:       step.Sequence,
			Title:          step.Title,
			Type:           string(step.Type),
			Status:         string(step.Status),
			RoleID:         step.RoleID.String(),
			RequiredChecks: requiredCheckNames(step),
		})
	}
	return PlanResponse{
		TaskID:       result.TaskID.String(),
		TaskStatus:   string(result.TaskStatus),
		Persisted:    result.Persisted,
		Summary:      result.Summary,
		Steps:        steps,
		SessionID:    result.SessionID.String(),
		FailureStage: result.FailureStage,
		Failure:      result.Failure,
		StartedAt:    result.StartedAt,
		FinishedAt:   result.FinishedAt,
	}
}

// requiredCheckNames 返回步骤中生效的必需检查名称。
func requiredCheckNames(step domain.Step) []string {
	names := make([]string, 0, len(step.VerificationSpec.Commands))
	for _, command := range step.VerificationSpec.Commands {
		if !command.IsRequired() {
			continue
		}
		names = append(names, strings.TrimSpace(command.Name))
	}
	return names
}
