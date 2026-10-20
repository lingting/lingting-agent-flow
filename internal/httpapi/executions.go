package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// 执行记录查询接口路径。
//
// 路径统一挂在任务下：执行记录永远属于某条任务链，独立顶层路径会让调用方
// 以为可以跨任务查询。
const (
	// stepsSegment 是任务下步骤集合的路径段。
	stepsSegment = "steps"
	// attemptsSegment 是步骤下尝试集合的路径段。
	attemptsSegment = "attempts"
	// sessionsSegment 是尝试下会话集合、以及任务下会话详情的路径段。
	sessionsSegment = "sessions"
)

// StepResponse 是步骤的对外表示。
//
// 只暴露查询需要的字段：角色快照与验证规格属于执行期配置，不通过执行记录接口
// 输出，避免把内部配置细节固化进接口。
type StepResponse struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	StepType  string `json:"step_type"`
	Name      string `json:"name"`
	Objective string `json:"objective"`
	Sequence  int    `json:"sequence"`
	Status    string `json:"status"`
	// StatusTerminal 表示步骤是否已结束，前端据此决定是否需要轮询。
	StatusTerminal bool `json:"status_terminal"`
	// InputSummary 是步骤的输入摘要。
	InputSummary string `json:"input_summary,omitempty"`
	// OutputSummary 是步骤的输出摘要。
	OutputSummary string `json:"output_summary,omitempty"`
	// Error 是失败原因摘要。
	Error string `json:"error,omitempty"`
	// FailureStage 是失败发生的阶段名。
	FailureStage string `json:"failure_stage,omitempty"`
	// CurrentAttemptID 是当前正在执行的尝试，可为空。
	CurrentAttemptID string     `json:"current_attempt_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

// StepListResponse 是步骤列表的分页响应。
type StepListResponse struct {
	Records []StepResponse `json:"records"`
	Total   int            `json:"total"`
	Current int            `json:"current"`
	Size    int            `json:"size"`
}

// AttemptResponse 是执行尝试的对外表示。
type AttemptResponse struct {
	ID            string `json:"id"`
	TaskID        string `json:"task_id"`
	StepID        string `json:"step_id"`
	AttemptNumber int    `json:"attempt_number"`
	Status        string `json:"status"`
	// StatusTerminal 表示尝试是否已结束。
	StatusTerminal bool `json:"status_terminal"`
	// ExitCode 是主执行退出码，未能取得时为 -1。
	ExitCode int `json:"exit_code"`
	// TimedOut 与 Cancelled 分别表达超时与取消，二者互不替代。
	TimedOut  bool `json:"timed_out"`
	Cancelled bool `json:"cancelled"`
	// ErrorClass 是失败原因分类。
	ErrorClass string `json:"error_class,omitempty"`
	// FailureStage 是失败发生的阶段名。
	FailureStage string `json:"failure_stage,omitempty"`
	// Error 是失败原因摘要。
	Error string `json:"error,omitempty"`
	// ResultSummary 是结果摘要（验证判定与摘要）。
	ResultSummary string `json:"result_summary,omitempty"`
	// VerificationVerdict 是本次尝试的验证判定。
	VerificationVerdict string `json:"verification_verdict,omitempty"`
	// RetryReason 是触发下一次重试的原因。
	RetryReason string `json:"retry_reason,omitempty"`
	// SessionIDs 是本次尝试关联的会话标识。
	SessionIDs []string   `json:"session_ids"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// AttemptListResponse 是尝试列表的分页响应。
type AttemptListResponse struct {
	Records []AttemptResponse `json:"records"`
	Total   int               `json:"total"`
	Current int               `json:"current"`
	Size    int               `json:"size"`
}

// SessionResponse 是 Agent 会话的对外表示。
//
// 会话执行状态、原始会话归档状态与 HTML 导出状态分三个字段输出，不得合并：
// 调用方需要能区分"调用成功但归档失败"与"调用失败但会话已保存"。
type SessionResponse struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	StepID    string `json:"step_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
	// AgentRole 是会话的角色类型，取自角色快照。
	AgentRole string `json:"agent_role"`
	// RoleID 是会话的角色标识。
	RoleID string `json:"role_id"`
	// Provider 与 Model 是实际使用的模型提供方与模型标识。
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Status 是会话执行状态。
	Status string `json:"status"`
	// StatusTerminal 表示会话是否已结束。
	StatusTerminal bool `json:"status_terminal"`
	// RunStatus 是本次执行自身的运行结果，不等同于步骤验收结果。
	RunStatus string `json:"run_status,omitempty"`
	// SessionID 是外部运行器的会话标识。
	SessionID string `json:"session_id,omitempty"`
	// InvocationID 是本次 Agent 调用的唯一标识。
	InvocationID string `json:"invocation_id,omitempty"`
	// SessionFilePath 是原始会话文件位置，由服务端生成与管理。
	SessionFilePath string `json:"session_file_path,omitempty"`
	// ArchiveStatus 是原始会话归档状态。
	ArchiveStatus string `json:"archive_status"`
	// ArchivePath 是归档后的原始会话文件位置。
	ArchivePath string `json:"archive_path,omitempty"`
	// ExportStatus 是 HTML 导出状态，独立于归档状态。
	ExportStatus string `json:"export_status"`
	// ExportPath 是导出后的 HTML 文件位置。
	ExportPath string `json:"html_export_path,omitempty"`
	// ExitCode 是 Agent 进程退出码，未能取得时省略。
	ExitCode *int `json:"exit_code,omitempty"`
	// Error 是失败原因摘要。
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// SessionListResponse 是会话列表的分页响应。
type SessionListResponse struct {
	Records []SessionResponse `json:"records"`
	Total   int               `json:"total"`
	Current int               `json:"current"`
	Size    int               `json:"size"`
}

// executionHandler 处理执行记录只读查询。
type executionHandler struct {
	records *app.ExecutionRecordService
	logger  *slog.Logger
}

// serve 按路径分发任务下的执行记录查询请求。
//
// 支持的四类路径：
//   - {taskID}/steps
//   - {taskID}/steps/{stepID}/attempts
//   - {taskID}/steps/{stepID}/attempts/{attemptID}/sessions
//   - {taskID}/sessions/{sessionID}
//
// 其他形状一律按接口不存在处理：执行记录接口不提供任何写操作，因此不注册
// POST、PATCH、DELETE 等入口。
func (h *executionHandler) serve(w http.ResponseWriter, r *http.Request) {
	segments := splitPathSegments(strings.TrimPrefix(r.URL.Path, taskPathPrefix))
	switch {
	case len(segments) == 2 && segments[1] == stepsSegment:
		h.listSteps(w, r, segments[0])
	case len(segments) == 4 && segments[1] == stepsSegment && segments[3] == attemptsSegment:
		h.listAttempts(w, r, segments[0], segments[2])
	case len(segments) == 6 &&
		segments[1] == stepsSegment &&
		segments[3] == attemptsSegment &&
		segments[5] == sessionsSegment:
		h.listSessions(w, r, segments[0], segments[2], segments[4])
	case len(segments) == 3 && segments[1] == sessionsSegment:
		h.getSession(w, r, segments[0], segments[2])
	default:
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
	}
}

// listSteps 处理 GET /api/v1/tasks/{taskID}/steps。
func (h *executionHandler) listSteps(w http.ResponseWriter, r *http.Request, taskID string) {
	if !h.allowGet(w, r) {
		return
	}
	request, err := parseExecutionPageQuery(r)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	page, err := h.records.ListSteps(r.Context(), taskID, request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	records := make([]StepResponse, 0, len(page.Steps))
	for _, step := range page.Steps {
		records = append(records, stepResponse(step))
	}
	writeJSON(w, r, h.logger, http.StatusOK, StepListResponse{
		Records: records,
		Total:   page.Total,
		Current: normalizedCurrent(request.Current),
		Size:    normalizedSize(request.Size),
	})
}

// listAttempts 处理 GET /api/v1/tasks/{taskID}/steps/{stepID}/attempts。
func (h *executionHandler) listAttempts(
	w http.ResponseWriter,
	r *http.Request,
	taskID, stepID string,
) {
	if !h.allowGet(w, r) {
		return
	}
	request, err := parseExecutionPageQuery(r)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	page, err := h.records.ListAttempts(r.Context(), taskID, stepID, request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	records := make([]AttemptResponse, 0, len(page.Attempts))
	for _, attempt := range page.Attempts {
		records = append(records, attemptResponse(attempt))
	}
	writeJSON(w, r, h.logger, http.StatusOK, AttemptListResponse{
		Records: records,
		Total:   page.Total,
		Current: normalizedCurrent(request.Current),
		Size:    normalizedSize(request.Size),
	})
}

// listSessions 处理 GET /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/sessions。
func (h *executionHandler) listSessions(
	w http.ResponseWriter,
	r *http.Request,
	taskID, stepID, attemptID string,
) {
	if !h.allowGet(w, r) {
		return
	}
	request, err := parseExecutionPageQuery(r)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	page, err := h.records.ListSessions(r.Context(), taskID, stepID, attemptID, request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	records := make([]SessionResponse, 0, len(page.Sessions))
	for _, session := range page.Sessions {
		records = append(records, sessionResponse(session))
	}
	writeJSON(w, r, h.logger, http.StatusOK, SessionListResponse{
		Records: records,
		Total:   page.Total,
		Current: normalizedCurrent(request.Current),
		Size:    normalizedSize(request.Size),
	})
}

// getSession 处理 GET /api/v1/tasks/{taskID}/sessions/{sessionID}。
func (h *executionHandler) getSession(
	w http.ResponseWriter,
	r *http.Request,
	taskID, sessionID string,
) {
	if !h.allowGet(w, r) {
		return
	}
	session, err := h.records.GetSession(r.Context(), taskID, sessionID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, sessionResponse(session))
}

// allowGet 校验请求方法，不满足时写出 405 并返回 false。
func (h *executionHandler) allowGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	writeError(
		w,
		r,
		h.logger,
		http.StatusMethodNotAllowed,
		CodeMethodNotAllowed,
		"该接口只支持 GET 请求",
	)
	return false
}

// writeServiceError 把应用层错误映射为统一错误响应。
//
// 与任务接口共用同一套映射：参数错误 400、不存在 404、状态冲突 409、其他 500。
func (h *executionHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrExecutionRecordInvalidInput),
		errors.Is(err, app.ErrTaskInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrExecutionRecordNotFound),
		errors.Is(err, app.ErrTaskNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "执行记录接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// splitPathSegments 按 / 切分路径，忽略空段。
func splitPathSegments(raw string) []string {
	parts := strings.Split(raw, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		segments = append(segments, trimmed)
	}
	return segments
}

// parseExecutionPageQuery 解析执行记录列表的分页与筛选参数。
func parseExecutionPageQuery(r *http.Request) (app.ExecutionPageRequest, error) {
	query := r.URL.Query()
	request := app.ExecutionPageRequest{
		Current: 1,
		Size:    app.DefaultTaskPageSize,
	}
	if raw := strings.TrimSpace(query.Get("current")); raw != "" {
		current, err := strconv.Atoi(raw)
		if err != nil || current <= 0 {
			return app.ExecutionPageRequest{}, errors.Join(
				app.ErrExecutionRecordInvalidInput,
				errors.New("分页参数 current 必须是大于 0 的整数"),
			)
		}
		request.Current = current
	}
	if raw := strings.TrimSpace(query.Get("size")); raw != "" {
		size, err := strconv.Atoi(raw)
		if err != nil || size <= 0 {
			return app.ExecutionPageRequest{}, errors.Join(
				app.ErrExecutionRecordInvalidInput,
				errors.New("分页参数 size 必须是大于 0 的整数"),
			)
		}
		request.Size = size
	}
	// status 支持重复参数与逗号分隔两种写法，与任务列表一致。
	for _, value := range query["status"] {
		for _, item := range strings.Split(value, ",") {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			request.Statuses = append(request.Statuses, trimmed)
		}
	}
	return request, nil
}

// normalizedCurrent 返回生效的页号，用于响应回显。
func normalizedCurrent(current int) int {
	if current <= 0 {
		return 1
	}
	return current
}

// normalizedSize 返回生效的页大小，用于响应回显。
func normalizedSize(size int) int {
	if size <= 0 {
		return app.DefaultTaskPageSize
	}
	return size
}

// stepResponse 把领域步骤转换为对外表示。
func stepResponse(step domain.Step) StepResponse {
	response := StepResponse{
		ID:             step.ID.String(),
		TaskID:         step.TaskID.String(),
		StepType:       string(step.Type),
		Name:           step.Title,
		Objective:      step.Objective,
		Sequence:       step.Sequence,
		Status:         string(step.Status),
		StatusTerminal: step.Status.IsTerminal(),
		InputSummary:   step.Objective,
		OutputSummary:  step.Result,
		Error:          step.FailureSummary,
		FailureStage:   step.FailureStage,
		CreatedAt:      step.CreatedAt,
		UpdatedAt:      step.UpdatedAt,
		StartedAt:      step.StartedAt,
		FinishedAt:     step.FinishedAt,
	}
	if step.CurrentAttemptID != nil {
		response.CurrentAttemptID = step.CurrentAttemptID.String()
	}
	return response
}

// attemptResponse 把领域尝试转换为对外表示。
func attemptResponse(attempt domain.Attempt) AttemptResponse {
	response := AttemptResponse{
		ID:                  attempt.ID.String(),
		TaskID:              attempt.TaskID.String(),
		StepID:              attempt.StepID.String(),
		AttemptNumber:       attempt.AttemptNumber,
		Status:              string(attempt.Status),
		StatusTerminal:      attempt.Status.IsTerminal(),
		ExitCode:            attempt.ExitCode,
		TimedOut:            attempt.TimedOut,
		Cancelled:           attempt.Cancelled,
		ErrorClass:          attempt.ErrorClass,
		FailureStage:        attempt.FailureStage,
		Error:               attempt.FailureSummary,
		ResultSummary:       attempt.VerificationSummary,
		VerificationVerdict: string(attempt.VerificationVerdict),
		RetryReason:         attempt.RetryReason,
		SessionIDs:          make([]string, 0, len(attempt.SessionIDs)),
		CreatedAt:           attempt.CreatedAt,
		StartedAt:           attempt.StartedAt,
		FinishedAt:          attempt.FinishedAt,
	}
	for _, id := range attempt.SessionIDs {
		response.SessionIDs = append(response.SessionIDs, id.String())
	}
	return response
}

// sessionResponse 把领域会话转换为对外表示。
//
// 执行状态、归档状态与导出状态分别输出：三者相互独立，合并成一个字段会丢失
// "调用成功但归档失败"这类真实情况。
func sessionResponse(session domain.Session) SessionResponse {
	response := SessionResponse{
		ID:              session.ID.String(),
		TaskID:          session.TaskID.String(),
		AgentRole:       string(session.RoleSnapshot.Type),
		RoleID:          session.RoleID.String(),
		Provider:        session.Provider,
		Model:           session.Model,
		Status:          string(session.Status),
		StatusTerminal:  session.Status.IsTerminal(),
		RunStatus:       string(session.RunStatus),
		SessionID:       session.ExternalSessionID,
		InvocationID:    session.InvocationID,
		SessionFilePath: session.JSONLPath,
		ArchiveStatus:   archiveStatusOf(session.ArchiveStatus),
		ArchivePath:     session.ArchivePath,
		ExportStatus:    exportStatusOf(session.ExportStatus),
		ExportPath:      session.HTMLPath,
		ExitCode:        session.ExitCode,
		Error:           session.FailureSummary,
		CreatedAt:       session.CreatedAt,
		StartedAt:       session.StartedAt,
		FinishedAt:      session.FinishedAt,
	}
	if session.StepID != nil {
		response.StepID = session.StepID.String()
	}
	if session.AttemptID != nil {
		response.AttemptID = session.AttemptID.String()
	}
	return response
}

// archiveStatusOf 返回归档状态的对外取值。
//
// 空值表示"还没有归档结论"，对外统一表达为未归档，避免前端拿到空字符串后无法判断。
func archiveStatusOf(status domain.SessionArchiveStatus) string {
	if status == "" {
		return string(domain.SessionArchiveNotAttempted)
	}
	return string(status)
}

// exportStatusOf 返回 HTML 导出状态的对外取值。
//
// 与归档状态同理：未记录的导出状态统一表达为未尝试。
func exportStatusOf(status domain.SessionExportStatus) string {
	if status == "" {
		return string(domain.SessionExportNotAttempted)
	}
	return string(status)
}

// newExecutionHandler 构造执行记录查询处理器。
func newExecutionHandler(
	records *app.ExecutionRecordService,
	logger *slog.Logger,
) *executionHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &executionHandler{records: records, logger: logger}
}
