package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// 规划会话业务接口的路径段。
const (
	// planningMessagesSegment 是规划会话消息子资源路径段。
	planningMessagesSegment = "messages"
	// planningRevisionsSegment 是规划会话方案版本子资源路径段。
	planningRevisionsSegment = "revisions"
	// planningAnalysisSegment 是两阶段分析动作路径段。
	planningAnalysisSegment = "analysis"
	// planningAbandonSegment 是放弃规划动作路径段。
	planningAbandonSegment = "abandon"
)

// maxPlanningRequestBytes 是规划接口请求体的最大字节数。
//
// 请求体只包含标识、用户文本与可选的执行节点，因此上限取够用的小值。
const maxPlanningRequestBytes = 32 << 10

// PlanningMessageResponse 是一条规划消息的对外表示。
type PlanningMessageResponse struct {
	// ID 是消息标识。
	ID string `json:"id"`
	// PlanningSessionID 是消息所属规划会话。
	PlanningSessionID string `json:"planning_session_id"`
	// Sequence 是会话内的消息序号。
	Sequence int `json:"sequence"`
	// SenderType 是发送方类型，未知取值原样输出。
	SenderType string `json:"sender_type"`
	// RoleID 是发送消息的角色配置标识，用户与系统消息省略。
	RoleID string `json:"role_id,omitempty"`
	// Content 是消息正文。
	Content string `json:"content"`
	// CreatedAt 是消息创建时间。
	CreatedAt time.Time `json:"created_at"`
}

// PlanningRevisionResponse 是一版规划快照的对外表示。
//
// 两个快照以原始 JSON 输出：它们已经是确定结构，接口层不再二次编码，避免预览内容与
// 落库内容出现差异。
type PlanningRevisionResponse struct {
	// ID 是方案版本标识。
	ID string `json:"id"`
	// PlanningSessionID 是版本所属规划会话。
	PlanningSessionID string `json:"planning_session_id"`
	// RevisionNumber 是会话内的版本号。
	RevisionNumber int `json:"revision_number"`
	// SnapshotVersion 是快照结构版本。
	SnapshotVersion int `json:"snapshot_version"`
	// RequirementAnalysis 是需求分析快照。
	RequirementAnalysis json.RawMessage `json:"requirement_analysis"`
	// ImplementationPlan 是实施方案快照。
	ImplementationPlan json.RawMessage `json:"implementation_plan"`
	// ValidationResult 是本轮结构化校验结论，尚未校验时省略。
	ValidationResult json.RawMessage `json:"validation_result,omitempty"`
	// CreatedAt 是版本生成时间。
	CreatedAt time.Time `json:"created_at"`
}

// PlanningMessageListResponse 是规划消息列表响应。
type PlanningMessageListResponse struct {
	Records []PlanningMessageResponse `json:"records"`
	Total   int                       `json:"total"`
}

// PlanningRevisionListResponse 是规划版本列表响应。
type PlanningRevisionListResponse struct {
	Records []PlanningRevisionResponse `json:"records"`
	Total   int                        `json:"total"`
}

// PlanningAnalysisResponse 是一轮两阶段分析的结果。
//
// 分析结果以领域类型直接输出：它们是本阶段唯一的字段契约，接口层再复制一份字段会让
// 快照结构与响应结构各自演化。
type PlanningAnalysisResponse struct {
	// Session 是推进到 preview_ready 的规划会话。
	Session PlanningSessionResponse `json:"session"`
	// Revision 是本次追加的方案版本。
	Revision PlanningRevisionResponse `json:"revision"`
	// Messages 是本次追加的两个角色消息。
	Messages []PlanningMessageResponse `json:"messages"`
	// RequirementAnalysis 是通过校验的需求分析。
	RequirementAnalysis domain.RequirementAnalysis `json:"requirement_analysis"`
	// ImplementationPlan 是通过校验的实施方案。
	ImplementationPlan domain.ImplementationPlan `json:"implementation_plan"`
}

// planningCreateRequest 是创建规划会话的请求体。
type planningCreateRequest struct {
	// ProjectID 是会话所属项目标识，必填。
	ProjectID string `json:"project_id"`
}

// planningMessageRequest 是提交用户消息的请求体。
//
// 它只接受正文：发送方、发送时间、序号与角色标识全部由服务端决定。
type planningMessageRequest struct {
	// Content 是用户输入原文。
	Content string `json:"content"`
}

// planningRunRequest 是运行分析的请求体。
//
// 它可以整体省略：省略时不指定执行节点，由服务端按会话已持久化的绑定决定。
type planningRunRequest struct {
	// HostID 是可选执行节点标识，仅在运行环境尚未准备时使用。
	HostID string `json:"host_id"`
}

// planningRetryRequest 是显式重试的请求体。
type planningRetryRequest struct {
	// Content 是可选的用户补充消息。
	Content string `json:"content"`
	// HostID 是可选执行节点标识。
	HostID string `json:"host_id"`
}

// create 处理 POST /api/v1/planning/sessions。
func (h *planningHandler) create(w http.ResponseWriter, r *http.Request) {
	var body planningCreateRequest
	if !decodeJSONBodyWithLimit(w, r, h.logger, &body, maxPlanningRequestBytes) {
		return
	}
	session, err := h.sessions.Create(r.Context(), app.CreatePlanningSessionRequest{
		ProjectID: body.ProjectID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusCreated, PlanningSessionDetailResponse{
		Session: planningSessionResponse(session),
	})
}

// list 处理 GET /api/v1/planning/sessions?project_id=...。
func (h *planningHandler) list(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeError(
			w,
			r,
			h.logger,
			http.StatusBadRequest,
			CodeInvalidRequest,
			"查询参数 project_id 不能为空",
		)
		return
	}
	sessions, err := h.sessions.ListByProject(r.Context(), projectID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	records := make([]PlanningSessionResponse, 0, len(sessions))
	for _, session := range sessions {
		records = append(records, planningSessionResponse(session))
	}
	writeJSON(w, r, h.logger, http.StatusOK, map[string]any{
		"records": records,
		"total":   len(records),
	})
}

// listMessages 处理 GET /api/v1/planning/sessions/{sessionID}/messages。
func (h *planningHandler) listMessages(w http.ResponseWriter, r *http.Request) {
	messages, err := h.sessions.Messages(r.Context(), pathID(r, paramSessionID))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	records := make([]PlanningMessageResponse, 0, len(messages))
	for _, message := range messages {
		records = append(records, planningMessageResponse(message))
	}
	writeJSON(w, r, h.logger, http.StatusOK, PlanningMessageListResponse{
		Records: records,
		Total:   len(records),
	})
}

// submitMessage 处理 POST /api/v1/planning/sessions/{sessionID}/messages。
//
// 它只追加用户消息并启动一轮分析（active / preview_ready → analyzing）。模型调用不在
// 这里发生：两阶段分析由 analysis 接口显式触发。
func (h *planningHandler) submitMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := pathID(r, paramSessionID)
	var body planningMessageRequest
	if !decodeJSONBodyWithLimit(w, r, h.logger, &body, maxPlanningRequestBytes) {
		return
	}
	start, err := h.sessions.SubmitMessage(r.Context(), sessionID, app.SubmitPlanningMessageRequest{
		Content: body.Content,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	response := map[string]any{
		"session": planningSessionResponse(start.Session),
	}
	if start.Message != nil {
		response["message"] = planningMessageResponse(*start.Message)
	}
	writeJSON(w, r, h.logger, http.StatusCreated, response)
}

// listRevisions 处理 GET /api/v1/planning/sessions/{sessionID}/revisions。
func (h *planningHandler) listRevisions(w http.ResponseWriter, r *http.Request) {
	revisions, err := h.sessions.Revisions(r.Context(), pathID(r, paramSessionID))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	records := make([]PlanningRevisionResponse, 0, len(revisions))
	for _, revision := range revisions {
		records = append(records, planningRevisionResponse(revision))
	}
	writeJSON(w, r, h.logger, http.StatusOK, PlanningRevisionListResponse{
		Records: records,
		Total:   len(records),
	})
}

// analyze 处理 POST /api/v1/planning/sessions/{sessionID}/analysis。
//
// 它在请求内同步执行两个阶段：占用由数据库条件更新完成，因此并发请求只会有一个真正
// 开始模型调用，其余返回 409。
func (h *planningHandler) analyze(w http.ResponseWriter, r *http.Request) {
	sessionID := pathID(r, paramSessionID)
	var body planningRunRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	result, err := h.analysis.Analyze(r.Context(), app.RunPlanningAnalysisRequest{
		PlanningSessionID: sessionID,
		HostID:            strings.TrimSpace(body.HostID),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, planningAnalysisResponse(result))
}

// retryAnalysis 处理 POST /api/v1/planning/sessions/{sessionID}/analysis/retry。
//
// 重试是显式动作：先由规划会话服务把 failed 收敛回 analyzing（可选附带补充消息），
// 再执行一轮完整的两阶段分析。它不接受状态覆盖，也不允许在 analyzing 期间强行插入。
func (h *planningHandler) retryAnalysis(w http.ResponseWriter, r *http.Request) {
	sessionID := pathID(r, paramSessionID)
	var body planningRetryRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	if _, err := h.sessions.RetryAnalysis(
		r.Context(),
		sessionID,
		app.RetryPlanningAnalysisRequest{Content: body.Content},
	); err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.analysis.Analyze(r.Context(), app.RunPlanningAnalysisRequest{
		PlanningSessionID: sessionID,
		HostID:            strings.TrimSpace(body.HostID),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, planningAnalysisResponse(result))
}

// abandon 处理 POST /api/v1/planning/sessions/{sessionID}/abandon。
func (h *planningHandler) abandon(w http.ResponseWriter, r *http.Request) {
	session, err := h.sessions.Abandon(r.Context(), pathID(r, paramSessionID))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, PlanningSessionDetailResponse{
		Session: planningSessionResponse(session),
	})
}

// planningMessageResponse 把规划消息转换为对外表示。
func planningMessageResponse(message domain.PlanningMessage) PlanningMessageResponse {
	response := PlanningMessageResponse{
		ID:                message.ID.String(),
		PlanningSessionID: message.PlanningSessionID.String(),
		Sequence:          message.Sequence,
		SenderType:        string(message.SenderType),
		Content:           message.Content,
		CreatedAt:         message.CreatedAt,
	}
	if message.RoleID != nil {
		response.RoleID = message.RoleID.String()
	}
	return response
}

// planningRevisionResponse 把规划版本转换为对外表示。
func planningRevisionResponse(revision domain.PlanningRevision) PlanningRevisionResponse {
	return PlanningRevisionResponse{
		ID:                  revision.ID.String(),
		PlanningSessionID:   revision.PlanningSessionID.String(),
		RevisionNumber:      revision.RevisionNumber,
		SnapshotVersion:     revision.SnapshotVersion,
		RequirementAnalysis: revision.RequirementAnalysisJSON,
		ImplementationPlan:  revision.ImplementationPlanJSON,
		ValidationResult:    revision.ValidationResultJSON,
		CreatedAt:           revision.CreatedAt,
	}
}

// planningAnalysisResponse 把两阶段分析结果转换为对外表示。
func planningAnalysisResponse(result app.PlanningAnalysisResult) PlanningAnalysisResponse {
	messages := make([]PlanningMessageResponse, 0, len(result.Messages))
	for _, message := range result.Messages {
		messages = append(messages, planningMessageResponse(message))
	}
	return PlanningAnalysisResponse{
		Session:             planningSessionResponse(result.Session),
		Revision:            planningRevisionResponse(result.Revision),
		Messages:            messages,
		RequirementAnalysis: result.Analysis,
		ImplementationPlan:  result.Plan,
	}
}

// isPlanningAnalysisError 判断错误是否属于两阶段分析编排。
//
// 它只用于把"分析已真实执行但没有成功"与"参数、状态或目标问题"分开映射，不解析文本。
func isPlanningAnalysisError(err error) bool {
	return errors.Is(err, app.ErrPlanningAnalysisAgentFailed) ||
		errors.Is(err, app.ErrPlanningAnalysisOutputInvalid)
}
