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

// startSegment 是统一任务启动动作的路径段。
const startSegment = "start"

// StartTaskRequest 是统一任务启动请求的请求体。
//
// 只接受执行参数：任务标识来自路径，要不要建容器、要不要生成计划完全由服务端依据
// 数据库事实决定，调用方不能指定容器、步骤列表、Session 路径、工作目录或角色覆盖。
type StartTaskRequest struct {
	// Prompt 是可选补充说明，会追加在计划与每一步骤的提示词之后。
	Prompt string `json:"prompt"`
	// TimeoutSeconds 是单个步骤开发阶段的超时秒数，0 表示使用任务配置或服务端默认值。
	TimeoutSeconds int `json:"timeout_seconds"`
	// ValidationTimeoutSeconds 是单个步骤验证阶段的超时秒数，0 表示使用服务端默认值。
	ValidationTimeoutSeconds int `json:"validation_timeout_seconds"`
	// PlanTimeoutSeconds 是本次规划调用的超时秒数，0 表示使用任务配置或服务端默认值。
	PlanTimeoutSeconds int `json:"plan_timeout_seconds"`
}

// StartTaskResponse 是一次统一启动的结果。
//
// 结构显式区分四件事：走到了哪个阶段、任务当前真实状态、计划是否真的写入数据库、
// 以及工作流是否真的启动。`workflow_started` 为 false 时不得把任务展示为已开始执行。
type StartTaskResponse struct {
	TaskID string `json:"task_id"`
	// Stage 是本次启动最后进入的阶段：validate / container / plan / workflow。
	Stage string `json:"stage"`
	// TaskStatus 是收尾后读取到的任务真实状态。
	TaskStatus string `json:"task_status"`
	// ContainerID 是本次启动使用的容器登记记录 ID，未准备时为空。
	ContainerID string `json:"container_id,omitempty"`
	// ContainerPrepared 表示本次调用真的创建了容器。
	ContainerPrepared bool `json:"container_prepared"`
	// PlanPersisted 表示计划是否已完整写入数据库。
	PlanPersisted bool `json:"plan_persisted"`
	// PlanSteps 是已持久化的计划步骤，按序号升序排列。
	PlanSteps []PlanStepResponse `json:"plan_steps"`
	// WorkflowStarted 表示本次调用真的启动了工作流推进。
	WorkflowStarted bool `json:"workflow_started"`
	// Run 是启动后的执行记录，未启动时为空对象。
	Run *WorkflowRunResponse `json:"run,omitempty"`
	// FailureStage / Failure 是失败阶段与原因摘要，成功时为空。
	FailureStage string    `json:"failure_stage,omitempty"`
	Failure      string    `json:"failure,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
}

// serveTaskLaunch 处理 POST /api/v1/tasks/{taskID}/start。
//
// 该入口是同步的：只有容器准备、计划生成与工作流启动三件事都得出真实结论后才写出
// 响应。工作流本身在受生命周期管理的后台推进，因此调用方不能把 202 当作"已完成"。
func (rt *router) serveTaskLaunch(w http.ResponseWriter, r *http.Request) {
	handler := newTaskLaunchHandler(rt.taskLaunch, rt.logger)
	if handler == nil {
		writeError(
			w,
			r,
			rt.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"任务启动服务未初始化",
		)
		return
	}
	handler.start(w, r)
}

// taskLaunchHandler 处理统一任务启动接口。
type taskLaunchHandler struct {
	launch *app.TaskLaunchService
	logger *slog.Logger
}

// newTaskLaunchHandler 构造统一启动接口处理器，服务未初始化时返回 nil。
func newTaskLaunchHandler(
	launch *app.TaskLaunchService,
	logger *slog.Logger,
) *taskLaunchHandler {
	if launch == nil {
		return nil
	}
	return &taskLaunchHandler{launch: launch, logger: logger}
}

// start 处理一次统一启动请求。
func (h *taskLaunchHandler) start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, h.logger, http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"该接口只支持 POST 请求")
		return
	}
	taskID, ok := actionTaskID(r.URL.Path, startSegment)
	if !ok {
		writeError(w, r, h.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
		return
	}
	var body StartTaskRequest
	if !decodeOptionalJSONBody(w, r, h.logger, &body) {
		return
	}
	request, err := body.toLaunchRequest(taskID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	result, err := h.launch.Launch(r.Context(), request)
	if err != nil {
		// 失败结果仍携带可用的诊断信息（阶段、真实任务状态、已建容器），但接口必须
		// 以错误响应结束：未启动工作流时返回 200 会让调用方误以为任务已在执行。
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, r, h.logger, http.StatusAccepted, startTaskResponse(result))
}

// toLaunchRequest 把请求体转换为应用层请求。
func (b StartTaskRequest) toLaunchRequest(taskID string) (app.TaskLaunchRequest, error) {
	timeout, validationTimeout, err := workflowTimeouts(
		b.TimeoutSeconds,
		b.ValidationTimeoutSeconds,
	)
	if err != nil {
		return app.TaskLaunchRequest{}, err
	}
	planTimeout, err := optionalTimeout(b.PlanTimeoutSeconds, "规划超时秒数")
	if err != nil {
		return app.TaskLaunchRequest{}, err
	}
	return app.TaskLaunchRequest{
		TaskID:            taskID,
		Prompt:            b.Prompt,
		Timeout:           timeout,
		ValidationTimeout: validationTimeout,
		PlanTimeout:       planTimeout,
	}, nil
}

// optionalTimeout 校验并转换可选的超时秒数。
func optionalTimeout(seconds int, label string) (time.Duration, error) {
	if seconds < 0 || seconds > maxExecuteTimeoutSeconds {
		return 0, errors.Join(
			app.ErrTaskLaunchInvalidInput,
			errors.New(label+"必须在 0 到 21600 之间"),
		)
	}
	return time.Duration(seconds) * time.Second, nil
}

// startTaskResponse 把启动结果转换为对外表示。
func startTaskResponse(result app.TaskLaunchResult) StartTaskResponse {
	steps := make([]PlanStepResponse, 0, len(result.PlanSteps))
	for _, step := range result.PlanSteps {
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
	response := StartTaskResponse{
		TaskID:            result.TaskID.String(),
		Stage:             result.Stage.String(),
		TaskStatus:        string(result.TaskStatus),
		ContainerID:       result.ContainerID,
		ContainerPrepared: result.ContainerPrepared,
		PlanPersisted:     result.PlanPersisted,
		PlanSteps:         steps,
		WorkflowStarted:   result.WorkflowStarted,
		FailureStage:      result.FailureStage,
		Failure:           result.Failure,
		StartedAt:         result.StartedAt,
		FinishedAt:        result.FinishedAt,
	}
	if result.WorkflowStarted {
		run := workflowRunResponse(result.Run)
		response.Run = &run
	}
	return response
}

// writeError 把统一启动服务的错误映射为统一错误响应。
func (h *taskLaunchHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := CodeInternal
	switch {
	case errors.Is(err, app.ErrTaskLaunchInvalidInput),
		errors.Is(err, app.ErrWorkflowInvalidInput),
		errors.Is(err, app.ErrPlanInvalidOutput),
		errors.Is(err, domain.ErrInvalidValue),
		errors.Is(err, domain.ErrEmptyID):
		status = http.StatusBadRequest
		code = CodeInvalidRequest
	case errors.Is(err, app.ErrTaskLaunchNotFound),
		errors.Is(err, app.ErrWorkflowNotFound),
		errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
		code = CodeNotFound
	case errors.Is(err, app.ErrTaskLaunchConflict),
		errors.Is(err, app.ErrWorkflowConflict),
		errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
		code = CodeConflict
	case errors.Is(err, app.ErrTaskLaunchPrerequisite),
		errors.Is(err, app.ErrWorkflowPrerequisite),
		errors.Is(err, app.ErrStepExecutionPrerequisite),
		errors.Is(err, app.ErrValidationPrerequisite),
		errors.Is(err, domain.ErrValidationPrerequisite):
		status = http.StatusPreconditionFailed
		code = CodePreconditionFailed
	case errors.Is(err, app.ErrTaskLaunchFailed):
		// 启动流程已经真实执行但没有成功：这不是参数错误，也不是前置条件缺失。
		status = http.StatusInternalServerError
		code = CodeExecutionFailed
	case errors.Is(err, app.ErrTaskLaunchStopped),
		errors.Is(err, app.ErrWorkflowStopped):
		status = http.StatusServiceUnavailable
		code = CodeInternal
	}
	message := strings.TrimSpace(err.Error())
	if status == http.StatusInternalServerError {
		// 内部错误不把底层细节返回给调用方，完整信息只写日志。
		h.logger.ErrorContext(r.Context(), "任务启动接口处理失败", "error", message)
		if code == CodeInternal {
			message = "服务内部错误"
		}
	}
	writeError(w, r, h.logger, status, code, message)
}

// isTaskLaunchPath 判断路径是否指向统一任务启动接口。
//
// 判定按路径段形状进行：只有 {taskID}/start 属于启动接口，其余形状仍由任务处理器
// 处理，从而保持它们原有的 405 与 404 语义。
func isTaskLaunchPath(path string) bool {
	_, ok := actionTaskID(path, startSegment)
	return ok
}
