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

// 代码初始化接口路径。
//
// 路径挂在任务、步骤与尝试之下：代码初始化永远属于某条完整任务链，
// 独立顶层路径会让调用方以为可以跨任务初始化任意工作区。
const (
	// initializeCodeSegment 是代码初始化动作的路径段。
	initializeCodeSegment = "initialize-code"
	// maxInitializeCodeTimeoutSeconds 是单次初始化允许请求的最大超时秒数。
	maxInitializeCodeTimeoutSeconds = 3600
)

// InitializeCodeRequest 是代码初始化的请求体。
//
// 只接受执行参数：任务、步骤与尝试标识来自路径，容器、仓库地址、凭据与命令行
// 参数都由服务端按数据库记录生成，调用方无法指定。
type InitializeCodeRequest struct {
	// WorkDir 是容器内目标工作目录，空值使用任务配置的目录。
	WorkDir string `json:"work_dir"`
	// TimeoutSeconds 是本次初始化的超时秒数，0 表示使用服务端默认值。
	TimeoutSeconds int `json:"timeout_seconds"`
}

// InitializeCodeResponse 是一次代码初始化的结果。
//
// 结构显式区分"代码是否就绪"与"记录是否完整写入"：
//   - Status 为 skipped 表示该尝试的工作区此前已就绪，本次没有重新 Clone；
//   - Persisted 为 false 时 PersistenceErrors 说明哪些写入失败。
type InitializeCodeResponse struct {
	RecordID  string `json:"record_id"`
	TaskID    string `json:"task_id"`
	StepID    string `json:"step_id"`
	AttemptID string `json:"attempt_id"`
	// SourceType 是代码获取方式，第一版固定为 git。
	SourceType string `json:"source_type"`
	// Repository 是脱敏后的仓库地址。
	Repository string `json:"repository,omitempty"`
	// Status 是初始化状态。
	Status string `json:"status"`
	// Succeeded 表示代码是否已经就绪。
	Succeeded bool `json:"succeeded"`
	// ContainerID 是容器登记记录 ID。
	ContainerID string `json:"container_id"`
	// WorkDir 是容器内项目目录。
	WorkDir string `json:"work_dir"`
	// BaseBranch 是识别出的基础分支。
	BaseBranch string `json:"base_branch,omitempty"`
	// TaskBranch 是任务分支名。
	TaskBranch string `json:"task_branch"`
	// Commit 是实际检出的 commit SHA。
	Commit string `json:"commit,omitempty"`
	// Stage 是最后一个阶段，失败时即为失败阶段。
	Stage string `json:"stage,omitempty"`
	// ErrorCode 是可诊断的错误类别。
	ErrorCode string `json:"error_code,omitempty"`
	// Error 是脱敏后的失败原因。
	Error string `json:"error,omitempty"`
	// Persisted 表示记录是否全部可靠写入数据库。
	Persisted bool `json:"persisted"`
	// PersistenceErrors 是写入失败的原因，成功时为空。
	PersistenceErrors []string  `json:"persistence_errors,omitempty"`
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at"`
}

// codeInitializeHandler 处理代码初始化请求。
type codeInitializeHandler struct {
	service *app.CodeInitializeService
	logger  *slog.Logger
}

// newCodeInitializeHandler 构造代码初始化处理器。
//
// 服务为空时仍然返回处理器：请求会得到 503，而不是 404 或前端页面。
func newCodeInitializeHandler(
	service *app.CodeInitializeService,
	logger *slog.Logger,
) *codeInitializeHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &codeInitializeHandler{service: service, logger: logger}
}

// serve 处理 POST /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code。
//
// 该入口是同步的：只有容器内 Git 操作结束、初始化记录进入终态后才写出响应。
// 执行本身已经由服务层持久化，因此即使响应写入失败，记录与事件也不会丢失。
func (h *codeInitializeHandler) serve(
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
			"代码初始化服务未初始化",
		)
		return
	}
	var body InitializeCodeRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toAppRequest(taskID, stepID, attemptID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	result, err := h.service.Initialize(r.Context(), request)
	if err != nil {
		switch {
		case errors.Is(err, app.ErrCodeInitFailed):
			// 执行已经发生但没有成功：返回明确的执行失败，而不是内部错误。
			writeError(
				w,
				r,
				h.logger,
				http.StatusInternalServerError,
				CodeExecutionFailed,
				initializeFailureMessage(result),
			)
		default:
			h.writeServiceError(w, r, err)
		}
		return
	}
	response := initializeCodeResponse(result)
	switch {
	case !result.Persisted:
		// 执行事实与持久化完整性必须分开报告：写入失败时不能返回成功。
		message := "代码初始化已结束，但结果未能完整写入"
		if len(result.PersistenceErrors) > 0 {
			message += ": " + strings.Join(result.PersistenceErrors, "；")
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
	case !result.Succeeded:
		writeError(
			w,
			r,
			h.logger,
			http.StatusInternalServerError,
			CodeExecutionFailed,
			initializeFailureMessage(result),
		)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, response)
}

// toAppRequest 把请求体转换为应用层请求。
func (b InitializeCodeRequest) toAppRequest(
	taskID, stepID, attemptID string,
) (app.CodeInitRequest, error) {
	if b.TimeoutSeconds < 0 || b.TimeoutSeconds > maxInitializeCodeTimeoutSeconds {
		return app.CodeInitRequest{}, errors.Join(
			app.ErrCodeInitInvalidInput,
			errors.New("超时秒数必须在 0 到 3600 之间"),
		)
	}
	timeout := time.Duration(b.TimeoutSeconds) * time.Second
	return app.CodeInitRequest{
		TaskID:    taskID,
		StepID:    stepID,
		AttemptID: attemptID,
		WorkDir:   b.WorkDir,
		Timeout:   timeout,
	}, nil
}

// initializeCodeResponse 把初始化结果转换为对外表示。
func initializeCodeResponse(result app.CodeInitResult) InitializeCodeResponse {
	return InitializeCodeResponse{
		RecordID:          result.RecordID.String(),
		TaskID:            result.TaskID.String(),
		StepID:            result.StepID.String(),
		AttemptID:         result.AttemptID.String(),
		SourceType:        result.Record.SourceType.String(),
		Repository:        result.Record.Repository,
		Status:            result.Status.String(),
		Succeeded:         result.Succeeded,
		ContainerID:       result.ContainerID,
		WorkDir:           result.WorkDir,
		BaseBranch:        result.BaseBranch,
		TaskBranch:        result.TaskBranch,
		Commit:            result.Commit,
		Stage:             result.Stage.String(),
		ErrorCode:         result.ErrorCode,
		Error:             result.Failure,
		Persisted:         result.Persisted,
		PersistenceErrors: result.PersistenceErrors,
		StartedAt:         result.StartedAt,
		FinishedAt:        result.FinishedAt,
	}
}

// initializeFailureMessage 生成失败响应中的说明。
func initializeFailureMessage(result app.CodeInitResult) string {
	detail := strings.TrimSpace(result.Failure)
	if detail == "" {
		detail = "代码初始化未成功"
	}
	stage := strings.TrimSpace(result.Stage.String())
	if stage == "" {
		return "代码初始化未成功（" + result.Status.String() + "）: " + detail
	}
	return "代码初始化未成功（" + result.Status.String() + "，阶段 " + stage + "）: " + detail
}

// writeServiceError 把应用层错误映射为统一错误响应。
func (h *codeInitializeHandler) writeServiceError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrCodeInitInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrCodeInitNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrCodeInitConflict),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrCodeInitPrerequisite):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	case errors.Is(err, app.ErrCodeInitNotPersisted):
		status = http.StatusInternalServerError
		code = CodeExecutionNotPersisted
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError && code == CodeInternal {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "代码初始化接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// parseInitializeCodePath 判断路径是否指向代码初始化动作。
//
// 只有 {taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code 这一种形状成立；
// 其余形状返回 false，由执行记录查询或任务处理器给出各自的 404 / 405 语义。
func parseInitializeCodePath(path string) (taskID, stepID, attemptID string, ok bool) {
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
		segments[5] != initializeCodeSegment {
		return "", "", "", false
	}
	if !validTaskPathID(segments[0]) ||
		!validTaskPathID(segments[2]) ||
		!validTaskPathID(segments[4]) {
		return "", "", "", false
	}
	return segments[0], segments[2], segments[4], true
}
