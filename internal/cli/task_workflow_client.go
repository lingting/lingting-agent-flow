package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/httpapi"
)

// newlineSeparator 是多行输出使用的换行分隔符。
//
// 单独声明为常量，使同一份输出格式只在一处定义，也避免在字符串字面量里书写转义。
const newlineSeparator = "\n"

// workflowAPIStartRequest 是启动任务级工作流的请求体。
//
// 只包含执行参数：要执行哪些步骤、顺序如何完全由服务端依据数据库中已持久化的
// 计划决定，命令层不能指定步骤列表、Session 路径或角色覆盖。
type workflowAPIStartRequest struct {
	Prompt                   string `json:"prompt"`
	TimeoutSeconds           int    `json:"timeout_seconds"`
	ValidationTimeoutSeconds int    `json:"validation_timeout_seconds"`
}

// workflowAPIRun 是一次工作流执行的对外表示。
type workflowAPIRun struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
	// Status 是工作流执行状态。
	Status string `json:"status"`
	// StatusTerminal 表示工作流执行是否已结束。
	StatusTerminal bool `json:"status_terminal"`
	// StatusActive 表示工作流是否正在推进。
	StatusActive bool `json:"status_active"`
	// Stage 是当前执行阶段。
	Stage            string `json:"stage"`
	CurrentStepID    string `json:"current_step_id"`
	CurrentAttemptID string `json:"current_attempt_id"`
	CompletedSteps   int    `json:"completed_steps"`
	TotalSteps       int    `json:"total_steps"`
	CancelRequested  bool   `json:"cancel_requested"`
	ErrorClass       string `json:"error_class"`
	ErrorSummary     string `json:"error_summary"`
	// CodeSaverAvailable 为 false 时任务不会因步骤全部通过而判定为完整成功。
	CodeSaverAvailable bool      `json:"code_saver_available"`
	PlanDigest         string    `json:"plan_digest"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// workflowAPIStatus 是工作流状态查询的响应。
type workflowAPIStatus struct {
	TaskID     string          `json:"task_id"`
	TaskStatus string          `json:"task_status"`
	HasRun     bool            `json:"has_run"`
	Run        *workflowAPIRun `json:"run"`
}

// workflowAPIPlanRequest 是生成计划请求的请求体。
//
// 只包含执行参数：要生成哪些步骤、使用哪个角色完全由服务端依据任务配置决定。
type workflowAPIPlanRequest struct {
	Prompt         string `json:"prompt"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// workflowAPIPlan 是一次计划生成的结果。
//
// Persisted 为 false 时任务不会处于 planned，命令必须如实报告失败而不是展示步骤。
type workflowAPIPlan struct {
	TaskID       string                `json:"task_id"`
	TaskStatus   string                `json:"task_status"`
	Persisted    bool                  `json:"persisted"`
	Summary      string                `json:"summary"`
	Steps        []workflowAPIPlanStep `json:"steps"`
	SessionID    string                `json:"session_id"`
	FailureStage string                `json:"failure_stage"`
	Failure      string                `json:"failure"`
	StartedAt    time.Time             `json:"started_at"`
	FinishedAt   time.Time             `json:"finished_at"`
}

// workflowAPIPlanStep 是计划中单个步骤的对外表示。
type workflowAPIPlanStep struct {
	StepID         string   `json:"step_id"`
	Sequence       int      `json:"sequence"`
	Title          string   `json:"title"`
	Type           string   `json:"step_type"`
	Status         string   `json:"status"`
	RoleID         string   `json:"role_id"`
	RequiredChecks []string `json:"required_checks"`
}

// workflowAPIStartTaskRequest 是统一任务启动请求的请求体。
//
// 只包含执行参数：要不要建容器、要不要生成计划完全由服务端依据数据库事实决定，
// 命令层不能指定容器、步骤列表、Session 路径或角色覆盖。
type workflowAPIStartTaskRequest struct {
	Prompt                   string `json:"prompt"`
	TimeoutSeconds           int    `json:"timeout_seconds"`
	ValidationTimeoutSeconds int    `json:"validation_timeout_seconds"`
	PlanTimeoutSeconds       int    `json:"plan_timeout_seconds"`
}

// workflowAPIStartTask 是一次统一启动的结果。
//
// 四个布尔与状态字段互相独立：`plan_persisted` 为 false 表示计划没有落库，
// `workflow_started` 为 false 表示编排没有启动；命令不得把它们当作成功。
type workflowAPIStartTask struct {
	TaskID            string `json:"task_id"`
	Stage             string `json:"stage"`
	TaskStatus        string `json:"task_status"`
	ContainerID       string `json:"container_id"`
	ContainerPrepared bool   `json:"container_prepared"`
	// WorkspaceStatus 是任务级工作区初始化状态，空值表示本次没有走到该阶段。
	WorkspaceStatus string `json:"workspace_status"`
	// WorkspaceCommit 是工作区初始化核验到的 commit SHA。
	WorkspaceCommit string                `json:"workspace_commit"`
	PlanPersisted   bool                  `json:"plan_persisted"`
	PlanSteps       []workflowAPIPlanStep `json:"plan_steps"`
	WorkflowStarted bool                  `json:"workflow_started"`
	Run             *workflowAPIRun       `json:"run"`
	FailureStage    string                `json:"failure_stage"`
	Failure         string                `json:"failure"`
	StartedAt       time.Time             `json:"started_at"`
	FinishedAt      time.Time             `json:"finished_at"`
}

// workflowAPIError 是统一错误响应。
type workflowAPIError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// workflowAPIClient 访问本地 AgentFlow HTTP 服务上的任务级工作流接口。
type workflowAPIClient struct {
	baseURL string
	client  *http.Client
	logger  *slog.Logger
}

// newWorkflowAPIClient 构造工作流接口客户端。
//
// 地址不带协议时按本机 HTTP 处理：服务默认只监听回环地址，不做任何加密假设。
func newWorkflowAPIClient(address string, logger *slog.Logger) *workflowAPIClient {
	base := strings.TrimSpace(address)
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	base = strings.TrimSuffix(base, "/")
	if logger == nil {
		logger = slog.Default()
	}
	return &workflowAPIClient{
		baseURL: base,
		client:  &http.Client{},
		logger:  logger,
	}
}

// startTask 发起一次统一任务启动请求。
//
// 该请求是同步的：服务端在容器准备、计划生成与工作流启动三件事都得出真实结论后
// 才返回，因此命令层不需要轮询即可知道启动停在哪一步。
func (c *workflowAPIClient) startTask(
	ctx context.Context,
	taskID string,
	request workflowAPIStartTaskRequest,
) (workflowAPIStartTask, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return workflowAPIStartTask{}, fmt.Errorf("构造请求失败: %w", err)
	}
	url := c.baseURL + httpapi.PathTasks + "/" + strings.TrimSpace(taskID) + "/start"
	var result workflowAPIStartTask
	if err := c.do(ctx, http.MethodPost, url, body, &result); err != nil {
		return workflowAPIStartTask{}, err
	}
	return result, nil
}

// generatePlan 发起一次计划生成请求。
//
// 该请求是同步的：服务端在计划写入数据库或失败收敛完成后才返回，因此命令层不需要
// 轮询，也不能在失败时假定计划已就绪。
func (c *workflowAPIClient) generatePlan(
	ctx context.Context,
	taskID string,
	request workflowAPIPlanRequest,
) (workflowAPIPlan, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return workflowAPIPlan{}, fmt.Errorf("构造请求失败: %w", err)
	}
	url := c.baseURL + httpapi.PathTasks + "/" + strings.TrimSpace(taskID) + "/plan"
	var plan workflowAPIPlan
	if err := c.do(ctx, http.MethodPost, url, body, &plan); err != nil {
		return workflowAPIPlan{}, err
	}
	return plan, nil
}

// start 发起一次任务级工作流执行。
func (c *workflowAPIClient) start(
	ctx context.Context,
	taskID string,
	request workflowAPIStartRequest,
) (workflowAPIRun, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return workflowAPIRun{}, fmt.Errorf("构造请求失败: %w", err)
	}
	url := c.baseURL + httpapi.PathTasks + "/" + strings.TrimSpace(taskID) + "/execute-workflow"
	var run workflowAPIRun
	if err := c.do(ctx, http.MethodPost, url, body, &run); err != nil {
		return workflowAPIRun{}, err
	}
	return run, nil
}

// status 查询任务当前的工作流进度。
func (c *workflowAPIClient) status(ctx context.Context, taskID string) (workflowAPIStatus, error) {
	url := c.baseURL + httpapi.PathTasks + "/" + strings.TrimSpace(taskID) + "/workflow"
	var status workflowAPIStatus
	if err := c.do(ctx, http.MethodGet, url, nil, &status); err != nil {
		return workflowAPIStatus{}, err
	}
	return status, nil
}

// wait 轮询真实进度直到执行收敛。
//
// 轮询只读取服务端已持久化的状态：命令层不推测进度，也不会在请求失败时假定成功。
func (c *workflowAPIClient) wait(
	ctx context.Context,
	taskID string,
	interval time.Duration,
	a *app.App,
) (workflowAPIRun, error) {
	last := workflowAPIRun{}
	for {
		status, err := c.status(ctx, taskID)
		if err != nil {
			if last.RunID != "" {
				return last, err
			}
			return workflowAPIRun{}, err
		}
		if status.Run != nil {
			if progressed(last, *status.Run) {
				_ = printWorkflowProgress(a, *status.Run)
			}
			last = *status.Run
			if !status.Run.StatusActive || status.Run.StatusTerminal {
				return last, nil
			}
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("等待工作流执行结束失败: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

// do 执行一次请求并解析响应。
func (c *workflowAPIClient) do(
	ctx context.Context,
	method, url string,
	body []byte,
	target any,
) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf(
			"访问本地服务 %s 失败（请确认已运行 agentflow server）: %w",
			c.baseURL,
			err,
		)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			c.logger.WarnContext(ctx, "关闭响应失败", slog.String("error", closeErr.Error()))
		}
	}()
	payload, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return fmt.Errorf("读取响应失败: %w", readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return apiError(response.StatusCode, payload)
	}
	if target == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	return nil
}

// apiError 把统一错误响应转换为可读错误。
func apiError(statusCode int, payload []byte) error {
	var parsed workflowAPIError
	if err := json.Unmarshal(payload, &parsed); err == nil && parsed.Error.Message != "" {
		return fmt.Errorf(
			"服务返回错误（%d %s）: %s",
			statusCode,
			parsed.Error.Code,
			parsed.Error.Message,
		)
	}
	return fmt.Errorf("服务返回错误: HTTP %d", statusCode)
}

// workflowRunStopped 判断执行是否已经收敛。
func workflowRunStopped(run workflowAPIRun) bool {
	return !run.StatusActive || run.StatusTerminal
}

// progressed 判断本次轮询是否带来了新的进度，用于避免重复输出相同状态。
func progressed(previous, current workflowAPIRun) bool {
	return previous.RunID != current.RunID ||
		previous.Status != current.Status ||
		previous.Stage != current.Stage ||
		previous.CompletedSteps != current.CompletedSteps
}

// printWorkflowRun 输出工作流执行摘要。
func printWorkflowRun(a *app.App, run workflowAPIRun) error {
	if strings.TrimSpace(run.RunID) == "" {
		return nil
	}
	lines := []string{
		fmt.Sprintf("执行: %s", run.RunID),
		fmt.Sprintf("任务: %s", run.TaskID),
		fmt.Sprintf("执行状态: %s（阶段 %s）", run.Status, emptyAsUnknown(run.Stage)),
		fmt.Sprintf("进度: %d/%d", run.CompletedSteps, run.TotalSteps),
		fmt.Sprintf("当前步骤: %s", emptyAsUnknown(run.CurrentStepID)),
		fmt.Sprintf("当前尝试: %s", emptyAsUnknown(run.CurrentAttemptID)),
		fmt.Sprintf("取消请求: %s", flagMark(run.CancelRequested)),
		fmt.Sprintf("代码保存可用: %s", flagMark(run.CodeSaverAvailable)),
		fmt.Sprintf("计划摘要: %s", emptyAsUnknown(run.PlanDigest)),
	}
	if run.ErrorClass != "" {
		lines = append(lines, fmt.Sprintf("停止原因: %s", run.ErrorClass))
	}
	if summary := strings.TrimSpace(run.ErrorSummary); summary != "" {
		lines = append(lines, fmt.Sprintf("说明: %s", summary))
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// printPlanResult 输出计划生成结果。
//
// 未写入数据库时只报告失败阶段与原因：把失败的计划展示成"已生成"会让用户误以为
// 任务可以直接执行。
func printPlanResult(a *app.App, plan workflowAPIPlan) error {
	if !plan.Persisted {
		lines := []string{
			fmt.Sprintf("任务: %s", plan.TaskID),
			fmt.Sprintf("任务状态: %s", emptyAsUnknown(plan.TaskStatus)),
			"计划未写入数据库",
		}
		if stage := strings.TrimSpace(plan.FailureStage); stage != "" {
			lines = append(lines, fmt.Sprintf("失败阶段: %s", stage))
		}
		if failure := strings.TrimSpace(plan.Failure); failure != "" {
			lines = append(lines, fmt.Sprintf("原因: %s", failure))
		}
		_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
		return err
	}
	lines := []string{
		fmt.Sprintf("任务: %s", plan.TaskID),
		fmt.Sprintf("任务状态: %s", emptyAsUnknown(plan.TaskStatus)),
		fmt.Sprintf("计划摘要: %s", emptyAsUnknown(plan.Summary)),
		fmt.Sprintf("步骤数: %d", len(plan.Steps)),
		fmt.Sprintf("规划会话: %s", emptyAsUnknown(plan.SessionID)),
	}
	for _, step := range plan.Steps {
		lines = append(lines, fmt.Sprintf(
			"  %d. %s（%s，必需检查 %d 项）",
			step.Sequence,
			step.Title,
			emptyAsUnknown(step.Status),
			len(step.RequiredChecks),
		))
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// printStartTaskResult 输出统一启动结果。
//
// 输出必须如实区分四个独立事实：走到哪个阶段、任务真实状态、计划是否落库、
// 编排是否启动。任何一个为 false 时都不得描述成"任务已开始执行"。
func printStartTaskResult(a *app.App, result workflowAPIStartTask) error {
	lines := []string{
		fmt.Sprintf("任务: %s", result.TaskID),
		fmt.Sprintf("启动阶段: %s", emptyAsUnknown(result.Stage)),
		fmt.Sprintf("任务状态: %s", emptyAsUnknown(result.TaskStatus)),
		fmt.Sprintf("容器: %s（本次创建: %s）",
			emptyAsUnknown(result.ContainerID),
			flagMark(result.ContainerPrepared),
		),
		fmt.Sprintf("工作区: %s（commit %s）",
			emptyAsUnknown(result.WorkspaceStatus),
			emptyAsUnknown(result.WorkspaceCommit),
		),
		fmt.Sprintf("计划已落库: %s（步骤数 %d）",
			flagMark(result.PlanPersisted),
			len(result.PlanSteps),
		),
		fmt.Sprintf("工作流已启动: %s", flagMark(result.WorkflowStarted)),
	}
	if stage := strings.TrimSpace(result.FailureStage); stage != "" {
		lines = append(lines, fmt.Sprintf("失败阶段: %s", stage))
	}
	if failure := strings.TrimSpace(result.Failure); failure != "" {
		lines = append(lines, fmt.Sprintf("原因: %s", failure))
	}
	if _, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, newlineSeparator)); err != nil {
		return err
	}
	if result.Run != nil {
		return printWorkflowRun(a, *result.Run)
	}
	return nil
}

// printWorkflowProgress 输出一次进度变化。
func printWorkflowProgress(a *app.App, run workflowAPIRun) error {
	line := fmt.Sprintf(
		"[%s] 阶段 %s，进度 %d/%d",
		run.Status,
		emptyAsUnknown(run.Stage),
		run.CompletedSteps,
		run.TotalSteps,
	)
	_, err := fmt.Fprintln(a.Stdout(), line)
	return err
}
