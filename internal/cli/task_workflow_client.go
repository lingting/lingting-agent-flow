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
