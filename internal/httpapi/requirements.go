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

// 需求接口的路径段与路径前缀。
const (
	// PathRequirements 是正式需求集合路径。
	PathRequirements = APIPrefix + "/requirements"
	// planningConfirmSegment 是确认方案并生成正式实体的动作路径段。
	planningConfirmSegment = "confirm"
	// requirementsSegment 是项目下需求集合的子资源路径段。
	requirementsSegment = "requirements"
)

// maxConfirmRequirementRequestBytes 是确认接口请求体的最大字节数。
//
// 请求体只包含两个标识与一个可选执行节点，因此上限取够用的小值。
const maxConfirmRequirementRequestBytes = 4 << 10

// RequirementResponse 是正式需求的对外表示。
//
// 字段与持久化事实一一对应，不做状态归一化：未知状态按原值输出，`project_id` /
// `planning_session_id` / `confirmed_at` 缺失表示该需求不是由确认流程生成的。
type RequirementResponse struct {
	// ID 是需求标识。
	ID string `json:"id"`
	// Title 是需求标题。
	Title string `json:"title"`
	// Description 是需求描述。
	Description string `json:"description"`
	// Priority 是需求优先级。
	Priority string `json:"priority"`
	// Status 是持久化的需求状态，未知取值原样输出。
	Status string `json:"status"`
	// Source 是需求来源。
	Source string `json:"source"`
	// ProjectID 是需求所属项目标识。
	ProjectID string `json:"project_id,omitempty"`
	// PlanningSessionID 是产生该需求的规划会话标识。
	PlanningSessionID string `json:"planning_session_id,omitempty"`
	// ConfirmedAt 是用户确认方案的时间。
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	// CreatedAt 是需求创建时间。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 是需求最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// RequirementTaskResponse 是确认生成的正式任务的对外表示。
//
// 它只输出任务自身的执行事实与配置摘要：`requirement_sequence` 是需求内顺序，
// `status` 是持久化状态（确认生成后为 queued）。接口不提供任何"开始执行"的语义。
type RequirementTaskResponse struct {
	// ID 是任务标识。
	ID string `json:"id"`
	// RequirementID 是任务所属需求。
	RequirementID string `json:"requirement_id"`
	// Status 是任务状态。
	Status string `json:"status"`
	// RequirementSequence 是任务在需求内的顺序，历史任务没有该事实时省略。
	RequirementSequence *int `json:"requirement_sequence,omitempty"`
	// HostID 是执行节点标识，本机执行时省略。
	HostID string `json:"host_id,omitempty"`
	// RepositoryPath 是仓库位置（已脱敏）。
	RepositoryPath string `json:"repository_path"`
	// BaseRef 是基础分支。
	BaseRef string `json:"base_ref"`
	// Goal 是任务目标描述（来自方案中的标题与描述）。
	Goal string `json:"goal"`
	// AcceptanceCriteria 是任务级验收条件。
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	// CreatedAt 是任务创建时间。
	CreatedAt time.Time `json:"created_at"`
}

// ConfirmRequirementResponse 是一次确认的创建结果。
type ConfirmRequirementResponse struct {
	// Requirement 是本次创建的正式需求。
	Requirement RequirementResponse `json:"requirement"`
	// Tasks 是本次创建的正式任务，按 requirement_sequence 升序排列。
	Tasks []RequirementTaskResponse `json:"tasks"`
	// AdoptedRevisionID 是被采纳的方案版本标识。
	AdoptedRevisionID string `json:"adopted_revision_id"`
}

// RequirementDetailResponse 是需求详情：需求本身与按顺序排列的正式任务。
type RequirementDetailResponse struct {
	Requirement RequirementResponse       `json:"requirement"`
	Tasks       []RequirementTaskResponse `json:"tasks"`
}

// RequirementListResponse 是项目需求列表响应。
type RequirementListResponse struct {
	Records []RequirementResponse `json:"records"`
	Total   int                   `json:"total"`
}

// confirmRequirementRequest 是确认方案的请求体。
//
// 只接受两个标识与一个可选执行节点：需求标识、任务列表、任务顺序、任务配置、项目仓库
// 配置与任务状态全部由服务端依据已确认的 Revision 计算，未知字段一律拒绝。
type confirmRequirementRequest struct {
	// RevisionID 是用户指定采纳的方案版本标识。
	RevisionID string `json:"revision_id"`
	// HostID 是可选执行节点标识。
	HostID string `json:"host_id"`
}

// requirementHandler 处理方案确认与正式需求查询接口。
type requirementHandler struct {
	service *app.RequirementService
	logger  *slog.Logger
}

// newRequirementHandler 构造需求接口处理器。
//
// 服务为空时仍返回处理器，接口由路由前置检查统一给出 503。
func newRequirementHandler(
	service *app.RequirementService,
	logger *slog.Logger,
) *requirementHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &requirementHandler{service: service, logger: logger}
}

// confirm 处理 POST /api/v1/planning/sessions/{sessionID}/confirm。
//
// 它调用生产 RequirementService.Confirm：校验用户指定的 Revision、在单个事务内生成正式
// 需求与任务并把会话推进为 confirmed。失败时整体回滚，不会留下部分实体。
func (h *requirementHandler) confirm(w http.ResponseWriter, r *http.Request) {
	sessionID := pathID(r, paramSessionID)
	var body confirmRequirementRequest
	if !decodeJSONBodyWithLimit(w, r, h.logger, &body, maxConfirmRequirementRequestBytes) {
		return
	}
	result, err := h.service.Confirm(r.Context(), app.ConfirmRequirementRequest{
		PlanningSessionID: sessionID,
		RevisionID:        body.RevisionID,
		HostID:            body.HostID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusCreated, ConfirmRequirementResponse{
		Requirement:       requirementResponse(result.Requirement),
		Tasks:             requirementTaskResponses(result.Tasks),
		AdoptedRevisionID: result.AdoptedRevisionID.String(),
	})
}

// detail 处理 GET /api/v1/requirements/{requirementID}。
func (h *requirementHandler) detail(w http.ResponseWriter, r *http.Request) {
	detail, err := h.service.Detail(r.Context(), pathID(r, paramRequirementID))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, RequirementDetailResponse{
		Requirement: requirementResponse(detail.Requirement),
		Tasks:       requirementTaskResponses(detail.Tasks),
	})
}

// listByProject 处理 GET /api/v1/projects/{projectID}/requirements。
func (h *requirementHandler) listByProject(w http.ResponseWriter, r *http.Request) {
	requirements, err := h.service.ListByProject(r.Context(), pathID(r, paramProjectID))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	records := make([]RequirementResponse, 0, len(requirements))
	for _, requirement := range requirements {
		records = append(records, requirementResponse(requirement))
	}
	writeJSON(w, r, h.logger, http.StatusOK, RequirementListResponse{
		Records: records,
		Total:   len(records),
	})
}

// writeError 把需求相关错误映射为统一错误响应。
//
// 映射依据 errors.Is 判定的错误类别，不解析错误文本：参数与方案非法 400、目标不存在
// 404、状态冲突 409、前置条件不满足 412、其余为持久化失败 500。
func (h *requirementHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrRequirementInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrRequirementNotFound),
		errors.Is(err, app.ErrRequirementRevisionNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrRequirementStateConflict),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrRequirementPrerequisite),
		errors.Is(err, domain.ErrValidationPrerequisite):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	case errors.Is(err, storage.ErrInvalidData):
		// 存储层拒绝写入不是参数问题：它是持久化失败，必须如实返回 500。
		status = http.StatusInternalServerError
		code = CodeExecutionNotPersisted
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError && code == CodeInternal {
		h.logger.ErrorContext(r.Context(), "需求接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// requirementResponse 把领域记录转换为对外表示。
func requirementResponse(requirement domain.Requirement) RequirementResponse {
	response := RequirementResponse{
		ID:          requirement.ID.String(),
		Title:       requirement.Title,
		Description: requirement.Description,
		Priority:    string(requirement.Priority),
		Status:      string(requirement.Status),
		Source:      string(requirement.Source),
		ProjectID:   requirement.ProjectID,
		ConfirmedAt: requirement.ConfirmedAt,
		CreatedAt:   requirement.CreatedAt,
		UpdatedAt:   requirement.UpdatedAt,
	}
	if requirement.PlanningSessionID != nil {
		response.PlanningSessionID = requirement.PlanningSessionID.String()
	}
	return response
}

// requirementTaskResponses 把正式任务列表转换为对外表示。
func requirementTaskResponses(tasks []domain.Task) []RequirementTaskResponse {
	records := make([]RequirementTaskResponse, 0, len(tasks))
	for _, task := range tasks {
		records = append(records, requirementTaskResponse(task))
	}
	return records
}

// requirementTaskResponse 把单个正式任务转换为对外表示。
func requirementTaskResponse(task domain.Task) RequirementTaskResponse {
	response := RequirementTaskResponse{
		ID:                  task.ID.String(),
		RequirementID:       task.RequirementID.String(),
		Status:              string(task.Status),
		RequirementSequence: task.RequirementSequence,
		RepositoryPath:      task.RepositoryPath,
		BaseRef:             task.BaseRef,
		Goal:                task.Config.Goal,
		AcceptanceCriteria:  task.Config.Verification.AcceptanceCriteria,
		CreatedAt:           task.CreatedAt,
	}
	if task.HostID != nil {
		response.HostID = task.HostID.String()
	}
	return response
}
