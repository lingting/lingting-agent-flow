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

// codeSavesSegment 是任务下代码保存记录的集合路径段。
//
// 它与既有的 `{taskID}/code-saves/{recordID}/verify-code-save` 共享前缀：
// 集合路径是 2 段，核验动作是 4 段，两者形状不同，路由按段数区分，不会互相抢占。
const codeSavesSegment = "code-saves"

// CodeSaveRecordListEntryResponse 是一条代码保存记录的对外表示。
//
// 数据来源是持久化的 `domain.CodeSaveRecord`，本结构只做字段映射：
//
//   - 提交、推送、合并是三个独立的 Git 事实，分别输出，不压缩成单一成功/失败；
//   - `persist_status` 描述"这条记录本身是否可信"，与 Git 做了什么无关；
//   - `recovery_status` 描述只读核验得到的结论，与上面两者都不同。
//
// 因此 `unknown`、`pending`、`unconfirmed`、`unverifiable` 一律按真实值输出，
// 不自动归并为失败或成功。
type CodeSaveRecordListEntryResponse struct {
	// ID 是记录标识：调用方据此发起只读核验（`…/verify-code-save`）。
	ID string `json:"id"`
	// TaskID 是记录所属任务。
	TaskID string `json:"task_id"`
	// ProjectID 是可选的项目标识。
	ProjectID string `json:"project_id,omitempty"`
	// ContainerID 是容器登记记录标识。
	ContainerID string `json:"container_id"`
	// DockerID 是执行时使用的 Docker 容器 ID。
	DockerID string `json:"docker_id,omitempty"`
	// AttemptID 是发起本次保存的执行尝试，用于从尝试反查保存结果。
	AttemptID string `json:"attempt_id,omitempty"`
	// RemoteName 是使用的 Git 远程名称。
	RemoteName string `json:"remote_name"`
	// Repository 是脱敏后的仓库地址。
	Repository string `json:"repository,omitempty"`
	// TaskBranch 是任务分支名，固定为 agent/<task-id>。
	TaskBranch string `json:"task_branch"`
	// BaseBranch 是识别出的基础分支。
	BaseBranch string `json:"base_branch,omitempty"`
	// CommitStatus 是提交结果。
	CommitStatus string `json:"commit_status"`
	// CommitSHA 是提交产生的 commit。
	CommitSHA string `json:"commit_sha,omitempty"`
	// CommitMessage 是提交信息摘要（单行，有长度上限）。
	CommitMessage string `json:"commit_message,omitempty"`
	// PushStatus 是任务分支的推送结果。
	PushStatus string `json:"push_status"`
	// RemoteCommitSHA 是核验到的远程任务分支 commit。
	RemoteCommitSHA string `json:"remote_commit_sha,omitempty"`
	// MergeEnabled 表示是否请求在推送成功后合并基础分支。
	MergeEnabled bool `json:"merge_enabled"`
	// MergeStatus 是合并结果。
	MergeStatus string `json:"merge_status"`
	// MergeSHA 是合并后基础分支的 commit。
	MergeSHA string `json:"merge_sha,omitempty"`
	// Stage 是最后一个进入的操作阶段，用于中断后定位。
	Stage string `json:"stage,omitempty"`
	// ErrorCode 是可诊断的错误分类代码。
	ErrorCode string `json:"error_code,omitempty"`
	// ErrorSummary 是持久化时已脱敏的失败摘要；本接口不拼接新的错误信息。
	ErrorSummary string `json:"error_summary,omitempty"`
	// CreatedAt 是记录创建时间。
	CreatedAt time.Time `json:"created_at"`
	// StartedAt 是开始执行远程操作的时间。
	StartedAt *time.Time `json:"started_at,omitempty"`
	// FinishedAt 是操作结束时间。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// UpdatedAt 是记录最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
	// BaselineCommitSHA 是提交前 HEAD 指向的 commit，缺失表示无法判定。
	BaselineCommitSHA string `json:"baseline_commit_sha,omitempty"`
	// BaselineBaseCommitSHA 是合并前远程基础分支的 commit，缺失表示无法判定。
	BaselineBaseCommitSHA string `json:"baseline_base_commit_sha,omitempty"`
	// PersistStatus 是记录写入状态，已按领域规则归一化：空值等价于 persisted。
	PersistStatus string `json:"persist_status"`
	// PersistError 是最后一次写入失败的脱敏摘要。
	PersistError string `json:"persist_error,omitempty"`
	// RecoveryStatus 是只读核验的恢复结论，已按领域规则归一化：空值等价于 unverified。
	RecoveryStatus string `json:"recovery_status"`
}

// CodeSaveRecordListResponse 是任务下代码保存记录的列表响应。
//
// 与任务列表保持一致使用 `records` + `total`：代码保存记录按任务归属、数量有限，
// 因此不引入分页，`total` 即本次返回的记录数。
type CodeSaveRecordListResponse struct {
	Records []CodeSaveRecordListEntryResponse `json:"records"`
	Total   int                               `json:"total"`
}

// codeSaveListHandler 处理任务下代码保存记录的只读列表查询。
type codeSaveListHandler struct {
	queries *app.CodeSaveQueryService
	logger  *slog.Logger
}

// newCodeSaveListHandler 构造处理器；服务为空时仍返回处理器，接口随后返回 503。
func newCodeSaveListHandler(
	queries *app.CodeSaveQueryService,
	logger *slog.Logger,
) *codeSaveListHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &codeSaveListHandler{queries: queries, logger: logger}
}

// serveCodeSaveList 分发任务下代码保存记录请求。
func (rt *router) serveCodeSaveList(w http.ResponseWriter, r *http.Request) {
	handler := newCodeSaveListHandler(rt.codeSaves, rt.logger)
	taskID, ok := actionTaskID(r.URL.Path, codeSavesSegment)
	if !ok {
		writeError(w, r, rt.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	if handler.queries == nil {
		writeError(
			w,
			r,
			rt.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"代码保存记录查询服务未初始化",
		)
		return
	}
	handler.list(w, r, taskID)
}

// list 处理 GET /api/v1/tasks/{taskID}/code-saves。
//
// 只读：不建立 SSH / Docker 连接、不核验或处置容器、不执行 Git 命令、不修改代码
// 保存记录、不推进任务状态，也不触发核验或恢复动作。
func (h *codeSaveListHandler) list(w http.ResponseWriter, r *http.Request, taskID string) {
	if !h.allowGet(w, r) {
		return
	}
	records, err := h.queries.ListByTask(r.Context(), taskID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, codeSaveRecordListResponse(records))
}

// allowGet 校验请求方法，不满足时写出 405 并返回 false。
func (h *codeSaveListHandler) allowGet(w http.ResponseWriter, r *http.Request) bool {
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
// 映射依据错误类别而不是文本：任务不存在或已逻辑删除为 404，其余为 500。
// 代码保存列表没有可用的客户端参数（路径段形状由路由判定），因此不会产生 400。
func (h *codeSaveListHandler) writeServiceError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrTaskNotFound), errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrTaskInvalidInput), errors.Is(err, domain.ErrInvalidValue):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "代码保存记录接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// codeSaveRecordListResponse 把领域记录转换为列表响应。
//
// 空列表输出 `[]` 而不是 `null`：调用方不需要区分"字段缺失"与"没有记录"。
func codeSaveRecordListResponse(
	records []domain.CodeSaveRecord,
) CodeSaveRecordListResponse {
	response := CodeSaveRecordListResponse{
		Records: make([]CodeSaveRecordListEntryResponse, 0, len(records)),
		Total:   len(records),
	}
	for _, record := range records {
		response.Records = append(response.Records, codeSaveRecordListEntry(record))
	}
	return response
}

// codeSaveRecordListEntry 转换单条代码保存记录。
//
// `persist_status` 与 `recovery_status` 都经过领域层的归一化：历史记录写入时
// 还没有这两个字段，空值必须分别按 persisted 与 unverified 处理，不能被输出成
// 空字符串让调用方误判为"写入状态未知"或"核验结论是第三种状态"。
func codeSaveRecordListEntry(record domain.CodeSaveRecord) CodeSaveRecordListEntryResponse {
	return CodeSaveRecordListEntryResponse{
		ID:                    record.ID.String(),
		TaskID:                record.TaskID,
		ProjectID:             record.ProjectID,
		ContainerID:           record.ContainerID,
		DockerID:              record.DockerID,
		AttemptID:             record.AttemptID,
		RemoteName:            record.RemoteName,
		Repository:            record.Repository,
		TaskBranch:            record.TaskBranch,
		BaseBranch:            record.BaseBranch,
		CommitStatus:          record.CommitStatus.String(),
		CommitSHA:             record.CommitSHA,
		CommitMessage:         record.CommitMessage,
		PushStatus:            record.PushStatus.String(),
		RemoteCommitSHA:       record.RemoteCommitSHA,
		MergeEnabled:          record.MergeEnabled,
		MergeStatus:           record.MergeStatus.String(),
		MergeSHA:              record.MergeSHA,
		Stage:                 record.Stage,
		ErrorCode:             record.ErrorCode,
		ErrorSummary:          record.ErrorSummary,
		CreatedAt:             record.CreatedAt,
		StartedAt:             record.StartedAt,
		FinishedAt:            record.FinishedAt,
		UpdatedAt:             record.UpdatedAt,
		BaselineCommitSHA:     record.BaselineCommitSHA,
		BaselineBaseCommitSHA: record.BaselineBaseCommitSHA,
		PersistStatus:         record.PersistStatus.Normalized().String(),
		PersistError:          record.PersistError,
		RecoveryStatus:        record.RecoveryStatus.Normalized().String(),
	}
}

// isCodeSaveListPath 判断路径是否指向任务下的代码保存记录列表。
//
// 判定按路径段形状进行：只有 `{taskID}/code-saves` 两段属于本接口，
// `{taskID}/code-saves/{recordID}/verify-code-save` 仍由代码保存恢复处理器处理。
func isCodeSaveListPath(path string) bool {
	return matchActionPath(path, codeSavesSegment)
}
