package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 代码保存核验与续跑资格的路径段。
const (
	// codeSaveVerifySegment 是任务下代码保存核验的动作路径段。
	codeSaveVerifySegment = "verify-code-save"
	// resumeEligibilitySegment 是任务续跑资格的查询路径段。
	resumeEligibilitySegment = "resume-eligibility"
)

// CodeSaveVerifyResponse 是一次只读核验的响应。
//
// 五个事实必须分别输出，合并成一句话会让"核验成功"掩盖"结论没写进数据库"：
// 判定是否可靠（observation）、结论是什么（decision）、是否落盘（persisted）、
// 记录是否已推进（record）、是否需要人工介入。
type CodeSaveVerifyResponse struct {
	Record      CodeSaveRecordResponse      `json:"record"`
	Observation CodeSaveObservationResponse `json:"observation"`
	Decision    CodeSaveDecisionResponse    `json:"decision"`
	Updated     CodeSaveRecordResponse      `json:"updated"`
	Persisted   bool                        `json:"persisted"`
}

// CodeSaveRecordResponse 是代码保存记录的对外表示。
type CodeSaveRecordResponse struct {
	RecordID       string `json:"record_id"`
	TaskID         string `json:"task_id"`
	TaskBranch     string `json:"task_branch"`
	CommitStatus   string `json:"commit_status"`
	CommitSHA      string `json:"commit_sha,omitempty"`
	PushStatus     string `json:"push_status"`
	RemoteCommit   string `json:"remote_commit_sha,omitempty"`
	MergeStatus    string `json:"merge_status"`
	MergeSHA       string `json:"merge_sha,omitempty"`
	PersistStatus  string `json:"persist_status"`
	RecoveryStatus string `json:"recovery_status"`
	ErrorSummary   string `json:"error_summary,omitempty"`
}

// CodeSaveObservationResponse 是只读核验得到的现场事实。
type CodeSaveObservationResponse struct {
	ReadFailed       bool   `json:"read_failed"`
	ReadError        string `json:"read_error,omitempty"`
	HeadCommit       string `json:"head_commit,omitempty"`
	RemoteTaskCommit string `json:"remote_task_commit,omitempty"`
	RemoteBaseCommit string `json:"remote_base_commit,omitempty"`
}

// CodeSaveDecisionResponse 是恢复判定的结论。
type CodeSaveDecisionResponse struct {
	Status      string `json:"status"`
	Stage       string `json:"stage,omitempty"`
	Summary     string `json:"summary"`
	SafeToRetry bool   `json:"safe_to_retry"`
}

// ResumeEligibilityResponse 是任务续跑资格的响应。
type ResumeEligibilityResponse struct {
	TaskID          string                      `json:"task_id"`
	TaskStatus      string                      `json:"task_status"`
	Run             *WorkflowRunResponse        `json:"run,omitempty"`
	CanResume       bool                        `json:"can_resume"`
	Blocking        []string                    `json:"blocking"`
	CodeSaveAction  string                      `json:"code_save_action"`
	CodeSaveSummary string                      `json:"code_save_summary"`
	CodeSaveRecords []ResumeEligibilityCodeSave `json:"code_save_records"`
	NextActions     []string                    `json:"next_actions"`
	Summary         string                      `json:"summary"`
}

// ResumeEligibilityCodeSave 是一条需要关注的代码保存记录。
type ResumeEligibilityCodeSave struct {
	RecordID       string `json:"record_id"`
	TaskBranch     string `json:"task_branch"`
	CommitStatus   string `json:"commit_status"`
	PushStatus     string `json:"push_status"`
	MergeStatus    string `json:"merge_status"`
	PersistStatus  string `json:"persist_status"`
	RecoveryStatus string `json:"recovery_status"`
	Action         string `json:"action"`
	ErrorClass     string `json:"error_class,omitempty"`
	Summary        string `json:"summary"`
}

// codeRecoveryHandler 处理代码保存核验与续跑资格查询。
type codeRecoveryHandler struct {
	containers  *app.ContainerService
	eligibility *app.ResumeEligibilityService
	logger      *slog.Logger
}

// newCodeRecoveryHandler 构造处理器。
//
// 两个能力各自独立装配：核验与续跑资格分别返回"核验服务未初始化"与"续跑资格
// 服务未初始化"，只有两者都缺失时才由路由前置检查统一返回 503。
func newCodeRecoveryHandler(
	containers *app.ContainerService,
	eligibility *app.ResumeEligibilityService,
	logger *slog.Logger,
) *codeRecoveryHandler {
	return &codeRecoveryHandler{
		containers:  containers,
		eligibility: eligibility,
		logger:      logger,
	}
}

// verify 处理 POST /api/v1/tasks/{taskID}/code-saves/{recordID}/verify-code-save。
//
// 该入口与 CLI 的 `agentflow code verify` 语义完全一致：只读核验、幂等、不改状态机。
// 它不提交、不推送、不合并，也不删除或接管容器。
func (h *codeRecoveryHandler) verify(w http.ResponseWriter, r *http.Request) {
	if h.containers == nil {
		writeError(w, r, h.logger, http.StatusServiceUnavailable, CodeInternal,
			"代码保存核验服务未初始化")
		return
	}
	recordID := pathID(r, paramRecordID)
	var body CodeSaveVerifyRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	result, err := h.containers.VerifyCodeSave(r.Context(), app.CodeSaveVerifyRequest{
		RecordID:    recordID,
		ContainerID: body.ContainerID,
		WorkDir:     body.WorkDir,
	})
	if err != nil {
		writeError(
			w,
			r,
			h.logger,
			http.StatusPreconditionFailed,
			CodePreconditionFailed,
			err.Error(),
		)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, codeSaveVerifyResponse(result))
}

// CodeSaveVerifyRequest 是核验请求体。
//
// 调用方只能覆盖容器与工作区路径：记录本身从数据库读取，不能由请求指定"已核验事实"。
type CodeSaveVerifyRequest struct {
	ContainerID string `json:"container_id"`
	WorkDir     string `json:"work_dir"`
}

// serveEligibility 处理 GET /api/v1/tasks/{taskID}/resume-eligibility。
func (h *codeRecoveryHandler) serveEligibility(w http.ResponseWriter, r *http.Request) {
	if h.eligibility == nil {
		writeError(w, r, h.logger, http.StatusServiceUnavailable, CodeInternal,
			"续跑资格服务未初始化")
		return
	}
	taskID := pathID(r, paramTaskID)
	result, err := h.eligibility.Get(r.Context(), taskID)
	if err != nil {
		status := http.StatusInternalServerError
		code := CodeInternal
		switch {
		case errors.Is(err, app.ErrWorkflowInvalidInput), errors.Is(err, domain.ErrInvalidValue):
			status = http.StatusBadRequest
			code = CodeInvalidRequest
		case errors.Is(err, app.ErrWorkflowNotFound), errors.Is(err, storage.ErrNotFound):
			status = http.StatusNotFound
			code = CodeNotFound
		}
		writeError(w, r, h.logger, status, code, err.Error())
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, resumeEligibilityResponse(result))
}

// codeSaveVerifyResponse 把核验结果转换为对外表示。
func codeSaveVerifyResponse(result app.CodeSaveVerifyResult) CodeSaveVerifyResponse {
	return CodeSaveVerifyResponse{
		Record:      codeSaveRecordResponse(result.Record),
		Observation: codeSaveObservationResponse(result.Observation),
		Decision: CodeSaveDecisionResponse{
			Status:      result.Decision.Status.String(),
			Stage:       result.Decision.Stage,
			Summary:     result.Decision.Summary,
			SafeToRetry: result.Decision.SafeToRetry,
		},
		Updated:   codeSaveRecordResponse(result.Updated),
		Persisted: result.Persisted,
	}
}

// codeSaveRecordResponse 转换代码保存记录表示。
func codeSaveRecordResponse(record domain.CodeSaveRecord) CodeSaveRecordResponse {
	return CodeSaveRecordResponse{
		RecordID:       record.ID.String(),
		TaskID:         record.TaskID,
		TaskBranch:     record.TaskBranch,
		CommitStatus:   record.CommitStatus.String(),
		CommitSHA:      record.CommitSHA,
		PushStatus:     record.PushStatus.String(),
		RemoteCommit:   record.RemoteCommitSHA,
		MergeStatus:    record.MergeStatus.String(),
		MergeSHA:       record.MergeSHA,
		PersistStatus:  record.PersistStatus.Normalized().String(),
		RecoveryStatus: record.RecoveryStatus.String(),
		ErrorSummary:   record.ErrorSummary,
	}
}

// codeSaveObservationResponse 转换只读核验的现场事实。
func codeSaveObservationResponse(
	observation domain.CodeSaveObservation,
) CodeSaveObservationResponse {
	return CodeSaveObservationResponse{
		ReadFailed:       observation.ReadFailed,
		ReadError:        observation.ReadError,
		HeadCommit:       observation.HeadCommit,
		RemoteTaskCommit: observation.RemoteTaskCommit,
		RemoteBaseCommit: observation.RemoteBaseCommit,
	}
}

// resumeEligibilityResponse 转换续跑资格表示。
func resumeEligibilityResponse(result app.ResumeEligibility) ResumeEligibilityResponse {
	response := ResumeEligibilityResponse{
		TaskID:          result.TaskID.String(),
		TaskStatus:      string(result.TaskStatus),
		CanResume:       result.CanResume,
		Blocking:        append([]string(nil), result.Blocking...),
		CodeSaveAction:  string(result.CodeSave.Action),
		CodeSaveSummary: result.CodeSave.Summary,
		CodeSaveRecords: make([]ResumeEligibilityCodeSave, 0, len(result.CodeSaveRecords)),
		NextActions:     append([]string(nil), result.NextActions...),
		Summary:         result.Summary,
	}
	if result.Run != nil {
		run := workflowRunResponse(*result.Run)
		response.Run = &run
	}
	for _, record := range result.CodeSaveRecords {
		response.CodeSaveRecords = append(response.CodeSaveRecords, ResumeEligibilityCodeSave{
			RecordID:       record.ID.String(),
			TaskBranch:     record.TaskBranch,
			CommitStatus:   record.CommitStatus.String(),
			PushStatus:     record.PushStatus.String(),
			MergeStatus:    record.MergeStatus.String(),
			PersistStatus:  record.PersistStatus.Normalized().String(),
			RecoveryStatus: record.RecoveryStatus.String(),
			Action:         string(record.Action),
			ErrorClass:     record.ErrorClass,
			Summary:        record.Summary,
		})
	}
	return response
}
