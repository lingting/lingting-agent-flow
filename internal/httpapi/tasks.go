package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 任务管理接口路径。
const (
	// PathTasks 是任务集合路径。
	PathTasks = APIPrefix + "/tasks"
	// taskPathPrefix 是单任务路径前缀，实际路径为 {taskPathPrefix}{taskID}。
	taskPathPrefix = PathTasks + "/"
	// cancelSuffix 是取消动作的子路径。
	cancelSuffix = "/cancel"
)

// maxTaskRequestBytes 是任务请求体的最大字节数。
//
// 任务描述有长度上限，请求体限制防止超大请求占用内存。
const maxTaskRequestBytes = 64 << 10

// TaskResponse 是任务的对外表示。
//
// 只暴露管理 API 需要的字段：执行期内部字段（租约、幂等键、容器关联等）不对外
// 输出，避免把尚未稳定的执行模型固化进管理接口。
type TaskResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	// StatusTerminal 表示任务是否处于终态，前端据此区分"已结束执行"与"可重试"。
	StatusTerminal bool `json:"status_terminal"`
	// StatusFinished 表示任务是否已停止执行，前端据此决定取消与删除是否可用。
	//
	// 它与 StatusTerminal 的差别在 failed：失败任务可以显式重试，因此不是终态，
	// 但它的执行已经停止，管理接口允许删除、拒绝再次取消。
	StatusFinished bool `json:"status_finished"`
	// FinalResult 是任务的最终结论。
	//
	// 它与 Status 不同：状态表达"处在哪个业务阶段"，结论表达"这次执行的结果是什么"。
	// 两者都可能为空，缺失时不得由状态推断结论。
	FinalResult string `json:"final_result,omitempty"`
	// Closeout 是派生的收尾闭环结论：confirmed / pending / unconfirmed /
	// inconsistent / cancelled。
	//
	// 它解决的是一个真实问题：任务状态可能是 completed（成功终态的写入先于执行记录
	// 的收尾写入），而执行记录仍是 recovery_required。此时调用方不能把该任务当作
	// 确定成功，必须依据这个字段判断。
	Closeout string `json:"closeout"`
	// CloseoutSummary 是收尾闭环结论的可读说明。
	CloseoutSummary string `json:"closeout_summary,omitempty"`
	// ProjectID 是关联项目标识，未关联时为空字符串。
	ProjectID         string     `json:"project_id"`
	CancelRequested   bool       `json:"cancel_requested"`
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
	// Error 是失败原因摘要，只在存在时输出。
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// TaskListResponse 是任务列表的分页响应。
type TaskListResponse struct {
	// Records 是当前页的任务。
	Records []TaskResponse `json:"records"`
	// Total 是满足筛选条件的总记录数。
	Total int `json:"total"`
	// Current 是当前页号。
	Current int `json:"current"`
	// Size 是页大小。
	Size int `json:"size"`
}

// CreateTaskRequest 是创建任务的请求体。
//
// 名称与描述必填；其余字段沿用执行型配置，与 CLI 的建任务参数一致。
// 客户端不能指定状态、创建时间、开始时间或完成时间。
type CreateTaskRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ProjectID   string `json:"project_id"`
	Priority    string `json:"priority"`
	HostID      string `json:"host_id"`
	// RepositoryURL 是仓库地址。
	RepositoryURL string `json:"repository_url"`
	// BaseBranch 是基础分支。
	BaseBranch         string   `json:"base_branch"`
	Environment        string   `json:"environment"`
	EnvironmentVersion string   `json:"environment_version"`
	Caches             []string `json:"caches"`
	WorkDir            string   `json:"work_dir"`
	// Commands 是必需验证命令，形式为「名称=可执行文件 参数...」。
	Commands []string `json:"commands"`
	// AcceptanceCriteria 是任务级验收条件。
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	// MaxAttempts 是尝试次数上限。
	MaxAttempts       int  `json:"max_attempts"`
	RetryTimeout      bool `json:"retry_timeout"`
	RetryVerification bool `json:"retry_verification"`
	RetryNetwork      bool `json:"retry_network"`
	RetryWaitSeconds  int  `json:"retry_wait_seconds"`
	// MergeAfterPush 表示推送成功后合并基础分支，默认关闭。
	MergeAfterPush             bool   `json:"merge_after_push"`
	PlannerRoleID              string `json:"planner_role_id"`
	DeveloperRoleID            string `json:"developer_role_id"`
	ValidatorRoleID            string `json:"validator_role_id"`
	MaxPlanSteps               int    `json:"max_plan_steps"`
	AgentTimeoutSeconds        int    `json:"agent_timeout_seconds"`
	VerificationTimeoutSeconds int    `json:"verification_timeout_seconds"`
	// IdempotencyKey 是幂等键，重复提交不会创建第二个任务。
	IdempotencyKey string `json:"idempotency_key"`
}

// taskHandler 处理任务管理接口请求。
type taskHandler struct {
	tasks  *app.TaskService
	logger *slog.Logger
}

// create 处理 POST /api/v1/tasks。
func (h *taskHandler) create(w http.ResponseWriter, r *http.Request) {
	if !h.allowMethod(w, r, http.MethodPost) {
		return
	}
	var body CreateTaskRequest
	if !decodeJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toAppRequest()
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	task, err := h.tasks.CreateTaskFromAPI(r.Context(), request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	name, description, err := h.taskNameAndDescription(r.Context(), task)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	closeouts, err := h.tasks.TaskCloseoutViews(r.Context(), []domain.Task{task})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(
		w,
		r,
		h.logger,
		http.StatusCreated,
		taskResponseWithCloseout(task, name, description, closeouts[task.ID]),
	)
}

// list 处理 GET /api/v1/tasks。
func (h *taskHandler) list(w http.ResponseWriter, r *http.Request) {
	if !h.allowMethod(w, r, http.MethodGet) {
		return
	}
	request, err := parseTaskPageQuery(r)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	page, err := h.tasks.ListTasksForAPI(r.Context(), request)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	responses, err := h.taskResponses(r.Context(), page.Tasks)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, TaskListResponse{
		Records: responses,
		Total:   page.Total,
		Current: request.Current,
		Size:    request.Size,
	})
}

// detail 处理 GET /api/v1/tasks/{taskID}。
func (h *taskHandler) detail(w http.ResponseWriter, r *http.Request, taskID string) {
	if !h.allowMethod(w, r, http.MethodGet) {
		return
	}
	task, err := h.tasks.GetTaskForAPI(r.Context(), taskID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	response, err := h.taskResponseWithDerivedCloseout(r.Context(), task)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, response)
}

// taskResponseWithDerivedCloseout 读取执行记录后转换单个任务表示。
func (h *taskHandler) taskResponseWithDerivedCloseout(
	ctx context.Context,
	task domain.Task,
) (TaskResponse, error) {
	name, description, err := h.taskNameAndDescription(ctx, task)
	if err != nil {
		return TaskResponse{}, err
	}
	closeouts, err := h.tasks.TaskCloseoutViews(ctx, []domain.Task{task})
	if err != nil {
		return TaskResponse{}, err
	}
	return taskResponseWithCloseout(task, name, description, closeouts[task.ID]), nil
}

// cancel 处理 POST /api/v1/tasks/{taskID}/cancel。
func (h *taskHandler) cancel(w http.ResponseWriter, r *http.Request, taskID string) {
	if !h.allowMethod(w, r, http.MethodPost) {
		return
	}
	task, err := h.tasks.CancelTaskForAPI(r.Context(), taskID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	response, err := h.taskResponseWithDerivedCloseout(r.Context(), task)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusOK, response)
}

// remove 处理 DELETE /api/v1/tasks/{taskID}。
//
// 删除是逻辑删除，只标记 deleted_at；成功时返回 204，不返回响应体。
func (h *taskHandler) remove(w http.ResponseWriter, r *http.Request, taskID string) {
	if !h.allowMethod(w, r, http.MethodDelete) {
		return
	}
	if err := h.tasks.DeleteTask(r.Context(), taskID); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serve 按路径与方法分发单任务请求。
func (h *taskHandler) serve(w http.ResponseWriter, r *http.Request) {
	remainder := strings.TrimPrefix(r.URL.Path, taskPathPrefix)
	if remainder == r.URL.Path || strings.TrimSpace(remainder) == "" {
		// 空标识或路径尾部多余斜杠：按接口不存在处理，避免与集合路径混淆。
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	if strings.HasSuffix(remainder, cancelSuffix) {
		taskID := strings.TrimSuffix(remainder, cancelSuffix)
		if !validTaskPathID(taskID) {
			writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
			return
		}
		h.cancel(w, r, taskID)
		return
	}
	if !validTaskPathID(remainder) {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.detail(w, r, remainder)
	case http.MethodDelete:
		h.remove(w, r, remainder)
	default:
		w.Header().Set("Allow", "GET, HEAD, DELETE")
		writeError(
			w,
			r,
			h.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			"该接口只支持 GET 与 DELETE 请求",
		)
	}
}

// validTaskPathID 校验路径中的任务标识不含多余路径段。
func validTaskPathID(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed != "" && !strings.Contains(trimmed, "/")
}

// allowMethod 校验请求方法，不满足时写出 405 并返回 false。
func (h *taskHandler) allowMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
		if method == http.MethodGet && r.Method == http.MethodHead {
			return true
		}
	}
	allowed := strings.Join(methods, ", ")
	w.Header().Set("Allow", allowed)
	writeError(
		w,
		r,
		h.logger,
		http.StatusMethodNotAllowed,
		CodeMethodNotAllowed,
		"该接口只支持 "+allowed+" 请求",
	)
	return false
}

// taskNameAndDescription 读取任务对应需求的标题与描述。
//
// 名称与描述由需求承载，任务只保存需求标识；读取失败时返回内部错误，
// 不会用任务标识冒充名称。
func (h *taskHandler) taskNameAndDescription(
	ctx context.Context,
	task domain.Task,
) (string, string, error) {
	requirement, err := h.tasks.Requirement(ctx, task.RequirementID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", "", errors.Join(app.ErrTaskNotFound, err)
		}
		return "", "", err
	}
	return requirement.Title, requirement.Description, nil
}

// taskResponses 批量转换任务表示。
func (h *taskHandler) taskResponses(
	ctx context.Context,
	tasks []domain.Task,
) ([]TaskResponse, error) {
	closeouts, err := h.tasks.TaskCloseoutViews(ctx, tasks)
	if err != nil {
		return nil, err
	}
	responses := make([]TaskResponse, 0, len(tasks))
	for _, task := range tasks {
		name, description, err := h.taskNameAndDescription(ctx, task)
		if err != nil {
			return nil, err
		}
		responses = append(
			responses,
			taskResponseWithCloseout(task, name, description, closeouts[task.ID]),
		)
	}
	return responses, nil
}

// taskResponse 把领域任务转换为对外表示。
func taskResponse(task domain.Task, name, description string) TaskResponse {
	return taskResponseWithCloseout(task, name, description, domain.TaskCloseoutUnconfirmed)
}

// taskResponseWithCloseout 把领域任务与派生的收尾结论一起转换为对外表示。
func taskResponseWithCloseout(
	task domain.Task,
	name, description string,
	closeout domain.TaskCloseoutState,
) TaskResponse {
	return TaskResponse{
		ID:                task.ID.String(),
		Name:              name,
		Description:       description,
		Status:            string(task.Status),
		StatusTerminal:    task.Status.IsTerminal(),
		StatusFinished:    task.Status.IsFinished(),
		FinalResult:       string(task.FinalResult),
		Closeout:          closeout.String(),
		CloseoutSummary:   closeout.Summary(),
		ProjectID:         task.Config.ProjectID,
		CancelRequested:   task.CancelRequested,
		CancelRequestedAt: task.CancelRequestedAt,
		Error:             task.FailureSummary,
		CreatedAt:         task.CreatedAt,
		UpdatedAt:         task.UpdatedAt,
		StartedAt:         task.StartedAt,
		FinishedAt:        task.FinishedAt,
	}
}

// toAppRequest 把请求体转换为应用层请求。
//
// 只做字段搬运与格式转换：名称、描述、项目标识的合法性由应用层统一校验。
func (b CreateTaskRequest) toAppRequest() (app.CreateTaskFromAPIRequest, error) {
	commands, err := parseVerificationCommands(b.Commands)
	if err != nil {
		return app.CreateTaskFromAPIRequest{}, err
	}
	return app.CreateTaskFromAPIRequest{
		Name:               b.Name,
		Description:        b.Description,
		ProjectID:          b.ProjectID,
		Priority:           domain.Priority(strings.TrimSpace(b.Priority)),
		HostID:             b.HostID,
		RepositoryURL:      b.RepositoryURL,
		BaseBranch:         b.BaseBranch,
		Environment:        b.Environment,
		EnvironmentVersion: b.EnvironmentVersion,
		Caches:             b.Caches,
		WorkDir:            b.WorkDir,
		Verification: domain.VerificationSpec{
			Commands:           commands,
			AcceptanceCriteria: b.AcceptanceCriteria,
		},
		Retry: domain.RetryConfig{
			MaxAttempts:       b.MaxAttempts,
			RetryTimeout:      b.RetryTimeout,
			RetryVerification: b.RetryVerification,
			RetryNetwork:      b.RetryNetwork,
			WaitSeconds:       b.RetryWaitSeconds,
		},
		MergeAfterPush:             b.MergeAfterPush,
		PlannerRoleID:              b.PlannerRoleID,
		DeveloperRoleID:            b.DeveloperRoleID,
		ValidatorRoleID:            b.ValidatorRoleID,
		MaxPlanSteps:               b.MaxPlanSteps,
		AgentTimeoutSeconds:        b.AgentTimeoutSeconds,
		VerificationTimeoutSeconds: b.VerificationTimeoutSeconds,
		IdempotencyKey:             b.IdempotencyKey,
	}, nil
}

// parseVerificationCommands 解析「名称=可执行文件 参数...」形式的命令声明。
//
// 命令与参数逐项传给容器执行层，不经过 shell 解释。
func parseVerificationCommands(raw []string) ([]domain.VerificationCommand, error) {
	commands := make([]domain.VerificationCommand, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			return nil, errors.Join(app.ErrTaskInvalidInput, errors.New("验证命令不能为空"))
		}
		name, body, ok := strings.Cut(trimmed, "=")
		if !ok {
			return nil, errors.Join(
				app.ErrTaskInvalidInput,
				errors.New("验证命令缺少名称，请使用「名称=可执行文件 参数...」形式: "+trimmed),
			)
		}
		fields := strings.Fields(body)
		if len(fields) == 0 {
			return nil, errors.Join(
				app.ErrTaskInvalidInput,
				errors.New("验证命令缺少可执行文件: "+trimmed),
			)
		}
		commands = append(commands, domain.VerificationCommand{
			Name:     strings.TrimSpace(name),
			Command:  fields[0],
			Args:     fields[1:],
			Required: true,
		})
	}
	return commands, nil
}

// parseTaskPageQuery 解析列表查询参数。
//
// 参数非法时返回参数错误而不是静默回退：调用方需要知道自己写错了。
func parseTaskPageQuery(r *http.Request) (app.TaskPageRequest, error) {
	query := r.URL.Query()
	request := app.TaskPageRequest{
		Current: 1,
		Size:    app.DefaultTaskPageSize,
	}
	if raw := strings.TrimSpace(query.Get("current")); raw != "" {
		current, err := strconv.Atoi(raw)
		if err != nil || current <= 0 {
			return app.TaskPageRequest{}, errors.Join(
				app.ErrTaskInvalidInput,
				errors.New("分页参数 current 必须是大于 0 的整数"),
			)
		}
		request.Current = current
	}
	if raw := strings.TrimSpace(query.Get("size")); raw != "" {
		size, err := strconv.Atoi(raw)
		if err != nil || size <= 0 {
			return app.TaskPageRequest{}, errors.Join(
				app.ErrTaskInvalidInput,
				errors.New("分页参数 size 必须是大于 0 的整数"),
			)
		}
		request.Size = size
	}
	// status 支持重复参数与逗号分隔两种写法。
	for _, value := range query["status"] {
		for _, item := range strings.Split(value, ",") {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			request.Statuses = append(request.Statuses, domain.TaskStatus(trimmed))
		}
	}
	return request, nil
}

// decodeJSONBody 解析请求体 JSON，失败时写出 400 并返回 false。
func decodeJSONBody(
	w http.ResponseWriter,
	r *http.Request,
	logger *slog.Logger,
	target any,
) bool {
	limited := http.MaxBytesReader(w, r.Body, maxTaskRequestBytes)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		message := "请求体不是合法的 JSON"
		if errors.Is(err, io.EOF) {
			message = "请求体不能为空"
		}
		writeError(w, r, logger, http.StatusBadRequest, CodeInvalidRequest, message)
		return false
	}
	// 请求体只能包含一个 JSON 对象，多余内容视为格式错误。
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(
			w,
			r,
			logger,
			http.StatusBadRequest,
			CodeInvalidRequest,
			"请求体只能包含一个 JSON 对象",
		)
		return false
	}
	return true
}

// writeServiceError 把应用层错误映射为统一错误响应。
//
// 映射依据错误类别而不是文本，状态码如实反映结果：参数错误 400、资源不存在
// 404、状态冲突 409、其他为 500。
func (h *taskHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrTaskInvalidInput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrTaskNotFound), errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrTaskStateConflict),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "任务接口处理失败", slog.String("error", message))
		message = "服务内部错误"
	}
	writeError(w, r, h.logger, status, code, message)
}

// newTaskHandler 构造任务接口处理器。
//
// 服务为空时仍然返回处理器：请求会得到 503，而不是 404 或前端页面。
func newTaskHandler(tasks *app.TaskService, logger *slog.Logger) *taskHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &taskHandler{tasks: tasks, logger: logger}
}
