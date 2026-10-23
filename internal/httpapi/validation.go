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

// 验证接口路径。
//
// 路径挂在任务、步骤与尝试之下，与执行记录查询保持一致：验证永远属于某条任务链，
// 独立顶层路径会让调用方以为可以跨任务验证任意尝试。
const (
	// validateSegment 是验证动作的路径段。
	validateSegment = "validate"
	// maxValidateTimeoutSeconds 是单次验证允许请求的最大超时秒数。
	maxValidateTimeoutSeconds = 6 * 60 * 60
)

// ValidateAttemptRequest 是发起一次验证的请求体。
//
// 只接受验证参数：任务、步骤与尝试标识来自路径，工作目录、命令行与环境变量都由
// 服务端按已校验的配置生成，调用方无法指定。
type ValidateAttemptRequest struct {
	// TimeoutSeconds 是本次验证的总超时秒数，0 表示使用任务配置或服务端默认值。
	TimeoutSeconds int `json:"timeout_seconds"`
	// Checks 是可选的检查覆盖配置。
	//
	// 覆盖不能放宽门槛：服务端仍要求存在启用的必需检查，并执行同一套结论规则。
	Checks []ValidateCheckRequest `json:"checks"`
}

// ValidateCheckRequest 是一项检查的对外配置表示。
//
// 命令以可执行文件与参数数组表达，不接受整条命令字符串：服务端不会把请求内容
// 交给 shell 解释。
type ValidateCheckRequest struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	Enabled                bool     `json:"enabled"`
	Required               bool     `json:"required"`
	Sequence               int      `json:"sequence"`
	Command                string   `json:"command"`
	Args                   []string `json:"args"`
	WorkDir                string   `json:"work_dir"`
	TimeoutSeconds         int      `json:"timeout_seconds"`
	MaxOutputBytes         int      `json:"max_output_bytes"`
	ExpectedExitCodes      []int    `json:"expected_exit_codes"`
	ExpectedStdoutContains string   `json:"expected_stdout_contains"`
	ExpectedStderrContains string   `json:"expected_stderr_contains"`
}

// ValidateAttemptResponse 是一次验证的对外表示。
//
// 状态、结论与持久化结果分别输出：调用方必须能区分"流程跑完了"、"结论是通过"
// 与"结果已经可靠保存"。
type ValidateAttemptResponse struct {
	ValidationRunID string `json:"validation_run_id"`
	TaskID          string `json:"task_id"`
	StepID          string `json:"step_id"`
	AttemptID       string `json:"attempt_id"`
	// Status 是验证流程状态：pending / running / completed / failed / cancelled / timed_out。
	Status string `json:"status"`
	// Verdict 是最终结论：passed / failed / inconclusive。
	Verdict string `json:"verdict"`
	// Passed 是结论的便捷布尔值，不表示流程一定正常结束。
	Passed         bool   `json:"passed"`
	Summary        string `json:"summary,omitempty"`
	FailureSummary string `json:"failure_summary,omitempty"`
	// PassedChecks / FailedChecks / SkippedChecks 是检查统计。
	PassedChecks  int `json:"passed_checks"`
	FailedChecks  int `json:"failed_checks"`
	SkippedChecks int `json:"skipped_checks"`
	// ExecutionEnv 是执行环境类型，Workspace 是执行命令的工作区标识。
	ExecutionEnv string `json:"execution_env,omitempty"`
	Workspace    string `json:"workspace,omitempty"`
	ContainerID  string `json:"container_id,omitempty"`
	// Persisted 表示结论与检查结果是否全部可靠写入数据库。
	Persisted bool `json:"persisted"`
	// PersistenceErrors 是写入失败的原因，成功时为空。
	PersistenceErrors []string `json:"persistence_errors,omitempty"`
	// ReviewRan 表示本轮验证是否真的发起了语义审查。
	ReviewRan bool `json:"review_ran"`
	// ReviewStatus 是语义审查的实际结论；未发起审查时为空。
	ReviewStatus string `json:"review_status,omitempty"`
	// ReviewSummary 是语义审查的可读说明（已脱敏并裁剪）。
	ReviewSummary string `json:"review_summary,omitempty"`
	// ValidatorSessionID 是语义审查使用的会话标识。
	ValidatorSessionID string `json:"validator_session_id,omitempty"`
	// Assessment 是语义审查产出的结构化评估。
	Assessment *domain.AgentAssessment `json:"assessment,omitempty"`
	// Checks 是逐项检查结果的摘要，不含完整 stdout / stderr。
	Checks     []ValidateCheckResultResponse `json:"checks"`
	StartedAt  time.Time                     `json:"started_at"`
	FinishedAt time.Time                     `json:"finished_at"`
}

// ValidateCheckResultResponse 是单项检查结果的对外表示。
//
// 完整输出只通过路径暴露，不把大段 stdout / stderr 塞进单个响应。
type ValidateCheckResultResponse struct {
	CheckID   string `json:"check_id"`
	CheckName string `json:"check_name"`
	Required  bool   `json:"required"`
	Sequence  int    `json:"sequence"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	// TimedOut / Cancelled / Certain 分别表达超时、取消与结果是否可信。
	TimedOut      bool   `json:"timed_out"`
	Cancelled     bool   `json:"cancelled"`
	Certain       bool   `json:"certain"`
	RequiredPass  bool   `json:"required_passed"`
	DurationMS    int64  `json:"duration_ms"`
	StdoutPath    string `json:"stdout_path,omitempty"`
	StderrPath    string `json:"stderr_path,omitempty"`
	StdoutSummary string `json:"stdout_summary,omitempty"`
	StderrSummary string `json:"stderr_summary,omitempty"`
	OutputTrunc   bool   `json:"output_truncated"`
	// ArchiveError 记录输出落盘失败的原因，与检查状态相互独立。
	ArchiveError  string    `json:"output_archive_error,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	ErrorDetail   string    `json:"error_detail,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
}

// validationHandler 处理验证请求。
type validationHandler struct {
	service *app.ValidationService
	logger  *slog.Logger
}

// newValidationHandler 构造验证处理器。
//
// 服务为空时仍然返回处理器：请求会得到 503，而不是 404 或前端页面。
func newValidationHandler(
	service *app.ValidationService,
	logger *slog.Logger,
) *validationHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &validationHandler{service: service, logger: logger}
}

// serve 处理 POST /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/validate。
//
// 该入口是同步的：只有验证进入终态后才写出响应。验证本身已经由服务层持久化，
// 因此即使响应写入失败，验证记录与检查结果也不会丢失。
func (h *validationHandler) serve(
	w http.ResponseWriter,
	r *http.Request,
	taskID, stepID, attemptID string,
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
	if h.service == nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"验证服务未初始化",
		)
		return
	}
	var body ValidateAttemptRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toAppRequest(taskID, stepID, attemptID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	result, err := h.service.Validate(r.Context(), request)
	if err != nil {
		// 验证已经执行但结果未能可靠保存时，仍然返回可诊断的结论摘要：
		// 调用方需要同时看到"结论是什么"与"它没有被可靠保存"。
		if errors.Is(err, app.ErrValidationNotPersisted) {
			h.writeNotPersisted(w, r, result, err)
			return
		}
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, validateAttemptResponse(result))
}

// writeNotPersisted 写出"验证已执行但结果未能可靠保存"的响应。
func (h *validationHandler) writeNotPersisted(
	w http.ResponseWriter,
	r *http.Request,
	result app.ValidationExecuteResult,
	err error,
) {
	message := "验证已执行，但结果未能可靠写入数据库"
	if len(result.PersistenceErrors) > 0 {
		message += ": " + strings.Join(result.PersistenceErrors, "；")
	}
	h.logger.ErrorContext(
		r.Context(),
		"验证结果未能持久化",
		slog.String("validation_run_id", result.ValidationRunID.String()),
		slog.String("error", err.Error()),
	)
	writeError(
		w,
		r,
		h.logger,
		http.StatusInternalServerError,
		CodeExecutionNotPersisted,
		message,
	)
}

// toAppRequest 把请求体转换为应用层请求。
func (b ValidateAttemptRequest) toAppRequest(
	taskID, stepID, attemptID string,
) (app.ValidationExecuteRequest, error) {
	if b.TimeoutSeconds < 0 || b.TimeoutSeconds > maxValidateTimeoutSeconds {
		return app.ValidationExecuteRequest{}, errors.Join(
			app.ErrValidationInvalidInput,
			errors.New("超时秒数必须在 0 到 21600 之间"),
		)
	}
	checks := make([]domain.ValidationCheckDefinition, 0, len(b.Checks))
	for _, check := range b.Checks {
		checks = append(checks, check.toDomain())
	}
	return app.ValidationExecuteRequest{
		TaskID:    taskID,
		StepID:    stepID,
		AttemptID: attemptID,
		Timeout:   time.Duration(b.TimeoutSeconds) * time.Second,
		Checks:    checks,
	}, nil
}

// toDomain 把请求表示转换为领域定义。
//
// 类型固定为命令型检查：请求无法要求服务端执行非命令类检查，也无法指定 shell。
func (c ValidateCheckRequest) toDomain() domain.ValidationCheckDefinition {
	return domain.ValidationCheckDefinition{
		ID:                     strings.TrimSpace(c.ID),
		Name:                   strings.TrimSpace(c.Name),
		Description:            strings.TrimSpace(c.Description),
		Type:                   domain.ValidationCheckTypeCommand,
		Enabled:                c.Enabled,
		Required:               c.Required,
		Sequence:               c.Sequence,
		Command:                c.Command,
		Args:                   c.Args,
		WorkDir:                strings.TrimSpace(c.WorkDir),
		TimeoutSeconds:         c.TimeoutSeconds,
		MaxOutputBytes:         c.MaxOutputBytes,
		ExpectedExitCodes:      c.ExpectedExitCodes,
		ExpectedStdoutContains: c.ExpectedStdoutContains,
		ExpectedStderrContains: c.ExpectedStderrContains,
	}
}

// validateAttemptResponse 把验证结果转换为对外表示。
func validateAttemptResponse(result app.ValidationExecuteResult) ValidateAttemptResponse {
	checks := make([]ValidateCheckResultResponse, 0, len(result.Checks))
	for _, check := range result.Checks {
		checks = append(checks, validateCheckResultResponse(check))
	}
	return ValidateAttemptResponse{
		ValidationRunID:    result.ValidationRunID.String(),
		TaskID:             result.TaskID.String(),
		StepID:             result.StepID.String(),
		AttemptID:          result.AttemptID.String(),
		Status:             string(result.Status),
		Verdict:            string(result.Verdict),
		Passed:             result.Passed,
		Summary:            result.Summary,
		FailureSummary:     result.FailureSummary,
		PassedChecks:       result.PassedChecks,
		FailedChecks:       result.FailedChecks,
		SkippedChecks:      result.SkippedChecks,
		ExecutionEnv:       result.ExecutionEnv,
		Workspace:          result.Workspace,
		ContainerID:        result.ContainerID,
		ReviewRan:          result.ReviewRan,
		ReviewStatus:       string(result.ReviewStatus),
		ReviewSummary:      result.ReviewSummary,
		ValidatorSessionID: validatorSessionIDOf(&result.ValidatorSessionID),
		Assessment:         result.Assessment,
		Persisted:          result.Persisted,
		PersistenceErrors:  result.PersistenceErrors,
		Checks:             checks,
		StartedAt:          result.StartedAt,
		FinishedAt:         result.FinishedAt,
	}
}

// validateCheckResultResponse 把单条检查结果转换为对外表示。
//
// 验证接口与开发—验证闭环接口共用同一套转换：两者输出的检查结果必须完全一致，
// 否则调用方需要为同一份数据维护两种解析逻辑。
func validateCheckResultResponse(check domain.ValidationCheckResult) ValidateCheckResultResponse {
	return ValidateCheckResultResponse{
		CheckID:       check.CheckID,
		CheckName:     check.CheckName,
		Required:      check.Required,
		Sequence:      check.Sequence,
		Status:        string(check.Status),
		ExitCode:      check.ExitCode,
		TimedOut:      check.TimedOut,
		Cancelled:     check.Cancelled,
		Certain:       check.Certain,
		RequiredPass:  check.Status.Passed(),
		DurationMS:    check.Duration.Milliseconds(),
		StdoutPath:    check.StdoutPath,
		StderrPath:    check.StderrPath,
		StdoutSummary: check.StdoutSummary,
		StderrSummary: check.StderrSummary,
		OutputTrunc:   check.OutputTruncated,
		ArchiveError:  check.OutputArchiveError,
		FailureReason: check.FailureReason,
		ErrorDetail:   check.ErrorDetail,
		StartedAt:     check.StartedAt,
		FinishedAt:    check.FinishedAt,
	}
}

// writeServiceError 把应用层错误映射为统一错误响应。
func (h *validationHandler) writeServiceError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrValidationInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrValidationNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrValidationConflict),
		errors.Is(err, domain.ErrValidationBlocked),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrValidationPrerequisite),
		errors.Is(err, domain.ErrValidationPrerequisite):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "验证接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// parseValidatePath 判断路径是否指向验证动作。
//
// 只有 {taskID}/steps/{stepID}/attempts/{attemptID}/validate 这一种形状成立；
// 其余形状返回 false，由执行记录查询或任务处理器给出各自的 404 / 405 语义。
func parseValidatePath(path string) (taskID, stepID, attemptID string, ok bool) {
	remainder := strings.TrimPrefix(path, taskPathPrefix)
	if remainder == path {
		return "", "", "", false
	}
	segments := splitPathSegments(remainder)
	if len(segments) != 6 {
		return "", "", "", false
	}
	if segments[1] != stepsSegment ||
		segments[3] != attemptsSegment ||
		segments[5] != validateSegment {
		return "", "", "", false
	}
	if !validTaskPathID(segments[0]) ||
		!validTaskPathID(segments[2]) ||
		!validTaskPathID(segments[4]) {
		return "", "", "", false
	}
	return segments[0], segments[2], segments[4], true
}
